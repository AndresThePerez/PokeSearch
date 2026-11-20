package search

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// listOf is n members of runes characters each, made of unit after a short
// prefix that tells them apart.
func listOf(unit string, runes, n int) []string {
	out := make([]string, n)
	for i := range out {
		id := strconv.Itoa(i)
		body := strings.Repeat(unit, (runes-len(id))/utf8.RuneCountInString(unit))
		out[i] = id + body + strings.Repeat("z", runes-len(id)-utf8.RuneCountInString(body))
	}
	return out
}

func totalRunes(list []string) int {
	n := 0
	for _, m := range list {
		n += utf8.RuneCountInString(m)
	}
	return n
}

// The total is counted in characters, whatever the encoding: members that
// together make exactly MaxListRunes characters of 1-, 2-, 3- or 4-byte
// UTF-8, combining marks or invalid bytes are accepted, and one character more
// is refused with the total's message, for both lists.
func TestListTotalCountsCharacters(t *testing.T) {
	for name, unit := range map[string]string{
		"1-byte": "a", "2-byte": "é", "3-byte": "語", "4-byte": "🜲",
		"combining": "é", "invalid byte": "\xff", "markup": "<",
	} {
		for _, field := range []string{"rarity", "series"} {
			t.Run(name+" "+field, func(t *testing.T) {
				exact := listOf(unit, MaxValueRunes, MaxListRunes/MaxValueRunes)
				if totalRunes(exact) != MaxListRunes {
					t.Fatalf("bad fixture: %d characters", totalRunes(exact))
				}
				p, errs := ParseParams(url.Values{field: {strings.Join(exact, ",")}})
				if len(errs) != 0 {
					t.Fatalf("%d characters refused: %v", MaxListRunes, errs)
				}
				if got := p.Rarity; field == "series" {
					if len(p.Series) != len(exact) {
						t.Errorf("series carries %d members, want %d", len(p.Series), len(exact))
					}
				} else if len(got) != len(exact) {
					t.Errorf("rarity carries %d members, want %d", len(got), len(exact))
				}
				over := append(exact, "x")
				p, errs = ParseParams(url.Values{field: {strings.Join(over, ",")}})
				if len(errs) != 1 || errs[0].Field != field ||
					errs[0].Message != field+" values may total at most 1024 characters" {
					t.Fatalf("%d characters: errors %v, want one total error on %s", MaxListRunes+1, errs, field)
				}
				if len(p.Rarity) != 0 || len(p.Series) != 0 {
					t.Errorf("a list refused for its total still carries members")
				}
			})
		}
	}
}

// Only the members count: spaces trimmed from around them, empty members and
// separators add nothing to the total, however many there are.
func TestListTotalIgnoresTrimmedSpaceAndEmptyMembers(t *testing.T) {
	exact := listOf("s", MaxValueRunes, MaxListRunes/MaxValueRunes)
	var b strings.Builder
	for _, m := range exact {
		b.WriteString(" \t \u0085," + strings.Repeat(" ", 200) + m + "   ,,, ,")
	}
	for _, field := range []string{"rarity", "series"} {
		if _, errs := ParseParams(url.Values{field: {b.String()}}); len(errs) != 0 {
			t.Errorf("%s: %d characters among separator noise refused: %v", field, MaxListRunes, errs)
		}
	}
}

// Repeats are counted before they are dropped, as members are: nine copies of
// one 128-character member total 1,152 characters and are refused, even
// though they would build a one-member filter; so are 1,025 one-character
// spellings that fold onto nothing.
func TestListTotalCountsRepeats(t *testing.T) {
	long := strings.Repeat("r", MaxValueRunes)
	for _, field := range []string{"rarity", "series"} {
		_, errs := ParseParams(url.Values{field: {strings.TrimSuffix(strings.Repeat(long+",", 9), ",")}})
		if len(errs) != 1 || errs[0].Message != field+" values may total at most 1024 characters" {
			t.Errorf("%s: nine repeats of a 128-character member: errors %v, want the total error", field, errs)
		}
	}
}

// A list that breaks several limits gets exactly one error, from the first
// limit it reaches walking the list: the member count, then the member's
// length, then the running total.
func TestListBreakingSeveralLimitsGetsOneError(t *testing.T) {
	long := strings.Repeat("l", MaxValueRunes+1)
	for _, tc := range []struct {
		name, list, want string
	}{
		{"65 members of 16 characters (1,040 in all)", strings.Join(listOf("a", 16, 65), ","), "rarity takes at most 64 values"},
		{"65 members whose total passes 1,024 at the 9th", strings.Join(listOf("a", 128, 65), ","), "rarity values may total at most 1024 characters"},
		{"an over-long member first, total and count broken later", long + "," + strings.Join(listOf("a", 128, 70), ","), "each rarity value must be at most 128 characters"},
		{"8 full members, then an over-long one", strings.Join(listOf("a", 128, 8), ",") + "," + long, "each rarity value must be at most 128 characters"},
		{"8 full members, then a short one", strings.Join(listOf("a", 128, 8), ",") + ",b", "rarity values may total at most 1024 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, errs := ParseParams(url.Values{"rarity": {tc.list}, "series": {tc.list}})
			want := []FieldError{{"rarity", tc.want}, {"series", strings.ReplaceAll(tc.want, "rarity", "series")}}
			if len(errs) != 2 || errs[0] != want[0] || errs[1] != want[1] {
				t.Errorf("errors %v, want %v", errs, want)
			}
			if len(p.Rarity) != 0 || len(p.Series) != 0 {
				t.Errorf("refused lists still carry members: %d, %d", len(p.Rarity), len(p.Series))
			}
		})
	}
}

