package search

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// rarityOf parses one rarity parameter value the way a request would carry it.
func rarityOf(t *testing.T, raw string) []string {
	t.Helper()
	p, errs := ParseParams(url.Values{"rarity": {raw}})
	if len(errs) != 0 {
		t.Fatalf("rarity=%q: unexpected field errors %v", raw, errs)
	}
	return p.Rarity
}

// titleCase upper-cases the first letter of every space-separated word and
// lower-cases the rest ("rare holo lv.x" -> "Rare Holo Lv.x").
func titleCase(s string) string {
	words := strings.Split(strings.ToLower(s), " ")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// A request can carry about a megabyte of query string, and the rarity list
// is split on every comma of it. Dropping repeats must stay linear in the
// list: a long run of distinct members costs milliseconds, not seconds of a
// core per request.
func TestRarityListStaysLinearInItsLength(t *testing.T) {
	const members = 100000 // about 590 KB of query string, under the 1 MB header limit
	var b strings.Builder
	for i := 0; i < members; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", i)
	}
	raw := b.String()
	start := time.Now()
	p, errs := ParseParams(url.Values{"rarity": {raw}})
	elapsed := time.Since(start)
	if len(errs) != 0 {
		t.Fatalf("unexpected field errors %v", errs)
	}
	if len(p.Rarity) != members {
		t.Fatalf("Rarity has %d members, want %d (unknown members are kept)", len(p.Rarity), members)
	}
	if elapsed > 2*time.Second {
		t.Errorf("parsing %d distinct rarity members took %v; dropping repeats is quadratic in the list", members, elapsed)
	}
}

// Only case and whitespace are spellings. A value that differs by punctuation,
// by an underscore for a space, by a missing space, or by a character that is
// neither a letter's case nor whitespace names no stored value and goes
// through exactly as given.
func TestRarityNonSpellingsPassThroughVerbatim(t *testing.T) {
	for _, in := range []string{
		"rare holo lvx",
		"rare holo lv x",
		"rare holo lv-x",
		"Rare Holo LV X",
		"mega attack rare",
		"mega-attack-rare",
		"megaattackrare",
		"rare_holo",
		"Rare_Holo",
		"rareholo",
		"rare-holo",
		"rare holo.",
		"rare\u200bholo",                // zero-width space is not whitespace
		"\u200brare holo",               // nor is it trimmed
		"rare holo\u200b",               // at either end
		"\uff32\uff21\uff32\uff25 holo", // full-width letters are another character
		"rar\u00e9 holo",                // an accent is another letter
		"rare holo v\u0130star",         // a dotted capital I lower-cases to i plus a combining dot
		"ace spec",
		"Rare Holo EXX",
		"R",
	} {
		in := in
		t.Run(fmt.Sprintf("%+q", in), func(t *testing.T) {
			// A trailing comma only adds an empty member.
			want := []string{strings.TrimSpace(strings.TrimSuffix(in, ","))}
			if got := rarityOf(t, in); !reflect.DeepEqual(got, want) {
				t.Errorf("Rarity = %+q, want %+q", got, want)
			}
		})
	}
}

// Whitespace that strings.Fields and strings.TrimSpace already treat as such
// folds like a plain space, so a spelling copied from a page with a no-break
// space, an em space or a line break still reaches the stored value.
func TestRarityUnicodeWhitespaceFolds(t *testing.T) {
	for _, sep := range []string{"\u00a0", "\u2003", "\u3000", "\u0085", "\t", "\n", "\r\n", "\v", "\f", " \u00a0 "} {
		for _, lead := range []string{"", "\u00a0", "\n\t "} {
			in := lead + "rare" + sep + "HOLO" + sep + "vstar" + lead
			if got, want := rarityOf(t, in), []string{"Rare Holo VSTAR"}; !reflect.DeepEqual(got, want) {
				t.Errorf("rarity=%+q: Rarity = %+q, want %+q", in, got, want)
			}
		}
	}
}

