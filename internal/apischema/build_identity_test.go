package apischema

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Two lines of go.mod name a Go release, and they mean different things.
//
// The go line is the oldest Go that may build the module. It stays where a
// workstation that is one patch release behind can still run every go command,
// so it is not what CI or the image are built with.
//
// The toolchain line is the release CI and the image are built with. It is
// also what the vulnerability scan in CI (govulncheck) reads the standard
// library of, so a stale patch level turns the badge red without any change to
// our code. 1.26.11 is the first 1.26 patch release on which the scan is
// clean; the toolchain line must not slip back below it. A go command that
// runs with GOTOOLCHAIN=local treats the line as a suggestion and builds with
// the Go it has.
const minGoRelease = "1.26.11"

// goVersionAtLeast reports whether the dotted release v is at or above want,
// comparing each numeric part, so 1.26.10 is above 1.26.9.
func goVersionAtLeast(t *testing.T, v, want string) bool {
	t.Helper()
	parts := func(s string) []int {
		var out []int
		for _, p := range strings.Split(s, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				t.Fatalf("version %q is not a dotted release: %v", s, err)
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parts(v), parts(want)
	if len(a) != 3 {
		t.Fatalf("version %q wants three parts (major.minor.patch)", v)
	}
	for i := range b {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

// parseGoMod reads the go and toolchain directives of a go.mod, field by field
// the way the go command reads them: a tab instead of a space or a trailing
// comment changes nothing. Each directive found is returned with its arguments
// joined by a space, so a line with extra fields cannot pass for a plain one.
func parseGoMod(lines []string) (goLines, toolchainLines []string) {
	for _, l := range lines {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "go":
			goLines = append(goLines, strings.Join(f[1:], " "))
		case "toolchain":
			toolchainLines = append(toolchainLines, strings.Join(f[1:], " "))
		}
	}
	return goLines, toolchainLines
}

var (
	fullRelease      = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	toolchainRelease = regexp.MustCompile(`^go([0-9]+\.[0-9]+\.[0-9]+)$`)
)

// goModPins returns the release of the go line and the release of the
// toolchain line (without its `go` prefix), and fails the test unless go.mod
// carries exactly one of each and both are a full X.Y.Z release.
func goModPins(t *testing.T) (goLine, toolchain string) {
	t.Helper()
	goLines, toolchainLines := parseGoMod(trimmedLines(t, "../../go.mod"))
	if len(goLines) != 1 {
		t.Fatalf("go.mod has %d go directives, want 1: %q", len(goLines), goLines)
	}
	if len(toolchainLines) != 1 {
		t.Fatalf("go.mod has %d toolchain lines, want 1: %q", len(toolchainLines), toolchainLines)
	}
	if !fullRelease.MatchString(goLines[0]) {
		t.Fatalf("go.mod says go %q: want a full release, `go X.Y.Z`", goLines[0])
	}
	m := toolchainRelease.FindStringSubmatch(toolchainLines[0])
	if m == nil {
		t.Fatalf("go.mod says toolchain %q: want `toolchain goX.Y.Z`", toolchainLines[0])
	}
	return goLines[0], m[1]
}

func TestGoModToolchainIsAPatchedGoRelease(t *testing.T) {
	_, toolchain := goModPins(t)
	if !goVersionAtLeast(t, toolchain, minGoRelease) {
		t.Errorf("go.mod says toolchain go%s: want go%s or newer, the first 1.26 release with a clean vulnerability scan", toolchain, minGoRelease)
	}
}

// A go line above the toolchain line contradicts it: the module would demand
// an older Go than the one it is built with, the wrong way round.
func TestGoModGoLineIsNotAboveTheToolchainLine(t *testing.T) {
	goLine, toolchain := goModPins(t)
	if !goVersionAtLeast(t, toolchain, goLine) {
		t.Errorf("go.mod says go %s above toolchain go%s", goLine, toolchain)
	}
}

func TestParseGoModReadsDirectivesFieldByField(t *testing.T) {
	for _, tc := range []struct {
		name          string
		lines         []string
		wantGo, wantT []string
	}{
		{"plain", []string{"go 1.26.10", "toolchain go1.26.11"}, []string{"1.26.10"}, []string{"go1.26.11"}},
		{"tabs and comments", []string{"go\t1.26.10 // oldest", "toolchain\tgo1.26.11\t// built with"}, []string{"1.26.10"}, []string{"go1.26.11"}},
		{"extra fields stay visible", []string{"go 1.26.10 extra", "toolchain go1.26.11 go1.27.0"}, []string{"1.26.10 extra"}, []string{"go1.26.11 go1.27.0"}},
		{"no toolchain", []string{"module x", "go 1.26.10"}, []string{"1.26.10"}, nil},
		{"two of each", []string{"go 1.26.10", "go 1.26.11", "toolchain go1.26.11", "toolchain go1.26.12"}, []string{"1.26.10", "1.26.11"}, []string{"go1.26.11", "go1.26.12"}},
		{"a commented-out toolchain is none", []string{"go 1.26.10", "// toolchain go1.26.11"}, []string{"1.26.10"}, nil},
		{"a module path that starts with go is no directive", []string{"module goexample", "require golang.org/x/text v0.14.0"}, nil, nil},
	} {
		gotGo, gotT := parseGoMod(tc.lines)
		if !slices.Equal(gotGo, tc.wantGo) || !slices.Equal(gotT, tc.wantT) {
			t.Errorf("%s: parseGoMod = %q, %q; want %q, %q", tc.name, gotGo, gotT, tc.wantGo, tc.wantT)
		}
	}
}

func TestToolchainLineMustBeAFullRelease(t *testing.T) {
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"go1.26.11", true},
		{"go1.26.100", true},
		{"go1.26", false},
		{"1.26.11", false},
		{"go1.26.11-rc1", false},
		{"go1.26.11 go1.27.0", false},
		{"default", false},
		{"local", false},
		{"", false},
	} {
		if got := toolchainRelease.MatchString(tc.line); got != tc.ok {
			t.Errorf("toolchain %q accepted = %v, want %v", tc.line, got, tc.ok)
		}
	}
}

func TestGoVersionAtLeastComparesNumerically(t *testing.T) {
	for _, tc := range []struct {
		v, want string
		ok      bool
	}{
		{"1.26.10", "1.26.10", true},
		{"1.26.9", "1.26.10", false},
		{"1.26.6", "1.26.10", false},
		{"1.26.11", "1.26.10", true},
		{"1.27.0", "1.26.10", true},
		{"1.25.99", "1.26.10", false},
		{"2.0.0", "1.26.10", true},
		{"1.26.11", "1.26.11", true},
		{"1.26.10", "1.26.11", false},
		{"1.26.9", "1.26.11", false},
		{"1.26.12", "1.26.11", true},
		{"1.26.100", "1.26.11", true},
		{"1.27.0", "1.26.11", true},
		{"1.25.99", "1.26.11", false},
	} {
		if got := goVersionAtLeast(t, tc.v, tc.want); got != tc.ok {
			t.Errorf("goVersionAtLeast(%q, %q) = %v, want %v", tc.v, tc.want, got, tc.ok)
		}
	}
}

var buildStage = regexp.MustCompile(`^FROM golang:([0-9]+\.[0-9]+\.[0-9]+)(@sha256:[0-9a-f]{64})? AS build$`)

// The image is built from the release of go.mod's toolchain line, the one CI
// builds with and the vulnerability scan reads, and by digest: a tag is a
// moving pointer, so the digest is what keeps "it built last week" meaning
// something. A build stage on another release would ship a binary compiled by
// a standard library the scan never looked at. It is the toolchain line that
// it must match, not the go line, which may sit a patch release behind.
func TestBuildImageGoVersionMatchesTheToolchainLine(t *testing.T) {
	_, want := goModPins(t)
	var stages []string
	for _, l := range trimmedLines(t, "../../Dockerfile") {
		if strings.HasPrefix(l, "FROM golang:") {
			stages = append(stages, l)
		}
	}
	if len(stages) != 1 {
		t.Fatalf("Dockerfile has %d golang build stages, want exactly 1: %q", len(stages), stages)
	}
	m := buildStage.FindStringSubmatch(stages[0])
	if m == nil {
		t.Fatalf("Dockerfile build stage %q is not `FROM golang:X.Y.Z@sha256:<64 hex> AS build`", stages[0])
	}
	if m[1] != want {
		t.Errorf("Dockerfile builds on golang:%s but go.mod says toolchain go%s", m[1], want)
	}
	if !goVersionAtLeast(t, m[1], minGoRelease) {
		t.Errorf("Dockerfile builds on golang:%s: want %s or newer, the first 1.26 release with a clean vulnerability scan", m[1], minGoRelease)
	}
	if m[2] == "" {
		t.Errorf("Dockerfile build stage %q is not pinned by a sha256 digest", stages[0])
	}
}

// startStackStep returns the lines of the acceptance workflow's step that runs
// `docker compose up -d --build`: from its `- name:` line to the next step.
func startStackStep(t *testing.T) []string {
	t.Helper()
	lines := trimmedLines(t, "../../.github/workflows/acceptance.yml")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "- name:") {
			start = i
		}
		if strings.Contains(l, "docker compose up -d --build") && !strings.HasPrefix(l, "#") {
			break
		}
	}
	if start < 0 {
		t.Fatal("acceptance.yml has no step that runs `docker compose up -d --build`")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- ") {
			end = i
			break
		}
	}
	step := lines[start:end]
	if !strings.Contains(strings.Join(step, "\n"), "docker compose up -d --build") {
		t.Fatal("the step before `docker compose up -d --build` does not contain it")
	}
	return step
}

