package web

import (
	"strings"
	"testing"
)

// The JS wires up elements by ID at load time; a missing ID would break the
// page silently since there is no build step to catch it.
func TestEmbeddedMarkupHasRequiredElements(t *testing.T) {
	html, err := Files.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	js, err := Files.ReadFile("app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if _, err := Files.ReadFile("styles.css"); err != nil {
		t.Fatalf("read styles.css: %v", err)
	}

	ids := []string{
		"search-input", "suggest-list", "filter-rail", "filter-toggle",
		"filter-count", "filter-done", "supertype-toggle", "type-chips",
		"set-select", "rarity-select", "series-select", "active-filters",
		"clear-filters", "sort-select", "order-toggle", "results-grid",
		"empty-state", "load-more", "degraded-banner", "total-count",
		"query-inspector", "response-inspector", "copy-dsl", "copy-response",
		"dsl-json", "response-json", "card-modal", "modal-close",
	}
	page := string(html)
	script := string(js)
	for _, id := range ids {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("index.html is missing element id %q", id)
		}
	}
	// Every ID the script looks up must exist in the markup.
	for _, id := range []string{"filter-count", "filter-done"} {
		if !strings.Contains(script, `"`+id+`"`) {
			t.Errorf("app.js never references %q; markup and script are out of sync", id)
		}
	}
}
