package search

import (
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func limitsParse(t *testing.T, raw string) (Params, []FieldError) {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", raw, err)
	}
	return ParseParams(v)
}

func fieldsOf(errs []FieldError) []string {
	out := make([]string, 0, len(errs))
	for _, fe := range errs {
		out = append(out, fe.Field)
	}
	return out
}

// The q cap counts what Go counts: runes after strings.TrimSpace, and terms as
// strings.Fields splits them. Hostile encodings must neither slip a much
// larger query past the cap nor make a legitimate one fail: an invalid byte is
// one character, a combining mark is its own character, a zero-width space
// joins rather than splits, and every Unicode space (no-break, em, ideographic,
// NEL) separates terms and is trimmed from the ends.
func TestQueryCapUnderHostileEncodings(t *testing.T) {
	invalid := func(n int) string { return strings.Repeat("\xff", n) }
	sepTerms := func(sep string, n int) string { return strings.TrimSuffix(strings.Repeat("ab"+sep, n), sep) }
	cases := []struct {
		name   string
		q      string // the value of q as the handler receives it, untrimmed
		wantQ  string // the trimmed q a passing request carries, ignored on error
		reject bool
	}{
		{"256 invalid bytes are 256 characters", invalid(256), invalid(256), false},
		{"257 invalid bytes are 257 characters", invalid(257), "", true},
		{"256 NUL bytes pass", strings.Repeat("\x00", 256), strings.Repeat("\x00", 256), false},
		{"257 NUL bytes fail", strings.Repeat("\x00", 257), "", true},
		{"128 decomposed é are 256 characters", strings.Repeat("e\u0301", 128), strings.Repeat("e\u0301", 128), false},
		{"129 decomposed é are 258 characters", strings.Repeat("e\u0301", 129), "", true},
		{"256 four-byte characters, 1024 bytes, pass", strings.Repeat("\U0001F525", 256), strings.Repeat("\U0001F525", 256), false},
		{"zero-width spaces join: 40 words are one term", sepTerms("\u200b", 40), sepTerms("\u200b", 40), false},
		{"32 terms split by no-break spaces", sepTerms("\u00a0", 32), sepTerms("\u00a0", 32), false},
		{"33 terms split by no-break spaces", sepTerms("\u00a0", 33), "", true},
		{"33 terms split by em spaces", sepTerms("\u2003", 33), "", true},
		{"33 terms split by ideographic spaces", sepTerms("\u3000", 33), "", true},
		{"33 terms split by next-line", sepTerms("\u0085", 33), "", true},
		{"32 terms split by tabs and newlines", sepTerms("\t\n", 32), sepTerms("\t\n", 32), false},
		{"33 terms split by tabs and newlines", sepTerms("\t\n", 33), "", true},
		{"256 characters padded with Unicode spaces",
			"\u3000\u00a0\u2003" + strings.Repeat("a", 256) + "\u0085\u3000",
			strings.Repeat("a", 256), false},
		{"1,000 Unicode spaces trim to an empty q", strings.Repeat("\u3000\u00a0 \t", 250), "", false},
		// 32 two-character terms with 31 one-character gaps is 95 runes; pad
		// each term so both limits sit exactly at the edge together.
		{"exactly 256 characters and 32 terms at once",
			strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 7)+" ", 32), " ") + strings.Repeat("y", 256-(32*8-1)),
			strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 7)+" ", 32), " ") + strings.Repeat("y", 256-(32*8-1)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, errs := ParseParams(url.Values{"q": {tc.q}})
			if tc.reject {
				if len(errs) != 1 || errs[0].Field != "q" {
					t.Fatalf("errs = %+v, want exactly one error on q", errs)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("errs = %+v for %d runes, %d terms; want none",
					errs, utf8.RuneCountInString(strings.TrimSpace(tc.q)), len(strings.Fields(tc.q)))
			}
			if p.Q != tc.wantQ {
				t.Fatalf("Q = %q, want %q", p.Q, tc.wantQ)
			}
			if tc.wantQ == "" && p.Sort != "newest" {
				t.Errorf("a q that trims to nothing must browse, got sort %q", p.Sort)
			}
			// What passes the cap is what the query builder sees and can
			// serialise: the DSL carries the trimmed text, nothing more.
			if p.Q != "" {
				b, err := json.Marshal(BuildQuery(p))
				if err != nil {
					t.Fatalf("BuildQuery body does not marshal: %v", err)
				}
				if len(b) == 0 {
					t.Fatal("empty DSL")
				}
			}
		})
	}
	// The exact-at-both-limits case must really be at both limits.
	both := cases[len(cases)-1].wantQ
	if n, f := utf8.RuneCountInString(both), len(strings.Fields(both)); n != MaxQueryRunes || f != MaxQueryTerms {
		t.Fatalf("fixture has %d runes and %d terms, want %d and %d", n, f, MaxQueryRunes, MaxQueryTerms)
	}
	if fe := CheckQueryLength(both + "z"); fe == nil {
		t.Error("one character past both limits must fail")
	}
}

