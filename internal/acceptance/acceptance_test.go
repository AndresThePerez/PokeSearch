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

	searchpkg "github.com/AndresThePerez/pokesearch/internal/search"
)

const (
	wantDocs      = 20324
	wantSets      = 173
	wantRarities  = 38
	wantSeries    = 17
	wantTypes     = 11
	wantClasses   = 3
	pageSize      = 24
	maxDocsWindow = 9600
	// Browse reaches 9600/24 pages — a frozen contract, not total/pageSize.
	wantBrowsePages = maxDocsWindow / pageSize
	esLatencyMs     = 100
	fullLatencyMs   = 250
	// Corpus superlatives the Stats view reports: the highest HP printed and
	// the two ends of the release timeline.
	wantMaxHP     = 380
	wantFirstYear = "1999"
	wantLastYear  = "2026"
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
	Total    int                 `json:"total"`
	Page     int                 `json:"page"`
	Pages    int                 `json:"pages"`
	PageSize int                 `json:"page_size"`
	TookMs   int                 `json:"took_ms"`
	Results  []map[string]any    `json:"results"`
	Facets   map[string][]bucket `json:"facets"`
	DSL      map[string]any      `json:"dsl"`
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

type errorResp struct {
	Error struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// rawSearch returns the status and decoded error envelope without asserting a
// 200 — search() fatals on non-2xx, which is exactly what the 400 table needs
// to inspect instead.
func rawSearch(t *testing.T, qs string) (int, errorResp) {
	t.Helper()
	res, err := http.Get(baseURL() + "/api/search?" + qs)
	if err != nil {
		t.Fatalf("GET %s: %v", qs, err)
	}
	defer res.Body.Close()
	var out errorResp
	if res.StatusCode != 200 {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("GET %s: decode error body: %v", qs, err)
		}
	}
	return res.StatusCode, out
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
	// sort=bogus used to fall back to newest; since M3 (D1) it is a 400.
	// See TestErrorContract.
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

// The M3 error contract (D1/A2): strict params return 400 with a field-named
// structured body; list members and unknown keys stay lenient at 200.
func TestErrorContract(t *testing.T) {
	strict := []struct{ query, field string }{
		{"sort=bogus", "sort"},
		{"sort=hp&order=sideways", "order"},
		{"supertype=wizard", "supertype"},
		{"hp_min=abc", "hp_min"},
		{"hp_max=1e3", "hp_max"},
		{"page=two", "page"},
		{"page_size=lots", "page_size"},
	}
	for _, tc := range strict {
		status, body := rawSearch(t, tc.query)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.query, status)
			continue
		}
		if body.Error.Code != "invalid_param" || body.Error.Field != tc.field || body.Error.Message == "" {
			t.Errorf("%s: error = %+v, want invalid_param on %q with a message", tc.query, body.Error, tc.field)
		}
	}

	lenient := []string{
		"types=Wizard", "types=Wizard,Fire", "rarity=NotARarity", "series=Nope",
		"page=999999", "page_size=500", "utm_source=x", "supertype=POKEMON",
	}
	for _, query := range lenient {
		if status, body := rawSearch(t, query); status != http.StatusOK {
			t.Errorf("%s: status %d, want 200 (lenient): %+v", query, status, body.Error)
		}
	}
}

// page_size (M3, D2) is bounded 1..100 and the reachable window stays at
// 9,600 docs at every size — which is exactly what keeps browse at 400 pages
// on the default. The default itself is a Courier fixture: unchanged at 24.
func TestPageSizeBounds(t *testing.T) {
	if r := search(t, ""); r.PageSize != pageSize || r.Pages != wantBrowsePages || len(r.Results) != pageSize {
		t.Errorf("default browse: page_size=%d pages=%d results=%d, want %d/%d/%d",
			r.PageSize, r.Pages, len(r.Results), pageSize, wantBrowsePages, pageSize)
	}
	if r := search(t, "page_size=100"); r.PageSize != 100 || r.Pages != 96 || len(r.Results) != 100 {
		t.Errorf("page_size=100: page_size=%d pages=%d results=%d, want 100/96/100",
			r.PageSize, r.Pages, len(r.Results))
	}
	if r := search(t, "page_size=1"); r.PageSize != 1 || r.Pages != maxDocsWindow || len(r.Results) != 1 {
		t.Errorf("page_size=1: page_size=%d pages=%d results=%d, want 1/%d/1",
			r.PageSize, r.Pages, len(r.Results), maxDocsWindow)
	}
	// Out-of-range clamps rather than rejects (D1: a clamp is a contract).
	if r := search(t, "page_size=500"); r.PageSize != 100 {
		t.Errorf("page_size=500 must clamp to 100, got %d", r.PageSize)
	}
	if r := search(t, "page_size=0"); r.PageSize != 1 {
		t.Errorf("page_size=0 must clamp to 1, got %d", r.PageSize)
	}
	// The page ceiling moves with the size: 9600/100 = 96.
	if r := search(t, "page_size=100&page=999999"); r.Page != 96 {
		t.Errorf("page clamp at size 100 = %d, want 96", r.Page)
	}
	if r := search(t, "page=999999"); r.Page != wantBrowsePages {
		t.Errorf("page clamp at default size = %d, want %d", r.Page, wantBrowsePages)
	}
}

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
	Types     []bucket `json:"types"`
	Supertype []bucket `json:"supertype"`
	Rarity    []bucket `json:"rarity"`
	Series    []bucket `json:"series"`
	TookMs    int      `json:"took_ms"`
}

