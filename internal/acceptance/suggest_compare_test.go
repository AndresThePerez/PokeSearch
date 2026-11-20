//go:build acceptance

package acceptance

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	searchpkg "github.com/AndresThePerez/pokesearch/internal/search"
)

// fetchRaw sends one request to the stack under test and returns the status,
// headers and whole body. It never fails on a non-2xx: the error cases need to
// read those.
func fetchRaw(t *testing.T, method, path string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	return res.StatusCode, res.Header, body
}

// suggest returns the names /api/suggest answers for q (already URL-escaped).
func suggest(t *testing.T, q string) []string {
	t.Helper()
	status, _, body := fetchRaw(t, http.MethodGet, "/api/suggest?q="+q)
	if status != http.StatusOK {
		t.Fatalf("GET /api/suggest?q=%s: status %d: %s", q, status, body)
	}
	var out struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET /api/suggest?q=%s: decode: %v", q, err)
	}
	if out.Suggestions == nil {
		t.Fatalf("GET /api/suggest?q=%s: suggestions is null or absent: %s", q, body)
	}
	return out.Suggestions
}

// hasWordWithPrefix reports whether any word of name starts with prefix, both
// compared lowercased: the ranked pass matches the start of any word of a name.
func hasWordWithPrefix(name, prefix string) bool {
	for _, w := range strings.Fields(strings.ToLower(name)) {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// A typed prefix returns names it begins, in any case: every name has a word
// starting with it, and the names that start with it outright are there.
func TestSuggestCompletesAPrefix(t *testing.T) {
	names := suggest(t, "pika")
	if len(names) == 0 {
		t.Fatal("pika completed to nothing")
	}
	starting := 0
	for _, name := range names {
		if !hasWordWithPrefix(name, "pika") {
			t.Errorf("%q has no word starting with pika", name)
		}
		if strings.HasPrefix(strings.ToLower(name), "pika") {
			starting++
		}
	}
	if starting == 0 {
		t.Errorf("no suggestion begins with pika: %v", names)
	}
	if upper := suggest(t, "PIKA"); !slices.Equal(upper, names) {
		t.Errorf("PIKA gave %v, pika gave %v: the match is case-insensitive", upper, names)
	}
}

// A misspelling no name starts with falls through to the fuzzy pass.
func TestSuggestFindsNamesForATypo(t *testing.T) {
	names := suggest(t, "pikuchu")
	pikachu := 0
	for _, name := range names {
		if strings.HasPrefix(strings.ToLower(name), "pikachu") {
			pikachu++
		}
	}
	if pikachu == 0 {
		t.Errorf("pikuchu suggested %v, want Pikachu names", names)
	}
}

// Input without accents reaches accented names: the fuzzy pass counts edits in
// characters, so pokemon is one edit from Pokémon.
func TestSuggestFindsAccentedNamesFromUnaccentedInput(t *testing.T) {
	names := suggest(t, "pokemon")
	accented := 0
	for _, name := range names {
		if strings.HasPrefix(strings.ToLower(name), "pokémon") {
			accented++
		}
	}
	if accented == 0 {
		t.Errorf("pokemon suggested %v, want names starting Pokémon", names)
	}
}

// The list repeats no name, ignoring case, and never exceeds the documented
// size, over every one-letter prefix and the prefixes above.
func TestSuggestListIsDeduplicatedAndBounded(t *testing.T) {
	prefixes := []string{"pika", "pikuchu", "pokemon", "char", "mr", "dark", "professor"}
	for c := 'a'; c <= 'z'; c++ {
		prefixes = append(prefixes, string(c))
	}
	full := 0
	for _, prefix := range prefixes {
		names := suggest(t, prefix)
		if len(names) > searchpkg.SuggestSize {
			t.Errorf("%q gave %d names, want at most %d", prefix, len(names), searchpkg.SuggestSize)
		}
		if len(names) == searchpkg.SuggestSize {
			full++
		}
		seen := map[string]bool{}
		for _, name := range names {
			folded := strings.ToLower(name)
			if name == "" || seen[folded] {
				t.Errorf("%q gave an empty or repeated name %q in %v", prefix, name, names)
			}
			seen[folded] = true
		}
	}
	if full == 0 {
		t.Errorf("no prefix filled the list, so the bound of %d went unchecked", searchpkg.SuggestSize)
	}
}

// An empty q, or one of only spaces, is an empty list and a 200, not an error.
func TestSuggestWithAnEmptyQIsAnEmptyList(t *testing.T) {
	for _, q := range []string{"", "%20%20"} {
		status, _, body := fetchRaw(t, http.MethodGet, "/api/suggest?q="+q)
		if status != http.StatusOK {
			t.Errorf("q=%q: status %d, want 200: %s", q, status, body)
			continue
		}
		if got := strings.TrimSpace(string(body)); got != `{"suggestions":[]}` {
			t.Errorf("q=%q: body %s, want {\"suggestions\":[]}", q, got)
		}
	}
}

// q is capped at 256 characters, counted as characters and not bytes.
func TestSuggestRejectsAnOverLongQ(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    string
		want int
	}{
		{"256 characters", strings.Repeat("a", 256), http.StatusOK},
		{"257 characters", strings.Repeat("a", 257), http.StatusBadRequest},
		{"256 accented characters", strings.Repeat("%C3%A9", 256), http.StatusOK},
		{"257 accented characters", strings.Repeat("%C3%A9", 257), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := fetchRaw(t, http.MethodGet, "/api/suggest?q="+tc.q)
			if status != tc.want {
				t.Fatalf("status %d, want %d: %.200s", status, tc.want, body)
			}
			if tc.want != http.StatusBadRequest {
				return
			}
			var e errorResp
			if err := json.Unmarshal(body, &e); err != nil {
				t.Fatal(err)
			}
			if e.Error.Code != "invalid_param" || e.Error.Field != "q" || e.Error.Message == "" || e.RequestID == "" {
				t.Errorf("error = %+v, want invalid_param on q with a message and a request id", e)
			}
		})
	}
}

