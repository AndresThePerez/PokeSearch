//go:build relevance

package relevance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// The sweep answers the question ADR 9 left open. The three named boosts encode
// an intended ordering — exact above prefix above fuzzy above card text — and
// nothing had ever checked that ordering, or the sizes inside it, against a
// judged set. This replays the judged queries at every point of a small grid
// around the served weights and prints what each point scores.
//
// It rewrites the boosts client-side rather than asking internal/search for a
// knob. ADR 2 keeps that package a pure builder with no configuration surface,
// and a one-off measurement is not a reason to grow one. Every body is built by
// BuildQuery exactly as the baseline builds it, and only then is the boost of
// each named should clause overwritten — so what is evaluated at a grid point
// is the served query with its three swept boosts rewritten, which is precisely
// what adopting that point would produce.
//
// Three points matter to the output and they are not the same point:
//
//	reference  what was served before this branch, referencePoint below. Every
//	           "before" figure and every delta is measured against it, and it
//	           does not move when weights are adopted — otherwise the next sweep
//	           would compare the new weights against themselves.
//	served     what BuildQuery produces today, read out of the builder rather
//	           than restated. It breaks ties: equal scores leave it in place.
//	adopted    what the run says to serve. It must have unrated@10 = 0.
//
// That last rule is the lesson of this package's own history. A point whose
// window holds cards nobody judged has not been measured against the pool: its
// score counts those cards as irrelevant because nobody looked, which is not
// the same as looking and finding them irrelevant. Such a point is a request to
// re-pool the judgment set and run again, and the run says so loudly rather
// than letting a half-judged window be adopted on a number.

// headlineMetric decides the winner. ADR 9 names nDCG@10 the headline because
// it is the only one of the three that reads both the grade of a hit and where
// the hit landed; precision@10 and MRR@10 are reported for every point and
// every stratum but do not vote. It is named here rather than taken as
// metricDefs[0] so that the choice is a stated decision instead of an accident
// of list order.
const headlineMetric = "ndcg@10"

// servedProbeQuery is any query that takes the text-search path, used only to
// read the boosts out of the body the server builds. Which query it is cannot
// matter: the boosts are literals in Branches and do not depend on q.
const servedProbeQuery = "charizard"

// boostPoint is one grid point: a boost for each of the three named branches
// the sweep moves. The fourth branch, text, keeps ES's implicit 1 at every
// point. Leaving it fixed is what makes the other three readable — a boost is
// only ever a ratio, and text's 1 is the unit they are ratios of.
type boostPoint struct {
	exact, prefix, fuzzy float64
}

func (p boostPoint) String() string {
	return fmt.Sprintf("%g/%g/%g", p.exact, p.prefix, p.fuzzy)
}

// boosts maps the point onto the clause names ES knows the branches by, which
// are the same names Branch.Name carries and the same ones the grid renders as
// badges.
func (p boostPoint) boosts() map[string]float64 {
	return map[string]float64{
		"exact":      p.exact,
		"prefix":     p.prefix,
		"fuzzy-name": p.fuzzy,
	}
}

// The grid is geometric and centred on the served point: each swept boost takes
// half its current value, its current value, and double it, for twenty-seven
// points in all. Halving and doubling rather than stepping by one because what
// a boost sets is a ratio — against the other branches and above all against
// the text branch's fixed 1 — and a ratio moves on a log scale. A finer grid
// would mostly measure the judgment set's own noise; a wider one would spend
// its points on orderings ADR 9 already rejects on argument.
var (
	exactValues  = []float64{4, 8, 16}
	prefixValues = []float64{2, 4, 8}
	fuzzyValues  = []float64{1.5, 3, 6}
)

// referencePoint is the weighting this branch inherited — exact 8, prefix 4,
// fuzzy-name 3 — the point that was served before any of this work. It is a
// fixed historical fact, not a reading of the current code, and that is the
// whole point of writing it down: it stays put when a weight is adopted, so a
// second sweep still reports movement against the same "before" the first one
// did. A delta against whatever happens to be served would go to zero the
// moment a point was adopted and would say nothing.
var referencePoint = boostPoint{exact: 8, prefix: 4, fuzzy: 3}

