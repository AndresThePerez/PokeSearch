package apischema

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The four endpoint schemas, attacked from the outside: every member given
// every wrong JSON type, every bound taken to its edge and one step past it,
// and a census proving no keyword in the schemas can be loosened without a
// vector here failing.

// edgeDeleted marks a member to remove rather than set.
type edgeDeleted struct{}

// edgeClone copies a decoded JSON value.
func edgeClone(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// edgeSet writes v at the slash-separated pointer (an array index is a number)
// in a copy of doc and returns the copy. A pointer of "" replaces the document.
func edgeSet(t testing.TB, doc any, ptr string, v any) any {
	t.Helper()
	doc = edgeClone(t, doc)
	segs := pointerSegments(ptr)
	if len(segs) == 0 {
		return v
	}
	cur := doc
	for _, seg := range segs[:len(segs)-1] {
		cur = edgeStep(t, cur, seg, ptr)
	}
	last := segs[len(segs)-1]
	switch x := cur.(type) {
	case map[string]any:
		if _, del := v.(edgeDeleted); del {
			delete(x, last)
		} else {
			x[last] = v
		}
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil || i >= len(x) {
			t.Fatalf("pointer %q: no element %q", ptr, last)
		}
		x[i] = v
	default:
		t.Fatalf("pointer %q: cannot set below a %T", ptr, cur)
	}
	return doc
}

func edgeStep(t testing.TB, cur any, seg, ptr string) any {
	t.Helper()
	switch x := cur.(type) {
	case map[string]any:
		next, ok := x[seg]
		if !ok {
			t.Fatalf("pointer %q: no member %q", ptr, seg)
		}
		return next
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil || i >= len(x) {
			t.Fatalf("pointer %q: no element %q", ptr, seg)
		}
		return x[i]
	}
	t.Fatalf("pointer %q: cannot step into a %T", ptr, cur)
	return nil
}

// edgeDocs are one valid answer of each endpoint, rich enough to reach every
// member: explain with a matched branch carrying components, compare with a card
// that entered, one that dropped and one that moved, stats with its debug echo.
func edgeDocs(t *testing.T) map[string]any {
	t.Helper()
	both := goldenCluster()
	both.hits, both.previousHits = []string{"base1-4", "ex10-?"}, []string{"ex10-?", "ex10-!"}
	docs := map[string]any{
		"suggest.json": serverBody(t, goldenCluster(), "/api/suggest?q=chari"),
		"explain.json": serverBody(t, goldenCluster(), "/api/explain?id=base1-4&q=charizard"),
		"compare.json": serverBody(t, both, "/api/compare?q=charizard"),
		"stats.json":   serverBody(t, goldenCluster(), "/api/stats?debug=1"),
	}
	schemas := loadSchemas(t)
	for name, doc := range docs {
		if bad := mustViolations(t, schemas, name, doc); len(bad) > 0 {
			t.Fatalf("the fixture for %s is not valid: %v", name, bad)
		}
	}
	statuses := map[string]bool{}
	for _, d := range docs["compare.json"].(map[string]any)["deltas"].([]any) {
		statuses[d.(map[string]any)["status"].(string)] = true
	}
	for _, s := range []string{"entered", "dropped"} {
		if !statuses[s] {
			t.Fatalf("the compare fixture has no %q delta (has %v)", s, statuses)
		}
	}
	return docs
}

// edgeCensus is the set of keyword locations the schemas declare, as
// violations reports them: "<file>#<pointer>/<keyword>".
func edgeCensus(t *testing.T, names []string) map[string]bool {
	t.Helper()
	want := map[string]bool{}
	keywords := []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "pattern", "enum", "required", "type"}
	for _, name := range names {
		walkSchema("", readSchema(t, name), func(ptr string, node map[string]any) {
			for _, kw := range keywords {
				if _, ok := node[kw]; ok {
					want[name+"#"+ptr+"/"+kw] = true
				}
			}
			if node["additionalProperties"] == false {
				want[name+"#"+ptr+"/additionalProperties"] = true
			}
		})
	}
	return want
}

var edgeSchemas = []string{"suggest.json", "explain.json", "compare.json", "stats.json"}

