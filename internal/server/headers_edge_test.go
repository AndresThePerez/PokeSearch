package server

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/web"
)

var generatedRequestID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Whatever the two inbound headers hold, the id the response carries is one
// that is safe in a header and a log line, the body of every kind of error
// reports the same id, and the edge's ray id is used whenever it is valid and
// the client's id only when it is not.
func TestRequestIDFromEveryHeaderCombination(t *testing.T) {
	long64 := strings.Repeat("a", maxRequestIDLen)
	long65 := strings.Repeat("a", maxRequestIDLen+1)
	const ray, client = "8f0a1b2c3d4e5f60-IAD", "client-1"
	values := []struct {
		name, v string
		valid   bool
	}{
		{"absent", "\x00absent", false},
		{"empty", "", false},
		{"valid", "", true},
		{"exactly the limit", long64, true},
		{"one past the limit", long65, false},
		{"with a space", "has space", false},
		{"with a control character", "ab\x01cd", false},
		{"with a newline", "ab\ncd", false},
		{"non-ASCII", "r\u00e9seau", false},
		{"with a quote", `a"b`, false},
		{"comma-joined pair", "one,two", false},
		{"slash", "a/b", false},
		{"percent", "a%0ab", false},
	}
	set := func(h http.Header, name, v string, valid bool, validValue string) {
		switch {
		case v == "\x00absent":
		case valid && v == "":
			h.Set(name, validValue)
		default:
			h.Set(name, v)
		}
	}
	for _, cf := range values {
		for _, xr := range values {
			t.Run(cf.name+"/"+xr.name, func(t *testing.T) {
				s, _ := newTestServer(t, noESCalls(t))
				for _, req := range []*http.Request{
					httptest.NewRequest("GET", "/api/search?sort=bogus", nil), // 400
					httptest.NewRequest("GET", "/api/nope", nil),              // 404
					httptest.NewRequest("POST", "/api/search", nil),           // 405
				} {
					set(req.Header, "Cf-Ray", cf.v, cf.valid, ray)
					set(req.Header, "X-Request-Id", xr.v, xr.valid, client)
					rec := httptest.NewRecorder()
					s.ServeHTTP(rec, req)
					got := rec.Header().Get("X-Request-Id")
					if len(rec.Header().Values("X-Request-Id")) != 1 {
						t.Fatalf("X-Request-Id values: %q", rec.Header().Values("X-Request-Id"))
					}
					body := decodeError(t, rec)
					if body.RequestID != got {
						t.Errorf("%s %s: request_id %q, header %q", req.Method, req.URL.Path, body.RequestID, got)
					}
					cfWant, xrWant := cf.v, xr.v
					if cf.valid && cf.v == "" {
						cfWant = ray
					}
					if xr.valid && xr.v == "" {
						xrWant = client
					}
					switch {
					case cf.valid && got != cfWant:
						t.Errorf("%s: id %q, want the ray id %q", req.URL.Path, got, cfWant)
					case !cf.valid && xr.valid && got != xrWant:
						t.Errorf("%s: id %q, want the client's id %q", req.URL.Path, got, xrWant)
					case !cf.valid && !xr.valid && !generatedRequestID.MatchString(got):
						t.Errorf("%s: id %q, want a generated 16-hex id", req.URL.Path, got)
					}
					if acceptRequestID(got) == "" {
						t.Errorf("%s: echoed id %q would not pass the validation it came through", req.URL.Path, got)
					}
				}
			})
		}
	}
}

// A header sent twice is one request id, the first value, and a second value
// can neither join it nor override a valid Cf-Ray.
func TestRequestIDWithRepeatedHeaders(t *testing.T) {
	s, _ := newTestServer(t, noESCalls(t))
	req := httptest.NewRequest("GET", "/api/nope", nil)
	req.Header["X-Request-Id"] = []string{"first-1", "second-2"}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "first-1" {
		t.Errorf("id %q, want first-1", got)
	}

	req = httptest.NewRequest("GET", "/api/nope", nil)
	req.Header["Cf-Ray"] = []string{"ray-1", "ray-2"}
	req.Header.Set("X-Request-Id", "client-1")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "ray-1" {
		t.Errorf("id %q, want ray-1", got)
	}
}