type compareEntry struct {
	Rank  int     `json:"rank"`
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

type compareSide struct {
	Profile string             `json:"profile"`
	Boosts  map[string]float64 `json:"boosts"`
	Total   int                `json:"total"`
	Results []compareEntry     `json:"results"`
}

type compareDelta struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	RankA  *int   `json:"rank_a"`
	RankB  *int   `json:"rank_b"`
	Delta  *int   `json:"delta"`
}

type compareResp struct {
	Q       string         `json:"q"`
	Window  int            `json:"window"`
	A       compareSide    `json:"a"`
	B       compareSide    `json:"b"`
	Deltas  []compareDelta `json:"deltas"`
	Missing int            `json:"missing_rank"`
}

func compare(t *testing.T, query string) compareResp {
	t.Helper()
	status, _, body := fetchRaw(t, http.MethodGet, "/api/compare?"+query)
	if status != http.StatusOK {
		t.Fatalf("GET /api/compare?%s: status %d: %s", query, status, body)
	}
	var out compareResp
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET /api/compare?%s: decode: %v", query, err)
	}
	return out
}

// Both sides the Ranking lab shows are ranked lists: ranks count from 1 without
// a gap, scores never rise, every row names a card, and the movement list is
// the union of the two windows.
func TestCompareHasTwoRankedSides(t *testing.T) {
	r := compare(t, "q=pikachu")
	if r.Q != "pikachu" || r.Window != 10 {
		t.Errorf("q = %q, window = %d, want pikachu and 10", r.Q, r.Window)
	}
	if r.A.Profile != "served" || r.B.Profile != "previous" {
		t.Errorf("profiles = %q and %q, want served and previous", r.A.Profile, r.B.Profile)
	}
	branches := map[string]bool{}
	for _, b := range searchpkg.Branches("pikachu") {
		branches[b.Name] = true
	}
	union := map[string]bool{}
	for side, s := range map[string]compareSide{"a": r.A, "b": r.B} {
		if len(s.Results) == 0 || len(s.Results) > r.Window {
			t.Fatalf("side %s has %d results, want 1 to %d", side, len(s.Results), r.Window)
		}
		if s.Total < len(s.Results) {
			t.Errorf("side %s: total %d is below its %d results", side, s.Total, len(s.Results))
		}
		if len(s.Boosts) != len(branches) {
			t.Errorf("side %s: boosts %v, want one per relevance branch %v", side, s.Boosts, branches)
		}
		for name := range s.Boosts {
			if !branches[name] {
				t.Errorf("side %s: boost for %q, which is no relevance branch", side, name)
			}
		}
		for i, e := range s.Results {
			if e.Rank != i+1 || e.ID == "" || e.Name == "" || e.Score <= 0 {
				t.Errorf("side %s result %d = %+v, want rank %d, an id, a name and a positive score", side, i, e, i+1)
			}
			if i > 0 && e.Score > s.Results[i-1].Score {
				t.Errorf("side %s: score rises from %v to %v at rank %d", side, s.Results[i-1].Score, e.Score, e.Rank)
			}
			union[e.ID] = true
		}
	}
	if top := r.A.Results[0].Name; !strings.EqualFold(top, "pikachu") {
		t.Errorf("served top result is %q, want Pikachu", top)
	}
	if len(r.Deltas) != len(union) {
		t.Errorf("%d deltas for a union of %d cards", len(r.Deltas), len(union))
	}
	for _, d := range r.Deltas {
		if !union[d.ID] {
			t.Errorf("delta for %s, which neither window holds", d.ID)
		}
		both := d.RankA != nil && d.RankB != nil
		if both != (d.Delta != nil) || (d.Status == "entered") != (d.RankA == nil) || (d.Status == "dropped") != (d.RankB == nil) {
			t.Errorf("delta %+v: status and null ranks disagree", d)
		}
	}
	if r.Missing != r.Window+1 {
		t.Errorf("missing_rank = %d, want window + 1 = %d", r.Missing, r.Window+1)
	}
}