// sweepGrid is the full cross product, in a fixed order so two runs print the
// same table in the same sequence.
func sweepGrid() []boostPoint {
	points := make([]boostPoint, 0, len(exactValues)*len(prefixValues)*len(fuzzyValues))
	for _, e := range exactValues {
		for _, p := range prefixValues {
			for _, f := range fuzzyValues {
				points = append(points, boostPoint{exact: e, prefix: p, fuzzy: f})
			}
		}
	}
	return points
}

// stratumLabels shorten ADR 9's seven stratum names into table columns. The
// per-stratum tables put every grid point down the side, so the full names
// would not fit a line anybody reads.
var stratumLabels = map[string]string{
	"exact name":              "exact",
	"prefix":                  "prefix",
	"typo":                    "typo",
	"attack text":             "attack",
	"artist":                  "artist",
	"set name":                "set",
	"natural-language intent": "intent",
}

// shouldClauses digs the branch list out of an evaluated body. It asserts the
// shape rather than assuming it, so a change to how BuildQuery nests the text
// query fails here loudly instead of quietly sweeping nothing.
func shouldClauses(t *testing.T, q string, body map[string]any) []any {
	t.Helper()
	query, ok := body["query"].(map[string]any)
	if !ok {
		t.Fatalf("query %q: the evaluated body carries no query object: %#v", q, body["query"])
	}
	boolQuery, ok := query["bool"].(map[string]any)
	if !ok {
		t.Fatalf("query %q: the evaluated query is not a bool query: %#v", q, query)
	}
	should, ok := boolQuery["should"].([]any)
	if !ok || len(should) == 0 {
		t.Fatalf("query %q: the bool query has no should clauses: %#v", q, boolQuery)
	}
	return should
}

// namedOptions finds the options map inside one should clause — the map
// carrying that clause's _name, which is also the map a boost belongs in.
// Every clause nests it differently (a term clause under its field name, a
// multi_match at the top), so this walks for it: the branch names are the
// contract, their nesting is an implementation detail of each query type.
func namedOptions(clause any) (string, map[string]any) {
	m, ok := clause.(map[string]any)
	if !ok {
		return "", nil
	}
	if name, ok := m["_name"].(string); ok {
		return name, m
	}
	for _, child := range m {
		if name, options := namedOptions(child); options != nil {
			return name, options
		}
	}
	return "", nil
}

// boostOf reads a boost that BuildQuery wrote as an untyped int literal or a
// rewrite wrote as a float64, and reads a missing boost as ES's implicit 1 —
// which is exactly what the text branch relies on.
func boostOf(t *testing.T, options map[string]any) float64 {
	t.Helper()
	switch n := options["boost"].(type) {
	case nil:
		return 1
	case int:
		return float64(n)
	case float64:
		return n
	default:
		t.Fatalf("branch %v carries a boost that is not a number: %#v", options["_name"], options["boost"])
		return 0
	}
}

// branchBoosts reads the boost every named branch of an evaluated body carries.
func branchBoosts(t *testing.T, q string, body map[string]any) map[string]float64 {
	t.Helper()
	out := make(map[string]float64)
	for _, clause := range shouldClauses(t, q, body) {
		name, options := namedOptions(clause)
		if options == nil {
			t.Fatalf("query %q: a should clause carries no _name: %#v", q, clause)
		}
		out[name] = boostOf(t, options)
	}
	return out
}

// rewriteBoosts overwrites the boost of each swept branch in place and leaves
// every other part of the body — including the text branch — untouched. It
// fails rather than skipping if a swept name is missing, because a sweep that
// silently moved two of three boosts would report a point it never evaluated.
func rewriteBoosts(t *testing.T, q string, body map[string]any, want map[string]float64) {
	t.Helper()
	rewritten := 0
	for _, clause := range shouldClauses(t, q, body) {
		name, options := namedOptions(clause)
		if options == nil {
			t.Fatalf("query %q: a should clause carries no _name: %#v", q, clause)
		}
		if boost, ok := want[name]; ok {
			options["boost"] = boost
			rewritten++
		}
	}
	if rewritten != len(want) {
		t.Fatalf("query %q: rewrote %d of %d swept boosts; the branch names in Branches moved", q, rewritten, len(want))
	}
}

