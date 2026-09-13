package esindex

import (
	"encoding/json"
	"testing"
)

// parse is the sibling of TestMappingIsValid's json.Unmarshal: a create-index
// body that does not parse cannot be sent, and a test that needs a cluster to
// notice that is a slow way to learn it.
func parse(t *testing.T, name, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v", name, err)
	}
	return m
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}

func filterList(t *testing.T, m map[string]any, path ...string) []string {
	t.Helper()
	raw, ok := dig(t, m, path...).([]any)
	if !ok {
		t.Fatalf("path %v: not a list", path)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("path %v: %v is not a string", path, v)
		}
		out = append(out, s)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestMappingV2IsValid(t *testing.T) {
	if IndexNameV2 != "cards_v2" {
		t.Errorf("IndexNameV2 = %q", IndexNameV2)
	}
	if IndexNameV2 == IndexName {
		t.Fatalf("the shadow index and the served index share the name %q", IndexName)
	}
	v2 := parse(t, "MappingV2", MappingV2)

	if got := dig(t, v2, "settings", "number_of_shards"); got != float64(1) {
		t.Errorf("shards = %v", got)
	}
	if got := dig(t, v2, "settings", "refresh_interval"); got != "30s" {
		t.Errorf("refresh_interval = %v", got)
	}
	v1 := parse(t, "Mapping", Mapping)
	for _, key := range []string{"number_of_shards", "number_of_replicas", "refresh_interval"} {
		if got, want := dig(t, v2, "settings", key), dig(t, v1, "settings", key); got != want {
			t.Errorf("settings.%s = %v, Mapping has %v", key, got, want)
		}
	}
}

// The chain is the whole point of the index, so it is asserted by name rather
// than by "an analyzer exists": asciifolding is what closes the accent gap,
// and a filter list that quietly lost it would still create an index, still
// reindex 20k documents and still pass a test that only counted filters.
func TestMappingV2AnalyzerFolds(t *testing.T) {
	v2 := parse(t, "MappingV2", MappingV2)

	if got := dig(t, v2, "mappings", "properties", "name", "analyzer"); got != "card_name" {
		t.Errorf("name analyzer = %v", got)
	}
	chain := filterList(t, v2, "settings", "analysis", "analyzer", "card_name", "filter")
	for _, want := range []string{"lowercase", "asciifolding", "card_suffix_synonyms", "name_delimiter"} {
		if !contains(chain, want) {
			t.Errorf("card_name filters %v do not include %q", chain, want)
		}
	}
	// flatten_graph has to close the chain: word_delimiter_graph produces a
	// token graph, and an index analyzer that leaves one unflattened writes
	// terms at positions the phrase queries cannot read back.
	if len(chain) == 0 || chain[len(chain)-1] != "flatten_graph" {
		t.Errorf("card_name filters %v must end in flatten_graph", chain)
	}
	// The synonym filter analyzes its own rules with the filters ahead of it,
	// and word_delimiter_graph cannot do that. Create-index rejects the wrong
	// order outright, so the order is pinned here where it costs nothing.
	syn, delim := -1, -1
	for i, f := range chain {
		switch f {
		case "card_suffix_synonyms":
			syn = i
		case "name_delimiter":
			delim = i
		}
	}
	if syn < 0 || delim < 0 || syn > delim {
		t.Errorf("card_name filters %v must list card_suffix_synonyms before name_delimiter", chain)
	}

	if got := dig(t, v2, "settings", "analysis", "filter", "name_delimiter", "type"); got != "word_delimiter_graph" {
		t.Errorf("name_delimiter type = %v", got)
	}
	if got := dig(t, v2, "settings", "analysis", "filter", "card_suffix_synonyms", "type"); got != "synonym" {
		t.Errorf("card_suffix_synonyms type = %v", got)
	}

	// The keyword side folds too, or name.kw, artist.kw, set_name.kw and
	// evolves_from.kw would still answer an accented query only.
	norm := filterList(t, v2, "settings", "analysis", "normalizer", "lc", "filter")
	if !contains(norm, "asciifolding") {
		t.Errorf("lc normalizer filters %v do not include asciifolding", norm)
	}
	if !contains(norm, "lowercase") {
		t.Errorf("lc normalizer filters %v do not include lowercase", norm)
	}
}

// MappingV2 is Mapping plus an analysis chain and nothing else. Undo the three
// changes and the two bodies have to be the same document: that is what makes
// "the served mapping is unchanged" a claim a reader can check rather than a
// promise in a comment. It also catches the quiet failure mode of a copied
// mapping — an index:false or an enabled:false dropped in transcription.
func TestMappingV2DiffersFromMappingInExactlyThreeWays(t *testing.T) {
	v1 := parse(t, "Mapping", Mapping)
	v2 := parse(t, "MappingV2", MappingV2)

	// 1 and 2: the analysis block, which carries both the new analyzer with
	// its synonym filter and the folded normalizer.
	v2Settings, ok := dig(t, v2, "settings").(map[string]any)
	if !ok {
		t.Fatal("MappingV2 settings is not an object")
	}
	v2Settings["analysis"] = dig(t, v1, "settings", "analysis")

	// 3: the analyzer reference on the name field.
	name, ok := dig(t, v2, "mappings", "properties", "name").(map[string]any)
	if !ok {
		t.Fatal("MappingV2 name field is not an object")
	}
	if _, ok := name["analyzer"]; !ok {
		t.Fatal("MappingV2 name field carries no analyzer")
	}
	delete(name, "analyzer")

	if got, want := canonical(t, v2), canonical(t, v1); got != want {
		t.Errorf("MappingV2 with its analysis changes undone is not Mapping\n got %s\nwant %s", got, want)
	}
}
