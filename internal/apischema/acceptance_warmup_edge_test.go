package apischema

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// warmupScript returns the warm-up step's run block as the runner executes it:
// the lines under "run: |" with the block's indentation removed, as YAML
// removes it from a literal block scalar.
func warmupScript(t *testing.T) string {
	t.Helper()
	steps, _, warm, _ := warmupStep(t)
	raw, err := os.ReadFile("../../.github/workflows/acceptance.yml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	header := "- name: " + steps[warm].name
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no line %q in acceptance.yml", header)
	}
	indentOf := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	run := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "run: |" {
			run = i
			break
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
			break
		}
	}
	if run < 0 {
		t.Fatalf("the warm-up step has no literal run block (run: |)")
	}
	keyIndent, blockIndent := indentOf(lines[run]), -1
	var out []string
	for _, l := range lines[run+1:] {
		if strings.TrimSpace(l) == "" {
			out = append(out, "")
			continue
		}
		if indentOf(l) <= keyIndent {
			break
		}
		if blockIndent < 0 {
			blockIndent = indentOf(l)
		}
		if indentOf(l) < blockIndent {
			t.Fatalf("line %q is indented less than the block's first line", l)
		}
		out = append(out, l[blockIndent:])
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// warmupArray returns the members of the script's paths=( ... ) array.
func warmupArray(t *testing.T) []string {
	t.Helper()
	script := warmupScript(t)
	_, rest, ok := strings.Cut(script, "paths=(\n")
	if !ok {
		t.Fatalf("the warm-up script has no paths=( array:\n%s", script)
	}
	body, _, ok := strings.Cut(rest, "\n)\n")
	if !ok {
		t.Fatalf("the warm-up script's paths array is not closed:\n%s", script)
	}
	var paths []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		unq, err := strconv.Unquote(l)
		if err != nil {
			t.Fatalf("paths member %s is not one double-quoted path: %v", l, err)
		}
		paths = append(paths, unq)
	}
	if len(paths) == 0 {
		t.Fatal("the warm-up's paths array is empty")
	}
	return paths
}

// The runner hands the run block to bash, and the per-pass report to python3
// through a heredoc inside the loop. The heredoc only ends if its terminator
// lands in column one once YAML has stripped the block's indentation, and a
// syntax error in either language would fail the step before the matrix runs.
func TestAcceptanceWarmupScriptIsValidBashAndPython(t *testing.T) {
	script := warmupScript(t)
	if !strings.HasPrefix(script, "set -euo pipefail\n") {
		t.Errorf("the warm-up script does not open with set -euo pipefail:\n%s", script)
	}
	_, after, ok := strings.Cut(script, "<<'PY'\n")
	if !ok {
		t.Fatalf("the warm-up script has no <<'PY' heredoc:\n%s", script)
	}
	py, _, ok := strings.Cut(after, "\nPY\n")
	if !ok {
		t.Fatalf("the heredoc's PY terminator is not alone in column one after YAML strips the block indentation:\n%s", after)
	}

	dir := t.TempDir()
	sh := filepath.Join(dir, "warmup.sh")
	if err := os.WriteFile(sh, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if bash, err := exec.LookPath("bash"); err != nil {
		t.Log("bash is not installed here; skipping its syntax check")
	} else if out, err := exec.Command(bash, "-n", sh).CombinedOutput(); err != nil {
		t.Errorf("bash -n rejects the warm-up script: %v\n%s", err, out)
	}
	if python, err := exec.LookPath("python3"); err != nil {
		t.Log("python3 is not installed here; skipping its compile check")
	} else {
		cmd := exec.Command(python, "-I", "-c", "import sys; compile(sys.stdin.read(), 'warmup', 'exec')")
		cmd.Stdin = strings.NewReader(py + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("python3 cannot compile the warm-up report:\n%s\n%v\n%s", py, err, out)
		}
	}
}

// Each warm-up path is a request the real server answers 200: a 4xx would
// fail the step on a healthy stack. Search paths are also checked against the
// parameter rules the server applies, so a misspelt sort or a page_size that
// is not a number is caught here and not on the runner.
func TestAcceptanceWarmupPathsAreAnswered200ByTheServer(t *testing.T) {
	base := startServer(t, goldenCluster())
	for _, p := range warmupArray(t) {
		u, err := url.Parse(p)
		if err != nil {
			t.Errorf("%q is not a URL path: %v", p, err)
			continue
		}
		if u.Path == "/api/search" {
			if _, errs := search.ParseParams(u.Query()); len(errs) > 0 {
				t.Errorf("GET %s: the search parameters are refused: %v", p, errs)
			}
		}
		if got := fetch(t, base, p, nil); got.status != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200: %s", p, got.status, got.body)
		}
	}
}

