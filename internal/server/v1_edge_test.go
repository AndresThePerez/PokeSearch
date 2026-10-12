package server

import (
	"bytes"
	"encoding/json"
	"expvar"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// edgeSentinel stands in for text that must never reach a client: it only ever
// appears inside the fake cluster's own error replies.
const edgeSentinel = "SENTINEL-es-internal-detail-0000"

func rawMgetBody(t *testing.T, calls []mgetCall) []string {
	t.Helper()
	var out []string
	for _, c := range calls {
		out = append(out, c.Body)
	}
	return out
}

// A comma percent-encoded into a single path id is a comma after the mux
// unescapes it, so the pattern must reject it: one segment never becomes two
// lookups, and no ES call is made.
func TestEdgeV1CardPercentEncodedCommaInPathIs400(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, es)
	for _, path := range []string{"/api/v1/cards/base1-4%2Cbase1-5", "/api/v1/cards/a,b", "/api/v1/cards/a%2cb"} {
		rec := get(t, s, path)
		assertV1Error(t, rec, http.StatusBadRequest, "invalid_param", "id")
	}
	if n := es.callCount(); n != 0 {
		t.Errorf("%d ES calls for rejected ids, want 0", n)
	}
}

// In the query string a %2C is a separator like any comma (the query is
// unescaped before the split); the body sent to ES must carry the split ids,
// each once, never a single id containing a comma.
func TestEdgeV1CardsPercentEncodedCommaIsASeparator(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"), v1Card("ex10-?", "Unown"))
	s, _ := newTestServer(t, es)
	rec := get(t, s, "/api/v1/cards?ids=base1-4%2Cex10-%3F%2c%20base1-4%20")
	assertV1OK(t, rec)
	bodies := rawMgetBody(t, es.mgets())
	if len(bodies) != 1 || bodies[0] != `{"ids":["base1-4","ex10-?"]}` {
		t.Errorf("_mget bodies = %q, want exactly one with the two split ids", bodies)
	}
}

// The id pattern's upper bound end to end: 128 characters is served, 129 is a
// 400, and the 128-char id reaches ES in the body, never in the URL path.
func TestEdgeV1Card128And129CharIDs(t *testing.T) {
	id128 := "a" + strings.Repeat("9", 127)
	card := v1Card(id128, "Long")
	es := newFakeCards(t, card)
	s, _ := newTestServer(t, es)

	assertV1OK(t, get(t, s, "/api/v1/cards/"+id128))
	assertV1OK(t, get(t, s, "/api/v1/cards?ids="+id128))
	assertV1Error(t, get(t, s, "/api/v1/cards/"+id128+"9"), http.StatusBadRequest, "invalid_param", "id")
	assertV1Error(t, get(t, s, "/api/v1/cards?ids="+id128+"9"), http.StatusBadRequest, "invalid_param", "ids")
	es.mu.Lock()
	defer es.mu.Unlock()
	for _, c := range es.calls {
		if strings.Contains(c.Path+"?"+c.RawQuery, id128) {
			t.Errorf("id reached the ES URL: %s?%s", c.Path, c.RawQuery)
		}
	}
}

