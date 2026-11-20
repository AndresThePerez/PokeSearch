package search

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

func TestParseParams(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Params
	}{
		{"empty is browse mode", "", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"q trimmed, relevance default", "q=+alakazam+", Params{Q: "alakazam", Sort: "relevance", Page: 1, PageSize: 24}},
		{"id passthrough", "id=cel25c-17_A", Params{ID: "cel25c-17_A", Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype canonicalized", "supertype=POKEMON", Params{Supertype: "Pokémon", Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype invalid dropped", "supertype=dragon", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"types canonicalized, unknown dropped, deduped",
			"types=lightning,FIRE,ghost,fire", Params{Types: []string{"Lightning", "Fire"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"rarity passthrough with empties dropped",
			"rarity=Rare+Holo,,Classic+Collection", Params{Rarity: []string{"Rare Holo", "Classic Collection"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"series passthrough", "series=Sword+%26+Shield", Params{Series: []string{"Sword & Shield"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"set id passthrough", "set=sv3pt5", Params{SetID: "sv3pt5", Sort: "newest", Page: 1, PageSize: 24}},
		{"hp range parsed", "hp_min=50&hp_max=120", Params{HPMin: intp(50), HPMax: intp(120), Sort: "newest", Page: 1, PageSize: 24}},
		{"hp non-numeric dropped", "hp_min=abc&hp_max=", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"sort whitelist, invalid falls back", "q=x&sort=bogus", Params{Q: "x", Sort: "relevance", Page: 1, PageSize: 24}},
		{"relevance meaningless without q, normalized", "sort=relevance", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"hp sort defaults desc", "sort=hp", Params{Sort: "hp", Order: "desc", Page: 1, PageSize: 24}},
		{"hp sort explicit asc", "sort=hp&order=asc", Params{Sort: "hp", Order: "asc", Page: 1, PageSize: 24}},
		{"name sort defaults asc", "sort=name", Params{Sort: "name", Order: "asc", Page: 1, PageSize: 24}},
		{"order dropped for relevance", "q=x&order=desc", Params{Q: "x", Sort: "relevance", Page: 1, PageSize: 24}},
		{"page parsed", "page=3", Params{Sort: "newest", Page: 3, PageSize: 24}},
		{"page floor 1", "page=0", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"page cap 400", "page=999", Params{Sort: "newest", Page: 400, PageSize: 24}},
		{"debug", "debug=1", Params{Sort: "newest", Page: 1, PageSize: 24, Debug: true}},
		{"debug true", "debug=true", Params{Sort: "newest", Page: 1, PageSize: 24, Debug: true}},
		{"debug wrong value", "debug=yes", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"debug zero", "debug=0", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"debug false", "debug=false", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"debug upper-case true", "debug=TRUE", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype in the facet's own spelling", "supertype=Pok%C3%A9mon", Params{Supertype: "Pokémon", Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype accented, upper case", "supertype=POK%C3%89MON", Params{Supertype: "Pokémon", Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype accented, lower case, padded", "supertype=+pok%C3%A9mon+", Params{Supertype: "Pokémon", Sort: "newest", Page: 1, PageSize: 24}},
		{"sort trimmed", "sort=%20hp%20", Params{Sort: "hp", Order: "desc", Page: 1, PageSize: 24}},
		{"order trimmed", "sort=hp&order=%20asc%20", Params{Sort: "hp", Order: "asc", Page: 1, PageSize: 24}},
		{"blank sort is absent", "q=x&sort=+++", Params{Q: "x", Sort: "relevance", Page: 1, PageSize: 24}},
		{"types Steel is Metal", "types=Steel", Params{Types: []string{"Metal"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"types Normal is Colorless", "types=normal", Params{Types: []string{"Colorless"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"types alias and canonical collapse", "types=STEEL,Metal,Fire,colorless,Normal",
			Params{Types: []string{"Metal", "Fire", "Colorless"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"types alias among unknown members", "types=ghost,steel,wizard", Params{Types: []string{"Metal"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"repeated q takes the first", "q=alakazam&q=pikachu", Params{Q: "alakazam", Sort: "relevance", Page: 1, PageSize: 24}},
		{"repeated sort takes the first", "sort=hp&sort=bogus", Params{Sort: "hp", Order: "desc", Page: 1, PageSize: 24}},
		{"repeated supertype takes the first", "supertype=trainer&supertype=wizard", Params{Supertype: "Trainer", Sort: "newest", Page: 1, PageSize: 24}},
		{"repeated types takes the first", "types=Fire&types=Water", Params{Types: []string{"Fire"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"repeated page takes the first", "page=2&page=two", Params{Sort: "newest", Page: 2, PageSize: 24}},
		{"repeated debug takes the first", "debug=0&debug=1", Params{Sort: "newest", Page: 1, PageSize: 24}},
		// The last page the response advertises (ceil of 9,600 / size) must be
		// requestable: 9600/7 = 1371.4, so 1372 pages.
		{"page cap is the advertised last page", "page_size=7&page=999999", Params{Sort: "newest", Page: 1372, PageSize: 7}},
		{"page_size parsed", "page_size=10", Params{Sort: "newest", Page: 1, PageSize: 10}},
		{"page_size clamped to max", "page_size=500", Params{Sort: "newest", Page: 1, PageSize: 100}},
		{"page_size clamped to min", "page_size=0", Params{Sort: "newest", Page: 1, PageSize: 1}},
		// The reachable window is 9,600 docs at every size, so the page cap
		// moves with page_size: 9600/100 = 96.
		{"page cap respects page_size", "page_size=100&page=999", Params{Sort: "newest", Page: 96, PageSize: 100}},
		{"page cap at size 1", "page_size=1&page=999999", Params{Sort: "newest", Page: 9600, PageSize: 1}},
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
		{"padded bad sort", "sort=+bogus+", "sort"},
		{"padded bad order", "sort=hp&order=+sideways+", "order"},
		{"q over the character limit", "q=" + strings.Repeat("a", MaxQueryRunes+1), "q"},
		{"q over the term limit", "q=" + strings.TrimSuffix(strings.Repeat("a+", MaxQueryTerms+1), "+"), "q"},
		{"repeated sort, bad first", "sort=bogus&sort=hp", "sort"},
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
		"supertype=Pok%C3%A9mon", "sort=%20hp%20&order=%20desc%20", "types=Steel,Normal",
		"debug=true", "q=" + strings.Repeat("a", MaxQueryRunes),
		"q=" + strings.TrimSuffix(strings.Repeat("a+", MaxQueryTerms), "+"),
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
