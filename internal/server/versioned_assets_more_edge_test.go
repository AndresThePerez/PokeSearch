package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AndresThePerez/pokesearch/web"
)

// A path under the prefix may spell the commit, the prefix or the file name
// with percent-escapes. Whatever it decodes to, the answer is either the
// asset exactly as its unversioned path answers it, or the one uncacheable
// 404 the prefix gives: never a 404 a cache in front may keep.
func TestEscapedPathUnderThePrefixIsTheAssetOrAnUncacheable404(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	plain := get(t, s, "/styles.css")
	for _, path := range []string{
		"/assets/%61bc1234/styles.css",
		"/assets/abc123%34/styles.css",
		"/%61ssets/abc1234/styles.css",
		"/assets/abc1234/%73tyles.css",
		"/assets/abc1234%2Fstyles.css",
		"/assets%2Fabc1234/styles.css",
		"/assets/%64ef5678/styles.css",
		"/assets/abc1234%2F..%2Fdef5678/styles.css",
		"/assets/%2e%2e/styles.css",
	} {
		rec := get(t, s, path)
		switch rec.Code {
		case http.StatusOK:
			if !bytes.Equal(rec.Body.Bytes(), plain.Body.Bytes()) ||
				rec.Header().Get("ETag") != plain.Header().Get("ETag") ||
				rec.Header().Get("Cache-Control") != staticCacheControl {
				t.Errorf("%s: a 200 that is not the stylesheet as /styles.css serves it (ETag %q, Cache-Control %q)",
					path, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
			}
		case http.StatusNotFound:
			assertPrefix404(t, path, rec)
		default:
			t.Errorf("%s = %d, want the stylesheet or the uncacheable 404", path, rec.Code)
		}
	}
}

// The running build's own validator does not turn a request for another
// build's URL into a 304: a cache revalidating what it holds for one build's
// name must never be told it is current by another build.
func TestRunningBuildsTagCannotRevalidateAnotherCommitsURL(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	for _, asset := range []string{"/styles.css", "/js/main.js"} {
		tag := get(t, s, "/assets/abc1234"+asset).Header().Get("ETag")
		if tag == "" {
			t.Fatalf("%s has no ETag under the running prefix", asset)
		}
		for _, path := range []string{"/assets/def5678" + asset, "/assets/abc12345" + asset, "/assets/abc123" + asset} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := serveMethod(t, s, method, path, map[string]string{"If-None-Match": tag})
				assertPrefix404(t, method+" "+path+" with the running build's tag", rec)
			}
		}
	}
}

// The rewritten document is longer than the markup on disk. Every way of
// reading it over HTTP agrees on the served bytes: the declared length of a
// GET and of a HEAD, a Range across the rewritten reference, the size a
// Content-Range reports, a gzip answer once decompressed, and an If-Range
// carrying the document's own validator.
func TestRewrittenDocumentLengthRangeHeadAndGzipAgree(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	full := get(t, s, "/")
	body := full.Body.Bytes()
	if full.Code != http.StatusOK || len(body) <= len(diskIndex(t)) {
		t.Fatalf("GET / = %d with %d bytes, want 200 and longer than the %d on disk", full.Code, len(body), len(diskIndex(t)))
	}
	if cl := full.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Errorf("GET / declares Content-Length %q and sends %d bytes", cl, len(body))
	}
	head := serveMethod(t, s, http.MethodHead, "/", nil)
	if head.Code != http.StatusOK || head.Header().Get("Content-Length") != strconv.Itoa(len(body)) || head.Body.Len() != 0 {
		t.Errorf("HEAD / = %d, Content-Length %q, %d body bytes; want 200, %d, none",
			head.Code, head.Header().Get("Content-Length"), head.Body.Len(), len(body))
	}
	if head.Header().Get("Cache-Control") != "no-cache" || head.Header().Get("ETag") != full.Header().Get("ETag") {
		t.Errorf("HEAD /: Cache-Control %q, ETag %q", head.Header().Get("Cache-Control"), head.Header().Get("ETag"))
	}
	if ct := full.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type %q, want text/html", ct)
	}

	at := bytes.Index(body, []byte(`href="/assets/abc1234/styles.css"`))
	if at < 0 {
		t.Fatal("the served document does not name the versioned stylesheet")
	}
	lo, hi := at-5, at+40
	rng := getWith(t, s, "/", map[string]string{"Range": "bytes=" + strconv.Itoa(lo) + "-" + strconv.Itoa(hi)})
	if rng.Code != http.StatusPartialContent || !bytes.Equal(rng.Body.Bytes(), body[lo:hi+1]) {
		t.Errorf("Range over the rewritten reference = %d %q, want 206 %q", rng.Code, rng.Body.String(), body[lo:hi+1])
	}
	wantCR := "bytes " + strconv.Itoa(lo) + "-" + strconv.Itoa(hi) + "/" + strconv.Itoa(len(body))
	if cr := rng.Header().Get("Content-Range"); cr != wantCR {
		t.Errorf("Content-Range %q, want %q", cr, wantCR)
	}
	tail := getWith(t, s, "/", map[string]string{"Range": "bytes=-16"})
	if tail.Code != http.StatusPartialContent || !bytes.Equal(tail.Body.Bytes(), body[len(body)-16:]) {
		t.Errorf("suffix Range = %d %q, want the last 16 served bytes %q", tail.Code, tail.Body.String(), body[len(body)-16:])
	}
	ifRange := getWith(t, s, "/", map[string]string{"Range": "bytes=0-9", "If-Range": full.Header().Get("ETag")})
	if ifRange.Code != http.StatusOK || !bytes.Equal(ifRange.Body.Bytes(), body) {
		// A weak validator never satisfies If-Range, so the whole document is
		// the only correct answer.
		t.Errorf("If-Range with the weak document tag = %d with %d bytes, want 200 with the whole document", ifRange.Code, ifRange.Body.Len())
	}

	gz := getWith(t, s, "/", map[string]string{"Accept-Encoding": "gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(gz.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("gzip GET /: Content-Encoding %q, Vary %q", gz.Header().Get("Content-Encoding"), gz.Header().Get("Vary"))
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(plain, body) {
		t.Errorf("gzip GET / decompresses to %d bytes (%v), want the %d served bytes", len(plain), err, len(body))
	}
	if gz.Header().Get("ETag") != full.Header().Get("ETag") || gz.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("gzip GET /: ETag %q, Cache-Control %q", gz.Header().Get("ETag"), gz.Header().Get("Cache-Control"))
	}
}