// 100 distinct ids with duplicates that collapse to 100 is fine; the 101st
// distinct id is the 400, even when duplicates pad the list and sit in front.
func TestEdgeV1CardsLimitCountsDistinctIDs(t *testing.T) {
	es := newFakeCards(t)
	s, _ := newTestServer(t, es)
	var ids []string
	for i := range 100 {
		ids = append(ids, "z-"+strconv.Itoa(i))
	}
	padded := strings.Join(append(append([]string{"z-0", "z-0", "z-99"}, ids...), "z-50", "z-99"), ",")
	rec := get(t, s, "/api/v1/cards?ids="+padded)
	assertV1OK(t, rec)
	var resp struct {
		Data    []tcg.Card `json:"data"`
		Missing []string   `json:"missing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// First occurrence wins: z-0, z-99 come first, then the rest in order.
	if len(resp.Missing) != 100 || resp.Missing[0] != "z-0" || resp.Missing[1] != "z-99" || resp.Missing[2] != "z-1" {
		t.Errorf("missing = %v (len %d), want 100 ids in first-occurrence order starting z-0 z-99 z-1", resp.Missing, len(resp.Missing))
	}
	assertV1Error(t, get(t, s, "/api/v1/cards?ids="+padded+",z-100"), http.StatusBadRequest, "invalid_param", "ids")
}

// Whitespace-only and empty segments inside ids= are a 400 on field ids and
// never reach the cluster (a batch must not silently shrink into "missing").
func TestEdgeV1CardsBlankSegmentsAre400(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, es)
	for _, q := range []string{"base1-4,%20,base1-5", "base1-4,%09", "%20", "base1-4,,base1-5", ",", "base1-4%2C", "ids=x&ids="} {
		path := "/api/v1/cards?ids=" + q
		if strings.HasPrefix(q, "ids=") {
			path = "/api/v1/cards?ids=&" + q
		}
		assertV1Error(t, get(t, s, path), http.StatusBadRequest, "invalid_param", "ids")
	}
	if n := es.callCount(); n != 0 {
		t.Errorf("%d ES calls for blank-segment lists, want 0", n)
	}
}

// ES owns the doc order and shape: the answer must follow the REQUEST order
// even when _mget replies out of order, with a found:false doc, a doc whose
// error is the JSON literal null (healthy) and an extra unrequested doc.
func TestEdgeV1CardsFollowRequestOrderWhenESReordersDocs(t *testing.T) {
	a, c := v1Card("base1-1", "A"), v1Card("base1-3", "C")
	src := func(card tcg.Card) string { return string(indexedSource(t, card)) }
	reply := `{"docs":[` +
		`{"_index":"cards","_id":"base1-3","found":true,"error":null,"_source":` + src(c) + `},` +
		`{"_index":"cards","_id":"base1-9","found":true,"_source":` + src(v1Card("base1-9", "Unrequested")) + `},` +
		`{"_index":"cards","_id":"base1-2","found":false},` +
		`{"_index":"cards","_id":"base1-1","found":true,"_source":` + src(a) + `}]}`
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			return esResponse(200, seedMetaBody), nil
		}
		return esResponse(200, reply), nil
	})
	s, _ := newTestServer(t, rt)
	rec := get(t, s, "/api/v1/cards?ids=base1-1,base1-2,base1-3")
	assertV1OK(t, rec)
	var resp struct {
		Data    []tcg.Card `json:"data"`
		Missing []string   `json:"missing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 2 || resp.Data[0].ID != "base1-1" || resp.Data[1].ID != "base1-3" {
		t.Errorf("data ids = %+v, want [base1-1 base1-3] in request order and no unrequested doc", resp.Data)
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != "base1-2" {
		t.Errorf("missing = %v, want [base1-2]", resp.Missing)
	}
}

// One failing document among healthy ones fails the request (503) with no
// partial data, and the cluster's own error text never reaches the client.
func TestEdgeV1MgetDocErrorIs503WithoutLeakingClusterText(t *testing.T) {
	good := string(indexedSource(t, v1Card("base1-1", "A")))
	reply := `{"docs":[{"_index":"cards","_id":"base1-1","found":true,"_source":` + good + `},` +
		`{"_index":"cards","_id":"base1-2","error":{"type":"` + edgeSentinel + `","reason":"` + edgeSentinel + `"}}]}`
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/_mapping") {
			return esResponse(200, seedMetaBody), nil
		}
		return esResponse(200, reply), nil
	})
	s, logBuf := newTestServer(t, rt)
	for _, path := range []string{"/api/v1/cards?ids=base1-1,base1-2", "/api/v1/cards/base1-2"} {
		rec := get(t, s, path)
		assertV1Error(t, rec, http.StatusServiceUnavailable, "es_unavailable", "")
		if strings.Contains(rec.Body.String(), edgeSentinel) || strings.Contains(fmt.Sprint(rec.Header()), edgeSentinel) {
			t.Errorf("%s leaks cluster text to the client: %s", path, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "" {
			t.Errorf("%s: error carries Cache-Control %q", path, rec.Header().Get("Cache-Control"))
		}
	}
	if !strings.Contains(logBuf.String(), edgeSentinel) {
		t.Error("the cause should reach the query log")
	}
	// A whole-request HTTP error body is not echoed either.
	rt500 := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return esResponse(500, `{"error":"`+edgeSentinel+`"}`), nil
	})
	s2, _ := newTestServer(t, rt500)
	rec := get(t, s2, "/api/v1/cards?ids=base1-1")
	assertV1Error(t, rec, http.StatusServiceUnavailable, "es_unavailable", "")
	if strings.Contains(rec.Body.String(), edgeSentinel) {
		t.Errorf("500 body leaked: %s", rec.Body.String())
	}
}