// Every spelling of the pair that shares a fold. The two exact values keep
// their own meaning, anything else that folds onto both is left as given, and
// the pair is never collapsed into one value.
func TestRarityExPairIsNeverResolved(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Rare Holo EX", []string{"Rare Holo EX"}},
		{"Rare Holo ex", []string{"Rare Holo ex"}},
		{"rare holo ex", []string{"rare holo ex"}},
		{"RARE HOLO EX", []string{"RARE HOLO EX"}},
		{"Rare Holo Ex", []string{"Rare Holo Ex"}},
		{"rare holo eX", []string{"rare holo eX"}},
		{"Rare  Holo   EX", []string{"Rare  Holo   EX"}},
		{"\u00a0Rare\u00a0Holo\u00a0ex", []string{"Rare\u00a0Holo\u00a0ex"}}, // only the ends are trimmed
		{"Rare Holo EX,Rare Holo ex", []string{"Rare Holo EX", "Rare Holo ex"}},
		{"Rare Holo ex,Rare Holo EX", []string{"Rare Holo ex", "Rare Holo EX"}},
		{"Rare Holo ex,rare holo ex,Rare Holo EX", []string{"Rare Holo ex", "rare holo ex", "Rare Holo EX"}},
		{"rare holo ex,RARE HOLO EX,rare holo ex", []string{"rare holo ex", "RARE HOLO EX"}},
		{"uncommon,rare holo ex,Bogus,Rare Holo EX,common", []string{"Uncommon", "rare holo ex", "Bogus", "Rare Holo EX", "Common"}},
		{"Rare Holo EX,Rare Holo EX,Rare Holo ex,Rare Holo ex", []string{"Rare Holo EX", "Rare Holo ex"}},
	}
	for _, tc := range cases {
		if got := rarityOf(t, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("rarity=%+q: Rarity = %+q, want %+q", tc.in, got, tc.want)
		}
	}
}