// + and %20 decode to the same space, so a q encoded either way is capped the
// same way and, when it passes, builds the same DSL as its trimmed form.
func TestQueryCapPlusAndPercentTwentyAgree(t *testing.T) {
	words := make([]string, MaxQueryTerms)
	for i := range words {
		words[i] = "w" + strconv.Itoa(i)
	}
	for _, n := range []int{MaxQueryTerms, MaxQueryTerms + 1} {
		ws := words
		if n > MaxQueryTerms {
			ws = append(append([]string{}, words...), "extra")
		}
		plus := "q=++" + strings.Join(ws, "+") + "++"
		pct := "q=%20%20" + strings.Join(ws, "%20") + "%20%20"
		pa, ea := limitsParse(t, plus)
		pb, eb := limitsParse(t, pct)
		if !reflect.DeepEqual(fieldsOf(ea), fieldsOf(eb)) || !reflect.DeepEqual(pa, pb) {
			t.Fatalf("%d terms: + gives %+v %+v, %%20 gives %+v %+v", n, pa, ea, pb, eb)
		}
		if wantErr := n > MaxQueryTerms; wantErr != (len(ea) == 1 && ea[0].Field == "q") {
			t.Fatalf("%d terms: errs %+v", n, ea)
		}
		if len(ea) == 0 {
			a, _ := json.Marshal(BuildQuery(pa))
			trimmed, _ := limitsParse(t, "q="+url.QueryEscape(strings.Join(ws, " ")))
			b, _ := json.Marshal(BuildQuery(trimmed))
			if string(a) != string(b) {
				t.Fatalf("padded and trimmed q build different DSL:\n%s\n%s", a, b)
			}
		}
	}
}

// With every strict parameter bad and q too long, every field is reported
// once, in the order the parser has always used, with q appended last. A
// repeated q is judged on its first value only, like every other parameter.
func TestQueryCapErrorOrderAndRepeatedQ(t *testing.T) {
	long := strings.Repeat("a", MaxQueryRunes+1)
	_, errs := limitsParse(t, "q="+long+"&sort=bogus&order=up&supertype=dragon&hp_min=x&hp_max=y&page_size=z&page=w")
	want := []string{"sort", "order", "supertype", "hp_min", "hp_max", "page_size", "page", "q"}
	if got := fieldsOf(errs); !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}

	p, errs := limitsParse(t, "q=short&q="+long)
	if len(errs) != 0 || p.Q != "short" {
		t.Fatalf("q=short&q=<long>: Q %q errs %+v, want short and no error", p.Q, errs)
	}
	_, errs = limitsParse(t, "q="+long+"&q=short")
	if got := fieldsOf(errs); !reflect.DeepEqual(got, []string{"q"}) {
		t.Fatalf("q=<long>&q=short: fields %v, want [q]", got)
	}
	terms := strings.Repeat("a+", MaxQueryTerms+1)
	_, errs = limitsParse(t, "q="+terms+"&q=fine")
	if got := fieldsOf(errs); !reflect.DeepEqual(got, []string{"q"}) {
		t.Fatalf("q=<33 terms>&q=fine: fields %v, want [q]", got)
	}
	// Both limits broken at once still yields a single q error.
	_, errs = limitsParse(t, "q="+strings.Repeat("ab+", 200))
	if got := fieldsOf(errs); !reflect.DeepEqual(got, []string{"q"}) {
		t.Fatalf("over both limits: fields %v, want exactly one q", got)
	}
}

