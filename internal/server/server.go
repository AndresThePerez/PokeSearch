// Package server is Pokesearch's HTTP layer: JSON endpoints plus the embedded
// static frontend. Elasticsearch is only reached from this package.
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/search"
	"github.com/AndresThePerez/pokesearch/internal/version"
)

// esRequestTimeout bounds every Elasticsearch round trip a request makes. It
// is generous next to the 100ms ES SLA — its job is to fail a wedged cluster
// fast enough that the app's own WriteTimeout never has to.
const esRequestTimeout = 5 * time.Second

type Server struct {
	es      *elasticsearch.Client
	mux     *http.ServeMux
	handler http.Handler
	// staticETags maps a request path to the validator for the embedded asset
	// it serves. Written once in New and only read afterwards, which is what
	// lets every request share it without a lock.
	staticETags  map[string]string
	logW         io.Writer
	log          *slog.Logger
	now          func() time.Time
	esTimeout    time.Duration
	setCatalogMu sync.RWMutex
	setCatalog   []facetBucket
	seedMetaMu   sync.RWMutex
	seedMeta     *seedMeta
	statsMu      sync.RWMutex
	stats        *statsPayload
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
	s.mux.HandleFunc("GET /api/explain", s.handleExplain)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/meta", s.handleMeta)

	etags, err := staticETags(static)
	if err != nil {
		// An unreadable static FS is a build defect, not a runtime condition,
		// and serving the assets without validators is still correct — they
		// only lose their conditional requests. Refusing to start would trade
		// a degraded frontend for no frontend at all.
		s.log.Warn("static asset validators unavailable", "err", err.Error())
	}
	s.staticETags = etags
	s.mux.Handle("GET /", s.withStaticCache(http.FileServerFS(static)))

	// Wrapped once: every route — including anything registered later, such as
	// EnableMetrics' /debug/vars — inherits the request ID, the access line and
	// the security headers. Security sits inside observability so the request
	// ID is assigned first and the access line covers the whole request.
	// Compression sits between them: outside the mux, so it can never see the
	// 304 withStaticCache returns from inside it, and inside observability, so
	// the access line's byte count reports what actually went on the wire.
	s.handler = s.withObservability(s.withGzip(s.withSecurityHeaders(s.mux)))
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

// securityCSP is the exact policy every response carries. It names only the
// origins this application actually uses, verified against the frontend:
//   - script-src and connect-src are 'self' and nothing more. The markup has
//     exactly one script tag and it is a same-origin module; the app talks to
//     no third-party endpoint.
//   - img-src allows images.scrydex.com because the corpus stores
//     pokemontcg.io art URLs that no longer serve and the frontend rewrites
//     them to scrydex card-ID routes at render time, and data: because the
//     favicon is an inline SVG data URI.
//   - style-src keeps 'unsafe-inline' because the UI writes style properties
//     at render time in five places (the holo tilt custom properties, the two
//     stat charts, the explain bars and the telemetry waterfall). Narrowing it
//     to 'self' needs a report-only pass to prove no violations first.
//
// The remaining directives are hardening with nothing to trade off: this app
// is never framed, sets no <base>, submits no forms and embeds no plugins.
const securityCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https://images.scrydex.com data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// withSecurityHeaders bounds what a page served from this origin may do. The
// application owns these rather than leaving them to the edge because the
// compose stack in this repository has no proxy in front of it: a deployment
// that is only correct behind Cloudflare is not correct.
//
// Strict-Transport-Security is deliberately absent, and the test asserts its
// absence so a future well-meaning edit cannot add it back quietly. TLS
// terminates upstream and this process is reached over a plaintext hop, so an
// HSTS header emitted here would be both meaningless — the browser negotiating
// TLS never sees this hop — and unverifiable from inside this topology. HSTS
// belongs to whoever terminates TLS.
//
// Every header is set before next.ServeHTTP, never after: a handler that has
// already written its status line cannot take a header any more.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", securityCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		// The pre-CSP companion to frame-ancestors 'none', for anything that
		// still honours only this one.
		h.Set("X-Frame-Options", "DENY")
		// Pins the current browser default rather than changing behaviour:
		// cross-origin card art still sends an origin, no request sends a path.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// indexFile is the document http.FileServerFS also serves under the bare
// directory path, which is why it needs two entries in the validator map and
// its own cache policy below.
const indexFile = "index.html"

// etagHexLen is how much of the SHA-256 sum an ETag carries. Sixteen hex
// characters is 64 bits of it: far more than enough to tell two builds of the
// same asset apart, and short enough to keep the header small.
const etagHexLen = 16

// staticETags hashes every embedded asset once and maps the request path that
// serves it to a quoted strong validator.
//
// This is the whole fix for the defect. embed.FS reports a zero ModTime for
// every entry, so http.ServeContent has no Last-Modified to emit and no
// validator to compare a conditional request against, and net/http never
// synthesizes a Cache-Control of its own: every reload re-sent the entire
// stylesheet. Hashing at construction rather than per request keeps SHA-256 off
// the hot path — the embedded bytes cannot change while the process runs — and
// leaves the map immutable, so it is safe to read concurrently.
func staticETags(fsys fs.FS) (map[string]string, error) {
	etags := make(map[string]string)
	if fsys == nil {
		return etags, nil
	}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tag := `"` + hex.EncodeToString(sum[:])[:etagHexLen] + `"`
		if name == indexFile || strings.HasSuffix(name, "/"+indexFile) {
			// http.FileServerFS redirects a request for index.html to the bare
			// directory path and serves the document from there, so that is
			// the only path the validator belongs on.
			etags["/"+strings.TrimSuffix(name, indexFile)] = tag
			return nil
		}
		etags["/"+name] = tag
		return nil
	})
	return etags, err
}