func getStats(t *testing.T) statsResp {
	t.Helper()
	started := time.Now()
	res, err := http.Get(baseURL() + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/stats: status %d", res.StatusCode)
	}
	var out statsResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("GET /api/stats: decode: %v", err)
	}
	if out.TookMs > esLatencyMs {
		t.Errorf("/api/stats: ES took %dms, target <%dms", out.TookMs, esLatencyMs)
	}
	if elapsed := time.Since(started).Milliseconds(); elapsed > fullLatencyMs {
		t.Errorf("/api/stats: round trip %dms, target <%dms", elapsed, fullLatencyMs)
	}
	return out
}

// The Stats view (M3/B4) describes the same pinned corpus the facets do, so its
// cardinalities are the same frozen numbers — asserted here because the charts
// are the one place a silently truncated aggregation would look plausible.
func TestStatsCorpusShape(t *testing.T) {
	r := getStats(t)
	if r.Total != wantDocs || r.MaxHP != wantMaxHP {
		t.Errorf("total = %d, max_hp = %d, want %d/%d", r.Total, r.MaxHP, wantDocs, wantMaxHP)
	}
	for name, got := range map[string]int{
		"types": len(r.Types), "supertype": len(r.Supertype),
		"rarity": len(r.Rarity), "series": len(r.Series),
	} {
		want := map[string]int{
			"types": wantTypes, "supertype": wantClasses,
			"rarity": wantRarities, "series": wantSeries,
		}[name]
		if got != want {
			t.Errorf("stats %s has %d buckets, want %d", name, got, want)
		}
	}

	// Every card has exactly one supertype and belongs to exactly one series,
	// so those two breakdowns must account for the whole corpus. types, rarity
	// and hp deliberately do not: a Trainer has no type or HP, and a handful of
	// cards carry no rarity at all.
	for name, buckets := range map[string][]bucket{"supertype": r.Supertype, "series": r.Series} {
		sum := 0
		for _, b := range buckets {
			sum += b.Count
		}
		if sum != wantDocs {
			t.Errorf("stats %s counts sum to %d, want %d", name, sum, wantDocs)
		}
	}

	// The timeline runs 1999 → 2026 with no year missing: min_doc_count 0 keeps
	// the empty years, and a gap would silently rescale the chart's x axis.
	if len(r.PerYear) == 0 {
		t.Fatal("per_year is empty")
	}
	if r.PerYear[0].Year != wantFirstYear || r.PerYear[len(r.PerYear)-1].Year != wantLastYear {
		t.Errorf("per_year spans %s..%s, want %s..%s",
			r.PerYear[0].Year, r.PerYear[len(r.PerYear)-1].Year, wantFirstYear, wantLastYear)
	}
	yearSum := 0
	for i, b := range r.PerYear {
		yearSum += b.Count
		if want := fmt.Sprint(1999 + i); b.Year != want {
			t.Errorf("per_year[%d] = %s, want %s (no year may be dropped)", i, b.Year, want)
		}
	}
	if yearSum != wantDocs {
		t.Errorf("per_year counts sum to %d, want %d", yearSum, wantDocs)
	}

	// HP bands are contiguous from 0 and reach the corpus maximum.
	for i, b := range r.HP {
		if want := i * 30; b.From != want {
			t.Errorf("hp[%d] starts at %d, want %d", i, b.From, want)
		}
	}
	if last := r.HP[len(r.HP)-1].From; last != (wantMaxHP/30)*30 {
		t.Errorf("hp bands stop at %d, want the band holding max HP %d", last, wantMaxHP)
	}
}

// TestFacetsLeaveNoRemainder makes the "rarity terms.size 30 against 38 live
// rarities" drift class structural instead of anecdotal.
//
// A terms aggregation only drops buckets into sum_other_doc_count when its
// size is below the field's cardinality, and the API deliberately does not
// surface sum_other_doc_count (it is ES bookkeeping, not a search result). So
// the remainder is asserted through its two observable halves: every registered
// facet returns exactly the corpus's known cardinality, and the size configured
// in searchpkg.Facets sits at or above it. Driving the loop off the registry means
// a sixth facet cannot be added without an expectation here.
func TestFacetsLeaveNoRemainder(t *testing.T) {
	live := map[string]int{
		"supertype": wantClasses, "types": wantTypes, "rarity": wantRarities,
		"set_series": wantSeries, searchpkg.SetsFacet: wantSets,
	}
	if len(searchpkg.Facets) != len(live) {
		t.Fatalf("%d facets registered, %d expectations — add the new facet's live cardinality",
			len(searchpkg.Facets), len(live))
	}

	browse := search(t, "")
	for _, f := range searchpkg.Facets {
		want, ok := live[f.Name]
		if !ok {
			t.Errorf("facet %q has no expected cardinality", f.Name)
			continue
		}
		if got := len(browse.Facets[f.Name]); got != want {
			t.Errorf("facet %q returned %d buckets, want %d — a short terms size leaves a "+
				"sum_other_doc_count remainder and the UI silently loses values", f.Name, got, want)
		}
		if f.Size != 0 && f.Size < want {
			t.Errorf("facet %q: terms size %d is below the live cardinality %d",
				f.Name, f.Size, want)
		}
	}
}