// The tree as served answers fs.ReadFile, fs.Stat and a seek on the document
// with the rewritten bytes and their length, and leaves every other file the
// tree's own.
func TestServedTreeReportsTheRewrittenDocumentConsistently(t *testing.T) {
	served, commit, err := versionedStatic(web.Files, "abc1234")
	if err != nil || commit != "abc1234" {
		t.Fatalf("versionedStatic = %q, %v", commit, err)
	}
	doc, err := fs.ReadFile(served, indexFile)
	if err != nil || !bytes.Contains(doc, []byte(`src="/assets/abc1234/js/main.js"`)) {
		t.Fatalf("ReadFile(index.html): %v, rewritten %v", err, bytes.Contains(doc, []byte("/assets/abc1234/")))
	}
	info, err := fs.Stat(served, indexFile)
	if err != nil || info.Size() != int64(len(doc)) || info.Name() != indexFile || info.IsDir() || !info.Mode().IsRegular() {
		t.Fatalf("Stat(index.html) = %v, %v; want a regular file named %s of %d bytes", info, err, indexFile, len(doc))
	}
	f, err := served.Open(indexFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sk, ok := f.(io.ReadSeeker)
	if !ok {
		t.Fatal("the document cannot seek; Range requests need it")
	}
	if end, err := sk.Seek(0, io.SeekEnd); err != nil || end != int64(len(doc)) {
		t.Errorf("Seek(0, end) = %d, %v; want %d", end, err, len(doc))
	}
	if _, err := sk.Seek(-10, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	last, _ := io.ReadAll(sk)
	if !bytes.Equal(last, doc[len(doc)-10:]) {
		t.Errorf("the last 10 bytes read %q, want %q", last, doc[len(doc)-10:])
	}
	// Two opens are independent readers over the same bytes.
	g, _ := served.Open(indexFile)
	again, _ := io.ReadAll(g)
	if !bytes.Equal(again, doc) {
		t.Error("a second open does not read the document from its start")
	}
	for _, name := range []string{"styles.css", "js/main.js"} {
		want, _ := fs.ReadFile(web.Files, name)
		got, err := fs.ReadFile(served, name)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s through the served tree: %v, equal %v", name, err, bytes.Equal(got, want))
		}
	}
}

// A method other than GET and HEAD under the prefix is answered as it is at
// the unversioned path, with the mux's 405 and its Allow header, for the
// running commit and any other.
func TestOtherMethodsUnderThePrefixAnswerAsTheUnversionedPath(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		plain := serveMethod(t, s, method, "/styles.css", nil)
		for _, path := range []string{"/assets/abc1234/styles.css", "/assets/def5678/styles.css"} {
			rec := serveMethod(t, s, method, path, nil)
			if rec.Code != plain.Code || rec.Header().Get("Allow") != plain.Header().Get("Allow") {
				t.Errorf("%s %s = %d Allow %q; /styles.css = %d Allow %q", method, path,
					rec.Code, rec.Header().Get("Allow"), plain.Code, plain.Header().Get("Allow"))
			}
			if rec.Header().Get("ETag") != "" || rec.Body.Len() > 0 && strings.Contains(rec.Body.String(), "{") {
				t.Errorf("%s %s answered with an asset: ETag %q", method, path, rec.Header().Get("ETag"))
			}
		}
	}
}

