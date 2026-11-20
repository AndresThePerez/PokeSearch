package apischema

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The acceptance matrix holds every search and stats answer
// to a latency target when it runs on loopback, as it does in the acceptance
// workflow. That workflow seeds the index seconds before the matrix starts, so
// the first request of each kind used to meet cold Elasticsearch caches and a
// JVM that had not compiled its hot paths yet, and missed the target while
// every repeat of the same request met it. The workflow therefore warms the
// app up between the corpus-size check and the matrix. These tests keep that
// warm-up honest: it goes through the app the matrix measures, it covers the
// kinds of request the matrix times, it runs a fixed number of passes, and it
// judges nothing by speed, so a slow service still fails the matrix.

const matrixLine = "run: POKESEARCH_URL=http://localhost:8080 go test -tags acceptance -count=1 -v ./internal/acceptance ./internal/apischema"

type workflowStep struct {
	name  string
	lines []string // trimmed, the "- name:" line first
}

// acceptanceSteps splits acceptance.yml into its job steps. A step starts at a
// list item indented as the steps of the single job are.
func acceptanceSteps(t *testing.T) []workflowStep {
	t.Helper()
	raw, err := os.ReadFile("../../.github/workflows/acceptance.yml")
	if err != nil {
		t.Fatal(err)
	}
	var steps []workflowStep
	for _, l := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(l, "      - ") && !strings.HasPrefix(l, "       ") {
			steps = append(steps, workflowStep{})
		}
		if len(steps) == 0 {
			continue
		}
		s := &steps[len(steps)-1]
		if name, ok := strings.CutPrefix(trimmed, "- name:"); ok {
			s.name = strings.TrimSpace(name)
		} else if name, ok := strings.CutPrefix(trimmed, "name:"); ok && s.name == "" {
			s.name = strings.TrimSpace(name)
		}
		s.lines = append(s.lines, trimmed)
	}
	if len(steps) == 0 {
		t.Fatal("acceptance.yml has no steps")
	}
	return steps
}

func (s workflowStep) text() string { return strings.Join(s.lines, "\n") }