// beforeYAMLComment cuts a YAML scalar at its trailing comment: a `#` that
// follows whitespace starts one, while a `#` glued to the text (as in "v1#rc")
// is part of the value. v is what follows `NAME: `, so the space the caller
// stripped counts as the whitespace before a `#` at index 0: `NAME: # note`
// is an empty value, which compose treats as unset.
func beforeYAMLComment(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			return v[:i]
		}
	}
	return v
}

// stampValue returns what the step assigns to name, written either as a shell
// prefix (`NAME=value \`) or as an `env:` entry (`NAME: value`), and whether it
// assigns it at all.
func stampValue(step []string, name string) (string, bool) {
	for _, l := range step {
		for _, sep := range []string{"=", ": "} {
			if rest, ok := strings.CutPrefix(l, name+sep); ok {
				if sep == ": " {
					rest = beforeYAMLComment(rest)
				}
				rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), `\`))
				return strings.Trim(rest, `"'`), true
			}
		}
	}
	return "", false
}

// GET /api/meta reports the build identity the image was stamped with, and the
// live acceptance run asserts it is a real one. docker-compose.yml maps VERSION,
// COMMIT and BUILT from the environment into the build arguments, and falls
// back to the honest unstamped labels when they are unset; so the workflow that
// builds the stack for that run has to provide all three, or the run fails on
// an unstamped image.
func TestAcceptanceWorkflowStampsTheStackBuild(t *testing.T) {
	step := startStackStep(t)
	unstamped := map[string]bool{"dev": true, "none": true, "unknown": true}
	for _, name := range []string{"VERSION", "COMMIT", "BUILT"} {
		v, ok := stampValue(step, name)
		if !ok {
			t.Errorf("the stack-start step does not provide %s: the image is built unstamped", name)
			continue
		}
		if v == "" || unstamped[v] {
			t.Errorf("the stack-start step sets %s to %q, an unstamped label", name, v)
		}
	}
}

