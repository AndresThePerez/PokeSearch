//go:build relevance

package relevance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
)

// servedBaselineFile and shadowBaselineFile are the two runs, side by side
// under docs/relevance. They are file names rather than paths because both are
// resolved against the module root: go test's working directory is the package
// directory, so a bare "docs/relevance/..." would land inside internal/relevance.
const (
	servedBaselineFile = "baseline.json"
	shadowBaselineFile = "baseline-v2.json"
)

// relevanceDoc resolves one of those two names to where it actually lives.
func relevanceDoc(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "docs", "relevance", name)
}

// readRecordedBaseline reads a run back off disk. The served side of this
// comparison is read rather than re-measured on purpose: _rank_eval is
// deterministic against an unchanged index, so the recorded file *is* the
// served measurement, and re-running it here would only score the same corpus
// with the same query builder a second time and invite the two copies to
// disagree about which one the ADR quotes.
func readRecordedBaseline(t *testing.T, path string) baselineReport {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v\nRun TestRelevanceBaseline first: the shadow run is a comparison, "+
			"and there is nothing to compare against until the served index has been measured.", path, err)
	}
	var report baselineReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return report
}

// assertComparable refuses to print a delta between two runs that are not
// measuring the same thing. A table subtracting one judgment set from another,
// or one evaluation window from another, would still render — it would simply
// be arithmetic on unrelated numbers, and every conclusion drawn from it would
// be wrong in a way no reader could see.
func assertComparable(t *testing.T, served, shadow baselineReport, queries int) {
	t.Helper()
	if served.Index != esindex.IndexName {
		t.Fatalf("%s records index %q, not the served index %q",
			relevanceDoc(t, servedBaselineFile), served.Index, esindex.IndexName)
	}
	if shadow.Index != esindex.IndexNameV2 {
		t.Fatalf("the shadow run measured index %q, not %q; POKESEARCH_INDEX did not take",
			shadow.Index, esindex.IndexNameV2)
	}
	if served.K != shadow.K {
		t.Fatalf("the two runs used different windows: served k=%d, shadow k=%d", served.K, shadow.K)
	}
	if served.Queries != queries || shadow.Queries != queries {
		t.Fatalf("the two runs scored different judgment sets: served %d queries, shadow %d, file holds %d; "+
			"re-run TestRelevanceBaseline so both sides describe the set on disk",
			served.Queries, shadow.Queries, queries)
	}
	// The shadow index was built by reindexing the served one, so a document
	// count that moved means the two are no longer the same corpus and the
	// delta below would be measuring content rather than analysis.
	if served.Docs != shadow.Docs {
		t.Errorf("%s holds %d documents and %s holds %d; the delta would be a corpus difference, not an analysis one",
			served.Index, served.Docs, shadow.Index, shadow.Docs)
	}
}

// stratumMetrics indexes a report's strata by name so two reports can be read
// row against row.
func stratumMetrics(report baselineReport) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(report.Strata))
	for _, s := range report.Strata {
		out[s.Name] = s.Metrics
	}
	return out
}