// Every size limit at its largest at once is still a valid request.
func TestEveryLimitAtItsLargestTogether(t *testing.T) {
	v := url.Values{
		"q":      {strings.Repeat("<", MaxQueryRunes)},
		"rarity": {strings.Join(listOf("<", MaxValueRunes, MaxListRunes/MaxValueRunes), ",")},
		"series": {strings.Join(listOf(">", MaxListRunes/MaxListMembers, MaxListMembers), ",")},
		"set":    {strings.Repeat("🜲", MaxValueRunes)},
		"id":     {strings.Repeat("\xff", MaxValueRunes)},
		"types":  {strings.Join(CanonicalTypes, ",")},
	}
	p, errs := ParseParams(v)
	if len(errs) != 0 {
		t.Fatalf("errors %v", errs)
	}
	if len(p.Rarity) != MaxListRunes/MaxValueRunes || len(p.Series) != MaxListMembers {
		t.Errorf("lists carry %d and %d members", len(p.Rarity), len(p.Series))
	}
}

// The rarity mapping never lets a list grow past the total: a stored value is
// never longer than the shortest spelling that maps onto it (case and inner
// spacing only), so the canonical list is at most as long as the input.
func TestRarityMappingNeverLengthensAMember(t *testing.T) {
	for _, stored := range CanonicalRarities {
		for _, spelling := range []string{stored, strings.ToLower(stored), strings.ToUpper(stored),
			strings.ReplaceAll(stored, " ", " "), strings.Join(strings.Fields(stored), "")} {
			got := canonicalRarity(strings.TrimSpace(spelling))
			if utf8.RuneCountInString(got) > utf8.RuneCountInString(spelling) {
				t.Errorf("%q maps to the longer %q", spelling, got)
			}
		}
	}
	// A list of 64 short spellings, every one a different stored value where
	// possible, folds to a canonical list no longer than its input.
	var spellings []string
	for _, stored := range CanonicalRarities {
		spellings = append(spellings, strings.ToLower(stored))
	}
	p, errs := ParseParams(url.Values{"rarity": {strings.Join(spellings, ",")}})
	if len(errs) != 0 {
		t.Fatalf("all %d rarities refused: %v", len(spellings), errs)
	}
	if totalRunes(p.Rarity) > totalRunes(spellings) {
		t.Errorf("mapping grew the list from %d to %d characters", totalRunes(spellings), totalRunes(p.Rarity))
	}
}

// The largest query any accepted request builds stays bounded: both lists at
// their total in the characters JSON writes longest, q, set and id at their
// longest, every type. The bound is generous (today's worst is about 80 KB);
// it fails when a limit is loosened, not when the query changes a little.
func TestLargestAcceptedQueryIsBounded(t *testing.T) {
	const bound = 128 << 10
	worst := 0
	for _, unit := range []string{"a", "<", "\x01", "\"", " ", "🜲", "\xff"} {
		for _, members := range []int{MaxListRunes / MaxValueRunes, 16, 32, MaxListMembers} {
			list := strings.Join(listOf(unit, MaxListRunes/members, members), ",")
			v := url.Values{
				"q": {strings.Repeat(unit, MaxQueryRunes/utf8.RuneCountInString(unit))}, "rarity": {list}, "series": {list},
				"set":   {strings.Repeat(unit, MaxValueRunes/utf8.RuneCountInString(unit))},
				"id":    {strings.Repeat(unit, MaxValueRunes/utf8.RuneCountInString(unit))},
				"types": {strings.Join(CanonicalTypes, ",")}, "supertype": {"pokemon"},
				"hp_min": {"10"}, "hp_max": {"300"}, "debug": {"1"},
			}
			p, errs := ParseParams(v)
			if len(errs) != 0 {
				continue
			}
			b, err := json.Marshal(BuildQuery(p))
			if err != nil {
				t.Fatal(err)
			}
			worst = max(worst, len(b))
		}
	}
	if worst == 0 || worst > bound {
		t.Errorf("the largest accepted query is %d bytes, want at most %d", worst, bound)
	}
}

// FuzzListTotalOverRawQuery: whatever is accepted totals at most MaxListRunes
// characters per list, before and after the rarity mapping.
func FuzzListTotalOverRawQuery(f *testing.F) {
	for _, seed := range []string{
		"rarity=" + strings.Repeat("r", 128) + strings.Repeat("%2C"+strings.Repeat("r", 128), 8),
		"series=" + strings.Repeat("%3C", 1024),
		"rarity=rare+holo,RARE%C2%A0HOLO," + strings.Repeat("x,", 63),
		"series=%FF%FF,%E2%80%A8," + strings.Repeat("%20,", 500),
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
			if refused(field) {
				continue
			}
			if in := totalRunes(splitList(v.Get(field))); in > MaxListRunes {
				t.Fatalf("%q: %s of %d characters accepted", raw, field, in)
			}
			if out := totalRunes(list); out > MaxListRunes {
				t.Fatalf("%q: %s carries %d characters", raw, field, out)
			}
		}
	})
}