// stepIndex returns the index of the one step whose text satisfies match.
func stepIndex(t *testing.T, steps []workflowStep, what string, match func(workflowStep) bool) int {
	t.Helper()
	found := -1
	for i, s := range steps {
		if match(s) {
			if found >= 0 {
				t.Fatalf("acceptance.yml has more than one %s step: %q and %q", what, steps[found].name, s.name)
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("acceptance.yml has no %s step", what)
	}
	return found
}

// warmupStep finds the corpus-size check, the matrix and the warm-up between
// them, and fails unless the warm-up sits after the first and before the
// second.
func warmupStep(t *testing.T) (steps []workflowStep, corpus, warm, matrix int) {
	t.Helper()
	steps = acceptanceSteps(t)
	corpus = stepIndex(t, steps, "corpus-size check", func(s workflowStep) bool {
		return strings.Contains(s.text(), `[ "$docs" = "20635" ]`)
	})
	matrix = stepIndex(t, steps, "acceptance matrix", func(s workflowStep) bool {
		return slices.Contains(s.lines, matrixLine)
	})
	warm = stepIndex(t, steps, "warm-up", func(s workflowStep) bool {
		return strings.Contains(strings.ToLower(s.name), "warm")
	})
	if warm <= corpus || warm >= matrix {
		t.Fatalf("the warm-up step %q is step %d; it must come after the corpus-size check (step %d) and before the matrix (step %d)",
			steps[warm].name, warm, corpus, matrix)
	}
	if matrix != warm+1 {
		t.Errorf("step %q runs between the warm-up and the matrix; the matrix must follow the warm-up directly", steps[warm+1].name)
	}
	return steps, corpus, warm, matrix
}

// warmupPaths lists every app path the warm-up step requests, query string
// included, in the order written.
func warmupPaths(s workflowStep) []string {
	return regexp.MustCompile(`(/api/[A-Za-z0-9_/.\-]+(?:\?[^\s"'\\]*)?)`).FindAllString(s.text(), -1)
}

func TestAcceptanceWarmupSitsBetweenCorpusCheckAndMatrix(t *testing.T) {
	steps, _, warm, _ := warmupStep(t)
	body := steps[warm].text()
	// Elasticsearch publishes no port in the compose file, and the matrix
	// times the app, so the warm-up must go through the app's published URL.
	if !strings.Contains(body, "http://localhost:8080") {
		t.Errorf("the warm-up does not request the app at http://localhost:8080, the URL the matrix measures:\n%s", body)
	}
	if strings.Contains(body, ":9200") || strings.Contains(body, "pokesearch-es-1") {
		t.Errorf("the warm-up talks to Elasticsearch directly; the app's own requests are what has to be warm:\n%s", body)
	}
	if !slices.Contains(steps[warm].lines, "set -euo pipefail") {
		t.Errorf("the warm-up script does not start with set -euo pipefail:\n%s", body)
	}
}

func TestAcceptanceWarmupCoversTheShapesTheMatrixTimes(t *testing.T) {
	steps, _, warm, _ := warmupStep(t)
	paths := warmupPaths(steps[warm])
	if len(paths) == 0 {
		t.Fatalf("the warm-up requests no /api/ path:\n%s", steps[warm].text())
	}
	has := func(pred func(path string, q map[string]string) bool) bool {
		for _, p := range paths {
			route, query, _ := strings.Cut(p, "?")
			q := map[string]string{}
			for _, kv := range strings.Split(query, "&") {
				if kv == "" {
					continue
				}
				k, v, _ := strings.Cut(kv, "=")
				q[k] = v
			}
			q[""] = route
			if pred(route, q) {
				return true
			}
		}
		return false
	}
	search := func(want func(q map[string]string) bool) func(string, map[string]string) bool {
		return func(route string, q map[string]string) bool { return route == "/api/search" && want(q) }
	}
	for _, c := range []struct {
		what string
		ok   func(string, map[string]string) bool
	}{
		{"a browse with no q", search(func(q map[string]string) bool { return len(q) == 1 })},
		{"a text query", search(func(q map[string]string) bool { return q["q"] != "" })},
		{"a text query with a filter", search(func(q map[string]string) bool { return q["q"] != "" && q["set"] != "" })},
		{"a set filter", search(func(q map[string]string) bool { return q["set"] != "" })},
		{"a rarity filter", search(func(q map[string]string) bool { return q["rarity"] != "" })},
		{"a types filter", search(func(q map[string]string) bool { return q["types"] != "" })},
		{"a supertype filter", search(func(q map[string]string) bool { return q["supertype"] != "" })},
		{"debug=1", search(func(q map[string]string) bool { return q["debug"] == "1" })},
		{"a non-default page_size", search(func(q map[string]string) bool { return q["page_size"] != "" && q["page_size"] != "24" })},
		{"a later page", search(func(q map[string]string) bool { return q["page"] != "" && q["page"] != "1" })},
		{"/api/stats", func(r string, _ map[string]string) bool { return r == "/api/stats" }},
		{"/api/suggest with a prefix", func(r string, q map[string]string) bool { return r == "/api/suggest" && q["q"] != "" }},
		{"/api/compare with a query", func(r string, q map[string]string) bool { return r == "/api/compare" && q["q"] != "" }},
		{"/api/explain of a card for a query", func(r string, q map[string]string) bool {
			return r == "/api/explain" && q["id"] != "" && q["q"] != ""
		}},
		{"/api/v1/cards/{id}", func(r string, _ map[string]string) bool {
			return strings.HasPrefix(r, "/api/v1/cards/") && len(r) > len("/api/v1/cards/")
		}},
		{"/api/v1/cards?ids=", func(r string, q map[string]string) bool { return r == "/api/v1/cards" && q["ids"] != "" }},
		{"/api/v1/sets", func(r string, _ map[string]string) bool { return r == "/api/v1/sets" }},
	} {
		if !has(c.ok) {
			t.Errorf("the warm-up never sends %s; requested: %q", c.what, paths)
		}
	}
	// Every sort the matrix times, each one at least once.
	for _, sort := range []string{"relevance", "newest", "oldest", "hp", "name"} {
		if !has(search(func(q map[string]string) bool { return q["sort"] == sort })) {
			t.Errorf("the warm-up never sends sort=%s; requested: %q", sort, paths)
		}
	}
	// Every warm-up request must be one the app answers 200 to, so none of
	// them can be one of the matrix's deliberate error cases.
	for _, p := range paths {
		for _, bad := range []string{"sort=bogus", "page_size=lots", "/api/nope", "id=nope"} {
			if strings.Contains(p, bad) {
				t.Errorf("the warm-up requests %q, which the app answers with an error", p)
			}
		}
	}
}

func TestAcceptanceWarmupRunsAFixedNumberOfPassesAndJudgesNoSpeed(t *testing.T) {
	steps, _, warm, _ := warmupStep(t)
	s := steps[warm]
	body := s.text()
	// A fixed count, written as a literal: the warm-up must never wait for
	// the service to become fast, or the matrix's latency target would be
	// something the workflow can wait its way past.
	counts := regexp.MustCompile(`(?m)^passes=([0-9]+)$`).FindAllStringSubmatch(body, -1)
	if len(counts) != 1 {
		t.Fatalf("the warm-up must set its pass count once as a literal (passes=N), found %d:\n%s", len(counts), body)
	}
	n, _ := strconv.Atoi(counts[0][1])
	if n < 2 || n > 20 {
		t.Errorf("the warm-up runs %d passes; at least 2 are needed for a repeat to land on a warm path, and more than 20 is waiting, not warming", n)
	}
	if !strings.Contains(body, `seq 1 "$passes"`) {
		t.Errorf("the warm-up does not loop over its fixed pass count with seq 1 \"$passes\":\n%s", body)
	}
	for _, l := range s.lines {
		for _, kw := range []string{"while ", "until "} {
			if strings.HasPrefix(l, kw) || strings.Contains(l, "; "+kw) || strings.Contains(l, "do "+kw) {
				t.Errorf("the warm-up has an open-ended loop (%q): its length must not depend on how the service answers", l)
			}
		}
	}
	// It reports how long each pass took Elasticsearch, but never compares
	// that to a threshold: the target belongs to the matrix alone.
	if !strings.Contains(body, "took_ms") {
		t.Errorf("the warm-up does not report took_ms, so the log cannot show the service warming:\n%s", body)
	}
	for _, l := range s.lines {
		if strings.Contains(l, "took") && regexp.MustCompile(`(-gt|-ge|-lt|-le|[<>]=?)\s*[0-9]`).MatchString(l) {
			t.Errorf("the warm-up compares a timing to a threshold (%q); latency is the matrix's job", l)
		}
	}
	// A request that is not answered 200 fails the step rather than being
	// warmed past.
	if !strings.Contains(body, "%{http_code}") || !strings.Contains(body, `!= 200`) {
		t.Errorf("the warm-up does not fail on a status other than 200:\n%s", body)
	}
	if !regexp.MustCompile(`--max-time [0-9]+`).MatchString(body) {
		t.Errorf("the warm-up's requests carry no --max-time, so one hung request could hold the job to its timeout:\n%s", body)
	}
	if !regexp.MustCompile(`(?m)^timeout-minutes: [0-9]+$`).MatchString(body) {
		t.Errorf("the warm-up step has no timeout-minutes of its own:\n%s", body)
	}
	for _, l := range s.lines {
		if strings.HasPrefix(l, "continue-on-error:") {
			t.Errorf("the warm-up step carries %q; a broken stack must fail it", l)
		}
	}
}

func TestAcceptanceMatrixRunsOnceAndUnretried(t *testing.T) {
	steps, _, warm, matrix := warmupStep(t)
	// Only the matrix step runs the acceptance tests. The warm-up may build
	// their binaries ahead of time, but then it runs none of them.
	for i, s := range steps {
		for _, l := range s.lines {
			if !strings.Contains(l, "go test -tags acceptance") || strings.HasPrefix(l, "#") {
				continue
			}
			switch {
			case i == matrix && l == matrixLine:
			case i == warm && strings.Contains(l, "-run '^$'"):
			default:
				t.Errorf("step %q runs the acceptance tests outside the matrix: %q", s.name, l)
			}
		}
	}
	// A step's lines run on to the next step, so the comment above that one
	// is skipped here.
	var keys []string
	for _, l := range steps[matrix].lines {
		if l != "" && !strings.HasPrefix(l, "#") {
			keys = append(keys, l)
		}
	}
	if len(keys) != 2 {
		t.Errorf("the matrix step is no longer just its name and its run line: %q", keys)
	}
	for _, s := range steps {
		for _, l := range s.lines {
			if strings.Contains(l, "retry") && !strings.HasPrefix(l, "#") {
				t.Errorf("step %q retries something (%q); a latency miss must stay red", s.name, l)
			}
		}
	}
}
