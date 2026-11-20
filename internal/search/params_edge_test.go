package search

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func parseOrFatal(t *testing.T, raw string) (Params, []FieldError) {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", raw, err)
	}
	return ParseParams(v)
}

func dslOf(t *testing.T, raw string) string {
	t.Helper()
	p, errs := parseOrFatal(t, raw)
	if len(errs) != 0 {
		t.Fatalf("%q: unexpected field errors %+v", raw, errs)
	}
	b, err := json.Marshal(BuildQuery(p))
	if err != nil {
		t.Fatalf("%q: %v", raw, err)
	}
	return string(b)
}

// The limits count characters (runes), not bytes, and terms as
// whitespace-separated words; each boundary passes and one past it fails.
func TestEdgeCheckQueryLengthBoundaries(t *testing.T) {
	terms := func(n int) string { return strings.TrimSpace(strings.Repeat("ab ", n)) }
	cases := []struct {
		name  string
		q     string
		wantE bool
	}{
		{"empty", "", false},
		{"one word", "pikachu", false},
		{"256 characters", strings.Repeat("a", 256), false},
		{"257 characters", strings.Repeat("a", 257), true},
		{"256 two-byte characters", strings.Repeat("é", 256), false},
		{"257 two-byte characters", strings.Repeat("é", 257), true},
		{"256 four-byte characters", strings.Repeat("🔥", 256), false},
		{"32 terms", terms(32), false},
		{"33 terms", terms(33), true},
		{"32 terms with runs of mixed whitespace", strings.Repeat("ab \t\n  ", 31) + "ab", false},
		{"33 one-letter terms", strings.TrimSpace(strings.Repeat("a ", 33)), true},
	}
	if MaxQueryRunes != 256 || MaxQueryTerms != 32 {
		t.Fatalf("limits = %d runes, %d terms; want 256 and 32", MaxQueryRunes, MaxQueryTerms)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := CheckQueryLength(tc.q)
			if tc.wantE != (fe != nil) {
				t.Fatalf("CheckQueryLength(%d runes, %d terms) = %+v, want error: %v",
					len([]rune(tc.q)), len(strings.Fields(tc.q)), fe, tc.wantE)
			}
			if fe != nil && (fe.Field != "q" || fe.Message == "") {
				t.Errorf("field error = %+v, want field q with a message", fe)
			}
		})
	}
}

// ParseParams applies the cap after trimming, so padding never counts, and
// reports q after every other check so the first error a client sees is
// unchanged for requests that were already invalid.
func TestEdgeParseParamsCapsQAfterTrimAndLast(t *testing.T) {
	padded := "+++" + strings.Repeat("a", MaxQueryRunes) + "+++"
	p, errs := parseOrFatal(t, "q="+padded)
	if len(errs) != 0 {
		t.Fatalf("256 characters padded with spaces: errs %+v", errs)
	}
	if p.Q != strings.Repeat("a", MaxQueryRunes) {
		t.Errorf("a passing q must reach Params unchanged, got %d runes", len([]rune(p.Q)))
	}

	long := strings.Repeat("a", 300)
	_, errs = parseOrFatal(t, "sort=bogus&q="+long)
	if len(errs) != 2 || errs[0].Field != "sort" || errs[1].Field != "q" {
		t.Fatalf("errs = %+v, want sort then q", errs)
	}
	_, errs = parseOrFatal(t, "page_size=lots&hp_min=x&q="+long)
	if len(errs) == 0 || errs[len(errs)-1].Field != "q" || errs[0].Field != "hp_min" {
		t.Fatalf("errs = %+v, want hp_min first and q last", errs)
	}
}

// The last page the handler advertises is ceil(9600/size); every one of them
// must be requestable and stay inside Elasticsearch's 10,000-result window.
func TestEdgeMaxPageForIsTheAdvertisedLastPageAtEverySize(t *testing.T) {
	const esWindow = 10000
	for s := MinPageSize; s <= MaxPageSize; s++ {
		want := (MaxDocsWindow + s - 1) / s
		if got := MaxPageFor(s); got != want {
			t.Errorf("MaxPageFor(%d) = %d, want ceil(%d/%d) = %d", s, got, MaxDocsWindow, s, want)
			continue
		}
		if end := (MaxPageFor(s)-1)*s + s; end > esWindow {
			t.Errorf("size %d: last page ends at %d, past the %d window", s, end, esWindow)
		}
		p, errs := parseOrFatal(t, "page=999999&page_size="+strconv.Itoa(s))
		if len(errs) != 0 || p.Page != want {
			t.Errorf("size %d: page clamps to %d (errs %+v), want %d", s, p.Page, errs, want)
		}
	}
	for size, pages := range map[int]int{24: 400, 100: 96, 1: 9600} {
		if got := MaxPageFor(size); got != pages {
			t.Errorf("MaxPageFor(%d) = %d, want %d", size, got, pages)
		}
	}
}

// An accepted alias must reach the query as its canonical value: the DSL for
// the alias and for the canonical spelling is byte-identical.
func TestEdgeAliasesBuildTheCanonicalDSL(t *testing.T) {
	pairs := [][2]string{
		{"supertype=Pok%C3%A9mon", "supertype=pokemon"},
		{"supertype=POK%C3%89MON&q=pikachu", "supertype=Pokemon&q=pikachu"},
		{"types=Steel", "types=Metal"},
		{"types=normal", "types=Colorless"},
		{"types=Steel,Metal", "types=Metal"},
		{"types=Fire,NORMAL,ghost&q=charizard", "types=fire,colorless&q=charizard"},
		{"q=pikachu&sort=%20hp%20&order=%20asc%20", "q=pikachu&sort=hp&order=asc"},
		{"q=pikachu&sort=%20name", "q=pikachu&sort=name"},
		{"debug=true&q=mew", "debug=1&q=mew"},
	}
	for _, pr := range pairs {
		if a, b := dslOf(t, pr[0]), dslOf(t, pr[1]); a != b {
			t.Errorf("%q and %q build different DSL:\n%s\n%s", pr[0], pr[1], a, b)
		}
	}
}

// Requests that were valid before the cap and the aliases existed parse with
// no error and their q reaches the query untouched.
func TestEdgeTodaysValidRequestsStayValid(t *testing.T) {
	for _, raw := range []string{
		"", "q=pikachu", "q=+alakazam+", "q=dark+charizard&sort=hp&order=asc",
		"supertype=POKEMON&types=Fire,Water", "types=lightning,FIRE,ghost,fire",
		"rarity=Rare+Holo&series=Base", "set=sv3pt5&page=3", "page_size=100&page=96",
		"hp_min=50&hp_max=120&sort=name&order=desc", "debug=1&q=mew", "id=base1-1",
		"q=" + strings.Repeat("%C3%A9", MaxQueryRunes),
	} {
		p, errs := parseOrFatal(t, raw)
		if len(errs) != 0 {
			t.Errorf("%q: errs %+v", raw, errs)
		}
		v, _ := url.ParseQuery(raw)
		if want := strings.TrimSpace(v.Get("q")); p.Q != want {
			t.Errorf("%q: Q = %q, want %q", raw, p.Q, want)
		}
	}
}
