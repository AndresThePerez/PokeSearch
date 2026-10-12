// Package apischema binds PokéSearch's published JSON Schemas
// (docs/api/schema) to what the handlers actually send. The schemas are the
// contract a second client (PokéPrices' Laravel side) vendors and validates
// its fixtures against, so a handler change that the schemas do not describe
// has to fail here first, not in the other repository.
//
// The untagged tests drive the real server over HTTP against a fake
// Elasticsearch; acceptance_test.go (build tag acceptance) points the same
// checks at a seeded stack.
package apischema

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/AndresThePerez/pokesearch/internal/search"
	"github.com/AndresThePerez/pokesearch/internal/server"
	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

const (
	schemaDir  = "../../docs/api/schema"
	invalidDir = "testdata/invalid"
	serverDir  = "../server"
	draft2020  = "https://json-schema.org/draft/2020-12/schema"
)

// ---------------------------------------------------------------------------
// The validator. schemaSet and violations are the only code that touches the
// JSON Schema library, so replacing it (for a hand-rolled validator, say)
// changes these two functions and nothing else; the negative vectors then
// prove the replacement still rejects what it must.

type schemaSet struct {
	byName map[string]*jsonschema.Schema
}

// loadSchemas compiles every *.json file in docs/api/schema. Relative $refs
// between the files resolve against each file's own location.
func loadSchemas(t testing.TB) *schemaSet {
	t.Helper()
	dir, err := filepath.Abs(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	set := &schemaSet{byName: map[string]*jsonschema.Schema{}}
	for _, name := range schemaFiles(t) {
		sch, err := c.Compile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
		set.byName[name] = sch
	}
	return set
}

// violations validates doc against the named schema. It returns one entry per
// failing assertion, "<file>#<schema pointer>/<keyword>" (for example
// "card.json#/properties/hp/type"); an empty result means doc is valid.
func (s *schemaSet) violations(name string, doc []byte) ([]string, error) {
	sch, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("no schema %q in %s", name, schemaDir)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, fmt.Errorf("document is not JSON: %w", err)
	}
	err = sch.Validate(inst)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil, err
	}
	var out []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		file, frag, _ := strings.Cut(e.SchemaURL, "#")
		loc := path.Base(file) + "#" + frag
		if kw := e.ErrorKind.KeywordPath(); len(kw) > 0 {
			loc += "/" + strings.Join(kw, "/")
		}
		out = append(out, loc)
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, err.Error())
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Shared HTTP helpers, also used by the acceptance-tagged test.

type answer struct {
	status int
	header http.Header
	body   []byte
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// fetch sends GET base+path. path is sent as written, so "ex10-%3F" reaches
// the server percent-encoded.
func fetch(t testing.TB, base, path string, header http.Header) answer {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: reading body: %v", path, err)
	}
	return answer{status: res.StatusCode, header: res.Header, body: body}
}

// ---------------------------------------------------------------------------
// A fake Elasticsearch: the real server, its middleware and handlers run
// unchanged; only the cluster's answers are canned.

type fakeCluster struct {
	cards     map[string]tcg.Card
	hits      []string // ids a card _search answers with, in order
	unstamped bool     // the mapping carries no seed provenance
	down      bool     // every call fails
}

func esAnswer(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"X-Elastic-Product": []string{"Elasticsearch"},
			"Content-Type":      []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *fakeCluster) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
	}
	if f.down {
		return esAnswer(500, `{"error":{"type":"cluster_block_exception"}}`), nil
	}
	switch r.URL.Path {
	case "/cards/_mapping":
		if f.unstamped {
			return esAnswer(200, `{"cards":{"mappings":{}}}`), nil
		}
		return esAnswer(200, `{"cards":{"mappings":{"_meta":{"seed_ref":"0000000000000000000000000000000000000000","seeded_at":"2026-01-01T00:00:00Z"}}}}`), nil
	case "/cards/_count":
		return esAnswer(200, fmt.Sprintf(`{"count":%d}`, len(f.cards))), nil
	case "/cards/_mget":
		return esAnswer(200, f.mget(body)), nil
	case "/cards/_search":
		return esAnswer(200, f.search(body)), nil
	}
	return nil, errors.New("unexpected ES call: " + r.URL.Path)
}

