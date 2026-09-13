package search

import (
	"encoding/json"
	"net/url"
	"testing"
)

// canonV normalizes any JSON-marshalable value for byte-for-byte comparison.
func canonV(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func canonS(t *testing.T, raw string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	return canonV(t, v)
}

func params(t *testing.T, qs string) Params {
	t.Helper()
	v, err := url.ParseQuery(qs)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := ParseParams(v)
	return p
}

// With no active filters facets can aggregate directly in the text-query
// scope; exclude-self filter wrappers are only needed when a scope is active.
const noFilterAggsJSON = `{
  "supertype":  {"terms": {"field": "supertype"}},
  "types":      {"terms": {"field": "types", "size": 11}},
  "rarity":     {"terms": {"field": "rarity", "size": 100}},
  "set_series": {"terms": {"field": "set_series", "size": 20}},
  "sets":       {"terms": {"field": "set_id", "size": 200}}
}`

func TestBuildQueryBrowseMode(t *testing.T) {
	got := canonV(t, BuildQuery(params(t, "")))
	want := canonS(t, `{
	  "track_total_hits": true, "size": 24,
	  "sort": [{"release_date": "desc"}, {"id": "asc"}],
	  "aggs": `+noFilterAggsJSON+`}`)
	if got != want {
		t.Errorf("browse DSL\n got %s\nwant %s", got, want)
	}
}

func TestBuildQueryFullText(t *testing.T) {
	got := canonV(t, BuildQuery(params(t, "q=Pikuchu")))
	want := canonS(t, `{
	  "track_total_hits": true, "size": 24,
	  "query": {"bool": {
	    "should": [
	      {"term": {"name.kw": {"value": "pikuchu", "boost": 8, "_name": "exact"}}},
	      {"multi_match": {"query": "Pikuchu", "type": "bool_prefix",
	        "fields": ["name.sayt", "name.sayt._2gram", "name.sayt._3gram"], "boost": 2,
	        "_name": "prefix"}},
	      {"match": {"name": {"query": "Pikuchu", "fuzziness": "AUTO", "boost": 1.5,
	        "_name": "fuzzy-name"}}},
	      {"multi_match": {"query": "Pikuchu", "type": "best_fields", "fuzziness": "AUTO",
	        "fields": ["attacks.name^2", "abilities.name^2", "attacks.text", "abilities.text",
	                   "flavor_text", "set_name^1.5", "artist"], "_name": "text"}}
	    ]
	  }},
	  "sort": ["_score", {"id": "asc"}],
	  "highlight": `+highlightJSON+`,
	  "aggs": `+noFilterAggsJSON+`}`)
	if got != want {
		t.Errorf("full-text DSL\n got %s\nwant %s", got, want)
	}
}

// The highlight block, server-side <mark> tagging over the fields a text query
// can actually match. Names come back whole (number_of_fragments 0); body text
// comes back as one short fragment, which is all a one-line snippet can show.
const highlightJSON = `{
  "pre_tags": ["<mark>"],
  "post_tags": ["</mark>"],
  "fields": {
    "name":           {"number_of_fragments": 0},
    "attacks.name":   {"number_of_fragments": 0},
    "abilities.name": {"number_of_fragments": 0},
    "attacks.text":   {"fragment_size": 120, "number_of_fragments": 1},
    "abilities.text": {"fragment_size": 120, "number_of_fragments": 1},
    "flavor_text":    {"fragment_size": 120, "number_of_fragments": 1}
  },
  "highlight_query": {"multi_match": {
    "query": "Pikuchu", "type": "best_fields",
    "fields": ["name", "attacks.name", "abilities.name",
               "attacks.text", "abilities.text", "flavor_text"]
  }}
}`

// Highlighting is for text queries only. Browse has no query to highlight and
// the exact-ID fast path is a single-document fetch — asking either for
// highlights buys nothing and costs a per-hit re-analysis of the source.
func TestHighlightOnlyForTextQueries(t *testing.T) {
	if _, ok := BuildQuery(params(t, "q=charizard"))["highlight"]; !ok {
		t.Error("a text query must carry a highlight block")
	}
	for _, qs := range []string{"", "supertype=pokemon", "types=Fire&sort=hp", "id=base1-1", "id=base1-1&types=Fire"} {
		if _, ok := BuildQuery(params(t, qs))["highlight"]; ok {
			t.Errorf("%q must not carry a highlight block", qs)
		}
	}
	// A query that is only whitespace is not a text query.
	if _, ok := BuildQuery(params(t, "q=+++"))["highlight"]; ok {
		t.Error("a whitespace-only q must not carry a highlight block")
	}
}

// The four branch names are a published contract: ES echoes them per hit as
// matched_queries, /api/explain scores them one at a time, and the grid renders
// them as badges. Branch.Name and the clause's own _name must never diverge.
func TestBranchesAreNamed(t *testing.T) {
	branches := Branches("Pikuchu")
	want := []string{"exact", "prefix", "fuzzy-name", "text"}
	if len(branches) != len(want) {
		t.Fatalf("got %d branches, want %d", len(branches), len(want))
	}
	for i, b := range branches {
		if b.Name != want[i] {
			t.Errorf("branch %d name = %q, want %q", i, b.Name, want[i])
		}
		if got := branchName(t, b.Query); got != b.Name {
			t.Errorf("branch %q carries _name %q", b.Name, got)
		}
	}
	// Every should clause of a text query is a named branch, in order.
	should := BuildQuery(params(t, "q=Pikuchu"))["query"].(map[string]any)["bool"].(map[string]any)["should"].([]any)
	if len(should) != len(branches) {
		t.Fatalf("should has %d clauses, want %d", len(should), len(branches))
	}
	for i, clause := range should {
		if got := branchName(t, clause.(map[string]any)); got != want[i] {
			t.Errorf("should[%d] _name = %q, want %q", i, got, want[i])
		}
	}
}

// branchName digs out the single _name key wherever it sits in a clause.
func branchName(t *testing.T, clause map[string]any) string {
	t.Helper()
	for _, v := range clause {
		options, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := options["_name"].(string); ok {
			return name
		}
		// term/match nest one level deeper: {"term": {"name.kw": {…}}}.
		for _, inner := range options {
			if opts, ok := inner.(map[string]any); ok {
				if name, ok := opts["_name"].(string); ok {
					return name
				}
			}
		}
	}
	t.Fatalf("clause carries no _name: %v", clause)
	return ""
}

// Filters must land in post_filter — never in the query — so aggregations keep
// the text-query scope and hits stay fully filtered.
func TestBuildQueryPostFilter(t *testing.T) {
	p := params(t, "q=surge&supertype=pokemon&types=Lightning,Water&rarity=Rare&series=Base&set=base1&hp_min=50&hp_max=120&page=3")
	body := BuildQuery(p)
	got := canonV(t, body["post_filter"])
	want := canonS(t, `{"bool": {"filter": [
	  {"term": {"supertype": "Pokémon"}},
	  {"terms": {"types": ["Lightning", "Water"]}},
	  {"terms": {"rarity": ["Rare"]}},
	  {"terms": {"set_series": ["Base"]}},
	  {"term": {"set_id": "base1"}},
	  {"range": {"hp": {"gte": 50, "lte": 120}}}
	]}}`)
	if got != want {
		t.Errorf("post_filter\n got %s\nwant %s", got, want)
	}
	if _, ok := body["query"].(map[string]any)["bool"].(map[string]any)["filter"]; ok {
		t.Error("query.bool must not carry filters once post_filter owns them")
	}
	if body["from"] != 48 || body["size"] != PageSize {
		t.Errorf("paging: from=%v size=%v", body["from"], body["size"])
	}
}

// A non-default page_size drives both size and the from offset.
func TestBuildQueryPageSize(t *testing.T) {
	body := BuildQuery(params(t, "q=eevee&page_size=10&page=3"))
	if body["size"] != 10 || body["from"] != 20 {
		t.Errorf("paging: from=%v size=%v, want from=20 size=10", body["from"], body["size"])
	}
	// Page one at a non-default size still omits from.
	if body := BuildQuery(params(t, "q=eevee&page_size=100")); body["size"] != 100 || body["from"] != nil {
		t.Errorf("page one: from=%v size=%v, want from=<nil> size=100", body["from"], body["size"])
	}
}

// A non-default page size opts out of the single-doc deep-link fast path —
// the caller clearly wants a real search response.
func TestExactIDLookupRequiresDefaultPageSize(t *testing.T) {
	if body := BuildQuery(params(t, "id=base1-1")); body["aggs"] != nil {
		t.Error("plain id lookup must stay on the fast path")
	}
	if body := BuildQuery(params(t, "id=base1-1&page_size=50")); body["aggs"] == nil {
		t.Error("id lookup with an explicit page_size must run the full search")
	}
}

func TestBuildQueryNoFiltersOmitsPostFilter(t *testing.T) {
	if body := BuildQuery(params(t, "q=eevee")); body["post_filter"] != nil {
		t.Errorf("post_filter must be omitted without filters, got %v", body["post_filter"])
	}
}

// Each facet aggregation's scope carries every active filter except its own,
// so a selection never collapses its own alternatives (disjunctive faceting).
func TestBuildQueryFacetScopesExcludeSelf(t *testing.T) {
	p := params(t, "supertype=pokemon&types=Fire&rarity=Rare&series=Base&set=base1&hp_min=50")
	body := BuildQuery(p)
	aggs := body["aggs"].(map[string]any)

	const supertypeF = `{"term": {"supertype": "Pokémon"}}`
	const typesF = `{"terms": {"types": ["Fire"]}}`
	const rarityF = `{"terms": {"rarity": ["Rare"]}}`
	const seriesF = `{"terms": {"set_series": ["Base"]}}`
	const setF = `{"term": {"set_id": "base1"}}`
	const hpF = `{"range": {"hp": {"gte": 50}}}`

	cases := map[string]string{
		"supertype":  `[` + typesF + `,` + rarityF + `,` + seriesF + `,` + setF + `,` + hpF + `]`,
		"types":      `[` + supertypeF + `,` + rarityF + `,` + seriesF + `,` + setF + `,` + hpF + `]`,
		"rarity":     `[` + supertypeF + `,` + typesF + `,` + seriesF + `,` + setF + `,` + hpF + `]`,
		"set_series": `[` + supertypeF + `,` + typesF + `,` + rarityF + `,` + setF + `,` + hpF + `]`,
		"sets":       `[` + supertypeF + `,` + typesF + `,` + rarityF + `,` + seriesF + `,` + hpF + `]`,
	}
	for name, wantRaw := range cases {
		agg := aggs[name].(map[string]any)
		scope := agg["filter"].(map[string]any)["bool"].(map[string]any)["filter"]
		if got, want := canonV(t, scope), canonS(t, wantRaw); got != want {
			t.Errorf("%s scope\n got %s\nwant %s", name, got, want)
		}
	}
	if _, ok := aggs["set_catalog"]; ok {
		t.Error("hot search DSL must not rebuild the static set catalog")
	}
}

func TestBuildQueryIDFilter(t *testing.T) {
	body := BuildQuery(params(t, "id=cel25c-17_A"))
	got := canonV(t, body)
	want := canonS(t, `{"size": 1, "query": {"term": {"id": "cel25c-17_A"}}}`)
	if got != want {
		t.Errorf("id fast path\n got %s\nwant %s", got, want)
	}

	// Combining ID with another constraint must keep the full search/facet path.
	combined := BuildQuery(params(t, "id=cel25c-17_A&types=Fire"))
	if combined["aggs"] == nil || combined["post_filter"] == nil {
		t.Errorf("combined ID query must keep full behavior: %v", combined)
	}
}

func TestBuildSetCatalogQuery(t *testing.T) {
	got := canonV(t, BuildSetCatalogQuery())
	want := canonS(t, `{
	  "track_total_hits": false, "size": 0,
	  "aggs": {"set_catalog": {
	    "terms": {"field": "set_id", "size": 200},
	    "aggs": {"identity": {"top_hits": {"size": 1,
	      "_source": {"includes": ["set_name", "release_date"]}}}}
	  }}
	}`)
	if got != want {
		t.Errorf("catalog DSL\n got %s\nwant %s", got, want)
	}
}

func TestBuildQueryHPRangeOpenEnded(t *testing.T) {
	body := BuildQuery(params(t, "hp_min=200"))
	got := canonV(t, body["post_filter"])
	want := canonS(t, `{"bool": {"filter": [{"range": {"hp": {"gte": 200}}}]}}`)
	if got != want {
		t.Errorf("open range\n got %s\nwant %s", got, want)
	}
}

func TestBuildQuerySorts(t *testing.T) {
	cases := map[string]string{
		"sort=newest":          `[{"release_date": "desc"}, {"id": "asc"}]`,
		"sort=oldest":          `[{"release_date": "asc"}, {"id": "asc"}]`,
		"sort=hp":              `[{"hp": {"missing": "_last", "order": "desc"}}, {"id": "asc"}]`,
		"sort=hp&order=asc":    `[{"hp": {"missing": "_last", "order": "asc"}}, {"id": "asc"}]`,
		"sort=name":            `[{"name.kw": "asc"}, {"id": "asc"}]`,
		"sort=name&order=desc": `[{"name.kw": "desc"}, {"id": "asc"}]`,
		"q=x":                  `["_score", {"id": "asc"}]`,
	}
	for qs, wantRaw := range cases {
		got := canonV(t, BuildQuery(params(t, qs))["sort"])
		if want := canonS(t, wantRaw); got != want {
			t.Errorf("%s sort\n got %s\nwant %s", qs, got, want)
		}
	}
}

func TestBuildSuggest(t *testing.T) {
	got := canonV(t, BuildSuggest("alak", false))
	want := canonS(t, `{"suggest": {"card": {"prefix": "alak",
	  "completion": {"field": "name.suggest", "size": 8, "skip_duplicates": true}}}}`)
	if got != want {
		t.Errorf("plain suggest\n got %s\nwant %s", got, want)
	}
	gotF := canonV(t, BuildSuggest("alak", true))
	wantF := canonS(t, `{"suggest": {"card": {"prefix": "alak",
	  "completion": {"field": "name.suggest", "size": 8, "skip_duplicates": true,
	    "fuzzy": {"fuzziness": "AUTO"}}}}}`)
	if gotF != wantF {
		t.Errorf("fuzzy suggest\n got %s\nwant %s", gotF, wantF)
	}
}

// The ranked suggest body, byte for byte. The count is SuggestSize and the
// ordering is doc_count descending: those two together are what make the
// endpoint return the same eight it always did, in the order a reader means
// rather than in alphabetical order. The top_hits sub-aggregation is not
// decoration either — name.kw is lowercase-normalized, so the bucket key
// cannot be shown and the display casing has to come from _source.
func TestBuildSuggestByPrintCount(t *testing.T) {
	got := canonV(t, BuildSuggestByPrintCount("alak"))
	want := canonS(t, `{
	  "track_total_hits": false,
	  "size": 0,
	  "query": {"multi_match": {"query": "alak", "type": "bool_prefix",
	    "fields": ["name.sayt", "name.sayt._2gram", "name.sayt._3gram"]}},
	  "aggs": {"names": {
	    "terms": {"field": "name.kw", "size": 8, "order": {"_count": "desc"}},
	    "aggs": {"display": {"top_hits": {
	      "size": 1, "_source": {"includes": ["name"]}}}}
	  }}
	}`)
	if got != want {
		t.Errorf("ranked suggest\n got %s\nwant %s", got, want)
	}
}

// The corpus's own shape, in one request: two numeric distributions (prints
// per year, HP bands), the facet breakdowns, and the maximum HP. Nothing in it
// depends on a request, which is what makes it cacheable for the process
// lifetime.
//
// The breakdown aggregations are asserted here with the field names and terms
// sizes the facet registry carries, because that is where BuildStatsQuery
// reads them from — a stats chart must not be able to aggregate a different
// field, or a shorter terms size, than the filter rail describing the same
// corpus. supertype therefore has no explicit size: the registry does not give
// it one (three values sit well inside the ES default).
func TestBuildStatsQuery(t *testing.T) {
	got := canonV(t, BuildStatsQuery())
	want := canonS(t, `{
	  "track_total_hits": true, "size": 0,
	  "aggs": {
	    "per_year": {"date_histogram": {"field": "release_date",
	      "calendar_interval": "year", "min_doc_count": 0}},
	    "hp": {"histogram": {"field": "hp", "interval": 30, "min_doc_count": 0}},
	    "max_hp": {"max": {"field": "hp"}},
	    "types":     {"terms": {"field": "types", "size": 11}},
	    "supertype": {"terms": {"field": "supertype"}},
	    "rarity":    {"terms": {"field": "rarity", "size": 100}},
	    "series":    {"terms": {"field": "set_series", "size": 20}}
	  }
	}`)
	if got != want {
		t.Errorf("stats DSL\n got %s\nwant %s", got, want)
	}
}

// Every stats breakdown must resolve to a registered facet, and must aggregate
// byte-for-byte what that facet aggregates. This is the drift alarm: renaming a
// facet or lowering its terms size can no longer leave the Stats view quietly
// describing a different corpus than the filter rail.
func TestStatsBreakdownsFollowFacetRegistry(t *testing.T) {
	aggs := BuildStatsQuery()["aggs"].(map[string]any)
	for _, sf := range statsFacets {
		def, ok := facetByName(sf.Facet)
		if !ok {
			t.Errorf("stats breakdown %q names facet %q, which is not registered", sf.Key, sf.Facet)
			continue
		}
		got := canonV(t, aggs[sf.Key])
		want := canonV(t, map[string]any{"terms": termsOf(def)})
		if got != want {
			t.Errorf("stats breakdown %q\n got %s\nwant %s", sf.Key, got, want)
		}
	}
	// The sets facet is deliberately not charted; 173 bars is a list.
	if _, ok := aggs[SetsFacet]; ok {
		t.Error("stats must not aggregate the sets facet")
	}
}

func TestBuildDidYouMean(t *testing.T) {
	got := canonV(t, BuildDidYouMean("charzard ex"))
	want := canonS(t, `{
	  "size": 0,
	  "suggest": {"dym": {
	    "text": "charzard ex",
	    "term": {"field": "name", "suggest_mode": "popular", "size": 1}
	  }}
	}`)
	if got != want {
		t.Errorf("did-you-mean DSL\n got %s\nwant %s", got, want)
	}
}
