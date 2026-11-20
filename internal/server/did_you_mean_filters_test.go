package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// dymCluster is a fake Elasticsearch for a zero-hit text search. It tells the
// four request kinds apart by their bodies: the search itself (it carries the
// facet aggregations), the set catalog, the term suggester, and the hit check
// a correction gets before it is offered (no aggregations, no suggester).
type dymCluster struct {
	mu           sync.Mutex
	checkStatus  int    // status of the hit check
	checkBody    string // body of the hit check
	searches     int
	suggests     int
	checks       int
	checkRequest map[string]any
}

func (c *dymCluster) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(string(raw), `"set_catalog"`):
		return esResponse(200, catalogESBody), nil
	case body["suggest"] != nil:
		c.suggests++
		return esResponse(200, `{"suggest": {"dym": [
		  {"text": "charizrd", "offset": 0, "length": 8,
		   "options": [{"text": "charizard", "score": 0.87, "freq": 107}]}
		]}}`), nil
	case body["aggs"] != nil:
		c.searches++
		return esResponse(200, zeroResultsESBody), nil
	default:
		c.checks++
		c.checkRequest = body
		return esResponse(c.checkStatus, c.checkBody), nil
	}
}

func hitCheckBody(total int) string {
	return `{"took":1,"hits":{"total":{"value":` + strconv.Itoa(total) + `,"relation":"eq"},"hits":[]}}`
}

func dymSearch(t *testing.T, c *dymCluster, query string) (*dymResponse, *strings.Builder) {
	t.Helper()
	var logBuf strings.Builder
	s := newTestServerLogging(t, c, &logBuf)
	rec := get(t, s, "/api/search?"+query)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp dymResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	resp.raw = rec.Body.String()
	return &resp, &logBuf
}

type dymResponse struct {
	Total      int     `json:"total"`
	DidYouMean *string `json:"did_you_mean"`
	raw        string
}

// The correction is checked against the request's own filters: if it finds
// nothing there either, offering it would lead the user to another empty page.
func TestDidYouMeanOmittedWhenFiltersExcludeTheCorrection(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(0)}
	resp, _ := dymSearch(t, c, "q=charizrd&supertype=Energy")
	if resp.DidYouMean != nil {
		t.Errorf("did_you_mean = %q, want it absent: the correction has no hit under supertype=Energy", *resp.DidYouMean)
	}
	if c.searches != 1 || c.suggests != 1 || c.checks != 1 {
		t.Errorf("ES calls: %d searches, %d suggests, %d checks; want 1, 1, 1", c.searches, c.suggests, c.checks)
	}
}

func TestDidYouMeanOfferedWhenFiltersKeepTheCorrection(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(1)}
	query := "q=charizrd&types=Fire&hp_min=100&sort=name"
	resp, _ := dymSearch(t, c, query)
	if resp.DidYouMean == nil || *resp.DidYouMean != "charizard" {
		t.Fatalf("did_you_mean = %v, want \"charizard\": %s", resp.DidYouMean, resp.raw)
	}
	if c.searches != 1 || c.suggests != 1 || c.checks != 1 {
		t.Errorf("ES calls: %d searches, %d suggests, %d checks; want 1, 1, 1", c.searches, c.suggests, c.checks)
	}

	// The check is the search the user would run by taking the correction:
	// the same parameters through the same builder, q replaced, nothing
	// fetched, counted no further than the first hit.
	values, _ := url.ParseQuery(query)
	values.Set("q", "charizard")
	p, ferrs := search.ParseParams(values)
	if len(ferrs) > 0 {
		t.Fatal(ferrs)
	}
	want := search.BuildQuery(p)
	var wantJSON map[string]any
	b, _ := json.Marshal(map[string]any{"query": want["query"], "post_filter": want["post_filter"]})
	_ = json.Unmarshal(b, &wantJSON)
	got := c.checkRequest
	if !reflect.DeepEqual(got["query"], wantJSON["query"]) {
		t.Errorf("check query = %v, want the built query for the correction %v", got["query"], wantJSON["query"])
	}
	if got["post_filter"] == nil || !reflect.DeepEqual(got["post_filter"], wantJSON["post_filter"]) {
		t.Errorf("check post_filter = %v, want the request's own filters %v", got["post_filter"], wantJSON["post_filter"])
	}
	if got["size"] != float64(0) || got["track_total_hits"] != float64(1) {
		t.Errorf("check size = %v, track_total_hits = %v; want 0 and 1", got["size"], got["track_total_hits"])
	}
	for _, heavy := range []string{"aggs", "highlight", "sort", "from"} {
		if _, ok := got[heavy]; ok {
			t.Errorf("check carries %q, which a hit count does not need", heavy)
		}
	}
}

// With no filter the correction needs no check: the suggester only offers a
// term the card names already contain, so the corrected query matches the
// card it came from. The answer is today's, and so is its cost.
func TestDidYouMeanWithoutFiltersNeedsNoCheck(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(0)}
	resp, _ := dymSearch(t, c, "q=charizrd")
	if resp.DidYouMean == nil || *resp.DidYouMean != "charizard" {
		t.Fatalf("did_you_mean = %v, want \"charizard\": %s", resp.DidYouMean, resp.raw)
	}
	if c.searches != 1 || c.suggests != 1 || c.checks != 0 {
		t.Errorf("ES calls: %d searches, %d suggests, %d checks; want 1, 1, 0", c.searches, c.suggests, c.checks)
	}
}

// A failing check costs the hint, never the search.
func TestDidYouMeanCheckFailureOmitsTheCorrection(t *testing.T) {
	c := &dymCluster{checkStatus: 500, checkBody: `{"error":{"type":"search_phase_execution_exception"}}`}
	resp, logBuf := dymSearch(t, c, "q=charizrd&rarity=Common")
	if resp.DidYouMean != nil {
		t.Errorf("did_you_mean = %q, want it absent when the check failed", *resp.DidYouMean)
	}
	if resp.Total != 0 {
		t.Errorf("total = %d, want the search's own 0", resp.Total)
	}
	if c.searches != 1 || c.suggests != 1 || c.checks != 1 {
		t.Errorf("ES calls: %d searches, %d suggests, %d checks; want 1, 1, 1", c.searches, c.suggests, c.checks)
	}
	if !strings.Contains(logBuf.String(), "did-you-mean") || !strings.Contains(logBuf.String(), "search_phase_execution_exception") {
		t.Errorf("the failed check must be logged with its cause: %s", logBuf.String())
	}
}

// No candidate, no check: a suggester with nothing to offer ends the work.
func TestDidYouMeanNoCandidateMakesNoCheck(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(1)}
	s := newTestServerLogging(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"dym"`) {
			return esResponse(200, `{"suggest":{"dym":[{"text":"zzzz","offset":0,"length":4,"options":[]}]}}`), nil
		}
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		return c.RoundTrip(r)
	}), io.Discard)
	if body := get(t, s, "/api/search?q=zzzz&supertype=Energy").Body.String(); strings.Contains(body, "did_you_mean") {
		t.Errorf("no candidate must mean no field: %s", body)
	}
	if c.checks != 0 {
		t.Errorf("checks = %d, want 0 without a candidate", c.checks)
	}
}