// staticCacheControl is what a hashed-path asset pipeline would spell
// "immutable" and this one deliberately does not. The asset paths are unhashed
// (/styles.css, /js/main.js), so a long max-age has no way to be busted and
// would strand a deployed fix behind a stale browser copy. Five minutes plus
// must-revalidate collapses a session's repeat requests into one conditional
// round trip and still picks a fix up within the same coffee break.
const staticCacheControl = "public, max-age=300, must-revalidate"

// withStaticCache wraps the static handler alone: it is the only handler whose
// response is a fixed sequence of bytes with a validator to offer. The JSON API
// is computed per request and has nothing to compare against.
//
// index.html is held at no-cache instead. It is the document that names every
// other asset, so a new build has to be picked up on the next navigation rather
// than up to five minutes later. It still carries an ETag, which is what keeps
// that mandatory revalidation a 304 rather than a full re-send.
func (s *Server) withStaticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag, ok := s.staticETags[r.URL.Path]
		if !ok {
			// Not an embedded asset — a 404, or a redirect to one. Neither may
			// be given a cache policy.
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("ETag", etag)
		if isIndexPath(r.URL.Path) {
			h.Set("Cache-Control", "no-cache")
		} else {
			h.Set("Cache-Control", staticCacheControl)
		}
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			// Answered here rather than in ServeContent so a validated request
			// never opens the file at all.
			w.WriteHeader(http.StatusNotModified)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isIndexPath reports whether a validated request path is one an index.html is
// served at, which by the mapping above is exactly a directory path.
func isIndexPath(p string) bool {
	return strings.HasSuffix(p, "/")
}

// etagMatches reports whether an If-None-Match header selects tag. The
// comparison is the weak one the specification requires for this header, so a
// "W/" prefix on the candidate is ignored, and "*" matches any representation
// that exists.
func etagMatches(inm, tag string) bool {
	for _, candidate := range strings.Split(inm, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || (candidate != "" && strings.TrimPrefix(candidate, "W/") == tag) {
			return true
		}
	}
	return false
}

// minGzipBody is the smallest body this wrapper will compress, and it is a
// correctness boundary rather than a performance tuning knob.
//
// The wrapper covers the JSON API the resident load tester consumes, and two of
// those bodies are frozen fixtures that other systems compare byte for byte:
// /healthz answers {"docs":N,"status":"ok"} and the 400 error envelope is only
// a little larger. Both are an order of magnitude below a kilobyte, so both sit
// under this threshold and are served exactly as written. Lowering it would
// pull those fixtures into the compressor; that is the argument any edit to
// this number has to answer first. /healthz is additionally excluded by path in
// withGzip, so no edit here can reach it at all.
//
// Below a kilobyte compression is also just a loss: the gzip header and trailer
// alone are eighteen bytes and a short JSON object barely shrinks.
const minGzipBody = 1 << 10

// gzippableTypes are the four text media types this application actually
// serves: the document, the stylesheet, the ES modules and every API response.
// Card art comes from a third-party origin and never passes through here.
var gzippableTypes = map[string]bool{
	"text/html":              true,
	"text/css":               true,
	"text/javascript":        true,
	"application/javascript": true,
	"application/json":       true,
}

// gzippableType drops any charset parameter and reports whether the media type
// is one worth compressing.
func gzippableType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return gzippableTypes[strings.ToLower(strings.TrimSpace(mediaType))]
}

