package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// v1Card is a fully populated card, so the "exactly as indexed" checks cover
// every optional field a real document can carry.
func v1Card(id, name string) tcg.Card {
	hp := 60
	return tcg.Card{
		ID:                     id,
		Name:                   name,
		Supertype:              "Pokémon",
		Subtypes:               []string{"Basic"},
		HP:                     &hp,
		Types:                  []string{"Psychic"},
		Attacks:                []tcg.Attack{{Name: "Hidden Power", Cost: []string{"Psychic"}, ConvertedCost: 1, Damage: "10+", DamageValue: 10, Text: "Flip a coin & see <what> happens."}},
		Abilities:              []tcg.Ability{{Name: "Shape Shift", Type: "Poké-Power", Text: "Once during your turn."}},
		Weaknesses:             []tcg.Value{{Type: "Psychic", Value: "×2"}},
		Resistances:            []tcg.Value{{Type: "Fighting", Value: "-30"}},
		RetreatCost:            1,
		Rarity:                 "Rare",
		Artist:                 "Example Artist",
		FlavorText:             "A flavor text.",
		NationalPokedexNumbers: []int{201},
		Number:                 strings.TrimPrefix(id, "ex10-"),
		SetID:                  "ex10",
		SetName:                "Unseen Forces",
		SetSeries:              "EX",
		SetTotal:               145,
		ReleaseDate:            "2005-08-22",
		ImageSmall:             "https://images.example.com/" + id + ".png",
		ImageLarge:             "https://images.example.com/" + id + "_hires.png",
	}
}

// indexedSource is the document exactly as the seeder writes it: esindex
// marshals a tcg.Card with encoding/json, so that is the reference encoding.
func indexedSource(t *testing.T, c tcg.Card) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mgetCall is one request the fake cluster received.
type mgetCall struct {
	Path, RawQuery, Body string
}

// fakeCards is a fake Elasticsearch holding a few card documents. It answers
// _mget the way ES does (one docs entry per requested id, in request order,
// found:false for an unknown one) and _mapping with the seed provenance.
type fakeCards struct {
	mu      sync.Mutex
	docs    map[string][]byte
	mapping string
	calls   []mgetCall
}

func newFakeCards(t *testing.T, cards ...tcg.Card) *fakeCards {
	t.Helper()
	f := &fakeCards{docs: map[string][]byte{}, mapping: seedMetaBody}
	for _, c := range cards {
		f.docs[c.ID] = indexedSource(t, c)
	}
	return f
}

func (f *fakeCards) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mgetCall{Path: r.URL.Path, RawQuery: r.URL.RawQuery, Body: string(body)})

	switch {
	case strings.HasSuffix(r.URL.Path, "/_mapping"):
		return esResponse(200, f.mapping), nil
	case r.URL.Path == "/cards/_mget":
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return esResponse(400, `{"error":"bad body"}`), nil
		}
		docs := make([]string, 0, len(req.IDs))
		for _, id := range req.IDs {
			idJSON, _ := json.Marshal(id)
			if src, ok := f.docs[id]; ok {
				docs = append(docs, fmt.Sprintf(`{"_index":"cards","_id":%s,"_version":1,"_seq_no":0,"_primary_term":1,"found":true,"_source":%s}`, idJSON, src))
			} else {
				docs = append(docs, fmt.Sprintf(`{"_index":"cards","_id":%s,"found":false}`, idJSON))
			}
		}
		return esResponse(200, `{"docs":[`+strings.Join(docs, ",")+`]}`), nil
	}
	return nil, errors.New("unexpected ES call: " + r.URL.Path)
}

// mgets returns the _mget calls only (the seed mapping read is separate).
func (f *fakeCards) mgets() []mgetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []mgetCall
	for _, c := range f.calls {
		if strings.Contains(c.Path, "_mget") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeCards) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// weakTag is the validator a v1 200 must carry for exactly these body bytes.
func weakTag(body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
}

const v1SeedJSON = `{"ref":"abc","seeded_at":"2026-01-01T00:00:00Z"}`