// The first requests a fresh server sees may arrive together. They share the
// one rewritten document and the validator map without a race, and every one
// of them gets the same bytes and the same answer.
func TestConcurrentFirstRequestsGetOneDocumentAndOneAnswer(t *testing.T) {
	s, _ := stampedServer(t, "abc1234", web.Files)
	paths := []string{"/", "/assets/abc1234/styles.css", "/assets/abc1234/js/main.js", "/assets/def5678/styles.css"}
	const rounds = 16
	type answer struct {
		code int
		etag string
		body string
	}
	got := make([][]answer, len(paths))
	for i := range got {
		got[i] = make([]answer, rounds)
	}
	var wg sync.WaitGroup
	for i, p := range paths {
		for n := range rounds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h := map[string]string{}
				if n%2 == 1 {
					h["Range"] = "bytes=0-"
				}
				rec := getWith(t, s, p, h)
				got[i][n] = answer{code: rec.Code, etag: rec.Header().Get("ETag"), body: rec.Body.String()}
			}()
		}
	}
	wg.Wait()
	for i, p := range paths {
		for n := 1; n < rounds; n++ {
			a, b := got[i][0], got[i][n]
			if b.etag != a.etag || b.body != a.body || (b.code != a.code && b.code != http.StatusPartialContent) {
				t.Errorf("%s: answer %d (%d, %q, %d bytes) differs from answer 0 (%d, %q, %d bytes)",
					p, n, b.code, b.etag, len(b.body), a.code, a.etag, len(a.body))
			}
		}
	}
}

// A commit of a single character from each allowed class, and one that looks
// like a path segment the prefix itself uses, names its assets and nothing
// else.
func TestCommitsAtTheEdgesOfTheAllowList(t *testing.T) {
	for _, commit := range []string{"-", "_", "Z", "9", "js", "assets", "-dirty", strings.Repeat("Z9_-", maxAssetCommitLen/4)} {
		t.Run(commit, func(t *testing.T) {
			s, logBuf := stampedServer(t, commit, web.Files)
			if rec := get(t, s, "/assets/"+commit+"/styles.css"); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != staticCacheControl {
				t.Errorf("/assets/%s/styles.css = %d, Cache-Control %q", commit, rec.Code, rec.Header().Get("Cache-Control"))
			}
			assertPrefix404(t, "/assets/"+commit+"/", get(t, s, "/assets/"+commit+"/"))
			assertPrefix404(t, "/assets/"+commit+"/assets/"+commit+"/styles.css", get(t, s, "/assets/"+commit+"/assets/"+commit+"/styles.css"))
			if rec := get(t, s, "/js/main.js"); rec.Code != http.StatusOK {
				t.Errorf("/js/main.js = %d with commit %q", rec.Code, commit)
			}
			if lines := constructionLines(t, logBuf); len(lines) != 0 {
				t.Errorf("commit %q logged at construction: %v", commit, lines)
			}
		})
	}
}

// The same paths as the values of other attributes are not references the
// page loads its assets by, and they stay as they are.
func TestOtherAttributesWithTheSameValueAreNotRewritten(t *testing.T) {
	doc := strings.Replace(goodDoc, "</head>",
		`<meta name="x-sheet" content="/styles.css"><link rel="alternate" title="/js/main.js" href="/feed"></head>`, 1)
	s, logBuf := stampedServer(t, "abc1234", staticDoc(doc))
	want := strings.Replace(doc, `<link rel="stylesheet" href="/styles.css">`, `<link rel="stylesheet" href="/assets/abc1234/styles.css">`, 1)
	want = strings.Replace(want, `<script type="module" src="/js/main.js">`, `<script type="module" src="/assets/abc1234/js/main.js">`, 1)
	if body := get(t, s, "/").Body.String(); body != want {
		t.Errorf("served %q\nwant   %q", body, want)
	}
	if lines := constructionLines(t, logBuf); len(lines) != 0 {
		t.Errorf("a clean rewrite logged: %v", lines)
	}
}
