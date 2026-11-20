package search

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fieldErrorsOn returns the field errors ParseParams reports on field.
func fieldErrorsOn(errs []FieldError, field string) []FieldError {
	var out []FieldError
	for _, fe := range errs {
		if fe.Field == field {
			out = append(out, fe)
		}
	}
	return out
}

// noisyList is n distinct members joined by commas, with every kind of
// separator noise ParseParams must see through: empty members, leading and
// trailing commas, and ASCII and Unicode spaces around each member.
func noisyList(n int) string {
	pads := []string{" ", "\t", "\u0085", " ", " ", "\n", ""}
	var b strings.Builder
	b.WriteString(",,  ,")
	for i := range n {
		pad := pads[i%len(pads)]
		b.WriteString(pad + "m" + strconv.Itoa(i) + pad + ",")
		if i%5 == 0 {
			b.WriteString(" ,\u0085,\t,")
		}
	}
	b.WriteString(", ,")
	return b.String()
}

// Separator noise neither counts as a member nor hides one: 64 members among
// any amount of empty and whitespace-only members are accepted, and one more
// is refused, for both lists.
func TestListLimitIgnoresSeparatorNoise(t *testing.T) {
	for _, field := range []string{"rarity", "series"} {
		for _, n := range []int{MaxListMembers, MaxListMembers + 1} {
			t.Run(field+" "+strconv.Itoa(n), func(t *testing.T) {
				p, errs := ParseParams(url.Values{field: {noisyList(n)}})
				got := fieldErrorsOn(errs, field)
				if n <= MaxListMembers {
					if len(errs) != 0 {
						t.Fatalf("%d members refused: %v", n, errs)
					}
					list := p.Rarity
					if field == "series" {
						list = p.Series
					}
					if len(list) != n {
						t.Fatalf("%s carries %d members, want %d", field, len(list), n)
					}
					return
				}
				if len(got) != 1 || got[0].Message != field+" takes at most 64 values" {
					t.Fatalf("errors %v, want one count error on %s", errs, field)
				}
			})
		}
	}
}

// The count bounds the work done with the list, so it is taken before the
// rarity mapping folds spellings together: 65 spellings of one stored rarity
// are refused even though they name a single value.
func TestRarityLimitCountsBeforeMapping(t *testing.T) {
	spellings := make([]string, MaxListMembers+1)
	for i := range spellings {
		// Vary case and inner spacing; each folds onto "Rare Holo".
		s := []rune("rare holo")
		for j := range s {
			if (i>>(j%7))&1 == 1 {
				s[j] = []rune(strings.ToUpper(string(s[j])))[0]
			}
		}
		spellings[i] = string(s) + strings.Repeat(" ", i%3)
	}
	_, errs := ParseParams(url.Values{"rarity": {strings.Join(spellings, ",")}})
	if got := fieldErrorsOn(errs, "rarity"); len(got) != 1 {
		t.Fatalf("65 spellings of one rarity: errors %v, want one on rarity", errs)
	}
	p, errs := ParseParams(url.Values{"rarity": {strings.Join(spellings[:MaxListMembers], ",")}})
	if len(errs) != 0 || !slices.Equal(p.Rarity, []string{"Rare Holo"}) {
		t.Fatalf("64 spellings: rarity %q errors %v, want the one stored value", p.Rarity, errs)
	}
}

// Lengths are counted in characters whatever their encoding: 128 characters
// of 1-, 2-, 3- or 4-byte UTF-8 are accepted and 129 refused, for a list
// member, set and id alike. Surrounding spaces do not count; a combining mark
// is a character of its own; an invalid byte counts as one character, so
// invalid bytes cannot carry a longer value through.
func TestValueLengthCountsCharacters(t *testing.T) {
	units := map[string]string{
		"1-byte":         "a",
		"2-byte":         "é",
		"3-byte":         "語",
		"4-byte":         "🜲",
		"combining pair": "é", // two characters per unit
		"invalid byte":   "\xff",
	}
	for name, unit := range units {
		per := utf8.RuneCountInString(unit)
		within := strings.Repeat(unit, MaxValueRunes/per)
		over := within + strings.Repeat("a", MaxValueRunes+1-utf8.RuneCountInString(within))
		if utf8.RuneCountInString(within) != MaxValueRunes || utf8.RuneCountInString(over) != MaxValueRunes+1 {
			t.Fatalf("%s: bad fixture %d/%d", name, utf8.RuneCountInString(within), utf8.RuneCountInString(over))
		}
		for _, field := range []string{"rarity", "series", "set", "id"} {
			t.Run(name+" "+field, func(t *testing.T) {
				for _, v := range []string{within, "  " + within + "\t\u0085"} {
					in := v
					if field == "rarity" || field == "series" {
						in = "Common," + v + ",Base"
					}
					if _, errs := ParseParams(url.Values{field: {in}}); len(errs) != 0 {
						t.Errorf("%d characters refused: %v", MaxValueRunes, errs)
					}
				}
				in := over
				if field == "rarity" || field == "series" {
					in = "Common," + over + ",Base"
				}
				_, errs := ParseParams(url.Values{field: {in}})
				if got := fieldErrorsOn(errs, field); len(got) != 1 || !strings.Contains(got[0].Message, "at most 128 characters") {
					t.Errorf("%d characters: errors %v, want one length error on %s", MaxValueRunes+1, errs, field)
				}
			})
		}
	}
}