// "Exactly as indexed": a card whose stored fields include zero values (hp 0 as
// a pointer, retreat_cost 0, nil slices) and markup/unicode text must come back
// byte-identical to the _source ES holds, inside data, in both routes.
func TestEdgeV1DataIsByteIdenticalToIndexedSourceWithZeroValues(t *testing.T) {
	zero := 0
	card := tcg.Card{
		ID: "edge-0", Name: "Zero <Mon> &   Pokémon \U0001F525", Supertype: "Pokémon",
		HP: &zero, RetreatCost: 0, Number: "0", SetID: "edge", SetName: "Edge", SetSeries: "Edge",
		SetTotal: 0, ReleaseDate: "1999-01-09", ImageSmall: "s", ImageLarge: "l",
		Attacks: []tcg.Attack{{Name: "Nil", ConvertedCost: 0, Damage: "", DamageValue: 0}},
	}
	want := indexedSource(t, card)
	if !bytes.Contains(want, []byte(`"hp":0`)) {
		t.Fatalf("fixture lost its hp:0: %s", want)
	}
	es := newFakeCards(t, card)
	s, _ := newTestServer(t, es)

	var single struct {
		Data json.RawMessage `json:"data"`
	}
	rec := get(t, s, "/api/v1/cards/edge-0")
	assertV1OK(t, rec)
	if err := json.Unmarshal(rec.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(single.Data, want) {
		t.Errorf("single data\n got  %s\n want %s", single.Data, want)
	}

	var batch struct {
		Data []json.RawMessage `json:"data"`
	}
	rec = get(t, s, "/api/v1/cards?ids=edge-0")
	assertV1OK(t, rec)
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Data) != 1 || !bytes.Equal(batch.Data[0], want) {
		t.Errorf("batch data %s, want [%s]", batch.Data, want)
	}
}

// Validators: the tag is a pure function of the body (a different request id
// must not change it), differs when the card differs, never rides an error,
// and If-None-Match: *, a tag list and a gzip request all revalidate to 304
// with the request id header intact.
func TestEdgeV1ETagStabilityAndConditionalForms(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"), v1Card("base1-5", "Other"))
	s, _ := newTestServer(t, es)

	first := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"X-Request-Id": "trace-edge-0001"})
	second := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"X-Request-Id": "trace-edge-0002"})
	assertV1OK(t, first)
	if first.Header().Get("X-Request-Id") != "trace-edge-0001" || second.Header().Get("X-Request-Id") != "trace-edge-0002" {
		t.Errorf("X-Request-Id not echoed: %q %q", first.Header().Get("X-Request-Id"), second.Header().Get("X-Request-Id"))
	}
	if first.Header().Get("ETag") != second.Header().Get("ETag") || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Error("the same card under two request ids must give the same body and ETag")
	}
	if strings.Contains(first.Body.String(), "trace-edge-0001") {
		t.Error("request id leaked into the v1 200 body")
	}
	other := get(t, s, "/api/v1/cards/base1-5")
	if other.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("two different cards share an ETag")
	}

	tag := first.Header().Get("ETag")
	for _, inm := range []string{"*", `"nope", ` + tag, tag, strings.TrimPrefix(tag, "W/"), `W/"nope",*`} {
		rec := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"If-None-Match": inm, "X-Request-Id": "trace-edge-0003"})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != tag {
			t.Errorf("If-None-Match %q: status %d, %d body bytes, ETag %q; want a bare 304 with %s", inm, rec.Code, rec.Body.Len(), rec.Header().Get("ETag"), tag)
		}
		if rec.Header().Get("X-Request-Id") != "trace-edge-0003" {
			t.Errorf("304 lost X-Request-Id: %q", rec.Header().Get("X-Request-Id"))
		}
	}
	for _, inm := range []string{`"nope"`, `W/"0000000000000000"`, "", `W/`, tag + "x"} {
		rec := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"If-None-Match": inm})
		if rec.Code != http.StatusOK {
			t.Errorf("If-None-Match %q: status %d, want 200", inm, rec.Code)
		}
	}
}

