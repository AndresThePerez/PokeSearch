package search

import (
	"net/url"
	"reflect"
	"testing"
)

func intp(n int) *int { return &n }

func TestParseParams(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Params
	}{
		{"empty is browse mode", "", Params{Sort: "newest", Page: 1}},
		{"q trimmed, relevance default", "q=+alakazam+", Params{Q: "alakazam", Sort: "relevance", Page: 1}},
		{"id passthrough", "id=cel25c-17_A", Params{ID: "cel25c-17_A", Sort: "newest", Page: 1}},
		{"supertype canonicalized", "supertype=POKEMON", Params{Supertype: "Pokémon", Sort: "newest", Page: 1}},
		{"supertype invalid dropped", "supertype=dragon", Params{Sort: "newest", Page: 1}},
		{"types canonicalized, unknown dropped, deduped",
			"types=lightning,FIRE,ghost,fire", Params{Types: []string{"Lightning", "Fire"}, Sort: "newest", Page: 1}},
		{"rarity passthrough with empties dropped",
			"rarity=Rare+Holo,,Classic+Collection", Params{Rarity: []string{"Rare Holo", "Classic Collection"}, Sort: "newest", Page: 1}},
		{"series passthrough", "series=Sword+%26+Shield", Params{Series: []string{"Sword & Shield"}, Sort: "newest", Page: 1}},
		{"hp range parsed", "hp_min=50&hp_max=120", Params{HPMin: intp(50), HPMax: intp(120), Sort: "newest", Page: 1}},
		{"hp non-numeric dropped", "hp_min=abc&hp_max=", Params{Sort: "newest", Page: 1}},
		{"sort whitelist, invalid falls back", "q=x&sort=bogus", Params{Q: "x", Sort: "relevance", Page: 1}},
		{"hp sort defaults desc", "sort=hp", Params{Sort: "hp", Order: "desc", Page: 1}},
		{"hp sort explicit asc", "sort=hp&order=asc", Params{Sort: "hp", Order: "asc", Page: 1}},
		{"name sort defaults asc", "sort=name", Params{Sort: "name", Order: "asc", Page: 1}},
		{"order dropped for relevance", "q=x&order=desc", Params{Q: "x", Sort: "relevance", Page: 1}},
		{"page parsed", "page=3", Params{Sort: "newest", Page: 3}},
		{"page floor 1", "page=0", Params{Sort: "newest", Page: 1}},
		{"page cap 400", "page=999", Params{Sort: "newest", Page: 400}},
		{"debug", "debug=1", Params{Sort: "newest", Page: 1, Debug: true}},
		{"debug wrong value", "debug=true", Params{Sort: "newest", Page: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			got := ParseParams(v)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseParams(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}