func TestStampValueReadsBothSpellings(t *testing.T) {
	step := []string{
		"- name: Start the stack",
		"env:",
		"VERSION: ${{ github.ref_name }}",
		`COMMIT="${GITHUB_SHA:0:7}" \`,
		"BUILT: 'dev'",
		"run: docker compose up -d --build",
	}
	for name, want := range map[string]string{
		"VERSION": "${{ github.ref_name }}",
		"COMMIT":  "${GITHUB_SHA:0:7}",
		"BUILT":   "dev",
	} {
		if got, ok := stampValue(step, name); !ok || got != want {
			t.Errorf("stampValue(%s) = %q, %v; want %q, true", name, got, ok, want)
		}
	}
	// A `#` glued to the text is part of a shell value, not a comment.
	shell := []string{
		`COMMIT="${GITHUB_SHA:0:7}" \`,
		`BUILT="$(date -u +%FT%TZ)" \`,
		`TAG=a#b \`,
	}
	for name, want := range map[string]string{
		"COMMIT": "${GITHUB_SHA:0:7}",
		"BUILT":  "$(date -u +%FT%TZ)",
		"TAG":    "a#b",
	} {
		if got, ok := stampValue(shell, name); !ok || got != want {
			t.Errorf("stampValue(%s) = %q, %v; want %q, true", name, got, ok, want)
		}
	}
	// YAML values end at a comment that follows whitespace, and a value that
	// starts with one is empty.
	for _, tc := range []struct{ line, want string }{
		{"V: # c", ""},
		{"V: #", ""},
		{"V: ''  # c", ""},
		{"V: v1#rc", "v1#rc"},
		{"V: v1 # c", "v1"},
		{"V: v1\t# c", "v1"},
	} {
		if got, ok := stampValue([]string{tc.line}, "V"); !ok || got != tc.want {
			t.Errorf("stampValue(%q) = %q, %v; want %q, true", tc.line, got, ok, tc.want)
		}
	}
	// A tab straight after the colon is not the `NAME: value` form: no entry.
	if got, ok := stampValue([]string{"V:\t# c"}, "V"); ok && got != "" {
		t.Errorf("stampValue of a tab-only entry = %q, %v; want no entry or empty", got, ok)
	}
	if _, ok := stampValue(step, "MISSING"); ok {
		t.Error("stampValue found a name the step does not set")
	}
}
