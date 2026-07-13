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
	return ParseParams(v)
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
	      {"term": {"name.kw": {"value": "pikuchu", "boost": 8}}},
	      {"multi_match": {"query": "Pikuchu", "type": "bool_prefix",
	        "fields": ["name.sayt", "name.sayt._2gram", "name.sayt._3gram"], "boost": 4}},
	      {"match": {"name": {"query": "Pikuchu", "fuzziness": "AUTO", "boost": 3}}},
	      {"multi_match": {"query": "Pikuchu", "type": "best_fields", "fuzziness": "AUTO",
	        "fields": ["attacks.name^2", "abilities.name^2", "attacks.text", "abilities.text",
	                   "flavor_text", "set_name^1.5", "artist"]}}
	    ]
	  }},
	  "sort": ["_score", {"id": "asc"}],
	  "aggs": `+noFilterAggsJSON+`}`)
	if got != want {
		t.Errorf("full-text DSL\n got %s\nwant %s", got, want)
	}
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
