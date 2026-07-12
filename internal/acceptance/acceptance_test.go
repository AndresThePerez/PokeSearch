//go:build acceptance

// Package acceptance runs the seeded-stack API matrix against a live
// Pokesearch instance (the pinned 20,324-card index). It is tag-gated:
//
//	go test -tags acceptance ./internal/acceptance -v
//
// POKESEARCH_URL overrides the default http://localhost:8080.
package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	wantDocs      = 20324
	wantSets      = 173
	wantRarities  = 38
	wantSeries    = 17
	wantTypes     = 11
	wantClasses   = 3
	pageSize      = 24
	esLatencyMs   = 100
	fullLatencyMs = 250
)

func baseURL() string {
	if u := os.Getenv("POKESEARCH_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8080"
}

type bucket struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	ReleaseDate string `json:"release_date"`
	Count       int    `json:"count"`
}

type searchResp struct {
	Total   int                 `json:"total"`
	Page    int                 `json:"page"`
	Pages   int                 `json:"pages"`
	TookMs  int                 `json:"took_ms"`
	Results []map[string]any    `json:"results"`
	Facets  map[string][]bucket `json:"facets"`
	DSL     map[string]any      `json:"dsl"`
}

func search(t *testing.T, qs string) searchResp {
	t.Helper()
	started := time.Now()
	res, err := http.Get(baseURL() + "/api/search?" + qs)
	if err != nil {
		t.Fatalf("GET %s: %v", qs, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", qs, res.StatusCode)
	}
	var out searchResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: decode: %v", qs, err)
	}
	elapsed := time.Since(started).Milliseconds()
	if out.TookMs > esLatencyMs {
		t.Errorf("GET %s: ES took %dms, target <%dms", qs, out.TookMs, esLatencyMs)
	}
	if elapsed > fullLatencyMs {
		t.Errorf("GET %s: round trip %dms, target <%dms", qs, elapsed, fullLatencyMs)
	}
	return out
}

func counts(buckets []bucket) map[string]int {
	m := make(map[string]int, len(buckets))
	for _, b := range buckets {
		m[b.Value] = b.Count
	}
	return m
}

func ids(results []map[string]any) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r["id"].(string))
	}
	return out
}

func TestBaselineCardinalities(t *testing.T) {
	r := search(t, "")
	if r.Total != wantDocs {
		t.Errorf("total = %d, want %d", r.Total, wantDocs)
	}
	for facet, want := range map[string]int{
		"supertype": wantClasses, "types": wantTypes, "rarity": wantRarities,
		"set_series": wantSeries, "sets": wantSets,
	} {
		if got := len(r.Facets[facet]); got != want {
			t.Errorf("%s facet has %d values, want %d", facet, got, want)
		}
	}
}

func TestAllRaritiesReachable(t *testing.T) {
	have := counts(search(t, "").Facets["rarity"])
	for _, rare := range []string{
		"Amazing Rare", "Radiant Rare", "Rare ACE", "Rare Shining",
		"Shiny Ultra Rare", "Black White Rare", "MEGA_ATTACK_RARE", "Mega Hyper Rare",
	} {
		if have[rare] == 0 {
			t.Errorf("rarity %q missing or zero in baseline facet", rare)
		}
	}
}

// A text query must narrow every facet, including Set, and each set count
// must equal the direct query total for that set (exclude-self semantics).
func TestQueryNarrowsAllFacetsIncludingSets(t *testing.T) {
	r := search(t, "q=Pikachu")
	if r.Total == 0 || r.Total >= wantDocs {
		t.Fatalf("q=Pikachu total = %d", r.Total)
	}
	if got := len(r.Facets["sets"]); got != wantSets {
		t.Errorf("sets catalog must stay complete: %d, want %d", got, wantSets)
	}
	sum := 0
	for _, b := range r.Facets["sets"] {
		sum += b.Count
	}
	if sum != r.Total {
		t.Errorf("set counts sum %d != total %d (cards belong to exactly one set)", sum, r.Total)
	}
	base := counts(r.Facets["sets"])["base1"]
	direct := search(t, "q=Pikachu&set=base1").Total
	if base != direct || base >= 102 {
		t.Errorf("base1 count %d, direct query %d, corpus-wide would be 102", base, direct)
	}
}

// Selecting a facet value must not collapse that facet's own alternatives.
func TestFacetDoesNotCollapseItself(t *testing.T) {
	baseline := search(t, "")
	selected := search(t, "rarity=Rare")
	if len(selected.Facets["rarity"]) != len(baseline.Facets["rarity"]) {
		t.Errorf("rarity options with Rare selected: %d, baseline %d",
			len(selected.Facets["rarity"]), len(baseline.Facets["rarity"]))
	}
	if c := counts(selected.Facets["rarity"]); c["Common"] != search(t, "rarity=Common").Total {
		t.Errorf("Common count with Rare selected = %d, direct = %d", c["Common"], search(t, "rarity=Common").Total)
	}

	series := search(t, "series=Base")
	if len(series.Facets["set_series"]) != len(baseline.Facets["set_series"]) {
		t.Errorf("series options with Base selected: %d, baseline %d",
			len(series.Facets["set_series"]), len(baseline.Facets["set_series"]))
	}
}

