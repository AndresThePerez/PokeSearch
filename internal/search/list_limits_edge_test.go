package search

import (
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// members joins n list members made by member(i) with commas.
func members(n int, member func(i int) string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = member(i)
	}
	return strings.Join(parts, ",")
}

func distinct(i int) string { return fmt.Sprintf("v%d", i) }
func same(int) string       { return "a" }

// The rarity and series lists take at most MaxListMembers members, counted
// after trimming and dropping empty members and before any mapping or
// dropping of repeats, so a list of one repeated member is counted in full.
func TestListMemberCountLimit(t *testing.T) {
	for _, field := range []string{"rarity", "series"} {
		for _, tc := range []struct {
			name    string
			raw     string
			refused bool
		}{
			{"64 distinct members", members(MaxListMembers, distinct), false},
			{"65 distinct members", members(MaxListMembers+1, distinct), true},
			{"64 repeats of one member", members(MaxListMembers, same), false},
			{"65 repeats of one member", members(MaxListMembers+1, same), true},
			{"65 repeats of a stored rarity", members(MaxListMembers+1, func(int) string { return "rare holo" }), true},
			{"64 members padded with blanks and spaces", strings.Repeat(" , ,", 50) + members(MaxListMembers, func(int) string { return " a " }) + ",,,", false},
			{"400 commas are no members", strings.Repeat(",,,,", 100), false},
			{"commas and whitespace only", strings.Repeat(" ,\t, ,", 100), false},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				p, errs := ParseParams(url.Values{field: {tc.raw}})
				if !tc.refused {
					if len(errs) != 0 {
						t.Fatalf("unexpected field errors %+v", errs)
					}
					return
				}
				if len(errs) != 1 || errs[0].Field != field {
					t.Fatalf("errs = %+v, want exactly one error on %q", errs, field)
				}
				if want := field + " takes at most 64 values"; errs[0].Message != want {
					t.Errorf("message = %q, want %q", errs[0].Message, want)
				}
				if len(p.Rarity) != 0 || len(p.Series) != 0 {
					t.Errorf("a refused list must not be carried on: rarity %d, series %d members", len(p.Rarity), len(p.Series))
				}
			})
		}
	}
}

// A list of only empty members is no filter at all, as before.
func TestListOfEmptyMembersIsNoFilter(t *testing.T) {
	p, errs := ParseParams(url.Values{"rarity": {strings.Repeat(",,,,", 100)}, "series": {strings.Repeat(",,,,", 100)}})
	if len(errs) != 0 || p.Rarity != nil || p.Series != nil {
		t.Fatalf("rarity %q series %q errs %+v, want no filter and no error", p.Rarity, p.Series, errs)
	}
}

// Each rarity and series member, set and id is at most MaxValueRunes
// characters after trimming, a character being a code point as for q.
func TestValueLengthLimit(t *testing.T) {
	type check struct {
		field, message string
		values         func(v string) url.Values
	}
	checks := []check{
		{"rarity", "each rarity value must be at most 128 characters", func(v string) url.Values { return url.Values{"rarity": {"Common," + v}} }},
		{"series", "each series value must be at most 128 characters", func(v string) url.Values { return url.Values{"series": {v + ",Base"}} }},
		{"set", "set must be at most 128 characters", func(v string) url.Values { return url.Values{"set": {v}} }},
		{"id", "id must be at most 128 characters", func(v string) url.Values { return url.Values{"id": {v}} }},
	}
	for _, c := range checks {
		for _, tc := range []struct {
			name    string
			value   string
			refused bool
		}{
			{"128 characters", strings.Repeat("a", MaxValueRunes), false},
			{"129 characters", strings.Repeat("a", MaxValueRunes+1), true},
			{"128 two-byte characters", strings.Repeat("é", MaxValueRunes), false},
			{"129 two-byte characters", strings.Repeat("é", MaxValueRunes+1), true},
			{"128 four-byte characters", strings.Repeat("\U0001F525", MaxValueRunes), false},
			{"129 four-byte characters", strings.Repeat("\U0001F525", MaxValueRunes+1), true},
			{"128 characters padded with whitespace", " \t　" + strings.Repeat("a", MaxValueRunes) + " \n", false},
			{"a megabyte", strings.Repeat("rare holo ", 100000), true},
		} {
			t.Run(c.field+"/"+tc.name, func(t *testing.T) {
				_, errs := ParseParams(c.values(tc.value))
				if !tc.refused {
					if len(errs) != 0 {
						t.Fatalf("unexpected field errors %+v", errs)
					}
					return
				}
				if len(errs) != 1 || errs[0].Field != c.field || errs[0].Message != c.message {
					t.Fatalf("errs = %+v, want exactly {%s %q}", errs, c.field, c.message)
				}
			})
		}
	}
}

// Accepted values within the limits come through exactly as before.
func TestValuesWithinLimitsAreUnchanged(t *testing.T) {
	long := strings.Repeat("x", MaxValueRunes)
	p, errs := ParseParams(url.Values{
		"rarity": {"rare holo," + long + ",Rare Holo"},
		"series": {" Base , " + long},
		"set":    {" " + long + " "},
		"id":     {long},
	})
	if len(errs) != 0 {
		t.Fatalf("unexpected field errors %+v", errs)
	}
	if !slices.Equal(p.Rarity, []string{"Rare Holo", long}) || !slices.Equal(p.Series, []string{"Base", long}) ||
		p.SetID != long || p.ID != long {
		t.Errorf("rarity %q series %q set %q id %q", p.Rarity, p.Series, p.SetID, p.ID)
	}
}

// A refused list or value does not hide the errors ParseParams reported
// before: those keep their place at the front, and every new limit that is
// broken is reported after them.
func TestValueLimitsKeepOtherErrors(t *testing.T) {
	over := members(MaxListMembers+1, same)
	tooLong := strings.Repeat("a", MaxValueRunes+1)
	_, errs := ParseParams(url.Values{
		"sort":   {"bogus"},
		"page":   {"two"},
		"q":      {strings.Repeat("a", MaxQueryRunes+1)},
		"rarity": {over},
		"series": {over},
		"set":    {tooLong},
		"id":     {tooLong},
	})
	want := []string{"sort", "page", "q", "rarity", "series", "set", "id"}
	if got := fieldsOf(errs); !slices.Equal(got, want) {
		t.Fatalf("fields = %q, want %q", got, want)
	}
}

// Refusing a list of 100,000 members takes time linear in its length and
// holds on to no copy of it: counting stops at the first member past the
// limit, and nothing is split into a slice first.
func TestHugeListIsRefusedCheaply(t *testing.T) {
	raw := members(100000, distinct) // about 690 KB
	for _, field := range []string{"rarity", "series"} {
		v := url.Values{field: {raw}}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, errs := ParseParams(v)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		if len(errs) != 1 || errs[0].Field != field {
			t.Fatalf("%s: errs = %+v, want one error on %s", field, errs, field)
		}
		if elapsed > 5*time.Second {
			t.Errorf("%s: refusing took %v", field, elapsed)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<10 {
			t.Errorf("%s: refusing a %d-byte list allocated %d bytes, want under 64 KiB", field, len(raw), alloc)
		}
	}
}
