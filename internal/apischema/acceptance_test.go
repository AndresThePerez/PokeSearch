//go:build acceptance

package apischema

import (
	"bytes"
	"encoding/json"
	"io"
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
// ids in one batch with a missing one, the set list, a miss, the two pre-v1
// endpoints PokéPrices reads, and the four the page's panels read (suggest,
// explain, compare, stats), with the error answers each can give.
func TestSeededIndexMatchesSchemas(t *testing.T) {
	schemas := loadSchemas(t)
	base := liveBase()
	cases := []struct {
		path   string
		status int
		schema string
		id     string // the card the answer must be
		// contains proves the case exercised the shape it is named for.
		contains string
	}{
		{"/api/v1/cards/base1-4", 200, "card-response.json", "base1-4", ""},
		{"/api/v1/cards/ex10-%3F", 200, "card-response.json", "ex10-?", ""},
		{"/api/v1/cards/ex10-!", 200, "card-response.json", "ex10-!", ""},
		{"/api/v1/cards?ids=base1-4,ex10-%3F,ex10-!,nope-1", 200, "cards-batch-response.json", "", ""},
		{"/api/v1/cards/nope-1", 404, "error.json", "", ""},
		{"/api/v1/sets", 200, "sets-response.json", "", ""},
		{"/api/meta", 200, "meta.json", "", ""},
		{"/api/search?q=charizard", 200, "search.json", "", ""},

		{"/api/suggest?q=pika", 200, "suggest.json", "", `"Pikachu"`},
		{"/api/suggest?q=pikuchu", 200, "suggest.json", "", `"Pikachu"`},
		{"/api/suggest?q=pokemon", 200, "suggest.json", "", `"Pokémon `},
		{"/api/suggest?q=", 200, "suggest.json", "", `"suggestions":[]`},
		{"/api/suggest?q=zzzzqqqqxxxx", 200, "suggest.json", "", `"suggestions":[]`},
		{"/api/suggest?q=" + strings.Repeat("a", 257), 400, "error.json", "", `"field":"q"`},

		{"/api/explain?id=base1-4&q=charizard", 200, "explain.json", "", `"found":true`},
		{"/api/explain?id=ex10-%3F&q=unown", 200, "explain.json", "", `"id":"ex10-?"`},
		{"/api/explain?id=nope-999&q=x", 200, "explain.json", "", `"found":false`},
		{"/api/explain?id=a%20b&q=x", 400, "error.json", "", `"field":"id"`},
		{"/api/explain?id=base1-4", 400, "error.json", "", `"field":"q"`},
		{"/api/explain?id=base1-4&q=" + strings.Repeat("a", 257), 400, "error.json", "", `"field":"q"`},

		{"/api/compare?q=pikachu", 200, "compare.json", "", `"profile":"served"`},
		{"/api/compare?q=pikachu&a=served&b=runner-up", 200, "compare.json", "", `"profile":"runner-up"`},
		{"/api/compare?q=zzzzqqqqxxxx", 200, "compare.json", "", `"spearman_rho_union":null`},
		{"/api/compare", 400, "error.json", "", `"field":"q"`},
		{"/api/compare?q=pikachu&b=bogus", 400, "error.json", "", `"field":"b"`},

		{"/api/stats", 200, "stats.json", "", `"total":20635`},
		{"/api/stats?debug=true", 200, "stats.json", "", `"dsl":{`},
		{"/api/stats?debug=1", 200, "stats.json", "", `"dsl":{`},

		{"/api/nope", 404, "error.json", "", `"code":"not_found"`},
		{"/api/search?sort=bogus", 400, "error.json", "", `"field":"sort"`},
	}
	for _, tc := range cases {
		name := tc.path
		if len(name) > 80 {
			name = name[:80] + "..." // the over-long q cases would otherwise name the test with 257 characters
		}
		t.Run(name, func(t *testing.T) {
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
			if !bytes.Contains(got.body, []byte(tc.contains)) {
				t.Errorf("body lacks %s, so the case did not exercise its shape: %s", tc.contains, got.body)
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

// A method the endpoint does not answer is the 405 envelope with an Allow
// header naming the methods it does.
func TestSeededIndexAnswersAWrongMethodWithTheEnvelope(t *testing.T) {
	schemas := loadSchemas(t)
	for _, path := range []string{"/api/search", "/api/suggest?q=pika", "/api/stats"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, liveBase()+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := httpClient.Do(req)
			if err != nil {
				t.Fatalf("POST %s: %v", path, err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status %d, want 405: %s", res.StatusCode, body)
			}
			if allow := res.Header.Get("Allow"); !strings.Contains(allow, "GET") {
				t.Errorf("Allow = %q, want it to list GET", allow)
			}
			bad, err := schemas.violations("error.json", body)
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) > 0 {
				t.Errorf("answer does not satisfy error.json:\n  %s\nbody: %s", strings.Join(bad, "\n  "), body)
			}
			if !bytes.Contains(body, []byte(`"code":"method_not_allowed"`)) {
				t.Errorf("body = %s, want the code method_not_allowed", body)
			}
		})
	}
}
