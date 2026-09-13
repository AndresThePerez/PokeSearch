package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/AndresThePerez/pokesearch/internal/version"
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
		// Deliberately past minGzipBody: the cache and compression tests need a
		// real asset, and the size threshold is one of the things under test.
		// Large enough that a Range request over the threshold is still a
		// genuine partial slice rather than the whole file.
		"styles.css": &fstest.MapFile{Data: []byte(strings.Repeat("body { color: #0b1020; }\n", 200))},
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

// Three hits carrying what ES reports for named relevance clauses: two, one,
// and none (a hit pulled in by a filter rather than by the text query).
const matchedESBody = `{
  "took": 3,
  "hits": {
    "total": {"value": 3, "relation": "eq"},
    "hits": [
      {"_id": "base1-4", "_source": {"id": "base1-4", "name": "Charizard"},
        "matched_queries": ["fuzzy-name", "exact"]},
      {"_id": "base1-9", "_source": {"id": "base1-9", "name": "Magmar"},
        "matched_queries": ["text"]},
      {"_id": "base1-7", "_source": {"id": "base1-7", "name": "Hitmonchan"}}
    ]
  },
  "aggregations": {
    "supertype": {"buckets": []}, "types": {"buckets": []}, "rarity": {"buckets": []},
    "set_series": {"buckets": []}, "sets": {"buckets": []}
  }
}`

