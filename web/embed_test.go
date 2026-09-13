package web

import (
	"io/fs"
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
