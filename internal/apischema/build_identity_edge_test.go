package apischema

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// YAML ends a plain or quoted scalar at ` #`, so `BUILT: unknown # placeholder`
// sets BUILT to "unknown". The stamp reader has to see that value, or an
// unstamped label with a comment beside it would pass for a real one.
func TestStampValueReadsTheYAMLValueBeforeATrailingComment(t *testing.T) {
	for _, tc := range []struct{ line, name, want string }{
		{"BUILT: unknown # placeholder until the release job", "BUILT", "unknown"},
		{`VERSION: "dev"  # local label`, "VERSION", "dev"},
		{"COMMIT: 'none'\t# not stamped yet", "COMMIT", "none"},
		{"VERSION: ${{ github.ref_name }}", "VERSION", "${{ github.ref_name }}"},
	} {
		got, ok := stampValue([]string{tc.line}, tc.name)
		if !ok || got != tc.want {
			t.Errorf("stampValue(%q, %s) = %q, %v; want %q, true", tc.line, tc.name, got, ok, tc.want)
		}
	}
}

// The stamp only reaches the image when docker compose sees it in its
// environment. Shell assignments do that only as a prefix of the same command,
// joined by line continuations; a bare `VERSION=x` on a line of its own sets an
// unexported shell variable that docker compose never reads. An `env:` entry on
// the step is the other spelling that works.
func TestStackStartStampsArePrefixedToTheComposeCommand(t *testing.T) {
	step := startStackStep(t)
	inEnv := false
	for _, l := range step {
		if l == "env:" {
			inEnv = true
		}
	}
	compose := -1
	for i, l := range step {
		if strings.Contains(l, "docker compose up -d --build") && !strings.HasPrefix(l, "#") {
			compose = i
			break
		}
	}
	if compose < 0 {
		t.Fatal("the stack-start step has no `docker compose up -d --build` line")
	}
	for _, name := range []string{"VERSION", "COMMIT", "BUILT"} {
		if inEnv && slices.ContainsFunc(step, func(l string) bool { return strings.HasPrefix(l, name+": ") }) {
			continue
		}
		at := -1
		for i, l := range step {
			if strings.HasPrefix(l, name+"=") {
				at = i
				break
			}
		}
		if at < 0 {
			if !inEnv {
				t.Errorf("the stack-start step assigns %s nowhere", name)
			}
			continue
		}
		if at > compose {
			t.Errorf("%s is assigned after `docker compose up` runs", name)
			continue
		}
		if strings.HasPrefix(step[compose], name+"=") {
			continue
		}
		for i := at; i < compose; i++ {
			if !strings.HasSuffix(step[i], `\`) {
				t.Errorf("line %q between the %s assignment and `docker compose up` does not continue the command: %s is never exported to it", step[i], name, name)
				break
			}
		}
	}
}

// The image job in ci.yml and the acceptance stack stamp the same identity from
// the same GitHub variables, so a build from either can be traced to the same
// ref and commit.
func TestAcceptanceStampMatchesTheImageJob(t *testing.T) {
	ci := trimmedLines(t, "../../.github/workflows/ci.yml")
	step := startStackStep(t)
	for _, tc := range []struct{ name, value string }{
		{"VERSION", "${GITHUB_REF_NAME}"},
		{"COMMIT", "${GITHUB_SHA:0:7}"},
		{"BUILT", "$(date -u +%FT%TZ)"},
	} {
		ciLine := `--build-arg ` + tc.name + `="` + tc.value + `" \`
		if !slices.Contains(ci, ciLine) {
			t.Errorf("ci.yml's image job has no line %q", ciLine)
		}
		if got, ok := stampValue(step, tc.name); !ok || got != tc.value {
			t.Errorf("acceptance.yml stamps %s as %q, %v; ci.yml stamps it as %q", tc.name, got, ok, tc.value)
		}
	}
	accept := trimmedLines(t, "../../.github/workflows/acceptance.yml")
	if !slices.Contains(accept, "go-version-file: go.mod") {
		t.Error("acceptance.yml no longer reads its Go release from go.mod")
	}
}

// Every stage is pinned by digest, not only the build stage: the runtime base
// is what ships. It stays the :nonroot variant, so the server never runs as
// root.
func TestDockerfileEveryStageIsDigestPinnedAndRuntimeIsNonroot(t *testing.T) {
	pinned := regexp.MustCompile(`^FROM [^ @]+:[^ @]+@sha256:[0-9a-f]{64}( AS [a-z]+)?$`)
	var froms []string
	for _, l := range trimmedLines(t, "../../Dockerfile") {
		if strings.HasPrefix(strings.ToUpper(l), "FROM ") {
			froms = append(froms, l)
		}
	}
	if len(froms) != 2 {
		t.Fatalf("Dockerfile has %d stages, want the build and the runtime stage: %q", len(froms), froms)
	}
	for _, f := range froms {
		if !pinned.MatchString(f) {
			t.Errorf("stage %q is not `FROM image:tag@sha256:<64 hex>`", f)
		}
	}
	if !strings.Contains(froms[1], "gcr.io/distroless/static-debian12:nonroot@sha256:") {
		t.Errorf("runtime stage %q is not distroless :nonroot", froms[1])
	}
}

// An unstamped local build has to stay honestly labelled: the Dockerfile's ARG
// defaults are internal/version's defaults, and each -X in the link flags names
// a variable that exists there and takes the matching ARG. A renamed variable
// would make -X stamp nothing, without any error from the linker.
func TestDockerfileStampDefaultsAndLinkFlagsMatchTheVersionPackage(t *testing.T) {
	const pkg = "github.com/AndresThePerez/pokesearch/internal/version"
	src := strings.Join(trimmedLines(t, "../version/version.go"), "\n")
	docker := trimmedLines(t, "../../Dockerfile")
	joined := strings.Join(docker, "\n")
	for _, tc := range []struct{ arg, goVar, def string }{
		{"VERSION", "Version", "dev"},
		{"COMMIT", "Commit", "none"},
		{"BUILT", "Built", "unknown"},
	} {
		decl := regexp.MustCompile(`(?m)^` + tc.goVar + `\s*=\s*"` + tc.def + `"$`)
		if !decl.MatchString(src) {
			t.Errorf("internal/version does not declare %s = %q", tc.goVar, tc.def)
		}
		if !slices.Contains(docker, "ARG "+tc.arg+"="+tc.def) {
			t.Errorf("Dockerfile has no `ARG %s=%s`: an unstamped build would be mislabelled", tc.arg, tc.def)
		}
		flag := "-X " + pkg + "." + tc.goVar + "=${" + tc.arg + "}"
		if !strings.Contains(joined, flag) {
			t.Errorf("Dockerfile's link flags lack %q", flag)
		}
	}
}

// docker-compose.yml is what carries the workflow's environment into the
// image's build arguments; without these three lines the stamp stops at the
// shell.
func TestComposeMapsTheStampIntoTheAppBuild(t *testing.T) {
	lines := trimmedLines(t, "../../docker-compose.yml")
	for _, want := range []string{
		"VERSION: ${VERSION:-dev}",
		"COMMIT: ${COMMIT:-none}",
		"BUILT: ${BUILT:-unknown}",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("docker-compose.yml has no build argument line %q", want)
		}
	}
}

// go.mod is read field by field, the way the go command reads it, so a tab
// instead of a space or a trailing comment cannot hide a toolchain line or
// smuggle a second go directive past the line-prefix reader.
func TestGoModHasOnePlainGoDirectiveAndNoToolchainInAnySpelling(t *testing.T) {
	var directives []string
	for _, l := range trimmedLines(t, "../../go.mod") {
		if i := strings.Index(l, "//"); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "toolchain":
			t.Errorf("go.mod has a toolchain line %q", l)
		case "go":
			directives = append(directives, strings.Join(f[1:], " "))
		}
	}
	if len(directives) != 1 {
		t.Fatalf("go.mod has %d go directives, want 1: %q", len(directives), directives)
	}
	if got := goModVersion(t); got != directives[0] {
		t.Errorf("the go directive reads %q field by field but %q by line prefix", directives[0], got)
	}
	if !goVersionAtLeast(t, directives[0], minGoRelease) {
		t.Errorf("go.mod says go %s, below %s", directives[0], minGoRelease)
	}
}

// A module go.mod requires without its go.sum lines fails `go mod download`
// in the image build with -mod=readonly; the checksums have to travel with the
// requirement.
func TestGoSumCoversEveryRequiredModule(t *testing.T) {
	sum := map[string]bool{}
	for _, l := range trimmedLines(t, "../../go.sum") {
		if f := strings.Fields(l); len(f) == 3 {
			sum[f[0]+" "+f[1]] = true
		}
	}
	inBlock := false
	n := 0
	for _, l := range trimmedLines(t, "../../go.mod") {
		var mod []string
		switch {
		case l == "require (":
			inBlock = true
			continue
		case inBlock && l == ")":
			inBlock = false
			continue
		case inBlock:
			mod = strings.Fields(l)
		case strings.HasPrefix(l, "require "):
			mod = strings.Fields(strings.TrimPrefix(l, "require "))
		}
		if len(mod) < 2 {
			continue
		}
		n++
		path, ver := mod[0], mod[1]
		if !sum[path+" "+ver+"/go.mod"] {
			t.Errorf("go.sum has no go.mod hash for %s %s", path, ver)
		}
		if !sum[path+" "+ver] {
			t.Errorf("go.sum has no module hash for %s %s", path, ver)
		}
	}
	if n == 0 {
		t.Fatal("found no requirements in go.mod")
	}
}

// A YAML value that is only a comment is null: `VERSION: # set later` leaves
// VERSION empty, docker-compose.yml falls back to "dev", and the image is built
// unstamped. The stamp reader must see an empty value there, while a `#` inside
// a quoted or glued value is no comment and an empty quoted value stays empty.
func TestStampValueTreatsACommentOnlyYAMLValueAsEmpty(t *testing.T) {
	for _, tc := range []struct{ line, name, want string }{
		{"VERSION: # set by the release job", "VERSION", ""},
		{"COMMIT: #", "COMMIT", ""},
		{"BUILT:\t# later", "BUILT", ""},
		{"BUILT: ''  # empty on purpose", "BUILT", ""},
		{"VERSION: v1#rc", "VERSION", "v1#rc"},
	} {
		got, ok := stampValue([]string{tc.line}, tc.name)
		if ok && got != tc.want {
			t.Errorf("stampValue(%q, %s) = %q; want %q", tc.line, tc.name, got, tc.want)
		}
	}
}
