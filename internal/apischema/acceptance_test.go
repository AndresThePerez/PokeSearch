//go:build acceptance

package apischema

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

// liveBase is the seeded stack under test, the same default and override as
// internal/acceptance: POKESEARCH_URL, else http://localhost:8080.
func liveBase() string {
	if u := os.Getenv("POKESEARCH_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8080"
}

// The seeded index's real answers satisfy the published schemas: the three
// golden-set cards one by one (two of them ids a URL path mangles), the same
// ids in one batch with a missing one, the set list, a miss, and the two
// pre-v1 endpoints PokéPrices reads.
func TestSeededIndexMatchesSchemas(t *testing.T) {
	schemas := loadSchemas(t)
	base := liveBase()
	cases := []struct {
		path   string
		status int
		schema string
		id     string // the card the answer must be
	}{
		{"/api/v1/cards/base1-4", 200, "card-response.json", "base1-4"},
		{"/api/v1/cards/ex10-%3F", 200, "card-response.json", "ex10-?"},
		{"/api/v1/cards/ex10-!", 200, "card-response.json", "ex10-!"},
		{"/api/v1/cards?ids=base1-4,ex10-%3F,ex10-!,nope-1", 200, "cards-batch-response.json", ""},
		{"/api/v1/cards/nope-1", 404, "error.json", ""},
		{"/api/v1/sets", 200, "sets-response.json", ""},
		{"/api/meta", 200, "meta.json", ""},
		{"/api/search?q=charizard", 200, "search.json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := fetch(t, base, tc.path, http.Header{})
			if got.status != tc.status {
				t.Fatalf("status %d, want %d: %s", got.status, tc.status, got.body)
			}
			bad, err := schemas.violations(tc.schema, got.body)
			if err != nil {
				t.Fatalf("%s: %v", tc.schema, err)
			}
			if len(bad) > 0 {
				t.Errorf("answer does not satisfy %s:\n  %s\nbody: %s", tc.schema, strings.Join(bad, "\n  "), got.body)
			}
			if tc.id == "" {
				return
			}
			var card struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.NewDecoder(bytes.NewReader(got.body)).Decode(&card); err != nil || card.Data.ID != tc.id {
				t.Errorf("data.id = %q (%v), want %q", card.Data.ID, err, tc.id)
			}
		})
	}
	// The batch answers in request order and reports the unknown id.
	got := fetch(t, base, "/api/v1/cards?ids=base1-4,ex10-%3F,ex10-!,nope-1", http.Header{})
	if !bytes.Contains(got.body, []byte(`"missing":["nope-1"]`)) {
		t.Errorf("batch missing = %s, want [\"nope-1\"]", got.body)
	}
}