// The distinct members of the list come out in the order they went in, and a
// repeat (in any spelling that maps to the same value) drops out, keeping the
// first position.
func TestRarityOrderAndRepeats(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Rare Holo,rare holo,RARE HOLO", []string{"Rare Holo"}},
		{"rare holo,Rare Holo,common,COMMON", []string{"Rare Holo", "Common"}},
		{"uncommon,common,rare,promo", []string{"Uncommon", "Common", "Rare", "Promo"}},
		{"promo,rare,common,uncommon", []string{"Promo", "Rare", "Common", "Uncommon"}},
		{"Bogus,bogus,Bogus", []string{"Bogus", "bogus"}}, // unknown: byte-exact, so case differs
		{"zzz,common,zzz,Common,aaa", []string{"zzz", "Common", "aaa"}},
		{"common, ,common,,", []string{"Common"}},
		{"rare,rare holo,rare", []string{"Rare", "Rare Holo"}}, // a prefix is not the same value
		{"rare holo,rare", []string{"Rare Holo", "Rare"}},
	}
	for _, tc := range cases {
		if got := rarityOf(t, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("rarity=%q: Rarity = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Members that are empty, only whitespace, or huge: nothing is invented, and a
// long member is kept whole.
func TestRarityDegenerateMembers(t *testing.T) {
	long := strings.Repeat("rare holo ", 100000) // 1 MB, folds to a value plus a lot of text
	for name, tc := range map[string]struct {
		in   string
		want []string
	}{
		"empty":                          {"", nil},
		"one comma":                      {",", nil},
		"only spaces":                    {"   ", nil},
		"only unicode spaces":            {"\u00a0\u2003\n\t", nil},
		"only commas and space":          {" , \t,\u00a0,", nil},
		"1 MB of repeats stays verbatim": {long, []string{strings.TrimSpace(long)}},
		"1 MB member beside a real one":  {"common," + long, []string{"Common", strings.TrimSpace(long)}},
	} {
		got := rarityOf(t, tc.in)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Rarity differs: %d members, want %d", name, len(got), len(tc.want))
		}
	}
}

// A comma that arrives percent-encoded is decoded by the query parser before
// the list is split, so it separates members exactly as a bare comma does.
func TestRarityEncodedCommaSeparatesMembers(t *testing.T) {
	enc, err := url.ParseQuery("rarity=rare+holo%2CCOMMON%2c%20uncommon")
	if err != nil {
		t.Fatal(err)
	}
	p, errs := ParseParams(enc)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if want := []string{"Rare Holo", "Common", "Uncommon"}; !reflect.DeepEqual(p.Rarity, want) {
		t.Errorf("Rarity = %q, want %q", p.Rarity, want)
	}
}

// For every stored value: it is its own result, its lower-case, upper-case,
// title-case and re-spaced spellings resolve to it, and the single fold
// collision in the list is the EX pair. A new collision would silently
// disable spellings of both values, so it is counted here.
func TestEveryStoredRarityIsReachableBySpelling(t *testing.T) {
	byFold := map[string][]string{}
	for _, r := range CanonicalRarities {
		k := strings.ToLower(strings.Join(strings.Fields(r), " "))
		byFold[k] = append(byFold[k], r)
	}
	var collisions [][]string
	for _, vals := range byFold {
		if len(vals) > 1 {
			collisions = append(collisions, vals)
		}
	}
	if len(collisions) != 1 {
		t.Fatalf("fold collisions = %q, want only the EX pair", collisions)
	}
	if slices.Index(collisions[0], "Rare Holo EX") < 0 || slices.Index(collisions[0], "Rare Holo ex") < 0 {
		t.Fatalf("the colliding pair is %q, want the EX pair", collisions[0])
	}

	for _, r := range CanonicalRarities {
		if got := rarityOf(t, r); !reflect.DeepEqual(got, []string{r}) {
			t.Errorf("exact %q became %q", r, got)
		}
		ambiguous := strings.EqualFold(r, "Rare Holo EX")
		for _, sp := range []string{
			strings.ToLower(r), strings.ToUpper(r), titleCase(r),
			"  " + strings.ReplaceAll(r, " ", " \t ") + "\n",
			strings.ReplaceAll(strings.ToUpper(r), " ", "\u00a0"),
		} {
			got := rarityOf(t, sp)
			switch {
			case slices.Contains(CanonicalRarities, strings.TrimSpace(sp)):
				// the spelling is itself a stored value (for example the
				// lower-case half of the pair)
				if want := []string{strings.TrimSpace(sp)}; !reflect.DeepEqual(got, want) {
					t.Errorf("%q: Rarity = %q, want the exact value %q", sp, got, want)
				}
			case ambiguous:
				if len(got) != 1 || got[0] == "Rare Holo EX" || got[0] == "Rare Holo ex" {
					t.Errorf("%q: ambiguous spelling resolved to %q", sp, got)
				}
			default:
				if want := []string{r}; !reflect.DeepEqual(got, want) {
					t.Errorf("%q: Rarity = %q, want %q", sp, got, want)
				}
			}
		}
	}
}

// A member that names no stored value builds the clause it always built: the
// trimmed bytes of the member inside a terms filter, in the order given.
func TestRarityUnknownMembersBuildTheVerbatimClause(t *testing.T) {
	for _, in := range []string{
		"Bogus", " Bogus ", "Not  A Rarity", "rare holo lvx", "mega attack rare",
		"rare holo ex", "Bogus,rare holo ex,common", "\"quoted\"", `back\slash`,
		"\u00e9\u00e8", "a\u2028b", "\x00nul", "rare holo\u200b",
	} {
		p, errs := ParseParams(url.Values{"rarity": {in}})
		if len(errs) != 0 {
			t.Fatalf("%q: %v", in, errs)
		}
		var members []string
		for _, m := range splitList(in) {
			// the parser maps stored spellings; none of these is one
			if raw := strings.ToLower(strings.Join(strings.Fields(m), " ")); raw == "common" {
				m = "Common"
			}
			members = append(members, m)
		}
		clause, err := json.Marshal(map[string]any{"terms": map[string]any{"rarity": members}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(BuildQuery(p))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), string(clause)) {
			t.Errorf("rarity=%q\n DSL %s\n lacks %s", in, got, clause)
		}
		if !json.Valid(got) {
			t.Errorf("rarity=%q builds invalid JSON", in)
		}
	}
}

// The mapping changes only rarity. The other list parameters keep reading
// their members as before: series and set are passed verbatim, and types keeps
// the earlier rules (aliases, canonical spelling, unknown members dropped).
func TestOtherListParametersAreNotFoldedLikeRarity(t *testing.T) {
	v := url.Values{
		"series": {" base ,  SCARLET  & VIOLET,base"},
		"set":    {"  BASE1 "},
		"types":  {"fire, WATER ,steel,bogus,fire"},
		"rarity": {"RARE HOLO,common"},
	}
	p, errs := ParseParams(v)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if want := []string{"base", "SCARLET  & VIOLET", "base"}; !reflect.DeepEqual(p.Series, want) {
		t.Errorf("Series = %q, want %q (verbatim, repeats kept)", p.Series, want)
	}
	if p.SetID != "BASE1" {
		t.Errorf("SetID = %q, want BASE1 (trimmed only)", p.SetID)
	}
	if want := []string{"Fire", "Water", "Metal"}; !reflect.DeepEqual(p.Types, want) {
		t.Errorf("Types = %q, want %q", p.Types, want)
	}
	if want := []string{"Rare Holo", "Common"}; !reflect.DeepEqual(p.Rarity, want) {
		t.Errorf("Rarity = %q, want %q", p.Rarity, want)
	}
	// A rarity-looking value in another list is not rewritten.
	q, _ := ParseParams(url.Values{"series": {"RARE HOLO"}, "set": {"rare holo"}})
	if !reflect.DeepEqual(q.Series, []string{"RARE HOLO"}) || q.SetID != "rare holo" {
		t.Errorf("series/set were rewritten: %q, %q", q.Series, q.SetID)
	}
}

// What a request hands back is its own copy: changing it cannot reach the
// stored list or the next request, and parsing leaves the stored list as it
// was.
func TestRarityResultDoesNotAliasStoredValues(t *testing.T) {
	before := slices.Clone(CanonicalRarities)
	first := rarityOf(t, "rare holo,common")
	first[0] = "tampered"
	first = append(first[:1], "more")
	_ = first
	if got, want := rarityOf(t, "rare holo,common"), []string{"Rare Holo", "Common"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a later request saw %q, want %q", got, want)
	}
	if !slices.Equal(CanonicalRarities, before) {
		t.Errorf("parsing changed the stored list")
	}
	// A copy of the stored list that a caller edits does not change what maps.
	cp := slices.Clone(CanonicalRarities)
	cp[slices.Index(cp, "Rare Holo")] = "Hacked"
	if got := rarityOf(t, "rare holo"); !reflect.DeepEqual(got, []string{"Rare Holo"}) {
		t.Errorf("rarity=rare holo -> %q after editing a copy", got)
	}
}

// Requests are served in parallel; parsing shares the fold index and must only
// read it.
func TestRarityParsingIsSafeInParallel(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p, _ := ParseParams(url.Values{"rarity": {"rare holo, COMMON,Rare Holo ex,bogus" + fmt.Sprint(g)}})
				want := []string{"Rare Holo", "Common", "Rare Holo ex", "bogus" + fmt.Sprint(g)}
				if !slices.Equal(p.Rarity, want) {
					t.Errorf("Rarity = %q, want %q", p.Rarity, want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// The rarity list is the one field whose output is tied to the stored list:
// a member is a stored value, or it names no single stored value and is a
// trimmed member of the input. Parsing what the parser returned changes
// nothing.
func FuzzRarityMapping(f *testing.F) {
	for _, seed := range []string{
		"", ",", "Rare Holo", "rare holo", "RARE  HOLO", "rare holo ex", "Rare Holo EX,Rare Holo ex",
		"mega_attack_rare", "rare holo lv.x", "rare\u00a0holo", "\u0130llustration rare", "Blac\u212a White Rare",
		"common,COMMON,Common", " , ,\t", "a,b,a,b", "rare holo vstar,Rare Holo VSTAR",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		p, _ := ParseParams(url.Values{"rarity": {raw}})
		in := splitList(raw)
		for i, r := range p.Rarity {
			if slices.Index(p.Rarity, r) != i {
				t.Fatalf("%q: %q repeats %q", raw, p.Rarity, r)
			}
			if r == "" || r != strings.TrimSpace(r) || strings.Contains(r, ",") {
				t.Fatalf("%q: member %q is empty, untrimmed or holds a comma", raw, r)
			}
			if !slices.Contains(CanonicalRarities, r) && !slices.Contains(in, r) {
				t.Fatalf("%q: member %q is neither stored nor given", raw, r)
			}
			// What the parser kept as given must not be a spelling of a value
			// it could have mapped.
			if !slices.Contains(CanonicalRarities, r) {
				for _, c := range CanonicalRarities {
					folded := strings.ToLower(strings.Join(strings.Fields(r), " "))
					if folded == strings.ToLower(c) && !strings.EqualFold(c, "Rare Holo EX") {
						t.Fatalf("%q: member %q kept as given although it spells %q", raw, r, c)
					}
				}
			}
		}
		again, _ := ParseParams(url.Values{"rarity": {strings.Join(p.Rarity, ",")}})
		if !slices.Equal(again.Rarity, p.Rarity) {
			t.Fatalf("%q: parsing the result again gave %q, first %q", raw, again.Rarity, p.Rarity)
		}
		// The request must always build valid JSON.
		if b, err := json.Marshal(BuildQuery(p)); err != nil || !json.Valid(b) {
			t.Fatalf("%q: DSL does not marshal: %v", raw, err)
		}
	})
}