func assertV1OK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got, want := rec.Header().Get("ETag"), weakTag(rec.Body.Bytes()); got != want {
		t.Errorf("ETag = %q, want %q (weak, first 16 hex of sha256(body))", got, want)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id header missing: the header carries the id a v1 body does not")
	}
	if strings.Contains(rec.Body.String(), "request_id") {
		t.Errorf("v1 200 body must not carry request_id: %s", rec.Body.String())
	}
}

func assertV1Error(t *testing.T, rec *httptest.ResponseRecorder, status int, code, field string) errorBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != code || body.Error.Field != field || body.Error.Message == "" {
		t.Errorf("error = %+v, want code %q field %q and a message", body.Error, code, field)
	}
	if body.RequestID == "" || body.RequestID != rec.Header().Get("X-Request-Id") {
		t.Errorf("request_id = %q, header = %q: must be present and equal", body.RequestID, rec.Header().Get("X-Request-Id"))
	}
	if tag := rec.Header().Get("ETag"); tag != "" {
		t.Errorf("an error response must carry no ETag, got %q", tag)
	}
	return body
}

// GET /api/v1/cards/{id}: the card exactly as indexed plus the corpus
// provenance, and nothing else — byte for byte.
func TestV1CardFound(t *testing.T) {
	card := v1Card("base1-4", "Charizard")
	es := newFakeCards(t, card)
	s, _ := newTestServer(t, es)

	rec := get(t, s, "/api/v1/cards/base1-4")
	assertV1OK(t, rec)
	want := `{"data":` + string(indexedSource(t, card)) + `,"seed":` + v1SeedJSON + "}\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}

	calls := es.mgets()
	if len(calls) != 1 {
		t.Fatalf("_mget calls = %d, want exactly 1: %+v", len(calls), es.calls)
	}
	if calls[0].Path != "/cards/_mget" || calls[0].RawQuery != "" {
		t.Errorf("ES request = %s?%s, want /cards/_mget with no query", calls[0].Path, calls[0].RawQuery)
	}
	if calls[0].Body != `{"ids":["base1-4"]}` {
		t.Errorf("_mget body = %s", calls[0].Body)
	}
}

