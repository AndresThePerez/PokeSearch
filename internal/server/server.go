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
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/search"
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
}

func New(es *elasticsearch.Client, static fs.FS, logW io.Writer, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	// One serialized sink for both line kinds: the hand-marshalled QueryLog
	// writes directly, slog writes the access lines, and neither may interleave
	// with the other mid-line under concurrency.
	var sink io.Writer = io.Discard
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

		s.log.LogAttrs(r.Context(), slog.LevelInfo, "access",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Int64("dur_ms", s.now().Sub(start).Milliseconds()),
			slog.String("request_id", id))
	})
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

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := s.es.Count(
		s.es.Count.WithContext(ctx),
		s.es.Count.WithIndex(esindex.IndexName),
	)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docs": 0})
		return
	}
	if res.IsError() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docs": body.Count})
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

	TookMs  int                      `json:"took_ms"`
	Results []json.RawMessage        `json:"results"`
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
		Facets: map[string][]facetBucket{
			"supertype":  {},
			"types":      {},
			"rarity":     {},
			"set_series": {},
			"sets":       {},
		},
	}
	for _, hit := range esr.Hits.Hits {
		resp.Results = append(resp.Results, hit.Source)
	}
	for _, name := range []string{"supertype", "types", "rarity", "set_series"} {
		agg := esr.Aggregations[name]
		esBuckets := agg.buckets()
		buckets := make([]facetBucket, 0, len(esBuckets))
		for _, b := range esBuckets {
			buckets = append(buckets, facetBucket{Value: b.Key, Count: b.DocCount})
		}
		resp.Facets[name] = buckets
	}
	if setCatalog != nil {
		resp.Facets["sets"] = mergeSetCatalog(setCatalog, esr.Aggregations["sets"])
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

func (s *Server) searchES(r *http.Request, dsl map[string]any) (*esSearchResponse, error) {
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

	var esr esSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&esr); err != nil {
		return nil, err
	}
	return &esr, nil
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

func (s *Server) suggestES(r *http.Request, dsl map[string]any) ([]string, int, error) {
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := s.es.Search(
		s.es.Search.WithContext(ctx),
		s.es.Search.WithIndex(esindex.IndexName),
		s.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, 0, esError(res.Status(), res.Body)
	}

	var esr esSuggestResponse
	if err := json.NewDecoder(res.Body).Decode(&esr); err != nil {
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
