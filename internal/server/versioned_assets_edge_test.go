package server

import (
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/AndresThePerez/pokesearch/web"
)

// A commit the build was stamped with goes into a URL path and into the
// document's markup. One that cannot sit in both unescaped is not used at
// all: the document is served as on disk, nothing answers under the prefix,
// and one warning names the problem. The page is never broken and nothing is
// injected into it.
func TestCommitThatCannotNameAPathIsNotUsed(t *testing.T) {
	disk := diskIndex(t)
	for _, commit := range []string{
		"",
		"abc/1234",
		`abc"1234`,
		"abc'1234",
		"<script>",
		"abc 1234",
		"abc\t1234",
		"abc\n1234",
		"abc%2F1234",
		"abc?1234",
		"abc#1234",
		"abc&amp;",
		"abc\\1234",
		".",
		"..",
		"abc.1234",
		"é1234",
		"abc\x00",
		strings.Repeat("a", maxAssetCommitLen+1),
	} {
		t.Run(url.PathEscape(commit), func(t *testing.T) {
			s, logBuf := stampedServer(t, commit, web.Files)
			rec := get(t, s, "/")
			if rec.Code != http.StatusOK || rec.Body.String() != disk {
				t.Fatalf("commit %q: / = %d and the document was changed", commit, rec.Code)
			}
			// path.Clean: an empty or dotted segment is the mux's redirect to
			// the cleaned path first, and that path must be a 404 as well.
			probe := path.Clean("/assets/" + url.PathEscape(commit) + "/styles.css")
			if strings.HasPrefix(probe, "/assets/") {
				assertPrefix404(t, probe, get(t, s, probe))
			}
			line := oneLine(t, logBuf, false)
			if line["level"] != "WARN" || !strings.Contains(line["msg"].(string), "asset") {
				t.Errorf("commit %q: construction log line %v, want one warning", commit, line)
			}
			if c, _ := line["commit"].(string); len(c) > 2*maxLoggedValueRunes {
				t.Errorf("commit %q: the warning repeats %d bytes of it", commit, len(c))
			}
		})
	}
}

// Every commit a real stamp produces is used as it is: a short or full hash,
// either case, a describe-style suffix, and the longest length allowed.
func TestCommitsAStampProducesAreUsed(t *testing.T) {
	for _, commit := range []string{
		"abc1234",
		"ABC1234",
		"0123456789abcdef0123456789abcdef01234567",
		"abc1234-dirty",
		"abc_1234",
		"a",
		strings.Repeat("a", maxAssetCommitLen),
	} {
		t.Run(commit, func(t *testing.T) {
			s, logBuf := stampedServer(t, commit, web.Files)
			body := get(t, s, "/").Body.String()
			for _, ref := range []string{`href="/assets/` + commit + `/styles.css"`, `src="/assets/` + commit + `/js/main.js"`} {
				if !strings.Contains(body, ref) {
					t.Errorf("commit %q: the document does not name %s", commit, ref)
				}
			}
			for _, path := range []string{"/assets/" + commit + "/styles.css", "/assets/" + commit + "/js/main.js"} {
				if rec := get(t, s, path); rec.Code != http.StatusOK {
					t.Errorf("%s = %d, want 200", path, rec.Code)
				}
			}
			if lines := constructionLines(t, logBuf); len(lines) != 0 {
				t.Errorf("commit %q logged at construction: %v", commit, lines)
			}
		})
	}
}

// unreadableFS is a tree one of whose files cannot be opened.
type unreadableFS struct {
	fs.FS
	broken string
}

func (u unreadableFS) Open(name string) (fs.File, error) {
	if name == u.broken {
		return nil, fs.ErrPermission
	}
	return u.FS.Open(name)
}