// Three hits carrying what ES returns for a highlight block: a body-text
// fragment, a whole highlighted name, and a hit with no highlight at all.
const highlightESBody = `{
  "took": 5,
  "hits": {
    "total": {"value": 3, "relation": "eq"},
    "hits": [
      {"_id": "base1-8", "_source": {"id": "base1-8", "name": "Machop"},
        "highlight": {"attacks.text": ["\u2026discard your hand: this attack does <mark>100x</mark> damage\u2026"]}},
      {"_id": "base1-57", "_source": {"id": "base1-57", "name": "Whirlwind Pidgey"},
        "highlight": {"name": ["<mark>Whirlwind</mark> Pidgey"],
                      "flavor_text": ["a gust of <mark>wind</mark>"]}},
      {"_id": "base1-7", "_source": {"id": "base1-7", "name": "Hitmonchan"}}
    ]
  },
  "aggregations": {
    "supertype": {"buckets": []}, "types": {"buckets": []}, "rarity": {"buckets": []},
    "set_series": {"buckets": []}, "sets": {"buckets": []}
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

// matched is the per-hit list of relevance branches ES reports as having
// matched. It is aligned index-for-index with results, and it only exists for
// a text query — browse has no named clauses to match.
func TestSearchMatchedBranches(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, matchedESBody), nil
	})
	s, _ := newTestServer(t, rt)

	var resp struct {
		Results []map[string]any `json:"results"`
		Matched [][]string       `json:"matched"`
	}
	rec := get(t, s, "/api/search?q=charizard")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Matched) != len(resp.Results) {
		t.Fatalf("matched has %d entries for %d results", len(resp.Matched), len(resp.Results))
	}
	// ES reports matched_queries in no particular order (the fixture's first hit
	// is scrambled); the response sorts them into registry order so the badge
	// strip is stable and the contract is deterministic.
	want := [][]string{{"exact", "fuzzy-name"}, {"text"}, {}}
	for i, branches := range want {
		if strings.Join(resp.Matched[i], ",") != strings.Join(branches, ",") {
			t.Errorf("matched[%d] = %v, want %v", i, resp.Matched[i], branches)
		}
	}
	// A hit ES reported no branch for must still be an array, never null: the
	// frontend iterates it without a guard.
	if !strings.Contains(rec.Body.String(), `[["exact","fuzzy-name"],["text"],[]]`) {
		t.Errorf("matched must serialize as aligned arrays: %s", rec.Body.String())
	}

	if body := get(t, s, "/api/search").Body.String(); strings.Contains(body, `"matched"`) {
		t.Errorf("browse response must omit matched: %s", body)
	}
}

// Highlights are aligned with results the same way matched is, and exist only
// for text queries. The fragments are ES's, already <mark>-tagged.
func TestSearchHighlights(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, highlightESBody), nil
	})
	s, _ := newTestServer(t, rt)

	var resp struct {
		Results    []map[string]any      `json:"results"`
		Highlights []map[string][]string `json:"highlights"`
	}
	rec := get(t, s, "/api/search?q=whirlwind")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Highlights) != len(resp.Results) {
		t.Fatalf("highlights has %d entries for %d results", len(resp.Highlights), len(resp.Results))
	}
	if got := resp.Highlights[0]["attacks.text"]; len(got) != 1 ||
		got[0] != "…discard your hand: this attack does <mark>100x</mark> damage…" {
		t.Errorf("hit 0 attacks.text = %v", got)
	}
	if got := resp.Highlights[1]["name"]; len(got) != 1 || got[0] != "<mark>Whirlwind</mark> Pidgey" {
		t.Errorf("hit 1 name = %v", got)
	}
	// A hit ES returned no highlight for still gets an entry, so the two
	// arrays stay index-for-index aligned.
	if resp.Highlights[2] == nil || len(resp.Highlights[2]) != 0 {
		t.Errorf("hit 2 must be an empty object, got %v", resp.Highlights[2])
	}
	if !strings.Contains(rec.Body.String(), `,{}]`) {
		t.Errorf("an absent highlight must serialize as {}, not null: %s", rec.Body.String())
	}

	if body := get(t, s, "/api/search").Body.String(); strings.Contains(body, `"highlights"`) {
		t.Errorf("browse response must omit highlights: %s", body)
	}
}

// A real-shaped /api/stats aggregation reply: a date_histogram whose buckets
// carry key_as_string, a numeric histogram, a max metric, and the four facet
// breakdowns.
const statsESBody = `{
  "took": 12,
  "hits": {"total": {"value": 20324, "relation": "eq"}, "hits": []},
  "aggregations": {
    "per_year": {"buckets": [
      {"key_as_string": "1999-01-01T00:00:00.000Z", "key": 915148800000, "doc_count": 271},
      {"key_as_string": "2000-01-01T00:00:00.000Z", "key": 946684800000, "doc_count": 0},
      {"key_as_string": "2026-01-01T00:00:00.000Z", "key": 1767225600000, "doc_count": 412}
    ]},
    "hp": {"buckets": [
      {"key": 0.0, "doc_count": 3175},
      {"key": 30.0, "doc_count": 1998},
      {"key": 360.0, "doc_count": 12}
    ]},
    "max_hp": {"value": 380.0},
    "types":     {"buckets": [{"key": "Water", "doc_count": 2331}, {"key": "Fire", "doc_count": 1502}]},
    "supertype": {"buckets": [{"key": "Pokémon", "doc_count": 17149}, {"key": "Trainer", "doc_count": 2783}]},
    "rarity":    {"buckets": [{"key": "Common", "doc_count": 5203}]},
    "series":    {"buckets": [{"key": "Base", "doc_count": 1234}, {"key": "Sword & Shield", "doc_count": 3210}]}
  }
}`

// emptyStatsESBody is the same shape against an unseeded index: no documents,
// no buckets, and a null max.
const emptyStatsESBody = `{
  "took": 1,
  "hits": {"total": {"value": 0, "relation": "eq"}, "hits": []},
  "aggregations": {
    "per_year": {"buckets": []}, "hp": {"buckets": []}, "max_hp": {"value": null},
    "types": {"buckets": []}, "supertype": {"buckets": []},
    "rarity": {"buckets": []}, "series": {"buckets": []}
  }
}`

type statsResp struct {
	Total   int `json:"total"`
	MaxHP   int `json:"max_hp"`
	PerYear []struct {
		Year  string `json:"year"`
		Count int    `json:"count"`
	} `json:"per_year"`
	HP []struct {
		From  int `json:"from"`
		Count int `json:"count"`
	} `json:"hp"`
	Types     []map[string]any `json:"types"`
	Supertype []map[string]any `json:"supertype"`
	Rarity    []map[string]any `json:"rarity"`
	Series    []map[string]any `json:"series"`
	TookMs    int              `json:"took_ms"`
	DSL       map[string]any   `json:"dsl"`
	RequestID string           `json:"request_id"`
}

func decodeStatsResp(t *testing.T, rec *httptest.ResponseRecorder) statsResp {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out statsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, rec.Body.String())
	}
	return out
}

func TestStatsHandler(t *testing.T) {
	var esReqBody []byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		esReqBody, _ = io.ReadAll(r.Body)
		return esResponse(200, statsESBody), nil
	})
	s, logBuf := newTestServer(t, rt)

	got := decodeStatsResp(t, get(t, s, "/api/stats"))
	if got.Total != 20324 || got.MaxHP != 380 || got.TookMs != 12 {
		t.Errorf("total=%d max_hp=%d took_ms=%d, want 20324/380/12", got.Total, got.MaxHP, got.TookMs)
	}
	// A date_histogram bucket is a year label, not an epoch millisecond value,
	// and an empty year survives (that is the shape the chart is about).
	if len(got.PerYear) != 3 || got.PerYear[0].Year != "1999" || got.PerYear[0].Count != 271 ||
		got.PerYear[1].Count != 0 || got.PerYear[2].Year != "2026" {
		t.Errorf("per_year = %+v", got.PerYear)
	}
	if len(got.HP) != 3 || got.HP[0].From != 0 || got.HP[1].From != 30 || got.HP[2].From != 360 ||
		got.HP[1].Count != 1998 {
		t.Errorf("hp = %+v", got.HP)
	}
	for name, buckets := range map[string][]map[string]any{
		"types": got.Types, "supertype": got.Supertype, "rarity": got.Rarity, "series": got.Series,
	} {
		if len(buckets) == 0 {
			t.Errorf("%s breakdown is empty", name)
			continue
		}
		if buckets[0]["value"] == "" || buckets[0]["count"] == nil {
			t.Errorf("%s bucket = %v, want {value,count}", name, buckets[0])
		}
	}
	if got.RequestID == "" {
		t.Error("response must carry request_id")
	}
	if got.DSL != nil {
		t.Errorf("dsl must be omitted without debug=1: %v", got.DSL)
	}
	if !bytes.Contains(esReqBody, []byte(`"calendar_interval"`)) {
		t.Errorf("ES body is not the stats aggregation: %s", esReqBody)
	}

	line := queryLine(t, logBuf)
	if line["endpoint"] != "stats" || line["total"] != float64(20324) || line["status"] != float64(200) {
		t.Errorf("query log = %v", line)
	}
	if line["dsl"] == nil {
		t.Error("query log must carry the stats DSL")
	}
}

func TestStatsDebugReturnsDSL(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(200, statsESBody), nil
	}))
	got := decodeStatsResp(t, get(t, s, "/api/stats?debug=1"))
	if got.DSL["aggs"] == nil {
		t.Errorf("debug=1 must return the aggregation DSL, got %v", got.DSL)
	}
}

// The corpus is immutable between reseeds, so the aggregation is computed once
// and served from memory afterwards — the same discipline as the set catalog.
func TestStatsCachedAcrossRequests(t *testing.T) {
	calls := 0
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return esResponse(200, statsESBody), nil
	}))
	first := decodeStatsResp(t, get(t, s, "/api/stats"))
	second := decodeStatsResp(t, get(t, s, "/api/stats"))
	if calls != 1 {
		t.Errorf("ES called %d times across two /api/stats requests, want 1", calls)
	}
	if first.Total != second.Total || len(second.PerYear) != len(first.PerYear) {
		t.Errorf("cached response differs: %+v vs %+v", first, second)
	}
}

// An unseeded index must be served, never cached: the first request after
// seeding has to heal it without a restart (Task 1's discipline).
func TestStatsEmptyIndexNotCached(t *testing.T) {
	calls := 0
	s, _ := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return esResponse(200, emptyStatsESBody), nil
		}
		return esResponse(200, statsESBody), nil
	}))
	if empty := decodeStatsResp(t, get(t, s, "/api/stats")); empty.Total != 0 || empty.MaxHP != 0 {
		t.Errorf("empty index: total=%d max_hp=%d, want 0/0", empty.Total, empty.MaxHP)
	}
	if seeded := decodeStatsResp(t, get(t, s, "/api/stats")); seeded.Total != 20324 {
		t.Errorf("after seeding: total=%d, want 20324 — an empty corpus must never be cached", seeded.Total)
	}
	if calls != 2 {
		t.Errorf("ES called %d times, want 2", calls)
	}
}

func TestStatsESDown(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	rec := get(t, s, "/api/stats")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != codeESUnavailable || body.RequestID == "" {
		t.Errorf("error envelope = %+v", body)
	}
	if line := queryLine(t, logBuf); line["status"] != float64(503) || line["error"] == nil {
		t.Errorf("query log must carry the ES cause: %v", line)
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

// rankedSuggestBody fakes the ranked aggregation reply: one bucket per
// distinct name in the order ES returns them (print count descending), each
// bucket keyed by the lowercase-normalized name and carrying the display
// casing in its top_hits, exactly as name.kw plus the sub-aggregation produce.
func rankedSuggestBody(took int, names ...string) string {
	buckets := make([]string, 0, len(names))
	for i, n := range names {
		buckets = append(buckets, fmt.Sprintf(
			`{"key":%q,"doc_count":%d,"display":{"hits":{"hits":[{"_source":{"name":%q}}]}}}`,
			strings.ToLower(n), len(names)-i, n))
	}
	return fmt.Sprintf(`{"took":%d,"aggregations":{"names":{"buckets":[%s]}}}`,
		took, strings.Join(buckets, ","))
}

// The served path: one ranked ES call, names taken from the top_hits display
// casing rather than from the lowercase bucket key, and in the order ES
// ranked them rather than re-sorted here.
func TestSuggestHandler(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return esResponse(200, rankedSuggestBody(2, "Alakazam", "Alakazam ex")), nil
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
	if len(resp.Suggestions) != 2 || resp.Suggestions[0] != "Alakazam" ||
		resp.Suggestions[1] != "Alakazam ex" {
		t.Errorf("suggestions: %v", resp.Suggestions)
	}
	if calls != 1 {
		t.Errorf("a ranked pass that found something must cost 1 ES call, got %d", calls)
	}
	if !strings.Contains(logBuf.String(), `"endpoint":"suggest"`) {
		t.Errorf("suggest must log: %q", logBuf.String())
	}
}

// A bucket whose top_hits came back empty has no display name to show, and the
// lowercase key is not one — it is skipped rather than emitted as "".
func TestSuggestSkipsBucketWithoutDisplayHit(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(200, `{"took":1,"aggregations":{"names":{"buckets":[
		  {"key":"alakazam","doc_count":9,"display":{"hits":{"hits":[]}}},
		  {"key":"alakazam ex","doc_count":4,"display":{"hits":{"hits":[
		    {"_source":{"name":"Alakazam ex"}}]}}}]}}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alak")
	if body := strings.TrimSpace(rec.Body.String()); body != `{"suggestions":["Alakazam ex"]}` {
		t.Errorf("body: %q", body)
	}
}

