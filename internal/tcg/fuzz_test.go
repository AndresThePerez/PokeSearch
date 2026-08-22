package tcg

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// FuzzParseDamage guards the transform's one hand-rolled parser. It runs over
// every printed damage string in the corpus ("30", "10+", "100×", "120-",
// "10×", ""), and the field it feeds is indexed as a number — a panic here
// fails a seed halfway through, and a wrong value is unnoticeable forever.
func FuzzParseDamage(f *testing.F) {
	for _, seed := range []string{
		"", "30", "10+", "100×", "120-", "10×", "50×", "20+", "×", "+",
		"abc", "0", "007", "999999999999999999999999", "-30", " 30", "3 0",
		"\x00", "١٢٣", "30damage",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := ParseDamage(s)

		// The value is indexed into an ES integer field; anything outside that
		// range fails the bulk chunk that carries it.
		if got < 0 || got > math.MaxInt32 {
			t.Fatalf("ParseDamage(%q) = %d, outside the indexable integer range", s, got)
		}
		// Only a leading digit can produce damage. "Poison 30" is not 30.
		if got != 0 && (s == "" || s[0] < '0' || s[0] > '9') {
			t.Fatalf("ParseDamage(%q) = %d without a leading digit", s, got)
		}
		// A trailing modifier is not part of the number, so appending one may
		// never change the answer — that is the whole point of the parser.
		for _, suffix := range []string{"+", "-", "×", " damage"} {
			if again := ParseDamage(s + suffix); again != got {
				t.Fatalf("ParseDamage(%q) = %d but ParseDamage(%q) = %d", s, got, s+suffix, again)
			}
		}
		if got == 0 {
			return
		}
		// A non-zero result is exactly the leading digit run, leading zeros
		// aside: nothing skipped, nothing beyond it consumed.
		run := 0
		for run < len(s) && s[run] >= '0' && s[run] <= '9' {
			run++
		}
		if want := strings.TrimLeft(s[:run], "0"); want != strconv.Itoa(got) {
			t.Fatalf("ParseDamage(%q) = %d, leading digit run is %q", s, got, want)
		}
	})
}

// FuzzNormalizeDate covers the source→ES date rewrite. Anything left holding a
// slash is rejected by the date mapping at index time, which surfaces as a
// bulk failure on a seed that has already run for a minute.
func FuzzNormalizeDate(f *testing.F) {
	for _, seed := range []string{
		"", "1999/01/09", "2005/10/31", "1999-01-09", "////", "9999/99/99",
		"1999/1/9", "not a date", "\x00/\x00", "１９９９/０１/０９",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := NormalizeDate(s)
		if strings.Contains(got, "/") {
			t.Fatalf("NormalizeDate(%q) = %q still contains a slash", s, got)
		}
		if len(got) != len(s) {
			t.Fatalf("NormalizeDate(%q) = %q changed length %d → %d", s, got, len(s), len(got))
		}
		if strings.ReplaceAll(got, "-", "/") != strings.ReplaceAll(s, "-", "/") {
			t.Fatalf("NormalizeDate(%q) = %q changed more than the separators", s, got)
		}
	})
}