// A versioned asset is only served with a validator. When the validators
// cannot all be computed, the document goes back to naming the unversioned
// paths rather than naming a prefix that would answer some assets and not
// others.
func TestUnreadableTreeFallsBackToTheUnversionedDocument(t *testing.T) {
	static := staticDoc(goodDoc)
	static["zz.css"] = &fstest.MapFile{Data: []byte("p {}")}
	s, logBuf := stampedServer(t, "abc1234", unreadableFS{FS: static, broken: "zz.css"})
	if body := get(t, s, "/").Body.String(); body != goodDoc {
		t.Errorf("served %q, want the document unrewritten", body)
	}
	assertPrefix404(t, "/assets/abc1234/styles.css after the fallback", get(t, s, "/assets/abc1234/styles.css"))
	if rec := get(t, s, "/styles.css"); rec.Code != http.StatusOK {
		t.Errorf("/styles.css = %d", rec.Code)
	}
	lines := constructionLines(t, logBuf)
	if len(lines) != 1 || lines[0]["msg"] != "static asset validators unavailable" {
		t.Errorf("construction log lines %v, want the one validator warning", lines)
	}
}

// A percent-escape changes how a path is spelled, not which path it is. Under
// the prefix the decoded path decides: an escaped spelling of the running
// prefix, commit or file name is the asset, with every header the plain
// spelling gets, and an escaped spelling of anything else is the uncacheable
// 404. The access line keeps the path as decoded from the request.
func TestEscapedSpellingsUnderThePrefixAnswerAsTheirDecodedPath(t *testing.T) {
	s, logBuf := stampedServer(t, "abc1234", web.Files)
	plain := get(t, s, "/assets/abc1234/styles.css")
	for path, served := range map[string]bool{
		"/assets/%61bc1234/styles.css":              true,
		"/assets/abc123%34/styles.css":              true,
		"/%61ssets/abc1234/styles.css":              true,
		"/assets/abc1234/%73tyles.css":              true,
		"/assets%2Fabc1234/styles.css":              true,
		"/assets/abc1234%2Fstyles.css":              true,
		"/assets/abc1234/js%2Fmain.js":              true,
		"/assets/%64ef5678/styles.css":              false,
		"/assets/abc1234%2F..%2Fdef5678/styles.css": false,
		"/assets/abc1234/%69ndex.html":              false,
		"/assets/abc1234/js%2F":                     false,
		"/assets/abc1234/%2e%2e/styles.css":         false,
	} {
		logBuf.Reset()
		rec := get(t, s, path)
		decoded, err := url.PathUnescape(path)
		if err != nil {
			t.Fatal(err)
		}
		if line := oneLine(t, logBuf, true); line["path"] != decoded {
			t.Errorf("%s: the access line names %v, want the requested path %s", path, line["path"], decoded)
		}
		if !served {
			assertPrefix404(t, path, rec)
			continue
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want the asset", path, rec.Code)
			continue
		}
		want := plain
		if strings.Contains(path, "main.js") {
			want = get(t, s, "/assets/abc1234/js/main.js")
		}
		for _, h := range []string{"ETag", "Cache-Control", "Content-Type", "Content-Length"} {
			if rec.Header().Get(h) != want.Header().Get(h) {
				t.Errorf("%s: %s %q, the plain spelling has %q", path, h, rec.Header().Get(h), want.Header().Get(h))
			}
		}
		if rec.Body.String() != want.Body.String() {
			t.Errorf("%s: the body differs from the plain spelling's", path)
		}
	}
	tag := plain.Header().Get("ETag")
	if rec := getWith(t, s, "/assets/%61bc1234/styles.css", map[string]string{"If-None-Match": tag}); rec.Code != http.StatusNotModified {
		t.Errorf("an escaped spelling with the asset's tag = %d, want 304", rec.Code)
	}
	if rec := getWith(t, s, "/assets/%61bc1234/styles.css", map[string]string{"Range": "bytes=0-9"}); rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 {
		t.Errorf("an escaped spelling with a Range = %d with %d bytes, want 206 with 10", rec.Code, rec.Body.Len())
	}
}