func (f *fakeCluster) mget(body []byte) string {
	var req struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(body, &req)
	docs := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if c, ok := f.cards[id]; ok {
			docs = append(docs, fmt.Sprintf(`{"_index":"cards","_id":%s,"_version":1,"found":true,"_source":%s}`, mustJSON(id), mustJSON(c)))
		} else {
			docs = append(docs, fmt.Sprintf(`{"_index":"cards","_id":%s,"found":false}`, mustJSON(id)))
		}
	}
	return `{"docs":[` + strings.Join(docs, ",") + `]}`
}

// sortedSets is one sample card per set id, in id order.
func (f *fakeCluster) sortedSets() []tcg.Card {
	bySet := map[string]tcg.Card{}
	for _, c := range f.cards {
		bySet[c.SetID] = c
	}
	ids := make([]string, 0, len(bySet))
	for id := range bySet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]tcg.Card, 0, len(ids))
	for _, id := range ids {
		out = append(out, bySet[id])
	}
	return out
}

func (f *fakeCluster) setCount(setID string) int {
	n := 0
	for _, c := range f.cards {
		if c.SetID == setID {
			n++
		}
	}
	return n
}

// search answers the four _search shapes the server sends: the v1 sets
// aggregation, the search facets' set catalog, the did-you-mean suggester and
// the card search itself.
func (f *fakeCluster) search(body []byte) string {
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	var v1Sets map[string]any
	_ = json.Unmarshal([]byte(mustJSON(search.BuildV1SetsQuery())), &v1Sets)

	switch {
	case reflect.DeepEqual(req, v1Sets):
		var buckets []string
		for _, c := range f.sortedSets() {
			src := mustJSON(map[string]any{"set_name": c.SetName, "set_series": c.SetSeries, "set_total": c.SetTotal, "release_date": c.ReleaseDate})
			buckets = append(buckets, fmt.Sprintf(`{"key":%s,"doc_count":%d,"identity":{"hits":{"hits":[{"_source":%s}]}}}`, mustJSON(c.SetID), f.setCount(c.SetID), src))
		}
		return `{"took":2,"hits":{"hits":[]},"aggregations":{"` + search.V1SetsAgg + `":{"buckets":[` + strings.Join(buckets, ",") + `]}}}`
	case bytes.Contains(body, []byte(`"set_catalog"`)):
		var buckets []string
		for _, c := range f.sortedSets() {
			src := mustJSON(map[string]any{"set_name": c.SetName, "release_date": c.ReleaseDate})
			buckets = append(buckets, fmt.Sprintf(`{"key":%s,"doc_count":%d,"identity":{"hits":{"hits":[{"_source":%s}]}}}`, mustJSON(c.SetID), f.setCount(c.SetID), src))
		}
		return `{"took":2,"hits":{"hits":[]},"aggregations":{"set_catalog":{"buckets":[` + strings.Join(buckets, ",") + `]}}}`
	case req["suggest"] != nil:
		var dym struct {
			Suggest struct {
				DYM struct {
					Text string `json:"text"`
				} `json:"dym"`
			} `json:"suggest"`
		}
		_ = json.Unmarshal(body, &dym)
		n := len([]rune(dym.Suggest.DYM.Text))
		return fmt.Sprintf(`{"took":1,"hits":{"hits":[]},"suggest":{"dym":[{"text":%s,"offset":0,"length":%d,"options":[{"text":"charizard","score":0.8,"freq":3}]}]}}`, mustJSON(dym.Suggest.DYM.Text), n)
	}

	_, highlighted := req["highlight"]
	branch := search.Branches("charizard")[0].Name
	hits := make([]string, 0, len(f.hits))
	for _, id := range f.hits {
		c := f.cards[id]
		hit := fmt.Sprintf(`{"_index":"cards","_id":%s,"_score":1.5,"_source":%s,"matched_queries":[%s]`, mustJSON(id), mustJSON(c), mustJSON(branch))
		if highlighted {
			hit += `,"highlight":{"name":["<mark>` + c.Name + `</mark>"]}`
		}
		hits = append(hits, hit+"}")
	}
	aggs := make([]string, 0, len(search.Facets))
	for _, facet := range search.Facets {
		var buckets []string
		if len(f.hits) > 0 {
			c := f.cards[f.hits[0]]
			key := map[string]string{"supertype": c.Supertype, "types": c.Types[0], "rarity": c.Rarity, "set_series": c.SetSeries, search.SetsFacet: c.SetID}[facet.Name]
			buckets = append(buckets, fmt.Sprintf(`{"key":%s,"doc_count":%d}`, mustJSON(key), len(f.hits)))
		}
		aggs = append(aggs, fmt.Sprintf(`%s:{"doc_count":%d,"items":{"buckets":[%s]}}`, mustJSON(facet.Name), len(f.hits), strings.Join(buckets, ",")))
	}
	return fmt.Sprintf(`{"took":4,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]},"aggregations":{%s}}`,
		len(f.hits), strings.Join(hits, ","), strings.Join(aggs, ","))
}