// bodyAt is the sweep's body builder: the served body with this point's boosts
// written over the served ones.
func bodyAt(p boostPoint) bodyBuilder {
	want := p.boosts()
	return func(t *testing.T, q string) map[string]any {
		t.Helper()
		body := evaluatedBody(t, q)
		rewriteBoosts(t, q, body, want)
		return body
	}
}

// servedPoint reads the boosts out of the query the server actually builds
// rather than restating them here. A grid centred on a literal would drift the
// day somebody edits Branches; read this way, the run fails loudly instead.
func servedPoint(t *testing.T) boostPoint {
	t.Helper()
	got := branchBoosts(t, servedProbeQuery, evaluatedBody(t, servedProbeQuery))
	return boostPoint{exact: got["exact"], prefix: got["prefix"], fuzzy: got["fuzzy-name"]}
}

// TestSweepGridContainsTheServedPoint keeps the tie rule honest: equal scores
// leave the served point in place, which the run can only do if the served
// point was scored.
func TestSweepGridContainsTheServedPoint(t *testing.T) {
	assertInGrid(t, servedPoint(t), "served")
}

// TestSweepGridContainsTheReferencePoint is the other half of the same guard.
// Every delta the sweep prints is measured against the reference point, so the
// reference point has to be one of the points it measures.
func TestSweepGridContainsTheReferencePoint(t *testing.T) {
	assertInGrid(t, referencePoint, "reference")
}

func assertInGrid(t *testing.T, p boostPoint, role string) {
	t.Helper()
	for _, candidate := range sweepGrid() {
		if candidate == p {
			return
		}
	}
	t.Fatalf("the %s point is %s, which is not a point of the grid "+
		"(exact %v x prefix %v x fuzzy-name %v); the grid has to hold both the "+
		"weights served today and the reference they are reported against",
		role, p, exactValues, prefixValues, fuzzyValues)
}

// TestBoostRewriteTouchesOnlyTheBoosts proves the claim the whole sweep rests
// on: a grid point differs from the served query in the three swept boosts and
// in nothing else. Rewriting to a point and back must reproduce the served body
// byte for byte, or the sweep would be measuring some other query.
func TestBoostRewriteTouchesOnlyTheBoosts(t *testing.T) {
	const q = servedProbeQuery
	served := servedPoint(t)
	body := evaluatedBody(t, q)
	before := mustEncode(t, body)

	point := boostPoint{exact: 5, prefix: 7, fuzzy: 9}
	rewriteBoosts(t, q, body, point.boosts())
	got := branchBoosts(t, q, body)
	for name, want := range point.boosts() {
		if got[name] != want {
			t.Errorf("branch %q: boost %v after the rewrite, want %v", name, got[name], want)
		}
	}
	if got["text"] != 1 {
		t.Errorf("the text branch moved to %v; it is not swept and must keep ES's implicit 1", got["text"])
	}

	rewriteBoosts(t, q, body, served.boosts())
	if after := mustEncode(t, body); after != before {
		t.Errorf("rewriting to %s and back to %s did not reproduce the served body:\n got %s\nwant %s",
			point, served, after, before)
	}
}

func mustEncode(t *testing.T, body map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	return string(encoded)
}