// pagesForHandler mirrors the search handler's pages count: the total capped
// at the window, rounded up to whole pages.
func pagesForHandler(total, pageSize int) int {
	capped := min(total, MaxDocsWindow)
	if capped == 0 {
		return 0
	}
	return (capped + pageSize - 1) / pageSize
}

// Every page the handler advertises is requestable, its from+size stays inside
// Elasticsearch's 10,000-result window, and the last advertised page starts
// before the capped total so it has results. Out-of-range page sizes and pages,
// including the int64 extremes, clamp without overflow.
func TestLastAdvertisedPageIsReachableAndInsideTheWindow(t *testing.T) {
	const esWindow = 10000
	from := func(p Params) int {
		body := BuildQuery(p)
		f, _ := body["from"].(int)
		return f
	}
	pages := []string{
		"", "0", "-1", "1", "999999",
		strconv.Itoa(math.MaxInt64), strconv.Itoa(math.MinInt64),
	}
	sizes := []string{"0", "-5", "101", "1000000", strconv.Itoa(math.MaxInt64), strconv.Itoa(math.MinInt64)}
	for s := MinPageSize; s <= MaxPageSize; s++ {
		sizes = append(sizes, strconv.Itoa(s))
	}
	for _, size := range sizes {
		for _, page := range pages {
			raw := "page_size=" + size + "&page=" + page
			p, errs := limitsParse(t, raw)
			if len(errs) != 0 {
				t.Fatalf("%s: errs %+v", raw, errs)
			}
			if p.PageSize < MinPageSize || p.PageSize > MaxPageSize {
				t.Fatalf("%s: page size %d out of range", raw, p.PageSize)
			}
			if p.Page < 1 || p.Page > MaxPageFor(p.PageSize) {
				t.Fatalf("%s: page %d outside 1..%d", raw, p.Page, MaxPageFor(p.PageSize))
			}
			if f := from(p); f < 0 || f+p.PageSize > esWindow {
				t.Fatalf("%s: from %d + size %d leaves the %d window", raw, f, p.PageSize, esWindow)
			}
		}
	}
	for s := MinPageSize; s <= MaxPageSize; s++ {
		for _, total := range []int{1, s - 1, s, s + 1, 9599, 9600, 9601, 20635} {
			if total < 1 {
				continue
			}
			last := pagesForHandler(total, s)
			p, _ := limitsParse(t, "page_size="+strconv.Itoa(s)+"&page="+strconv.Itoa(last))
			if p.Page != last {
				t.Fatalf("size %d total %d: advertised last page %d clamps to %d", s, total, last, p.Page)
			}
			if f := from(p); f >= min(total, MaxDocsWindow) {
				t.Fatalf("size %d total %d: last page starts at %d, past the %d results", s, total, f, min(total, MaxDocsWindow))
			}
		}
		// At or above the window the advertised count is exactly the ceiling.
		if got, want := MaxPageFor(s), pagesForHandler(20635, s); got != want {
			t.Fatalf("size %d: MaxPageFor %d, handler advertises %d", s, got, want)
		}
	}
	for _, s := range []int{0, -1, math.MinInt} {
		if got := MaxPageFor(s); got != MaxDocsWindow {
			t.Errorf("MaxPageFor(%d) = %d, want the size-1 ceiling %d", s, got, MaxDocsWindow)
		}
	}
}

