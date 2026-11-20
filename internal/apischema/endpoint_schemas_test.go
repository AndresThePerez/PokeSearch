package apischema

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// serverBody runs the real server over the fake cluster and returns the 200
// body at path as a generic JSON value.
func serverBody(t *testing.T, cluster *fakeCluster, path string) map[string]any {
	t.Helper()
	got := fetch(t, startServer(t, cluster), path, nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, got.status, got.body)
	}
	var doc map[string]any
	if err := json.Unmarshal(got.body, &doc); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return doc
}

// A route added under /api/ with no schema is reported, and every route the
// server has today is mapped.
func TestAnAPIRouteWithoutASchemaIsReported(t *testing.T) {
	patterns := muxPatterns(t)
	if missing := routesWithoutSchema(patterns); len(missing) > 0 {
		t.Fatalf("routes with no schema: %v", missing)
	}
	extra := append(slices.Clone(patterns), "GET /api/widgets", "POST /api/suggest", "GET /api/v2/cards")
	want := []string{"GET /api/widgets", "POST /api/suggest", "GET /api/v2/cards"}
	if got := routesWithoutSchema(extra); !slices.Equal(got, want) {
		t.Errorf("routesWithoutSchema = %v, want %v", got, want)
	}
	for _, route := range []string{"GET /api/suggest", "GET /api/explain", "GET /api/compare", "GET /api/stats"} {
		if !slices.Contains(patterns, route) {
			t.Errorf("the server registers no %q", route)
		}
		if _, ok := legacyRoutes[route]; !ok {
			t.Errorf("%s is not mapped to a schema", route)
		}
	}
}

// The smallest answer of each endpoint is valid, and is as small as claimed.
func TestSmallestAnswersValidate(t *testing.T) {
	schemas := loadSchemas(t)
	empty := func() *fakeCluster { return &fakeCluster{cards: map[string]tcg.Card{}} }

	suggest := serverBody(t, empty(), "/api/suggest?q=")
	if list, _ := suggest["suggestions"].([]any); len(suggest) != 1 || list == nil || len(list) != 0 {
		t.Errorf("an empty q answers %v, want only an empty suggestions list", suggest)
	}
	if bad := mustViolations(t, schemas, "suggest.json", suggest); len(bad) > 0 {
		t.Errorf("suggest: %v", bad)
	}

	explain := serverBody(t, empty(), "/api/explain?id=nope-999&q=x")
	if explain["found"] != false || explain["score"] != float64(0) {
		t.Errorf("explain on a missing card = %v, want found:false and score 0", explain)
	}
	for _, b := range explain["branches"].([]any) {
		branch := b.(map[string]any)
		if len(branch) != 3 || branch["matched"] != false {
			t.Errorf("a branch of a missing card is %v, want only name, matched:false and score", branch)
		}
	}
	if bad := mustViolations(t, schemas, "explain.json", explain); len(bad) > 0 {
		t.Errorf("explain: %v", bad)
	}

	f := goldenCluster()
	f.hits = nil
	compare := serverBody(t, f, "/api/compare?q=zzz")
	if v, ok := compare["spearman_rho_union"]; !ok || v != nil || len(compare["deltas"].([]any)) != 0 {
		t.Errorf("compare with no hits = %v, want no deltas and a null correlation", compare)
	}
	if bad := mustViolations(t, schemas, "compare.json", compare); len(bad) > 0 {
		t.Errorf("compare: %v", bad)
	}

	stats := serverBody(t, empty(), "/api/stats")
	for _, key := range []string{"per_year", "hp", "types", "supertype", "rarity", "series"} {
		if list, ok := stats[key].([]any); !ok || len(list) != 0 {
			t.Errorf("stats %s on an empty index = %v, want an empty list", key, stats[key])
		}
	}
	if _, has := stats["dsl"]; has {
		t.Error("stats carries dsl without debug")
	}
	if bad := mustViolations(t, schemas, "stats.json", stats); len(bad) > 0 {
		t.Errorf("stats: %v", bad)
	}
}

