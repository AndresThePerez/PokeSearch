package search

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The v1 lookups reach Elasticsearch through one _mget whose ids travel in the
// JSON body. The body is pinned byte for byte, the same way the search DSL is:
// a drift here is a drift in what the index is asked.
func TestBuildCardsMgetGolden(t *testing.T) {
	got := canonV(t, BuildCardsMget([]string{"ex10-?", "ex10-!", "base1-4"}))
	want := canonS(t, `{"ids": ["ex10-?", "ex10-!", "base1-4"]}`)
	if got != want {
		t.Errorf("_mget body\n got %s\nwant %s", got, want)
	}

	got = canonV(t, BuildCardsMget([]string{"base1-4"}))
	want = canonS(t, `{"ids": ["base1-4"]}`)
	if got != want {
		t.Errorf("single-id _mget body\n got %s\nwant %s", got, want)
	}
}

// The builder owns its body: a caller that reuses its slice afterwards must not
// be able to change what was already built (the body is logged after the ES
// call returns).
func TestBuildCardsMgetDoesNotAliasInput(t *testing.T) {
	ids := []string{"base1-4", "ex10-?"}
	body := BuildCardsMget(ids)
	ids[0] = "changed-1"
	if got, want := canonV(t, body), canonS(t, `{"ids": ["base1-4", "ex10-?"]}`); got != want {
		t.Errorf("body changed with its input\n got %s\nwant %s", got, want)
	}
}

func TestValidCardID(t *testing.T) {
	long := "a" + strings.Repeat("b", 127)
	for _, id := range []string{"base1-4", "ex10-?", "ex10-!", "cel25c-17_A", "a", "sv3.5-1", "9", long} {
		if !ValidCardID(id) {
			t.Errorf("ValidCardID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"", "-a", ".a", "_a", "?a", "a/b", "a b", " a", "a ", "a%2Fb", "a#b", "a\n", "a\nb",
		"ä", "a,b", long + "c", "a\x00",
	} {
		if ValidCardID(id) {
			t.Errorf("ValidCardID(%q) = true, want false", id)
		}
	}
}

func TestParseCardIDs(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"base1-4", []string{"base1-4"}},
		{" base1-4 , ex10-? ,ex10-!", []string{"base1-4", "ex10-?", "ex10-!"}},
		// Duplicates collapse and the first occurrence keeps its position.
		{"b-1,a-1,b-1,c-1,a-1", []string{"b-1", "a-1", "c-1"}},
	} {
		got, err := ParseCardIDs(tc.raw)
		if err != nil {
			t.Errorf("ParseCardIDs(%q) error %v", tc.raw, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ParseCardIDs(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseCardIDsBounds(t *testing.T) {
	distinct := func(n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("set1-%d", i+1)
		}
		return ids
	}

	if MaxCardIDs != 100 {
		t.Fatalf("MaxCardIDs = %d, want 100", MaxCardIDs)
	}

	hundred := distinct(MaxCardIDs)
	got, err := ParseCardIDs(strings.Join(hundred, ","))
	if err != nil || len(got) != MaxCardIDs {
		t.Fatalf("100 distinct ids: len %d, err %v; want 100 and no error", len(got), err)
	}

	// 101 entries that collapse to 100 distinct ids are inside the limit: the
	// limit counts distinct ids, not entries.
	withDup := append(slices.Clone(hundred), hundred[42])
	if got, err := ParseCardIDs(strings.Join(withDup, ",")); err != nil || len(got) != MaxCardIDs {
		t.Errorf("101 entries, 100 distinct: len %d, err %v; want 100 and no error", len(got), err)
	}

	if _, err := ParseCardIDs(strings.Join(distinct(MaxCardIDs+1), ",")); err == nil {
		t.Error("101 distinct ids: want an error")
	}
}

func TestParseCardIDsRejects(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", "a-1,", ",a-1", "a-1,,b-1", "a-1, ,b-1", "a/b", "a-1,-b", "a-1,b c"} {
		if got, err := ParseCardIDs(raw); err == nil {
			t.Errorf("ParseCardIDs(%q) = %q, want an error", raw, got)
		}
	}
}