// corpusCards are the golden-set cards: Charizard carries every optional
// field a card document can have, and the two Unown ids are the ones a URL
// path mangles.
func corpusCards() map[string]tcg.Card {
	hp120, hp50 := 120, 50
	unown := func(id, symbol string) tcg.Card {
		return tcg.Card{
			ID: id, Name: "Unown", Supertype: "Pokémon", Subtypes: []string{"Basic"}, HP: &hp50,
			Types:   []string{"Psychic"},
			Attacks: []tcg.Attack{{Name: "Hidden Power", Cost: []string{"Psychic"}, ConvertedCost: 1, Damage: "10", DamageValue: 10}},
			Rarity:  "Rare", NationalPokedexNumbers: []int{201},
			Number: symbol, SetID: "ex10", SetName: "Unseen Forces", SetSeries: "EX", SetTotal: 115,
			ReleaseDate: "2005-08-22",
			ImageSmall:  "https://images.pokemontcg.io/ex10/" + symbol + ".png",
			ImageLarge:  "https://images.pokemontcg.io/ex10/" + symbol + "_hires.png",
		}
	}
	return map[string]tcg.Card{
		"base1-4": {
			ID: "base1-4", Name: "Charizard", Supertype: "Pokémon", Subtypes: []string{"Stage 2"}, HP: &hp120,
			Types: []string{"Fire"}, EvolvesFrom: "Charmeleon",
			Attacks:     []tcg.Attack{{Name: "Fire Spin", Cost: []string{"Fire", "Fire", "Fire", "Fire"}, ConvertedCost: 4, Damage: "100", DamageValue: 100, Text: "Discard 2 Energy cards attached to Charizard in order to use this attack."}},
			Abilities:   []tcg.Ability{{Name: "Energy Burn", Type: "Pokémon Power", Text: "As often as you like during your turn, you may turn all Energy attached to Charizard into Fire Energy for the rest of the turn."}},
			Weaknesses:  []tcg.Value{{Type: "Water", Value: "×2"}},
			Resistances: []tcg.Value{{Type: "Fighting", Value: "-30"}},
			RetreatCost: 3, Rarity: "Rare Holo", Artist: "Mitsuhiro Arita",
			FlavorText:             "Spits fire that is hot enough to melt boulders.",
			NationalPokedexNumbers: []int{6},
			Number:                 "4", SetID: "base1", SetName: "Base", SetSeries: "Base", SetTotal: 102,
			ReleaseDate: "1999-01-09",
			ImageSmall:  "https://images.pokemontcg.io/base1/4.png",
			ImageLarge:  "https://images.pokemontcg.io/base1/4_hires.png",
		},
		"ex10-?": unown("ex10-?", "?"),
		"ex10-!": unown("ex10-!", "!"),
	}
}