// Every member of every answer, given each JSON type it is not, is rejected on
// "type". Integers reject a fraction; the members documented as null in some
// answers accept null and nothing else new; the debug echo is opaque.
func TestEndpointSchemasRejectEveryMemberGivenEachWrongType(t *testing.T) {
	covered, attacks := edgeTypeSweep(t)
	if attacks < 400 {
		t.Errorf("only %d wrong-type attacks ran", attacks)
	}
	// The sweep reached every type keyword that guards a member.
	for loc := range edgeCensus(t, edgeSchemas) {
		if strings.HasSuffix(loc, "/type") && !covered[loc] {
			t.Logf("type keyword not reached by the sweep: %s", loc)
		}
	}
}

// edgeTypeSweep gives every member of one valid answer of each endpoint every
// JSON type it is not, requires a "type" rejection each time, and returns the
// "type" keyword locations that rejected, with the number of attacks.
func edgeTypeSweep(t *testing.T) (map[string]bool, int) {
	t.Helper()
	schemas := loadSchemas(t)
	docs := edgeDocs(t)

	// Members whose number may carry a fraction. Every other number is an integer.
	fractional := map[string]bool{
		"/score": true, "/branches/*/score": true, "/branches/*/components/*/value": true,
		"/a/boosts/*": true, "/b/boosts/*": true, "/spearman_rho_union": true,
		"/a/results/*/score": true, "/b/results/*/score": true,
	}
	nullable := map[string]bool{"/spearman_rho_union": true, "/deltas/*/rank_a": true, "/deltas/*/rank_b": true, "/deltas/*/delta": true}
	candidates := []struct {
		kind string
		v    any
	}{{"null", nil}, {"string", "x"}, {"number", 1.5}, {"boolean", true}, {"array", []any{}}, {"object", map[string]any{}}}

	kindOf := func(v any) string {
		switch v.(type) {
		case nil:
			return "null"
		case string:
			return "string"
		case float64:
			return "number"
		case bool:
			return "boolean"
		case []any:
			return "array"
		}
		return "object"
	}
	normalize := func(segs []string) string {
		out := make([]string, len(segs))
		afterBoosts := false
		for i, s := range segs {
			if _, err := strconv.Atoi(s); err == nil || afterBoosts {
				s = "*"
			}
			afterBoosts = s == "boosts"
			out[i] = s
		}
		return "/" + strings.Join(out, "/")
	}
	var walk func(v any, segs []string, visit func(segs []string, v any))
	walk = func(v any, segs []string, visit func([]string, any)) {
		visit(segs, v)
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				walk(c, append(slices.Clone(segs), k), visit)
			}
		case []any:
			for i, c := range x {
				walk(c, append(slices.Clone(segs), strconv.Itoa(i)), visit)
			}
		}
	}

	covered := map[string]bool{}
	attacks := 0
	for _, name := range edgeSchemas {
		doc := docs[name]
		walk(doc, nil, func(segs []string, orig any) {
			if len(segs) > 0 && segs[0] == "dsl" {
				return // the echo of Elasticsearch's request: any value is allowed
			}
			shp := normalize(segs)
			if len(segs) == 0 {
				shp = ""
			}
			allowed := map[string]bool{kindOf(orig): true}
			if fractional[shp] {
				allowed["number"] = true
			}
			if nullable[shp] {
				allowed["null"] = true
			}
			for _, c := range candidates {
				if allowed[c.kind] {
					continue
				}
				attacks++
				mutated := edgeSet(t, doc, "/"+strings.Join(segs, "/"), c.v)
				if len(segs) == 0 {
					mutated = c.v
				}
				bad := mustViolations(t, schemas, name, mutated)
				if !hasKeyword(bad, "type") {
					t.Errorf("%s at %q (%s) accepted a %s: %v", name, "/"+strings.Join(segs, "/"), shp, c.kind, bad)
				}
				for _, v := range bad {
					if strings.HasSuffix(v, "/type") {
						covered[v] = true
					}
				}
			}
		})
	}
	return covered, attacks
}

type edgeVector struct {
	name    string
	schema  string
	ptr     string
	val     any
	keyword string // the keyword that must reject it; "" means the body must be valid
}

func edgeRepeat(s string, n int) string { return strings.Repeat(s, n) }