// A prefix the aggregation cannot serve still answers: the ranked pass finding
// nothing falls back to the completion suggester, non-fuzzy first.
func TestSuggestRankedFallsBackToCompletion(t *testing.T) {
	var bodies [][]byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			return esResponse(200, `{"took":1,"aggregations":{"names":{"buckets":[]}}}`), nil
		}
		return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"alak","offset":0,"length":4,
		  "options":[{"text":"Alakazam","_id":"base1-1"}]}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alak")
	if len(bodies) != 2 {
		t.Fatalf("want 2 ES calls (ranked then plain completion), got %d", len(bodies))
	}
	if !bytes.Contains(bodies[0], []byte(`"bool_prefix"`)) {
		t.Errorf("pass 1 must be the ranked aggregation: %s", bodies[0])
	}
	if !bytes.Contains(bodies[1], []byte(`"name.suggest"`)) || bytes.Contains(bodies[1], []byte("fuzzy")) {
		t.Errorf("pass 2 must be the plain completion suggester: %s", bodies[1])
	}
	if !strings.Contains(rec.Body.String(), "Alakazam") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

// A misspelling reaches the fuzzy retry the endpoint always had, now behind
// the ranked pass: ranked, then plain completion, then fuzzy completion.
func TestSuggestFuzzyRetry(t *testing.T) {
	var bodies [][]byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		switch len(bodies) {
		case 1:
			return esResponse(200, `{"took":1,"aggregations":{"names":{"buckets":[]}}}`), nil
		case 2:
			return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"alakazm","offset":0,"length":7,"options":[]}]}}`), nil
		}
		return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"alakazm","offset":0,"length":7,
		  "options":[{"text":"Alakazam","_id":"base1-1"}]}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alakazm")
	if len(bodies) != 3 {
		t.Fatalf("want 3 ES calls (ranked, plain, fuzzy), got %d", len(bodies))
	}
	if !bytes.Contains(bodies[0], []byte(`"bool_prefix"`)) {
		t.Errorf("pass 1 must be the ranked aggregation: %s", bodies[0])
	}
	if bytes.Contains(bodies[1], []byte("fuzzy")) || !bytes.Contains(bodies[2], []byte(`"fuzziness":"AUTO"`)) {
		t.Errorf("pass 2 must be plain, pass 3 fuzzy:\n%s\n%s", bodies[1], bodies[2])
	}
	if !strings.Contains(rec.Body.String(), "Alakazam") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

// Nothing anywhere answers with an empty list, not an error and not a 500:
// all three passes run and the endpoint still returns its contract shape.
func TestSuggestNoMatchAnywhere(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return esResponse(200, `{"took":1,"aggregations":{"names":{"buckets":[]}}}`), nil
		}
		return esResponse(200, `{"took":1,"suggest":{"card":[{"text":"zzzzzzzz","offset":0,"length":8,"options":[]}]}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=zzzzzzzz")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"suggestions":[]}` {
		t.Errorf("body: %q", body)
	}
	if calls != 3 {
		t.Errorf("want 3 ES calls, got %d", calls)
	}
}