// throwawayKey is generated per run; no key literal exists in this package.
func throwawayKey(t testing.TB) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// startServer runs the real server (every middleware included) over HTTP.
func startServer(t *testing.T, es http.RoundTripper) string {
	t.Helper()
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://fake-es:9200"},
		Transport: es,
	})
	if err != nil {
		t.Fatal(err)
	}
	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>PokéSearch</title>")}}
	ts := httptest.NewServer(server.New(client, static, io.Discard, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// ---------------------------------------------------------------------------
// (a) Handler output, validated against its schema.

type liveCase struct {
	name    string
	path    string
	cluster func() *fakeCluster // nil: the golden-set cluster
	header  func(t *testing.T) http.Header
	status  int
	schema  string // the schema the body must satisfy
	// contains proves the case exercised the shape it is named for.
	contains string
	// revalidate repeats the request with the 200's ETag and expects a 304
	// with no body, which is a v1 answer no schema describes.
	revalidate bool
}

func goldenCluster() *fakeCluster {
	return &fakeCluster{cards: corpusCards(), hits: []string{"base1-4", "ex10-?", "ex10-!"}}
}

func idList(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "base1-" + strconv.Itoa(i+1)
	}
	return strings.Join(ids, ",")
}

// wrongKey configures one generated key and presents another.
func wrongKey(t *testing.T) http.Header {
	t.Setenv("POKESEARCH_API_KEYS", throwawayKey(t))
	return http.Header{"X-Api-Key": []string{throwawayKey(t)}}
}

var liveCases = []liveCase{
	{name: "card hit", path: "/api/v1/cards/base1-4", status: 200, schema: "card-response.json", contains: `"evolves_from":"Charmeleon"`},
	{name: "card hit on an unstamped index", path: "/api/v1/cards/base1-4", status: 200, schema: "card-response.json",
		cluster: func() *fakeCluster { f := goldenCluster(); f.unstamped = true; return f }, contains: `"seed":{"ref":null,"seeded_at":null}`},
	{name: "card ex10-?", path: "/api/v1/cards/ex10-%3F", status: 200, schema: "card-response.json", contains: `"id":"ex10-?"`},
	{name: "card ex10-!", path: "/api/v1/cards/ex10-!", status: 200, schema: "card-response.json", contains: `"id":"ex10-!"`},
	{name: "card miss", path: "/api/v1/cards/base1-999", status: 404, schema: "error.json", contains: `"code":"not_found"`},
	{name: "card id failing the pattern", path: "/api/v1/cards/-base1-4", status: 400, schema: "error.json", contains: `"code":"invalid_param"`},
	{name: "card with an empty id", path: "/api/v1/cards/", status: 400, schema: "error.json", contains: `"field":"id"`},
	{name: "card with Elasticsearch down", path: "/api/v1/cards/base1-4", status: 503, schema: "error.json",
		cluster: func() *fakeCluster { f := goldenCluster(); f.down = true; return f }, contains: `"code":"es_unavailable"`},
	{name: "card 304", path: "/api/v1/cards/ex10-%3F", status: 200, schema: "card-response.json", revalidate: true},
	{name: "batch with missing ids", path: "/api/v1/cards?ids=base1-4,nope-1,ex10-%3F,ex10-!,nope-2", status: 200,
		schema: "cards-batch-response.json", contains: `"missing":["nope-1","nope-2"]`},
	{name: "batch with every id missing", path: "/api/v1/cards?ids=nope-1", status: 200,
		schema: "cards-batch-response.json", contains: `"data":[]`},
	{name: "batch of 100 ids", path: "/api/v1/cards?ids=" + idList(100), status: 200, schema: "cards-batch-response.json"},
	{name: "batch of 101 ids", path: "/api/v1/cards?ids=" + idList(101), status: 400, schema: "error.json", contains: `"field":"ids"`},
	{name: "batch 304", path: "/api/v1/cards?ids=base1-4,nope-1", status: 200, schema: "cards-batch-response.json", revalidate: true},
	{name: "sets", path: "/api/v1/sets", status: 200, schema: "sets-response.json", contains: `"card_count":2`},
	{name: "sets 304", path: "/api/v1/sets", status: 200, schema: "sets-response.json", revalidate: true},
	{name: "sets with a wrong API key", path: "/api/v1/sets", header: wrongKey, status: 401, schema: "error.json", contains: `"code":"unauthorized"`},
	{name: "card with a wrong API key", path: "/api/v1/cards/base1-4", header: wrongKey, status: 401, schema: "error.json", contains: `"code":"unauthorized"`},
	{name: "meta", path: "/api/meta", status: 200, schema: "meta.json", contains: `"seeded_at":"2026-01-01T00:00:00Z"`},
	{name: "meta on an unstamped index", path: "/api/meta", status: 200, schema: "meta.json",
		cluster: func() *fakeCluster { f := goldenCluster(); f.unstamped = true; return f }, contains: `"seed":null`},
	{name: "search text query", path: "/api/search?q=charizard", status: 200, schema: "search.json", contains: `"highlights":[{"name":`},
	{name: "search browse with filters", path: "/api/search?set=base1&types=Fire&sort=newest", status: 200, schema: "search.json", contains: `"label":"Base"`},
	{name: "search with no hits", path: "/api/search?q=charizrd", status: 200, schema: "search.json",
		cluster: func() *fakeCluster { f := goldenCluster(); f.hits = nil; return f }, contains: `"did_you_mean":"charizard"`},
	{name: "search with debug", path: "/api/search?q=charizard&debug=1", status: 200, schema: "search.json", contains: `"dsl":{`},
	{name: "search with a bad parameter", path: "/api/search?sort=bogus", status: 400, schema: "error.json", contains: `"field":"sort"`},
}

func TestHandlerOutputMatchesSchemas(t *testing.T) {
	schemas := loadSchemas(t)
	for _, tc := range liveCases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := goldenCluster()
			if tc.cluster != nil {
				cluster = tc.cluster()
			}
			var header http.Header
			if tc.header != nil {
				header = tc.header(t)
			}
			base := startServer(t, cluster)

			got := fetch(t, base, tc.path, header)
			if got.status != tc.status {
				t.Fatalf("status %d, want %d: %s", got.status, tc.status, got.body)
			}
			bad, err := schemas.violations(tc.schema, got.body)
			if err != nil {
				t.Fatalf("%s: %v\nbody: %s", tc.schema, err, got.body)
			}
			if len(bad) > 0 {
				t.Errorf("body does not satisfy %s:\n  %s\nbody: %s", tc.schema, strings.Join(bad, "\n  "), got.body)
			}
			if !bytes.Contains(got.body, []byte(tc.contains)) {
				t.Errorf("body lacks %s, so the case did not exercise its shape: %s", tc.contains, got.body)
			}

			if !tc.revalidate {
				return
			}
			etag := got.header.Get("ETag")
			if etag == "" {
				t.Fatal("a v1 200 carries no ETag to revalidate with")
			}
			again := fetch(t, base, tc.path, http.Header{"If-None-Match": []string{etag}})
			if again.status != http.StatusNotModified || len(again.body) != 0 || again.header.Get("ETag") != etag {
				t.Errorf("revalidation = %d with %d body bytes and ETag %q; want 304, no body, ETag %q",
					again.status, len(again.body), again.header.Get("ETag"), etag)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (b) Negative vectors: every file in testdata/invalid must fail, and for the
// reason its name gives. The name is <schema stem>__<keyword>__<what>.json, so
// "card-response__required__missing-id.json" must fail card-response.json on
// a "required" assertion. A validator that accepts everything fails this test.

func TestInvalidDocumentsAreRejected(t *testing.T) {
	schemas := loadSchemas(t)
	entries, err := os.ReadDir(invalidDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			parts := strings.Split(strings.TrimSuffix(name, ".json"), "__")
			if len(parts) != 3 || filepath.Ext(name) != ".json" {
				t.Fatalf("%s: want <schema stem>__<keyword>__<what>.json", name)
			}
			schema, keyword := parts[0]+".json", parts[1]
			doc, err := os.ReadFile(filepath.Join(invalidDir, name))
			if err != nil {
				t.Fatal(err)
			}
			// A vector that is not JSON would "fail" for the wrong reason.
			if !json.Valid(doc) {
				t.Fatalf("%s is not valid JSON", name)
			}
			bad, err := schemas.violations(schema, doc)
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) == 0 {
				t.Fatalf("%s satisfies %s; it must not", name, schema)
			}
			for _, v := range bad {
				if strings.HasSuffix(v, "/"+keyword) {
					seen[keyword+"/"+parts[2]] = true
					return
				}
			}
			t.Errorf("%s fails %s, but not on %q: %v", name, schema, keyword, bad)
		})
	}
	// The three vectors the contract names must exist, so deleting one cannot
	// leave this test passing.
	for _, want := range []string{"additionalProperties/extra-field", "required/missing-id", "type/wrong-type"} {
		if !seen[want] {
			t.Errorf("no passing negative vector for %s in %s", want, invalidDir)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) Completeness: every /api/v1/ route the server registers maps to the
// schema its successful answer satisfies, and the cases above exercise it.

// v1Routes is the route → schema map. Every v1 error answer is error.json; an
// empty card id ({$}) can only ever be an error.
var v1Routes = map[string]string{
	"GET /api/v1/cards":      "cards-batch-response.json",
	"GET /api/v1/cards/{id}": "card-response.json",
	"GET /api/v1/cards/{$}":  "error.json",
	"GET /api/v1/sets":       "sets-response.json",
}

// legacyRoutes are the two pre-v1 endpoints PokéPrices reads; they are
// described, not changed.
var legacyRoutes = map[string]string{
	"GET /api/meta":   "meta.json",
	"GET /api/search": "search.json",
}

// muxPatterns parses the server package and returns every pattern passed to
// s.mux.HandleFunc or s.mux.Handle as a string literal.
func muxPatterns(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(serverDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			recv, ok := sel.X.(*ast.SelectorExpr)
			if !ok || recv.Sel.Name != "mux" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: a mux pattern that is not a string literal cannot be checked", fset.Position(call.Pos()))
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, pattern)
			return true
		})
	}
	return out
}