// Two requests for the same query give the same windows and the same movement:
// nothing in the lists depends on when they were asked.
func TestCompareIsRepeatable(t *testing.T) {
	for _, q := range []string{"q=pikachu", "q=charizard&a=served&b=runner-up"} {
		first, second := compare(t, q), compare(t, q)
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s: two requests differ:\n%+v\n%+v", q, first, second)
		}
	}
}

// compare needs a query to rank.
func TestCompareWithoutQIsAnError(t *testing.T) {
	for _, path := range []string{"/api/compare", "/api/compare?q=", "/api/compare?q=%20"} {
		status, _, body := fetchRaw(t, http.MethodGet, path)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", path, status, body)
			continue
		}
		var e errorResp
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatal(err)
		}
		if e.Error.Code != "invalid_param" || e.Error.Field != "q" {
			t.Errorf("%s: error = %+v, want invalid_param on q", path, e.Error)
		}
	}
}

// HEAD answers the GET's status and headers, and no body.
func TestHeadSearchAnswersTheGetHeadersWithoutABody(t *testing.T) {
	const path = "/api/search?q=pikachu"
	getStatus, getHeader, getBody := fetchRaw(t, http.MethodGet, path)
	headStatus, headHeader, headBody := fetchRaw(t, http.MethodHead, path)
	if getStatus != http.StatusOK || headStatus != http.StatusOK {
		t.Fatalf("GET %d, HEAD %d, want 200 for both", getStatus, headStatus)
	}
	if len(getBody) == 0 {
		t.Fatal("the GET has no body, so a HEAD proves nothing")
	}
	if len(headBody) != 0 {
		t.Errorf("HEAD answered %d body bytes", len(headBody))
	}
	if got := headHeader.Get("Content-Type"); got == "" || got != getHeader.Get("Content-Type") {
		t.Errorf("HEAD Content-Type = %q, GET = %q", got, getHeader.Get("Content-Type"))
	}
	if got := headHeader.Get("X-Request-Id"); got == "" {
		t.Error("HEAD carries no X-Request-Id")
	}
}

// The rarity facet of an unfiltered search holds exactly the canonical list,
// so a reseed that adds or renames a rarity fails here, not silently by leaving
// the new value's other spellings unmapped.
func TestRarityFacetIsTheCanonicalList(t *testing.T) {
	var facet []string
	for _, b := range search(t, "").Facets["rarity"] {
		facet = append(facet, b.Value)
	}
	canonical := slices.Clone(searchpkg.CanonicalRarities)
	slices.Sort(facet)
	slices.Sort(canonical)
	if len(facet) != wantRarities || len(canonical) != wantRarities {
		t.Errorf("facet has %d rarities and the canonical list %d, want %d", len(facet), len(canonical), wantRarities)
	}
	if !slices.Equal(facet, canonical) {
		t.Errorf("facet rarities %v differ from search.CanonicalRarities %v", facet, canonical)
	}
}
