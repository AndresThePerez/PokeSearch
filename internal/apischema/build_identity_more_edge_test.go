package apischema

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A go.mod saved with Windows line endings is read from disk the same way as
// one with plain newlines: the carriage return is whitespace, so it neither
// hides a directive nor rides along into its release.
func TestGoModReaderIgnoresCRLFLineEndings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.mod")
	src := "module example.com/m\r\n\r\ngo 1.26.10\r\n\r\ntoolchain go1.26.11\r\n\r\nrequire (\r\n\tgolang.org/x/text v0.14.0\r\n)\r\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	goLines, toolchainLines := parseGoMod(trimmedLines(t, path))
	if !slices.Equal(goLines, []string{"1.26.10"}) || !slices.Equal(toolchainLines, []string{"go1.26.11"}) {
		t.Fatalf("CRLF go.mod read as go %q, toolchain %q; want [1.26.10], [go1.26.11]", goLines, toolchainLines)
	}
	if !fullRelease.MatchString(goLines[0]) || toolchainRelease.FindStringSubmatch(toolchainLines[0]) == nil {
		t.Errorf("CRLF go.mod directives %q, %q do not pass the release patterns", goLines[0], toolchainLines[0])
	}
}

// Spellings the go command accepts read as the same toolchain line, and
// spellings it treats differently are kept apart. Rows marked "current
// behaviour" document what the reader does where the go command itself
// rejects the file, so either answer would be safe there.
func TestParseGoModSpellingsOfTheToolchainLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		wantT []string
	}{
		{"trailing comment", []string{"toolchain go1.26.11 // built with"}, []string{"go1.26.11"}},
		{"two spaces", []string{"toolchain  go1.26.11"}, []string{"go1.26.11"}},
		{"tab", []string{"toolchain\tgo1.26.11"}, []string{"go1.26.11"}},
		{"carriage return left on the line", []string{"toolchain go1.26.11\r"}, []string{"go1.26.11"}},
		{"after a require block", []string{"go 1.26.10", "require (", "golang.org/x/text v0.14.0", ")", "toolchain go1.26.11"}, []string{"go1.26.11"}},
		{"comment glued to the release", []string{"toolchain go1.26.11//note"}, []string{"go1.26.11"}},
		{"only a comment", []string{"//toolchain go1.26.11"}, nil},
		// current behaviour: a block form is read as the directive "(" and
		// then fails the release pattern, so the guard stays red.
		{"block form", []string{"toolchain (", "go1.26.11", ")"}, []string{"("}},
		// current behaviour: the reader has no notion of blocks, so a line
		// inside a require block is counted as a toolchain line; the go
		// command rejects that go.mod, and TestGoSumCoversEveryRequiredModule
		// goes red on it too.
		{"inside a require block", []string{"require (", "toolchain go1.26.11", ")"}, []string{"go1.26.11"}},
		// current behaviour: a byte-order mark is not whitespace, so the
		// directive is not recognised and the guard reports no toolchain line.
		{"byte-order mark", []string{"\ufefftoolchain go1.26.11"}, nil},
		{"capitalised verb is no directive", []string{"Toolchain go1.26.11"}, nil},
		{"verb glued to the release is no directive", []string{"toolchaingo1.26.11"}, nil},
	} {
		_, gotT := parseGoMod(tc.lines)
		if !slices.Equal(gotT, tc.wantT) {
			t.Errorf("%s: toolchain lines = %q, want %q", tc.name, gotT, tc.wantT)
		}
	}
}

