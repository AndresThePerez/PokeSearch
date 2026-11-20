package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// idLookup matches the two ways a module reaches for an element by id. Only
// string literals are found, which is deliberate: a computed id (the
// suggest-opt-N options) has no fixed counterpart in the markup to check.
var idLookup = regexp.MustCompile(`(?:\$\(|getElementById\()"([^"]+)"\)`)

// The modules wire themselves to the markup by id at load time. There is no
// build step to catch a rename, and a missing id fails silently at the first
// property access — so the two halves are checked against each other here.
func TestEmbeddedMarkupHasRequiredElements(t *testing.T) {
	html, err := Files.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if _, err := Files.ReadFile("styles.css"); err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	page := string(html)

	// The markup's own contract: these must exist whether or not a module
	// currently looks them up.
	ids := []string{
		"search-input", "suggest-list", "filter-rail", "filter-toggle",
		"filter-count", "filter-done", "supertype-toggle", "type-chips",
		"set-select", "rarity-select", "series-select", "active-filters",
		"clear-filters", "sort-select", "order-toggle", "results-grid",
		"empty-state", "empty-clear", "empty-browse", "load-more",
		"degraded-banner", "retry-search",
		"total-count", "query-inspector", "response-inspector", "copy-dsl",
		"copy-response", "dsl-json", "response-json", "card-modal", "modal-close",
		"ranking-lab", "lab-status", "lab-columns", "lab-movers",
		"stats-link", "stats-view", "stats-total", "stats-superlatives",
		"stats-charts", "latency-waterfall", "waterfall-es", "waterfall-rest",
		"wf-es-ms", "wf-rest-ms", "latency-spark", "spark-percentiles",
		"spark-p50", "spark-p95", "request-id-line", "stat-request-id",
		"archive-cards", "archive-sets", "landing", "landing-examples",
		"empty-guide", "empty-examples", "badge-legend",
	}
	for _, id := range ids {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("index.html is missing element id %q", id)
		}
	}

	// Every id any module looks up must exist in the markup.
	modules := jsFiles(t)
	if len(modules) == 0 {
		t.Fatal("web/js holds no modules")
	}
	var referenced []string
	for _, name := range modules {
		body, err := Files.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range idLookup.FindAllStringSubmatch(string(body), -1) {
			id := m[1]
			if !strings.Contains(page, `id="`+id+`"`) {
				t.Errorf("%s looks up %q, which index.html does not define", name, id)
			}
			if !slices.Contains(referenced, id) {
				referenced = append(referenced, id)
			}
		}
	}

	// The entry point has to be the one the markup actually loads.
	if !strings.Contains(page, `type="module" src="/js/main.js"`) {
		t.Error(`index.html must load /js/main.js as a module`)
	}
	if !slices.Contains(modules, "js/main.js") {
		t.Error("js/main.js is not embedded")
	}
	// A sanity floor: a regex that silently stopped matching would otherwise
	// turn this whole check into a no-op that always passes.
	if len(referenced) < 20 {
		t.Errorf("only %d ids referenced across %d modules; the id scan looks broken",
			len(referenced), len(modules))
	}
}

// The old single-file bundle must be gone, or the split is only half done and
// two copies of every handler ship in the binary.
func TestLegacyBundleIsNotEmbedded(t *testing.T) {
	if _, err := Files.ReadFile("app.js"); err == nil {
		t.Error("web/app.js is still embedded; the ES-module split replaced it")
	}
}

func jsFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(Files, "js", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".js") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk js: %v", err)
	}
	slices.Sort(out)
	return out
}