func edgeVectors() []edgeVector {
	const emoji = "\U0001F600"
	var v []edgeVector
	add := func(schema, ptr string, val any, keyword, name string) {
		v = append(v, edgeVector{name: schema + " " + ptr + ": " + name, schema: schema, ptr: ptr, val: val, keyword: keyword})
	}
	names := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "Name " + strconv.Itoa(i)
		}
		return out
	}

	// The three answers that carry an id, a request id and a took_ms share the
	// same boundaries.
	for _, s := range []string{"explain.json", "compare.json", "stats.json"} {
		add(s, "/request_id", edgeRepeat("a", 64), "", "64 characters")
		add(s, "/request_id", edgeRepeat("a", 65), "pattern", "65 characters")
		add(s, "/request_id", "", "pattern", "empty")
		add(s, "/request_id", "abc\n", "pattern", "a trailing newline")
		add(s, "/request_id", "a b", "pattern", "a space")
		add(s, "/request_id", "\u00e9", "pattern", "an accented letter")
		add(s, "/request_id", "a.b:c_d-e", "", "every allowed punctuation mark")
		add(s, "/took_ms", 0, "", "zero")
		add(s, "/took_ms", -1, "minimum", "negative")
		add(s, "/took_ms", 1.5, "type", "fractional")
		add(s, "/took_ms", float64(1<<53), "", "a very large count")
		add(s, "", edgeDeleted{}, "", "") // placeholder removed below
	}
	v = slices.DeleteFunc(v, func(e edgeVector) bool { return e.ptr == "" })
	for _, s := range []string{"explain.json", "compare.json", "stats.json"} {
		add(s, "/request_id", edgeDeleted{}, "required", "removed")
		add(s, "/took_ms", edgeDeleted{}, "required", "removed")
		add(s, "/zz", 1, "additionalProperties", "an unknown member")
	}
	add("suggest.json", "/zz", 1, "additionalProperties", "an unknown member")
	add("suggest.json", "/suggestions", edgeDeleted{}, "required", "removed")

	// suggest
	add("suggest.json", "/suggestions", names(8), "", "eight names")
	add("suggest.json", "/suggestions", names(9), "maxItems", "nine names")
	add("suggest.json", "/suggestions", []any{}, "", "none")
	add("suggest.json", "/suggestions", []any{"Pikachu", "Pikachu"}, "uniqueItems", "the same name twice")
	add("suggest.json", "/suggestions", []any{"Pikachu", "pikachu"}, "", "names that differ in case are distinct")
	add("suggest.json", "/suggestions", []any{"Pok\u00e9mon", "Pokemon"}, "", "names that differ in an accent are distinct")
	add("suggest.json", "/suggestions", []any{""}, "minLength", "an empty name")
	add("suggest.json", "/suggestions", []any{nil}, "type", "a null name")
	add("suggest.json", "/suggestions", []any{emoji}, "", "a four-byte name")
	add("suggest.json", "/suggestions", nil, "type", "null in place of the list")
	add("suggest.json", "/suggestions", map[string]any{}, "type", "an object in place of the list")
	add("suggest.json", "/suggestions", "Pikachu", "type", "a string in place of the list")

	// explain
	add("explain.json", "/q", edgeRepeat("a", 256), "", "256 characters")
	add("explain.json", "/q", edgeRepeat("a", 257), "maxLength", "257 characters")
	add("explain.json", "/q", edgeRepeat(emoji, 256), "", "256 four-byte characters")
	add("explain.json", "/q", edgeRepeat(emoji, 257), "maxLength", "257 four-byte characters")
	add("explain.json", "/q", "", "minLength", "empty")
	add("explain.json", "/id", edgeRepeat("a", 128), "", "128 characters")
	add("explain.json", "/id", edgeRepeat("a", 129), "pattern", "129 characters")
	add("explain.json", "/id", "ex10-?", "", "a question mark")
	add("explain.json", "/id", "ex10-!", "", "an exclamation mark")
	add("explain.json", "/id", "base1-4\n", "pattern", "a trailing newline")
	add("explain.json", "/id", "-x", "pattern", "a leading hyphen")
	add("explain.json", "/id", "a b", "pattern", "a space")
	add("explain.json", "/id", "a/b", "pattern", "a slash")
	add("explain.json", "/id", "\u00e9", "pattern", "an accented letter")
	add("explain.json", "/id", "", "pattern", "empty")
	add("explain.json", "/score", 0, "", "zero")
	add("explain.json", "/score", -0.0001, "minimum", "just below zero")
	add("explain.json", "/score", 1e300, "", "a huge score")
	add("explain.json", "/branches", []any{}, "minItems", "no branches")
	add("explain.json", "/branches/0/name", "", "minLength", "an empty branch name")
	add("explain.json", "/branches/0/score", -0.001, "minimum", "a negative branch score")
	add("explain.json", "/branches/0/score", 0, "", "a zero branch score")
	add("explain.json", "/branches/0/description", "", "minLength", "an empty description")
	add("explain.json", "/branches/0/components", []any{}, "minItems", "an empty component list")
	add("explain.json", "/branches/0/components/0/value", -3.5, "", "a negative component value (the engine's own)")
	add("explain.json", "/branches/0/components/0/description", "", "", "an empty component description")
	add("explain.json", "/branches/0/components/0/value", edgeDeleted{}, "required", "a component without its value")
	add("explain.json", "/branches/0/components/0/description", edgeDeleted{}, "required", "a component without its description")
	add("explain.json", "/branches/0/matched", edgeDeleted{}, "required", "a branch without matched")
	add("explain.json", "/found", edgeDeleted{}, "required", "no found")
	add("explain.json", "/branches/0/zz", 1, "additionalProperties", "an unknown branch member")
	add("explain.json", "/branches/0/components/0/zz", 1, "additionalProperties", "an unknown component member")

	// compare
	add("compare.json", "/q", edgeRepeat("a", 256), "", "256 characters")
	add("compare.json", "/q", edgeRepeat("a", 257), "maxLength", "257 characters")
	add("compare.json", "/q", edgeRepeat(emoji, 256), "", "256 four-byte characters")
	add("compare.json", "/q", edgeRepeat(emoji, 257), "maxLength", "257 four-byte characters")
	add("compare.json", "/q", "", "minLength", "empty")
	add("compare.json", "/spearman_rho_union", 1, "", "exactly 1")
	add("compare.json", "/spearman_rho_union", -1, "", "exactly -1")
	add("compare.json", "/spearman_rho_union", 0, "", "exactly 0")
	add("compare.json", "/spearman_rho_union", nil, "", "null")
	add("compare.json", "/spearman_rho_union", 1.0000001, "maximum", "just above 1")
	add("compare.json", "/spearman_rho_union", -1.0000001, "minimum", "just below -1")
	add("compare.json", "/window", 1, "", "one")
	add("compare.json", "/window", 0, "minimum", "zero")
	add("compare.json", "/missing_rank", 11, "", "window + 1")
	add("compare.json", "/missing_rank", 0, "minimum", "zero")
	add("compare.json", "/deltas", []any{}, "", "no deltas")
	for _, side := range []string{"/a", "/b"} {
		add("compare.json", side+"/profile", "served", "", "served")
		add("compare.json", side+"/profile", "runner-up", "", "runner-up")
		add("compare.json", side+"/profile", "Served", "enum", "wrong case")
		add("compare.json", side+"/profile", "", "enum", "empty")
		add("compare.json", side+"/boosts", map[string]any{}, "", "no boosts")
		add("compare.json", side+"/boosts", map[string]any{"zz": 0}, "", "a zero boost under a new branch name")
		add("compare.json", side+"/boosts", map[string]any{"zz": -0.0001}, "minimum", "a negative boost")
		add("compare.json", side+"/boosts", map[string]any{"zz": nil}, "type", "a null boost")
		add("compare.json", side+"/boosts", map[string]any{"zz": map[string]any{"y": 1}}, "type", "a nested object boost")
		add("compare.json", side+"/boosts", map[string]any{"zz": "1"}, "type", "a string boost")
		add("compare.json", side+"/boosts", map[string]any{"zz": true}, "type", "a boolean boost")
		add("compare.json", side+"/boosts", map[string]any{"zz": []any{1}}, "type", "an array boost")
		add("compare.json", side+"/boosts", nil, "type", "null boosts")
		add("compare.json", side+"/total", 0, "", "zero")
		add("compare.json", side+"/total", -1, "minimum", "negative")
		add("compare.json", side+"/took_ms", -1, "minimum", "negative")
		add("compare.json", side+"/results", []any{}, "", "an empty window")
		add("compare.json", side+"/results", edgeDeleted{}, "required", "no results member")
		add("compare.json", side+"/total", edgeDeleted{}, "required", "no total member")
		add("compare.json", side+"/took_ms", edgeDeleted{}, "required", "no took_ms member")
		add("compare.json", side+"/profile", edgeDeleted{}, "required", "no profile member")
		add("compare.json", side+"/zz", 1, "additionalProperties", "an unknown member of a side")
		add("compare.json", side+"/results/0/rank", 0, "minimum", "rank zero")
		add("compare.json", side+"/results/0/rank", 1, "", "rank one")
		add("compare.json", side+"/results/0/score", -0.001, "minimum", "a negative score")
		add("compare.json", side+"/results/0/score", 0, "", "a zero score")
		add("compare.json", side+"/results/0/id", "a b", "pattern", "an id with a space")
		add("compare.json", side+"/results/0/zz", 1, "additionalProperties", "an unknown member of a result")
		add("compare.json", side+"/results/0/id", edgeDeleted{}, "required", "a result without an id")
	}
	add("compare.json", "/deltas/0/status", "UP", "enum", "a status in capitals")
	add("compare.json", "/deltas/0/status", "", "enum", "an empty status")
	add("compare.json", "/deltas/0/status", "same", "", "same")
	add("compare.json", "/deltas/0/rank_a", 0, "minimum", "rank_a zero")
	add("compare.json", "/deltas/0/rank_a", -1, "minimum", "rank_a negative")
	add("compare.json", "/deltas/0/rank_a", nil, "", "rank_a null")
	add("compare.json", "/deltas/0/rank_b", 0, "minimum", "rank_b zero")
	add("compare.json", "/deltas/0/rank_b", nil, "", "rank_b null")
	add("compare.json", "/deltas/0/delta", -9, "", "a negative delta")
	add("compare.json", "/deltas/0/delta", 0, "", "a zero delta")
	add("compare.json", "/deltas/0/delta", nil, "", "a null delta")
	add("compare.json", "/deltas/0/delta", edgeDeleted{}, "required", "no delta member")
	add("compare.json", "/deltas/0/rank_a", edgeDeleted{}, "required", "no rank_a member")
	add("compare.json", "/deltas/0/zz", 1, "additionalProperties", "an unknown member of a delta")
	add("compare.json", "/deltas/0/id", "a b", "pattern", "a delta id with a space")

	// stats
	add("stats.json", "/total", 0, "", "zero")
	add("stats.json", "/total", 1<<31, "", "past 32 bits")
	add("stats.json", "/total", -1, "minimum", "negative")
	add("stats.json", "/max_hp", 0, "", "zero")
	add("stats.json", "/max_hp", -1, "minimum", "negative")
	for _, year := range []struct {
		val any
		kw  string
		why string
	}{
		{"0000", "", "0000"}, {"9999", "", "9999"}, {"199", "pattern", "three digits"}, {"12345", "pattern", "five digits"},
		{"1999 ", "pattern", "a trailing space"}, {"1999\n", "pattern", "a trailing newline"}, {"\u0661\u0669\u0669\u0669", "pattern", "Arabic-Indic digits"},
		{"", "pattern", "empty"}, {1999, "type", "a number"}, {nil, "type", "null"}, {"1999-01-01", "pattern", "a whole date"},
	} {
		add("stats.json", "/per_year/0/year", year.val, year.kw, year.why)
	}
	add("stats.json", "/per_year/0/count", -1, "minimum", "negative")
	add("stats.json", "/per_year/0/count", 0, "", "zero")
	add("stats.json", "/hp/0/from", 0, "", "zero")
	add("stats.json", "/hp/0/from", -30, "minimum", "negative")
	add("stats.json", "/hp/0/count", -1, "minimum", "negative")
	add("stats.json", "/hp/0/label", "0-29", "additionalProperties", "a label")
	add("stats.json", "/per_year/0/label", "x", "additionalProperties", "a label")
	add("stats.json", "/per_year/0/year", edgeDeleted{}, "required", "no year")
	add("stats.json", "/hp/0/from", edgeDeleted{}, "required", "no from")
	add("stats.json", "/hp/0/count", edgeDeleted{}, "required", "no hp count")
	add("stats.json", "/per_year/0/count", edgeDeleted{}, "required", "no year count")
	for _, facet := range []string{"types", "supertype", "rarity", "series"} {
		add("stats.json", "/"+facet+"/0/count", -1, "minimum", "negative")
		add("stats.json", "/"+facet+"/0/value", nil, "type", "a null value")
		add("stats.json", "/"+facet+"/0/value", edgeDeleted{}, "required", "no value")
		add("stats.json", "/"+facet+"/0/count", edgeDeleted{}, "required", "no count")
		add("stats.json", "/"+facet+"/0/zz", 1, "additionalProperties", "an unknown member")
		add("stats.json", "/"+facet, edgeDeleted{}, "required", "removed")
		add("stats.json", "/"+facet, nil, "type", "null in place of the list")
		add("stats.json", "/"+facet, map[string]any{}, "type", "an object in place of the list")
	}
	for _, key := range []string{"total", "max_hp", "per_year", "hp"} {
		add("stats.json", "/"+key, edgeDeleted{}, "required", "removed")
	}
	// The debug echo is Elasticsearch's request, outside the contract.
	add("stats.json", "/dsl", map[string]any{"size": 0}, "", "an object echo")
	add("stats.json", "/dsl", edgeDeleted{}, "", "no echo (without debug)")
	return v
}

