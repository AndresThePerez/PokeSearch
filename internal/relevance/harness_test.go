//go:build relevance

package relevance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	searchpkg "github.com/AndresThePerez/pokesearch/internal/search"
)

// k is the evaluation window. Ten is the first page a searcher actually looks
// at, and it is the k of all three metrics; the request's own size is set to
// the same number so the window ES fetches and the window the metric reads can
// never disagree.
const k = 10

// judgmentFile is read relative to the package directory, which is go test's
// working directory.
const judgmentFile = "testdata/judgments.json"

// plainlyMeantGrade is the top of the 0..3 scale — "the card the searcher
// plainly meant". MRR counts only these, because MRR exists here to answer
// "how far down is the right answer", and a grade-2 near-miss at rank 1 is not
// the right answer. nDCG and precision read the lower grades too.
const plainlyMeantGrade = 3

// strata are ADR 9's seven named failure modes, in the order the report prints
// them. A judgment naming anything else fails validation: a stratum that only
// exists in one file is a stratum nobody can attribute a regression to.
var strata = []string{
	"exact name",
	"prefix",
	"typo",
	"attack text",
	"artist",
	"set name",
	"natural-language intent",
}

// metricDef is one _rank_eval metric. The API takes exactly one metric per
// request, so the harness posts the whole judgment set once per metric and
// groups the per-query details afterwards.
type metricDef struct {
	// Key names the metric in the printed table and in the baseline.
	Key string
	// Body is the metric object _rank_eval expects.
	Body map[string]any
}

// metricDefs is the reporting set ADR 9 fixes: nDCG@10 as the headline (it
// reads both the grade of a hit and where the hit landed), precision@10
// because it is the one number a reader interprets correctly at a glance, and
// MRR because for a name or a prefix the rank of the single right answer is
// the whole story.
//
// The thresholds are stated rather than defaulted so the request documents its
// own definition of "relevant": precision counts anything a human called
// defensible, MRR counts only the plainly-meant card.
var metricDefs = []metricDef{
	{Key: "ndcg@10", Body: map[string]any{"dcg": map[string]any{
		"k": k, "normalize": true,
	}}},
	{Key: "precision@10", Body: map[string]any{"precision": map[string]any{
		"k": k, "relevant_rating_threshold": 1,
	}}},
	{Key: "mrr@10", Body: map[string]any{"mean_reciprocal_rank": map[string]any{
		"k": k, "relevant_rating_threshold": plainlyMeantGrade,
	}}},
}

// unratedKey names the fourth column, which is a count of documents rather
// than a score in 0..1: how many hits in the evaluated windows no judgment
// covers. It sits beside the metrics because it is what makes them readable —
// a score that fell while this rose means the ranking surfaced cards nobody
// judged, not that it ranked judged cards worse.
const unratedKey = "unrated@10"

// servedKeysDropped are the two top-level keys _rank_eval refuses. Probed
// against the pinned node: a request carrying either comes back 400 ("Failed
// to build [request]"), while sort, size, track_total_hits and post_filter are
// all accepted. Neither aggregations nor highlighting influences the order of
// the hits, so dropping them evaluates the served ranking exactly.
var servedKeysDropped = []string{"aggs", "highlight"}

// servedKeysKept is every other top-level key BuildQuery is allowed to
// produce. It is an allowlist, not documentation: if the builder grows a key
// that is not in either list, TestEvaluatedBodyCoversEveryServedKey fails and
// somebody has to decide whether the new key changes the ranking, instead of
// the harness silently evaluating a query the server no longer sends.
var servedKeysKept = []string{
	"track_total_hits", "size", "sort", "from", "query", "post_filter",
}

type judgedCard struct {
	ID     string `json:"id"`
	Grade  int    `json:"grade"`
	Reason string `json:"reason"`
}

type judgedQuery struct {
	Query     string       `json:"query"`
	Stratum   string       `json:"stratum"`
	Judgments []judgedCard `json:"judgments"`
}