// embeddedJS lists every file under the embedded js/ directory, whatever its
// extension, so a stray file cannot hide behind the ".js" filter of jsFiles.
func embeddedJS(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(Files, "js")
	if err != nil {
		t.Fatalf("read embedded js: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

// The node:test suites live beside the modules they cover. They are not part
// of the page and must not ship in the binary.
func TestTestFilesAreNotEmbedded(t *testing.T) {
	for _, name := range embeddedJS(t) {
		if strings.HasSuffix(name, ".mjs") {
			t.Errorf("js/%s is embedded; test files must stay out of the binary", name)
		}
	}
}

// Narrowing the embed pattern to js/*.js must never silently drop a shipped
// script: the embedded set has to be exactly the .js files on disk.
func TestEmbeddedScriptsAreTheJSFilesOnDisk(t *testing.T) {
	onDisk, err := filepath.Glob("js/*.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) == 0 {
		t.Fatal("no js/*.js files on disk; the glob is wrong, not the code")
	}
	var want []string
	for _, p := range onDisk {
		want = append(want, filepath.Base(p))
	}
	slices.Sort(want)
	got := embeddedJS(t)
	if !slices.Equal(got, want) {
		t.Errorf("embedded js/ files = %v, js/*.js on disk = %v", got, want)
	}
}

// CI runs the frontend tests in a step of their own. The glob is expanded by
// the shell, and that step must not set nullglob: with it an empty glob would
// pass the step without running a single test.
func TestCIRunsTheFrontendTests(t *testing.T) {
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	const run = "run: node --test web/js/*.test.mjs"
	at := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == run {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("ci.yml has no line %q", run)
	}
	// Walk back to the step's own "- name:" line and check only that step.
	start := at
	for start > 0 && !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ") {
		start--
	}
	for _, l := range lines[start : at+1] {
		if strings.Contains(l, "nullglob") {
			t.Errorf("the node test step sets nullglob: %q", strings.TrimSpace(l))
		}
	}
}

// frontendJob returns the lines of the frontend job in ci.yml.
func frontendJob(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(l)
		if indent := len(l) - len(strings.TrimLeft(l, " ")); indent == 2 && strings.HasSuffix(trim, ":") && !strings.HasPrefix(trim, "#") {
			in = trim == "frontend:"
			continue
		}
		if in {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatal("ci.yml has no frontend job")
	}
	return out
}

func lineIndex(lines []string, want string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) == want {
			return i
		}
	}
	return -1
}

// The suites have to exist on disk: with none, the CI step would run zero
// tests and pass, and go test would not notice either.
func TestFrontendTestFilesExist(t *testing.T) {
	files, err := filepath.Glob("js/*.test.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no web/js/*.test.mjs file exists; the frontend tests would run zero cases and pass")
	}
}

// Node expands the pattern itself and, when nothing matches, runs no test and
// exits 0. A step of its own, before the test step, has to fail in that case.
func TestFrontendJobFailsWhenNoTestFileMatches(t *testing.T) {
	lines := frontendJob(t)
	run := lineIndex(lines, "run: node --test web/js/*.test.mjs")
	if run < 0 {
		t.Fatal("frontend job does not run the frontend tests")
	}
	glob := lineIndex(lines, "files=(web/js/*.test.mjs)")
	check := lineIndex(lines, `if [ ! -e "${files[0]}" ]; then`)
	if glob < 0 || check < 0 {
		t.Fatal("frontend job has no step that checks web/js/*.test.mjs matches a file")
	}
	if glob >= check || check >= run {
		t.Errorf("the no-test-files check (lines %d, %d) must come before the test step (line %d)", glob, check, run)
	}
	// The check's own body must exit non-zero, and must not use nullglob,
	// which would hide the unmatched pattern it relies on.
	exit := -1
	for i := check; i < run; i++ {
		if strings.TrimSpace(lines[i]) == "exit 1" {
			exit = i
			break
		}
	}
	if exit < 0 {
		t.Error("the no-test-files check never runs exit 1 before the test step")
	}
	for i := glob; i < run; i++ {
		trim := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trim, "#") && strings.Contains(trim, "nullglob") {
			t.Errorf("the no-test-files check uses nullglob: %s", trim)
		}
	}
}

// The scripts are ES modules with no package.json, which needs Node 20.19 or
// 22.7 and later. The job pins the major the suites were verified on instead
// of taking whatever the runner ships.
func TestFrontendJobPinsNode(t *testing.T) {
	lines := frontendJob(t)
	setup := lineIndex(lines, "- uses: actions/setup-node@v4")
	if setup < 0 {
		t.Fatal("frontend job has no actions/setup-node@v4 step")
	}
	checkout := lineIndex(lines, "- uses: actions/checkout@v4")
	if checkout < 0 || checkout > setup {
		t.Error("setup-node must come after the checkout")
	}
	version := -1
	for i := setup + 1; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trim, "- ") {
			break
		}
		if trim == "node-version: 24" {
			version = i
		}
	}
	if version < 0 {
		t.Error("the setup-node step does not set node-version: 24")
	}
	// Setup has to come before the first step that runs node.
	for i, l := range lines {
		if strings.Contains(l, "node ") && !strings.HasPrefix(strings.TrimSpace(l), "#") && i < setup {
			t.Errorf("line %q runs node before setup-node", strings.TrimSpace(l))
		}
	}
}