// The per-pass report only prints. If it could exit on what it reads, a
// timing threshold could hide in it under any variable name.
func TestAcceptanceWarmupReportCannotFailTheStep(t *testing.T) {
	_, after, ok := strings.Cut(warmupScript(t), "<<'PY'\n")
	if !ok {
		t.Fatal("the warm-up script has no <<'PY' heredoc")
	}
	py, _, _ := strings.Cut(after, "\nPY\n")
	for _, bad := range regexp.MustCompile(`\b(sys\.exit|exit\(|quit\(|raise|assert|os\._exit)`).FindAllString(py, -1) {
		t.Errorf("the warm-up report uses %q; it must print the slowest took_ms and nothing else:\n%s", bad, py)
	}
}

// A repeated path warms nothing a first copy did not, and only makes the step
// longer.
func TestAcceptanceWarmupPathsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range warmupArray(t) {
		if seen[p] {
			t.Errorf("the warm-up requests %q more than once per pass", p)
		}
		seen[p] = true
	}
}

// The warm-up warms the app the matrix measures, so every URL it requests is
// that app's published address, and it never reaches round the app into the
// containers.
func TestAcceptanceWarmupOnlyTalksToTheApp(t *testing.T) {
	script := warmupScript(t)
	for _, u := range regexp.MustCompile(`https?://[^\s"'$]+`).FindAllString(script, -1) {
		if u != "http://localhost:8080" {
			t.Errorf("the warm-up requests %q; it must only send requests to http://localhost:8080", u)
		}
	}
	for _, l := range strings.Split(script, "\n") {
		if f := strings.Fields(l); len(f) > 0 && (f[0] == "docker" || slices.Contains(f, "docker")) {
			t.Errorf("the warm-up runs docker (%q); it must reach Elasticsearch only through the app", l)
		}
	}
}

// The step's own timeout must bound it below the job's, so a hung stack fails
// in the warm-up with time left for the logs step, and one curl's --max-time
// must fit inside the step's timeout.
func TestAcceptanceWarmupTimeoutSitsInsideTheJobs(t *testing.T) {
	steps, _, warm, _ := warmupStep(t)
	minutes := func(lines []string) int {
		for _, l := range lines {
			if v, ok := strings.CutPrefix(l, "timeout-minutes:"); ok {
				n, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					t.Fatalf("timeout-minutes %q is not a number", v)
				}
				return n
			}
		}
		return 0
	}
	step := minutes(steps[warm].lines)
	job := minutes(trimmedLines(t, "../../.github/workflows/acceptance.yml"))
	if step <= 0 || job <= 0 {
		t.Fatalf("timeout-minutes: step %d, job %d; both must be set", step, job)
	}
	if step >= job {
		t.Errorf("the warm-up may run %d minutes, the whole job only %d", step, job)
	}
	m := regexp.MustCompile(`--max-time ([0-9]+)`).FindStringSubmatch(warmupScript(t))
	if m == nil {
		t.Fatal("the warm-up's curl has no --max-time")
	}
	if s, _ := strconv.Atoi(m[1]); s <= 0 || s >= step*60 {
		t.Errorf("one request may take %ds, the step only %d minutes", s, step)
	}
}

// The README describes the workflow's steps in order; the warm-up it names
// must exist in the workflow, sit where it says, and judge nothing by speed.
func TestAcceptanceWarmupReadmeAgreesWithTheWorkflow(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	var sentence string
	for _, l := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(l, []byte("`.github/workflows/acceptance.yml` runs")) {
			sentence = string(l)
		}
	}
	if sentence == "" {
		t.Fatal("README.md no longer describes .github/workflows/acceptance.yml")
	}
	corpus, warm, tests := strings.Index(sentence, "20,635"), strings.Index(sentence, "warm"), strings.Index(sentence, "runs the tests")
	if corpus < 0 || warm < 0 || tests < 0 || corpus >= warm || warm >= tests {
		t.Errorf("the README does not place the warm-up between the corpus check and the tests, as the workflow does: %q", sentence)
	}
	warmupStep(t) // fails unless the workflow's order is the same
	script := warmupScript(t)
	if strings.Contains(sentence, "never how fast") && strings.Contains(script, "exit 1") {
		for _, l := range strings.Split(script, "\n") {
			if strings.Contains(l, "took") && strings.Contains(l, "exit") {
				t.Errorf("the README says the warm-up never judges speed, but it exits on a timing: %q", l)
			}
		}
	}
}
