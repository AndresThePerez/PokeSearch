// Package server is Pokesearch's HTTP layer: JSON endpoints plus the embedded
// static frontend. Elasticsearch is only reached from this package.
package server

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/search"
	"github.com/AndresThePerez/pokesearch/internal/version"
)

// esRequestTimeout bounds every Elasticsearch round trip a request makes. It
// is generous next to the 100ms ES SLA — its job is to fail a wedged cluster
// fast enough that the app's own WriteTimeout never has to.
const esRequestTimeout = 5 * time.Second

type Server struct {
	es           *elasticsearch.Client
	mux          *http.ServeMux
	handler      http.Handler
	logW         io.Writer
	log          *slog.Logger
	now          func() time.Time
	esTimeout    time.Duration
	setCatalogMu sync.RWMutex
	setCatalog   []facetBucket
	seedMetaMu   sync.RWMutex
	seedMeta     *seedMeta
}

func New(es *elasticsearch.Client, static fs.FS, logW io.Writer, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	// One serialized sink for both line kinds: the hand-marshalled QueryLog
	// writes directly, slog writes the access lines, and neither may interleave
	// with the other mid-line under concurrency.
	sink := io.Discard
	if logW != nil {
		sink = &syncWriter{w: logW}
	}

	s := &Server{
		es:        es,
		mux:       http.NewServeMux(),
		logW:      sink,
		log:       slog.New(slog.NewJSONHandler(sink, logHandlerOptions())),
		now:       now,
		esTimeout: esRequestTimeout,
	}
	s.mux.HandleFunc("GET /livez", s.handleLivez)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/suggest", s.handleSuggest)
	s.mux.HandleFunc("GET /api/meta", s.handleMeta)
	s.mux.Handle("GET /", http.FileServerFS(static))
	// Wrapped once: every route — including anything registered later, such as
	// EnableMetrics' /debug/vars — inherits the request ID and the access line.
	s.handler = s.withObservability(s.mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// syncWriter serializes writes to the log sink. Without it two concurrent
// requests can interleave a QueryLog line with an access line and produce a
// stdout stream no JSON reader can parse.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

type ctxRequestID struct{}

// requestID returns the id the observability middleware assigned to this
// request, or "" for a request that never went through it.
func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxRequestID{}).(string)
	return id
}

// maxRequestIDLen bounds an inbound id. It is echoed into a response header
// and into every log line the request produces, so a client must not be able
// to make either unbounded.
const maxRequestIDLen = 64

// acceptRequestID takes an inbound trace id only when it looks like one — a
// Cloudflare Cf-Ray, a UUID, a hex token. Anything else (control characters,
// header-splitting attempts, novels) is rejected rather than repaired, and the
// caller generates a fresh id instead.
func acceptRequestID(id string) string {
	if id == "" || len(id) > maxRequestIDLen {
		return ""
	}
	for i := range len(id) {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return ""
		}
	}
	return id
}

func newRequestID() string {
	var b [8]byte
	// crypto/rand.Read never returns an error; it panics if the OS source fails.
	_, _ = cryptorand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// withObservability gives every request an id (honouring the edge's, when the
// edge sent one), echoes it back, threads it into the request context for the
// QueryLog and the error envelope, and writes one access line per request.
func (s *Server) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := acceptRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = acceptRequestID(r.Header.Get("Cf-Ray"))
		}
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := s.now()
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), ctxRequestID{}, id)))

		httpStats.Add(routeLabel(r.URL.Path)+"_"+strconv.Itoa(rec.status), 1)
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "access",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Int64("dur_ms", s.now().Sub(start).Milliseconds()),
			slog.String("request_id", id))
	})
}

// httpStats counts requests by route and status class. expvar registration is
// global and happens once at package init, so every Server shares one map —
// which is what a process-wide counter should be. Publishing it is a separate,
// opt-in decision (EnableMetrics); counting is always on, so switching metrics
// on later still shows the process's whole history.
var httpStats = expvar.NewMap("pokesearch_http")

