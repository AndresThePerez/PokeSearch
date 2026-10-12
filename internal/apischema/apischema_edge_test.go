package apischema

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// ---------------------------------------------------------------------------
// Helpers for the edge-case tests.

// mustViolations validates doc against the named schema and fails the test on
// a harness error; it returns the failing assertions (empty when valid).
func mustViolations(t *testing.T, s *schemaSet, schema string, doc any) []string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := s.violations(schema, raw)
	if err != nil {
		t.Fatal(err)
	}
	return bad
}

func hasKeyword(bad []string, keyword string) bool {
	for _, v := range bad {
		if strings.HasSuffix(v, "/"+keyword) {
			return true
		}
	}
	return false
}

// validCardResponse is a card-response document built from the golden-set
// Charizard, as the handler would emit it, as a generic JSON value.
func validCardResponse(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"data": corpusCards()["base1-4"],
		"seed": map[string]any{"ref": "0000000000000000000000000000000000000000", "seeded_at": "2026-01-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func cardOf(doc map[string]any) map[string]any { return doc["data"].(map[string]any) }

// ---------------------------------------------------------------------------
// Types and formats are real constraints, not decoration.

func TestCardSchemaRejectsWrongTypesAndFormats(t *testing.T) {
	schemas := loadSchemas(t)
	if bad := mustViolations(t, schemas, "card-response.json", validCardResponse(t)); len(bad) > 0 {
		t.Fatalf("the control document must be valid, got %v", bad)
	}
	tests := []struct {
		name    string
		mutate  func(card map[string]any)
		keyword string
	}{
		{"set_total as a string", func(c map[string]any) { c["set_total"] = "102" }, "type"},
		{"set_total fractional", func(c map[string]any) { c["set_total"] = 102.5 }, "type"},
		{"set_total negative", func(c map[string]any) { c["set_total"] = -1 }, "minimum"},
		{"set_total null", func(c map[string]any) { c["set_total"] = nil }, "type"},
		{"hp fractional", func(c map[string]any) { c["hp"] = 1.5 }, "type"},
		{"hp negative", func(c map[string]any) { c["hp"] = -10 }, "minimum"},
		{"hp as a string", func(c map[string]any) { c["hp"] = "120" }, "type"},
		{"retreat_cost negative", func(c map[string]any) { c["retreat_cost"] = -1 }, "minimum"},
		{"release_date with slashes", func(c map[string]any) { c["release_date"] = "1999/01/09" }, "pattern"},
		{"release_date without zero padding", func(c map[string]any) { c["release_date"] = "1999-1-9" }, "pattern"},
		{"release_date compact", func(c map[string]any) { c["release_date"] = "19990109" }, "pattern"},
		{"release_date with a time part", func(c map[string]any) { c["release_date"] = "1999-01-09T00:00:00Z" }, "pattern"},
		{"release_date with a trailing newline", func(c map[string]any) { c["release_date"] = "1999-01-09\n" }, "pattern"},
		{"release_date empty", func(c map[string]any) { c["release_date"] = "" }, "pattern"},
		{"release_date as a number", func(c map[string]any) { c["release_date"] = 19990109 }, "type"},
		{"id empty", func(c map[string]any) { c["id"] = "" }, "pattern"},
		{"id starting with a dot", func(c map[string]any) { c["id"] = ".base1-4" }, "pattern"},
		{"id with a trailing newline", func(c map[string]any) { c["id"] = "base1-4\n" }, "pattern"},
		{"id with a space", func(c map[string]any) { c["id"] = "base1 4" }, "pattern"},
		{"id with a non-ASCII letter", func(c map[string]any) { c["id"] = "bäse1-4" }, "pattern"},
		{"id of 129 characters", func(c map[string]any) { c["id"] = strings.Repeat("a", 129) }, "pattern"},
		{"image_small null", func(c map[string]any) { c["image_small"] = nil }, "type"},
		{"types holding a number", func(c map[string]any) { c["types"] = []any{1} }, "type"},
		{"national_pokedex_numbers holding a string", func(c map[string]any) { c["national_pokedex_numbers"] = []any{"6"} }, "type"},
		{"attack damage_value negative", func(c map[string]any) {
			c["attacks"].([]any)[0].(map[string]any)["damage_value"] = -5
		}, "minimum"},
		{"attack converted_cost as a string", func(c map[string]any) {
			c["attacks"].([]any)[0].(map[string]any)["converted_cost"] = "4"
		}, "type"},
		{"weakness as a string", func(c map[string]any) { c["weaknesses"] = []any{"Water"} }, "type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := validCardResponse(t)
			tc.mutate(cardOf(doc))
			bad := mustViolations(t, schemas, "card-response.json", doc)
			if len(bad) == 0 {
				t.Fatalf("a card with %s was accepted", tc.name)
			}
			if !hasKeyword(bad, tc.keyword) {
				t.Errorf("rejected, but not on %q: %v", tc.keyword, bad)
			}
		})
	}
}

func TestCardSchemaAcceptsTheIdAndDateBoundaries(t *testing.T) {
	schemas := loadSchemas(t)
	for _, id := range []string{"a", "0", "ex10-?", "ex10-!", "base1-4", "x.y_z-1", strings.Repeat("a", 128)} {
		doc := validCardResponse(t)
		cardOf(doc)["id"] = id
		if bad := mustViolations(t, schemas, "card-response.json", doc); len(bad) > 0 {
			t.Errorf("id %q is one the v1 lookup accepts, but the schema rejects it: %v", id, bad)
		}
	}
	// hp is absent, not zero, when a card prints none (the field is omitempty).
	doc := validCardResponse(t)
	delete(cardOf(doc), "hp")
	if bad := mustViolations(t, schemas, "card-response.json", doc); len(bad) > 0 {
		t.Errorf("a card without hp (a Trainer) must be valid: %v", bad)
	}
}

// ---------------------------------------------------------------------------
// seed: always present, both members nullable.

func TestSeedObjectIsAlwaysPresentAndNullable(t *testing.T) {
	schemas := loadSchemas(t)
	ref := "0000000000000000000000000000000000000000"
	tests := []struct {
		name  string
		seed  any
		valid bool
	}{
		{"both members set", map[string]any{"ref": ref, "seeded_at": "2026-01-01T00:00:00Z"}, true},
		{"both members null", map[string]any{"ref": nil, "seeded_at": nil}, true},
		{"ref set, seeded_at null", map[string]any{"ref": ref, "seeded_at": nil}, true},
		{"ref null, seeded_at set", map[string]any{"ref": nil, "seeded_at": "2026-01-01T00:00:00Z"}, true},
		{"seed itself null", nil, false},
		{"seed an empty object", map[string]any{}, false},
		{"ref omitted", map[string]any{"seeded_at": nil}, false},
		{"seeded_at omitted", map[string]any{"ref": nil}, false},
		{"ref a number", map[string]any{"ref": 5, "seeded_at": nil}, false},
		{"ref an empty string", map[string]any{"ref": "", "seeded_at": nil}, false},
		{"extra member", map[string]any{"ref": nil, "seeded_at": nil, "dataset": "x"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := validCardResponse(t)
			doc["seed"] = tc.seed
			bad := mustViolations(t, schemas, "card-response.json", doc)
			if tc.valid && len(bad) > 0 {
				t.Errorf("must be valid: %v", bad)
			}
			if !tc.valid && len(bad) == 0 {
				t.Errorf("must be rejected")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The error envelope.

func TestErrorSchemaCodeAndRequestIDBoundaries(t *testing.T) {
	schemas := loadSchemas(t)
	envelope := func(code, requestID string) map[string]any {
		return map[string]any{
			"error":      map[string]any{"code": code, "message": "m"},
			"request_id": requestID,
		}
	}
	for _, code := range []string{"invalid_param", "not_found", "unauthorized", "es_unavailable"} {
		if bad := mustViolations(t, schemas, "error.json", envelope(code, "abc")); len(bad) > 0 {
			t.Errorf("code %q is one the server emits, but the schema rejects it: %v", code, bad)
		}
	}
	for _, code := range []string{"teapot", "", "NOT_FOUND", "not_found ", "forbidden", "rate_limited"} {
		if bad := mustViolations(t, schemas, "error.json", envelope(code, "abc")); !hasKeyword(bad, "enum") {
			t.Errorf("unknown code %q must fail the enum, got %v", code, bad)
		}
	}
	for _, id := range []string{"a", strings.Repeat("a", 64), "req-1.2:3_4"} {
		if bad := mustViolations(t, schemas, "error.json", envelope("not_found", id)); len(bad) > 0 {
			t.Errorf("request_id %q must be valid: %v", id, bad)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 65), "has space", "new\nline", "emoji-\U0001F409", "a/b", "abc\n"} {
		if bad := mustViolations(t, schemas, "error.json", envelope("not_found", id)); !hasKeyword(bad, "pattern") {
			t.Errorf("request_id %q must fail the pattern, got %v", id, bad)
		}
	}

	t.Run("the field is optional but never empty", func(t *testing.T) {
		doc := envelope("invalid_param", "abc")
		doc["error"].(map[string]any)["field"] = "ids"
		if bad := mustViolations(t, schemas, "error.json", doc); len(bad) > 0 {
			t.Errorf("a named field must be valid: %v", bad)
		}
		doc["error"].(map[string]any)["field"] = ""
		if bad := mustViolations(t, schemas, "error.json", doc); len(bad) == 0 {
			t.Error("an empty field name must be rejected")
		}
	})
	t.Run("an envelope without request_id or without a message is rejected", func(t *testing.T) {
		noID := envelope("not_found", "abc")
		delete(noID, "request_id")
		if bad := mustViolations(t, schemas, "error.json", noID); !hasKeyword(bad, "required") {
			t.Errorf("missing request_id: %v", bad)
		}
		noMsg := envelope("not_found", "abc")
		delete(noMsg["error"].(map[string]any), "message")
		if bad := mustViolations(t, schemas, "error.json", noMsg); !hasKeyword(bad, "required") {
			t.Errorf("missing message: %v", bad)
		}
	})
}

// ---------------------------------------------------------------------------
// Batch limits.

func TestBatchSchemaEnforcesItsLimits(t *testing.T) {
	schemas := loadSchemas(t)
	card := func() map[string]any { return cardOf(validCardResponse(t)) }
	seed := map[string]any{"ref": nil, "seeded_at": nil}
	batch := func(nData, nMissing int, missing []any) map[string]any {
		data := make([]any, nData)
		for i := range data {
			data[i] = card()
		}
		if missing == nil {
			missing = make([]any, nMissing)
			for i := range missing {
				missing[i] = "nope-" + strconv.Itoa(i)
			}
		}
		return map[string]any{"data": data, "missing": missing, "seed": seed}
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(100, 100, nil)); len(bad) > 0 {
		t.Errorf("100 cards and 100 missing ids are within the limits: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(0, 0, nil)); len(bad) > 0 {
		t.Errorf("an empty answer is valid: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(101, 0, nil)); !hasKeyword(bad, "maxItems") {
		t.Errorf("101 cards must exceed maxItems: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(0, 101, nil)); !hasKeyword(bad, "maxItems") {
		t.Errorf("101 missing ids must exceed maxItems: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(0, 0, []any{"a", "b", "a"})); !hasKeyword(bad, "uniqueItems") {
		t.Errorf("a repeated missing id must fail uniqueItems: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(0, 0, []any{"bad id"})); !hasKeyword(bad, "pattern") {
		t.Errorf("a missing id that fails the id pattern must be rejected: %v", bad)
	}
	if bad := mustViolations(t, schemas, "cards-batch-response.json", batch(0, 0, []any{7})); !hasKeyword(bad, "type") {
		t.Errorf("a numeric missing id must be rejected: %v", bad)
	}
}

// ---------------------------------------------------------------------------
// Every object in every live answer is closed, and exactly the always-present
// members are required: injecting a member anywhere is rejected, and removing
// a member is rejected exactly when the handler always emits it.

func pointerSegments(ptr string) []string {
	if ptr == "" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(ptr, "/"), "/")
}

// objectPaths collects the JSON pointer of every object in v, skipping the
// subtree at skipPrefix (search.json's opaque debug echo).
func objectPaths(v any, ptr, skipPrefix string, out *[]string) {
	if skipPrefix != "" && ptr == skipPrefix {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		*out = append(*out, ptr)
		for k, c := range x {
			objectPaths(c, ptr+"/"+k, skipPrefix, out)
		}
	case []any:
		for i, c := range x {
			objectPaths(c, ptr+"/"+strconv.Itoa(i), skipPrefix, out)
		}
	}
}

func objectAt(t *testing.T, root any, ptr string) map[string]any {
	t.Helper()
	cur := root
	for _, seg := range pointerSegments(ptr) {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i >= len(x) {
				t.Fatalf("bad pointer %q", ptr)
			}
			cur = x[i]
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("%q is not an object", ptr)
	}
	return m
}

// shape turns "/data/0/attacks/1" into "/data/*/attacks/*".
func shape(ptr string) string {
	segs := pointerSegments(ptr)
	for i, s := range segs {
		if _, err := strconv.Atoi(s); err == nil {
			segs[i] = "*"
		}
	}
	if len(segs) == 0 {
		return ""
	}
	return "/" + strings.Join(segs, "/")
}

// optionalMembers is what a handler may legitimately omit from the object at
// the given shape, derived from the Go types where one exists.
func optionalMembers(schema, shp string) []string {
	all, always := jsonFields(reflect.TypeFor[tcg.Card]())
	cardOptional := diff(all, always)
	cardAt := map[string]string{"card-response.json": "/data", "cards-batch-response.json": "/data/*", "search.json": "/results/*"}
	if prefix, ok := cardAt[schema]; ok && strings.HasPrefix(shp, prefix) {
		switch strings.TrimPrefix(shp, prefix) {
		case "":
			return cardOptional
		case "/attacks/*":
			attackAll, attackAlways := jsonFields(reflect.TypeFor[tcg.Attack]())
			return diff(attackAll, attackAlways)
		}
		return nil
	}
	switch {
	case schema == "error.json" && shp == "/error":
		return []string{"field"}
	case schema == "search.json" && shp == "":
		return []string{"matched", "highlights", "did_you_mean", "dsl"}
	case schema == "search.json" && shp == "/highlights/*":
		return []string{"name", "attacks.name", "abilities.name", "attacks.text", "abilities.text", "flavor_text"}
	case schema == "search.json" && strings.HasPrefix(shp, "/facets/") && strings.HasSuffix(shp, "/*"):
		return []string{"label", "release_date"}
	}
	return nil
}

func diff(all, always []string) []string {
	var out []string
	for _, n := range all {
		if !slices.Contains(always, n) {
			out = append(out, n)
		}
	}
	return out
}

func TestLiveAnswersAreClosedAtEveryDepthAndRequireExactlyTheAlwaysPresentMembers(t *testing.T) {
	schemas := loadSchemas(t)
	visited := map[string]map[string]bool{}
	rejectedRemovals := map[string][]string{}

	for _, tc := range liveCases {
		if tc.status != 200 && tc.schema != "error.json" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			cluster := goldenCluster()
			if tc.cluster != nil {
				cluster = tc.cluster()
			}
			var header http.Header
			if tc.header != nil {
				header = tc.header(t)
			}
			got := fetch(t, startServer(t, cluster), tc.path, header)
			if got.status != tc.status {
				t.Fatalf("status %d, want %d: %s", got.status, tc.status, got.body)
			}
			fresh := func() any {
				var v any
				if err := json.Unmarshal(got.body, &v); err != nil {
					t.Fatalf("not JSON: %v", err)
				}
				return v
			}
			var paths []string
			objectPaths(fresh(), "", "/dsl", &paths)
			if visited[tc.schema] == nil {
				visited[tc.schema] = map[string]bool{}
			}
			for _, p := range paths {
				shp := shape(p)
				visited[tc.schema][shp] = true

				// An unknown member anywhere is rejected.
				doc := fresh()
				objectAt(t, doc, p)["zz_extra"] = 1
				if bad := mustViolations(t, schemas, tc.schema, doc); !hasKeyword(bad, "additionalProperties") {
					t.Errorf("%s at %q accepted an unknown member (violations %v)", tc.schema, p, bad)
				}

				// Removing a member is rejected exactly when the handler
				// always emits it.
				optional := optionalMembers(tc.schema, shp)
				for key := range objectAt(t, fresh(), p) {
					doc := fresh()
					delete(objectAt(t, doc, p), key)
					bad := mustViolations(t, schemas, tc.schema, doc)
					wantOptional := slices.Contains(optional, key)
					if wantOptional && len(bad) > 0 {
						t.Errorf("%s at %q: %q is optional, but removing it is rejected: %v", tc.schema, p, key, bad)
					}
					if !wantOptional {
						if !hasKeyword(bad, "required") {
							t.Errorf("%s at %q: %q is always emitted, but removing it is accepted (violations %v)", tc.schema, p, key, bad)
						} else if tc.schema == "card-response.json" && shp == "/data" {
							rejectedRemovals[tc.name] = append(rejectedRemovals[tc.name], key)
						}
					}
				}
			}
		})
	}

	// Non-vacuity: the live answers reached every nested object kind.
	for schema, want := range map[string][]string{
		"card-response.json":        {"", "/data", "/data/attacks/*", "/data/abilities/*", "/data/weaknesses/*", "/data/resistances/*", "/seed"},
		"cards-batch-response.json": {"", "/data/*", "/data/*/attacks/*", "/seed"},
		"sets-response.json":        {"", "/data/*", "/seed"},
		"error.json":                {"", "/error"},
		"meta.json":                 {"", "/seed"},
		"search.json":               {"", "/results/*", "/results/*/attacks/*", "/highlights/*", "/facets", "/facets/sets/*", "/facets/types/*"},
	} {
		for _, shp := range want {
			if !visited[schema][shp] {
				t.Errorf("no live answer put an object at %q under %s, so its closure went unchecked (visited %v)", shp, schema, visited[schema])
			}
		}
	}
	// The plan's eleven required card members are exactly what removal rejects.
	wantRequired := []string{"id", "name", "supertype", "number", "set_id", "set_name", "set_series", "set_total", "release_date", "image_small", "image_large"}
	slices.Sort(wantRequired)
	got := rejectedRemovals["card hit"]
	slices.Sort(got)
	if !slices.Equal(got, wantRequired) {
		t.Errorf("removing a card member is rejected for %v, want the plan's eleven %v", got, wantRequired)
	}
}

// ---------------------------------------------------------------------------
// The pre-v1 endpoints in the states the main table does not reach.

func TestLegacyEndpointsValidateInTheirEdgeStates(t *testing.T) {
	schemas := loadSchemas(t)
	empty := func() *fakeCluster { f := goldenCluster(); f.hits = nil; return f }
	tests := []struct {
		name    string
		path    string
		cluster func() *fakeCluster
		schema  string
	}{
		{"browse with no hits", "/api/search?set=zzz", empty, "search.json"},
		{"a page past the last", "/api/search?q=charizard&page=99", nil, "search.json"},
		{"a non-ASCII query", "/api/search?q=%E2%9C%A8%F0%9F%90%89", empty, "search.json"},
		{"a page size at the cap", "/api/search?q=charizard&page_size=100", nil, "search.json"},
		{"browse sorted by name", "/api/search?sort=name", nil, "search.json"},
		{"meta on an empty cluster", "/api/meta", func() *fakeCluster { return &fakeCluster{cards: map[string]tcg.Card{}} }, "meta.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := goldenCluster()
			if tc.cluster != nil {
				cluster = tc.cluster()
			}
			got := fetch(t, startServer(t, cluster), tc.path, nil)
			if got.status != http.StatusOK && got.status != http.StatusBadRequest {
				t.Fatalf("status %d: %s", got.status, got.body)
			}
			schema := tc.schema
			if got.status == http.StatusBadRequest {
				schema = "error.json"
			}
			bad, err := schemas.violations(schema, got.body)
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) > 0 {
				t.Errorf("%d answer does not satisfy %s: %v\nbody: %s", got.status, schema, bad, got.body)
			}
		})
	}
}

func TestBatchCollapsesDuplicatesAndTrimsIdsInsideTheSchema(t *testing.T) {
	schemas := loadSchemas(t)
	got := fetch(t, startServer(t, goldenCluster()), "/api/v1/cards?ids=base1-4,%20base1-4,nope-1,nope-1,%20nope-2%20", nil)
	if got.status != http.StatusOK {
		t.Fatalf("status %d: %s", got.status, got.body)
	}
	bad, err := schemas.violations("cards-batch-response.json", got.body)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("duplicates and padding broke the missing[] contract (uniqueItems/pattern): %v\nbody: %s", bad, got.body)
	}
	var body struct {
		Data    []map[string]any `json:"data"`
		Missing []string         `json:"missing"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || !slices.Equal(body.Missing, []string{"nope-1", "nope-2"}) {
		t.Errorf("want one card and missing [nope-1 nope-2], got %d cards, missing %v", len(body.Data), body.Missing)
	}
}

// ---------------------------------------------------------------------------
// The validator stays one seam: the dependency is pinned, and only the two
// helper functions in apischema_test.go touch it.

func TestValidatorIsPinnedAndConfinedToOneSeam(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.Contains(line, "santhosh-tekuri/jsonschema/v6") {
			found = true
			if strings.Fields(line)[1] != "v6.0.3" || strings.Contains(line, "// indirect") {
				t.Errorf("go.mod line %q: want a direct require at exactly v6.0.3", line)
			}
		}
	}
	if !found {
		t.Error("go.mod does not require github.com/santhosh-tekuri/jsonschema/v6")
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		importsLib := strings.Contains(string(src), "\"github.com/santhosh-tekuri/"+"jsonschema/v6\"")
		if importsLib && f != "apischema_test.go" {
			t.Errorf("%s imports the JSON Schema library; only apischema_test.go may (the OQ2 seam)", f)
		}
	}
}

// ---------------------------------------------------------------------------
// R147: CI must vet every tag-gated package, so the tagged apischema test
// cannot stop compiling unseen.

func trimmedLines(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

func TestCIAndReadmeVetEveryAcceptanceTaggedPackage(t *testing.T) {
	const wide = "go vet -tags acceptance ./internal/..."
	for _, tc := range []struct{ file, vetLine string }{
		{"../../.github/workflows/ci.yml", "- run: " + wide},
		{"../../README.md", wide},
	} {
		lines := trimmedLines(t, tc.file)
		if !slices.Contains(lines, tc.vetLine) {
			t.Errorf("%s has no line %q: the acceptance-tagged internal/apischema test is never vetted", tc.file, tc.vetLine)
		}
		for _, l := range lines {
			if strings.HasSuffix(l, "go vet -tags acceptance ./internal/acceptance") {
				t.Errorf("%s still vets only ./internal/acceptance: %q", tc.file, l)
			}
		}
	}
}

// The seeded-stack acceptance run covers both acceptance-tagged packages: the
// API matrix in internal/acceptance and the live schema check here, so the
// golden-set answers are validated wherever the acceptance tests run (the
// acceptance workflow and the README command that mirrors it).
func TestAcceptanceRunsCoverEveryAcceptanceTaggedPackage(t *testing.T) {
	const pkgs = "./internal/acceptance ./internal/apischema"
	for _, tc := range []struct{ file, runLine string }{
		{"../../.github/workflows/acceptance.yml", "run: POKESEARCH_URL=http://localhost:8080 go test -tags acceptance -count=1 -v " + pkgs},
		{"../../README.md", "POKESEARCH_URL=http://localhost:8080 go test -tags acceptance -count=1 " + pkgs},
	} {
		lines := trimmedLines(t, tc.file)
		if !slices.Contains(lines, tc.runLine) {
			t.Errorf("%s has no line %q: the acceptance-tagged internal/apischema test never runs against the seeded stack", tc.file, tc.runLine)
		}
		runs := 0
		for _, l := range lines {
			if !strings.Contains(l, "go test -tags acceptance") {
				continue
			}
			runs++
			fields := strings.Fields(l)
			if !slices.Contains(fields, "./internal/acceptance") || !slices.Contains(fields, "./internal/apischema") {
				t.Errorf("%s runs the acceptance tests without both packages: %q", tc.file, l)
			}
		}
		if runs == 0 {
			t.Errorf("%s has no go test -tags acceptance line at all", tc.file)
		}
	}
}