// TestBoostSweep is the measurement. It scores every grid point with every
// metric, prints the result matrix overall and per stratum, ranks the points by
// the headline metric, and names the queries a change would cost.
func TestBoostSweep(t *testing.T) {
	assertHeadlineIsReported(t)
	set := loadJudgments(t)

	// An invented id is ignored by _rank_eval inside a 200, so this has to
	// happen before a single number is computed — for every point at once,
	// since they all score the same judgments.
	assertJudgedIDsExist(t, set)

	served := servedPoint(t)
	grid := sweepGrid()
	results := make(map[boostPoint]scored, len(grid))
	for _, p := range grid {
		results[p] = scoreSet(t, set, bodyAt(p))
	}
	assertServedPointMatchesBaseline(t, results[served])

	t.Logf("grid: exact %v x prefix %v x fuzzy-name %v = %d points, text fixed at its implicit 1",
		exactValues, prefixValues, fuzzyValues, len(grid))
	t.Logf("served point: %s", served)
	logOverall(t, grid, results, served)
	for _, key := range append(metricKeys(), unratedKey) {
		logByStratum(t, grid, results, served, key)
	}

	ranked := rankPoints(grid, results, served)
	best := ranked[0]
	headline := func(p boostPoint) float64 { return results[p].overall[headlineMetric] }
	fromReference := func(p boostPoint) float64 { return headline(p) - headline(referencePoint) }

	t.Logf("reference point: %s, at %s %.6f — every delta below is measured against it",
		referencePoint, headlineMetric, headline(referencePoint))
	if tied := tiedWith(ranked, results, best); len(tied) > 0 {
		t.Logf("TIED WITH THE BEST at %s %.6f: %s — one ranking under several labels",
			headlineMetric, headline(best), joinPoints(tied))
	}
	t.Logf("BEST: %s, at %s %.6f (%+.6f against the reference %s).",
		best, headlineMetric, headline(best), fromReference(best), referencePoint)

	// The adoption rule. A point that reached outside the judged pool has not
	// been measured against it, whatever it scored, so it cannot be adopted —
	// it is a request to judge what it surfaced and run again. Failing here
	// rather than logging is deliberate: this is exactly the mistake that costs
	// a re-pool, and it has to be impossible to read past.
	adopted, adoptable := bestAdoptable(ranked, results)
	switch {
	case !adoptable:
		t.Errorf("RE-POOL REQUIRED: every point in the grid reached outside the judged pool, "+
			"so none of them can be adopted. Judge the cards the grid surfaces until %s is 0 "+
			"at the points worth adopting, then run again.", unratedKey)
	case adopted != best:
		t.Errorf("RE-POOL REQUIRED: the best point %s scores %s %.6f but reached outside the "+
			"judged pool (%s %.0f), so it cannot be adopted on that number. Judge the cards it "+
			"surfaces and run again. The best fully judged point is %s at %.6f.",
			best, headlineMetric, headline(best), unratedKey, results[best].overall[unratedKey],
			adopted, headline(adopted))
	}
	if adoptable {
		if adopted == served {
			t.Logf("ADOPTED: the served point %s, at %s %.6f (%+.6f against the reference %s). "+
				"No boost moves.", adopted, headlineMetric, headline(adopted), fromReference(adopted), referencePoint)
		} else {
			t.Logf("ADOPTED: %s, at %s %.6f (%+.6f against the reference %s), moving %d of %d "+
				"boosts away from the served %s.", adopted, headlineMetric, headline(adopted),
				fromReference(adopted), referencePoint, boostsMoved(adopted, served), len(adopted.boosts()), served)
		}
	}

	runnerUp, hasRunnerUp := runnerUpBehind(ranked, results)
	if !hasRunnerUp {
		t.Logf("RUNNER-UP: none — every point in the grid scores the same %s", headlineMetric)
	} else {
		t.Logf("RUNNER-UP: %s, at %s %.6f (%+.6f against the reference %s, %+.6f against the best %s).",
			runnerUp, headlineMetric, headline(runnerUp), fromReference(runnerUp), referencePoint,
			headline(runnerUp)-headline(best), best)
	}

	// The two points worth a per-query table are the winner and the runner-up:
	// between them they are the whole case for changing anything, or for
	// leaving it alone. Neither has anything to compare against the reference
	// if it *is* the reference.
	reported := []boostPoint{best}
	if hasRunnerUp {
		reported = append(reported, runnerUp)
	}
	for _, p := range reported {
		if p != referencePoint {
			logQueryDeltas(t, set, results, referencePoint, p)
		}
	}
}

