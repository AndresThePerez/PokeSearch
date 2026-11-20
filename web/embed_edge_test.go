package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/server"
)

var (
	// importSpec finds the module specifier of a static import or re-export, a
	// binding list included, over one line or many. A side-effect import has
	// no "from" and is matched by its own alternative.
	importSpec = regexp.MustCompile(`(?m)^(?:import|export)\s+(?:[^;"'\n]*?\bfrom\s*|\{[^}]*\}\s*from\s*)["']([^"']+)["']|^import\s+["']([^"']+)["']`)
	scriptSrc  = regexp.MustCompile(`<script\b[^>]*\bsrc="([^"]+)"`)
)

// The browser resolves the page's imports against the server, so a module the
// binary does not carry is a 404 that only shows up as a blank page. Every
// script the page loads and every module those scripts import has to be in the
// embedded set.
func TestEveryScriptAndImportTheEmbeddedPageNeedsIsEmbedded(t *testing.T) {
	html, err := Files.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	var queue []string
	for _, m := range scriptSrc.FindAllStringSubmatch(string(html), -1) {
		queue = append(queue, strings.TrimPrefix(path.Clean(m[1]), "/"))
	}
	if len(queue) == 0 {
		t.Fatal("index.html loads no script; the pattern is wrong, not the page")
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		src, err := Files.ReadFile(name)
		if err != nil {
			t.Errorf("%s is loaded by the page but is not embedded: %v", name, err)
			continue
		}
		for _, m := range importSpec.FindAllStringSubmatch(string(src), -1) {
			spec := m[1] + m[2]
			if !strings.HasPrefix(spec, ".") {
				t.Errorf("%s imports %q, which the browser cannot resolve without a build step", name, spec)
				continue
			}
			queue = append(queue, path.Join(path.Dir(name), spec))
		}
	}
	// Every shipped script is reachable from the page; one that is not is dead
	// weight in the binary or an import this test failed to see.
	for _, name := range embeddedJS(t) {
		if !seen["js/"+name] {
			t.Errorf("js/%s is embedded but nothing the page loads imports it", name)
		}
	}
}

// A file the page needs but the narrowed pattern leaves out would vanish from
// the binary without any test noticing, unless something compares the whole of
// js/ on disk (subdirectories included) with what is embedded. Only test files
// may be on disk and out of the binary.
func TestOnlyTestFilesInJSAreLeftOutOfTheBinary(t *testing.T) {
	err := filepath.WalkDir("js", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(d.Name(), ".test.mjs") {
			if _, err := Files.Open(filepath.ToSlash(p)); err == nil {
				t.Errorf("%s is a test file and is embedded", p)
			}
			return nil
		}
		if _, err := Files.Open(filepath.ToSlash(p)); err != nil {
			t.Errorf("%s is on disk, is not a test file and is not embedded: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// No embedded name may look like a test, a source map or a hidden file, and
// js/ holds scripts only, with no nested directory the pattern would skip.
func TestEmbeddedJSHoldsOnlyPlainScripts(t *testing.T) {
	entries, err := fs.ReadDir(Files, "js")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case e.IsDir():
			t.Errorf("js/%s is a directory inside the embedded set", n)
		case !strings.HasSuffix(n, ".js"):
			t.Errorf("js/%s is embedded but is not a .js file", n)
		case strings.Contains(n, ".test.") || strings.Contains(n, ".edge.") || strings.HasSuffix(n, ".map"):
			t.Errorf("js/%s looks like a test or a map and must not ship", n)
		case strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_"):
			t.Errorf("js/%s starts with a character go:embed treats specially", n)
		}
	}
	if !slices.Contains(embeddedJS(t), "main.js") {
		t.Error("main.js, the page's entry point, is not embedded")
	}
}

// The embedded set as the running server sees it: every script is served as
// JavaScript, and a test file is a 404, not the index page and not its source.
func TestServerServesScriptsAndHidesTestFiles(t *testing.T) {
	srv := server.New(nil, Files, nil, nil)
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	for _, name := range embeddedJS(t) {
		rec := get("/js/" + name)
		if rec.Code != http.StatusOK {
			t.Errorf("/js/%s = %d, want 200", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("/js/%s Content-Type = %q, want a JavaScript type", name, ct)
		}
	}
	disk, err := filepath.Glob("js/*.test.mjs")
	if err != nil || len(disk) == 0 {
		t.Fatalf("no test files found on disk: %v", err)
	}
	for _, p := range disk {
		rec := get("/" + filepath.ToSlash(p))
		if rec.Code != http.StatusNotFound {
			t.Errorf("/%s = %d, want 404", p, rec.Code)
		}
	}
}

// The step that runs the frontend tests has to live in the frontend job, run
// by itself, and be unable to pass by being skipped or its failure forgiven.
func TestCIFrontendTestStepIsInTheFrontendJobAndCannotBeForgiven(t *testing.T) {
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	job := ""
	step := -1
	for i, l := range lines {
		trim := strings.TrimSpace(l)
		if indent := len(l) - len(strings.TrimLeft(l, " ")); indent == 2 && strings.HasSuffix(trim, ":") && !strings.HasPrefix(trim, "#") {
			job = strings.TrimSuffix(trim, ":")
		}
		if trim == "run: node --test web/js/*.test.mjs" {
			if job != "frontend" {
				t.Errorf("line %d runs the frontend tests in job %q, want frontend", i+1, job)
			}
			step = i
		}
	}
	if step < 0 {
		t.Fatal("ci.yml has no step running the frontend tests")
	}
	start, end := step, step
	for start > 0 && !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ") {
		start--
	}
	for end+1 < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end+1]), "- ") && strings.TrimSpace(lines[end+1]) != "" {
		end++
	}
	for _, l := range lines[start : end+1] {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		for _, bad := range []string{"continue-on-error", "|| true", "nullglob", "if:", "set +e"} {
			if strings.Contains(trim, bad) {
				t.Errorf("the frontend test step contains %q: %s", bad, trim)
			}
		}
	}
}
