package search

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzQueryLengthCap feeds raw q values, including invalid UTF-8, straight to
// ParseParams (no URL decoding in between) and asserts the cap agrees with a
// plain count: a q error exactly when the trimmed text is over either limit,
// the same answer from CheckQueryLength, no other field ever reported, and
// whitespace padding never changing the outcome.
func FuzzQueryLengthCap(f *testing.F) {
	for _, seed := range []string{
		"", "pikachu", strings.Repeat("a", MaxQueryRunes), strings.Repeat("a", MaxQueryRunes+1),
		strings.Repeat("\xff", MaxQueryRunes), strings.Repeat("e\u0301", 128),
		strings.Repeat("a\u3000", MaxQueryTerms), strings.Repeat("a\u00a0", MaxQueryTerms+1),
		strings.Repeat("\t\n ", 400), "\x00\x00", strings.Repeat("ab\u200b", 100),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		trimmed := strings.TrimSpace(q)
		over := utf8.RuneCountInString(trimmed) > MaxQueryRunes || len(strings.Fields(trimmed)) > MaxQueryTerms

		p, errs := ParseParams(url.Values{"q": {q}})
		if p.Q != trimmed {
			t.Fatalf("Q = %q, want the trimmed %q", p.Q, trimmed)
		}
		if over != (len(errs) == 1) {
			t.Fatalf("over limits %v, errs %+v", over, errs)
		}
		for _, fe := range errs {
			if fe.Field != "q" {
				t.Fatalf("q alone produced an error on %q", fe.Field)
			}
		}
		if (CheckQueryLength(trimmed) != nil) != over {
			t.Fatalf("CheckQueryLength disagrees with ParseParams for %q", trimmed)
		}
		_, padded := ParseParams(url.Values{"q": {" \t\u3000" + q + "\n\u00a0"}})
		if len(padded) != len(errs) {
			t.Fatalf("whitespace padding changed the outcome: %+v vs %+v", padded, errs)
		}
	})
}