// The 401 carries the challenge on every refusal and on nothing else: not on a
// success, not on an anonymous read, not on any route outside /api/v1/.
func TestWWWAuthenticateOnlyOnTheKeyRefusal(t *testing.T) {
	good, wrong := throwawayKey(t), throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	s, _ := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))
	const challenge = `ApiKey realm="pokesearch", header="X-Api-Key"`

	two := httptest.NewRequest("GET", "/api/v1/sets", nil)
	two.Header["X-Api-Key"] = []string{good, good}
	empty := httptest.NewRequest("GET", "/api/v1/sets", nil)
	empty.Header["X-Api-Key"] = []string{""}
	padded := httptest.NewRequest("GET", "/api/v1/sets", nil)
	padded.Header.Set("X-Api-Key", " "+good+" ")
	lower := httptest.NewRequest("GET", "/api/v1/sets", nil)
	lower.Header.Set("X-Api-Key", strings.ToUpper(good))
	for name, req := range map[string]*http.Request{"two keys": two, "empty key": empty, "padded key": padded, "wrong-case key": lower} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != challenge {
			t.Errorf("%s: status %d WWW-Authenticate %q, want 401 with the challenge", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		if values := rec.Header().Values("WWW-Authenticate"); len(values) != 1 {
			t.Errorf("%s: %d WWW-Authenticate values, want 1", name, len(values))
		}
	}

	for _, tc := range []struct{ path, key string }{
		{"/api/v1/cards/base1-4", good},
		{"/api/v1/cards/base1-4", ""},
		{"/api/v1/nope", good},
		{"/api/search?sort=bogus", wrong},
		{"/api/v1", wrong},
		{"/healthz", wrong},
		{"/", wrong},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		if tc.key != "" {
			req.Header.Set("X-Api-Key", tc.key)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if got := rec.Header().Get("WWW-Authenticate"); got != "" {
			t.Errorf("%s key=%v: status %d carries WWW-Authenticate %q", tc.path, tc.key != "", rec.Code, got)
		}
	}
}

// The comparison If-None-Match needs: weak on both sides, case-sensitive in
// the W/ prefix and the tag itself, whole tags only, and a header of any
// length answered without matching what it does not name.
func TestETagMatchesTruthTable(t *testing.T) {
	for _, tc := range []struct {
		name, inm, tag string
		want           bool
	}{
		{"star", `*`, `W/"abc"`, true},
		{"star among tags", `"x", *`, `"abc"`, true},
		{"star with spaces", `  *  `, `"abc"`, true},
		{"empty header", ``, `W/"abc"`, false},
		{"blank header", `   `, `W/"abc"`, false},
		{"only commas", `,,,`, `W/"abc"`, false},
		{"list with spaces", `"x" , W/"abc" , "y"`, `W/"abc"`, true},
		{"list without spaces", `"x",W/"abc","y"`, `W/"abc"`, true},
		{"strong candidate, weak stored", `"abc"`, `W/"abc"`, true},
		{"weak candidate, strong stored", `W/"abc"`, `"abc"`, true},
		{"candidate is a prefix of the stored tag", `W/"abc"`, `W/"abcd"`, false},
		{"candidate is a suffix of the stored tag", `W/"bcd"`, `W/"abcd"`, false},
		{"stored tag is a prefix of the candidate", `W/"abcd"`, `W/"abc"`, false},
		{"unquoted candidate", `abc`, `W/"abc"`, false},
		{"unquoted candidate, weak prefix", `W/abc`, `W/"abc"`, false},
		{"lower-case weak prefix is no weak prefix", `w/"abc"`, `W/"abc"`, false},
		{"different case in the tag", `W/"ABC"`, `W/"abc"`, false},
		{"weak prefix only", `W/`, `W/"abc"`, false},
		{"double weak prefix", `W/W/"abc"`, `W/"abc"`, false},
		{"empty candidate between commas", `"x",,"y"`, `W/"abc"`, false},
		{"empty header and a stored empty tag", ``, ``, false},
		{"blank candidate and a stored empty tag", ` , `, ``, false},
	} {
		if got := etagMatches(tc.inm, tc.tag); got != tc.want {
			t.Errorf("%s: etagMatches(%q, %q) = %v, want %v", tc.name, tc.inm, tc.tag, got, tc.want)
		}
	}

	var b strings.Builder
	for i := range 10000 {
		fmt.Fprintf(&b, `W/"%016x", `, i)
	}
	big := b.String()
	start := time.Now()
	if etagMatches(big+`W/"zz"`, `"nope"`) {
		t.Error("a 10,000-entry list matched a tag it does not hold")
	}
	if !etagMatches(big+`W/"zz"`, `"zz"`) {
		t.Error("a 10,000-entry list lost its last tag")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("10,000-entry If-None-Match took %v", time.Since(start))
	}
}

func embeddedAssetServer(t *testing.T) *Server {
	t.Helper()
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://fake-es:9200"},
		Transport: staticRT(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixed := func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }
	return New(es, web.Files, io.Discard, fixed)
}

func embeddedAssetPaths(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := fs.WalkDir(web.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if p == "index.html" {
			p = ""
		}
		paths = append(paths, "/"+p)
		return nil
	})
	if err != nil || len(paths) < 3 {
		t.Fatalf("embedded assets: %v, %d files", err, len(paths))
	}
	return paths
}

