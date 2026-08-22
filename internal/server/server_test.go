package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// roundTripperFunc fakes ES. Responses must carry X-Elastic-Product or the v8
// client rejects them.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func esResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"X-Elastic-Product": []string{"Elasticsearch"},
			"Content-Type":      []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func newTestServer(t *testing.T, rt http.RoundTripper) (*Server, *bytes.Buffer) {
	t.Helper()
	var logBuf bytes.Buffer
	return newTestServerLogging(t, rt, &logBuf), &logBuf
}

// newTestServerLogging is newTestServer with an explicit log sink — concurrent
// tests pass io.Discard because a bytes.Buffer is not safe under -race.
func newTestServerLogging(t *testing.T, rt http.RoundTripper, logW io.Writer) *Server {
	t.Helper()
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://fake-es:9200"},
		Transport: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Pokesearch</title>")},
	}
	fixed := func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }
	return New(es, static, logW, fixed)
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return getWith(t, s, path, nil)
}

// getWith is get with inbound request headers — the observability middleware
// is the only thing in the server that reads them.
func getWith(t *testing.T, s *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// logLines decodes the test log sink. Two line kinds share it: the
// hand-marshalled QueryLog (carries "endpoint") and slog's access line
// (msg="access").
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("undecodable log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// oneLine returns the single access (or single non-access) log line, failing
// when the sink holds a different number of them.
func oneLine(t *testing.T, buf *bytes.Buffer, access bool) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, m := range logLines(t, buf) {
		if (m["msg"] == "access") == access {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 line (access=%v), got %d:\n%s", access, len(found), buf.String())
	}
	return found[0]
}

func accessLine(t *testing.T, buf *bytes.Buffer) map[string]any { return oneLine(t, buf, true) }
func queryLine(t *testing.T, buf *bytes.Buffer) map[string]any  { return oneLine(t, buf, false) }

// facetLen reports how many buckets a successful search response carries for
// the named facet.
func facetLen(t *testing.T, rec *httptest.ResponseRecorder, name string) int {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Facets map[string][]map[string]any `json:"facets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return len(resp.Facets[name])
}

// Two real docs (base1-1 Alakazam, ex11-12 Mewtwo delta) inside a real-shaped
// hot-search response. The static set catalog is fetched separately below.
const searchESBody = `{
  "took": 4,
  "hits": {
    "total": {"value": 61, "relation": "eq"},
    "hits": [
      {"_id": "base1-1", "_source": {"id": "base1-1", "name": "Alakazam", "supertype": "Pokémon", "hp": 80,
        "types": ["Psychic"], "number": "1", "set_id": "base1", "set_name": "Base", "set_series": "Base",
        "set_total": 102, "release_date": "1999-01-09",
        "image_small": "https://images.pokemontcg.io/base1/1.png",
        "image_large": "https://images.pokemontcg.io/base1/1_hires.png"}},
      {"_id": "ex11-12", "_source": {"id": "ex11-12", "name": "Mewtwo δ", "supertype": "Pokémon", "hp": 70,
        "types": ["Fire", "Metal"], "number": "12", "set_id": "ex11", "set_name": "Delta Species",
        "set_series": "EX", "set_total": 114, "release_date": "2005-10-31",
        "image_small": "https://images.pokemontcg.io/ex11/12.png",
        "image_large": "https://images.pokemontcg.io/ex11/12_hires.png"}}
    ]
  },
  "aggregations": {
    "supertype":  {"doc_count": 61, "items": {"buckets": [{"key": "Pokémon", "doc_count": 61}]}},
    "types":      {"doc_count": 61, "items": {"buckets": [{"key": "Lightning", "doc_count": 61}]}},
    "rarity":     {"doc_count": 61, "items": {"buckets": [{"key": "Common", "doc_count": 30}]}},
    "set_series": {"doc_count": 61, "items": {"buckets": [{"key": "Base", "doc_count": 16}]}},
    "sets":       {"doc_count": 61, "items": {"buckets": [{"key": "base1", "doc_count": 41}]}}
  }
}`

const catalogESBody = `{
  "took": 6,
  "hits": {"hits": []},
  "aggregations": {
    "set_catalog": {"buckets": [
      {"key": "base1", "doc_count": 102, "identity": {"hits": {"hits": [
        {"_source": {"set_name": "Base", "release_date": "1999-01-09"}}
      ]}}},
      {"key": "ex11", "doc_count": 114, "identity": {"hits": {"hits": [
        {"_source": {"set_name": "Delta Species", "release_date": "2005-10-31"}}
      ]}}}
    ]}
  }
}`

func TestSearchHandler(t *testing.T) {
	var esReqBody []byte
	catalogCalls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			catalogCalls++
			return esResponse(200, catalogESBody), nil
		}
		esReqBody = body
		return esResponse(200, searchESBody), nil
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=pikuchu&types=Lightning&set=base1&debug=1")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Total   int                         `json:"total"`
		Page    int                         `json:"page"`
		Pages   int                         `json:"pages"`
		TookMs  int                         `json:"took_ms"`
		Results []map[string]any            `json:"results"`
		Facets  map[string][]map[string]any `json:"facets"`
		DSL     map[string]any              `json:"dsl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 61 || resp.Page != 1 || resp.Pages != 3 {
		t.Errorf("total/page/pages = %d/%d/%d", resp.Total, resp.Page, resp.Pages)
	}
	if resp.TookMs != 4 {
		t.Errorf("took_ms = %d, want 4", resp.TookMs)
	}
	if len(resp.Results) != 2 || resp.Results[0]["name"] != "Alakazam" || resp.Results[1]["name"] != "Mewtwo δ" {
		t.Errorf("results: %v", resp.Results)
	}
	if len(resp.Facets) != 5 || resp.Facets["types"][0]["value"] != "Lightning" || resp.Facets["types"][0]["count"] != float64(61) {
		t.Errorf("facets: %v", resp.Facets)
	}
	// The sets facet is the full catalog with per-request dynamic counts
	// merged on: base1 matched 41 docs, ex11 matched none but keeps its label.
	if sets := resp.Facets["sets"]; len(sets) != 2 ||
		sets[0]["value"] != "base1" || sets[0]["label"] != "Base" || sets[0]["count"] != float64(41) ||
		sets[1]["value"] != "ex11" || sets[1]["label"] != "Delta Species" || sets[1]["count"] != float64(0) {
		t.Errorf("set facets: %v", sets)
	}
	if _, ok := resp.Facets["set_catalog"]; ok {
		t.Error("set_catalog is internal and must not leak into the facets contract")
	}
	if resp.DSL == nil || resp.DSL["track_total_hits"] != true {
		t.Errorf("debug=1 must echo the DSL, got %v", resp.DSL)
	}
	if bytes.Contains(esReqBody, []byte(`"minimum_should_match"`)) || bytes.Contains(esReqBody, []byte(`"set_catalog"`)) {
		t.Errorf("hot ES request must omit implicit bool defaults and static catalog: %s", esReqBody)
	}
	if catalogCalls != 1 {
		t.Errorf("set catalog calls = %d, want 1", catalogCalls)
	}

	lg := queryLine(t, logBuf)
	if lg["endpoint"] != "search" || lg["took_ms"] != float64(4) || lg["total"] != float64(61) ||
		lg["status"] != float64(200) || lg["time"] != "2026-07-06T12:00:00.000Z" {
		t.Errorf("log line: %v", lg)
	}
	// The query line and the response header name the same request, so a user
	// report ("id 3f2a…") lands on the exact DSL that answered it.
	if id := rec.Header().Get("X-Request-Id"); id == "" || lg["request_id"] != id {
		t.Errorf("query log request_id = %v, header = %q", lg["request_id"], id)
	}
	p := lg["params"].(map[string]any)
	if p["q"] != "pikuchu" || p["sort"] != "relevance" || p["set"] != "base1" {
		t.Errorf("log params: %v", p)
	}
	if types := p["types"].([]any); len(types) != 1 || types[0] != "Lightning" {
		t.Errorf("log types: %v", p)
	}
	if _, ok := p["page"]; ok {
		t.Errorf("page=1 must be omitted from log params: %v", p)
	}
}

// pagesFor caps at the 9,600-doc reachable window, so the page count moves
// with page_size while the browse fixture (400 pages at the default) holds.
func TestPagesForRespectsPageSize(t *testing.T) {
	cases := []struct{ total, pageSize, want int }{
		{20324, 24, 400},
		{20324, 100, 96},
		{20324, 1, 9600},
		{61, 24, 3},
		{0, 24, 0},
	}
	for _, tc := range cases {
		if got := pagesFor(tc.total, tc.pageSize); got != tc.want {
			t.Errorf("pagesFor(%d, %d) = %d, want %d", tc.total, tc.pageSize, got, tc.want)
		}
	}
}

// The search response echoes the effective page size so a client never has to
// infer it from len(results).
func TestSearchResponseEchoesPageSize(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)

	var resp struct {
		PageSize int `json:"page_size"`
	}
	if err := json.Unmarshal(get(t, s, "/api/search?q=pikachu").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PageSize != 24 {
		t.Errorf("default page_size = %d, want 24", resp.PageSize)
	}
	if err := json.Unmarshal(get(t, s, "/api/search?q=pikachu&page_size=10").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PageSize != 10 {
		t.Errorf("page_size=10 echoed as %d", resp.PageSize)
	}
}

func TestSearchHandlerEmptyResults(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, `{"took":1,"hits":{"total":{"value":0},"hits":[]},
		  "aggregations":{
		    "supertype":{"buckets":[]},
		    "types":{"buckets":[]},
		    "rarity":{"buckets":[]},
		    "set_series":{"buckets":[]},
		    "sets":{"buckets":[]}}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=zzzzzz")
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Errorf("empty results must be [], got %s", rec.Body.String())
	}
	var resp struct {
		Pages  int                         `json:"pages"`
		Facets map[string][]map[string]any `json:"facets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Pages != 0 {
		t.Errorf("pages = %d, want 0", resp.Pages)
	}
	// Zero results must still expose the whole set catalog at count 0 so the
	// UI never drops options (and never clears a selected set).
	if sets := resp.Facets["sets"]; len(sets) != 2 || sets[0]["label"] != "Base" || sets[0]["count"] != float64(0) ||
		sets[1]["label"] != "Delta Species" || sets[1]["count"] != float64(0) {
		t.Errorf("zero-result set catalog: %v", resp.Facets["sets"])
	}
}

func TestSetCatalogCachedAcrossSearches(t *testing.T) {
	hotCalls := 0
	catalogCalls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			catalogCalls++
			return esResponse(200, catalogESBody), nil
		}
		hotCalls++
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)
	for _, path := range []string{"/api/search?q=pikachu&types=Lightning", "/api/search?q=charizard&types=Fire"} {
		if rec := get(t, s, path); rec.Code != 200 {
			t.Fatalf("%s: status %d body %s", path, rec.Code, rec.Body.String())
		}
	}
	if hotCalls != 2 || catalogCalls != 1 {
		t.Errorf("hot/catalog calls = %d/%d, want 2/1", hotCalls, catalogCalls)
	}
}