// Each boundary of each schema keyword is taken to its edge: the last accepted
// value passes and the first value past it is rejected on the named keyword. A
// final census proves every keyword the four schemas declare is attacked.
func TestEndpointSchemaKeywordsHoldAtTheirBoundaries(t *testing.T) {
	schemas := loadSchemas(t)
	docs := edgeDocs(t)
	covered, _ := edgeTypeSweep(t)
	for _, vec := range edgeVectors() {
		mutated := edgeSet(t, docs[vec.schema], vec.ptr, vec.val)
		bad := mustViolations(t, schemas, vec.schema, mutated)
		if vec.keyword == "" {
			if len(bad) > 0 {
				t.Errorf("%s: want it valid, got %v", vec.name, bad)
			}
			continue
		}
		if !hasKeyword(bad, vec.keyword) {
			t.Errorf("%s: want a %q violation, got %v", vec.name, vec.keyword, bad)
			continue
		}
		for _, v := range bad {
			if strings.HasSuffix(v, "/"+vec.keyword) {
				covered[v] = true
			}
		}
	}
	for loc := range edgeCensus(t, edgeSchemas) {
		if !covered[loc] {
			t.Errorf("no vector rejects on %s: that keyword could be loosened unnoticed", loc)
		}
	}
}

// The schemas read a body the way the server writes it and the way a client
// that numbers things differently might: a count of 3.0 is an integer, a count
// of 3.5 is not, and an integer-valued number written with an exponent is.
func TestEndpointSchemasTreatIntegralFloatsAsIntegers(t *testing.T) {
	schemas := loadSchemas(t)
	docs := edgeDocs(t)
	raw := func(schema, ptr, literal string) []string {
		doc := edgeSet(t, docs[schema], ptr, json.Number("0"))
		enc, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		// Swap the placeholder 0 for the literal by re-encoding the one member.
		out := strings.Replace(string(enc), `"`+lastSegment(ptr)+`":0`, `"`+lastSegment(ptr)+`":`+literal, 1)
		if out == string(enc) {
			t.Fatalf("could not place %s at %s", literal, ptr)
		}
		bad, err := schemas.violations(schema, []byte(out))
		if err != nil {
			t.Fatal(err)
		}
		return bad
	}
	for _, tc := range []struct {
		literal string
		valid   bool
	}{{"3.0", true}, {"3e0", true}, {"30e-1", true}, {"3.5", false}, {"3.0000001", false}} {
		bad := raw("stats.json", "/total", tc.literal)
		if tc.valid && len(bad) > 0 {
			t.Errorf("total %s: %v", tc.literal, bad)
		}
		if !tc.valid && !hasKeyword(bad, "type") {
			t.Errorf("total %s was accepted: %v", tc.literal, bad)
		}
	}
}