// Unown "?" and "!" are the two corpus ids an id-in-the-path lookup cannot
// reach: the client writes "?" into the URL unescaped, so ES would look up
// "ex10-". Through _mget the id rides in the body and both are found.
func TestV1CardUnownIDs(t *testing.T) {
	es := newFakeCards(t, v1Card("ex10-?", "Unown"), v1Card("ex10-!", "Unown"))
	s, _ := newTestServer(t, es)

	for _, tc := range []struct{ path, id string }{
		{"/api/v1/cards/ex10-%3F", "ex10-?"},
		{"/api/v1/cards/ex10-!", "ex10-!"},
		{"/api/v1/cards/ex10-%21", "ex10-!"},
	} {
		rec := get(t, s, tc.path)
		assertV1OK(t, rec)
		var resp struct {
			Data tcg.Card `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Data.ID != tc.id {
			t.Errorf("%s: data.id = %q, want %q", tc.path, resp.Data.ID, tc.id)
		}
	}
	for _, c := range es.calls {
		if c.Path != "/cards/_mget" && c.Path != "/cards/_mapping" {
			t.Errorf("unexpected ES path %q: only _mget and the seed mapping may be called", c.Path)
		}
		if strings.Contains(c.Path, "ex10") || strings.Contains(c.RawQuery, "ex10") {
			t.Errorf("a card id reached the ES URL: %s?%s", c.Path, c.RawQuery)
		}
	}
	if got := es.mgets(); len(got) != 3 || got[0].Body != `{"ids":["ex10-?"]}` || got[1].Body != `{"ids":["ex10-!"]}` {
		t.Errorf("_mget bodies = %+v", got)
	}
}

func TestV1CardNotFound(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, es)

	rec := getWith(t, s, "/api/v1/cards/zzz9-1", map[string]string{"X-Request-Id": "trace-v1-404"})
	body := assertV1Error(t, rec, http.StatusNotFound, "not_found", "id")
	if body.Error.Message != "no card with id zzz9-1" {
		t.Errorf("message = %q", body.Error.Message)
	}
	if body.RequestID != "trace-v1-404" {
		t.Errorf("request_id = %q, want the inbound id", body.RequestID)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control on a 404 = %q, want none", got)
	}
}

// An id that fails the pattern never reaches Elasticsearch. "%2F" is unescaped
// by the mux into a literal slash inside the one path segment, and the pattern
// then rejects it.
func TestV1CardInvalidID(t *testing.T) {
	es := newFakeCards(t)
	s, _ := newTestServer(t, es)

	for _, path := range []string{
		"/api/v1/cards/a%2Fb",
		"/api/v1/cards/-base1-4",
		"/api/v1/cards/.hidden",
		"/api/v1/cards/a%20b",
		"/api/v1/cards/%C3%A4",
		"/api/v1/cards/a" + strings.Repeat("b", 128),
		"/api/v1/cards/",
	} {
		rec := get(t, s, path)
		assertV1Error(t, rec, http.StatusBadRequest, "invalid_param", "id")
	}
	if n := es.callCount(); n != 0 {
		t.Errorf("ES calls = %d, want 0: an invalid id must not reach the cluster", n)
	}

	// 128 characters is the inclusive maximum.
	es.docs["a"+strings.Repeat("b", 127)] = indexedSource(t, v1Card("a"+strings.Repeat("b", 127), "Long"))
	assertV1OK(t, get(t, s, "/api/v1/cards/a"+strings.Repeat("b", 127)))
}

// An index seeded before provenance stamping reports both seed fields as null,
// never a missing object: the v1 shape is fixed.
func TestV1CardSeedNull(t *testing.T) {
	card := v1Card("base1-4", "Charizard")
	es := newFakeCards(t, card)
	es.mapping = `{"cards":{"mappings":{}}}`
	s, _ := newTestServer(t, es)

	rec := get(t, s, "/api/v1/cards/base1-4")
	assertV1OK(t, rec)
	want := `{"data":` + string(indexedSource(t, card)) + `,"seed":{"ref":null,"seeded_at":null}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}

	// A stamp that carries only one of the two fields reports the other as
	// null rather than as an empty string.
	es.mapping = `{"cards":{"mappings":{"_meta":{"seeded_at":"2026-01-01T00:00:00Z"}}}}`
	s, _ = newTestServer(t, es)
	rec = get(t, s, "/api/v1/cards?ids=base1-4")
	assertV1OK(t, rec)
	if b := decodeBatch(t, rec); string(b.Seed) != `{"ref":null,"seeded_at":"2026-01-01T00:00:00Z"}` {
		t.Errorf("seed = %s", b.Seed)
	}
}

type v1BatchBody struct {
	Data    []tcg.Card      `json:"data"`
	Missing []string        `json:"missing"`
	Seed    json.RawMessage `json:"seed"`
}

func decodeBatch(t *testing.T, rec *httptest.ResponseRecorder) v1BatchBody {
	t.Helper()
	var b v1BatchBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode batch body %q: %v", rec.Body.String(), err)
	}
	return b
}

func ids(cards []tcg.Card) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c.ID)
	}
	return out
}