type judgmentSet struct {
	Queries []judgedQuery `json:"queries"`
}

func esURL() string {
	if v := os.Getenv("POKESEARCH_ES"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:9200"
}

func baseURL() string {
	if v := os.Getenv("POKESEARCH_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:8080"
}

func indexName() string {
	if v := os.Getenv("POKESEARCH_INDEX"); v != "" {
		return v
	}
	return "cards"
}

// baselinePath resolves where the run is written. The default is relative to
// the module root rather than to the package directory, because go test's
// working directory is the package — so "docs/relevance/baseline.json" alone
// would land inside internal/relevance.
func baselinePath(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("POKESEARCH_BASELINE_OUT"); v != "" {
		return v
	}
	return filepath.Join(moduleRoot(t), "docs", "relevance", "baseline.json")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %q; set POKESEARCH_BASELINE_OUT", dir)
		}
		dir = parent
	}
}

func loadJudgments(t *testing.T) judgmentSet {
	t.Helper()
	raw, err := os.ReadFile(judgmentFile)
	if err != nil {
		t.Fatalf("read %s: %v", judgmentFile, err)
	}
	var set judgmentSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("parse %s: %v", judgmentFile, err)
	}
	if len(set.Queries) == 0 {
		t.Fatalf("%s holds no queries", judgmentFile)
	}
	return set
}

// evaluatedBody is the body _rank_eval is handed for one judged query: the
// served body, minus the two keys the API refuses. It goes through ParseParams
// first so the request is canonicalized exactly as an /api/search request with
// the same q would be — sort=relevance, the id tiebreaker, all of it.
func evaluatedBody(t *testing.T, q string) map[string]any {
	t.Helper()
	params, errs := searchpkg.ParseParams(url.Values{
		"q":         {q},
		"page_size": {strconv.Itoa(k)},
	})
	if len(errs) > 0 {
		t.Fatalf("query %q: ParseParams rejected it: %+v", q, errs)
	}
	body := searchpkg.BuildQuery(params)
	for _, key := range servedKeysDropped {
		delete(body, key)
	}
	return body
}

type rankEvalDetail struct {
	MetricScore float64 `json:"metric_score"`
	// UnratedDocs are the hits inside the window that no judgment covers.
	// _rank_eval returns them on every metric, and they are the only thing in
	// the response that tells a regression from a discovery: a ranking change
	// that drops the score because it reordered judged cards is a regression,
	// while one that drops it because it surfaced cards nobody has judged is a
	// gap in the judgment set. Discarding this field would leave the two
	// indistinguishable.
	UnratedDocs []struct {
		ID string `json:"_id"`
	} `json:"unrated_docs"`
}

// rankEvalResponse is the successful shape only. Failures are read separately,
// before this is decoded, because a response carrying them also carries a
// metric_score of "NaN".
type rankEvalResponse struct {
	MetricScore float64                   `json:"metric_score"`
	Details     map[string]rankEvalDetail `json:"details"`
}

var client = &http.Client{Timeout: 60 * time.Second}