// An ES failure on the ranked pass is the endpoint's 503 contract, not a
// silent slide into the fallback that would hide a broken cluster.
func TestSuggestRankedESFailureIs503(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return esResponse(500, `{"error":{"type":"search_phase_execution_exception"}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/suggest?q=alak")
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Errorf("a failed ranked pass must not retry, got %d calls", calls)
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

// staticRT fails the test if a static asset request reaches Elasticsearch.
func staticRT(t *testing.T) roundTripperFunc {
	t.Helper()
	return roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("static must not call ES")
		return nil, nil
	})
}

// gunzip decodes a compressed response body. It reports with Errorf rather
// than Fatalf so the concurrency test can call it from its goroutines.
func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Errorf("gzip reader: %v", err)
		return nil
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Errorf("gzip read: %v", err)
		return nil
	}
	if err := zr.Close(); err != nil {
		t.Errorf("gzip close: %v", err)
	}
	return plain
}

// embed.FS reports a zero ModTime, so ServeContent emits no Last-Modified and
// has nothing to validate against: the content-derived ETag is the only
// validator these assets can carry, and it has to be identical across requests
// or a conditional request can never hit.
func TestStaticCacheValidators(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))

	first := get(t, s, "/styles.css")
	if first.Code != 200 {
		t.Fatalf("status %d", first.Code)
	}
	if got := first.Header().Get("Cache-Control"); got != staticCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, staticCacheControl)
	}
	etag := first.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) || len(etag) != etagHexLen+2 {
		t.Fatalf("ETag = %q, want a quoted %d-character hash", etag, etagHexLen)
	}
	if second := get(t, s, "/styles.css").Header().Get("ETag"); second != etag {
		t.Errorf("ETag changed between requests: %q then %q", etag, second)
	}
}

// The point of the validator: a browser that already holds the asset gets a
// bodyless 304 instead of the whole stylesheet. A validator that does not match
// still gets the file.
func TestStaticConditionalRequest(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	etag := get(t, s, "/styles.css").Header().Get("ETag")

	rec := getWith(t, s, "/styles.css", map[string]string{"If-None-Match": etag})
	if rec.Code != 304 {
		t.Fatalf("status %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 must carry no body, got %d bytes", rec.Body.Len())
	}
	if got := rec.Header().Get("Cache-Control"); got != staticCacheControl {
		t.Errorf("304 Cache-Control = %q, want %q", got, staticCacheControl)
	}

	stale := getWith(t, s, "/styles.css", map[string]string{"If-None-Match": `"0123456789abcdef"`})
	if stale.Code != 200 || stale.Body.Len() == 0 {
		t.Errorf("a stale validator must get the whole file: %d, %d bytes", stale.Code, stale.Body.Len())
	}
}

// index.html names every other asset, so it must never be reused without
// asking — but it still carries the validator that keeps the ask cheap.
func TestIndexHTMLStaysNoCache(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	rec := get(t, s, "/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("index.html must still carry an ETag to revalidate against")
	}
}

func TestGzipNegotiation(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	plain := get(t, s, "/styles.css")
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("Vary") != "" {
		t.Errorf("a client that did not ask for gzip must get neither header: %v", plain.Header())
	}

	rec := getWith(t, s, "/styles.css", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	// A stale Content-Length describing the uncompressed body would truncate
	// the response for every client that read it.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want it dropped on a compressed body", got)
	}
	if got := gunzip(t, rec.Body.Bytes()); !bytes.Equal(got, plain.Body.Bytes()) {
		t.Errorf("compressed body decodes to %d bytes, want the %d served plain",
			len(got), plain.Body.Len())
	}
	if rec.Body.Len() >= plain.Body.Len() {
		t.Errorf("compressed %d bytes is not smaller than plain %d", rec.Body.Len(), plain.Body.Len())
	}
}

// A 304 has no body to compress, and inventing one would make it undecodable.
func TestGzipSkipsNotModified(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	etag := get(t, s, "/styles.css").Header().Get("ETag")

	rec := getWith(t, s, "/styles.css", map[string]string{
		"If-None-Match":   etag,
		"Accept-Encoding": "gzip",
	})
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("status %d with %d body bytes, want 304 and none", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none on a 304", got)
	}
}

// A 206 is the one success status whose body describes part of the
// representation rather than all of it, and Content-Range names that part in
// identity bytes. Compressing it makes the header lie: the declared span and
// the delivered length disagree, and a client resuming a download writes the
// wrong bytes at the wrong offset.
func TestGzipSkipsPartialContent(t *testing.T) {
	s, _ := newTestServer(t, staticRT(t))
	full := get(t, s, "/styles.css").Body.Bytes()

	rec := getWith(t, s, "/styles.css", map[string]string{
		"Range":           "bytes=0-4000",
		"Accept-Encoding": "gzip",
	})
	if rec.Code != 206 {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none on partial content", got)
	}

	contentRange := rec.Header().Get("Content-Range")
	var first, last, total int
	if _, err := fmt.Sscanf(contentRange, "bytes %d-%d/%d", &first, &last, &total); err != nil {
		t.Fatalf("Content-Range %q: %v", contentRange, err)
	}
	if total != len(full) {
		t.Errorf("Content-Range %q names a total of %d, want %d", contentRange, total, len(full))
	}
	// The whole point: the span the header declares and the number of bytes
	// actually delivered have to agree.
	if span := last - first + 1; span != rec.Body.Len() {
		t.Errorf("Content-Range %q declares %d bytes, body carries %d", contentRange, span, rec.Body.Len())
	}
	if !bytes.Equal(rec.Body.Bytes(), full[first:last+1]) {
		t.Error("partial body is not the identity slice its Content-Range names")
	}
}

// The frozen fixtures other systems compare byte for byte sit under the size
// threshold, so content negotiation cannot change them. /healthz is excluded by
// path as well, and both facts are asserted here so neither can be lost.
func TestFrozenFixturesNeverCompressed(t *testing.T) {
	rt := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(200, `{"count":20324,"_shards":{"total":1}}`), nil
	})
	s, _ := newTestServer(t, rt)

	for _, path := range []string{"/healthz", "/api/search?sort=bogus"} {
		t.Run(path, func(t *testing.T) {
			// The error envelope echoes the request id, so the id is pinned:
			// the comparison is about the encoding, not about the trace.
			plain := getWith(t, s, path, map[string]string{"X-Request-Id": "frozen-fixture"})
			offered := getWith(t, s, path, map[string]string{
				"X-Request-Id":    "frozen-fixture",
				"Accept-Encoding": "gzip",
			})
			if got := offered.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want none", got)
			}
			if plain.Body.String() != offered.Body.String() {
				t.Errorf("body diverged under content negotiation:\n plain: %q\n gzip:  %q",
					plain.Body.String(), offered.Body.String())
			}
		})
	}
	if body := get(t, s, "/healthz").Body.String(); body != `{"docs":20324,"status":"ok"}`+"\n" {
		t.Errorf("healthz body = %q, the frozen fixture changed", body)
	}
}

// TestStaticCacheConcurrentReads: the validator map is built once in New and
// read by every static request afterwards, and the compressing writer holds
// per-request state that must not leak between them. Meaningful under -race.
func TestStaticCacheConcurrentReads(t *testing.T) {
	s := newTestServerLogging(t, staticRT(t), io.Discard)
	etag := get(t, s, "/styles.css").Header().Get("ETag")
	plain := get(t, s, "/styles.css").Body.Bytes()

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/styles.css", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			// Half the goroutines revalidate and half fetch, so the 304 branch
			// and the compressing branch run against each other.
			if i%2 == 0 {
				req.Header.Set("If-None-Match", etag)
			}
			s.ServeHTTP(rec, req)

			if i%2 == 0 {
				if rec.Code != 304 || rec.Body.Len() != 0 {
					t.Errorf("status %d with %d body bytes, want 304 and none", rec.Code, rec.Body.Len())
				}
				return
			}
			if rec.Code != 200 {
				t.Errorf("status %d, want 200", rec.Code)
			}
			if got := gunzip(t, rec.Body.Bytes()); !bytes.Equal(got, plain) {
				t.Errorf("concurrent compressed body decoded to %d bytes, want %d", len(got), len(plain))
			}
			if got := rec.Header().Get("ETag"); got != etag {
				t.Errorf("ETag = %q, want %q", got, etag)
			}
		}()
	}
	wg.Wait()
}

// wideSearchESBody is a search reply with enough hits to put the response
// comfortably past minGzipBody, so the JSON-under-gzip assertion lands on the
// compressed path rather than on the short-body passthrough beside it.
func wideSearchESBody(hits int) string {
	hit := `{"_id": "base1-1", "_source": {"id": "base1-1", "name": "Alakazam",` +
		` "supertype": "Pokémon", "hp": 80, "types": ["Psychic"], "number": "1",` +
		` "set_id": "base1", "set_name": "Base", "set_series": "Base", "set_total": 102,` +
		` "release_date": "1999-01-09"}}`
	return fmt.Sprintf(`{"took": 4, "hits": {"total": {"value": %d, "relation": "eq"}, "hits": [%s]},
	  "aggregations": {"supertype": {"buckets": []}, "types": {"buckets": []},
	  "rarity": {"buckets": []}, "set_series": {"buckets": []}, "sets": {"buckets": []}}}`,
		hits, strings.TrimSuffix(strings.Repeat(hit+",", hits), ","))
}

// A compressed API response has to decode back to exactly the body the client
// would otherwise have been sent — the load tester reads both.
func TestJSONAPIDecodableUnderGzip(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, wideSearchESBody(24)), nil
	})
	s, _ := newTestServer(t, rt)

	plain := get(t, s, "/api/search?page_size=24")
	rec := getWith(t, s, "/api/search?page_size=24", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (plain body was %d bytes)", got, plain.Body.Len())
	}

	decoded := gunzip(t, rec.Body.Bytes())
	if !bytes.Equal(decoded, plain.Body.Bytes()) {
		t.Errorf("compressed body differs from the plain one:\n gzip:  %s\n plain: %s", decoded, plain.Body.Bytes())
	}
	var resp struct {
		Total   int               `json:"total"`
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(decoded, &resp); err != nil {
		t.Fatalf("decode gzipped response: %v", err)
	}
	if resp.Total != 24 || len(resp.Results) != 24 {
		t.Errorf("total=%d results=%d, want 24 and 24", resp.Total, len(resp.Results))
	}
}

// metaRT fakes the two calls /api/meta makes: the doc count and the index
// mapping that carries the seed provenance. mappingBody of "" means the
// mapping has no _meta — the state the live index is in until its next reseed.
func metaRT(mappingCalls *int, mappingBody string) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "_mapping"):
			*mappingCalls++
			if mappingBody == "" {
				return esResponse(200, `{"cards":{"mappings":{}}}`), nil
			}
			return esResponse(200, mappingBody), nil
		case strings.Contains(r.URL.Path, "_count"):
			return esResponse(200, `{"count":20324}`), nil
		}
		return nil, errors.New("unexpected ES call: " + r.URL.Path)
	}
}

const seedMetaBody = `{"cards":{"mappings":{"_meta":{"seed_ref":"abc","seeded_at":"2026-01-01T00:00:00Z"}}}}`

// /api/meta answers "which build is this, and which corpus is it serving" —
// the question every bug report and every Courier run has to pin down.
func TestMetaEndpoint(t *testing.T) {
	mappingCalls := 0
	s, _ := newTestServer(t, metaRT(&mappingCalls, seedMetaBody))
	rec := getWith(t, s, "/api/meta", map[string]string{"X-Request-Id": "trace-meta"})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Built   string `json:"built"`
		Docs    int    `json:"docs"`
		Seed    *struct {
			Ref      string `json:"ref"`
			SeededAt string `json:"seeded_at"`
		} `json:"seed"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Version != version.Version || resp.Commit != version.Commit || resp.Built != version.Built {
		t.Errorf("build identity = %+v", resp)
	}
	if resp.Docs != 20324 {
		t.Errorf("docs = %d, want 20324", resp.Docs)
	}
	if resp.Seed == nil || resp.Seed.Ref != "abc" || resp.Seed.SeededAt != "2026-01-01T00:00:00Z" {
		t.Errorf("seed = %+v", resp.Seed)
	}
	if resp.RequestID != "trace-meta" {
		t.Errorf("request_id = %q", resp.RequestID)
	}

	// The mapping is immutable between reseeds, so it is fetched once.
	if rec := get(t, s, "/api/meta"); rec.Code != 200 {
		t.Fatalf("second call: %d", rec.Code)
	}
	if mappingCalls != 1 {
		t.Errorf("_mapping calls = %d, want 1 (cached)", mappingCalls)
	}
}

// An index seeded before _meta stamping existed reports seed: null. That is
// the live production index's state until its next reseed — documented
// behaviour, not a failure.
func TestMetaWithoutSeedProvenance(t *testing.T) {
	mappingCalls := 0
	s, _ := newTestServer(t, metaRT(&mappingCalls, ""))

	for range 2 {
		rec := get(t, s, "/api/meta")
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"seed":null`) {
			t.Errorf("missing _meta must report seed:null, got %s", rec.Body.String())
		}
	}
	// Absence is never cached — the first /api/meta after a reseed must see the
	// new stamp, exactly like the set catalog.
	if mappingCalls != 2 {
		t.Errorf("_mapping calls = %d, want 2 (absence must not be cached)", mappingCalls)
	}
}

// /api/meta needs ES for both docs and provenance, so an unreachable cluster
// is the standard 503 contract rather than a half-populated 200.
func TestMetaESDown(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	rec := get(t, s, "/api/meta")
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if body := decodeError(t, rec); body.Error.Code != "es_unavailable" {
		t.Errorf("503 body = %+v", body.Error)
	}
}

// Metrics are opt-in: /debug/vars must not exist unless the operator asked for
// it, because the production tunnel forwards every path it is given.
func TestDebugVarsRequiresOptIn(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("/debug/vars must not call ES")
		return nil, nil
	}))
	rec := get(t, s, "/debug/vars")
	if rec.Code != 404 {
		t.Fatalf("status %d, want 404 — metrics must be off by default: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pokesearch_http") {
		t.Errorf("counters leaked without EnableMetrics: %s", rec.Body.String())
	}
}

// With metrics on, /debug/vars serves the stdlib expvar document and the
// per-route/per-status counters the access middleware maintains.
func TestDebugVarsCounters(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)
	s.EnableMetrics()

	if rec := get(t, s, "/api/search?q=pikachu"); rec.Code != 200 {
		t.Fatalf("search: %d", rec.Code)
	}
	if rec := get(t, s, "/api/search?sort=bogus"); rec.Code != 400 {
		t.Fatalf("bad search: %d", rec.Code)
	}

	rec := get(t, s, "/debug/vars")
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		HTTP map[string]int `json:"pokesearch_http"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("expvar document must be JSON: %v\n%s", err, rec.Body.String())
	}
	if doc.HTTP["search_200"] < 1 {
		t.Errorf("search_200 = %d, want >= 1 (counters: %v)", doc.HTTP["search_200"], doc.HTTP)
	}
	if doc.HTTP["search_400"] < 1 {
		t.Errorf("search_400 = %d, want >= 1 (counters: %v)", doc.HTTP["search_400"], doc.HTTP)
	}
}

// routeLabel keeps the counter map bounded: an arbitrary URL path must never
// become an arbitrary expvar key, or a crawler turns the metrics map into a
// memory leak.
func TestRouteLabel(t *testing.T) {
	for path, want := range map[string]string{
		"/api/search":       "search",
		"/api/suggest":      "suggest",
		"/api/explain":      "explain",
		"/api/stats":        "stats",
		"/api/meta":         "meta",
		"/healthz":          "healthz",
		"/livez":            "livez",
		"/debug/vars":       "metrics",
		"/":                 "static",
		"/styles.css":       "static",
		"/../../etc/passwd": "static",
	} {
		if got := routeLabel(path); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

// Every response carries the security headers, whatever produced it: a static
// asset, a JSON endpoint, or a 404 that never reached a handler of ours. The
// CSP is compared against securityCSP whole rather than by substring, so
// widening the policy has to be a deliberate edit to the constant.
func TestSecurityHeaders(t *testing.T) {
	mappingCalls := 0
	s, _ := newTestServer(t, metaRT(&mappingCalls, seedMetaBody))

	want := map[string]string{
		"Content-Security-Policy": securityCSP,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	}

	for _, tc := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/", wantStatus: 200},
		{path: "/api/meta", wantStatus: 200},
		{path: "/no-such-asset", wantStatus: 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, s, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			for name, value := range want {
				if got := rec.Header().Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			// TLS terminates upstream and this hop is plaintext, so an HSTS
			// header from here would be meaningless and unverifiable. Assert
			// the absence so a later edit cannot add it back quietly.
			if _, ok := rec.Header()["Strict-Transport-Security"]; ok {
				t.Errorf("Strict-Transport-Security must not be set by this app, got %q",
					rec.Header().Get("Strict-Transport-Security"))
			}
		})
	}
}

// The policy is only worth as much as the origins it names. script-src and
// connect-src must stay same-origin-only — the Cloudflare Web Analytics
// allowances an earlier draft carried are gone and must not come back — and
// the card-art host has to survive a careless edit to the constant.
func TestSecurityCSPNamesOnlyOwnedOrigins(t *testing.T) {
	for _, want := range []string{
		"script-src 'self';",
		"connect-src 'self';",
		"img-src 'self' https://images.scrydex.com data:;",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(securityCSP, want) {
			t.Errorf("securityCSP is missing %q:\n%s", want, securityCSP)
		}
	}
	for _, forbidden := range []string{"cloudflareinsights.com", "unsafe-eval", "*"} {
		if strings.Contains(securityCSP, forbidden) {
			t.Errorf("securityCSP must not contain %q:\n%s", forbidden, securityCSP)
		}
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

// explainRT scripts _explain per branch: the fake reads which relevance clause
// the body carries and answers with that branch's score, so the assembled
// response can be checked branch by branch.
func explainRT(t *testing.T, calls *[]string, scores map[string]float64) roundTripperFunc {
	t.Helper()
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var name string
		switch {
		case bytes.Contains(body, []byte(`"name.kw"`)):
			name = "exact"
		case bytes.Contains(body, []byte(`"bool_prefix"`)):
			name = "prefix"
		case bytes.Contains(body, []byte(`"fuzziness"`)) && bytes.Contains(body, []byte(`"match"`)):
			name = "fuzzy-name"
		case bytes.Contains(body, []byte(`"best_fields"`)):
			name = "text"
		default:
			t.Errorf("unrecognised explain body: %s", body)
			return esResponse(400, `{"error":"?"}`), nil
		}
		*calls = append(*calls, r.URL.Path+" "+name)
		score, matched := scores[name]
		if !matched {
			return esResponse(200, `{"matched":false,"explanation":{"value":0.0,"description":"no match"}}`), nil
		}
		return esResponse(200, fmt.Sprintf(
			`{"matched":true,"explanation":{"value":%v,"description":"sum of:"}}`, score)), nil
	})
}

func TestExplainHandler(t *testing.T) {
	var calls []string
	rt := explainRT(t, &calls, map[string]float64{"exact": 33.2, "prefix": 6.1, "fuzzy-name": 2.4})
	s, logBuf := newTestServer(t, rt)

	rec := get(t, s, "/api/explain?id=base1-4&q=charizard")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID       string  `json:"id"`
		Q        string  `json:"q"`
		Found    bool    `json:"found"`
		Score    float64 `json:"score"`
		Branches []struct {
			Name    string  `json:"name"`
			Matched bool    `json:"matched"`
			Score   float64 `json:"score"`
		} `json:"branches"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "base1-4" || resp.Q != "charizard" || !resp.Found {
		t.Errorf("identity: %+v", resp)
	}
	// One _explain per branch, in registry order, all against the same doc.
	if len(calls) != 4 {
		t.Fatalf("explain calls = %v", calls)
	}
	for i, want := range []string{"exact", "prefix", "fuzzy-name", "text"} {
		if calls[i] != "/cards/_explain/base1-4 "+want {
			t.Errorf("call %d = %q, want branch %q on /cards/_explain/base1-4", i, calls[i], want)
		}
		if resp.Branches[i].Name != want {
			t.Errorf("branch %d = %q, want %q", i, resp.Branches[i].Name, want)
		}
	}
	// A bool should sums its matched clauses, so the score is the sum of the
	// matched branches and nothing else.
	if resp.Score != 41.7 {
		t.Errorf("score = %v, want 41.7 (33.2 + 6.1 + 2.4)", resp.Score)
	}
	if !resp.Branches[0].Matched || resp.Branches[0].Score != 33.2 {
		t.Errorf("exact branch: %+v", resp.Branches[0])
	}
	if resp.Branches[3].Matched || resp.Branches[3].Score != 0 {
		t.Errorf("unmatched text branch must score 0: %+v", resp.Branches[3])
	}
	if id := rec.Header().Get("X-Request-Id"); id == "" || resp.RequestID != id {
		t.Errorf("request_id = %q, header = %q", resp.RequestID, id)
	}
	lg := queryLine(t, logBuf)
	if lg["endpoint"] != "explain" || lg["status"] != float64(200) {
		t.Errorf("log line: %v", lg)
	}
}

// An id ES has never heard of is a legitimate answer, not an error: every
// branch simply fails to match.
func TestExplainUnknownDocument(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(404, `{"_index":"cards","_id":"nope-1","found":false}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/explain?id=nope-1&q=charizard")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Found    bool    `json:"found"`
		Score    float64 `json:"score"`
		Branches []struct {
			Matched bool    `json:"matched"`
			Score   float64 `json:"score"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Found || resp.Score != 0 || len(resp.Branches) != 4 {
		t.Errorf("unknown doc: %+v", resp)
	}
	for i, b := range resp.Branches {
		if b.Matched || b.Score != 0 {
			t.Errorf("branch %d must be unmatched at 0: %+v", i, b)
		}
	}
}

func TestExplainRequiresIDAndQuery(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("must not reach ES without both parameters")
		return esResponse(200, `{}`), nil
	})
	s, _ := newTestServer(t, rt)
	cases := []struct{ path, field string }{
		{"/api/explain?q=charizard", "id"},
		{"/api/explain?id=base1-4", "q"},
		{"/api/explain?id=&q=", "id"},
		{"/api/explain?id=base1-4&q=%20%20", "q"},
	}
	for _, c := range cases {
		rec := get(t, s, c.path)
		if rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", c.path, rec.Code)
			continue
		}
		if body := decodeError(t, rec); body.Error.Code != codeInvalidParam || body.Error.Field != c.field {
			t.Errorf("%s: error %+v, want field %q", c.path, body.Error, c.field)
		}
	}
}

func TestExplainESDown(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/explain?id=base1-4&q=charizard")
	if rec.Code != 503 {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if body := decodeError(t, rec); body.Error.Code != codeESUnavailable {
		t.Errorf("error body: %+v", body)
	}
	if lg := queryLine(t, logBuf); lg["endpoint"] != "explain" || lg["status"] != float64(503) {
		t.Errorf("log line: %v", lg)
	}
}

// zeroResultsESBody is a text search that matched nothing — the only shape
// that triggers a did-you-mean lookup.
const zeroResultsESBody = `{
  "took": 2,
  "hits": {"total": {"value": 0, "relation": "eq"}, "hits": []},
  "aggregations": {
    "supertype": {"buckets": []}, "types": {"buckets": []}, "rarity": {"buckets": []},
    "set_series": {"buckets": []}, "sets": {"buckets": []}
  }
}`

// dymRT routes the three request kinds a zero-result search makes: the search
// itself, the set catalog, and the suggester.
func dymRT(t *testing.T, suggestStatus int, suggestBody string, suggestCalls *int) roundTripperFunc {
	t.Helper()
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case bytes.Contains(body, []byte(`"dym"`)):
			*suggestCalls++
			return esResponse(suggestStatus, suggestBody), nil
		case bytes.Contains(body, []byte(`"set_catalog"`)):
			return esResponse(200, catalogESBody), nil
		default:
			return esResponse(200, zeroResultsESBody), nil
		}
	})
}

func TestDidYouMeanOnZeroResults(t *testing.T) {
	calls := 0
	// Two tokens, one correctable: the reassembly must replace only that one
	// and keep the rest of the query verbatim.
	rt := dymRT(t, 200, `{"suggest": {"dym": [
	  {"text": "charzard", "offset": 0, "length": 8,
	   "options": [{"text": "charizard", "score": 0.87, "freq": 107}]},
	  {"text": "zzz", "offset": 9, "length": 3, "options": []}
	]}}`, &calls)
	s, _ := newTestServer(t, rt)

	rec := get(t, s, "/api/search?q=charzard+zzz")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total      int    `json:"total"`
		DidYouMean string `json:"did_you_mean"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 || resp.DidYouMean != "charizard zzz" {
		t.Errorf("total=%d did_you_mean=%q, want 0 / %q", resp.Total, resp.DidYouMean, "charizard zzz")
	}
	if calls != 1 {
		t.Errorf("suggester calls = %d, want 1", calls)
	}
}

// A search that found something must not pay for a suggester call at all.
func TestDidYouMeanSkippedWhenResultsExist(t *testing.T) {
	calls := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"dym"`)) {
			calls++
			return esResponse(200, `{"suggest":{"dym":[]}}`), nil
		}
		if bytes.Contains(body, []byte(`"set_catalog"`)) {
			return esResponse(200, catalogESBody), nil
		}
		return esResponse(200, searchESBody), nil
	})
	s, _ := newTestServer(t, rt)
	if body := get(t, s, "/api/search?q=pikuchu").Body.String(); strings.Contains(body, "did_you_mean") {
		t.Errorf("a search with results must not carry did_you_mean: %s", body)
	}
	if calls != 0 {
		t.Errorf("suggester calls = %d, want 0", calls)
	}
}

// Browse cannot produce a correction: there is no query to correct.
func TestDidYouMeanSkippedForBrowse(t *testing.T) {
	calls := 0
	rt := dymRT(t, 200, `{"suggest":{"dym":[]}}`, &calls)
	s, _ := newTestServer(t, rt)
	if body := get(t, s, "/api/search?rarity=Nonexistent").Body.String(); strings.Contains(body, "did_you_mean") {
		t.Errorf("browse must not carry did_you_mean: %s", body)
	}
	if calls != 0 {
		t.Errorf("suggester calls = %d, want 0", calls)
	}
}

// No token got a suggestion: the field is absent rather than echoing q back.
func TestDidYouMeanAbsentWithoutSuggestions(t *testing.T) {
	calls := 0
	rt := dymRT(t, 200, `{"suggest": {"dym": [
	  {"text": "zzzzqqqq", "offset": 0, "length": 8, "options": []}
	]}}`, &calls)
	s, _ := newTestServer(t, rt)
	if body := get(t, s, "/api/search?q=zzzzqqqq").Body.String(); strings.Contains(body, "did_you_mean") {
		t.Errorf("no suggestion must mean no field: %s", body)
	}
	if calls != 1 {
		t.Errorf("suggester calls = %d, want 1", calls)
	}
}

// Best effort: a suggester failure must not turn a perfectly good (if empty)
// search result into an error.
func TestDidYouMeanFailureKeepsSearchSuccessful(t *testing.T) {
	calls := 0
	rt := dymRT(t, 500, `{"error":{"type":"search_phase_execution_exception"}}`, &calls)
	s, logBuf := newTestServer(t, rt)

	rec := get(t, s, "/api/search?q=charzard")
	if rec.Code != 200 {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "did_you_mean") {
		t.Errorf("failed suggester must leave the field off: %s", rec.Body.String())
	}
	if calls != 1 {
		t.Errorf("suggester calls = %d, want 1", calls)
	}
	// The failure still has to be visible to an operator.
	if !strings.Contains(logBuf.String(), "did-you-mean") {
		t.Errorf("the suggester failure must be logged: %s", logBuf.String())
	}
}

// Offsets are character offsets in the query ES was given, so a multi-byte
// query must not be sliced by byte position.
func TestDidYouMeanMultiByteOffsets(t *testing.T) {
	calls := 0
	// q is "ééé pikchu": the token starts at CHARACTER 4. Slicing that by byte
	// offset would cut one of the two-byte é's in half.
	rt := dymRT(t, 200, `{"suggest": {"dym": [
	  {"text": "pikchu", "offset": 4, "length": 6,
	   "options": [{"text": "pikachu", "score": 0.9, "freq": 221}]}
	]}}`, &calls)
	s, _ := newTestServer(t, rt)
	var resp struct {
		DidYouMean string `json:"did_you_mean"`
	}
	if err := json.Unmarshal(get(t, s, "/api/search?q=%C3%A9%C3%A9%C3%A9+pikchu").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DidYouMean != "ééé pikachu" {
		t.Errorf("did_you_mean = %q, want %q", resp.DidYouMean, "ééé pikachu")
	}
}
