//go:build acceptance

package acceptance

// These tests read the repository's own files and never touch the network, so
// they pass without a live stack: they pin the freeze-anchor invariant that the
// seed ref, the CI size assertion, the prose counts and the Go constants all
// name one corpus. Run alone with:
//
//	go test -tags acceptance -run TestCorpusPin ./internal/acceptance

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	searchpkg "github.com/AndresThePerez/pokesearch/internal/search"
)

const pinnedRef = "39a26a144c8b6ef6c2fb17b2c29d0bb7121e3a11"

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// withCommas renders 20635 as "20,635".
func withCommas(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	return s[:len(s)-3] + "," + s[len(s)-3:]
}

func submatch(t *testing.T, re, src, what string) []string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s: pattern %q not found", what, re)
	}
	return m
}

// TestCorpusPinSeedRefAndCountsNameOneCorpus demands that the compose seed ref,
// the compose comment count, the workflow prose count, the workflow's healthz
// assertion and the Go constants never drift apart.
func TestCorpusPinSeedRefAndCountsNameOneCorpus(t *testing.T) {
	compose := repoFile(t, "docker-compose.yml")
	workflow := repoFile(t, ".github/workflows/acceptance.yml")
	self := repoFile(t, "internal/acceptance/acceptance_test.go")

	refs := regexp.MustCompile(`"-ref", "([0-9a-f]+)"`).FindAllStringSubmatch(compose, -1)
	if len(refs) != 1 {
		t.Fatalf("compose has %d seed -ref pins, want exactly 1", len(refs))
	}
	if refs[0][1] != pinnedRef {
		t.Errorf("compose seed ref = %q, want %q", refs[0][1], pinnedRef)
	}
	if len(refs[0][1]) != 40 {
		t.Errorf("compose seed ref has %d hex chars, want a full 40-char sha", len(refs[0][1]))
	}

	if got := submatch(t, `expected count, ([0-9,]+)\)`, compose, "compose comment")[1]; got != withCommas(wantDocs) {
		t.Errorf("compose comment count = %s, want %s", got, withCommas(wantDocs))
	}
	if got := submatch(t, `indexes ([0-9,]+) documents`, workflow, "workflow prose")[1]; got != withCommas(wantDocs) {
		t.Errorf("workflow prose count = %s, want %s", got, withCommas(wantDocs))
	}
	if got := submatch(t, `\[ "\$docs" = "([0-9]+)" \]`, workflow, "workflow healthz check")[1]; got != strconv.Itoa(wantDocs) {
		t.Errorf("workflow healthz assertion = %s, want %d", got, wantDocs)
	}
	if got := submatch(t, `pinned ([0-9,]+)-card index`, self, "acceptance doc comment")[1]; got != withCommas(wantDocs) {
		t.Errorf("acceptance doc comment = %s-card, want %s", got, withCommas(wantDocs))
	}
	// T01's measurement of the pinned ref: 20635 cards, 176 sets, 44 rarities.
	if wantDocs != 20635 || wantSets != 176 || wantRarities != 44 {
		t.Errorf("constants docs/sets/rarities = %d/%d/%d, want 20635/176/44", wantDocs, wantSets, wantRarities)
	}
}

// TestCorpusPinLeavesNoSupersededLiteral demands that no file the freeze anchor
// owns still carries the superseded corpus (20324 docs, ref 0af6250a, 173 sets,
// 38 rarities) as a live value, and that the tuned thresholds did not move.
func TestCorpusPinLeavesNoSupersededLiteral(t *testing.T) {
	// Assembled from parts so this file does not itself contain the literals.
	stale := []string{"20" + "324", "20," + "324", "0af6" + "250a"}
	for _, rel := range []string{
		"docker-compose.yml",
		".github/workflows/acceptance.yml",
		"internal/acceptance/acceptance_test.go",
		"internal/search/query.go",
	} {
		src := repoFile(t, rel)
		for _, s := range stale {
			if strings.Contains(src, s) {
				t.Errorf("%s still contains superseded corpus literal %q", rel, s)
			}
		}
	}
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"wantSeries", wantSeries, 17}, {"wantTypes", wantTypes, 11}, {"wantClasses", wantClasses, 3},
		{"wantMaxHP", wantMaxHP, 380},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want unchanged %d", c.name, c.got, c.want)
		}
	}
	if wantFirstYear != "1999" || wantLastYear != "2026" {
		t.Errorf("year span = %s-%s, want 1999-2026", wantFirstYear, wantLastYear)
	}
	workflow := repoFile(t, ".github/workflows/acceptance.yml")
	for _, line := range []string{`NDCG_TOLERANCE: "0.01"`, `MAX_UNRATED: "0"`} {
		if strings.Count(workflow, line) != 1 {
			t.Errorf("workflow line %q must appear exactly once, unchanged", line)
		}
	}
}

// TestCorpusPinFacetCommentMatchesRegistry demands that the numbers in the
// Facets comment ("rarity 44 of 100", "sets 176 of 200", ...) equal the live
// cardinality constants and the configured terms sizes, and that every size
// still covers its cardinality at the pinned corpus.
func TestCorpusPinFacetCommentMatchesRegistry(t *testing.T) {
	src := repoFile(t, "internal/search/query.go")
	start := strings.Index(src, "// Facets is the registry.")
	if start < 0 {
		t.Fatal("Facets registry comment not found")
	}
	end := strings.Index(src[start:], "var Facets")
	if end < 0 {
		t.Fatal("var Facets not found after its comment")
	}
	comment := strings.NewReplacer("\n//", " ").Replace(src[start : start+end])

	live := map[string]int{
		"types": wantTypes, "rarity": wantRarities, "series": wantSeries, "sets": wantSets,
	}
	sizeByKey := map[string]int{}
	for _, f := range searchpkg.Facets {
		switch f.Name {
		case "types":
			sizeByKey["types"] = f.Size
		case "rarity":
			sizeByKey["rarity"] = f.Size
		case "set_series":
			sizeByKey["series"] = f.Size
		case searchpkg.SetsFacet:
			sizeByKey["sets"] = f.Size
		}
	}
	for key, cardinality := range live {
		m := submatch(t, fmt.Sprintf(`\b%s (\d+) of (\d+)\b`, key), comment, "facet comment "+key)
		gotLive, _ := strconv.Atoi(m[1])
		gotSize, _ := strconv.Atoi(m[2])
		if gotLive != cardinality {
			t.Errorf("comment says %s %d, live constant is %d", key, gotLive, cardinality)
		}
		if gotSize != sizeByKey[key] {
			t.Errorf("comment says %s of %d, registry size is %d", key, gotSize, sizeByKey[key])
		}
		if sizeByKey[key] < cardinality {
			t.Errorf("facet %s: terms size %d is below cardinality %d", key, sizeByKey[key], cardinality)
		}
	}
}