// acceptsGzip reports whether the client offered gzip. An explicit "gzip;q=0"
// is a refusal and must not be read as an offer.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(token), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(params)), "q="); ok {
			if weight, err := strconv.ParseFloat(q, 64); err == nil && weight == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// withGzip compresses the text responses of clients that asked for one. It sits
// outside the mux, so the 304 withStaticCache produces is already a finished
// response by the time it gets here and is passed through untouched.
//
// /healthz is excluded by path, above and beyond the size threshold: it is a
// frozen contract that other systems assert on verbatim, and belt and braces is
// the right amount of caution for a body that is compared byte for byte.
func (s *Server) withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w, status: http.StatusOK}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter compresses a response body on its way out, deciding as
// late as it has to. The content type is only known once the handler has set
// it, and the size is only known once the body has been written — the handlers
// here stream their JSON and declare no Content-Length — so the status line is
// held back until both questions are answered, buffering at most minGzipBody
// bytes. A body that ends under the threshold is released verbatim; one that
// crosses it is compressed from its first byte.
type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	// settled records that the status line has gone out, either compressed or
	// not. pending holds the body while that is still open.
	settled bool
	pending []byte
	gz      *gzip.Writer
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	if !gzipEligible(code, w.Header()) {
		// Nothing left to negotiate: release the status line now and let every
		// later write go straight through.
		w.settled = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	switch {
	case w.gz != nil:
		return w.gz.Write(b)
	case w.settled:
		return w.ResponseWriter.Write(b)
	}

	w.pending = append(w.pending, b...)
	if len(w.pending) <= minGzipBody {
		return len(b), nil
	}
	// Past the threshold: commit to gzip and replay what was buffered.
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	// Without this a shared cache can hand the compressed body to a client
	// that never offered to decode it.
	h.Set("Vary", "Accept-Encoding")
	// Whatever length the handler declared describes the uncompressed body.
	h.Del("Content-Length")
	w.settled = true
	w.ResponseWriter.WriteHeader(w.status)
	w.gz = gzip.NewWriter(w.ResponseWriter)
	pending := w.pending
	w.pending = nil
	if _, err := w.gz.Write(pending); err != nil {
		return 0, err
	}
	return len(b), nil
}

// close releases whatever the writer is still holding: a short body that never
// reached the threshold, or the gzip trailer. A handler that wrote nothing at
// all is left alone, so net/http still emits its own empty 200.
func (w *gzipResponseWriter) close() {
	switch {
	case w.gz != nil:
		// The status line went out long ago, so a failing flush has nowhere to
		// be reported and nothing to retry.
		_ = w.gz.Close()
	case w.wroteHeader && !w.settled:
		w.settled = true
		w.ResponseWriter.WriteHeader(w.status)
		if len(w.pending) > 0 {
			_, _ = w.ResponseWriter.Write(w.pending)
		}
		w.pending = nil
	}
}