// TestSetCatalogEmptyNotCached: the first search runs against an unseeded index
// (the catalog agg returns zero buckets); once the fake starts returning
// buckets — i.e. after a seed — the next search must show the populated
// catalog. Caching the empty result would wedge the facet until a restart.
func TestSetCatalogEmptyNotCached(t *testing.T) {
	var seeded atomic.Bool
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			if seeded.Load() {
				return esResponse(200, catalogESBody), nil
			}
			return esResponse(200, `{"took":1,"hits":{"total":{"value":0},"hits":[]},
			  "aggregations":{"set_catalog":{"buckets":[]}}}`), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)

	if got := facetLen(t, get(t, s, "/api/search?q=pikachu"), "sets"); got != 0 {
		t.Fatalf("pre-seed sets facet = %d, want 0", got)
	}
	seeded.Store(true)
	if got := facetLen(t, get(t, s, "/api/search?q=pikachu"), "sets"); got == 0 {
		t.Fatal("post-seed sets facet still empty — the empty catalog was cached")
	}
}

// TestSetCatalogConcurrentColdStart: N parallel cold-start searches must not
// deadlock and must all complete. Meaningful under -race.
func TestSetCatalogConcurrentColdStart(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s := newTestServerLogging(t, rt, io.Discard)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=pikachu", nil))
			if rec.Code != 200 {
				t.Errorf("status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
}

func TestExactIDLookupSkipsAggregationsAndCatalog(t *testing.T) {
	calls := 0
	var esReqBody []byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		esReqBody, _ = io.ReadAll(r.Body)
		return esResponse(200, `{"took":1,"hits":{"total":{"value":1},"hits":[
		  {"_source":{"id":"base1-1","name":"Alakazam"}}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/search?id=base1-1&debug=1")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if calls != 1 || bytes.Contains(esReqBody, []byte(`"aggs"`)) || bytes.Contains(esReqBody, []byte(`"set_catalog"`)) {
		t.Errorf("ID lookup calls/body = %d/%s", calls, esReqBody)
	}
	var resp searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || len(resp.Results) != 1 || len(resp.Facets) != 5 || len(resp.Facets["sets"]) != 0 {
		t.Errorf("ID response: %+v", resp)
	}
}

// errorBody decodes the M3 error contract:
//
//	{"error":{"code":...,"field":...,"message":...},"request_id":"..."}
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestSearchHandlerESDown(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=x")
	if rec.Code != 503 {
		t.Errorf("status %d, want 503", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Error.Code != "es_unavailable" || body.Error.Message != "elasticsearch unavailable" {
		t.Errorf("503 body = %+v", body.Error)
	}
	if !strings.Contains(logBuf.String(), `"status":503`) {
		t.Errorf("failure must still log: %q", logBuf.String())
	}
}

// Strict params (D1) are a client error, reported with the offending field.
func TestSearchHandlerInvalidParams(t *testing.T) {
	for _, tc := range []struct{ query, field string }{
		{"sort=bogus", "sort"},
		{"supertype=wizard", "supertype"},
		{"hp_min=abc", "hp_min"},
		{"page=two", "page"},
		{"page_size=lots", "page_size"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				t.Error("invalid params must be rejected before ES is called")
				return esResponse(200, searchESBody), nil
			})
			s, logBuf := newTestServer(t, rt)
			rec := get(t, s, "/api/search?"+tc.query)
			if rec.Code != 400 {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			body := decodeError(t, rec)
			if body.Error.Code != "invalid_param" || body.Error.Field != tc.field || body.Error.Message == "" {
				t.Errorf("400 body = %+v, want invalid_param on %q", body.Error, tc.field)
			}
			if !strings.Contains(logBuf.String(), `"status":400`) {
				t.Errorf("rejection must log: %q", logBuf.String())
			}
		})
	}
}

// Lenient inputs (D1) keep returning 200: unknown comma-list members are
// dropped, out-of-range integers clamp, unknown keys are ignored.
func TestSearchHandlerLenientParams(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)
	for _, query := range []string{
		"types=Wizard", "types=Wizard,Fire", "page=999999", "utm_source=x",
		"rarity=NotARarity", "series=Nope", "sort=hp&order=desc", "supertype=POKEMON",
	} {
		if rec := get(t, s, "/api/search?"+query); rec.Code != 200 {
			t.Errorf("%s: status %d, want 200: %s", query, rec.Code, rec.Body.String())
		}
	}
}

// /api/suggest reads only q, so field errors on other params are ignored
// there rather than turned into a 400.
func TestSuggestIgnoresOtherFieldErrors(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(200, `{"took":2,"suggest":{"card":[{"text":"pika","offset":0,"length":4,
		  "options":[{"text":"Pikachu","_id":"base1-58"}]}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?sort=bogus&q=pika")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Pikachu") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// An ES error body is folded into the returned error so the log can tell a
// mapping error from a down cluster, while the client body stays generic.
func TestSearchESErrorBodyLogged(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(400, `{"error":{"type":"search_phase_execution_exception",
		  "reason":"No mapping found for [nope] in order to sort on"}}`), nil
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=x")
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if body := decodeError(t, rec); body.Error.Code != "es_unavailable" ||
		strings.Contains(body.Error.Message, "mapping") {
		t.Errorf("client body must stay generic, got %+v", body.Error)
	}
	if !strings.Contains(logBuf.String(), "search_phase_execution_exception") {
		t.Errorf("ES error body must reach the log: %q", logBuf.String())
	}
}

// TestESCallTimeout: a wedged ES that never answers must not hang a request —
// the per-request ES budget cancels it and the handler returns the 503 contract.
func TestESCallTimeout(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done() // wedged ES: never answers
		return nil, r.Context().Err()
	})
	s, _ := newTestServer(t, rt)
	s.esTimeout = 50 * time.Millisecond

	start := time.Now()
	rec := get(t, s, "/api/search?q=pikachu")
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("request did not time out (took %s)", elapsed)
	}
}

func TestSuggestHandler(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(200, `{"took":2,"suggest":{"card":[{"text":"alak","offset":0,"length":4,
		  "options":[{"text":"Alakazam","_id":"base1-1"},{"text":"Alakazam ex","_id":"ex10-98"}]}]}}`), nil
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alak")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Suggestions) != 2 || resp.Suggestions[0] != "Alakazam" {
		t.Errorf("suggestions: %v", resp.Suggestions)
	}
	if !strings.Contains(logBuf.String(), `"endpoint":"suggest"`) {
		t.Errorf("suggest must log: %q", logBuf.String())
	}
}

func TestSuggestFuzzyRetry(t *testing.T) {
	var bodies [][]byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"alakazm","offset":0,"length":7,"options":[]}]}}`), nil
		}
		return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"alakazm","offset":0,"length":7,
		  "options":[{"text":"Alakazam","_id":"base1-1"}]}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alakazm")
	if len(bodies) != 2 {
		t.Fatalf("want 2 ES calls (plain then fuzzy), got %d", len(bodies))
	}
	if bytes.Contains(bodies[0], []byte("fuzzy")) || !bytes.Contains(bodies[1], []byte(`"fuzziness":"AUTO"`)) {
		t.Errorf("pass 1 must be plain, pass 2 fuzzy:\n%s\n%s", bodies[0], bodies[1])
	}
	if !strings.Contains(rec.Body.String(), "Alakazam") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestSuggestEmptyQSkipsES(t *testing.T) {
	called := false
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return esResponse(200, `{}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=++")
	if called {
		t.Error("empty q must not call ES")
	}
	if rec.Body.String() != `{"suggestions":[]}`+"\n" && rec.Body.String() != `{"suggestions":[]}` {
		t.Errorf("body: %q", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(200, `{"count":20324,"_shards":{"total":1}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/healthz")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"docs":20324`) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHealthzIndexMissing(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(404, `{"error":{"type":"index_not_found_exception"}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/healthz")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"docs":0`) {
		t.Errorf("missing index is the seeded-yet signal: %d %s", rec.Code, rec.Body.String())
	}
}

// TestLivez: liveness must never touch ES — the container healthcheck has to
// answer while the cluster is down, otherwise Docker restarts a healthy app.
func TestLivez(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("/livez must not call ES")
		return nil, errors.New("connection refused")
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/livez")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"alive"`) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHealthzESDown(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	s, _ := newTestServer(t, rt)
	if rec := get(t, s, "/healthz"); rec.Code != 503 {
		t.Errorf("status %d", rec.Code)
	}
}

func TestStaticServing(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("static must not call ES")
		return nil, nil
	}))
	rec := get(t, s, "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Pokesearch") {
		t.Errorf("static /: %d %q", rec.Code, rec.Body.String())
	}
}

