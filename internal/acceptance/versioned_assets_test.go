//go:build acceptance

package acceptance

import (
	"io"
	"net/http"
	"regexp"
	"testing"
)

var (
	pageStylesheet = regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)">`)
	pageEntry      = regexp.MustCompile(`<script type="module" src="([^"]+)"></script>`)
)

func liveStatus(t *testing.T, path string) (int, http.Header) {
	t.Helper()
	res, err := http.Get(baseURL() + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, res.Header
}

// The page the service serves names its stylesheet and its entry script under
// the commit /api/meta reports, so the document and the assets it loads come
// from one build. Both answer, a module the entry script imports answers under
// the same prefix, and the same paths under any other commit are 404s that no
// cache may keep. The
// suite requires a stamped build; an unstamped one keeps its assets at the
// unversioned paths and fails here.
func TestPageNamesItsAssetsUnderTheBuildCommit(t *testing.T) {
	var meta struct {
		Commit string `json:"commit"`
	}
	getJSON(t, "/api/meta", &meta)
	if meta.Commit == "" || meta.Commit == "none" {
		t.Fatalf("unstamped build (commit %q): its asset URLs carry no commit; build with the -ldflags stamp", meta.Commit)
	}

	res, err := http.Get(baseURL() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	page, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /: status %d, %v", res.StatusCode, err)
	}
	prefix := "/assets/" + meta.Commit
	for _, ref := range []struct {
		re   *regexp.Regexp
		want string
	}{
		{pageStylesheet, prefix + "/styles.css"},
		{pageEntry, prefix + "/js/main.js"},
	} {
		m := ref.re.FindAllSubmatch(page, -1)
		if len(m) != 1 || string(m[0][1]) != ref.want {
			t.Errorf("the page names %q by %s, want exactly %s", m, ref.re, ref.want)
		}
	}

	for _, path := range []string{prefix + "/styles.css", prefix + "/js/main.js", prefix + "/js/util.js"} {
		status, h := liveStatus(t, path)
		if status != http.StatusOK || h.Get("ETag") == "" || h.Get("Cache-Control") == "" {
			t.Errorf("%s: status %d, ETag %q, Cache-Control %q; want 200 with both", path, status, h.Get("ETag"), h.Get("Cache-Control"))
		}
	}

	other := "0000000"
	if meta.Commit == other {
		other = "1111111"
	}
	for _, path := range []string{"/assets/" + other + "/styles.css", "/assets/" + other + "/js/main.js", prefix + "/index.html"} {
		if status, h := liveStatus(t, path); status != http.StatusNotFound || h.Get("Cache-Control") != "no-store" || h.Get("ETag") != "" {
			t.Errorf("%s: status %d, Cache-Control %q, ETag %q; want a 404 with no-store and no ETag", path, status, h.Get("Cache-Control"), h.Get("ETag"))
		}
	}
}