// GET /api/v1/cards?ids=: trimmed, de-duplicated (first occurrence wins), one
// _mget, found cards and missing ids each in request order.
func TestV1CardsBatch(t *testing.T) {
	base := v1Card("base1-4", "Charizard")
	q := v1Card("ex10-?", "Unown")
	bang := v1Card("ex10-!", "Unown")
	es := newFakeCards(t, base, q, bang)
	s, _ := newTestServer(t, es)

	rec := get(t, s, "/api/v1/cards?ids=ex10-%21,%20nope1-1%20,base1-4,ex10-%3F,%20ex10-%21,gone2-2,base1-4")
	assertV1OK(t, rec)
	want := `{"data":[` + string(indexedSource(t, bang)) + "," + string(indexedSource(t, base)) + "," +
		string(indexedSource(t, q)) + `],"missing":["nope1-1","gone2-2"],"seed":` + v1SeedJSON + "}\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}

	calls := es.mgets()
	if len(calls) != 1 {
		t.Fatalf("_mget calls = %d, want exactly 1", len(calls))
	}
	if calls[0].Path != "/cards/_mget" || calls[0].RawQuery != "" {
		t.Errorf("ES request = %s?%s", calls[0].Path, calls[0].RawQuery)
	}
	if calls[0].Body != `{"ids":["ex10-!","nope1-1","base1-4","ex10-?","gone2-2"]}` {
		t.Errorf("_mget body = %s", calls[0].Body)
	}
}

// Every id missing is still a valid answer to a valid request: 200 with empty
// data, never a 404 and never null.
func TestV1CardsAllMissing(t *testing.T) {
	s, _ := newTestServer(t, newFakeCards(t))
	rec := get(t, s, "/api/v1/cards?ids=nope1-1,nope1-2")
	assertV1OK(t, rec)
	want := `{"data":[],"missing":["nope1-1","nope1-2"],"seed":` + v1SeedJSON + "}\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
}

func TestV1CardsAllFound(t *testing.T) {
	s, _ := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))
	rec := get(t, s, "/api/v1/cards?ids=base1-4")
	assertV1OK(t, rec)
	if b := decodeBatch(t, rec); !slices.Equal(ids(b.Data), []string{"base1-4"}) || b.Missing == nil || len(b.Missing) != 0 {
		t.Errorf("data %v, missing %#v; want [base1-4] and an empty (non-null) list", ids(b.Data), b.Missing)
	}
	if !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Errorf("missing must serialise as [], got %s", rec.Body.String())
	}
}

func idList(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i+1)
	}
	return out
}

func TestV1CardsBadParams(t *testing.T) {
	es := newFakeCards(t)
	s, _ := newTestServer(t, es)

	for _, path := range []string{
		"/api/v1/cards",
		"/api/v1/cards?ids=",
		"/api/v1/cards?ids=%20",
		"/api/v1/cards?ids=,",
		"/api/v1/cards?ids=base1-4,,base1-5",
		"/api/v1/cards?ids=base1-4,a%2Fb",
		"/api/v1/cards?ids=base1-4,-bad",
		"/api/v1/cards?ids=" + strings.Join(idList("set1", 101), ","),
	} {
		rec := get(t, s, path)
		assertV1Error(t, rec, http.StatusBadRequest, "invalid_param", "ids")
	}
	if n := es.callCount(); n != 0 {
		t.Errorf("ES calls = %d, want 0: a rejected batch must not reach the cluster", n)
	}
}

// The limit is on distinct ids: 100 distinct ids plus a repeat is 101 entries
// and still a valid request.
func TestV1CardsDuplicatesCollapseUnderLimit(t *testing.T) {
	es := newFakeCards(t)
	s, _ := newTestServer(t, es)
	hundred := idList("set1", 100)
	rec := get(t, s, "/api/v1/cards?ids="+strings.Join(append(slices.Clone(hundred), hundred[0]), ","))
	assertV1OK(t, rec)
	if b := decodeBatch(t, rec); !slices.Equal(b.Missing, hundred) || len(b.Data) != 0 {
		t.Errorf("missing = %d ids, data = %d; want the 100 distinct ids in request order", len(b.Missing), len(b.Data))
	}
}

