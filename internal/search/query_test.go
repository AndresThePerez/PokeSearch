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

const aggsJSON = `{
  "supertype":  {"terms": {"field": "supertype",  "size": 3}},
  "types":      {"terms": {"field": "types",      "size": 11}},
  "rarity":     {"terms": {"field": "rarity",     "size": 30}},
  "set_series": {"terms": {"field": "set_series", "size": 20}}
}`

func TestBuildQueryBrowseMode(t *testing.T) {
	got := canonV(t, BuildQuery(params(t, "")))
	want := canonS(t, `{
	  "track_total_hits": true, "from": 0, "size": 24,
	  "query": {"bool": {"must": [{"match_all": {}}]}},
	  "sort": [{"release_date": "desc"}, {"id": "asc"}],
	  "aggs": `+aggsJSON+`}`)
	if got != want {
		t.Errorf("browse DSL\n got %s\nwant %s", got, want)
	}
}

func TestBuildQueryFullText(t *testing.T) {
	got := canonV(t, BuildQuery(params(t, "q=Pikuchu")))
	want := canonS(t, `{
	  "track_total_hits": true, "from": 0, "size": 24,
	  "query": {"bool": {
	    "should": [
	      {"term": {"name.kw": {"value": "pikuchu", "boost": 8}}},
	      {"multi_match": {"query": "Pikuchu", "type": "bool_prefix",
	        "fields": ["name.sayt", "name.sayt._2gram", "name.sayt._3gram"], "boost": 4}},
	      {"match": {"name": {"query": "Pikuchu", "fuzziness": "AUTO", "boost": 3}}},
	      {"multi_match": {"query": "Pikuchu", "type": "best_fields", "fuzziness": "AUTO",
	        "fields": ["attacks.name^2", "abilities.name^2", "attacks.text", "abilities.text",
	                   "flavor_text", "set_name^1.5", "artist"]}}
	    ],
	    "minimum_should_match": 1
	  }},
	  "sort": ["_score", {"id": "asc"}],
	  "aggs": `+aggsJSON+`}`)
	if got != want {
		t.Errorf("full-text DSL\n got %s\nwant %s", got, want)
	}
}

func TestBuildQueryFiltersAndPaging(t *testing.T) {
	p := params(t, "q=surge&supertype=pokemon&types=Lightning,Water&rarity=Rare&series=Base&hp_min=50&hp_max=120&page=3")
	body := BuildQuery(p)
	got := canonV(t, body["query"].(map[string]any)["bool"].(map[string]any)["filter"])
	want := canonS(t, `[
	  {"term": {"supertype": "Pokémon"}},
	  {"terms": {"types": ["Lightning", "Water"]}},
	  {"terms": {"rarity": ["Rare"]}},
	  {"terms": {"set_series": ["Base"]}},
	  {"range": {"hp": {"gte": 50, "lte": 120}}}
	]`)
	if got != want {
		t.Errorf("filters\n got %s\nwant %s", got, want)
	}
	if body["from"] != 48 || body["size"] != PageSize {
		t.Errorf("paging: from=%v size=%v", body["from"], body["size"])
	}
}

func TestBuildQueryIDFilter(t *testing.T) {
	body := BuildQuery(params(t, "id=cel25c-17_A"))
	got := canonV(t, body["query"].(map[string]any)["bool"].(map[string]any)["filter"])
	want := canonS(t, `[{"term": {"id": "cel25c-17_A"}}]`)
	if got != want {
		t.Errorf("id filter\n got %s\nwant %s", got, want)
	}
}

func TestBuildQueryHPRangeOpenEnded(t *testing.T) {
	body := BuildQuery(params(t, "hp_min=200"))
	got := canonV(t, body["query"].(map[string]any)["bool"].(map[string]any)["filter"])
	want := canonS(t, `[{"range": {"hp": {"gte": 200}}}]`)
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
