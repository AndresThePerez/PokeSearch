package search

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
)

// strictFields are the only parameters allowed to produce a FieldError. If a
// lenient one ever starts rejecting, /api/search begins returning 400s to
// requests the contract promises to accept — Courier's curated collections
// assert on exactly this split.
var strictFields = []string{"sort", "order", "supertype", "hp_min", "hp_max", "page", "page_size"}

// FuzzParseParams asserts the invariants every downstream consumer relies on:
// BuildQuery reads Page/PageSize straight into from/size (an out-of-range pair
// is an ES 400 or a deep-paging window violation), the handler switches on
// Sort, and the facet filters switch on Supertype and Types.
func FuzzParseParams(f *testing.F) {
	for _, seed := range []string{
		"",
		"q=pikachu",
		"sort=bogus", "sort=hp&order=sideways", "supertype=wizard",
		"hp_min=abc", "hp_max=1e3", "page=two", "page_size=lots",
		"q=pikachu&types=Wizard,Fire", "page=999999", "utm_source=x",
		"sort=hp&order=desc", "supertype=POKEMON", "hp_min=10&hp_max=99999",
		"page_size=100&page=999999", "page_size=0", "page_size=-5",
		"types=fire,FIRE,Fire", "rarity=Rare,,Common", "series= Base ",
		"id=base1-1", "debug=1", "q=%C3%B4&sort=name&order=asc",
		"page=9223372036854775808", "hp_min=-9223372036854775809",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// ParseQuery returns everything it could decode alongside its error;
		// ParseParams has to survive that partial value either way.
		v, _ := url.ParseQuery(raw)
		p, ferrs := ParseParams(v)

		if p.PageSize < MinPageSize || p.PageSize > MaxPageSize {
			t.Fatalf("%q: PageSize = %d, want %d..%d", raw, p.PageSize, MinPageSize, MaxPageSize)
		}
		if p.Page < 1 || p.Page > MaxPageFor(p.PageSize) {
			t.Fatalf("%q: Page = %d at PageSize %d, want 1..%d",
				raw, p.Page, p.PageSize, MaxPageFor(p.PageSize))
		}
		switch p.Sort {
		case "relevance", "newest", "oldest", "hp", "name":
		default:
			t.Fatalf("%q: Sort = %q is not a sort the handler knows", raw, p.Sort)
		}
		// Relevance ordering over a match-all is meaningless: every _score is
		// identical, so the result order would be arbitrary but stable-looking.
		if p.Sort == "relevance" && p.Q == "" {
			t.Fatalf("%q: relevance sort with no query", raw)
		}
		switch p.Order {
		case "", "asc", "desc":
		default:
			t.Fatalf("%q: Order = %q", raw, p.Order)
		}
		if p.Supertype != "" && !slices.Contains([]string{"Pokémon", "Trainer", "Energy"}, p.Supertype) {
			t.Fatalf("%q: Supertype = %q is not canonical", raw, p.Supertype)
		}
		for i, ty := range p.Types {
			if !slices.Contains(CanonicalTypes, ty) {
				t.Fatalf("%q: Types[%d] = %q is not canonical", raw, i, ty)
			}
			if slices.Index(p.Types, ty) != i {
				t.Fatalf("%q: Types = %v repeats %q", raw, p.Types, ty)
			}
		}
		for _, fe := range ferrs {
			if !slices.Contains(strictFields, fe.Field) {
				t.Fatalf("%q: field error on lenient parameter %q", raw, fe.Field)
			}
			if fe.Message == "" {
				t.Fatalf("%q: field error on %q carries no message", raw, fe.Field)
			}
		}

		// Whatever came out must still produce sendable DSL.
		if _, err := json.Marshal(BuildQuery(p)); err != nil {
			t.Fatalf("%q: BuildQuery is not marshalable: %v", raw, err)
		}
		if _, err := json.Marshal(BuildSuggest(p.Q, true)); err != nil {
			t.Fatalf("%q: BuildSuggest is not marshalable: %v", raw, err)
		}
	})
}
