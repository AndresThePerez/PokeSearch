package web

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The server names the stylesheet and the entry script under a prefix that
// carries the build commit, and the browser fetches everything else relative
// to them. That holds only while nothing the page loads names a file of this
// tree by a root path: one `import "/js/x.js"` and that module is fetched at
// its unversioned, cacheable URL again, from whichever build a cache holds.
// These checks are what keeps the versioned prefix whole.

var (
	// moduleSpec is the specifier of any static import or re-export, at the
	// start of a line or indented, and of a side-effect import. It is not
	// anchored, so it also reads a line that merely contains `from "…"`; no
	// shipped script has one outside an import.
	moduleSpec    = regexp.MustCompile("\\b(?:from|import)\\s*[\"'`]([^\"'`]+)[\"'`]")
	dynamicImport = regexp.MustCompile(`\bimport\s*\(`)
	// workerOrModuleURL is a worker script, or a URL resolved against the
	// module, named by a root path.
	workerOrModuleURL = regexp.MustCompile("new\\s+(?:(?:Shared)?Worker\\s*\\(\\s*[\"'`](/[^\"'`]*)|URL\\s*\\(\\s*[\"'`](/[^\"'`]*)[\"'`]\\s*,\\s*import\\.meta\\.url)")
	cssURL            = regexp.MustCompile(`(?i)(?:url\(\s*|@import\s+(?:url\(\s*)?)["']?([^"')\s;]+)`)
	markupURL         = regexp.MustCompile(`\b(?:href|src)\s*=\s*["']([^"']*)["']`)
)

// rootPathProblems reports each reference in a script, a stylesheet or the
// document (by the name's extension) that names a file of this tree by a root
// path or leaves the tree. The document may name exactly the two entry points
// the server rewrites, once each, and the home link.
func rootPathProblems(name, src string) []string {
	var out []string
	switch {
	case strings.HasSuffix(name, ".js"):
		for _, m := range moduleSpec.FindAllStringSubmatch(src, -1) {
			spec := m[1]
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				out = append(out, name+" imports "+spec+", which is not relative")
				continue
			}
			if strings.HasPrefix(path.Join(path.Dir(name), spec), "..") {
				out = append(out, name+" imports "+spec+", which leaves the tree")
			}
		}
		if dynamicImport.MatchString(src) {
			out = append(out, name+" uses a dynamic import")
		}
		for _, m := range workerOrModuleURL.FindAllStringSubmatch(src, -1) {
			out = append(out, name+" names "+m[1]+m[2]+" by a root path")
		}
	case strings.HasSuffix(name, ".css"):
		for _, m := range cssURL.FindAllStringSubmatch(src, -1) {
			if strings.HasPrefix(m[1], "/") {
				out = append(out, name+" names "+m[1]+" by a root path")
			}
		}
	case strings.HasSuffix(name, ".html"):
		entries := map[string]int{"/styles.css": 0, "/js/main.js": 0}
		for _, m := range markupURL.FindAllStringSubmatch(src, -1) {
			ref := m[1]
			switch {
			case !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") || ref == "/":
			default:
				if _, ok := entries[ref]; ok {
					entries[ref]++
					continue
				}
				out = append(out, name+" names "+ref+" by a root path")
			}
		}
		for ref, n := range entries {
			if n != 1 {
				out = append(out, fmt.Sprintf("%s names %s %d times, want once", name, ref, n))
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestRootPathProblemsCatchesWhatBreaksTheVersionedPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		bad       bool
	}{
		{"js/a.js", `import { x } from "./b.js";`, false},
		{"js/a.js", "import {\n  x,\n  y,\n} from \"./b.js\";", false},
		{"js/a.js", `import "../styles.js";`, false},
		{"js/a.js", `import { x } from "/js/b.js";`, true},
		{"js/a.js", `import "/js/b.js";`, true},
		{"js/a.js", "import {\n  x,\n} from \"/js/b.js\";", true},
		{"js/a.js", `export { x } from "/js/b.js";`, true},
		{"js/a.js", `import { x } from "b.js";`, true},
		{"js/a.js", `import { x } from "https://example.com/b.js";`, true},
		{"js/a.js", `import { x } from "../../b.js";`, true},
		{"js/a.js", `const m = await import("./b.js");`, true},
		{"styles.css", `.a { background: url(img/x.png); }`, false},
		{"styles.css", `.a { background: url("data:image/svg+xml,%3Csvg%3E"); }`, false},
		{"styles.css", `.a { background: url(/img/x.png); }`, true},
		{"styles.css", `.a { background: url( "/img/x.png" ); }`, true},
		{"styles.css", `.a { background: URL('/img/x.png'); }`, true},
		{"styles.css", `@import "/more.css";`, true},
		{"styles.css", `@import url("/x.css");`, true},
		{"styles.css", `@import url(/x.css);`, true},
		{"styles.css", `@import url( '/x.css' ) screen;`, true},
		{"styles.css", `@import url("more.css");`, false},
		{"styles.css", `@import "./more.css";`, false},
		{"js/a.js", "function f() {\n  import { x } from \"/js/b.js\";\n}", true},
		{"js/a.js", "\timport \"/js/b.js\";", true},
		{"js/a.js", "  export * from \"/js/b.js\";", true},
		{"js/a.js", "  import { x } from \"./b.js\";", false},
		{"js/a.js", `const w = new Worker("/js/worker.js", { type: "module" });`, true},
		{"js/a.js", `const w = new SharedWorker('/js/worker.js');`, true},
		{"js/a.js", `const w = new Worker(new URL("./worker.js", import.meta.url));`, false},
		{"js/a.js", "const u = new URL(`/js/data.json`, import.meta.url);", true},
		{"js/a.js", `const u = new URL("./data.json", import.meta.url);`, false},
		{"js/a.js", `const u = new URL("/api/search", location.origin);`, false},
		{"js/a.js", `const res = await fetch("/api/search?q=pikachu");`, false},
		{"index.html", `<link rel="stylesheet" href="/styles.css"><a href="/">home</a><a href="#x">x</a><script type="module" src="/js/main.js"></script>`, false},
		{"index.html", `<link rel="stylesheet" href="/styles.css"><script type="module" src="/js/main.js"></script><link rel="modulepreload" href="/js/util.js">`, true},
		{"index.html", `<link rel="stylesheet" href="/styles.css"><script type="module" src="/js/main.js"></script><img src="/logo.png">`, true},
		{"index.html", `<link rel="stylesheet" href="/styles.css"><link rel="stylesheet" href="/styles.css"><script type="module" src="/js/main.js"></script>`, true},
		{"index.html", `<link rel="stylesheet" href="/styles.css">`, true},
	} {
		if got := rootPathProblems(tc.name, tc.src); (len(got) > 0) != tc.bad {
			t.Errorf("rootPathProblems(%s, %q) = %q, want a problem: %v", tc.name, tc.src, got, tc.bad)
		}
	}
}

// Every shipped script, the stylesheet and the document pass.
func TestNothingThePageLoadsNamesATreeFileByARootPath(t *testing.T) {
	names := []string{"index.html", "styles.css"}
	for _, n := range embeddedJS(t) {
		names = append(names, "js/"+n)
	}
	for _, name := range names {
		src, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range rootPathProblems(name, string(src)) {
			t.Error(p)
		}
	}
	if len(names) < 5 {
		t.Errorf("only %d files checked; the embedded set looks wrong", len(names))
	}
}