// routeLabel maps a request path onto a fixed, small set of counter names.
// Unknown paths collapse to "static" on purpose: an arbitrary URL must never
// become an arbitrary expvar key, or a crawler turns the map into a slow leak.
func routeLabel(path string) string {
	switch path {
	case "/api/search":
		return "search"
	case "/api/suggest":
		return "suggest"
	case "/api/meta":
		return "meta"
	case "/healthz":
		return "healthz"
	case "/livez":
		return "livez"
	case "/debug/vars":
		return "metrics"
	default:
		return "static"
	}
}

// EnableMetrics publishes the expvar document at /debug/vars. It is a method
// rather than a constructor argument so New's signature stays stable, and it
// is opt-in because the production Cloudflare tunnel forwards every path it is
// given — the ingress routes only "/", and this endpoint must not change that.
func (s *Server) EnableMetrics() {
	s.mux.Handle("GET /debug/vars", expvar.Handler())
}

// statusRecorder captures what the handler actually sent, so the access line
// can report a status and a size the handler never told anyone about.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// esCtx derives the per-request Elasticsearch budget from the inbound request
// context, so a client disconnect and a slow cluster both cancel the call.
func (s *Server) esCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.esTimeout)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error codes in the M3 contract. invalid_param is the client's fault (400);
// es_unavailable is ours (503).
const (
	codeInvalidParam  = "invalid_param"
	codeESUnavailable = "es_unavailable"
)

type errorDetail struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error     errorDetail `json:"error"`
	RequestID string      `json:"request_id"`
}

// writeError emits the single error shape every non-2xx response uses:
//
//	{"error":{"code":...,"field":...,"message":...},"request_id":"..."}
//
// request_id is the id the observability middleware assigned, which is also
// the value of the response's X-Request-Id header.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, field, message string) {
	writeJSON(w, status, errorEnvelope{
		Error:     errorDetail{Code: code, Field: field, Message: message},
		RequestID: requestID(r),
	})
}

// writeES503 is the one place a failed ES call becomes a client response: the
// error (which carries the truncated ES body) goes to the log, the client gets
// the generic contract.
func (s *Server) writeES503(w http.ResponseWriter, r *http.Request, entry QueryLog, cause error) {
	entry.Status = http.StatusServiceUnavailable
	if cause != nil {
		entry.Error = cause.Error()
	}
	s.writeLog(entry)
	s.writeError(w, r, http.StatusServiceUnavailable, codeESUnavailable, "", "elasticsearch unavailable")
}

// esErrorBodyLimit caps how much of an ES error body reaches the log. Enough
// to identify a mapping error, not enough to flood a log on a bad day.
const esErrorBodyLimit = 2 << 10

// esError folds the ES error body into the returned error so the log can tell
// a mapping error from a down cluster. Callers must not surface it to clients.
func esError(status string, body io.Reader) error {
	snippet, err := io.ReadAll(io.LimitReader(body, esErrorBodyLimit))
	if err != nil || len(snippet) == 0 {
		return errors.New(status)
	}
	return fmt.Errorf("es %s: %s", status, bytes.TrimSpace(snippet))
}

// handleLivez is the cheap liveness probe and the container healthcheck
// target: process is up and serving. It deliberately never touches ES —
// /healthz is the ES round-trip, and its response shape is frozen.
func (s *Server) handleLivez(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive"})
}

// handleHealthz is a frozen contract — {"docs":N,"status":"ok"} with a real ES
// round trip. Courier asserts on it; do not change its shape or its semantics.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	docs, err := s.countDocs(r)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docs": docs})
}

// countDocs is the shared _count round trip. A missing index is not an error:
// it is the not-seeded-yet signal, and it counts as zero documents.
func (s *Server) countDocs(r *http.Request) (int, error) {
	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := s.es.Count(
		s.es.Count.WithContext(ctx),
		s.es.Count.WithIndex(esindex.IndexName),
	)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if res.IsError() {
		return 0, esError(res.Status(), res.Body)
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.Count, nil
}

// seedMeta is the corpus provenance the seeder stamps into the index mapping's
// _meta. It answers "which snapshot of pokemon-tcg-data is this index?".
type seedMeta struct {
	Ref      string `json:"ref"`
	SeededAt string `json:"seeded_at"`
}

type metaResponse struct {
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	Built     string    `json:"built"`
	Docs      int       `json:"docs"`
	Seed      *seedMeta `json:"seed"`
	RequestID string    `json:"request_id"`
}

// handleMeta reports build identity plus corpus provenance: the pair that lets
// a bug report, a screenshot or a Courier run name the exact thing it hit.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	entry := s.queryLog(r, "meta")
	entry.Params = map[string]any{}

	docs, err := s.countDocs(r)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	seed, err := s.loadSeedMeta(r)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	writeJSON(w, http.StatusOK, metaResponse{
		Version:   version.Version,
		Commit:    version.Commit,
		Built:     version.Built,
		Docs:      docs,
		Seed:      seed,
		RequestID: requestID(r),
	})
}

