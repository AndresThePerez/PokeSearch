package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// Every filter the search carried reaches the hit check, built by the same
// query builder from the same parameters, and a correction that finds nothing
// under any one of them is withheld. A filter the check dropped would offer a
// correction that leads to another empty page.
func TestDidYouMeanCheckCarriesEveryFilterOfTheSearch(t *testing.T) {
	for _, tc := range []struct{ name, query, clause string }{
		{"supertype", "supertype=Energy", `"supertype"`},
		{"supertype by another spelling", "supertype=pokemon", `"supertype"`},
		{"types", "types=Fire,Water", `"types"`},
		{"rarity", "rarity=Common", `"rarity"`},
		{"series", "series=Base", `"set_series"`},
		{"set", "set=base1", `"set_id"`},
		{"hp_min alone", "hp_min=100", `"range"`},
		{"hp_max alone", "hp_max=20", `"range"`},
		{"hp_min of zero", "hp_min=0", `"range"`},
		{"id with a text query", "id=base1-4", `"id"`},
		{"everything at once", "supertype=Pokemon&types=Fire&rarity=Rare&series=Base&set=base1&hp_min=10&hp_max=500", `"set_id"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(0)}
			resp, _ := dymSearch(t, c, "q=charizrd&"+tc.query)
			if resp.DidYouMean != nil {
				t.Errorf("did_you_mean = %q: the check found nothing under %s", *resp.DidYouMean, tc.query)
			}
			if c.checks != 1 {
				t.Fatalf("checks = %d, want 1: %s makes a post_filter", c.checks, tc.query)
			}
			values, _ := url.ParseQuery(tc.query)
			values.Set("q", "charizard")
			p, ferrs := search.ParseParams(values)
			if len(ferrs) > 0 {
				t.Fatal(ferrs)
			}
			var want any
			raw, _ := json.Marshal(search.BuildQuery(p)["post_filter"])
			_ = json.Unmarshal(raw, &want)
			if !reflect.DeepEqual(c.checkRequest["post_filter"], want) {
				t.Errorf("check post_filter = %v, want the builder's %v", c.checkRequest["post_filter"], want)
			}
			if got, _ := json.Marshal(c.checkRequest["post_filter"]); !strings.Contains(string(got), tc.clause) {
				t.Errorf("check post_filter %s has no %s clause", got, tc.clause)
			}
		})
	}
}

// What the check answers decides the field; only a positive count offers the
// correction, and no answer at all is read as a positive.
func TestDidYouMeanCheckAnswerShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		offer  bool
	}{
		{"one hit, exact", 200, `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[]}}`, true},
		{"capped at one, lower bound", 200, `{"hits":{"total":{"value":1,"relation":"gte"},"hits":[]}}`, true},
		{"zero, exact", 200, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`, false},
		{"zero with a lower-bound relation", 200, `{"hits":{"total":{"value":0,"relation":"gte"},"hits":[]}}`, false},
		{"no hits object at all", 200, `{"took":1}`, false},
		{"empty object", 200, `{}`, false},
		{"null total", 200, `{"hits":{"total":null}}`, false},
		{"legacy integer total", 200, `{"hits":{"total":3,"hits":[]}}`, false},
		{"malformed JSON", 200, `{"hits":{"total":{"value":1`, false},
		{"empty body", 200, ``, false},
		{"html body", 200, `<html>proxy error</html>`, false},
		{"rejected request", 400, `{"error":{"type":"parsing_exception"}}`, false},
		{"server error", 500, `{"error":{"type":"search_phase_execution_exception"}}`, false},
		{"missing index", 404, `{"error":{"type":"index_not_found_exception"},"status":404}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &dymCluster{checkStatus: tc.status, checkBody: tc.body}
			resp, _ := dymSearch(t, c, "q=charizrd&supertype=Pokemon")
			if resp.Total != 0 {
				t.Errorf("total = %d, want the search's own 0", resp.Total)
			}
			if got := resp.DidYouMean != nil; got != tc.offer {
				t.Errorf("did_you_mean offered = %v, want %v (%s)", got, tc.offer, resp.raw)
			}
		})
	}
}

// A request whose context ends while the correction is being checked still
// gets its search answered, with no correction, and the check is made with the
// request's own context, not a detached one.
func TestDidYouMeanCheckRunsUnderTheRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var checkSawCancelled bool
	checks := 0
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(string(raw), `"set_catalog"`):
			return esResponse(200, catalogESBody), nil
		case body["suggest"] != nil:
			// The client goes away after the suggester has answered.
			cancel()
			return esResponse(200, `{"suggest":{"dym":[{"text":"charizrd","offset":0,"length":8,"options":[{"text":"charizard"}]}]}}`), nil
		case body["aggs"] != nil:
			return esResponse(200, zeroResultsESBody), nil
		}
		checks++
		if err := r.Context().Err(); err != nil {
			checkSawCancelled = true
			return nil, err
		}
		return esResponse(200, hitCheckBody(5)), nil
	})
	s := newTestServerLogging(t, rt, io.Discard)
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/search?q=charizrd&supertype=Pokemon", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want the search's own 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "did_you_mean") {
		t.Errorf("a cancelled check must leave no correction: %s", rec.Body.String())
	}
	// The client library may retry a failed call; what matters is that every
	// attempt was made under the request's context.
	if checks == 0 || !checkSawCancelled {
		t.Errorf("checks = %d, saw a cancelled context = %v; want the check run under the request's context", checks, checkSawCancelled)
	}
}

// The check counts hits and nothing else: paging, ordering, debug output and
// highlighting of the original search stay out of it, and the original page
// number cannot make it skip past the first hit.
func TestDidYouMeanCheckIgnoresPagingSortAndDebug(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(1)}
	resp, _ := dymSearch(t, c, "q=charizrd&supertype=Pokemon&page=3&page_size=10&sort=hp&order=asc&debug=1")
	if resp.DidYouMean == nil || *resp.DidYouMean != "charizard" {
		t.Fatalf("did_you_mean = %v, want charizard: %s", resp.DidYouMean, resp.raw)
	}
	for key := range c.checkRequest {
		switch key {
		case "size", "track_total_hits", "query", "post_filter":
		default:
			t.Errorf("check body carries %q", key)
		}
	}
	if c.checkRequest["size"] != float64(0) || c.checkRequest["track_total_hits"] != float64(1) {
		t.Errorf("size = %v, track_total_hits = %v, want 0 and 1", c.checkRequest["size"], c.checkRequest["track_total_hits"])
	}
}

// A filter the search ignored is a filter the check ignores: with every value
// dropped as unknown there is no post_filter, so there is nothing to check and
// the correction is offered as it always was.
func TestDidYouMeanDroppedFilterValuesAreNotChecked(t *testing.T) {
	c := &dymCluster{checkStatus: 200, checkBody: hitCheckBody(0)}
	resp, _ := dymSearch(t, c, "q=charizrd&types=Bogus,Nonsense&set=&series=&rarity=&supertype=")
	if resp.DidYouMean == nil || *resp.DidYouMean != "charizard" {
		t.Fatalf("did_you_mean = %v, want charizard: %s", resp.DidYouMean, resp.raw)
	}
	if c.checks != 0 {
		t.Errorf("checks = %d, want 0: the search carried no filter", c.checks)
	}
}

// A search that found cards pays for no suggester call and no check, filters
// or not: the correction is only for an empty page.
func TestDidYouMeanFilteredSearchWithHitsAsksNothing(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(string(raw), `"set_catalog"`):
			return esResponse(200, catalogESBody), nil
		case strings.Contains(string(raw), `"suggest"`):
			calls["suggest"]++
			return esResponse(200, `{"suggest":{"dym":[]}}`), nil
		case strings.Contains(string(raw), `"aggs"`):
			calls["search"]++
			return esResponse(200, searchESBody), nil
		}
		calls["check"]++
		return esResponse(200, hitCheckBody(1)), nil
	})
	s := newTestServerLogging(t, rt, io.Discard)
	if body := get(t, s, "/api/search?q=pikuchu&supertype=Pokemon&hp_min=10").Body.String(); strings.Contains(body, "did_you_mean") {
		t.Errorf("a search with hits carries did_you_mean: %s", body)
	}
	if calls["suggest"] != 0 || calls["check"] != 0 || calls["search"] != 1 {
		t.Errorf("ES calls = %v, want one search and nothing else", calls)
	}
}

// Parallel zero-hit searches each make their own one check and keep their own
// answer: the check shares no state between requests.
func TestDidYouMeanChecksAreIndependentUnderConcurrency(t *testing.T) {
	var mu sync.Mutex
	checks := map[string]int{}
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		switch {
		case strings.Contains(string(raw), `"set_catalog"`):
			return esResponse(200, catalogESBody), nil
		case body["suggest"] != nil:
			return esResponse(200, `{"suggest":{"dym":[{"text":"charizrd","offset":0,"length":8,"options":[{"text":"charizard"}]}]}}`), nil
		case body["aggs"] != nil:
			return esResponse(200, zeroResultsESBody), nil
		}
		// The check answers by the filter it was sent: Energy has none.
		filter, _ := json.Marshal(body["post_filter"])
		mu.Lock()
		checks[string(filter)]++
		mu.Unlock()
		if strings.Contains(string(filter), "Energy") {
			return esResponse(200, hitCheckBody(0)), nil
		}
		return esResponse(200, hitCheckBody(2)), nil
	})
	s := newTestServerLogging(t, rt, io.Discard)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			energy := i%2 == 0
			path := "/api/search?q=charizrd&supertype=Pokemon"
			if energy {
				path = "/api/search?q=charizrd&supertype=Energy"
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if got := strings.Contains(rec.Body.String(), `"did_you_mean":"charizard"`); got == energy {
				t.Errorf("%s: did_you_mean present = %v", path, got)
			}
		}()
	}
	wg.Wait()
	total := 0
	for _, n := range checks {
		total += n
	}
	if total != 40 || len(checks) != 2 {
		t.Errorf("checks = %v, want 40 split over two filters", checks)
	}
}
