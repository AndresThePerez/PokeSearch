package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://fake-es:9200"},
		Transport: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Pokesearch</title>")},
	}
	fixed := func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }
	return New(es, static, &logBuf, fixed), &logBuf
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// Two real docs (base1-1 Alakazam, ex11-12 Mewtwo delta) inside a real-shaped
// ES search response, with aggregations.
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
    "supertype":  {"buckets": [{"key": "Pokémon", "doc_count": 61}]},
    "types":      {"buckets": [{"key": "Lightning", "doc_count": 61}]},
    "rarity":     {"buckets": [{"key": "Common", "doc_count": 30}]},
    "set_series": {"buckets": [{"key": "Base", "doc_count": 16}]}
  }
}`

func TestSearchHandler(t *testing.T) {
	var esReqBody []byte
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		esReqBody, _ = io.ReadAll(r.Body)
		return esResponse(200, searchESBody), nil
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=pikuchu&types=Lightning&debug=1")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Total   int                         `json:"total"`
		Page    int                         `json:"page"`
		Pages   int                         `json:"pages"`
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
	if len(resp.Results) != 2 || resp.Results[0]["name"] != "Alakazam" || resp.Results[1]["name"] != "Mewtwo δ" {
		t.Errorf("results: %v", resp.Results)
	}
	if len(resp.Facets) != 4 || resp.Facets["types"][0]["value"] != "Lightning" || resp.Facets["types"][0]["count"] != float64(61) {
		t.Errorf("facets: %v", resp.Facets)
	}
	if resp.DSL == nil || resp.DSL["track_total_hits"] != true {
		t.Errorf("debug=1 must echo the DSL, got %v", resp.DSL)
	}
	if !bytes.Contains(esReqBody, []byte(`"minimum_should_match":1`)) {
		t.Errorf("ES request body: %s", esReqBody)
	}

	line := strings.TrimSpace(logBuf.String())
	if strings.Count(line, "\n") != 0 || line == "" {
		t.Fatalf("want exactly 1 log line, got %q", logBuf.String())
	}
	var lg map[string]any
	if err := json.Unmarshal([]byte(line), &lg); err != nil {
		t.Fatal(err)
	}
	if lg["endpoint"] != "search" || lg["took_ms"] != float64(4) || lg["total"] != float64(61) ||
		lg["status"] != float64(200) || lg["time"] != "2026-07-06T12:00:00Z" {
		t.Errorf("log line: %v", lg)
	}
	p := lg["params"].(map[string]any)
	if p["q"] != "pikuchu" || p["sort"] != "relevance" {
		t.Errorf("log params: %v", p)
	}
	if types := p["types"].([]any); len(types) != 1 || types[0] != "Lightning" {
		t.Errorf("log types: %v", p)
	}
	if _, ok := p["page"]; ok {
		t.Errorf("page=1 must be omitted from log params: %v", p)
	}
}

func TestSearchHandlerEmptyResults(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(200, `{"took":1,"hits":{"total":{"value":0},"hits":[]},
		  "aggregations":{"supertype":{"buckets":[]},"types":{"buckets":[]},"rarity":{"buckets":[]},"set_series":{"buckets":[]}}}`), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=zzzzzz")
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Errorf("empty results must be [], got %s", rec.Body.String())
	}
	var resp struct {
		Pages int `json:"pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Pages != 0 {
		t.Errorf("pages = %d, want 0", resp.Pages)
	}
}

func TestSearchHandlerESDown(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	s, logBuf := newTestServer(t, rt)
	rec := get(t, s, "/api/search?q=x")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "elasticsearch unavailable") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), `"status":503`) {
		t.Errorf("failure must still log: %q", logBuf.String())
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