func lastSegment(ptr string) string {
	segs := pointerSegments(ptr)
	return segs[len(segs)-1]
}

// A body that is not one JSON object is rejected by every schema, whatever the
// bytes: truncated, empty, a bare scalar, a list, trailing data.
func TestEndpointSchemasRejectBodiesThatAreNotOneObject(t *testing.T) {
	schemas := loadSchemas(t)
	docs := edgeDocs(t)
	for _, name := range edgeSchemas {
		valid, err := json.Marshal(docs[name])
		if err != nil {
			t.Fatal(err)
		}
		for what, body := range map[string][]byte{
			"empty":         nil,
			"whitespace":    []byte(" \n"),
			"truncated":     valid[:len(valid)/2],
			"null":          []byte("null"),
			"a list":        []byte("[" + string(valid) + "]"),
			"a string":      []byte(`"x"`),
			"a number":      []byte("1"),
			"the empty obj": []byte("{}"),
			"trailing data": append(slices.Clone(valid), []byte(` {}`)...),
		} {
			bad, err := schemas.violations(name, body)
			if err == nil && len(bad) == 0 {
				t.Errorf("%s accepted %s: %s", name, what, body)
			}
		}
		if bad, err := schemas.violations(name, append(slices.Clone(valid), '\n')); err != nil || len(bad) > 0 {
			t.Errorf("%s rejected its own body with a trailing newline: %v %v", name, bad, err)
		}
	}
}

