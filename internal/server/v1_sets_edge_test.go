package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// Every key in this file is built at run time from throwawayKey; nothing here
// is a key literal and no assertion message prints one.

// getKeyedHeader sends the X-Api-Key header with the given raw values, which
// httptest does not canonicalise or trim.
func getKeyedHeader(s *Server, path string, values ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header["X-Api-Key"] = values
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// The env list is a comma list: members are trimmed, empty members are not
// keys, a prefix of a key is not the key, characters like = and : are part of
// a key, and a duplicated member is still just that key.
func TestEdgeAPIKeyEnvListSemantics(t *testing.T) {
	plain := throwawayKey(t)
	odd := "k=" + throwawayKey(t) + ":v==" // = and : inside a key
	dup := throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", ",, "+plain+" , ,\t"+odd+" ,"+dup+","+dup+", ")
	s, _ := newTestServer(t, &fakeSets{buckets: sampleSets()})

	for name, k := range map[string]string{"plain": plain, "equals-colon": odd, "duplicated": dup} {
		rec := getKeyedHeader(s, "/api/v1/sets", k)
		if rec.Code != http.StatusOK {
			t.Errorf("%s key: status %d, want 200", name, rec.Code)
		}
	}
	for name, k := range map[string]string{
		"empty":                 "",
		"spaces only":           "   ",
		"prefix of a key":       plain[:len(plain)/2],
		"first char only":       plain[:1],
		"key without suffix":    odd[:len(odd)-1],
		"two keys comma-joined": plain + "," + dup,
		"two keys space-joined": plain + " " + dup,
		"key then comma":        plain + ",",
		"comma then key":        "," + plain,
		"nul-suffixed":          plain + "\x00",
	} {
		rec := getKeyedHeader(s, "/api/v1/sets", k)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
}

// An unset, empty or separator-only env means no keys exist: every presented
// key is 401 and the anonymous demo is untouched.
func TestEdgeAPIKeyEmptyEnvIssuesNoKeys(t *testing.T) {
	for _, env := range []string{"", ",", " , ,, ", "\t"} {
		t.Setenv("POKESEARCH_API_KEYS", env)
		s, logBuf := newTestServer(t, &fakeSets{buckets: sampleSets()})
		if rec := get(t, s, "/api/v1/sets"); rec.Code != http.StatusOK {
			t.Errorf("env %q: anonymous status %d, want 200", env, rec.Code)
		}
		for _, k := range []string{"", " ", ",", throwawayKey(t)} {
			if rec := getKeyedHeader(s, "/api/v1/sets", k); rec.Code != http.StatusUnauthorized {
				t.Errorf("env %q, presented %q: status %d, want 401", env, k, rec.Code)
			}
		}
		for _, line := range accessLines(t, logBuf) {
			if line["status"] == float64(200) && line["client"] != nil {
				t.Errorf("env %q: anonymous line carries client %v", env, line["client"])
			}
		}
	}
}

// The check keys on the decoded path the handler will route; no spelling of a
// v1 path may reach a v1 handler with a wrong key and get a 200.
func TestEdgeAPIKeyWrongKeyNeverServedOnPathVariants(t *testing.T) {
	t.Setenv("POKESEARCH_API_KEYS", throwawayKey(t))
	es := &fakeSets{buckets: sampleSets()}
	s, _ := newTestServer(t, es)
	wrong := throwawayKey(t)

	for _, p := range []string{
		"/api/v1/sets", "/api/v1/sets?x=1", "/api/v1/%73ets", "/api/v1/./sets", "//api/v1/sets",
		"/api/v1/sets/../sets", "/api/v1/x/../sets", "/api/%76%31/sets", "/api/v1%2Fsets", "/api//v1/sets",
		"/API/V1/sets", "/api/v1/sets/", "/api/v1/sets;a=b",
	} {
		rec := getKeyedHeader(s, p, wrong)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s with a wrong key = 200", p)
		}
	}
	if n := es.searchCount(); n != 0 {
		t.Errorf("%d ES searches for wrong-key requests, want 0", n)
	}
}

// A very long header value must be rejected as a plain 401: no panic, no echo
// of the value, no growth of the log line. A very long configured key must
// still match itself (the digest comparison has no length cap).
func TestEdgeAPIKeyHugeValues(t *testing.T) {
	long := throwawayKey(t) + strings.Repeat("A", 200<<10)
	t.Setenv("POKESEARCH_API_KEYS", long)
	s, logBuf := newTestServer(t, &fakeSets{buckets: sampleSets()})

	if rec := getKeyedHeader(s, "/api/v1/sets", long); rec.Code != http.StatusOK {
		t.Errorf("a 200 KiB configured key does not match itself: status %d", rec.Code)
	}
	logBuf.Reset()
	huge := strings.Repeat("Z", 512<<10)
	rec := getKeyedHeader(s, "/api/v1/sets", huge)
	assertV1Error(t, rec, http.StatusUnauthorized, "unauthorized", "")
	if strings.Contains(rec.Body.String(), huge[:64]) || strings.Contains(logBuf.String(), huge[:64]) {
		t.Error("the presented value is echoed into the response or the log")
	}
	if logBuf.Len() > 4<<10 {
		t.Errorf("one rejected request wrote %d log bytes", logBuf.Len())
	}
}

// The 401 uses the ADR 7 envelope exactly: error.code and error.message, the
// request id equal to the header, no field member (it names no parameter), and
// the same security headers as every other answer.
func TestEdgeAPIKey401EnvelopeShape(t *testing.T) {
	good := throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	s, _ := newTestServer(t, &fakeSets{buckets: sampleSets()})
	rec := getKeyedHeader(s, "/api/v1/sets", throwawayKey(t))
	assertV1Error(t, rec, http.StatusUnauthorized, "unauthorized", "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw["error"] == nil || raw["request_id"] == nil {
		t.Errorf("envelope members = %v, want exactly error and request_id", rec.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(raw["error"], &detail); err != nil {
		t.Fatal(err)
	}
	if _, has := detail["field"]; has || len(detail) != 2 {
		t.Errorf("error members = %v, want code and message only", detail)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the 401 lacks the security headers the other answers carry")
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("the 401 sets a cookie")
	}
}

// A keyed request is logged as keyed whatever the outcome: a 304, an ES
// failure and an unknown v1 path all carry client "keyed" and the status the
// client saw, and the label is the only trace of the key.
func TestEdgeAPIKeyKeyedLabelSurvivesEveryOutcome(t *testing.T) {
	good := throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	es := &fakeSets{buckets: sampleSets()}
	s, logBuf := newTestServer(t, es)

	first := getKeyedHeader(s, "/api/v1/sets", good)
	assertV1OK(t, first)
	cond := httptest.NewRequest("GET", "/api/v1/sets", nil)
	cond.Header.Set("X-Api-Key", good)
	cond.Header.Set("If-None-Match", first.Header().Get("ETag"))
	rec304 := httptest.NewRecorder()
	s.ServeHTTP(rec304, cond)

	rec404 := getKeyedHeader(s, "/api/v1/nope", good)

	downES := &fakeSets{fail: true}
	sDown, downLog := newTestServer(t, downES)
	rec503 := getKeyedHeader(sDown, "/api/v1/sets", good)

	for name, c := range map[string]struct {
		got  int
		want int
		log  *bytes.Buffer
	}{
		"304": {rec304.Code, 304, logBuf}, "404": {rec404.Code, 404, logBuf}, "503": {rec503.Code, 503, downLog},
	} {
		if c.got != c.want {
			t.Fatalf("%s: status %d", name, c.got)
		}
		found := false
		for _, l := range accessLines(t, c.log) {
			if l["status"] == float64(c.want) {
				found = true
				if l["client"] != "keyed" {
					t.Errorf("%s access line client = %v, want keyed", name, l["client"])
				}
			}
		}
		if !found {
			t.Errorf("%s: no access line with that status", name)
		}
	}
	if strings.Contains(logBuf.String(), good) || strings.Contains(downLog.String(), good) {
		t.Error("the API key appears in a log")
	}
	if rec304.Body.Len() != 0 {
		t.Error("304 carries a body")
	}
}

// Through a real HTTP server the Go server trims optional whitespace around a
// header value, so a client that sends "  key  " is the keyed client it meant
// to be; and header names are case-insensitive.
func TestEdgeAPIKeyOverRealServerWhitespaceAndCase(t *testing.T) {
	good := throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	s, logBuf := newTestServer(t, &fakeSets{buckets: sampleSets()})
	_ = logBuf
	ts := httptest.NewServer(s)
	defer ts.Close()

	for name, hdr := range map[string][2]string{
		"padded":     {"X-Api-Key", "  " + good + "  "},
		"lower name": {"x-api-key", good},
		"upper name": {"X-API-KEY", good},
	} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/sets", nil)
		req.Header[hdr[0]] = []string{hdr[1]}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", name, resp.StatusCode)
		}
	}
}

// The key must be compared in constant time: the matcher uses
// subtle.ConstantTimeCompare and never an early-exit comparison of the
// presented value (== , bytes.Equal, strings.EqualFold, slices.Contains).
func TestEdgeAPIKeyComparisonIsConstantTime(t *testing.T) {
	src, err := os.ReadFile("v1.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte("subtle.ConstantTimeCompare(")) {
		t.Error("v1.go does not use subtle.ConstantTimeCompare")
	}
	start := bytes.Index(src, []byte("func (k apiKeys) match("))
	end := bytes.Index(src[start:], []byte("\n}\n"))
	if start < 0 || end < 0 {
		t.Fatal("cannot find apiKeys.match")
	}
	body := string(src[start : start+end])
	for _, bad := range []string{"bytes.Equal", "EqualFold", "slices.Contains", "== presented", "presented ==", "strings.Compare", "break", "return true"} {
		if strings.Contains(body, bad) {
			t.Errorf("apiKeys.match contains %q: not a constant-time, exhaustive comparison", bad)
		}
	}
}

// 176 sets, as the real corpus has: past the gzip threshold the body encodes,
// the weak tag is identical to the identity one, and a conditional gzip
// request gets a bare 304. Sets released the same day order by id however ES
// ordered its buckets.
func TestEdgeSetsLargeCatalogGzipETagAndStableOrder(t *testing.T) {
	var buckets []v1SetBucket
	for i := range 176 {
		buckets = append(buckets, v1SetBucket{
			ID: fmt.Sprintf("s%03d", 175-i), Name: fmt.Sprintf("Set %d", i), Series: "Series",
			ReleaseDate: fmt.Sprintf("20%02d-01-01", i/4), Total: 100 + i, Count: 90 + i,
		})
	}
	s, _ := newTestServer(t, &fakeSets{buckets: buckets})
	plain := get(t, s, "/api/v1/sets")
	assertV1OK(t, plain)
	var resp struct {
		Data []v1Set `json:"data"`
	}
	if err := json.Unmarshal(plain.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 176 {
		t.Fatalf("%d sets, want 176", len(resp.Data))
	}
	for i := 1; i < len(resp.Data); i++ {
		a, b := resp.Data[i-1], resp.Data[i]
		if a.ReleaseDate > b.ReleaseDate || (a.ReleaseDate == b.ReleaseDate && a.ID >= b.ID) {
			t.Fatalf("order broken at %d: %s/%s before %s/%s", i, a.ReleaseDate, a.ID, b.ReleaseDate, b.ID)
		}
	}
	if plain.Body.Len() <= minGzipBody {
		t.Fatalf("body %d bytes: the gzip leg would be vacuous", plain.Body.Len())
	}
	zipped := getWith(t, s, "/api/v1/sets", map[string]string{"Accept-Encoding": "gzip"})
	if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatalf("gzip: Content-Encoding %q, ETag %q vs %q", zipped.Header().Get("Content-Encoding"), zipped.Header().Get("ETag"), plain.Header().Get("ETag"))
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(zr)
	if !bytes.Equal(decoded, plain.Body.Bytes()) {
		t.Error("the gzipped sets body does not decode to the identity body")
	}
	nm := getWith(t, s, "/api/v1/sets", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": plain.Header().Get("ETag")})
	if nm.Code != 304 || nm.Body.Len() != 0 {
		t.Errorf("conditional gzip: %d with %d body bytes, want a bare 304", nm.Code, nm.Body.Len())
	}
}

// A bucket whose sample document is missing release_date, or whose top_hits
// came back empty, still yields a complete six-member entry (id and count from
// the bucket; the rest zero values), and the order is deterministic whatever
// order ES returned the buckets in.
func TestEdgeSetsIncompleteSamplesStayWellFormedAndDeterministic(t *testing.T) {
	bucket := func(id string, count int, source string) string {
		hits := `[]`
		if source != "" {
			hits = `[{"_id":"x","_source":` + source + `}]`
		}
		return fmt.Sprintf(`{"key":%q,"doc_count":%d,"identity":{"hits":{"hits":%s}}}`, id, count, hits)
	}
	all := []string{
		bucket("nodate", 3, `{"set_name":"No Date","set_series":"S","set_total":9}`),
		bucket("nohits", 1, ""),
		bucket("b", 5, `{"set_name":"B","set_series":"S","set_total":5,"release_date":"2001-02-03"}`),
		bucket("a", 5, `{"set_name":"A","set_series":"S","set_total":5,"release_date":"2001-02-03"}`),
		bucket("c", 2, `{"set_name":"C","set_series":"S","set_total":5,"release_date":"2000-01-01"}`),
	}
	serve := func(order []int) string {
		var parts []string
		for _, i := range order {
			parts = append(parts, all[i])
		}
		rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/_mapping") {
				return esResponse(200, seedMetaBody), nil
			}
			return esResponse(200, `{"aggregations":{"sets":{"buckets":[`+strings.Join(parts, ",")+`]}}}`), nil
		})
		s, _ := newTestServer(t, rt)
		rec := get(t, s, "/api/v1/sets")
		assertV1OK(t, rec)
		return rec.Body.String()
	}
	want := serve([]int{0, 1, 2, 3, 4})
	for _, order := range [][]int{{4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}, {1, 3, 0, 2, 4}} {
		if got := serve(order); got != want {
			t.Errorf("bucket order %v changed the body:\n got %s\nwant %s", order, got, want)
		}
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(want), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 5 {
		t.Fatalf("%d sets, want 5 (an empty sample must not drop the set)", len(resp.Data))
	}
	for _, e := range resp.Data {
		for _, k := range []string{"id", "name", "series", "total", "release_date", "card_count"} {
			if _, ok := e[k]; !ok {
				t.Errorf("entry %v lacks %q", e, k)
			}
		}
		if _, ok := e["release_date"].(string); !ok {
			t.Errorf("entry %v: release_date is not a string", e)
		}
	}
	// Dated sets keep date-then-id order; undated sets are not interleaved.
	var dated []string
	for _, e := range resp.Data {
		if e["release_date"] != "" {
			dated = append(dated, e["id"].(string))
		}
	}
	if strings.Join(dated, ",") != "c,a,b" {
		t.Errorf("dated order = %v, want c,a,b", dated)
	}
}

// Many cold callers at once (run with -race) agree on one body; afterwards the
// process-lifetime cache answers without another search, and a second Server
// owns its own cache rather than sharing one.
func TestEdgeSetsColdStartConcurrencyAndPerServerCache(t *testing.T) {
	es := &fakeSets{buckets: sampleSets()}
	s := newTestServerLogging(t, es, io.Discard)

	var wg sync.WaitGroup
	bodies := make([]string, 40)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/v1/sets", nil)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			bodies[i] = rec.Body.String()
		}()
	}
	wg.Wait()
	for i, b := range bodies {
		if b != bodies[0] || !strings.Contains(b, `"id":"base1"`) {
			t.Fatalf("caller %d got a different or bad body: %s", i, b)
		}
	}
	warm := es.searchCount()
	if warm < 1 || warm > len(bodies) {
		t.Fatalf("%d searches for a cold start", warm)
	}
	for range 5 {
		get(t, s, "/api/v1/sets")
	}
	if n := es.searchCount(); n != warm {
		t.Errorf("searches grew %d -> %d after the cache was warm", warm, n)
	}

	other := &fakeSets{buckets: sampleSets()[:1]}
	s2 := newTestServerLogging(t, other, io.Discard)
	rec := get(t, s2, "/api/v1/sets")
	if other.searchCount() != 1 || strings.Contains(rec.Body.String(), `"id":"base1"`) {
		t.Errorf("a second Server shares the first one's cache: %d searches, body %s", other.searchCount(), rec.Body.String())
	}
}