func TestEveryV1RouteHasASchema(t *testing.T) {
	patterns := muxPatterns(t)
	for want := range legacyRoutes {
		// Also proves the parser reads the registrations at all.
		if !slices.Contains(patterns, want) {
			t.Errorf("route %q not found among the server's mux patterns %v", want, patterns)
		}
	}
	var v1 []string
	for _, p := range patterns {
		if strings.Contains(p, "/api/v1/") {
			v1 = append(v1, p)
		}
	}
	if len(v1) == 0 {
		t.Fatalf("no /api/v1/ pattern found in %v", patterns)
	}
	for _, p := range v1 {
		if _, ok := v1Routes[p]; !ok {
			t.Errorf("v1 route %q has no schema: add it to v1Routes and docs/api/schema", p)
		}
	}
	for p := range v1Routes {
		if !slices.Contains(v1, p) {
			t.Errorf("v1Routes names %q, which the server does not register", p)
		}
	}

	files := schemaFiles(t)
	// Every mapped route is exercised by a case validating against its schema:
	// a registry mux resolves each case's path the way the server's does.
	mux := http.NewServeMux()
	for _, p := range patterns {
		mux.HandleFunc(p, func(http.ResponseWriter, *http.Request) {})
	}
	routes := map[string]string{}
	for p, s := range v1Routes {
		routes[p] = s
	}
	for p, s := range legacyRoutes {
		routes[p] = s
	}
	for p, schema := range routes {
		if !slices.Contains(files, schema) {
			t.Errorf("%s maps to %s, which is not in %s", p, schema, schemaDir)
		}
		covered := false
		for _, tc := range liveCases {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if _, matched := mux.Handler(req); matched == p && tc.schema == schema {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("no handler case validates %s against %s", p, schema)
		}
	}
}

// ---------------------------------------------------------------------------
// Criterion 1: the schema files themselves.

func schemaFiles(t testing.TB) []string {
	t.Helper()
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	return out
}

func readSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

// opaque lists the subschemas allowed to constrain nothing, with the reason.
var opaque = map[string]string{
	// debug=1 echoes the Elasticsearch request body: Elasticsearch's query
	// language, not this contract's. Closing it would make every query tweak
	// a contract change; PokéPrices never sends debug=1.
	"search.json#/properties/dsl": "Elasticsearch DSL echo",
}

// walkSchema visits every subschema reachable through the structural
// keywords, with its JSON pointer.
func walkSchema(ptr string, node map[string]any, visit func(string, map[string]any)) {
	visit(ptr, node)
	for _, kw := range []string{"properties", "$defs"} {
		if m, ok := node[kw].(map[string]any); ok {
			for k, v := range m {
				if sub, ok := v.(map[string]any); ok {
					walkSchema(ptr+"/"+kw+"/"+k, sub, visit)
				}
			}
		}
	}
	for _, kw := range []string{"items", "additionalProperties"} {
		if sub, ok := node[kw].(map[string]any); ok {
			walkSchema(ptr+"/"+kw, sub, visit)
		}
	}
	for _, kw := range []string{"oneOf", "anyOf", "allOf", "prefixItems"} {
		if list, ok := node[kw].([]any); ok {
			for i, v := range list {
				if sub, ok := v.(map[string]any); ok {
					walkSchema(ptr+"/"+kw+"/"+strconv.Itoa(i), sub, visit)
				}
			}
		}
	}
}

func declaresObject(node map[string]any) bool {
	switch typ := node["type"].(type) {
	case string:
		if typ == "object" {
			return true
		}
	case []any:
		if slices.Contains(typ, any("object")) {
			return true
		}
	}
	_, hasProps := node["properties"]
	_, hasRequired := node["required"]
	return hasProps || hasRequired
}

func TestSchemasAreClosedDraft2020(t *testing.T) {
	files := schemaFiles(t)
	want := []string{"card.json", "card-response.json", "cards-batch-response.json", "sets-response.json", "error.json", "meta.json", "search.json"}
	for _, f := range want {
		if !slices.Contains(files, f) {
			t.Errorf("%s missing from %s", f, schemaDir)
		}
	}
	for _, name := range files {
		doc := readSchema(t, name)
		if doc["$schema"] != draft2020 {
			t.Errorf("%s: $schema = %v, want %s", name, doc["$schema"], draft2020)
		}
		objects := 0
		walkSchema("", doc, func(ptr string, node map[string]any) {
			at := name + "#" + ptr
			if declaresObject(node) {
				objects++
				if node["additionalProperties"] != false {
					t.Errorf("%s: an object without additionalProperties: false", at)
				}
			}
			for _, kw := range []string{"type", "$ref", "const", "enum", "oneOf", "anyOf", "allOf"} {
				if _, ok := node[kw]; ok {
					return
				}
			}
			if _, ok := opaque[at]; !ok && ptr != "" {
				t.Errorf("%s constrains nothing", at)
			}
		})
		if objects == 0 && name != "card.json" {
			t.Errorf("%s declares no object", name)
		}
	}
}

// jsonFields returns a struct's JSON member names and the ones encoding/json
// always emits (no omitempty), in declaration order.
func jsonFields(typ reflect.Type) (all, always []string) {
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		all = append(all, name)
		if !slices.Contains(strings.Split(opts, ","), "omitempty") {
			always = append(always, name)
		}
	}
	return all, always
}