// runRankEval posts the whole judgment set as one _rank_eval request for a
// single metric. It returns the overall score, the per-query scores, and the
// per-query count of window hits no judgment covers — all three keyed by query
// text.
func runRankEval(t *testing.T, set judgmentSet, m metricDef) (overall float64, perQuery map[string]float64, unrated map[string]int) {
	t.Helper()
	index := indexName()
	requests := make([]map[string]any, 0, len(set.Queries))
	for _, q := range set.Queries {
		ratings := make([]map[string]any, 0, len(q.Judgments))
		for _, j := range q.Judgments {
			ratings = append(ratings, map[string]any{
				"_index": index,
				"_id":    j.ID,
				"rating": j.Grade,
			})
		}
		requests = append(requests, map[string]any{
			"id":      q.Query,
			"request": evaluatedBody(t, q.Query),
			"ratings": ratings,
		})
	}

	payload, err := json.Marshal(map[string]any{"requests": requests, "metric": m.Body})
	if err != nil {
		t.Fatalf("%s: encode request: %v", m.Key, err)
	}
	endpoint := fmt.Sprintf("%s/%s/_rank_eval", esURL(), index)
	res, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("%s: POST %s: %v", m.Key, endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var body bytes.Buffer
		_, _ = body.ReadFrom(res.Body)
		t.Fatalf("%s: POST %s: status %d: %s", m.Key, endpoint, res.StatusCode, body.String())
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s: read response: %v", m.Key, err)
	}
	// Failures come back inside a 200, and a failed request also turns
	// metric_score into the string "NaN" — which will not decode into a
	// float64. So the failures are read on their own first, or a wrong index
	// name would surface as a JSON type error instead of "no such index".
	var envelope struct {
		Failures map[string]json.RawMessage `json:"failures"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("%s: decode response: %v", m.Key, err)
	}
	if n := len(envelope.Failures); n > 0 {
		ids := make([]string, 0, n)
		for id := range envelope.Failures {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		t.Fatalf("%s: _rank_eval failed on %d of %d queries; first was %q: %s",
			m.Key, n, len(set.Queries), ids[0], envelope.Failures[ids[0]])
	}
	var out rankEvalResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: decode response: %v", m.Key, err)
	}
	if len(out.Details) != len(set.Queries) {
		t.Fatalf("%s: %d queries sent, %d scored", m.Key, len(set.Queries), len(out.Details))
	}

	perQuery = make(map[string]float64, len(out.Details))
	unrated = make(map[string]int, len(out.Details))
	for id, d := range out.Details {
		perQuery[id] = d.MetricScore
		unrated[id] = len(d.UnratedDocs)
	}
	return out.MetricScore, perQuery, unrated
}

// mgetBatch is how many ids go into one _mget. The judged set is a few hundred
// documents; batching keeps the request a sane size if it grows.
const mgetBatch = 500

// assertJudgedIDsExist fails the run if any judged card id is absent from the
// index being evaluated.
//
// This cannot be left to _rank_eval. A rating naming a document that does not
// exist comes back inside an HTTP 200 with no entry in failures: the rating is
// simply ignored, and the query's score silently drops as though the ranking
// had missed a card it was supposed to find. A typo in a hand-maintained
// judgment file would therefore read as a relevance regression, which is the
// most expensive kind of wrong answer this harness can give. One _mget against
// the cluster the test already has rules it out.
func assertJudgedIDsExist(t *testing.T, set judgmentSet) {
	t.Helper()
	seen := make(map[string]bool)
	ids := make([]string, 0)
	for _, q := range set.Queries {
		for _, j := range q.Judgments {
			if !seen[j.ID] {
				seen[j.ID] = true
				ids = append(ids, j.ID)
			}
		}
	}
	sort.Strings(ids) // so a failure lists the same ids in the same order twice

	index := indexName()
	endpoint := fmt.Sprintf("%s/%s/_mget?_source=false", esURL(), index)
	var missing []string
	for start := 0; start < len(ids); start += mgetBatch {
		end := min(start+mgetBatch, len(ids))
		payload, err := json.Marshal(map[string]any{"ids": ids[start:end]})
		if err != nil {
			t.Fatalf("encode _mget request: %v", err)
		}
		res, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("POST %s: %v", endpoint, err)
		}
		var out struct {
			Docs []struct {
				ID    string `json:"_id"`
				Found bool   `json:"found"`
			} `json:"docs"`
		}
		err = json.NewDecoder(res.Body).Decode(&out)
		status := res.StatusCode
		res.Body.Close()
		if status != http.StatusOK {
			t.Fatalf("POST %s: status %d", endpoint, status)
		}
		if err != nil {
			t.Fatalf("POST %s: decode: %v", endpoint, err)
		}
		for _, d := range out.Docs {
			if !d.Found {
				missing = append(missing, d.ID)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d of %d judged card ids are not in index %q: %s\n"+
			"A judgment on an id the corpus does not hold is worse than no judgment: "+
			"_rank_eval ignores the rating inside a 200 and the score drops as if the ranking had missed it.",
			len(missing), len(ids), index, strings.Join(missing, ", "))
	}
	t.Logf("all %d distinct judged card ids exist in index %q", len(ids), index)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

type buildIdentity struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
}

type seedIdentity struct {
	Ref      string `json:"ref"`
	SeededAt string `json:"seeded_at"`
}

type metaResponse struct {
	Version string        `json:"version"`
	Commit  string        `json:"commit"`
	Built   string        `json:"built"`
	Seed    *seedIdentity `json:"seed"`
}

// stratumReport is one stratum's block. Metrics holds the three scores plus
// unrated@10, which is a document count rather than a score — see unratedKey.
type stratumReport struct {
	Name    string             `json:"name"`
	Queries int                `json:"queries"`
	Metrics map[string]float64 `json:"metrics"`
}

// baselineReport is the whole written artifact. Everything in it either names
// what was measured or is a measurement: an index, its size, the build that
// served it, the shape of the judgment set, and the numbers.
type baselineReport struct {
	Index   string             `json:"index"`
	Docs    int                `json:"docs"`
	Queries int                `json:"queries"`
	K       int                `json:"k"`
	Build   buildIdentity      `json:"build"`
	Seed    *seedIdentity      `json:"seed"`
	Metrics map[string]float64 `json:"metrics"`
	Strata  []stratumReport    `json:"strata"`
}

// fetchBuildIdentity reads /api/meta so the baseline names the build that
// produced it. It is the one thing the cluster cannot answer.
func fetchBuildIdentity(t *testing.T) metaResponse {
	t.Helper()
	endpoint := baseURL() + "/api/meta"
	res, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET %s: %v (set POKESEARCH_URL to the running instance)", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", endpoint, res.StatusCode)
	}
	var out metaResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: decode: %v", endpoint, err)
	}
	return out
}

// fetchDocCount counts the index actually being evaluated rather than trusting
// /api/meta, which counts whatever index the running instance was pointed at.
func fetchDocCount(t *testing.T) int {
	t.Helper()
	endpoint := fmt.Sprintf("%s/%s/_count", esURL(), indexName())
	res, err := client.Get(endpoint)
	if err != nil {
		t.Fatalf("GET %s: %v", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", endpoint, res.StatusCode)
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s: decode: %v", endpoint, err)
	}
	return out.Count
}

// TestJudgmentSetIsWellFormed guards the file the metrics rest on. A judgment
// set with a duplicate query, an unnamed stratum, an out-of-range grade or a
// reasonless grade is not a judgment set — it is a table of numbers nobody can
// argue with.
func TestJudgmentSetIsWellFormed(t *testing.T) {
	set := loadJudgments(t)
	known := make(map[string]bool, len(strata))
	for _, s := range strata {
		known[s] = true
	}
	seenQuery := make(map[string]bool, len(set.Queries))
	for _, q := range set.Queries {
		if q.Query == "" {
			t.Errorf("a judged entry has an empty query")
			continue
		}
		// The query text is the _rank_eval request id, so it has to be unique
		// across the whole set or two queries would share a score.
		if seenQuery[q.Query] {
			t.Errorf("query %q appears twice; the query text is the _rank_eval request id", q.Query)
		}
		seenQuery[q.Query] = true
		if !known[q.Stratum] {
			t.Errorf("query %q: stratum %q is not one of ADR 9's seven", q.Query, q.Stratum)
		}
		if len(q.Judgments) == 0 {
			t.Errorf("query %q: no judgments", q.Query)
		}
		seenID := make(map[string]bool, len(q.Judgments))
		plainlyMeant := 0
		for _, j := range q.Judgments {
			if j.ID == "" {
				t.Errorf("query %q: a judgment has no card id", q.Query)
			}
			if seenID[j.ID] {
				t.Errorf("query %q: card %s judged twice", q.Query, j.ID)
			}
			seenID[j.ID] = true
			if j.Grade < 0 || j.Grade > plainlyMeantGrade {
				t.Errorf("query %q: card %s graded %d, outside 0..%d", q.Query, j.ID, j.Grade, plainlyMeantGrade)
			}
			if strings.TrimSpace(j.Reason) == "" {
				t.Errorf("query %q: card %s carries a grade with no reason", q.Query, j.ID)
			}
			if j.Grade == plainlyMeantGrade {
				plainlyMeant++
			}
		}
		// Without one, MRR can only ever score this query 0 and nDCG has no
		// ideal ranking to normalize against.
		if plainlyMeant == 0 {
			t.Errorf("query %q: no judgment at grade %d", q.Query, plainlyMeantGrade)
		}
	}
	for _, s := range strata {
		n := 0
		for _, q := range set.Queries {
			if q.Stratum == s {
				n++
			}
		}
		if n == 0 {
			t.Errorf("stratum %q has no queries; a failure mode nobody measures", s)
		}
	}
}

// TestEvaluatedBodyCoversEveryServedKey is the anti-drift guard. BuildQuery is
// the served builder, and the harness sends what it returns minus two keys
// _rank_eval refuses. If it ever grows a third top-level key, this fails and
// somebody decides whether that key changes the ranking — rather than the
// harness quietly evaluating a query the server no longer sends.
func TestEvaluatedBodyCoversEveryServedKey(t *testing.T) {
	accounted := make(map[string]bool, len(servedKeysKept)+len(servedKeysDropped))
	for _, key := range servedKeysKept {
		accounted[key] = true
	}
	for _, key := range servedKeysDropped {
		accounted[key] = true
	}
	for _, q := range loadJudgments(t).Queries {
		params, errs := searchpkg.ParseParams(url.Values{
			"q":         {q.Query},
			"page_size": {strconv.Itoa(k)},
		})
		if len(errs) > 0 {
			t.Fatalf("query %q: ParseParams rejected it: %+v", q.Query, errs)
		}
		for key := range searchpkg.BuildQuery(params) {
			if !accounted[key] {
				t.Errorf("query %q: BuildQuery produced unaccounted key %q; "+
					"decide whether it affects ranking, then add it to servedKeysKept or servedKeysDropped",
					q.Query, key)
			}
		}
		body := evaluatedBody(t, q.Query)
		for _, key := range servedKeysDropped {
			if _, ok := body[key]; ok {
				t.Errorf("query %q: %q survived into the evaluated body; _rank_eval rejects it", q.Query, key)
			}
		}
		if _, ok := body["query"]; !ok {
			t.Errorf("query %q: the evaluated body carries no query", q.Query)
		}
		if _, ok := body["sort"]; !ok {
			t.Errorf("query %q: the evaluated body carries no sort; ranking order would be ES's default", q.Query)
		}
	}
}

// TestRelevanceBaseline is the measurement. It scores every judged query with
// every metric, prints the per-stratum and overall table, and writes the
// baseline.
func TestRelevanceBaseline(t *testing.T) {
	set := loadJudgments(t)

	byStratum := make(map[string][]string, len(strata))
	for _, q := range set.Queries {
		byStratum[q.Stratum] = append(byStratum[q.Stratum], q.Query)
	}

	// An invented id is ignored by _rank_eval inside a 200, so this has to
	// happen before a single number is computed.
	assertJudgedIDsExist(t, set)

	overall := make(map[string]float64, len(metricDefs)+1)
	stratumScores := make(map[string]map[string]float64, len(strata))
	for _, s := range strata {
		stratumScores[s] = make(map[string]float64, len(metricDefs)+1)
	}
	unratedByMetric := make(map[string]map[string]int, len(metricDefs))

	for _, m := range metricDefs {
		score, perQuery, unrated := runRankEval(t, set, m)
		unratedByMetric[m.Key] = unrated

		// ES's own metric_score is the mean over the requests. Recomputing it
		// from the details proves the per-stratum means below are grouping the
		// same numbers the headline is made of.
		all := make([]float64, 0, len(set.Queries))
		for _, q := range set.Queries {
			v, ok := perQuery[q.Query]
			if !ok {
				t.Fatalf("%s: no score returned for query %q", m.Key, q.Query)
			}
			all = append(all, v)
		}
		if recomputed := mean(all); !closeEnough(recomputed, score) {
			t.Errorf("%s: ES reported %v overall, the per-query details mean %v", m.Key, score, recomputed)
		}
		overall[m.Key] = score

		for _, s := range strata {
			values := make([]float64, 0, len(byStratum[s]))
			for _, q := range byStratum[s] {
				values = append(values, perQuery[q])
			}
			stratumScores[s][m.Key] = mean(values)
		}
	}

	// The unrated window is a property of the search, not of the metric, so
	// all three metrics must report the same counts. Asserting it rather than
	// assuming it is what lets the rest of this read one metric's numbers.
	unrated := unratedByMetric[metricDefs[0].Key]
	for _, m := range metricDefs[1:] {
		for _, q := range set.Queries {
			if got, want := unratedByMetric[m.Key][q.Query], unrated[q.Query]; got != want {
				t.Errorf("query %q: %s reports %d unrated docs, %s reports %d",
					q.Query, m.Key, got, metricDefs[0].Key, want)
			}
		}
	}
	totalUnrated := 0
	for _, s := range strata {
		n := 0
		for _, q := range byStratum[s] {
			n += unrated[q]
		}
		stratumScores[s][unratedKey] = float64(n)
		totalUnrated += n
	}
	overall[unratedKey] = float64(totalUnrated)

	report := baselineReport{
		Index:   indexName(),
		Docs:    fetchDocCount(t),
		Queries: len(set.Queries),
		K:       k,
		Metrics: overall,
	}
	meta := fetchBuildIdentity(t)
	report.Build = buildIdentity{Version: meta.Version, Commit: meta.Commit, Built: meta.Built}
	report.Seed = meta.Seed
	for _, s := range strata {
		report.Strata = append(report.Strata, stratumReport{
			Name:    s,
			Queries: len(byStratum[s]),
			Metrics: stratumScores[s],
		})
	}

	printTable(t, report)
	writeBaseline(t, report)
}

// closeEnough compares two means of the same float64s, which differ only by
// summation order.
func closeEnough(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

func printTable(t *testing.T, report baselineReport) {
	t.Helper()
	keys := make([]string, 0, len(metricDefs))
	for _, m := range metricDefs {
		keys = append(keys, m.Key)
	}
	t.Logf("index=%s docs=%d queries=%d k=%d build=%s/%s",
		report.Index, report.Docs, report.Queries, report.K, report.Build.Version, report.Build.Commit)
	t.Logf("%-26s %5s  %-12s %-12s %-12s %s", "stratum", "n", keys[0], keys[1], keys[2], unratedKey)
	for _, s := range report.Strata {
		t.Logf("%-26s %5d  %-12.6f %-12.6f %-12.6f %.0f",
			s.Name, s.Queries, s.Metrics[keys[0]], s.Metrics[keys[1]], s.Metrics[keys[2]], s.Metrics[unratedKey])
	}
	t.Logf("%-26s %5d  %-12.6f %-12.6f %-12.6f %.0f",
		"OVERALL", report.Queries,
		report.Metrics[keys[0]], report.Metrics[keys[1]], report.Metrics[keys[2]], report.Metrics[unratedKey])
}

func writeBaseline(t *testing.T, report baselineReport) {
	t.Helper()
	path := baselinePath(t)
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode baseline: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("baseline written to %s", path)
}