// bestAdoptable is the highest-ranked point whose evaluated windows are fully
// judged. Everything in the grid is reported; only these may be served.
func bestAdoptable(ranked []boostPoint, results map[boostPoint]scored) (boostPoint, bool) {
	for _, p := range ranked {
		if results[p].overall[unratedKey] == 0 {
			return p, true
		}
	}
	return boostPoint{}, false
}

// assertHeadlineIsReported keeps the deciding metric and the reported metrics
// from drifting apart: a headline nobody computes would rank every point at
// zero and declare the first one the winner.
func assertHeadlineIsReported(t *testing.T) {
	t.Helper()
	for _, m := range metricDefs {
		if m.Key == headlineMetric {
			return
		}
	}
	t.Fatalf("%s decides the sweep but is not one of the reported metrics", headlineMetric)
}

// assertServedPointMatchesBaseline ties the sweep to the recorded baseline: the
// grid point the server actually serves has to reproduce
// docs/relevance/baseline.json. _rank_eval is deterministic against an
// unchanged index, so a difference means one of the two is stale — most likely
// weights were adopted and the baseline was never re-measured — and every delta
// in this report is read against that point.
func assertServedPointMatchesBaseline(t *testing.T, served scored) {
	t.Helper()
	path := baselinePath(t)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Logf("no baseline at %s yet, so there is nothing to reconcile the served point against", path)
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var recorded baselineReport
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for key, want := range recorded.Metrics {
		if got := served.overall[key]; !closeEnough(got, want) {
			t.Errorf("the served point scores %s %v, but %s records %v; "+
				"re-run TestRelevanceBaseline so the recorded baseline describes the served weights",
				key, got, path, want)
		}
	}
}

// rankPoints orders the grid by the headline metric, best first. _rank_eval is
// deterministic against an unchanged index, so a tie is exact equality, and
// equal points are then ordered by how little they would change: the served
// point first, then whatever moves the fewest boosts away from it. A ranking
// change has to be bought with a measured improvement, so a boost that scores
// the same wherever the grid put it is a boost the sweep gives no reason to
// touch.
func rankPoints(grid []boostPoint, results map[boostPoint]scored, served boostPoint) []boostPoint {
	ranked := append([]boostPoint(nil), grid...)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := results[ranked[i]].overall[headlineMetric], results[ranked[j]].overall[headlineMetric]
		if a != b {
			return a > b
		}
		return boostsMoved(ranked[i], served) < boostsMoved(ranked[j], served)
	})
	return ranked
}

// boostsMoved counts how many of the swept boosts a point changes.
func boostsMoved(p, served boostPoint) int {
	moved := 0
	current := served.boosts()
	for name, boost := range p.boosts() {
		if boost != current[name] {
			moved++
		}
	}
	return moved
}

// tiedWith lists the points scoring exactly what p scores, p excluded. An inert
// boost makes several grid labels describe one ranking, and a reader who is not
// told that will read the repetition as three independent confirmations.
func tiedWith(ranked []boostPoint, results map[boostPoint]scored, p boostPoint) []boostPoint {
	score := results[p].overall[headlineMetric]
	var tied []boostPoint
	for _, other := range ranked {
		if other != p && results[other].overall[headlineMetric] == score {
			tied = append(tied, other)
		}
	}
	return tied
}

// runnerUpBehind is the best point that does not score exactly what the winner
// scores — the second-best *ranking* rather than a second label for the winning
// one. Reporting a tied twin as the runner-up would tell a reader nothing about
// what the next-best option is worth.
func runnerUpBehind(ranked []boostPoint, results map[boostPoint]scored) (boostPoint, bool) {
	best := results[ranked[0]].overall[headlineMetric]
	for _, p := range ranked[1:] {
		if results[p].overall[headlineMetric] != best {
			return p, true
		}
	}
	return boostPoint{}, false
}