// A conditional request for an unchanged representation is a 304 with the
// validator and no body; any other validator gets the full 200.
func TestV1ConditionalGet(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, es)

	for _, path := range []string{"/api/v1/cards/base1-4", "/api/v1/cards?ids=base1-4,nope1-1"} {
		first := get(t, s, path)
		assertV1OK(t, first)
		tag := first.Header().Get("ETag")
		strong := strings.TrimPrefix(tag, "W/")

		for _, inm := range []string{tag, strong, `"other", ` + tag, "*"} {
			rec := getWith(t, s, path, map[string]string{"If-None-Match": inm})
			if rec.Code != http.StatusNotModified {
				t.Fatalf("%s If-None-Match %s: status %d, want 304", path, inm, rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("304 carries a body: %q", rec.Body.String())
			}
			if got := rec.Header().Get("ETag"); got != tag {
				t.Errorf("304 ETag = %q, want %q", got, tag)
			}
		}
		rec := getWith(t, s, path, map[string]string{"If-None-Match": `W/"0000000000000000"`})
		assertV1OK(t, rec)
		if !bytes.Equal(rec.Body.Bytes(), first.Body.Bytes()) {
			t.Errorf("a non-matching validator must get the full body")
		}
	}
}

// The tag is computed on the identity body before withGzip encodes it, so a
// gzip client and an identity client see the same weak validator — which is
// why it is weak (one tag, two codings).
func TestV1ETagSameWithAndWithoutGzip(t *testing.T) {
	var cards []tcg.Card
	var want []string
	for i := range 6 {
		c := v1Card(fmt.Sprintf("ex10-%d", i+1), "Unown")
		cards = append(cards, c)
		want = append(want, c.ID)
	}
	s, _ := newTestServer(t, newFakeCards(t, cards...))
	path := "/api/v1/cards?ids=" + strings.Join(want, ",")

	plain := get(t, s, path)
	assertV1OK(t, plain)
	if plain.Body.Len() <= minGzipBody {
		t.Fatalf("body is %d bytes; the test needs one past minGzipBody (%d) to exercise gzip", plain.Body.Len(), minGzipBody)
	}

	zipped := getWith(t, s, path, map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Code != 200 || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, Content-Encoding %q; want a gzipped 200", zipped.Code, zipped.Header().Get("Content-Encoding"))
	}
	if got, want := zipped.Header().Get("ETag"), plain.Header().Get("ETag"); got != want || got == "" {
		t.Errorf("gzip ETag %q != identity ETag %q", got, want)
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plain.Body.Bytes()) {
		t.Error("the gzipped body does not decode to the identity body")
	}

	notModified := getWith(t, s, path, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": plain.Header().Get("ETag")})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("Content-Encoding") != "" {
		t.Errorf("gzip conditional: status %d, %d body bytes, Content-Encoding %q; want a bare 304",
			notModified.Code, notModified.Body.Len(), notModified.Header().Get("Content-Encoding"))
	}
}

func TestV1ESDown(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	for _, path := range []string{"/api/v1/cards/base1-4", "/api/v1/cards?ids=base1-4"} {
		rec := get(t, s, path)
		assertV1Error(t, rec, http.StatusServiceUnavailable, "es_unavailable", "")
	}
	if !strings.Contains(logBuf.String(), "connection refused") {
		t.Error("the ES cause must reach the query log")
	}
}

// A per-document error inside a 200 _mget (an unseeded or closed index) is the
// cluster failing to answer, not a missing card.
func TestV1MgetDocumentError(t *testing.T) {
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			return esResponse(200, seedMetaBody), nil
		}
		return esResponse(200, `{"docs":[{"_index":"cards","_id":"base1-4","error":{"type":"index_not_found_exception","reason":"no such index [cards]"}}]}`), nil
	}))
	for _, path := range []string{"/api/v1/cards/base1-4", "/api/v1/cards?ids=base1-4"} {
		assertV1Error(t, get(t, s, path), http.StatusServiceUnavailable, "es_unavailable", "")
	}
}

func TestV1MgetHTTPError(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			return esResponse(200, seedMetaBody), nil
		}
		return esResponse(400, `{"error":{"type":"action_request_validation_exception"}}`), nil
	}))
	assertV1Error(t, get(t, s, "/api/v1/cards/base1-4"), http.StatusServiceUnavailable, "es_unavailable", "")
	if !strings.Contains(logBuf.String(), "action_request_validation_exception") {
		t.Error("the ES error body must reach the query log")
	}
}

