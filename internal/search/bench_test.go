package search

import (
	"encoding/json"
	"net/url"
	"testing"
)

// BuildQuery runs on every keystroke that survives the debounce, so its cost
// sits directly in the 250ms browser budget. These benchmarks exist to catch a
// regression — a nested loop, an accidental deep copy — before profiling the
// cluster for something the app did to itself.
func BenchmarkBuildQuery(b *testing.B) {
	cases := map[string]string{
		"browse":   "",
		"fulltext": "q=charizard",
		"filtered": "q=surge&supertype=pokemon&types=Lightning,Water&rarity=Rare&series=Base&set=base1&hp_min=50&hp_max=120&page=3",
		"exact-id": "id=base1-4",
	}
	for name, qs := range cases {
		p := benchParams(b, qs)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = BuildQuery(p)
			}
		})
	}
}

// The DSL is marshalled on every request too, and the aggregation block is the
// bulk of those bytes — worth measuring alongside the builder itself.
func BenchmarkBuildQueryMarshal(b *testing.B) {
	p := benchParams(b, "q=surge&supertype=pokemon&types=Lightning,Water&rarity=Rare")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(BuildQuery(p)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseParams(b *testing.B) {
	v, err := url.ParseQuery("q=surge&supertype=pokemon&types=Lightning,Water&rarity=Rare&series=Base&set=base1&hp_min=50&hp_max=120&page=3&page_size=48")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = ParseParams(v)
	}
}

func benchParams(b *testing.B, qs string) Params {
	b.Helper()
	v, err := url.ParseQuery(qs)
	if err != nil {
		b.Fatal(err)
	}
	p, errs := ParseParams(v)
	if len(errs) > 0 {
		b.Fatalf("benchmark query %q is invalid: %+v", qs, errs)
	}
	return p
}