// logShadowDelta prints the whole breakdown, one table per reported metric,
// every stratum in every table.
//
// It prints all seven strata rather than the movers, and it prints every metric
// rather than the headline, because the failure mode this comparison exists to
// catch is a gain in one stratum hiding a loss in another. A summary that only
// showed what improved would be the same mistake in a different font.
//
// unratedKey gets a table of its own for the reason doc.go gives: it is a count
// of window hits no judgment covers, not a score, and a metric that moved can
// only be read beside it. On the shadow index it is the measurement's own
// caveat — the judgment set was pooled from windows the served analysis chain
// returned, so a chain that surfaces different cards is scored as if the cards
// it surfaced were irrelevant, whether or not anybody has looked at them.
func logShadowDelta(t *testing.T, served, shadow baselineReport) {
	t.Helper()
	servedStrata, shadowStrata := stratumMetrics(served), stratumMetrics(shadow)
	counts := make(map[string]int, len(shadow.Strata))
	for _, s := range shadow.Strata {
		counts[s.Name] = s.Queries
	}

	t.Logf("served  index=%-9s docs=%d  queries=%d  k=%d", served.Index, served.Docs, served.Queries, served.K)
	t.Logf("shadow  index=%-9s docs=%d  queries=%d  k=%d", shadow.Index, shadow.Docs, shadow.Queries, shadow.K)

	for _, key := range append(metricKeys(), unratedKey) {
		format := "%-12.6f %-12.6f %+.6f"
		if key == unratedKey {
			format = "%-12.0f %-12.0f %+.0f"
		}
		t.Logf("%s  (%s -> %s)", key, served.Index, shadow.Index)
		t.Logf("%-26s %5s  %-12s %-12s %s", "stratum", "n", served.Index, shadow.Index, "delta")
		for _, s := range strata {
			before, after := servedStrata[s][key], shadowStrata[s][key]
			t.Logf("%-26s %5d  "+format, s, counts[s], before, after, after-before)
		}
		before, after := served.Metrics[key], shadow.Metrics[key]
		t.Logf("%-26s %5d  "+format, "OVERALL", shadow.Queries, before, after, after-before)
	}
}

// TestShadowIndexScore scores the shadow analyzer index against the served one.
//
// Both sides run the same judged set through the same search.BuildQuery, over
// the same 20,324 documents, with the same boosts and the same window, so the
// only thing that differs between the two numbers is the analysis chain the
// documents were indexed with. That is the entire design of this test: it is
// not a better ranking being demonstrated, it is one variable being changed.
//
// The judgments are deliberately not touched. They were pooled from windows the
// served chain returned, which biases this comparison against the shadow index
// — a card the new chain surfaces that nobody has graded scores as irrelevant.
// Re-pooling to suit the index under test would be marking its homework, so the
// bias is measured instead: unrated@10 is reported per stratum and read as the
// caveat it is.
func TestShadowIndexScore(t *testing.T) {
	set := loadJudgments(t)

	// Read the served run before the environment is repointed, so this cannot
	// accidentally read the file the shadow run is about to write.
	served := readRecordedBaseline(t, relevanceDoc(t, servedBaselineFile))

	// Everything downstream — the _rank_eval target, the index named in every
	// rating, the _mget id check, the document count and the written report —
	// reads these two, which is what lets the measurement be reused whole.
	t.Setenv("POKESEARCH_INDEX", esindex.IndexNameV2)
	t.Setenv("POKESEARCH_BASELINE_OUT", relevanceDoc(t, shadowBaselineFile))

	shadow := measureBaseline(t)

	assertComparable(t, served, shadow, len(set.Queries))
	logShadowDelta(t, served, shadow)

	if n := shadow.Metrics[unratedKey]; n > 0 {
		t.Logf("%s: %.0f of the %d evaluated window slots hold a card no judgment covers. "+
			"Those slots score as irrelevant because nobody graded them, not because anybody judged them so, "+
			"which is the known bias of a pool drawn from the other index's windows. "+
			"Read every score below it with that in mind.",
			unratedKey, n, len(set.Queries)*shadow.K)
	}

	// Written last so the file on disk and the table above cannot disagree.
	t.Logf("shadow run written to %s", relevanceDoc(t, shadowBaselineFile))
}

// TestShadowIndexIsNotServed is the guard on the claim the README and ADR 11
// both make: this chain is demonstrated, not deployed. The server compiles the
// served index name in, so if the two constants ever became the same string,
// the shadow index would quietly be the live one and every "not what the live
// deployment serves" sentence in the documentation would be false.
func TestShadowIndexIsNotServed(t *testing.T) {
	if esindex.IndexNameV2 == esindex.IndexName {
		t.Fatalf("the shadow index and the served index are both %q", esindex.IndexName)
	}
	if esindex.IndexNameV2 == "" {
		t.Fatalf("the shadow index has no name")
	}
}
