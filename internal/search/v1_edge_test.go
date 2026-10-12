package search

import (
	"fmt"
	"strings"
	"testing"
)

// A nil or empty id list must still encode as an array: the body is the whole
// contract with _mget, and "ids":null is a 400 from the cluster.
func TestEdgeBuildCardsMgetNilIDsEncodeAsEmptyArray(t *testing.T) {
	if got, want := canonV(t, BuildCardsMget(nil)), canonS(t, `{"ids":[]}`); got != want {
		t.Errorf("BuildCardsMget(nil) = %s, want %s", got, want)
	}
}

// The ids parameter limit counts distinct ids: duplicates, in any position and
// in any quantity, must collapse before the limit applies, and the 101st
// distinct id must be the thing that rejects the list.
func TestEdgeParseCardIDsLimitCountsDistinctAfterManyDuplicates(t *testing.T) {
	var parts []string
	for i := range MaxCardIDs {
		parts = append(parts, fmt.Sprintf("s-%d", i))
	}
	atLimit := append([]string{}, parts...)
	// Interleave a thousand duplicates, including of the first and the last.
	for range 1000 {
		atLimit = append(atLimit, " s-0 ", "s-99")
	}
	got, err := ParseCardIDs(strings.Join(atLimit, ","))
	if err != nil {
		t.Fatalf("100 distinct ids plus duplicates rejected: %v", err)
	}
	if len(got) != MaxCardIDs || got[0] != "s-0" || got[MaxCardIDs-1] != "s-99" {
		t.Errorf("got %d ids, first %q last %q; want 100 in first-occurrence order", len(got), got[0], got[len(got)-1])
	}

	over := append(atLimit, "s-100")
	if _, err := ParseCardIDs(strings.Join(over, ",")); err == nil {
		t.Error("101 distinct ids behind 2000 duplicates accepted, want an error")
	}
}

// Blank members are malformed, not skippable: whitespace-only, tab-only and
// empty segments anywhere in the list reject the whole list.
func TestEdgeParseCardIDsBlankSegmentsReject(t *testing.T) {
	for _, raw := range []string{"a, ,b", "a,\t,b", ",a", "a,", " ,a", "a,,", "a,\n,b", ",,,", "\t"} {
		if ids, err := ParseCardIDs(raw); err == nil {
			t.Errorf("ParseCardIDs(%q) = %v, want an error", raw, ids)
		}
	}
}

// 128 characters is the longest id and 129 the first rejected, inside a list
// as well as alone, and a list must not be able to smuggle a long id past the
// pattern by trimming (the trimmed id is what is measured).
func TestEdgeParseCardIDsLengthBoundaryInsideList(t *testing.T) {
	ok128 := "a" + strings.Repeat("b", 127)
	bad129 := ok128 + "c"
	if ids, err := ParseCardIDs("x-1," + ok128); err != nil || len(ids) != 2 || ids[1] != ok128 {
		t.Errorf("128-char member: ids=%v err=%v, want accepted", ids, err)
	}
	if ids, err := ParseCardIDs("x-1,  " + ok128 + "  "); err != nil || ids[1] != ok128 {
		t.Errorf("128-char member padded with spaces: ids=%v err=%v, want accepted trimmed", ids, err)
	}
	if ids, err := ParseCardIDs("x-1," + bad129); err == nil {
		t.Errorf("129-char member accepted: %d ids", len(ids))
	}
}