// Every response carries X-Request-Id. An inbound id is honoured — X-Request-Id
// first, then Cloudflare's Cf-Ray — so one trace spans edge, app log and
// client. An implausible id is replaced rather than repaired: it would
// otherwise be echoed into a response header and into every log line.
func TestRequestIDHeader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string // "" means "generated, must not equal the inbound value"
	}{
		{name: "generated"},
		{name: "echoes X-Request-Id", headers: map[string]string{"X-Request-Id": "abc123"}, want: "abc123"},
		{name: "falls back to Cf-Ray", headers: map[string]string{"Cf-Ray": "8f0a1b2c3d4e5f60-FRA"}, want: "8f0a1b2c3d4e5f60-FRA"},
		{name: "prefers X-Request-Id over Cf-Ray",
			headers: map[string]string{"X-Request-Id": "abc123", "Cf-Ray": "8f0a-FRA"}, want: "abc123"},
		{name: "rejects a hostile id", headers: map[string]string{"X-Request-Id": "id with spaces"}},
		{name: "rejects an oversized id", headers: map[string]string{"X-Request-Id": strings.Repeat("a", 65)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				t.Error("/livez must not call ES")
				return nil, nil
			}))
			rec := getWith(t, s, "/livez", tc.headers)
			got := rec.Header().Get("X-Request-Id")
			if got == "" {
				t.Fatal("every response must carry X-Request-Id")
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("X-Request-Id = %q, want %q", got, tc.want)
			}
			if tc.want == "" {
				for _, v := range tc.headers {
					if got == v {
						t.Fatalf("inbound id %q must not be echoed", v)
					}
				}
			}
		})
	}
}