// Every entry of the allowlists names something that exists, and is as narrow
// as its reason says, so a stale or over-broad entry fails here instead of
// silently excusing an open object.
func TestSchemaAllowlistsAreNeitherStaleNorBroader(t *testing.T) {
	resolve := func(entry string) (string, map[string]any, bool) {
		file, ptr, _ := strings.Cut(entry, "#")
		var node any = readSchema(t, file)
		for _, seg := range pointerSegments(ptr) {
			m, ok := node.(map[string]any)
			if !ok {
				return file, nil, false
			}
			if node, ok = m[seg]; !ok {
				return file, nil, false
			}
		}
		m, ok := node.(map[string]any)
		return file, m, ok
	}
	for entry := range dataKeyedMaps {
		_, node, ok := resolve(entry)
		if !ok {
			t.Errorf("dataKeyedMaps names %s, which is not in the schemas", entry)
			continue
		}
		if node["type"] != "object" {
			t.Errorf("%s is listed as a map but is not typed object: %v", entry, node["type"])
		}
		sub, isSchema := node["additionalProperties"].(map[string]any)
		if !isSchema || sub["type"] == nil {
			t.Errorf("%s is listed as a map but its values are not typed: %v", entry, node["additionalProperties"])
		}
		if _, has := node["properties"]; has {
			t.Errorf("%s is listed as a map but also names members", entry)
		}
	}
	for entry := range opaque {
		_, node, ok := resolve(entry)
		if !ok {
			t.Errorf("opaque names %s, which is not in the schemas", entry)
			continue
		}
		for _, kw := range []string{"type", "$ref", "enum", "const", "oneOf", "anyOf", "allOf", "properties", "required", "additionalProperties"} {
			if _, has := node[kw]; has {
				t.Errorf("opaque names %s, but it carries %q, so it is not opaque", entry, kw)
			}
		}
		if node["description"] == nil || !strings.Contains(fmt.Sprint(node["description"]), "Elasticsearch") {
			t.Errorf("%s is opaque without saying why", entry)
		}
	}

	// Each shape excused from the unknown-member check really takes any member
	// name (else the excuse is stale) and still types its values.
	schemas := loadSchemas(t)
	docs := edgeDocs(t)
	for shp := range dataKeyedShapes {
		file, ptr, _ := strings.Cut(shp, "#")
		if _, err := edgeStepAll(docs[file], ptr); err != nil {
			t.Errorf("dataKeyedShapes names %s, which a real answer does not hold: %v", shp, err)
			continue
		}
		open := edgeSet(t, docs[file], ptr+"/zz_branch", 1.5)
		if bad := mustViolations(t, schemas, file, open); len(bad) > 0 {
			t.Errorf("%s is excused as data-keyed but rejects a new numeric member: %v", shp, bad)
		}
		typed := edgeSet(t, docs[file], ptr+"/zz_branch", "1.5")
		if bad := mustViolations(t, schemas, file, typed); len(bad) == 0 {
			t.Errorf("%s accepts a string where a boost belongs", shp)
		}
	}

	// The route tables name registered routes only, and no route twice.
	patterns := muxPatterns(t)
	seen := map[string]string{}
	for table, routes := range map[string]map[string]string{"v1Routes": v1Routes, "legacyRoutes": legacyRoutes, "errorOnlyRoutes": errorOnlyRoutes} {
		for route, schema := range routes {
			if !slices.Contains(patterns, route) {
				t.Errorf("%s names %q, which the server does not register", table, route)
			}
			if other, dup := seen[route]; dup {
				t.Errorf("%q is in both %s and %s", route, other, table)
			}
			seen[route] = table
			if !slices.Contains(schemaFiles(t), schema) {
				t.Errorf("%s: %q maps to %s, which does not exist", table, route, schema)
			}
		}
	}
	for route, schema := range errorOnlyRoutes {
		if schema != "error.json" {
			t.Errorf("%q answers only errors but maps to %s", route, schema)
		}
	}
}

