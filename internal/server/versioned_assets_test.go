package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/version"
	"github.com/AndresThePerez/pokesearch/web"
)

// The two references the served document names its assets by, as the markup
// on disk spells them.
const (
	diskStylesheetRef = `href="/styles.css"`
	diskEntryRef      = `src="/js/main.js"`
)

// stampedServer builds a server as a binary stamped with commit would, over
// static, logging to the returned buffer. The stamp is restored when the test
// ends, as /api/meta reads the same variable.
func stampedServer(t *testing.T, commit string, static fs.FS) (*Server, *bytes.Buffer) {
	t.Helper()
	old := version.Commit
	version.Commit = commit
	t.Cleanup(func() { version.Commit = old })
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://fake-es:9200"},
		Transport: staticRT(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	fixed := func() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }
	return New(es, static, &logBuf, fixed), &logBuf
}

// constructionLines are the log lines other than the access lines: what New
// itself logged.
func constructionLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range logLines(t, buf) {
		if m["msg"] != "access" {
			out = append(out, m)
		}
	}
	return out
}

// assertPrefix404 checks the one answer every refused path under the asset
// prefix gets: a 404 a cache must not keep (no-store), with no validator.
func assertPrefix404(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s = %d, want 404", label, rec.Code)
	}
	if cc, tag := rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"); cc != "no-store" || tag != "" {
		t.Errorf("%s: Cache-Control %q, ETag %q on the 404, want no-store and no ETag", label, cc, tag)
	}
}