// gzipEligible decides, from the status and the headers the handler has just
// set, whether a response may be compressed at all.
func gzipEligible(status int, h http.Header) bool {
	// A 304 and a 204 carry no body; compressing either would invent one.
	if status == http.StatusNotModified || status == http.StatusNoContent {
		return false
	}
	// Something upstream already encoded this. Re-encoding it would produce a
	// body no client can decode from the single Content-Encoding it is told.
	if h.Get("Content-Encoding") != "" {
		return false
	}
	if !gzippableType(h.Get("Content-Type")) {
		return false
	}
	// A declared length settles the size question before a byte is buffered,
	// which is how the static assets take this path.
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n <= minGzipBody {
		return false
	}
	return true
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
	case "/api/explain":
		return "explain"
	case "/api/stats":
		return "stats"
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
	Matched [][]string `json:"matched,omitempty"`
	// Highlights is aligned the same way: field name → <mark>-tagged fragments,
	// as ES produced them. Also text-query only.
	Highlights []map[string][]string `json:"highlights,omitempty"`
	// DidYouMean is a corrected spelling of q, present only when the search
	// found nothing and the suggester had something to offer (D8).
	DidYouMean string                   `json:"did_you_mean,omitempty"`
	Facets     map[string][]facetBucket `json:"facets"`
	DSL        map[string]any           `json:"dsl,omitempty"`
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
			// Highlight carries the <mark>-tagged fragments the highlight block
			// asked for, keyed by field.
			Highlight map[string][]string `json:"highlight"`
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
		resp.Highlights = make([]map[string][]string, 0, len(esr.Hits.Hits))
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

		// Same alignment rule as matched: a hit ES highlighted nothing on still
		// gets an entry, empty rather than null.
		highlight := hit.Highlight
		if highlight == nil {
			highlight = map[string][]string{}
		}
		resp.Highlights = append(resp.Highlights, highlight)
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
	// D8: only a zero-result text search asks for a correction. It is the
	// cheapest response shape there is, and it is the one moment a correction
	// cannot compete with the autocomplete the user was already offered.
	if resp.Total == 0 && p.Q != "" {
		resp.DidYouMean = s.didYouMean(r, p.Q)
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

// yearBucket is one column of the releases-per-year chart. The year is a label
// rather than a number because that is all the chart does with it, and ES's own
// bucket key is epoch milliseconds — a detail no client should have to know.
type yearBucket struct {
	Year  string `json:"year"`
	Count int    `json:"count"`
}

// hpBucket is one band of the HP distribution, named by its lower bound.
type hpBucket struct {
	From  int `json:"from"`
	Count int `json:"count"`
}

// statsPayload is the corpus description itself — everything about /api/stats
// that is the same for every caller, and therefore the part that is cached.
//
// TookMs is the ES time of the aggregation that PRODUCED this payload, not of
// the request being served: after the first hit, /api/stats never touches ES
// again. The rail's round-trip number is the one that reflects the served
// request, and the two together are exactly the point of the Stats inspector.
type statsPayload struct {
	Total     int           `json:"total"`
	MaxHP     int           `json:"max_hp"`
	PerYear   []yearBucket  `json:"per_year"`
	HP        []hpBucket    `json:"hp"`
	Types     []facetBucket `json:"types"`
	Supertype []facetBucket `json:"supertype"`
	Rarity    []facetBucket `json:"rarity"`
	Series    []facetBucket `json:"series"`
	TookMs    int           `json:"took_ms"`
}

// statsResponse is the payload plus the two per-request fields, which is why
// they are not cached with it.
type statsResponse struct {
	statsPayload
	DSL       map[string]any `json:"dsl,omitempty"`
	RequestID string         `json:"request_id"`
}

// esStatsResponse decodes the aggregation reply. The categorical breakdowns
// reuse esAggregation — they are the same terms aggregations the facet rail
// runs — while the two histograms and the max metric have their own bucket
// shapes.
type esStatsResponse struct {
	Took int `json:"took"`
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
	} `json:"hits"`
	Aggregations struct {
		PerYear struct {
			Buckets []struct {
				KeyAsString string `json:"key_as_string"`
				DocCount    int    `json:"doc_count"`
			} `json:"buckets"`
		} `json:"per_year"`
		HP struct {
			Buckets []struct {
				Key      float64 `json:"key"`
				DocCount int     `json:"doc_count"`
			} `json:"buckets"`
		} `json:"hp"`
		MaxHP struct {
			Value float64 `json:"value"`
		} `json:"max_hp"`
		Types     esAggregation `json:"types"`
		Supertype esAggregation `json:"supertype"`
		Rarity    esAggregation `json:"rarity"`
		Series    esAggregation `json:"series"`
	} `json:"aggregations"`
}

// handleStats serves the corpus analytics behind the Stats view: one cached
// aggregation over the whole archive (B4/D7).
//
// It takes no search parameters — the view describes the archive, not the
// current query — so debug is the only thing read off the request, the same
// exemption /api/suggest and /api/explain have from the strict-parameter 400s.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	p, _ := search.ParseParams(r.URL.Query())
	dsl := search.BuildStatsQuery()
	entry := s.queryLog(r, "stats")
	entry.Params = map[string]any{}
	entry.DSL = dsl

	payload, err := s.loadStats(r)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}

	resp := statsResponse{statsPayload: *payload, RequestID: requestID(r)}
	if p.Debug {
		resp.DSL = dsl
	}

	entry.TookMs = payload.TookMs
	entry.Total = payload.Total
	entry.Status = http.StatusOK
	s.writeLog(entry)
	writeJSON(w, http.StatusOK, resp)
}