// Each schema rejects the server's own body once it is damaged: a member
// removed, retyped or added, a value out of range.
func TestEndpointSchemasRejectMutatedBodies(t *testing.T) {
	schemas := loadSchemas(t)
	enteredCluster := func() *fakeCluster {
		f := goldenCluster()
		f.hits, f.previousHits = []string{"base1-4", "ex10-?"}, []string{"ex10-?", "base1-4", "ex10-!"}
		return f
	}
	object := func(v any) map[string]any { return v.(map[string]any) }
	list := func(v any) []any { return v.([]any) }

	tests := []struct {
		name    string
		schema  string
		cluster func() *fakeCluster
		path    string
		mutate  func(doc map[string]any)
		keyword string
	}{
		{"suggest: nine names", "suggest.json", nil, "/api/suggest?q=chari", func(d map[string]any) {
			d["suggestions"] = []any{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
		}, "maxItems"},
		{"suggest: a repeated name", "suggest.json", nil, "/api/suggest?q=chari", func(d map[string]any) {
			d["suggestions"] = []any{"Charizard", "Charizard"}
		}, "uniqueItems"},
		{"suggest: a request id", "suggest.json", nil, "/api/suggest?q=chari", func(d map[string]any) { d["request_id"] = "abc" }, "additionalProperties"},
		{"suggest: a name that is a number", "suggest.json", nil, "/api/suggest?q=chari", func(d map[string]any) {
			d["suggestions"] = []any{7}
		}, "type"},

		{"explain: no found", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { delete(d, "found") }, "required"},
		{"explain: no request id", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { delete(d, "request_id") }, "required"},
		{"explain: a branch without its score", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) {
			delete(object(list(d["branches"])[0]), "score")
		}, "required"},
		{"explain: a component without its value", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) {
			delete(object(list(object(list(d["branches"])[0])["components"])[0]), "value")
		}, "required"},
		{"explain: score as a string", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["score"] = "15.75" }, "type"},
		{"explain: took_ms fractional", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["took_ms"] = 1.5 }, "type"},
		{"explain: an unknown member", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["dsl"] = map[string]any{} }, "additionalProperties"},
		{"explain: an id with a space", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["id"] = "base1 4" }, "pattern"},
		{"explain: an empty q", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["q"] = "" }, "minLength"},
		{"explain: a q of 257 characters", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) {
			d["q"] = strings.Repeat("a", 257)
		}, "maxLength"},
		{"explain: an empty branch list", "explain.json", nil, "/api/explain?id=base1-4&q=charizard", func(d map[string]any) { d["branches"] = []any{} }, "minItems"},

		{"compare: no deltas", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { delete(d, "deltas") }, "required"},
		{"compare: no correlation member", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { delete(d, "spearman_rho_union") }, "required"},
		{"compare: a side without its boosts", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { delete(object(d["a"]), "boosts") }, "required"},
		{"compare: an unknown profile", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { object(d["b"])["profile"] = "bespoke" }, "enum"},
		{"compare: a boost as a string", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { object(object(d["a"])["boosts"])["exact"] = "8" }, "type"},
		{"compare: a negative boost", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { object(object(d["a"])["boosts"])["exact"] = -1 }, "minimum"},
		{"compare: rank zero", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) {
			object(list(object(d["a"])["results"])[0])["rank"] = 0
		}, "minimum"},
		{"compare: a result with an extra member", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) {
			object(list(object(d["a"])["results"])[0])["set"] = "base1"
		}, "additionalProperties"},
		{"compare: a delta status outside the five", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) {
			object(list(d["deltas"])[0])["status"] = "moved"
		}, "enum"},
		{"compare: a rank as a string", "compare.json", enteredCluster, "/api/compare?q=charizard", func(d map[string]any) {
			object(list(d["deltas"])[0])["rank_b"] = "1"
		}, "type"},
		{"compare: a delta without rank_a", "compare.json", enteredCluster, "/api/compare?q=charizard", func(d map[string]any) {
			delete(object(list(d["deltas"])[0]), "rank_a")
		}, "required"},
		{"compare: a correlation above one", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { d["spearman_rho_union"] = 1.0001 }, "maximum"},
		{"compare: a correlation below minus one", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { d["spearman_rho_union"] = -1.5 }, "minimum"},
		{"compare: window as a string", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { d["window"] = "10" }, "type"},
		{"compare: a request id in a side", "compare.json", nil, "/api/compare?q=charizard", func(d map[string]any) { object(d["a"])["request_id"] = "x" }, "additionalProperties"},

		{"stats: no total", "stats.json", nil, "/api/stats", func(d map[string]any) { delete(d, "total") }, "required"},
		{"stats: no request id", "stats.json", nil, "/api/stats", func(d map[string]any) { delete(d, "request_id") }, "required"},
		{"stats: a list as an object", "stats.json", nil, "/api/stats", func(d map[string]any) { d["hp"] = map[string]any{} }, "type"},
		{"stats: a year of two digits", "stats.json", nil, "/api/stats", func(d map[string]any) { object(list(d["per_year"])[0])["year"] = "99" }, "pattern"},
		{"stats: a year as a number", "stats.json", nil, "/api/stats", func(d map[string]any) { object(list(d["per_year"])[0])["year"] = 1999 }, "type"},
		{"stats: a negative HP band", "stats.json", nil, "/api/stats", func(d map[string]any) { object(list(d["hp"])[0])["from"] = -30 }, "minimum"},
		{"stats: a bucket count as a string", "stats.json", nil, "/api/stats", func(d map[string]any) { object(list(d["rarity"])[0])["count"] = "1" }, "type"},
		{"stats: a bucket with a label", "stats.json", nil, "/api/stats", func(d map[string]any) { object(list(d["series"])[0])["label"] = "Base" }, "additionalProperties"},
		{"stats: a bucket without its value", "stats.json", nil, "/api/stats", func(d map[string]any) { delete(object(list(d["types"])[0]), "value") }, "required"},
		{"stats: an unknown member", "stats.json", nil, "/api/stats", func(d map[string]any) { d["sets"] = []any{} }, "additionalProperties"},
		{"stats: max_hp fractional", "stats.json", nil, "/api/stats", func(d map[string]any) { d["max_hp"] = 120.5 }, "type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := goldenCluster()
			if tc.cluster != nil {
				cluster = tc.cluster()
			}
			doc := serverBody(t, cluster, tc.path)
			if bad := mustViolations(t, schemas, tc.schema, doc); len(bad) > 0 {
				t.Fatalf("the unmutated body must be valid, got %v", bad)
			}
			tc.mutate(doc)
			bad := mustViolations(t, schemas, tc.schema, doc)
			if len(bad) == 0 {
				t.Fatalf("a mutated body was accepted: %v", doc)
			}
			if !hasKeyword(bad, tc.keyword) {
				t.Errorf("rejected, but not on %q: %v", tc.keyword, bad)
			}
		})
	}
}