func stringSet(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func keys(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// card.json is tcg.Card: the same members, and exactly the non-omitempty ones
// required. The nested shapes follow their structs the same way.
func TestCardSchemaFollowsTheIndexedStruct(t *testing.T) {
	doc := readSchema(t, "card.json")
	defs, _ := doc["$defs"].(map[string]any)
	shapes := []struct {
		at   string
		node any
		typ  reflect.Type
	}{
		{"card.json#", doc, reflect.TypeFor[tcg.Card]()},
		{"card.json#/$defs/attack", defs["attack"], reflect.TypeFor[tcg.Attack]()},
		{"card.json#/$defs/ability", defs["ability"], reflect.TypeFor[tcg.Ability]()},
		{"card.json#/$defs/value", defs["value"], reflect.TypeFor[tcg.Value]()},
	}
	for _, s := range shapes {
		node, _ := s.node.(map[string]any)
		all, always := jsonFields(s.typ)
		slices.Sort(all)
		slices.Sort(always)
		if got := keys(node["properties"]); !slices.Equal(got, all) {
			t.Errorf("%s properties = %v, want %s's members %v", s.at, got, s.typ, all)
		}
		if got := stringSet(node["required"]); !slices.Equal(got, always) {
			t.Errorf("%s required = %v, want %s's non-omitempty members %v", s.at, got, s.typ, always)
		}
	}
	_, always := jsonFields(reflect.TypeFor[tcg.Card]())
	slices.Sort(always)
	wantRequired := []string{"id", "name", "supertype", "number", "set_id", "set_name", "set_series", "set_total", "release_date", "image_small", "image_large"}
	slices.Sort(wantRequired)
	if !slices.Equal(always, wantRequired) {
		t.Errorf("tcg.Card's non-omitempty members are %v; the published contract requires %v", always, wantRequired)
	}
}

// serverErrorCodes collects the string constants named code* in the server
// package: every code an error envelope can carry.
func serverErrorCodes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(serverDir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "code") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						out = append(out, v)
					}
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestErrorCodeEnumIsTheServersCodes(t *testing.T) {
	doc := readSchema(t, "error.json")
	props, _ := doc["properties"].(map[string]any)
	errObj, _ := props["error"].(map[string]any)
	errProps, _ := errObj["properties"].(map[string]any)
	code, _ := errProps["code"].(map[string]any)
	enum := stringSet(code["enum"])
	for _, want := range []string{"not_found", "unauthorized", "invalid_param"} {
		if !slices.Contains(enum, want) {
			t.Errorf("error.json code enum %v lacks %q", enum, want)
		}
	}
	if codes := serverErrorCodes(t); !slices.Equal(enum, codes) {
		t.Errorf("error.json code enum = %v, the server's code* constants = %v", enum, codes)
	}
}