// loadStats computes the corpus aggregation once and holds it for the process
// lifetime. Same shape as loadSetCatalog and for the same three reasons: the
// ES call runs OUTSIDE the lock, an empty corpus is served but never cached
// (so the first request after a seed heals it without a restart), and a
// redundant concurrent fetch on a cold start is cheaper than serializing every
// request behind one mutex.
func (s *Server) loadStats(r *http.Request) (*statsPayload, error) {
	s.statsMu.RLock()
	cached := s.stats
	s.statsMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	esr, err := esQuery[esStatsResponse](s, r, search.BuildStatsQuery())
	if err != nil {
		return nil, err
	}
	payload := decodeStats(esr)
	if payload.Total == 0 {
		return payload, nil // unseeded: serve empty, cache nothing
	}

	s.statsMu.Lock()
	if s.stats == nil {
		s.stats = payload
	}
	cached = s.stats
	s.statsMu.Unlock()
	return cached, nil
}

// yearLabelLen is the leading portion of a date_histogram key_as_string that
// names the year ("1999-01-01T00:00:00.000Z").
const yearLabelLen = 4

func decodeStats(esr *esStatsResponse) *statsPayload {
	aggs := esr.Aggregations
	payload := &statsPayload{
		Total:     esr.Hits.Total.Value,
		MaxHP:     int(aggs.MaxHP.Value),
		PerYear:   make([]yearBucket, 0, len(aggs.PerYear.Buckets)),
		HP:        make([]hpBucket, 0, len(aggs.HP.Buckets)),
		Types:     statsBuckets(aggs.Types),
		Supertype: statsBuckets(aggs.Supertype),
		Rarity:    statsBuckets(aggs.Rarity),
		Series:    statsBuckets(aggs.Series),
		TookMs:    esr.Took,
	}
	for _, b := range aggs.PerYear.Buckets {
		if len(b.KeyAsString) < yearLabelLen {
			continue
		}
		payload.PerYear = append(payload.PerYear, yearBucket{
			Year:  b.KeyAsString[:yearLabelLen],
			Count: b.DocCount,
		})
	}
	for _, b := range aggs.HP.Buckets {
		payload.HP = append(payload.HP, hpBucket{From: int(b.Key), Count: b.DocCount})
	}
	return payload
}

// statsBuckets flattens one terms aggregation into the response's bucket shape.
// Empty rather than null: the Stats view renders a fixed set of charts and must
// never have to guard the inner value.
func statsBuckets(agg esAggregation) []facetBucket {
	esBuckets := agg.buckets()
	buckets := make([]facetBucket, 0, len(esBuckets))
	for _, b := range esBuckets {
		buckets = append(buckets, facetBucket{Value: b.Key, Count: b.DocCount})
	}
	return buckets
}