// What compare's schema cannot say, the answers keep: ranks count 1, 2, 3 from
// the top, scores never rise down a list, and a delta's status says which of
// its three numbers are null.
func TestCompareAnswersAreInternallyConsistent(t *testing.T) {
	both := goldenCluster()
	entered := goldenCluster()
	entered.hits, entered.previousHits = []string{"base1-4", "ex10-?"}, []string{"ex10-?", "base1-4", "ex10-!"}
	dropped := goldenCluster()
	dropped.previousHits = []string{"ex10-?", "base1-4"}
	seen := map[string]bool{}
	for name, f := range map[string]*fakeCluster{"same": both, "entered": entered, "dropped": dropped} {
		doc := serverBody(t, f, "/api/compare?q=charizard")
		for _, side := range []string{"a", "b"} {
			prev := 0.0
			for i, r := range doc[side].(map[string]any)["results"].([]any) {
				e := r.(map[string]any)
				if e["rank"] != float64(i+1) {
					t.Errorf("%s: %s result %d has rank %v", name, side, i, e["rank"])
				}
				if score := e["score"].(float64); i > 0 && score > prev {
					t.Errorf("%s: %s scores rise at rank %d", name, side, i+1)
				} else {
					prev = score
				}
			}
		}
		for _, d := range doc["deltas"].([]any) {
			delta := d.(map[string]any)
			status := delta["status"].(string)
			seen[status] = true
			aNull, bNull, dNull := delta["rank_a"] == nil, delta["rank_b"] == nil, delta["delta"] == nil
			var ok bool
			switch status {
			case "entered":
				ok = aNull && !bNull && dNull
			case "dropped":
				ok = !aNull && bNull && dNull
			case "up":
				ok = !aNull && !bNull && !dNull && delta["delta"].(float64) > 0
			case "down":
				ok = !aNull && !bNull && !dNull && delta["delta"].(float64) < 0
			case "same":
				ok = !aNull && !bNull && !dNull && delta["delta"].(float64) == 0
			}
			if !ok {
				t.Errorf("%s: delta %v does not match its status", name, delta)
			}
		}
	}
	for _, status := range []string{"same", "entered", "dropped", "up", "down"} {
		if !seen[status] {
			t.Errorf("no delta had status %q, so its null pattern went unchecked", status)
		}
	}
}