// loadSeedMeta reads the index mapping's _meta, caching it for the process
// lifetime — the mapping is immutable between reseeds. Absence is never
// cached: an index seeded before stamping existed reports seed:null until its
// next reseed, and that reseed must be visible without a restart. Same
// fetch-outside-the-lock shape as loadSetCatalog, for the same reasons.
func (s *Server) loadSeedMeta(r *http.Request) (*seedMeta, error) {
	s.seedMetaMu.RLock()
	cached := s.seedMeta
	s.seedMetaMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := s.es.Indices.GetMapping(
		s.es.Indices.GetMapping.WithContext(ctx),
		s.es.Indices.GetMapping.WithIndex(esindex.IndexName),
	)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil // unseeded index: no provenance to report
	}
	if res.IsError() {
		return nil, esError(res.Status(), res.Body)
	}

	var body map[string]struct {
		Mappings struct {
			Meta *struct {
				SeedRef  string `json:"seed_ref"`
				SeededAt string `json:"seeded_at"`
			} `json:"_meta"`
		} `json:"mappings"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	index, ok := body[esindex.IndexName]
	if !ok || index.Mappings.Meta == nil {
		return nil, nil
	}
	found := &seedMeta{Ref: index.Mappings.Meta.SeedRef, SeededAt: index.Mappings.Meta.SeededAt}

	s.seedMetaMu.Lock()
	if s.seedMeta == nil {
		s.seedMeta = found
	}
	cached = s.seedMeta
	s.seedMetaMu.Unlock()
	return cached, nil
}

// branchOrder is the position of each relevance branch in search.Branches.
// The order does not depend on the query, so it is computed once.
var branchOrder = func() map[string]int {
	order := make(map[string]int)
	for i, b := range search.Branches("") {
		order[b.Name] = i
	}
	return order
}()

// branchRank sorts a hit's matched branches into registry order — strongest
// first. ES reports matched_queries in no defined order, and a badge strip
// that reshuffles between two identical searches reads as a bug.
func branchRank(name string) int {
	if i, ok := branchOrder[name]; ok {
		return i
	}
	return len(branchOrder)
}

type facetBucket struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Count       int    `json:"count"`
}

type searchResponse struct {
	Total    int `json:"total"`
	Page     int `json:"page"`
	Pages    int `json:"pages"`
	PageSize int `json:"page_size"`

	TookMs  int               `json:"took_ms"`
	Results []json.RawMessage `json:"results"`
	// Matched is aligned index-for-index with Results: the relevance branches
	// (search.Branches) ES reports each hit as having matched. Present only for
	// a text query — browse has no named clauses, so there is nothing to report.
	Matched [][]string               `json:"matched,omitempty"`
	Facets  map[string][]facetBucket `json:"facets"`
	DSL     map[string]any           `json:"dsl,omitempty"`
}

type esFacetBucket struct {
	Key      string `json:"key"`
	DocCount int    `json:"doc_count"`
	Identity struct {
		Hits struct {
			Hits []struct {
				Source struct {
					SetName     string `json:"set_name"`
					ReleaseDate string `json:"release_date"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	} `json:"identity"`
}

// esAggregation decodes both direct terms aggregations and filtered facet
// scopes, whose terms buckets sit under the "items" sub-aggregation.
type esAggregation struct {
	Buckets []esFacetBucket `json:"buckets"`
	Items   struct {
		Buckets []esFacetBucket `json:"buckets"`
	} `json:"items"`
}

func (a esAggregation) buckets() []esFacetBucket {
	if a.Buckets != nil {
		return a.Buckets
	}
	return a.Items.Buckets
}