// A list over both limits gets one error, not two; a refused list carries
// nothing on while the other list, within its limits, is parsed as before.
func TestListOverBothLimitsIsOneError(t *testing.T) {
	long := strings.Repeat("x", MaxValueRunes+1)
	for name, raw := range map[string]string{
		"long member first": long + "," + strings.Repeat("a,", MaxListMembers+5),
		"long member last":  strings.Repeat("a,", MaxListMembers+5) + long,
	} {
		t.Run(name, func(t *testing.T) {
			p, errs := ParseParams(url.Values{"rarity": {raw}, "series": {"Base,Jungle"}})
			if got := fieldErrorsOn(errs, "rarity"); len(got) != 1 || len(errs) != 1 {
				t.Fatalf("errors %v, want exactly one, on rarity", errs)
			}
			if len(p.Rarity) != 0 {
				t.Errorf("refused rarity still carries %d members", len(p.Rarity))
			}
			if !slices.Equal(p.Series, []string{"Base", "Jungle"}) {
				t.Errorf("series = %q, want the list within its limits parsed", p.Series)
			}
		})
	}
}

// The first error a client sees is the one it saw before the size limits
// existed: an older strict field still comes first when every new limit is
// broken at once.
func TestOlderFieldErrorsStayFirst(t *testing.T) {
	over := strings.Repeat("a,", MaxListMembers+1)
	long := strings.Repeat("z", MaxValueRunes+1)
	for _, tc := range []struct {
		older, value string
	}{
		{"sort", "bogus"}, {"order", "sideways"}, {"supertype", "item"},
		{"hp_min", "ten"}, {"hp_max", "1e3"}, {"page_size", "x"}, {"page", "two"},
		{"q", strings.Repeat("q ", MaxQueryTerms+1)},
	} {
		t.Run(tc.older, func(t *testing.T) {
			v := url.Values{"rarity": {over}, "series": {over}, "set": {long}, "id": {long}, tc.older: {tc.value}}
			_, errs := ParseParams(v)
			if len(errs) != 5 {
				t.Fatalf("errors %v, want the older one and four size errors", errs)
			}
			if errs[0].Field != tc.older {
				t.Errorf("first error on %q, want %q: %v", errs[0].Field, tc.older, errs)
			}
			want := []string{"rarity", "series", "set", "id"}
			for i, fe := range errs[1:] {
				if fe.Field != want[i] {
					t.Errorf("error %d on %q, want %q", i+1, fe.Field, want[i])
				}
			}
		})
	}
}

// Whatever the shape of a 20 KB value, parsing it stays fast and never
// panics, and a list is refused or bounded.
func TestListLimitsOnLargeInputsOfEveryShape(t *testing.T) {
	const size = 20 << 10
	shapes := map[string]string{
		"commas only":      strings.Repeat(",", size),
		"spaces only":      strings.Repeat(" ", size),
		"one long member":  strings.Repeat("a", size),
		"short members":    strings.Repeat("a,", size/2),
		"unicode spaces":   strings.Repeat(" ,", size/3),
		"invalid bytes":    strings.Repeat("\xff,", size/2),
		"4-byte members":   strings.Repeat("🜲,", size/5),
		"empty then over":  strings.Repeat(",", size-200) + strings.Repeat("b,", MaxListMembers+1),
		"empty then fine":  strings.Repeat(" ,", size/2-100) + strings.Repeat("c,", MaxListMembers),
		"combining marks":  "e" + strings.Repeat("́", size/2),
		"quotes and slash": strings.Repeat(`"\,`, size/3),
	}
	for name, raw := range shapes {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			p, _ := ParseParams(url.Values{"rarity": {raw}, "series": {raw}, "set": {raw}, "id": {raw}, "types": {raw}})
			if d := time.Since(start); d > time.Second {
				t.Errorf("parsing took %s", d)
			}
			for _, list := range [][]string{p.Rarity, p.Series} {
				if len(list) > MaxListMembers {
					t.Errorf("a list of %d members was accepted", len(list))
				}
				for _, m := range list {
					if utf8.RuneCountInString(m) > MaxValueRunes {
						t.Errorf("a member of %d characters was accepted", utf8.RuneCountInString(m))
					}
				}
			}
		})
	}
}

// FuzzListLimitsOverRawQuery feeds whole raw query strings: whatever is
// accepted is within both limits, and a list with an error is carried no
// further.
func FuzzListLimitsOverRawQuery(f *testing.F) {
	for _, seed := range []string{
		"rarity=" + strings.Repeat("a%2C", MaxListMembers+1),
		"series=%C2%85a%C2%85," + strings.Repeat("%2C", 200),
		"rarity=a&rarity=" + strings.Repeat("b,", 70),
		"set=" + strings.Repeat("%FF", MaxValueRunes+1),
		"id=" + strings.Repeat("e%CC%81", 64),
		"sort=bogus&series=" + strings.Repeat("s,", 65),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		p, errs := ParseParams(v)
		refused := func(field string) bool { return len(fieldErrorsOn(errs, field)) > 0 }
		for field, list := range map[string][]string{"rarity": p.Rarity, "series": p.Series} {
			if refused(field) && len(list) != 0 {
				t.Fatalf("%q: refused %s carries %q", raw, field, list)
			}
			if !refused(field) && len(splitList(v.Get(field))) > MaxListMembers {
				t.Fatalf("%q: %s of %d members accepted", raw, field, len(splitList(v.Get(field))))
			}
			for _, m := range list {
				if utf8.RuneCountInString(m) > MaxValueRunes {
					t.Fatalf("%q: %s member of %d characters accepted", raw, field, utf8.RuneCountInString(m))
				}
			}
		}
		for field, s := range map[string]string{"set": p.SetID, "id": p.ID} {
			if !refused(field) && utf8.RuneCountInString(s) > MaxValueRunes {
				t.Fatalf("%q: %s of %d characters accepted", raw, field, utf8.RuneCountInString(s))
			}
		}
	})
}