// Every embedded file, in both encodings: a weak tag that is never the same
// as another file's, whose own value gives a bare 304 under either encoding
// and another file's gives the file.
func TestEveryEmbeddedAssetRevalidatesOnItsOwnTagOnly(t *testing.T) {
	s := embeddedAssetServer(t)
	paths := embeddedAssetPaths(t)
	tags := map[string]string{}
	weak := regexp.MustCompile(`^W/"[0-9a-f]{16}"$`)
	for _, p := range paths {
		for _, enc := range []string{"", "gzip"} {
			rec := getWith(t, s, p, map[string]string{"Accept-Encoding": enc})
			tag := rec.Header().Get("ETag")
			if rec.Code != 200 || !weak.MatchString(tag) {
				t.Fatalf("%s (%q): status %d ETag %q, want 200 and a weak 16-hex tag", p, enc, rec.Code, tag)
			}
			if prev, ok := tags[p]; ok && prev != tag {
				t.Errorf("%s: the tag changed between encodings: %s then %s", p, prev, tag)
			}
			tags[p] = tag
		}
	}
	for _, p := range paths {
		for _, other := range paths {
			if p != other && tags[p] == tags[other] {
				t.Logf("%s and %s share content and therefore a tag", p, other)
			}
		}
		for _, enc := range []string{"", "gzip"} {
			rec := getWith(t, s, p, map[string]string{"Accept-Encoding": enc, "If-None-Match": tags[p]})
			if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != tags[p] {
				t.Errorf("%s (%q) on its own tag: status %d, %d bytes, ETag %q", p, enc, rec.Code, rec.Body.Len(), rec.Header().Get("ETag"))
			}
			if rec.Header().Get("Cache-Control") == "" {
				t.Errorf("%s (%q): the 304 carries no Cache-Control", p, enc)
			}
			stale := getWith(t, s, p, map[string]string{"Accept-Encoding": enc, "If-None-Match": `W/"0000000000000000"`})
			if stale.Code != 200 || stale.Body.Len() == 0 {
				t.Errorf("%s (%q) on a stale tag: status %d, %d bytes", p, enc, stale.Code, stale.Body.Len())
			}
			head := httptest.NewRecorder()
			req := httptest.NewRequest("HEAD", p, nil)
			req.Header.Set("If-None-Match", tags[p])
			s.ServeHTTP(head, req)
			if head.Code != http.StatusNotModified {
				t.Errorf("HEAD %s on its own tag: status %d, want 304", p, head.Code)
			}
		}
		// A file never revalidates on another file's tag unless the two have
		// identical content.
		for _, other := range paths {
			if p == other || tags[p] == tags[other] {
				continue
			}
			rec := getWith(t, s, p, map[string]string{"If-None-Match": tags[other]})
			if rec.Code != 200 {
				t.Errorf("%s revalidated on %s's tag: status %d", p, other, rec.Code)
			}
		}
	}
}

// A validator is not a date: If-None-Match decides, and a file with no
// Last-Modified cannot be revalidated by If-Modified-Since into a 304 that
// would skip the tag check.
func TestStaticIfModifiedSinceDoesNotOverrideTheTag(t *testing.T) {
	s := embeddedAssetServer(t)
	future := time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)
	tag := get(t, s, "/styles.css").Header().Get("ETag")

	stale := getWith(t, s, "/styles.css", map[string]string{"If-None-Match": `W/"0000000000000000"`, "If-Modified-Since": future})
	if stale.Code != 200 || stale.Body.Len() == 0 {
		t.Errorf("stale tag with a future If-Modified-Since: status %d, %d bytes, want the file", stale.Code, stale.Body.Len())
	}
	fresh := getWith(t, s, "/styles.css", map[string]string{"If-None-Match": tag, "If-Modified-Since": "Mon, 02 Jan 2006 15:04:05 GMT"})
	if fresh.Code != http.StatusNotModified {
		t.Errorf("own tag with an old If-Modified-Since: status %d, want 304", fresh.Code)
	}
}

// The v1 routes keep matching the tags they send, as before the static tag
// became weak, with and without the weak prefix.
func TestV1ETagsStillRevalidate(t *testing.T) {
	s, _ := newTestServer(t, newFakeCards(t, v1Card("base1-4", "Charizard")))
	first := get(t, s, "/api/v1/cards/base1-4")
	tag := first.Header().Get("ETag")
	if first.Code != 200 || !strings.HasPrefix(tag, `W/"`) {
		t.Fatalf("status %d ETag %q, want 200 and a weak tag", first.Code, tag)
	}
	for _, inm := range []string{tag, strings.TrimPrefix(tag, "W/"), `"zzz", ` + tag, "*"} {
		rec := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"If-None-Match": inm})
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: status %d with %d bytes, want a bare 304", inm, rec.Code, rec.Body.Len())
		}
	}
	rec := getWith(t, s, "/api/v1/cards/base1-4", map[string]string{"If-None-Match": `W/"other"`})
	if rec.Code != 200 {
		t.Errorf("a different tag: status %d, want 200", rec.Code)
	}
}