// The two aliases map only to their own canonical type, in any case, keep the
// request's order, collapse with their canonical spelling either way round, and
// never widen to another type. A near-miss is still an unknown member and is
// dropped.
func TestTypeAliasesMapOnlyToTheirOwnType(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"types=Steel", []string{"Metal"}},
		{"types=sTeEl", []string{"Metal"}},
		{"types=NORMAL", []string{"Colorless"}},
		{"types=Metal,steel", []string{"Metal"}},
		{"types=colorless,Normal,NORMAL", []string{"Colorless"}},
		{"types=Normal,Steel", []string{"Colorless", "Metal"}},
		{"types=steel,normal,metal,colorless,Fire", []string{"Metal", "Colorless", "Fire"}},
		{"types=steels,norm,steel+normal,iron", nil},
		{"types=+steel+,%09normal%0A", []string{"Metal", "Colorless"}},
		{"types=steel&types=normal", []string{"Metal"}},
	}
	for _, tc := range cases {
		p, errs := limitsParse(t, tc.in)
		if len(errs) != 0 {
			t.Errorf("%q: errs %+v", tc.in, errs)
		}
		if !reflect.DeepEqual(p.Types, tc.want) {
			t.Errorf("%q: Types = %v, want %v", tc.in, p.Types, tc.want)
		}
	}
	steel, _ := json.Marshal(BuildQuery(mustParams(t, "types=Steel")))
	colorless, _ := json.Marshal(BuildQuery(mustParams(t, "types=Colorless")))
	if strings.Contains(string(steel), "Colorless") || string(steel) == string(colorless) {
		t.Errorf("types=Steel must filter on Metal only:\n%s", steel)
	}
}

func mustParams(t *testing.T, raw string) Params {
	t.Helper()
	p, errs := limitsParse(t, raw)
	if len(errs) != 0 {
		t.Fatalf("%q: errs %+v", raw, errs)
	}
	return p
}

// The accented supertype is the same canonical value as the plain one in every
// case and with any surrounding whitespace. A decomposed accent (e plus a
// combining acute) is not one of the accepted spellings and stays a 400.
func TestSupertypeAccentedSpellings(t *testing.T) {
	for _, raw := range []string{
		"Pok\u00e9mon", "POK\u00c9MON", "pOk\u00c9mOn", " \tPok\u00e9mon\n", "\u3000pok\u00e9mon\u00a0", "pokemon",
	} {
		p, errs := ParseParams(url.Values{"supertype": {raw}})
		if len(errs) != 0 || p.Supertype != "Pok\u00e9mon" {
			t.Errorf("supertype=%q: %q errs %+v, want Pok\u00e9mon", raw, p.Supertype, errs)
		}
	}
	for _, raw := range []string{"Poke\u0301mon", "pok\u00e9mons", "pok\u00e8mon", "pok mon"} {
		_, errs := ParseParams(url.Values{"supertype": {raw}})
		if got := fieldsOf(errs); !reflect.DeepEqual(got, []string{"supertype"}) {
			t.Errorf("supertype=%q: fields %v, want [supertype]", raw, got)
		}
	}
}

// sort and order are trimmed of any whitespace, a padded unknown value is the
// same 400 with the same message, and a blank order leaves each sort's default.
func TestSortAndOrderTrimming(t *testing.T) {
	cases := []struct {
		sort, order       string
		wantSort, wantOrd string
	}{
		{"\thp\n", "", "hp", "desc"},
		{"\u3000name\u00a0", "   ", "name", "asc"},
		{" newest ", "\t", "newest", ""},
		{"oldest\r\n", " asc ", "oldest", ""},
		{"relevance", "", "relevance", ""},
		{"  hp", "\nasc\t", "hp", "asc"},
		{"name ", "\u2003desc\u2003", "name", "desc"},
	}
	for _, tc := range cases {
		v := url.Values{"q": {"mew"}, "sort": {tc.sort}}
		if tc.order != "" {
			v.Set("order", tc.order)
		}
		p, errs := ParseParams(v)
		if len(errs) != 0 || p.Sort != tc.wantSort || p.Order != tc.wantOrd {
			t.Errorf("sort=%q order=%q: %q/%q errs %+v, want %q/%q",
				tc.sort, tc.order, p.Sort, p.Order, errs, tc.wantSort, tc.wantOrd)
		}
	}
	_, errs := ParseParams(url.Values{"sort": {"\t h p \n"}})
	if len(errs) != 1 || errs[0] != (FieldError{"sort", "sort must be one of relevance|newest|oldest|hp|name"}) {
		t.Errorf("inner-space sort: errs %+v", errs)
	}
	_, errs = ParseParams(url.Values{"sort": {"hp"}, "order": {" DESC "}})
	if len(errs) != 1 || errs[0] != (FieldError{"order", "order must be one of asc|desc"}) {
		t.Errorf("upper-case order: errs %+v", errs)
	}
}