// The release patterns take digits only, a full X.Y.Z, nothing around it.
// Leading zeros pass the pattern (current behaviour; the go command rejects
// them, and the Dockerfile comparison is by exact text, so `go1.26.011` can
// never match a `golang:1.26.11` stage).
func TestReleasePatternsRejectNearMisses(t *testing.T) {
	for _, tc := range []struct {
		goLine string
		ok     bool
	}{
		{"1.26.10", true},
		{"1.26.010", true},
		{"1.26.10 ", false},
		{" 1.26.10", false},
		{"1.26.10.1", false},
		{"1.26.", false},
		{"v1.26.10", false},
		{"1.26.10rc1", false},
		{"1.26.1O", false},
		{"\uff11.26.10", false},
		{"1.26.10\n", false},
	} {
		if got := fullRelease.MatchString(tc.goLine); got != tc.ok {
			t.Errorf("go line %q accepted = %v, want %v", tc.goLine, got, tc.ok)
		}
	}
	for _, tc := range []struct {
		line, want string
	}{
		{"go1.26.11", "1.26.11"},
		{"go1.26.011", "1.26.011"},
		{"go1.26.4294967296", "1.26.4294967296"},
		{"Go1.26.11", ""},
		{"go 1.26.11", ""},
		{"go1.26.11\r", ""},
		{"gogo1.26.11", ""},
		{"go1.26.11-local", ""},
		{"go1.26.11+auto", ""},
		{"path", ""},
	} {
		got := ""
		if m := toolchainRelease.FindStringSubmatch(tc.line); m != nil {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("toolchain %q reads release %q, want %q", tc.line, got, tc.want)
		}
	}
}

// A go line equal to the toolchain line is not above it, a very large patch
// number still compares numerically, and a go line one patch above the
// toolchain is caught.
func TestGoLineAgainstToolchainBoundaries(t *testing.T) {
	for _, tc := range []struct {
		toolchain, goLine string
		ok                bool
	}{
		{"1.26.11", "1.26.11", true},
		{"1.26.11", "1.26.12", false},
		{"1.26.11", "1.27.0", false},
		{"1.26.11", "2.0.0", false},
		{"1.26.4294967296", "1.26.11", true},
		{"1.26.11", "1.26.4294967296", false},
		{"1.26.011", "1.26.11", true},
		{"1.26.11", "1.26.0", true},
	} {
		if got := goVersionAtLeast(t, tc.toolchain, tc.goLine); got != tc.ok {
			t.Errorf("toolchain %s not below go %s = %v, want %v", tc.toolchain, tc.goLine, got, tc.ok)
		}
	}
}

// The build stage pattern accepts exactly the canonical spelling once the
// reader has trimmed the line, and nothing that could hide another release or
// an unpinned base: a platform flag, a lower-case `as`, an upper-case or short
// digest, a registry prefix, a variant suffix or a second tag.
func TestBuildStagePatternEdgeSpellings(t *testing.T) {
	const digest = "@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f"
	for _, tc := range []struct {
		line    string
		release string
		pinned  bool
	}{
		{"FROM golang:1.26.11" + digest + " AS build", "1.26.11", true},
		{"FROM golang:1.26.11" + digest + " AS build   ", "1.26.11", true},
		{"\tFROM golang:1.26.11" + digest + " AS build", "1.26.11", true},
		{"FROM golang:1.26.11 AS build", "1.26.11", false},
		{"FROM --platform=linux/amd64 golang:1.26.11" + digest + " AS build", "", false},
		{"FROM --platform=$BUILDPLATFORM golang:1.26.11" + digest + " AS build", "", false},
		{"FROM golang:1.26.11" + digest + " as build", "", false},
		{"from golang:1.26.11" + digest + " AS build", "", false},
		{"FROM golang:1.26.11" + strings.ToUpper(digest[8:]) + " AS build", "", false},
		{"FROM golang:1.26.11@sha256:" + strings.ToUpper(digest[8:]) + " AS build", "", false},
		{"FROM golang:1.26.11" + digest[:len(digest)-1] + " AS build", "", false},
		{"FROM golang:1.26.11" + digest + "0 AS build", "", false},
		{"FROM golang:1.26.11-alpine" + digest + " AS build", "", false},
		{"FROM golang:1.26.11:1.26.10" + digest + " AS build", "", false},
		{"FROM docker.io/library/golang:1.26.11" + digest + " AS build", "", false},
		{"FROM golang:1.26.11" + digest + " AS builder", "", false},
		{"FROM golang:1.26.11" + digest + "  AS build", "", false},
	} {
		got, pinned := "", false
		if m := buildStage.FindStringSubmatch(strings.TrimSpace(tc.line)); m != nil {
			got, pinned = m[1], m[2] != ""
		}
		if got != tc.release || pinned != tc.pinned {
			t.Errorf("build stage %q reads release %q pinned %v, want %q pinned %v", tc.line, got, pinned, tc.release, tc.pinned)
		}
	}
}
