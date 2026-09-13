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
	"net"
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

// latencyGated reports whether the target is loopback. esLatencyMs and
// fullLatencyMs are calibrated to a client on the same host as the service;
// measured across a residential link, TLS and an edge, they measure the
// link, not the service, so the assertions only apply on loopback. An unset
// POKESEARCH_URL defaults to http://localhost:8080 and is loopback. A set
// POKESEARCH_URL is loopback only when its host is localhost, 127.0.0.1 or
// ::1.
func latencyGated() bool {
	raw := os.Getenv("POKESEARCH_URL")
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
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
	// The M3 per-hit additions. All three are text-query only, and the first
	// two are aligned index-for-index with Results.
	Matched    [][]string            `json:"matched"`
	Highlights []map[string][]string `json:"highlights"`
	DidYouMean string                `json:"did_you_mean"`
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
	gated := latencyGated()
	if gated {
		if out.TookMs > esLatencyMs {
			t.Errorf("GET %s: ES took %dms, target <%dms", qs, out.TookMs, esLatencyMs)
		}
	} else {
		t.Logf("GET %s: ES took %dms (latency SLO applies only to a loopback target)", qs, out.TookMs)
	}
	if gated {
		if elapsed > fullLatencyMs {
			t.Errorf("GET %s: round trip %dms, target <%dms", qs, elapsed, fullLatencyMs)
		}
	} else {
		t.Logf("GET %s: round trip %dms (latency SLO applies only to a loopback target)", qs, elapsed)
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

// The relevance X-Ray and the highlighter, asserted against the live corpus:
// a text query answers "why is this card here" per hit, and browse — which has
// no query to be answered about — carries neither field at all.
func TestSearchResponseAdditions(t *testing.T) {
	r := search(t, "q=charizard")
	if r.Total != 107 {
		t.Fatalf("q=charizard total = %d, want 107", r.Total)
	}
	if len(r.Matched) != len(r.Results) || len(r.Highlights) != len(r.Results) {
		t.Fatalf("matched %d, highlights %d, results %d — the three must stay aligned",
			len(r.Matched), len(r.Highlights), len(r.Results))
	}
	// The first hit for an exact name is the exact branch, and every reported
	// branch must be one the builder actually names.
	named := map[string]bool{}
	for _, b := range searchpkg.Branches("charizard") {
		named[b.Name] = true
	}
	if len(r.Matched[0]) == 0 || r.Matched[0][0] != "exact" {
		t.Errorf("top hit matched %v, want exact first", r.Matched[0])
	}
	for i, branches := range r.Matched {
		for _, name := range branches {
			if !named[name] {
				t.Errorf("hit %d reports unknown branch %q", i, name)
			}
		}
	}
	// Highlighting is default-on for text queries (D5, Contract A) and the
	// fragments are <mark>-tagged server-side.
	marked := 0
	for _, highlight := range r.Highlights {
		for _, fragments := range highlight {
			for _, fragment := range fragments {
				if strings.Contains(fragment, "<mark>") {
					marked++
				}
			}
		}
	}
	if marked == 0 {
		t.Error("no highlighted fragment on a text query — highlighting is default-on")
	}

	browse := search(t, "")
	if browse.Matched != nil || browse.Highlights != nil {
		t.Errorf("browse must carry neither matched nor highlights: %v / %v", browse.Matched, browse.Highlights)
	}
}

type explainResp struct {
	ID       string  `json:"id"`
	Q        string  `json:"q"`
	Found    bool    `json:"found"`
	Score    float64 `json:"score"`
	Branches []struct {
		Name    string  `json:"name"`
		Matched bool    `json:"matched"`
		Score   float64 `json:"score"`
	} `json:"branches"`
}

// /api/explain must reconstruct the score it explains: a bool query's should
// clauses sum, so the matched branches' scores have to add up to the reported
// total. That is the sanity check that keeps the score bars honest.
func TestExplainBranchSum(t *testing.T) {
	res, err := http.Get(baseURL() + "/api/explain?id=base1-4&q=charizard")
	if err != nil {
		t.Fatalf("GET /api/explain: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/explain: status %d", res.StatusCode)
	}
	var out explainResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode explain: %v", err)
	}
	if !out.Found || out.Score <= 0 {
		t.Fatalf("explain base1-4 = %+v, want found with a positive score", out)
	}
	if len(out.Branches) != len(searchpkg.Branches("charizard")) {
		t.Errorf("explain reported %d branches, want %d", len(out.Branches), len(searchpkg.Branches("charizard")))
	}
	sum := 0.0
	for _, b := range out.Branches {
		if b.Matched {
			sum += b.Score
		}
	}
	if diff := sum - out.Score; diff > 0.01 || diff < -0.01 {
		t.Errorf("matched branches sum to %.3f but score is %.3f", sum, out.Score)
	}
	// An unknown document is an answer, not an error (found:false, no 404).
	res404, err := http.Get(baseURL() + "/api/explain?id=nope-99999&q=charizard")
	if err != nil {
		t.Fatalf("GET /api/explain (missing doc): %v", err)
	}
	defer res404.Body.Close()
	if res404.StatusCode != 200 {
		t.Errorf("explain on a missing document: status %d, want 200", res404.StatusCode)
	}
}

// Did-you-mean fires only on a true zero-result text search (D8). The trigger
// window is narrow — the search's own fuzziness rescues most typos — so these
// are the locked probes, not invented ones.
func TestDidYouMeanFixtures(t *testing.T) {
	for query, want := range map[string]string{
		"q=abxx":  "abra",
		"q=zubxy": "zubat",
		"q=ekxnz": "ekans",
		"q=onxz":  "onix",
	} {
		r := search(t, query)
		if r.Total != 0 {
			t.Errorf("%s: total = %d, want 0 (the fixture must stay a zero-result query)", query, r.Total)
			continue
		}
		if r.DidYouMean != want {
			t.Errorf("%s: did_you_mean = %q, want %q", query, r.DidYouMean, want)
		}
	}
	// A query with nothing to correct says nothing rather than guessing.
	if r := search(t, "q=zzzzqqqqxxxx"); r.Total != 0 || r.DidYouMean != "" {
		t.Errorf("q=zzzzqqqqxxxx: total = %d, did_you_mean = %q, want 0 and empty", r.Total, r.DidYouMean)
	}
	// A query that finds cards is never second-guessed.
	if r := search(t, "q=charizard"); r.DidYouMean != "" {
		t.Errorf("a successful search must not carry did_you_mean, got %q", r.DidYouMean)
	}
}

// The identity surface: liveness, the frozen health contract, build identity,
// the request-id echo, and metrics staying off unless they are asked for.
func TestIdentityEndpoints(t *testing.T) {
	var live struct {
		Status string `json:"status"`
	}
	getJSON(t, "/livez", &live)
	if live.Status != "alive" {
		t.Errorf("/livez = %+v", live)
	}

	var health map[string]any
	getJSON(t, "/healthz", &health)
	if len(health) != 2 || health["status"] != "ok" || health["docs"] != float64(wantDocs) {
		t.Errorf("/healthz = %v, want exactly {docs:%d, status:ok} — this shape is frozen", health, wantDocs)
	}

	var meta struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Docs    int    `json:"docs"`
	}
	getJSON(t, "/api/meta", &meta)
	if meta.Version == "" || meta.Commit == "" || meta.Docs != wantDocs {
		t.Errorf("/api/meta = %+v", meta)
	}

	// An inbound trace id is honoured and echoed; every response carries one.
	req, err := http.NewRequest("GET", baseURL()+"/api/search?q=pikachu", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-Id", "acceptance-probe-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with request id: %v", err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("X-Request-Id"); got != "acceptance-probe-1" {
		t.Errorf("X-Request-Id echo = %q, want the inbound id", got)
	}

	// D3: expvar is opt-in and the compose stack does not opt in.
	vars, err := http.Get(baseURL() + "/debug/vars")
	if err != nil {
		t.Fatalf("GET /debug/vars: %v", err)
	}
	defer vars.Body.Close()
	if vars.StatusCode != http.StatusNotFound {
		t.Errorf("/debug/vars status %d, want 404 without METRICS", vars.StatusCode)
	}
}

func getJSON(t *testing.T, path string, into any) {
	t.Helper()
	res, err := http.Get(baseURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
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
	gated := latencyGated()
	if gated {
		if out.TookMs > esLatencyMs {
			t.Errorf("/api/stats: ES took %dms, target <%dms", out.TookMs, esLatencyMs)
		}
	} else {
		t.Logf("/api/stats: ES took %dms (latency SLO applies only to a loopback target)", out.TookMs)
	}
	elapsed := time.Since(started).Milliseconds()
	if gated {
		if elapsed > fullLatencyMs {
			t.Errorf("/api/stats: round trip %dms, target <%dms", elapsed, fullLatencyMs)
		}
	} else {
		t.Logf("/api/stats: round trip %dms (latency SLO applies only to a loopback target)", elapsed)
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