// An If-None-Match: * must not turn an error into a 304: 404, 400 and 503
// stay full envelopes with no validator.
func TestEdgeV1ErrorsStayErrorsUnderConditionalRequests(t *testing.T) {
	es := newFakeCards(t)
	s, _ := newTestServer(t, es)
	hdr := map[string]string{"If-None-Match": "*"}
	for path, want := range map[string]int{
		"/api/v1/cards/nope-1":    http.StatusNotFound,
		"/api/v1/cards/-bad":      http.StatusBadRequest,
		"/api/v1/cards":           http.StatusBadRequest,
		"/api/v1/cards/":          http.StatusBadRequest,
		"/api/v1/cards?ids=a/b":   http.StatusBadRequest,
		"/api/v1/cards/a%2Fb":     http.StatusBadRequest,
		"/api/v1/cards/nope-1%20": http.StatusBadRequest,
	} {
		rec := getWith(t, s, path, hdr)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", path, rec.Code, want, rec.Body.String())
			continue
		}
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "" {
			t.Errorf("%s: error carries ETag %q / Cache-Control %q", path, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Body.String(), `"request_id"`) {
			t.Errorf("%s: error body has no request_id: %s", path, rec.Body.String())
		}
	}
	down := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	s2, _ := newTestServer(t, down)
	rec := getWith(t, s2, "/api/v1/cards/base1-4", hdr)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("ETag") != "" {
		t.Errorf("ES down: status %d ETag %q, want 503 and none", rec.Code, rec.Header().Get("ETag"))
	}
}

// Routing around the v1 handlers: deeper paths, the empty id, a wrong method
// and a prefix lookalike must not reach ES, must not be a 200 and must not mint
// new expvar keys beyond the fixed labels.
func TestEdgeV1RoutingEdgesStayBounded(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"))
	s, _ := newTestServer(t, es)

	for _, p := range []string{"/api/v1/cards/anything/else", "/api/v1/cards/anything/else/", "/api/v1/cardsx", "/api/v1/cards/a/b/c/d"} {
		if rec := get(t, s, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	for path, want := range map[string]string{
		"/api/v1/cards/anything/else": "v1_card", "/api/v1/cards/": "v1_card", "/api/v1/cards": "v1_cards",
		"/api/v1/cardsx": "static", "/api/v1/card": "static", "/api/v1/cards/%": "v1_card",
	} {
		if got := routeLabel(path); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", path, got, want)
		}
	}
	assertV1Error(t, get(t, s, "/api/v1/cards/"), http.StatusBadRequest, "invalid_param", "id")

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/v1/cards/base1-4", nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code >= 500 {
			t.Errorf("%s v1 card = %d, want a client error", m, rec.Code)
		}
	}
	if n := es.callCount(); n != 0 {
		t.Errorf("%d ES calls from routing-edge requests, want 0", n)
	}

	// A crawler walking ids may not grow the expvar map.
	keys := func() int {
		n := 0
		httpStats.Do(func(expvar.KeyValue) { n++ })
		return n
	}
	before := keys()
	for i := range 200 {
		get(t, s, "/api/v1/cards/walk-"+strconv.Itoa(i))
		get(t, s, "/api/v1/cards/walk-"+strconv.Itoa(i)+"/x")
	}
	if after := keys(); after > before+3 {
		t.Errorf("expvar keys grew %d -> %d across 400 id probes", before, after)
	}
}

// Parallel callers (including the first-request seed read) must agree with each
// other and be race-free; run with -race.
func TestEdgeV1ConcurrentRequestsAgree(t *testing.T) {
	es := newFakeCards(t, v1Card("base1-4", "Charizard"), v1Card("ex10-?", "Unown"))
	s := newTestServerLogging(t, es, io.Discard)
	var wg sync.WaitGroup
	tags := make([]string, 40)
	for i := range tags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "/api/v1/cards?ids=base1-4,ex10-%3F,nope-1"
			if i%2 == 1 {
				path = "/api/v1/cards/base1-4"
			}
			req := httptest.NewRequest("GET", path, nil)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				tags[i] = rec.Header().Get("ETag")
			}
		}()
	}
	wg.Wait()
	for i, tag := range tags {
		if tag == "" || tag != tags[i%2] {
			t.Fatalf("request %d: ETag %q, want %q", i, tag, tags[i%2])
		}
	}
}

// F-P1-R1-01 guard, stricter than the implementer's: v1 code must not import
// net/url (the only way to build an escaped id path) and must not name any
// single-document ES API or a /cards/ path literal, in any v1 source file.
func TestEdgeV1SourceNeverBuildsIDPaths(t *testing.T) {
	for _, file := range []string{"v1.go", "../search/v1.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			switch imp.Path.Value {
			case `"net/url"`, `"path"`, `"path/filepath"`:
				t.Errorf("%s imports %s: v1 code has no reason to build a path", file, imp.Path.Value)
			}
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"PathEscape", "QueryEscape", "Explain", "GetSource", "Exists(", `"/cards/`, `"cards/`, "Sprintf(\"/", "WithDocumentID", "esapi.GetRequest", "esapi.ExplainRequest"} {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s contains %q", file, bad)
			}
		}
	}
}