// errESNotFound marks the one non-2xx that can be an answer rather than a
// failure: _explain on a document that is not there. Callers that cannot get
// one (search, suggest) treat it like any other ES error, so the 503 path is
// unchanged; the cause still carries the ES body for the log.
var errESNotFound = errors.New("elasticsearch: not found")

// esCall runs one Elasticsearch round trip and decodes the reply into T. The
// caller supplies the API call, so _search and _explain share the same
// per-request timeout, the same error-body capture and the same decode — this
// is the only place in the package that reads an ES response body.
func esCall[T any](s *Server, r *http.Request, call func(context.Context) (*esapi.Response, error)) (*T, error) {
	ctx, cancel := s.esCtx(r)
	defer cancel()
	res, err := call(ctx)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %w", errESNotFound, esError(res.Status(), res.Body))
	}
	if res.IsError() {
		return nil, esError(res.Status(), res.Body)
	}

	var decoded T
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

// esQuery runs one _search against the cards index and decodes the reply into T.
func esQuery[T any](s *Server, r *http.Request, dsl map[string]any) (*T, error) {
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, err
	}
	return esCall[T](s, r, func(ctx context.Context) (*esapi.Response, error) {
		return s.es.Search(
			s.es.Search.WithContext(ctx),
			s.es.Search.WithIndex(esindex.IndexName),
			s.es.Search.WithBody(bytes.NewReader(body)),
		)
	})
}

func (s *Server) searchES(r *http.Request, dsl map[string]any) (*esSearchResponse, error) {
	return esQuery[esSearchResponse](s, r, dsl)
}

// maxCardIDLen bounds the document id /api/explain will put in an ES URL path.
// Real card ids are short ("cel25c-17_A" is among the longest); anything past
// this is a mistake, and a 400 says so more usefully than found:false.
const maxCardIDLen = 128

// esExplainResponse is the part of _explain this endpoint reads. The full
// Lucene explanation tree is deliberately not decoded: with one named branch
// per call, the root value is that branch's contribution.
type esExplainResponse struct {
	Matched     bool `json:"matched"`
	Explanation struct {
		Value float64 `json:"value"`
	} `json:"explanation"`
}

type explainBranch struct {
	Name    string  `json:"name"`
	Matched bool    `json:"matched"`
	Score   float64 `json:"score"`
}

type explainResponse struct {
	ID    string `json:"id"`
	Q     string `json:"q"`
	Found bool   `json:"found"`
	// Score is the sum of the matched branches: a bool query's should clauses
	// sum, so scoring each branch alone and adding them reconstructs the score
	// the card was actually ranked by.
	Score     float64         `json:"score"`
	Branches  []explainBranch `json:"branches"`
	TookMs    int64           `json:"took_ms"`
	RequestID string          `json:"request_id"`
}

// handleExplain answers "why is this card here?" for one card: it replays each
// relevance branch of the current query against that document through ES
// _explain and reports the per-branch contributions.
//
// D4: this is on demand and single-document on purpose. Lucene explain on 24
// hits per keystroke is pure waste; the per-hit matched_queries in /api/search
// already covers the cheap half of the same question.
//
// Only id and q are read. Filters cannot change a score — they live in
// post_filter — so the other search parameters are ignored rather than
// rejected, the same exemption /api/suggest has.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	entry := s.queryLog(r, "explain")
	entry.Params = map[string]any{"id": id, "q": q}

	if bad, field, message := explainParamError(id, q); bad {
		entry.Status = http.StatusBadRequest
		entry.Error = message
		s.writeLog(entry)
		s.writeError(w, r, http.StatusBadRequest, codeInvalidParam, field, message)
		return
	}

	// One budget for the whole handler, not one per branch: WithTimeout takes
	// the earlier of the two deadlines, so deriving the request's context once
	// here caps all four calls at the single per-request ES budget instead of
	// four times it.
	ctx, cancel := s.esCtx(r)
	defer cancel()
	r = r.WithContext(ctx)

	started := s.now()
	branches := search.Branches(q)
	resp := explainResponse{
		ID:        id,
		Q:         q,
		Found:     true,
		Branches:  make([]explainBranch, 0, len(branches)),
		RequestID: requestID(r),
	}
	for _, b := range branches {
		esr, err := s.explainBranch(r, id, b)
		if errors.Is(err, errESNotFound) {
			// The document is not in the index. Every branch is simply
			// unmatched — that is an answer, and a 404 would make the UI
			// invent an error for it.
			resp.Found = false
			esr, err = &esExplainResponse{}, nil
		}
		if err != nil {
			s.writeES503(w, r, entry, err)
			return
		}
		resp.Branches = append(resp.Branches, explainBranch{
			Name:    b.Name,
			Matched: esr.Matched,
			Score:   esr.Explanation.Value,
		})
		if esr.Matched {
			resp.Score += esr.Explanation.Value
		}
	}
	// Summing floats leaves noise (33.2 + 6.1 + 2.4 is 41.699999999999996);
	// the score is a display value, so it is rounded once, here.
	resp.Score = math.Round(resp.Score*1e3) / 1e3
	resp.TookMs = s.now().Sub(started).Milliseconds()

	entry.TookMs = int(resp.TookMs)
	entry.Total = len(resp.Branches)
	entry.Status = http.StatusOK
	s.writeLog(entry)
	writeJSON(w, http.StatusOK, resp)
}