func diskIndex(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(web.Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func serveMethod(t *testing.T, s *Server, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// The document a stamped build serves names its stylesheet and its entry
// script under the build's commit, and it differs from the markup on disk in
// those two attribute values and nowhere else.
func TestDocumentNamesItsAssetsUnderTheBuildCommit(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<link rel="stylesheet" href="/assets/abc1234/styles.css">`,
		`<script type="module" src="/assets/abc1234/js/main.js"></script>`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("the served document holds %q %d times, want once", want, strings.Count(body, want))
		}
	}
	for _, gone := range []string{diskStylesheetRef, diskEntryRef} {
		if strings.Contains(body, gone) {
			t.Errorf("the served document still names %s", gone)
		}
	}
	want := strings.Replace(diskIndex(t), diskStylesheetRef, `href="/assets/abc1234/styles.css"`, 1)
	want = strings.Replace(want, diskEntryRef, `src="/assets/abc1234/js/main.js"`, 1)
	if body != want {
		t.Error("the served document differs from the markup on disk somewhere other than the two asset references")
	}
	// What must not move: the link-preview tags, the skip link and the home link.
	for _, kept := range []string{
		`<meta property="og:url" content="https://pokesearch.andrestheperez.com">`,
		`<meta property="og:image" content="https://raw.githubusercontent.com/AndresThePerez/PokeSearch/master/docs/media/social.png">`,
		`<a class="skip-link" href="#results-grid">Skip to results</a>`,
		`<a class="brand" href="/" aria-label="PokéSearch TCG archive, home">`,
	} {
		if !strings.Contains(body, kept) {
			t.Errorf("the served document lost %q", kept)
		}
	}
}

// Under the running build's prefix an asset is answered exactly as at its
// unversioned path: status, body, validator, cache policy, type, security
// headers and encoding. A module the entry script imports by a relative
// specifier is reachable under the same prefix.
func TestVersionedAssetAnswersAsItsUnversionedPath(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	headers := []string{"ETag", "Cache-Control", "Content-Type", "Content-Encoding", "Vary",
		"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"}
	for _, asset := range []string{"/styles.css", "/js/main.js", "/js/util.js", "/js/state.js"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, enc := range []string{"", "gzip"} {
				h := map[string]string{"Accept-Encoding": enc}
				plain := serveMethod(t, s, method, asset, h)
				versioned := serveMethod(t, s, method, "/assets/abc1234"+asset, h)
				if plain.Code != http.StatusOK || versioned.Code != http.StatusOK {
					t.Errorf("%s %s (%q): status %d unversioned, %d versioned; want 200 both", method, asset, enc, plain.Code, versioned.Code)
					continue
				}
				if !bytes.Equal(plain.Body.Bytes(), versioned.Body.Bytes()) {
					t.Errorf("%s %s (%q): the versioned body differs", method, asset, enc)
				}
				for _, name := range headers {
					if p, v := plain.Header().Get(name), versioned.Header().Get(name); p != v {
						t.Errorf("%s %s (%q): %s is %q unversioned and %q versioned", method, asset, enc, name, p, v)
					}
				}
				if versioned.Header().Get("Cache-Control") != staticCacheControl || versioned.Header().Get("ETag") == "" {
					t.Errorf("%s %s (%q): Cache-Control %q, ETag %q", method, asset, enc,
						versioned.Header().Get("Cache-Control"), versioned.Header().Get("ETag"))
				}
			}
		}
	}
}

func TestVersionedAssetRevalidatesTo304(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	for _, asset := range []string{"/assets/abc1234/styles.css", "/assets/abc1234/js/main.js"} {
		tag := get(t, s, asset).Header().Get("ETag")
		if tag == "" {
			t.Fatalf("%s carries no ETag", asset)
		}
		for _, enc := range []string{"", "gzip"} {
			rec := getWith(t, s, asset, map[string]string{"If-None-Match": tag, "Accept-Encoding": enc})
			if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
				t.Errorf("%s with its own tag (%q): %d with %d bytes, want a bare 304", asset, enc, rec.Code, rec.Body.Len())
			}
			if rec.Header().Get("ETag") != tag || rec.Header().Get("Cache-Control") != staticCacheControl {
				t.Errorf("%s 304: ETag %q, Cache-Control %q", asset, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
			}
		}
		stale := getWith(t, s, asset, map[string]string{"If-None-Match": `W/"0000000000000000"`})
		if stale.Code != http.StatusOK || stale.Body.Len() == 0 {
			t.Errorf("%s with a stale tag: %d with %d bytes, want the file", asset, stale.Code, stale.Body.Len())
		}
	}
}

// Under the prefix only the running build's commit names anything, and only
// an embedded asset other than the document. Every other path there is a
// plain 404 with no validator and Cache-Control: no-store, even for a
// conditional request and whatever the encoding: no cache can be filled under
// one build's name with another build's bytes, nor keep a 404 for the next
// build's URL that it would go on serving once that build is deployed.
func TestAnythingElseUnderTheAssetPrefixIsAnUncacheable404(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	for _, path := range []string{
		"/assets/def5678/styles.css",
		"/assets/def5678/js/main.js",
		"/assets/abc12345/styles.css",
		"/assets/abc123/styles.css",
		"/assets/ABC1234/styles.css",
		"/assets/none/styles.css",
		"/assets/abc1234/",
		"/assets/abc1234/index.html",
		"/assets/abc1234/js",
		"/assets/abc1234/js/",
		"/assets/abc1234/nope.css",
		"/assets/abc1234/js/main.test.mjs",
		"/assets/abc1234/assets/abc1234/styles.css",
		"/assets/abc1234",
		"/assets/",
	} {
		for _, inm := range []string{"", "*", `W/"0000000000000000"`} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, enc := range []string{"", "gzip"} {
					rec := serveMethod(t, s, method, path, map[string]string{"If-None-Match": inm, "Accept-Encoding": enc})
					assertPrefix404(t, method+" "+path+" (If-None-Match "+inm+", "+enc+")", rec)
					if rec.Header().Get("Content-Encoding") != "" {
						t.Errorf("%s %s: the 404 was compressed", method, path)
					}
					if rec.Header().Get("Content-Security-Policy") != securityCSP {
						t.Errorf("%s %s: the 404 lost the security headers", method, path)
					}
					if strings.Contains(rec.Body.String(), "<!doctype") || strings.Contains(rec.Body.String(), "body") {
						t.Errorf("%s %s: the 404 carries an asset's bytes: %.60q", method, path, rec.Body.String())
					}
				}
			}
		}
	}
}

// The unversioned paths keep answering exactly as before the prefix existed.
func TestUnversionedAssetPathsAreUnchangedUnderAStampedBuild(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	unstamped, _ := stampedServer(t, "none", web.Files)
	for _, asset := range []string{"/styles.css", "/js/main.js", "/js/util.js"} {
		a, b := get(t, s, asset), get(t, unstamped, asset)
		if a.Code != http.StatusOK || a.Header().Get("Cache-Control") != staticCacheControl {
			t.Errorf("%s: %d, Cache-Control %q", asset, a.Code, a.Header().Get("Cache-Control"))
		}
		if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) || a.Header().Get("ETag") != b.Header().Get("ETag") {
			t.Errorf("%s answers differently once the build is stamped", asset)
		}
	}
	if rec := get(t, s, "/"); rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Errorf("/: Cache-Control %q, ETag %q", rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	if rec := get(t, s, "/index.html"); rec.Code != http.StatusMovedPermanently {
		t.Errorf("/index.html = %d, want the file server's redirect to /", rec.Code)
	}
}

// The document's validator is computed over the bytes served, so it moves
// with the commit and is stable within one build, and a 304 is given only for
// the running build's document.
func TestDocumentETagFollowsTheCommit(t *testing.T) {
	a, _ := stampedServer(t, "abc1234", web.Files)
	b, _ := stampedServer(t, "def5678", web.Files)
	again, _ := stampedServer(t, "abc1234", web.Files)
	unstamped, _ := stampedServer(t, "none", web.Files)
	tagA, tagB := get(t, a, "/").Header().Get("ETag"), get(t, b, "/").Header().Get("ETag")
	tagAgain, tagNone := get(t, again, "/").Header().Get("ETag"), get(t, unstamped, "/").Header().Get("ETag")
	if tagA == "" || tagA == tagB || tagA == tagNone || tagB == tagNone {
		t.Errorf("document ETags %q (abc1234), %q (def5678), %q (unstamped): want three different tags", tagA, tagB, tagNone)
	}
	if tagA != tagAgain || tagA != get(t, a, "/").Header().Get("ETag") {
		t.Errorf("one commit gives document ETags %q and %q, want the same", tagA, tagAgain)
	}
	if rec := getWith(t, a, "/", map[string]string{"If-None-Match": tagA}); rec.Code != http.StatusNotModified {
		t.Errorf("/ with its own tag = %d, want 304", rec.Code)
	}
	if rec := getWith(t, a, "/", map[string]string{"If-None-Match": tagB}); rec.Code != http.StatusOK {
		t.Errorf("/ with another build's tag = %d, want 200", rec.Code)
	}
}

// An unstamped build has no identity to put in a path: it serves the markup
// exactly as on disk, answers nothing under the prefix, and says nothing about
// it in the log, since a local build is unstamped by design.
func TestUnstampedBuildServesTheDocumentAsOnDisk(t *testing.T) {
	s, logBuf := stampedServer(t, "none", web.Files)
	if body := get(t, s, "/").Body.String(); body != diskIndex(t) {
		t.Error("an unstamped build rewrote the document")
	}
	for _, path := range []string{"/assets/none/styles.css", "/assets/none/js/main.js"} {
		assertPrefix404(t, path, get(t, s, path))
	}
	if lines := constructionLines(t, logBuf); len(lines) != 0 {
		t.Errorf("an unstamped build logged at construction: %v", lines)
	}
}

// The access line names the path as requested, and the route label stays the
// one "static" every other asset has, so a new commit never adds a counter.
func TestVersionedAssetsShareTheStaticLabelAndLogTheirPath(t *testing.T) {
	s, logBuf := stampedServer(t, "abc1234", web.Files)
	get(t, s, "/assets/abc1234/styles.css")
	line := oneLine(t, logBuf, true)
	if line["path"] != "/assets/abc1234/styles.css" || line["status"] != float64(200) {
		t.Errorf("access line %v", line)
	}
	for _, path := range []string{"/assets/abc1234/styles.css", "/assets/def5678/js/main.js", "/assets/"} {
		if got := routeLabel(path); got != "static" {
			t.Errorf("routeLabel(%q) = %q, want static", path, got)
		}
		if exemptFromCap(path) {
			t.Errorf("%s is exempt from the cap on requests in flight; only the probes are", path)
		}
	}
}

// A versioned asset takes a place under the cap like any other static file:
// with every place held it is refused, not served.
func TestVersionedAssetIsRefusedWhenTheServerIsFull(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	if err := s.SetMaxInFlight(1); err != nil {
		t.Fatal(err)
	}
	pool := s.inFlight.Load()
	pool.slots <- struct{}{}
	defer func() { <-pool.slots }()
	rec := get(t, s, "/assets/abc1234/styles.css")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != overloadedRetryAfter {
		t.Errorf("a full server answered a versioned asset %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// staticDoc is a small static tree whose document holds the given markup.
func staticDoc(doc string) fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(doc)},
		"styles.css": &fstest.MapFile{Data: []byte(strings.Repeat("body { color: #0b1020; }\n", 200))},
		"js/main.js": &fstest.MapFile{Data: []byte(`import { x } from "./util.js";` + "\n")},
		"js/util.js": &fstest.MapFile{Data: []byte("export const x = 1;\n")},
	}
}

const goodDoc = `<!doctype html><html><head>` +
	`<meta property="og:url" content="https://example.com/styles.css">` +
	`<link rel="stylesheet" href="/styles.css"></head><body>` +
	`<p>The entry point is /js/main.js and the sheet is <code>/styles.css</code>.</p>` +
	`<a href="/styles.css.map">not the stylesheet</a>` +
	`<script type="module" src="/js/main.js"></script></body></html>`

// The rewrite is a substitution of two exact attribute values. When the
// markup does not hold each exactly once, nothing is rewritten: the document
// is served as it is, the prefix answers nothing, and one warning says why.
func TestDocumentWithoutExactlyOneOfEachReferenceIsServedUnrewritten(t *testing.T) {
	for name, doc := range map[string]string{
		"no stylesheet":   strings.Replace(goodDoc, diskStylesheetRef, `href="/style.css"`, 1),
		"no entry script": strings.Replace(goodDoc, `<script type="module" src="/js/main.js">`, `<script type="module" src="/js/app.js">`, 1),
		"two stylesheets": strings.Replace(goodDoc, "</head>", `<link rel="stylesheet" href="/styles.css"></head>`, 1),
		"two entries":     strings.Replace(goodDoc, "</body>", `<script type="module" src="/js/main.js"></script></body>`, 1),
		"neither":         "<!doctype html><title>PokéSearch</title>",
	} {
		t.Run(name, func(t *testing.T) {
			s, logBuf := stampedServer(t, "abc1234", staticDoc(doc))
			if body := get(t, s, "/").Body.String(); body != doc {
				t.Errorf("served %q, want the document unrewritten", body)
			}
			assertPrefix404(t, "/assets/abc1234/styles.css with the document unrewritten", get(t, s, "/assets/abc1234/styles.css"))
			if rec := get(t, s, "/styles.css"); rec.Code != http.StatusOK {
				t.Errorf("/styles.css = %d", rec.Code)
			}
			line := oneLine(t, logBuf, false)
			if line["level"] != "WARN" || !strings.Contains(line["msg"].(string), "asset") {
				t.Errorf("construction log line %v, want one warning about the asset references", line)
			}
		})
	}
}

// With exactly one of each, only the two attribute values move: a URL that
// merely ends in the same path, the same path in text and an attribute that
// merely starts with the same value stay as they are.
func TestOnlyTheTwoAttributeValuesAreRewritten(t *testing.T) {
	s, logBuf := stampedServer(t, "abc1234", staticDoc(goodDoc))
	want := strings.Replace(goodDoc, `<link rel="stylesheet" href="/styles.css">`, `<link rel="stylesheet" href="/assets/abc1234/styles.css">`, 1)
	want = strings.Replace(want, `<script type="module" src="/js/main.js">`, `<script type="module" src="/assets/abc1234/js/main.js">`, 1)
	if body := get(t, s, "/").Body.String(); body != want {
		t.Errorf("served %q\nwant   %q", body, want)
	}
	if lines := constructionLines(t, logBuf); len(lines) != 0 {
		t.Errorf("a clean rewrite logged: %v", lines)
	}
	if rec := get(t, s, "/assets/abc1234/js/util.js"); rec.Code != http.StatusOK || rec.Body.String() != "export const x = 1;\n" {
		t.Errorf("imported module under the prefix: %d %q", rec.Code, rec.Body.String())
	}
}

// The tree is served whatever happens to the document, including no document.
func TestStampedBuildWithoutADocumentStillServesItsAssets(t *testing.T) {
	static := staticDoc(goodDoc)
	delete(static, "index.html")
	s, logBuf := stampedServer(t, "abc1234", static)
	if rec := get(t, s, "/styles.css"); rec.Code != http.StatusOK {
		t.Errorf("/styles.css = %d", rec.Code)
	}
	assertPrefix404(t, "/assets/abc1234/styles.css with no document", get(t, s, "/assets/abc1234/styles.css"))
	if lines := constructionLines(t, logBuf); len(lines) != 0 {
		t.Errorf("a tree with no document logged: %v", lines)
	}
	if s, _ := stampedServer(t, "abc1234", nil); s == nil {
		t.Error("New returned nil for a nil tree")
	}
}

// A 404 outside the prefix is the file server's, exactly as before the prefix
// existed: no cache policy, no validator, whether the build is stamped or not,
// with the security headers on it. "/assets" without its slash is outside.
func TestA404OutsideTheAssetPrefixIsUnchanged(t *testing.T) {
	stamped, _ := stampedServer(t, "abc1234", web.Files)
	unstamped, _ := stampedServer(t, "none", web.Files)
	for _, s := range []*Server{stamped, unstamped} {
		for _, path := range []string{"/nope.css", "/js/nope.js", "/assets", "/assetsx/abc1234/styles.css", "/abc1234/styles.css"} {
			for _, enc := range []string{"", "gzip"} {
				rec := getWith(t, s, path, map[string]string{"Accept-Encoding": enc})
				if rec.Code != http.StatusNotFound || rec.Body.String() != "404 page not found\n" {
					t.Errorf("%s = %d %q, want the file server's 404", path, rec.Code, rec.Body.String())
				}
				if cc, tag := rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"); cc != "" || tag != "" {
					t.Errorf("%s: Cache-Control %q, ETag %q, want neither", path, cc, tag)
				}
				if rec.Header().Get("Content-Security-Policy") != securityCSP {
					t.Errorf("%s: the 404 lost the security headers", path)
				}
			}
		}
	}
}
