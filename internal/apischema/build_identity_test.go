package apischema

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The vulnerability scan in CI (govulncheck) reads the standard library of the
// Go release the module pins, so a stale patch level turns the badge red
// without any change to our code. 1.26.10 is the first 1.26 patch release on
// which the scan is clean; the pin must not slip back below it.
const minGoRelease = "1.26.10"

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

// goModVersion returns the go directive of go.mod and fails the test when the
// file carries a toolchain line: a toolchain line can silently pick a different
// compiler than the directive, which is exactly the drift this guards against.
func goModVersion(t *testing.T) string {
	t.Helper()
	version := ""
	for _, l := range trimmedLines(t, "../../go.mod") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == "toolchain" {
			t.Errorf("go.mod has a toolchain line %q: the go directive alone names the release", l)
		}
		if strings.HasPrefix(l, "go ") && version == "" {
			version = strings.TrimSpace(strings.TrimPrefix(l, "go "))
		}
	}
	if version == "" {
		t.Fatal("go.mod has no go directive")
	}
	return version
}

func TestGoModPinsAPatchedGoRelease(t *testing.T) {
	version := goModVersion(t)
	if !goVersionAtLeast(t, version, minGoRelease) {
		t.Errorf("go.mod says go %s: want %s or newer, the first 1.26 release with a clean vulnerability scan", version, minGoRelease)
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
	} {
		if got := goVersionAtLeast(t, tc.v, tc.want); got != tc.ok {
			t.Errorf("goVersionAtLeast(%q, %q) = %v, want %v", tc.v, tc.want, got, tc.ok)
		}
	}
}

var buildStage = regexp.MustCompile(`^FROM golang:([0-9]+\.[0-9]+\.[0-9]+)(@sha256:[0-9a-f]{64})? AS build$`)

// The image is built from the same Go release the module pins, and by digest:
// a tag is a moving pointer, so the digest is what keeps "it built last week"
// meaning something. A build stage on another release than go.mod would ship a
// binary compiled by a compiler the tests never ran under.
func TestBuildImageGoVersionMatchesGoMod(t *testing.T) {
	want := goModVersion(t)
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
		t.Errorf("Dockerfile builds on golang:%s but go.mod says go %s", m[1], want)
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
// follows whitespace starts one, while a `#` glued to the text (as in
// "${GITHUB_SHA:0:7}") is part of the value.
func beforeYAMLComment(v string) string {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
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
	if _, ok := stampValue(step, "MISSING"); ok {
		t.Error("stampValue found a name the step does not set")
	}
}