// The provenance read can fail on its own; a v1 200 always carries seed, so
// that is a 503 too rather than a half-populated answer.
func TestV1SeedReadFails(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			return nil, errors.New("mapping read failed")
		}
		return es.RoundTrip(r)
	}))
	for _, path := range []string{"/api/v1/cards/base1-4", "/api/v1/cards?ids=base1-4"} {
		assertV1Error(t, get(t, s, path), http.StatusServiceUnavailable, "es_unavailable", "")
	}
}

// Each v1 request writes one query line naming its endpoint and the _mget body
// it sent, like every other ES-backed endpoint.
func TestV1QueryLog(t *testing.T) {
	s, logBuf := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))
	rec := getWith(t, s, "/api/v1/cards?ids=base1-4,nope1-1", map[string]string{"X-Request-Id": "trace-v1-log"})
	assertV1OK(t, rec)
	line := oneLine(t, logBuf, false)
	if line["endpoint"] != "v1_cards" || line["request_id"] != "trace-v1-log" || line["status"] != float64(200) || line["total"] != float64(1) {
		t.Errorf("query line = %v", line)
	}
	if dsl, _ := json.Marshal(line["dsl"]); string(dsl) != `{"ids":["base1-4","nope1-1"]}` {
		t.Errorf("logged dsl = %s", dsl)
	}
}

// routeLabel gives the v1 routes fixed labels: every card id collapses into
// one key, so a crawler walking ids cannot grow the expvar map.
func TestV1RouteLabels(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/cards/base1-4": "v1_card",
		"/api/v1/cards/ex10-?":  "v1_card",
		"/api/v1/cards/a/b/c":   "v1_card",
		"/api/v1/cards/":        "v1_card",
		"/api/v1/cards":         "v1_cards",
		"/api/v1/cardsx":        "static",
		"/api/v1":               "static",
	} {
		if got := routeLabel(path); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", path, got, want)
		}
	}

	s, _ := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))
	before := counter("v1_card_200")
	beforeBatch := counter("v1_cards_200")
	get(t, s, "/api/v1/cards/base1-4")
	get(t, s, "/api/v1/cards?ids=base1-4")
	if counter("v1_card_200") != before+1 || counter("v1_cards_200") != beforeBatch+1 {
		t.Errorf("v1 counters did not advance: v1_card_200 %d->%d, v1_cards_200 %d->%d",
			before, counter("v1_card_200"), beforeBatch, counter("v1_cards_200"))
	}
}

func counter(key string) int64 {
	v := httpStats.Get(key)
	if v == nil {
		return 0
	}
	var n int64
	_, _ = fmt.Sscan(v.String(), &n)
	return n
}

// F-P1-R1-01: the ES client writes a document id into the URL path unescaped,
// so any single-document API (Get, Explain, GetSource, Exists...) loses "?"
// ids. The v1 code may reach Elasticsearch through _mget only, and no id may
// be put in a URL path.
func TestV1CodeUsesOnlyMget(t *testing.T) {
	esAPI := regexp.MustCompile(`\bes\.([A-Za-z]+)`)
	for _, file := range []string{"v1.go", "../search/v1.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range esAPI.FindAllStringSubmatch(string(src), -1) {
			if m[1] != "Mget" {
				t.Errorf("%s calls es.%s; v1 code may only use es.Mget", file, m[1])
			}
		}
		for _, forbidden := range []string{"_doc", "_source/", "_explain", "WithDocumentID", "DocumentID"} {
			if bytes.Contains(src, []byte(forbidden)) {
				t.Errorf("%s mentions %q: v1 ids travel in the _mget body only", file, forbidden)
			}
		}
	}
	src, err := os.ReadFile("v1.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte("s.es.Mget(")) {
		t.Error("v1.go does not call s.es.Mget: the guard above would pass vacuously")
	}
}
