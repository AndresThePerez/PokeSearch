//go:build acceptance

package apischema

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// liveBody fetches path from the stack under test, requires one of the wanted
// statuses, validates the body against the schema for a 200 and against
// error.json for anything else, and returns the decoded 200 body (nil for an
// error answer).
func liveBody(t *testing.T, schemas *schemaSet, path, schema string, want ...int) map[string]any {
	t.Helper()
	got := fetch(t, liveBase(), path, nil)
	if !slices.Contains(want, got.status) {
		t.Errorf("GET %.120s: status %d, want one of %v: %.200s", path, got.status, want, got.body)
		return nil
	}
	name := schema
	if got.status != http.StatusOK {
		name = "error.json"
	}
	bad, err := schemas.violations(name, got.body)
	if err != nil {
		t.Fatalf("GET %.120s: %v", path, err)
	}
	if len(bad) > 0 {
		t.Errorf("GET %.120s: %d answer does not satisfy %s: %v\nbody: %.400s", path, got.status, name, bad, got.body)
		return nil
	}
	if got.status != http.StatusOK {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(got.body, &doc); err != nil {
		t.Fatalf("GET %.120s: %v", path, err)
	}
	return doc
}

// cardIDsAndNames collects real ids and names from a spread of searches, so the
// explain and compare sweeps ask about cards the index holds.
func cardIDsAndNames(t *testing.T, queries []string) (ids, names []string) {
	t.Helper()
	seenID, seenName := map[string]bool{}, map[string]bool{}
	for _, q := range queries {
		got := fetch(t, liveBase(), "/api/search?page_size=24&q="+url.QueryEscape(q), nil)
		var body struct {
			Results []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := json.Unmarshal(got.body, &body); err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		for _, r := range body.Results {
			if !seenID[r.ID] {
				seenID[r.ID] = true
				ids = append(ids, r.ID)
			}
			if !seenName[r.Name] {
				seenName[r.Name] = true
				names = append(names, r.Name)
			}
		}
	}
	return ids, names
}

var sweepSearches = []string{"pikachu", "charizard", "mew", "unown", "trainer", "energy", "pok\u00e9mon", "mr", "dark", "professor", "fire", "ex", "vmax", "gold", "arceus", "eevee", "bulbasaur", "squirtle", "gengar", "lugia", "rayquaza", "snorlax", "ditto", "machamp", "zubat", "alakazam", "blastoise", "jigglypuff", "psyduck", "togepi"}

// Suggest answers over seven hundred varied prefixes (every two-letter
// prefix, punctuation, accents, four-byte characters, a q at the cap) all
// satisfy the schema, repeat no name exactly, and never exceed eight.
func TestLiveSuggestAnswersSatisfyTheSchemaAcrossManyPrefixes(t *testing.T) {
	schemas := loadSchemas(t)
	var qs []string
	for a := 'a'; a <= 'z'; a++ {
		qs = append(qs, string(a))
		for b := 'a'; b <= 'z'; b++ {
			qs = append(qs, string(a)+string(b))
		}
	}
	qs = append(qs, "%20", "pika%20chu", "%C3%A9", "pok%C3%A9mon", "%F0%9F%98%80", "%FF", "%FF%FE%FD", "%01", "%C2%A0pika%C2%A0", "%E3%80%80", "'", "%22", "%00", "%5C", "*", "?", "+", "(", ")",
		"a%20b%20c%20d%20e%20f%20g", "mr.%20mime", "farfetch%27d", "%E2%99%82", "nidoran%E2%99%80", "%20pika%20", "PIKA", "pika-chu", "1", "0", "%25",
		strings.Repeat("%F0%9F%98%80", 256), strings.Repeat("a", 256), strings.Repeat("%C3%A9", 256), strings.Repeat("%FF", 256))
	nonEmpty := 0
	for _, q := range qs {
		doc := liveBody(t, schemas, "/api/suggest?q="+q, "suggest.json", http.StatusOK)
		if doc == nil {
			continue
		}
		list := doc["suggestions"].([]any)
		if len(list) > search.SuggestSize {
			t.Errorf("q=%.40s: %d suggestions", q, len(list))
		}
		if len(list) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty < 100 {
		t.Errorf("only %d of %d prefixes suggested anything, so the sweep barely reached a non-empty answer", nonEmpty, len(qs))
	}
}

// Explain answers over real cards and varied queries satisfy the schema, name
// the four relevance branches in the builder's order, and report as score the
// matched branches' sum rounded to three decimals.
func TestLiveExplainAnswersSatisfyTheSchemaAcrossCardsAndQueries(t *testing.T) {
	schemas := loadSchemas(t)
	ids, _ := cardIDsAndNames(t, sweepSearches[:12])
	ids = ids[:min(len(ids), 70)]
	ids = append(ids, "ex10-?", "ex10-!", "base1-4", "nope-1", "A", "0", strings.Repeat("a", 128), "cel25c-17_A")
	qs := []string{"charizard", "pikachu", "%C3%A9", "%FF", "%01", "%C2%A0x%E3%80%80", "x", "fire%20energy", "name%3Apikachu", "%F0%9F%98%80", "%20pika%20", "%22", "a%20b%20c%20d",
		strings.Repeat("%F0%9F%98%80", 256), strings.Repeat("a", 256)}
	requests, matchedSomething := 0, 0
	for i, id := range ids {
		for j := range 3 {
			q := qs[(i*3+j)%len(qs)]
			if i%7 == 0 {
				q = qs[(i+j)%len(qs)]
			}
			doc := liveBody(t, schemas, "/api/explain?id="+url.QueryEscape(id)+"&q="+q, "explain.json", http.StatusOK)
			requests++
			if doc == nil {
				continue
			}
			sum := 0.0
			var names []string
			for _, b := range doc["branches"].([]any) {
				br := b.(map[string]any)
				names = append(names, br["name"].(string))
				if br["matched"] == true {
					sum += br["score"].(float64)
				} else if br["score"].(float64) != 0 {
					t.Errorf("explain %s: an unmatched branch scores %v", id, br["score"])
				}
			}
			var want []string
			for _, b := range search.Branches("x") {
				want = append(want, b.Name)
			}
			if !slices.Equal(names, want) {
				t.Errorf("explain %s: branches %v, want %v", id, names, want)
			}
			if got := doc["score"].(float64); math.Abs(got-sum) > 0.0011 {
				t.Errorf("explain %s: score %v, matched branches sum to %v", id, got, sum)
			}
			if sum > 0 {
				matchedSomething++
			}
			if doc["found"] == false && doc["score"].(float64) != 0 {
				t.Errorf("explain %s: found:false with score %v", id, doc["score"])
			}
		}
	}
	if requests < 200 || matchedSomething < 20 {
		t.Errorf("%d requests and %d matched answers: too few to mean anything", requests, matchedSomething)
	}
}

// Compare answers over real card names, every profile pair, queries with no
// hit and queries at the cap satisfy the schema, and carry the invariants the
// schema's words promise but its keywords cannot say.
func TestLiveCompareAnswersSatisfyTheSchemaAndKeepTheirWords(t *testing.T) {
	schemas := loadSchemas(t)
	_, names := cardIDsAndNames(t, append(slices.Clone(sweepSearches), "a", "e", "i", "o", "u", "ch", "st", "gr", "ma", "ro"))
	profiles := []string{"served", "previous", "runner-up"}
	var paths []string
	for i, n := range names {
		a, b := profiles[i%3], profiles[(i/3)%3]
		paths = append(paths, "/api/compare?q="+url.QueryEscape(n)+"&a="+a+"&b="+b)
	}
	for _, a := range profiles {
		for _, b := range profiles {
			paths = append(paths, "/api/compare?q=pikachu&a="+a+"&b="+b)
		}
	}
	for _, q := range []string{"zzzzqqqqxxxx", "%F0%9F%98%80", "%C3%A9", "%FF%FE", "%01", "%C2%A0pikachu%C2%A0", strings.Repeat("%FF", 256), strings.Repeat("%F0%9F%98%80", 256), strings.Repeat("a", 256), "x", "%22", "%20pika%20", "a%20b%20c%20d%20e%20f%20g%20h", "*", "%00"} {
		paths = append(paths, "/api/compare?q="+q)
	}
	if len(paths) < 200 {
		t.Fatalf("only %d compare requests", len(paths))
	}
	sawNull, sawEntered, sawDropped := false, false, false
	for _, path := range paths {
		doc := liveBody(t, schemas, path, "compare.json", http.StatusOK)
		if doc == nil {
			continue
		}
		window := int(doc["window"].(float64))
		for _, side := range []string{"a", "b"} {
			s := doc[side].(map[string]any)
			results := s["results"].([]any)
			if len(results) > window {
				t.Errorf("%s: side %s holds %d results for a window of %d", path, side, len(results), window)
			}
			if int(s["total"].(float64)) < len(results) {
				t.Errorf("%s: side %s total %v below %d results", path, side, s["total"], len(results))
			}
			prev := math.Inf(1)
			for i, r := range results {
				e := r.(map[string]any)
				if int(e["rank"].(float64)) != i+1 {
					t.Errorf("%s: side %s rank %v at position %d", path, side, e["rank"], i+1)
				}
				if sc := e["score"].(float64); sc > prev {
					t.Errorf("%s: side %s scores rise at rank %d", path, side, i+1)
				} else {
					prev = sc
				}
			}
		}
		// Largest movement first, every card of only one window before every
		// card of both.
		lastMag, lastBoth := math.MaxInt, false
		for _, d := range doc["deltas"].([]any) {
			dl := d.(map[string]any)
			status := dl["status"].(string)
			mag := window
			if dl["delta"] != nil {
				mag = int(math.Abs(dl["delta"].(float64)))
			}
			both := dl["rank_a"] != nil && dl["rank_b"] != nil
			if both && !lastBoth {
				lastBoth, lastMag = true, math.MaxInt
			}
			if !both && lastBoth {
				t.Errorf("%s: a card of one window comes after a card of both", path)
			}
			if mag > lastMag {
				t.Errorf("%s: deltas are not largest movement first (%d after %d)", path, mag, lastMag)
			}
			lastMag = mag
			switch status {
			case "entered":
				sawEntered = true
			case "dropped":
				sawDropped = true
			}
		}
		rho, has := doc["spearman_rho_union"]
		if !has {
			t.Errorf("%s: no spearman_rho_union member", path)
		}
		if rho == nil {
			sawNull = true
		}
		if q := doc["q"].(string); q == "" || len([]rune(q)) > 256 {
			t.Errorf("%s: echoed q has %d characters", path, len([]rune(q)))
		}
		// The same profile twice moves nothing.
		if strings.Contains(path, "&a=served&b=served") || strings.Contains(path, "&a=previous&b=previous") || strings.Contains(path, "&a=runner-up&b=runner-up") {
			for _, d := range doc["deltas"].([]any) {
				if d.(map[string]any)["status"] != "same" {
					t.Errorf("%s: identical profiles gave %v", path, d)
				}
			}
			if rho != nil && rho != float64(1) {
				t.Errorf("%s: identical profiles gave a correlation of %v", path, rho)
			}
		}
	}
	if !sawNull {
		t.Error("no compare answer had a null correlation (zero hits and identical profiles with one card should)")
	}
	t.Logf("entered seen: %v, dropped seen: %v", sawEntered, sawDropped)
}

// Stats answers every parameter the search endpoint reads without changing,
// and its figures agree with each other.
func TestLiveStatsAnswersSatisfyTheSchemaUnderEveryParameterAndAddUp(t *testing.T) {
	schemas := loadSchemas(t)
	params := []string{"", "debug=1", "debug=true", "debug=0", "debug=false", "debug=yes", "debug=", "debug=1&debug=0", "q=pikachu", "q=%C3%A9", "page=3",
		"page=-1", "page=zz", "page_size=7", "page_size=0", "page_size=101", "sort=bogus", "order=up", "types=Fire", "types=Fire,Water", "types=zz", "supertype=Energy",
		"rarity=Rare%20Holo", "series=Base", "set=base1", "hp=100", "hp=99999", "id=base1-4", "q=" + strings.Repeat("a", 300), "unknown=1", "%zz", "&&&&", "debug=1&q=x"}
	var base map[string]any
	requests := 0
	for range 8 {
		for _, p := range params {
			path := "/api/stats"
			if p != "" {
				path += "?" + p
			}
			doc := liveBody(t, schemas, path, "stats.json", http.StatusOK)
			requests++
			if doc == nil {
				continue
			}
			if _, has := doc["dsl"]; has != (strings.Contains(p, "debug=1") || strings.Contains(p, "debug=true")) {
				t.Errorf("%s: dsl present = %v", path, has)
			}
			delete(doc, "dsl")
			delete(doc, "request_id")
			if base == nil {
				base = doc
				continue
			}
			if a, b := mustJSONString(t, base), mustJSONString(t, doc); a != b {
				t.Errorf("%s: figures differ from the first answer:\n%.300s\n%.300s", path, a, b)
			}
		}
	}
	if requests < 200 {
		t.Fatalf("%d stats requests", requests)
	}
	if base == nil {
		t.Fatal("no stats answer")
	}
	total := int(base["total"].(float64))
	for _, key := range []string{"supertype"} {
		sum := 0
		for _, b := range base[key].([]any) {
			sum += int(b.(map[string]any)["count"].(float64))
		}
		if sum != total {
			t.Errorf("%s buckets add up to %d, total is %d", key, sum, total)
		}
	}
	for _, key := range []string{"types", "supertype", "rarity", "series"} {
		prev := math.MaxInt
		for _, b := range base[key].([]any) {
			bucket := b.(map[string]any)
			if bucket["value"] == "" {
				t.Errorf("%s has a bucket with an empty value", key)
			}
			c := int(bucket["count"].(float64))
			if c > prev {
				t.Errorf("%s is not most common first at %v", key, bucket)
			}
			if c > total {
				t.Errorf("%s bucket %v holds more cards than the archive", key, bucket)
			}
			prev = c
		}
	}
	years := base["per_year"].([]any)
	for i := 1; i < len(years); i++ {
		prev, err1 := strconv.Atoi(years[i-1].(map[string]any)["year"].(string))
		cur, err2 := strconv.Atoi(years[i].(map[string]any)["year"].(string))
		if err1 != nil || err2 != nil {
			t.Fatalf("per_year holds a year that is not a number: %v %v", err1, err2)
		}
		if cur != prev+1 {
			t.Errorf("per_year jumps from %d to %d", prev, cur)
		}
	}
	hp := base["hp"].([]any)
	maxHP := int(base["max_hp"].(float64))
	for i, b := range hp {
		if from := int(b.(map[string]any)["from"].(float64)); from != 30*i {
			t.Errorf("hp band %d starts at %d, want %d", i, from, 30*i)
		}
	}
	if last := int(hp[len(hp)-1].(map[string]any)["from"].(float64)); maxHP < last || maxHP >= last+30 {
		t.Errorf("max_hp %d is outside the last band %d", maxHP, last)
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A caller's own trace id is echoed into the body only when it fits the
// request_id pattern: whatever header a client sends, the three answers that
// carry request_id still satisfy their schema and the body names the same id as
// the X-Request-Id header.
func TestLiveRequestIDFromAClientHeaderStillSatisfiesTheSchema(t *testing.T) {
	schemas := loadSchemas(t)
	headers := []struct{ name, value string }{
		{"X-Request-Id", strings.Repeat("a", 64)},
		{"X-Request-Id", strings.Repeat("a", 65)},
		{"X-Request-Id", "has space"},
		{"X-Request-Id", "caf\u00e9"},
		{"X-Request-Id", "a/b;c"},
		{"X-Request-Id", "8a1b2c3d4e5f6a7b-AMS"},
		{"X-Request-Id", "a.b:c_d-e"},
		{"X-Request-Id", "-"},
		{"X-Request-Id", "\t"},
		{"Cf-Ray", "8a1b2c3d4e5f6a7b-AMS"},
		{"Cf-Ray", strings.Repeat("9", 65)},
		{"Cf-Ray", "bad id"},
	}
	paths := map[string]string{
		"/api/explain?id=base1-4&q=charizard": "explain.json",
		"/api/compare?q=charizard":            "compare.json",
		"/api/stats":                          "stats.json",
	}
	for path, schema := range paths {
		for _, h := range headers {
			got := fetch(t, liveBase(), path, http.Header{h.name: {h.value}})
			if got.status != http.StatusOK {
				t.Fatalf("GET %s with %s %q: status %d", path, h.name, h.value, got.status)
			}
			if bad, err := schemas.violations(schema, got.body); err != nil || len(bad) > 0 {
				t.Errorf("GET %s with %s %q does not satisfy %s: %v %v\n%.300s", path, h.name, h.value, schema, bad, err, got.body)
			}
			var doc struct {
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(got.body, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.RequestID != got.header.Get("X-Request-Id") {
				t.Errorf("GET %s with %s %q: body request_id %q, header %q", path, h.name, h.value, doc.RequestID, got.header.Get("X-Request-Id"))
			}
		}
	}
}