// The error envelope's request_id is the same id as the header, which is what
// makes "here is my request id" a usable bug report.
func TestErrorBodyCarriesRequestID(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("invalid params must be rejected before ES is called")
		return nil, nil
	}))
	rec := getWith(t, s, "/api/search?sort=bogus", map[string]string{"X-Request-Id": "trace-42"})
	if rec.Code != 400 {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if body := decodeError(t, rec); body.RequestID != "trace-42" ||
		rec.Header().Get("X-Request-Id") != "trace-42" {
		t.Errorf("request_id = %q, header = %q", body.RequestID, rec.Header().Get("X-Request-Id"))
	}
}

// A static asset produces an access line and no QueryLog line: the query log
// is for requests that actually reach Elasticsearch.
func TestAccessLogLine(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("static must not call ES")
		return nil, nil
	}))
	rec := get(t, s, "/")

	line := accessLine(t, logBuf)
	if line["method"] != "GET" || line["path"] != "/" || line["status"] != float64(200) {
		t.Errorf("access line: %v", line)
	}
	if line["bytes"] == nil || line["bytes"] == float64(0) {
		t.Errorf("access line must count response bytes: %v", line["bytes"])
	}
	if _, ok := line["dur_ms"]; !ok {
		t.Errorf("access line must carry dur_ms: %v", line)
	}
	if id := rec.Header().Get("X-Request-Id"); line["request_id"] != id {
		t.Errorf("access request_id = %v, header = %q", line["request_id"], id)
	}
	// The access line's clock has to match the query line's, or correlating
	// the two by timestamp means reasoning about time zones.
	ts, ok := line["time"].(string)
	if !ok {
		t.Fatalf("access line time: %v", line["time"])
	}
	if _, err := time.Parse(logTimeFormat, ts); err != nil {
		t.Errorf("access time %q is not UTC millisecond format: %v", ts, err)
	}
	for _, m := range logLines(t, logBuf) {
		if _, ok := m["endpoint"]; ok {
			t.Errorf("a static request must not write a QueryLog line: %v", m)
		}
	}
}

// Search writes both lines, and they name the same request — that correlation
// is the whole point of threading the id through.
func TestAccessLogCorrelatesWithQueryLog(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, logBuf := newTestServer(t, rt)
	getWith(t, s, "/api/search?q=pikachu", map[string]string{"X-Request-Id": "trace-7"})

	access, query := accessLine(t, logBuf), queryLine(t, logBuf)
	if access["request_id"] != "trace-7" || query["request_id"] != "trace-7" {
		t.Errorf("access %v / query %v", access["request_id"], query["request_id"])
	}
	if access["path"] != "/api/search" || access["status"] != float64(200) {
		t.Errorf("access line: %v", access)
	}
}