func edgeStepAll(doc any, ptr string) (any, error) {
	cur := doc
	for _, seg := range pointerSegments(ptr) {
		switch x := cur.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return nil, fmt.Errorf("no member %q", seg)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i >= len(x) {
				return nil, fmt.Errorf("no element %q", seg)
			}
			cur = x[i]
		default:
			return nil, fmt.Errorf("cannot step into %T at %q", cur, seg)
		}
	}
	return cur, nil
}

// The route guard reports exactly the undescribed routes under /api/, in the
// order given, for route tables the real server does not have today.
func TestRouteGuardReportsUndescribedAPIRoutes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		want     []string
	}{
		{"an empty table", nil, nil},
		{"the mapped routes", []string{"GET /api/search", "GET /api/suggest", "GET /api/explain", "GET /api/compare", "GET /api/stats", "GET /api/meta", "GET /api/"}, nil},
		{"every v1 route", []string{"GET /api/v1/cards", "GET /api/v1/cards/{id}", "GET /api/v1/cards/{$}", "GET /api/v1/sets"}, nil},
		{"a new GET route", []string{"GET /api/widgets"}, []string{"GET /api/widgets"}},
		{"a new method on a mapped path", []string{"POST /api/search", "DELETE /api/stats", "HEAD /api/meta"}, []string{"POST /api/search", "DELETE /api/stats", "HEAD /api/meta"}},
		{"a pattern with no method", []string{"/api/search"}, []string{"/api/search"}},
		{"a new v1 route", []string{"GET /api/v1/decks"}, []string{"GET /api/v1/decks"}},
		{"a v1 route with a trailing wildcard", []string{"GET /api/v1/cards/{id...}"}, []string{"GET /api/v1/cards/{id...}"}},
		{"a wildcard route under /api/", []string{"GET /api/{thing}"}, []string{"GET /api/{thing}"}},
		{"a host-qualified route", []string{"GET example.com/api/suggest"}, []string{"GET example.com/api/suggest"}},
		{"routes outside /api/", []string{"GET /", "GET /livez", "GET /healthz", "GET /debug/vars", "GET /api", "GET /apix/y", "GET /static/app.js"}, nil},
		{"an order that is kept", []string{"GET /api/b", "GET /healthz", "GET /api/a"}, []string{"GET /api/b", "GET /api/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := routesWithoutSchema(tc.patterns)
			if !slices.Equal(got, tc.want) {
				t.Errorf("routesWithoutSchema(%v) = %v, want %v", tc.patterns, got, tc.want)
			}
		})
	}
	// The guard must not mutate its input.
	in := []string{"GET /api/a", "GET /healthz"}
	before := slices.Clone(in)
	routesWithoutSchema(in)
	if !slices.Equal(in, before) {
		t.Errorf("routesWithoutSchema changed its argument: %v", in)
	}
}