// Type selections are OR, so alternative type counts must predict the next
// click: with Fire selected, the Water bucket equals the Water-only total.
func TestTypeCountsPredictNextClick(t *testing.T) {
	fire := search(t, "types=Fire")
	water := search(t, "types=Water")
	if c := counts(fire.Facets["types"]); c["Water"] != water.Total {
		t.Errorf("Water shown as %d with Fire selected, clicking Water yields %d", c["Water"], water.Total)
	}
	both := search(t, "types=Fire,Water")
	if both.Total < fire.Total || both.Total < water.Total {
		t.Errorf("OR selection shrank: fire=%d water=%d both=%d", fire.Total, water.Total, both.Total)
	}
	pokemon := search(t, "supertype=pokemon")
	trainerDirect := search(t, "supertype=trainer").Total
	if c := counts(pokemon.Facets["supertype"]); c["Trainer"] != trainerDirect {
		t.Errorf("Trainer shown as %d with Pokémon selected, switching yields %d", c["Trainer"], trainerDirect)
	}
}

// Cross-facet: a selection in one facet must reshape the others.
func TestCrossFacetIntersection(t *testing.T) {
	fire := search(t, "types=Fire")
	if c := counts(fire.Facets["rarity"]); c["Common"] != search(t, "types=Fire&rarity=Common").Total {
		t.Errorf("rarity Common under Fire = %d, direct = %d", c["Common"], search(t, "types=Fire&rarity=Common").Total)
	}
	sum := 0
	for _, b := range fire.Facets["sets"] {
		sum += b.Count
	}
	if sum != fire.Total {
		t.Errorf("set counts under Fire sum %d != total %d", sum, fire.Total)
	}
}

// A zero-result combination keeps both filters live: total stays 0 and the
// facets still expose every option under each exclude-self scope.
func TestZeroResultsKeepFilters(t *testing.T) {
	r := search(t, "set=base1&rarity=Promo&debug=1")
	if r.Total != 0 {
		t.Fatalf("base1+Promo total = %d, want 0", r.Total)
	}
	post, _ := json.Marshal(r.DSL["post_filter"])
	for _, want := range []string{`"base1"`, `"Promo"`} {
		if !strings.Contains(string(post), want) {
			t.Errorf("post_filter must keep both filters, got %s", post)
		}
	}
	if got := len(r.Facets["sets"]); got != wantSets {
		t.Errorf("sets catalog during zero results: %d, want %d", got, wantSets)
	}
	// The rarity scope (set=base1 only) must still list base1's rarities.
	if len(r.Facets["rarity"]) == 0 {
		t.Error("rarity alternatives must survive a zero-result selection")
	}
}

func TestSortMatrix(t *testing.T) {
	release := func(r map[string]any) string { d, _ := r["release_date"].(string); return d }
	name := func(r map[string]any) string { n, _ := r["name"].(string); return strings.ToLower(n) }
	hp := func(r map[string]any) (float64, bool) { h, ok := r["hp"].(float64); return h, ok }

	assertOrdered := func(t *testing.T, qs string, cmp func(prev, cur map[string]any) bool) {
		t.Helper()
		r := search(t, qs)
		if len(r.Results) != pageSize {
			t.Fatalf("%s: page 1 has %d results", qs, len(r.Results))
		}
		for i := 1; i < len(r.Results); i++ {
			if !cmp(r.Results[i-1], r.Results[i]) {
				t.Errorf("%s: order breaks at index %d", qs, i)
				return
			}
		}
	}

	assertOrdered(t, "sort=newest", func(a, b map[string]any) bool { return release(a) >= release(b) })
	assertOrdered(t, "sort=oldest", func(a, b map[string]any) bool { return release(a) <= release(b) })
	assertOrdered(t, "sort=hp&order=desc", func(a, b map[string]any) bool {
		ha, oka := hp(a)
		hb, okb := hp(b)
		return oka && (!okb || ha >= hb)
	})
	assertOrdered(t, "sort=hp&order=asc", func(a, b map[string]any) bool {
		ha, oka := hp(a)
		hb, okb := hp(b)
		return !oka || (okb && ha <= hb) || !okb
	})
	assertOrdered(t, "sort=name&order=asc", func(a, b map[string]any) bool { return name(a) <= name(b) })
	assertOrdered(t, "sort=name&order=desc", func(a, b map[string]any) bool { return name(a) >= name(b) })

	if r := search(t, "q=charizard&sort=relevance&debug=1"); fmt.Sprint(r.DSL["sort"].([]any)[0]) != "_score" {
		t.Errorf("relevance must sort by _score: %v", r.DSL["sort"])
	}
	if r := search(t, "sort=bogus&debug=1"); fmt.Sprint(r.DSL["sort"].([]any)[0]) != "map[release_date:desc]" {
		t.Errorf("invalid sort must fall back to newest: %v", r.DSL["sort"])
	}
	if r := search(t, "sort=relevance&debug=1"); fmt.Sprint(r.DSL["sort"].([]any)[0]) != "map[release_date:desc]" {
		t.Errorf("blank-query relevance must normalize to newest: %v", r.DSL["sort"])
	}
}

func TestPageBoundariesDeterministic(t *testing.T) {
	seen := map[string]bool{}
	for page := 1; page <= 2; page++ {
		r := search(t, url.Values{"sort": {"newest"}, "page": {fmt.Sprint(page)}}.Encode())
		for _, id := range ids(r.Results) {
			if seen[id] {
				t.Errorf("duplicate id %s across pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 2*pageSize {
		t.Errorf("pages 1-2 yielded %d unique ids, want %d", len(seen), 2*pageSize)
	}
}
