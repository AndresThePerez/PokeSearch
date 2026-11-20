package search

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The rarity list is the corpus's own: 44 distinct values, read from a terms
// aggregation on the rarity field. A value is listed once, spelled exactly as
// the index stores it, because the terms filter matches bytes.
func TestCanonicalRaritiesAreTheCorpusValues(t *testing.T) {
	if len(CanonicalRarities) != 44 {
		t.Fatalf("CanonicalRarities has %d values, want 44", len(CanonicalRarities))
	}
	seen := map[string]bool{}
	for _, r := range CanonicalRarities {
		if seen[r] {
			t.Errorf("%q is listed twice", r)
		}
		seen[r] = true
		if r != strings.TrimSpace(r) || strings.Join(strings.Fields(r), " ") != r {
			t.Errorf("%q carries stray whitespace", r)
		}
	}
	for _, r := range []string{"Rare Holo", "Rare Holo EX", "Rare Holo ex", "Common", "MEGA_ATTACK_RARE", "Holo Rare VSTAR"} {
		if !seen[r] {
			t.Errorf("%q is missing", r)
		}
	}
}

func TestParseParamsRarityMapsOtherSpellings(t *testing.T) {
	cases := []struct {
		name string
		in   []string // raw rarity parameter values, one per rarity= key
		want []string
	}{
		{"exact value untouched", []string{"Rare Holo"}, []string{"Rare Holo"}},
		{"lower case", []string{"rare holo"}, []string{"Rare Holo"}},
		{"upper case", []string{"RARE HOLO"}, []string{"Rare Holo"}},
		{"mixed case, one word", []string{"cOMMON"}, []string{"Common"}},
		{"outer whitespace", []string{"  rare holo\t"}, []string{"Rare Holo"}},
		{"inner run of spaces", []string{"Rare   Holo"}, []string{"Rare Holo"}},
		{"inner tab", []string{"rare\tholo"}, []string{"Rare Holo"}},
		{"case and spacing together", []string{" special  illustration   RARE "}, []string{"Special Illustration Rare"}},
		{"underscore value by case", []string{"mega_attack_rare"}, []string{"MEGA_ATTACK_RARE"}},
		{"punctuated value by case", []string{"rare holo lv.x"}, []string{"Rare Holo LV.X"}},
		{"ambiguous exact upper kept", []string{"Rare Holo EX"}, []string{"Rare Holo EX"}},
		{"ambiguous exact lower kept", []string{"Rare Holo ex"}, []string{"Rare Holo ex"}},
		{"ambiguous fold passed through verbatim", []string{"rare holo ex"}, []string{"rare holo ex"}},
		{"ambiguous fold with spacing, trimmed only", []string{" RARE  HOLO  EX "}, []string{"RARE  HOLO  EX"}},
		{"unknown passed through verbatim", []string{"Bogus"}, []string{"Bogus"}},
		{"unknown keeps its case and inner spacing", []string{" Not  A Rarity "}, []string{"Not  A Rarity"}},
		{"near miss is not a spelling", []string{"RareHolo"}, []string{"RareHolo"}},
		{"duplicates after mapping collapse", []string{"Rare Holo,rare holo"}, []string{"Rare Holo"}},
		{"duplicates collapse, first position kept", []string{"common,Rare Holo,COMMON, rare  holo"}, []string{"Common", "Rare Holo"}},
		{"exact duplicates collapse", []string{"Rare,Rare"}, []string{"Rare"}},
		{"unknown duplicates collapse", []string{"Bogus,Bogus"}, []string{"Bogus"}},
		{"ambiguous pair kept apart", []string{"Rare Holo EX,Rare Holo ex,rare holo ex"}, []string{"Rare Holo EX", "Rare Holo ex", "rare holo ex"}},
		{"empty members dropped", []string{",, ,rare holo,,"}, []string{"Rare Holo"}},
		{"only empty members", []string{" , ,"}, nil},
		{"mixed known, unknown and ambiguous", []string{"uncommon,Bogus,rare holo ex"}, []string{"Uncommon", "Bogus", "rare holo ex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, errs := ParseParams(url.Values{"rarity": tc.in})
			if len(errs) != 0 {
				t.Fatalf("unexpected field errors %v", errs)
			}
			if !reflect.DeepEqual(p.Rarity, tc.want) {
				t.Errorf("Rarity = %q, want %q", p.Rarity, tc.want)
			}
		})
	}
}

// Another spelling of a value builds the very same request as the value
// itself: the filter clause keeps its shape and carries the stored bytes.
func TestBuildQueryRaritySpellingMatchesExactValue(t *testing.T) {
	want := canonV(t, BuildQuery(params(t, "rarity=Rare+Holo")))
	for _, qs := range []string{"rarity=rare+holo", "rarity=RARE%20%20HOLO", "rarity=+Rare+Holo+", "rarity=Rare+Holo,rare+holo"} {
		if got := canonV(t, BuildQuery(params(t, qs))); got != want {
			t.Errorf("%s\n got %s\nwant %s", qs, got, want)
		}
	}
}

// Every one of the 44 values, given exactly, builds byte-for-byte the request
// it built when the parameter was passed through verbatim.
func TestBuildQueryEveryExactRarityUnchanged(t *testing.T) {
	for _, r := range CanonicalRarities {
		for _, q := range []string{"", "pikachu"} {
			v := url.Values{"rarity": {r}}
			if q != "" {
				v.Set("q", q)
			}
			p, errs := ParseParams(v)
			if len(errs) != 0 {
				t.Fatalf("%q: unexpected field errors %v", r, errs)
			}
			verbatim := p
			verbatim.Rarity = []string{r}
			got, want := canonV(t, BuildQuery(p)), canonV(t, BuildQuery(verbatim))
			if got != want {
				t.Errorf("%q (q=%q)\n got %s\nwant %s", r, q, got, want)
			}
			clause := canonV(t, map[string]any{"terms": map[string]any{"rarity": []string{r}}})
			if !strings.Contains(got, clause) {
				t.Errorf("%q: DSL lacks the clause %s", r, clause)
			}
		}
	}
}

// The only fold collision in the corpus is the EX pair; any other folded
// spelling of a listed value resolves to that value.
func TestEveryRarityFoldedSpellingResolves(t *testing.T) {
	for _, r := range CanonicalRarities {
		for _, spelling := range []string{strings.ToLower(r), strings.ToUpper(r), "  " + strings.ReplaceAll(r, " ", "   ") + " "} {
			p, _ := ParseParams(url.Values{"rarity": {spelling}})
			if len(p.Rarity) != 1 {
				t.Fatalf("%q: Rarity = %q, want one member", spelling, p.Rarity)
			}
			got := p.Rarity[0]
			switch {
			case slices.Contains(CanonicalRarities, strings.TrimSpace(spelling)):
				if got != strings.TrimSpace(spelling) {
					t.Errorf("%q: exact value became %q", spelling, got)
				}
			case strings.EqualFold(r, "Rare Holo EX"):
				if got != strings.TrimSpace(spelling) {
					t.Errorf("%q: ambiguous spelling became %q, want it verbatim", spelling, got)
				}
			default:
				if got != r {
					t.Errorf("%q: Rarity = %q, want %q", spelling, got, r)
				}
			}
		}
	}
}