// A schema accepts a document exactly when an independent reading of its words
// does, for the one schema simple enough to restate: a list of at most eight
// distinct, non-empty names under one member. The seeds are the boundaries.
func FuzzSuggestSchemaAgreesWithItsDescription(f *testing.F) {
	for _, seed := range []string{
		`{"suggestions":[]}`, `{"suggestions":["A","B"]}`, `{"suggestions":["A","A"]}`, `{"suggestions":[""]}`,
		`{"suggestions":["1","2","3","4","5","6","7","8"]}`, `{"suggestions":["1","2","3","4","5","6","7","8","9"]}`,
		`{"suggestions":[]} `, `{"suggestions":[],"request_id":"x"}`, `{"suggestions":null}`, `{"suggestions":[1]}`,
		`{"suggestions":["Pok\u00e9mon","Pokemon"]}`, `{"suggestions":["\ud83d\ude00"]}`, `{"suggestions":["a","A"]}`,
		`[]`, `null`, `{}`, `{"suggestions":["\u0000"]}`, `{"suggestions":["\ud800"]}`, `{"suggestions":["x"],"suggestions":[]}`,
	} {
		f.Add([]byte(seed))
	}
	schemas := loadSchemas(f)
	f.Fuzz(func(t *testing.T, body []byte) {
		bad, err := schemas.violations("suggest.json", body)
		var doc map[string]any
		decodeErr := json.Unmarshal(body, &doc)
		if !utf8.Valid(body) || decodeErr != nil || doc == nil {
			if err == nil && len(bad) == 0 && decodeErr != nil {
				t.Fatalf("accepted a body encoding/json rejects (%v): %q", decodeErr, body)
			}
			return
		}
		if err != nil {
			t.Fatalf("validator error on a decodable body: %v: %q", err, body)
		}
		want := len(doc) == 1
		list, isList := doc["suggestions"].([]any)
		want = want && isList && len(list) <= 8
		seen := map[string]bool{}
		for _, e := range list {
			s, ok := e.(string)
			if !ok || s == "" || seen[s] {
				want = false
				break
			}
			seen[s] = true
		}
		if got := len(bad) == 0; got != want {
			t.Fatalf("schema says valid=%v, description says %v: %q (violations %v)", got, want, body, bad)
		}
	})
}