// explainParamError enforces the two parameters the endpoint cannot work
// without, in a fixed order so a request missing both names id first.
func explainParamError(id, q string) (bool, string, string) {
	switch {
	case id == "":
		return true, "id", "id is required"
	case len(id) > maxCardIDLen:
		return true, "id", fmt.Sprintf("id must be at most %d characters", maxCardIDLen)
	case q == "":
		return true, "q", "q is required"
	}
	return false, "", ""
}

// explainBranch scores one relevance branch against one document. Each call is
// a single-document operation — the cost the design accepted in exchange for
// not explaining 24 hits per keystroke.
func (s *Server) explainBranch(r *http.Request, id string, b search.Branch) (*esExplainResponse, error) {
	body, err := json.Marshal(map[string]any{"query": b.Query})
	if err != nil {
		return nil, err
	}
	return esCall[esExplainResponse](s, r, func(ctx context.Context) (*esapi.Response, error) {
		return s.es.Explain(esindex.IndexName, id,
			s.es.Explain.WithContext(ctx),
			s.es.Explain.WithBody(bytes.NewReader(body)),
		)
	})
}

// esTermSuggestResponse decodes the term suggester. Offset and Length locate
// the token inside the text ES was given, which is what lets the correction be
// spliced back into the original query rather than replacing all of it.
type esTermSuggestResponse struct {
	Suggest struct {
		DYM []struct {
			Offset  int `json:"offset"`
			Length  int `json:"length"`
			Options []struct {
				Text string `json:"text"`
			} `json:"options"`
		} `json:"dym"`
	} `json:"suggest"`
}

// didYouMean returns a corrected spelling of q, or "" when there is nothing to
// offer. It is best effort in the strict sense: a suggester failure is logged
// and then dropped, because the search itself succeeded and turning a valid
// empty result into a 503 over a spelling hint would be a bad trade.
func (s *Server) didYouMean(r *http.Request, q string) string {
	esr, err := esQuery[esTermSuggestResponse](s, r, search.BuildDidYouMean(q))
	if err != nil {
		s.log.Warn("did-you-mean suggester failed",
			"request_id", requestID(r), "q", q, "err", err.Error())
		return ""
	}

	// Offsets are character positions in q, so the query is walked as runes:
	// slicing "ééé pikchu" by byte offset would cut mid-character.
	runes := []rune(q)
	var b strings.Builder
	last, corrected := 0, false
	for _, entry := range esr.Suggest.DYM {
		if len(entry.Options) == 0 {
			continue
		}
		end := entry.Offset + entry.Length
		// Defensive: a token that does not sit cleanly after the previous one
		// is skipped rather than allowed to panic on a slice.
		if entry.Offset < last || end > len(runes) {
			continue
		}
		b.WriteString(string(runes[last:entry.Offset]))
		b.WriteString(entry.Options[0].Text)
		last, corrected = end, true
	}
	if !corrected {
		return ""
	}
	b.WriteString(string(runes[last:]))
	return b.String()
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