// Only the exact values 1 and true turn debug on; nothing is trimmed or
// case-folded, so no other spelling starts returning the DSL.
func TestDebugOnlyOneAndTrue(t *testing.T) {
	for raw, want := range map[string]bool{
		"1": true, "true": true,
		"": false, "0": false, "false": false, "TRUE": false, "True": false, " true": false,
		"true ": false, " 1": false, "01": false, "yes": false, "on": false, "t": false, "1.0": false,
	} {
		p, errs := ParseParams(url.Values{"debug": {raw}})
		if len(errs) != 0 || p.Debug != want {
			t.Errorf("debug=%q: Debug %v errs %+v, want %v", raw, p.Debug, errs, want)
		}
	}
}

// Requests that were valid before the cap, the aliases and the new page
// ceiling parse to exactly the struct they always did. The expected values are
// spelled out so any drift is visible without the old code.
func TestRequestsValidBeforeParseToTheSameParams(t *testing.T) {
	cases := []struct {
		in   string
		want Params
	}{
		{"", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"q=pikachu", Params{Q: "pikachu", Sort: "relevance", Page: 1, PageSize: 24}},
		{"q=%20dark%20charizard%20&sort=hp&order=asc",
			Params{Q: "dark charizard", Sort: "hp", Order: "asc", Page: 1, PageSize: 24}},
		{"supertype=pokemon&types=Metal,Colorless,fire",
			Params{Supertype: "Pokémon", Types: []string{"Metal", "Colorless", "Fire"}, Sort: "newest", Page: 1, PageSize: 24}},
		{"supertype=Trainer&sort=name", Params{Supertype: "Trainer", Sort: "name", Order: "asc", Page: 1, PageSize: 24}},
		{"supertype=ENERGY&sort=oldest&order=desc", Params{Supertype: "Energy", Sort: "oldest", Page: 1, PageSize: 24}},
		{"rarity=Rare+Holo&series=Base&set=base1",
			Params{Rarity: []string{"Rare Holo"}, Series: []string{"Base"}, SetID: "base1", Sort: "newest", Page: 1, PageSize: 24}},
		{"hp_min=+50+&hp_max=120", Params{HPMin: intp(50), HPMax: intp(120), Sort: "newest", Page: 1, PageSize: 24}},
		{"page=400", Params{Sort: "newest", Page: 400, PageSize: 24}},
		{"page=401", Params{Sort: "newest", Page: 400, PageSize: 24}},
		{"page_size=100&page=999", Params{Sort: "newest", Page: 96, PageSize: 100}},
		{"page_size=1&page=999999", Params{Sort: "newest", Page: 9600, PageSize: 1}},
		{"page_size=7&page=1371", Params{Sort: "newest", Page: 1371, PageSize: 7}},
		{"page_size=50&page=192", Params{Sort: "newest", Page: 192, PageSize: 50}},
		{"debug=1&id=base1-4", Params{ID: "base1-4", Sort: "newest", Page: 1, PageSize: 24, Debug: true}},
		{"q=mew&sort=relevance&order=asc", Params{Q: "mew", Sort: "relevance", Page: 1, PageSize: 24}},
		{"sort=relevance", Params{Sort: "newest", Page: 1, PageSize: 24}},
		{"q=" + strings.Repeat("a", 256), Params{Q: strings.Repeat("a", 256), Sort: "relevance", Page: 1, PageSize: 24}},
		{"q=" + strings.TrimSuffix(strings.Repeat("ab+", 32), "+"),
			Params{Q: strings.TrimSuffix(strings.Repeat("ab ", 32), " "), Sort: "relevance", Page: 1, PageSize: 24}},
		{"utm_source=x&types=ghost", Params{Sort: "newest", Page: 1, PageSize: 24}},
	}
	for _, tc := range cases {
		p, errs := limitsParse(t, tc.in)
		if len(errs) != 0 {
			t.Errorf("%q: errs %+v", tc.in, errs)
			continue
		}
		if !reflect.DeepEqual(p, tc.want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.in, p, tc.want)
		}
	}
}
