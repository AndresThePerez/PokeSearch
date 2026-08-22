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
		{"set id passthrough", "set=sv3pt5", Params{SetID: "sv3pt5", Sort: "newest", Page: 1}},
		{"hp range parsed", "hp_min=50&hp_max=120", Params{HPMin: intp(50), HPMax: intp(120), Sort: "newest", Page: 1}},
		{"hp non-numeric dropped", "hp_min=abc&hp_max=", Params{Sort: "newest", Page: 1}},
		{"sort whitelist, invalid falls back", "q=x&sort=bogus", Params{Q: "x", Sort: "relevance", Page: 1}},
		{"relevance meaningless without q, normalized", "sort=relevance", Params{Sort: "newest", Page: 1}},
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
			got, _ := ParseParams(v)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseParams(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseParamsFieldErrors locks the D1 strict/lenient split: a strict field
// with a non-empty, invalid value is a client error; comma-list members,
// unknown keys, and out-of-range numerics stay lenient (clamping is a
// contract, an alphabetic page is a typo).
func TestParseParamsFieldErrors(t *testing.T) {
	cases := []struct{ name, query, field string }{
		{"bad sort", "sort=bogus", "sort"},
		{"bad order", "sort=hp&order=sideways", "order"},
		{"bad supertype", "supertype=wizard", "supertype"},
		{"bad hp_min", "hp_min=abc", "hp_min"},
		{"bad hp_max", "hp_max=1e3", "hp_max"},
		{"bad page", "page=two", "page"},
		{"bad page_size", "page_size=lots", "page_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			_, errs := ParseParams(v)
			if len(errs) != 1 || errs[0].Field != tc.field {
				t.Fatalf("errs = %+v, want exactly one error on %q", errs, tc.field)
			}
			if errs[0].Message == "" {
				t.Errorf("field error on %q must carry a message", tc.field)
			}
		})
	}

	for _, ok := range []string{
		"", "q=pikachu&types=Wizard,Fire", "page=999999", "utm_source=x",
		"sort=hp&order=desc", "supertype=POKEMON", "hp_min=10&hp_max=99999",
	} {
		v, err := url.ParseQuery(ok)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := ParseParams(v); len(errs) != 0 {
			t.Errorf("query %q: unexpected errs %+v", ok, errs)
		}
	}
}