func joinPoints(points []boostPoint) string {
	labels := make([]string, 0, len(points))
	for _, p := range points {
		labels = append(labels, p.String())
	}
	return strings.Join(labels, ", ")
}

func metricKeys() []string {
	keys := make([]string, 0, len(metricDefs))
	for _, m := range metricDefs {
		keys = append(keys, m.Key)
	}
	return keys
}

// pointLabel marks the two rows a reader keeps looking back at — the point
// served today and the reference every delta is measured against — and flags a
// point whose window reached outside the judged pool, which is the one thing
// that disqualifies a row from being adopted whatever it scored.
func pointLabel(p, served boostPoint, r scored) string {
	label := p.String()
	if p == served {
		label += " ="
	}
	if p == referencePoint {
		label += " ^"
	}
	if r.overall[unratedKey] > 0 {
		label += " !"
	}
	return label
}

func logOverall(t *testing.T, grid []boostPoint, results map[boostPoint]scored, served boostPoint) {
	t.Helper()
	keys := metricKeys()
	t.Logf("OVERALL  (= served today, ^ reference, ! reached outside the judged pool and so cannot be adopted)")
	t.Logf("%-14s %-12s %-12s %-12s %s", "point", keys[0], keys[1], keys[2], unratedKey)
	for _, p := range grid {
		r := results[p]
		t.Logf("%-14s %-12.6f %-12.6f %-12.6f %.0f",
			pointLabel(p, served, r), r.overall[keys[0]], r.overall[keys[1]], r.overall[keys[2]], r.overall[unratedKey])
	}
}

func logByStratum(t *testing.T, grid []boostPoint, results map[boostPoint]scored, served boostPoint, metric string) {
	t.Helper()
	format := "%9.6f"
	if metric == unratedKey {
		format = "%9.0f"
	}
	header := fmt.Sprintf("%-14s", "point")
	for _, s := range strata {
		header += fmt.Sprintf(" %9s", stratumLabels[s])
	}
	t.Logf("%s BY STRATUM", strings.ToUpper(metric))
	t.Logf("%s", header)
	for _, p := range grid {
		r := results[p]
		row := fmt.Sprintf("%-14s", pointLabel(p, served, r))
		for _, s := range strata {
			row += " " + fmt.Sprintf(format, r.stratum[s][metric])
		}
		t.Logf("%s", row)
	}
}

// logQueryDeltas names what one point costs, query by query, against the
// reference. This is the part of the sweep that earns the decision record: a
// headline that rose says a change is worth making, and only this says what it
// is worth making at the expense of — including when the point being described
// is the one already being served.
func logQueryDeltas(t *testing.T, set judgmentSet, results map[boostPoint]scored, reference, p boostPoint) {
	t.Helper()
	from := results[reference].perQuery[headlineMetric]
	to := results[p].perQuery[headlineMetric]

	type delta struct {
		query, stratum string
		before, after  float64
	}
	moved := make([]delta, 0, len(set.Queries))
	unchanged := 0
	for _, q := range set.Queries {
		d := delta{query: q.Query, stratum: q.Stratum, before: from[q.Query], after: to[q.Query]}
		if closeEnough(d.before, d.after) {
			unchanged++
			continue
		}
		moved = append(moved, d)
	}
	// Worst first: the losses are what a reader of the decision record needs
	// named, and burying them under the wins is how a sweep sells itself.
	sort.SliceStable(moved, func(i, j int) bool {
		return moved[i].after-moved[i].before < moved[j].after-moved[j].before
	})

	losses := 0
	for _, d := range moved {
		if d.after < d.before {
			losses++
		}
	}
	t.Logf("PER-QUERY %s: %s against the reference %s — %d worse, %d better, %d unchanged",
		headlineMetric, p, reference, losses, len(moved)-losses, unchanged)
	for _, d := range moved {
		t.Logf("%+9.6f  %-26s %-24s %.6f -> %.6f", d.after-d.before, d.query, d.stratum, d.before, d.after)
	}
}