type esSearchResponse struct {
	Took int `json:"took"`
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			Source json.RawMessage `json:"_source"`
			// MatchedQueries is what the _name keys on the relevance branches buy:
			// ES names, per hit, which of them matched.
			MatchedQueries []string `json:"matched_queries"`
		} `json:"hits"`
	} `json:"hits"`
	Aggregations map[string]esAggregation `json:"aggregations"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	p, ferrs := search.ParseParams(r.URL.Query())
	dsl := search.BuildQuery(p)
	entry := s.queryLog(r, "search")
	entry.Params = logParams(p)
	entry.DSL = dsl

	// Strict params are rejected before ES is touched: a typo must not cost a
	// cluster round trip, and it must not return a plausible-looking answer.
	if len(ferrs) > 0 {
		entry.Status = http.StatusBadRequest
		entry.Error = ferrs[0].Message
		s.writeLog(entry)
		s.writeError(w, r, http.StatusBadRequest, codeInvalidParam, ferrs[0].Field, ferrs[0].Message)
		return
	}

	esr, err := s.searchES(r, dsl)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}

	var setCatalog []facetBucket
	if _, hasAggs := dsl["aggs"]; hasAggs {
		setCatalog, err = s.loadSetCatalog(r)
		if err != nil {
			entry.TookMs = esr.Took
			entry.Total = esr.Hits.Total.Value
			s.writeES503(w, r, entry, err)
			return
		}
	}

	resp := searchResponse{
		Total:    esr.Hits.Total.Value,
		Page:     p.Page,
		Pages:    pagesFor(esr.Hits.Total.Value, p.PageSize),
		PageSize: p.PageSize,

		TookMs:  esr.Took,
		Results: make([]json.RawMessage, 0, len(esr.Hits.Hits)),
		Facets:  make(map[string][]facetBucket, len(search.Facets)),
	}
	if p.Q != "" {
		resp.Matched = make([][]string, 0, len(esr.Hits.Hits))
	}
	for _, hit := range esr.Hits.Hits {
		resp.Results = append(resp.Results, hit.Source)
		if p.Q == "" {
			continue
		}
		// A hit can match no named branch (a filter brought it in). It still
		// gets an entry, empty rather than null, so the two arrays stay aligned
		// and the client never has to guard the inner value.
		branches := hit.MatchedQueries
		if branches == nil {
			branches = []string{}
		}
		slices.SortFunc(branches, func(a, b string) int { return branchRank(a) - branchRank(b) })
		resp.Matched = append(resp.Matched, branches)
	}
	// Every registered facet is always present in the response, empty or not —
	// the UI renders a fixed set of controls and must never have to guess.
	for _, f := range search.Facets {
		esBuckets := esr.Aggregations[f.Name].buckets()
		buckets := make([]facetBucket, 0, len(esBuckets))
		for _, b := range esBuckets {
			buckets = append(buckets, facetBucket{Value: b.Key, Count: b.DocCount})
		}
		resp.Facets[f.Name] = buckets
	}
	if setCatalog != nil {
		resp.Facets[search.SetsFacet] = mergeSetCatalog(setCatalog, esr.Aggregations[search.SetsFacet])
	}
	if p.Debug {
		resp.DSL = dsl
	}

	entry.TookMs = esr.Took
	entry.Total = esr.Hits.Total.Value
	entry.Status = http.StatusOK
	s.writeLog(entry)
	writeJSON(w, http.StatusOK, resp)
}

// loadSetCatalog lazily fetches the immutable set metadata. The ES call runs
// OUTSIDE the lock (a slow ES must not serialize every cold-start request),
// and an empty catalog — an unseeded index — is served but never cached, so
// the first search after seeding heals it. Concurrent cold starts may fetch
// redundantly; that is bounded, harmless, and keeps this stdlib-only.
// The standalone size:0 request is also eligible for ES's request cache,
// while hot search requests only calculate dynamic counts.
func (s *Server) loadSetCatalog(r *http.Request) ([]facetBucket, error) {
	s.setCatalogMu.RLock()
	cached := s.setCatalog
	s.setCatalogMu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}

	esr, err := s.searchES(r, search.BuildSetCatalogQuery())
	if err != nil {
		return nil, err
	}
	esBuckets := esr.Aggregations["set_catalog"].buckets()
	catalog := make([]facetBucket, 0, len(esBuckets))
	for _, b := range esBuckets {
		bucket := facetBucket{Value: b.Key}
		if len(b.Identity.Hits.Hits) > 0 {
			bucket.Label = b.Identity.Hits.Hits[0].Source.SetName
			bucket.ReleaseDate = b.Identity.Hits.Hits[0].Source.ReleaseDate
		}
		catalog = append(catalog, bucket)
	}
	if len(catalog) == 0 {
		return catalog, nil // unseeded: serve empty, cache nothing
	}

	s.setCatalogMu.Lock()
	if len(s.setCatalog) == 0 {
		s.setCatalog = catalog
	}
	cached = s.setCatalog
	s.setCatalogMu.Unlock()
	return cached, nil
}

// mergeSetCatalog joins cached labels/releases with per-request dynamic
// counts: every catalog set stays visible, and non-matching sets read 0.
func mergeSetCatalog(catalog []facetBucket, dynamic esAggregation) []facetBucket {
	dynamicBuckets := dynamic.buckets()
	counts := make(map[string]int, len(dynamicBuckets))
	for _, b := range dynamicBuckets {
		counts[b.Key] = b.DocCount
	}
	buckets := make([]facetBucket, 0, len(catalog))
	for _, cached := range catalog {
		bucket := cached
		bucket.Count = counts[bucket.Value]
		buckets = append(buckets, bucket)
	}
	return buckets
}

// pagesFor reports how many pages a client can actually reach. It caps at the
// 9,600-doc window rather than at total, so `pages` deliberately diverges from
// total/pageSize on large result sets — that divergence is documented in the
// README, and at the default size it keeps the browse contract at 400 pages.
func pagesFor(total, pageSize int) int {
	capped := min(total, search.MaxDocsWindow)
	if capped == 0 {
		return 0
	}
	return (capped + pageSize - 1) / pageSize
}

// esQuery runs one _search against the cards index and decodes the reply into
// T. Every ES round trip in this package goes through it, so the per-request
// timeout, the error-body capture and the decode all happen in exactly one
// place — the response shape is the only thing that varies.
func esQuery[T any](s *Server, r *http.Request, dsl map[string]any) (*T, error) {
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := s.es.Search(
		s.es.Search.WithContext(ctx),
		s.es.Search.WithIndex(esindex.IndexName),
		s.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, esError(res.Status(), res.Body)
	}

	var decoded T
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func (s *Server) searchES(r *http.Request, dsl map[string]any) (*esSearchResponse, error) {
	return esQuery[esSearchResponse](s, r, dsl)
}

type esSuggestResponse struct {
	Took    int `json:"took"`
	Suggest struct {
		Card []struct {
			Options []struct {
				Text string `json:"text"`
			} `json:"options"`
		} `json:"card"`
	} `json:"suggest"`
}

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	// /api/suggest reads only q, so field errors on any other parameter are
	// irrelevant here and are deliberately ignored rather than rejected.
	p, _ := search.ParseParams(r.URL.Query())
	if p.Q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": []string{}})
		return
	}

	dsl := search.BuildSuggest(p.Q, false)
	entry := s.queryLog(r, "suggest")
	entry.Params = map[string]any{"q": p.Q}
	entry.DSL = dsl

	names, took, err := s.suggestES(r, dsl)
	if err == nil && len(names) == 0 {
		dsl = search.BuildSuggest(p.Q, true)
		entry.DSL = dsl
		var fuzzyTook int
		names, fuzzyTook, err = s.suggestES(r, dsl)
		took += fuzzyTook
	}
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}

	entry.TookMs = took
	entry.Total = len(names)
	entry.Status = http.StatusOK
	s.writeLog(entry)
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": names})
}

// suggestES is esQuery plus the flattening the completion suggester needs:
// ES nests options one level deeper than the endpoint's contract wants.
func (s *Server) suggestES(r *http.Request, dsl map[string]any) ([]string, int, error) {
	esr, err := esQuery[esSuggestResponse](s, r, dsl)
	if err != nil {
		return nil, 0, err
	}
	names := []string{}
	for _, group := range esr.Suggest.Card {
		for _, opt := range group.Options {
			names = append(names, opt.Text)
		}
	}
	return names, esr.Took, nil
}
