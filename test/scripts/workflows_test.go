package scripts_test

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
)

// The workflow, dependabot and goreleaser files are checked structurally with
// a small indentation parser: the module has no direct YAML dependency and
// these files use only block mappings, block sequences, flow lists and block
// scalars. The schemas are validated separately, by actionlint (the CI lint
// job and `make lint`) and by `goreleaser check` (the CI goreleaser job).

// yamlEntry is one scalar of a YAML document with the keys leading to it.
// Sequence items appear in the path as "-<n>", numbered through the file so
// entries of the same item share a prefix.
type yamlEntry struct {
	path  []string
	value string
	line  int
}

func (e yamlEntry) key() string { return e.path[len(e.path)-1] }

// at reports whether the entry path matches pattern, where "-" matches any
// sequence item.
func (e yamlEntry) at(pattern ...string) bool {
	if len(e.path) != len(pattern) {
		return false
	}
	for i, p := range pattern {
		if p != e.path[i] && (p != "-" || !strings.HasPrefix(e.path[i], "-")) {
			return false
		}
	}
	return true
}

type yamlFrame struct {
	indent int
	key    string
}

func parseYAML(t *testing.T, path string) []yamlEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var (
		entries []yamlEntry
		stack   []yamlFrame
		items   int
		block   = -1 // indent of the key owning a block scalar, or -1
	)
	pathOf := func() []string {
		p := make([]string, len(stack))
		for i, f := range stack {
			p[i] = f.key
		}
		return p
	}
	for n, raw := range strings.Split(string(data), "\n") {
		content := strings.TrimLeft(raw, " ")
		indent := len(raw) - len(content)
		if block >= 0 {
			if content == "" || indent > block {
				last := &entries[len(entries)-1]
				if last.value != "" || content != "" {
					last.value += strings.TrimPrefix(raw, strings.Repeat(" ", min(len(raw), block+2))) + "\n"
				}
				continue
			}
			block = -1
		}
		if content == "" || strings.HasPrefix(content, "#") {
			continue
		}
		for strings.HasPrefix(content, "- ") || content == "-" {
			for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
				stack = stack[:len(stack)-1]
			}
			items++
			stack = append(stack, yamlFrame{indent: indent, key: fmt.Sprintf("-%d", items)})
			content = strings.TrimPrefix(strings.TrimPrefix(content, "-"), " ")
			indent += 2
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		key, value, isMap := splitYAMLKey(content)
		if !isMap {
			entries = append(entries, yamlEntry{path: pathOf(), value: unquoteYAML(content), line: n + 1})
			continue
		}
		stack = append(stack, yamlFrame{indent: indent, key: key})
		if value == "" {
			continue
		}
		switch value {
		case "|", "|-", ">", ">-":
			block = indent
			value = ""
		}
		entries = append(entries, yamlEntry{path: pathOf(), value: unquoteYAML(value), line: n + 1})
	}
	return entries
}

// splitYAMLKey splits "key: value" (or "key:"), ignoring colons inside quotes
// and in values such as URLs.
func splitYAMLKey(s string) (key, value string, ok bool) {
	if strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'") {
		return "", "", false
	}
	i := strings.Index(s, ": ")
	if i < 0 {
		if strings.HasSuffix(s, ":") && !strings.ContainsAny(s, " ") {
			return strings.TrimSuffix(s, ":"), "", true
		}
		return "", "", false
	}
	return s[:i], strings.TrimSpace(s[i+2:]), true
}

func unquoteYAML(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func find(entries []yamlEntry, pattern ...string) []yamlEntry {
	var out []yamlEntry
	for _, e := range entries {
		if e.at(pattern...) {
			out = append(out, e)
		}
	}
	return out
}

func values(entries []yamlEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.value
	}
	return out
}

// steps groups a job's step entries by sequence item, keyed "uses" or "run"
// plus the remaining keys (with. and env. prefixed).
func steps(entries []yamlEntry, job string) []map[string]string {
	var out []map[string]string
	index := map[string]int{}
	for _, e := range entries {
		if len(e.path) < 4 || e.path[0] != "jobs" || e.path[1] != job || e.path[2] != "steps" {
			continue
		}
		item := e.path[3]
		i, ok := index[item]
		if !ok {
			i = len(out)
			index[item] = i
			out = append(out, map[string]string{})
		}
		out[i][strings.Join(e.path[4:], ".")] = e.value
	}
	return out
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), filepath.FromSlash(name))
}

var workflowFiles = []string{".github/workflows/ci.yml", ".github/workflows/release.yml"}

func TestParseYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.yml")
	doc := "a:\n  b: 1 # note\n  list:\n    - x\n    - k: v\n      j: w\n  run: |\n    echo ${{ x }}\n\n    done\n  flow: [p, q]\nc: \"q: r\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0)
	for _, e := range parseYAML(t, path) {
		got = append(got, strings.Join(e.path, ".")+"="+e.value)
	}
	want := []string{"a.b=1 # note", "a.list.-1=x", "a.list.-2.k=v", "a.list.-2.j=w", "a.run=echo ${{ x }}\n\ndone\n", "a.flow=[p, q]", "c=q: r"}
	if !slices.Equal(got, want) {
		t.Fatalf("parse\n got  %q\n want %q", got, want)
	}
}

var (
	pinnedUse = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?)@([0-9a-f]{40}) # (v[0-9]+\.[0-9]+\.[0-9]+)$`)
	// A reusable workflow of this repository runs at the same commit as the
	// workflow calling it, so the path is the pin.
	localWorkflow = regexp.MustCompile(`^\./\.github/workflows/[a-z0-9-]+\.yml$`)
)

func TestWorkflowActionsArePinned(t *testing.T) {
	pins := map[string]string{}
	for _, file := range workflowFiles {
		entries := parseYAML(t, repoFile(t, file))
		var uses []yamlEntry
		for _, e := range entries {
			if e.key() == "uses" {
				uses = append(uses, e)
			}
		}
		if len(uses) == 0 {
			t.Fatalf("%s: no actions found", file)
		}
		for _, e := range uses {
			if strings.HasPrefix(e.value, "./") {
				if !localWorkflow.MatchString(e.value) {
					t.Fatalf("%s:%d: %q is not a workflow of this repository", file, e.line, e.value)
				}
				if _, err := os.Stat(repoFile(t, strings.TrimPrefix(e.value, "./"))); err != nil {
					t.Fatalf("%s:%d: %v", file, e.line, err)
				}
				continue
			}
			m := pinnedUse.FindStringSubmatch(e.value)
			if m == nil {
				t.Fatalf("%s:%d: %q is not pinned to a commit SHA with a version comment", file, e.line, e.value)
			}
			repo := strings.Join(strings.Split(m[1], "/")[:2], "/")
			if prev, ok := pins[repo]; ok && prev != m[2]+" "+m[3] {
				t.Fatalf("%s:%d: %s pinned to %s %s, elsewhere %s", file, e.line, repo, m[2], m[3], prev)
			}
			pins[repo] = m[2] + " " + m[3]
		}
	}
}

func TestWorkflowPermissions(t *testing.T) {
	cases := []struct {
		file string
		// jobs lists the job-level permissions each job may declare.
		jobs map[string]map[string]string
	}{
		{file: ".github/workflows/ci.yml", jobs: map[string]map[string]string{}},
		{file: ".github/workflows/release.yml", jobs: map[string]map[string]string{
			"verify":  {"contents": "read"},
			"release": {"contents": "write", "id-token": "write", "attestations": "write"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			entries := parseYAML(t, repoFile(t, tc.file))
			top := map[string]string{}
			got := map[string]map[string]string{}
			for _, e := range entries {
				switch {
				case len(e.path) == 2 && e.path[0] == "permissions":
					top[e.path[1]] = e.value
				case len(e.path) == 4 && e.path[0] == "jobs" && e.path[2] == "permissions":
					if got[e.path[1]] == nil {
						got[e.path[1]] = map[string]string{}
					}
					got[e.path[1]][e.path[3]] = e.value
				case e.key() == "permissions":
					t.Fatalf("%s:%d: permissions must be a mapping, got %q", tc.file, e.line, e.value)
				}
			}
			if !maps.Equal(top, map[string]string{"contents": "read"}) {
				t.Fatalf("top-level permissions = %v, want contents: read only", top)
			}
			if len(got) != len(tc.jobs) {
				t.Fatalf("job permissions = %v, want %v", got, tc.jobs)
			}
			for job, want := range tc.jobs {
				if !maps.Equal(got[job], want) {
					t.Fatalf("job %s permissions = %v, want %v", job, got[job], want)
				}
			}
		})
	}
}

func TestWorkflowRunScriptsHaveNoExpressions(t *testing.T) {
	for _, file := range workflowFiles {
		for _, e := range parseYAML(t, repoFile(t, file)) {
			// Expressions expanded into a shell script are an injection
			// vector; values reach scripts through env instead.
			if e.key() == "run" && strings.Contains(e.value, "${{") {
				t.Fatalf("%s:%d: run script contains an expression:\n%s", file, e.line, e.value)
			}
		}
	}
}

func TestWorkflowCheckoutsDropCredentials(t *testing.T) {
	for _, file := range workflowFiles {
		entries := parseYAML(t, repoFile(t, file))
		for _, job := range jobNames(entries) {
			for _, step := range steps(entries, job) {
				if strings.HasPrefix(step["uses"], "actions/checkout@") && step["with.persist-credentials"] != "false" {
					t.Fatalf("%s: job %s checks out with persisted credentials", file, job)
				}
			}
		}
	}
}

func jobNames(entries []yamlEntry) []string {
	var names []string
	for _, e := range entries {
		if len(e.path) >= 2 && e.path[0] == "jobs" && !slices.Contains(names, e.path[1]) {
			names = append(names, e.path[1])
		}
	}
	return names
}

// jobRuns joins the run scripts and action arguments of a job's steps.
func jobRuns(entries []yamlEntry, job string) string {
	var b strings.Builder
	for _, step := range steps(entries, job) {
		b.WriteString(step["uses"] + "\n" + step["run"] + "\n" + step["with.args"] + "\n")
	}
	return b.String()
}

func TestCIWorkflow(t *testing.T) {
	entries := parseYAML(t, repoFile(t, ".github/workflows/ci.yml"))
	if got, want := jobNames(entries), []string{"lint", "govulncheck", "test", "test-debian", "fuzz", "cover", "goreleaser"}; !slices.Equal(got, want) {
		t.Fatalf("jobs = %v, want %v", got, want)
	}
	if got := values(find(entries, "concurrency", "cancel-in-progress")); !slices.Equal(got, []string{"true"}) {
		t.Fatalf("concurrency cancel-in-progress = %v", got)
	}
	if got := values(find(entries, "on", "push", "branches")); !slices.Equal(got, []string{"[main]"}) {
		t.Fatalf("push branches = %v", got)
	}
	// "pull_request:" has no value, so the parser records no entry for it.
	if !strings.Contains(string(mustRead(t, repoFile(t, ".github/workflows/ci.yml"))), "\n  pull_request:\n") {
		t.Fatal("CI does not run on pull requests")
	}

	cases := []struct {
		job  string
		want []string
	}{
		{job: "lint", want: []string{"go mod verify", "go mod tidy -diff", "go vet ./...", "golangci/golangci-lint-action@", "shellcheck -x scripts/*.sh scripts/lib/*.sh lyna-tmux.tmux internal/devcontainer/templates/init-firewall.sh", "actionlint@${ACTIONLINT_VERSION}", "scripts/check-text.sh"}},
		{job: "govulncheck", want: []string{"golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}\" ./..."}},
		{job: "test", want: []string{"go test -race -shuffle=on -count=1 ./...", "brew install tmux", "sha256sum -c -", "actions/cache@", "neovim"}},
		{job: "test-debian", want: []string{"go test -race -shuffle=on -count=1 ./...", "safe.directory", "tmux", "gcc"}},
		{job: "fuzz", want: []string{"scripts/fuzz.sh"}},
		{job: "cover", want: []string{"make cover"}},
		{job: "goreleaser", want: []string{"anchore/sbom-action/download-syft@", "goreleaser/goreleaser-action@", "\ncheck\n", "release --snapshot --clean"}},
	}
	for _, tc := range cases {
		t.Run(tc.job, func(t *testing.T) {
			runs := jobRuns(entries, tc.job)
			for _, want := range tc.want {
				if !strings.Contains(runs, want) {
					t.Fatalf("job %s does not run %q:\n%s", tc.job, want, runs)
				}
			}
		})
	}

	if got := values(find(entries, "jobs", "test-debian", "container")); !slices.Equal(got, []string{"debian:12"}) {
		t.Fatalf("debian container = %v", got)
	}
	matrix := map[string]string{}
	for _, e := range find(entries, "jobs", "test", "strategy", "matrix", "include", "-", "os") {
		matrix[e.value] = e.value
	}
	tmuxVersions := values(find(entries, "jobs", "test", "strategy", "matrix", "include", "-", "tmux-version"))
	if matrix["ubuntu-24.04"] == "" || matrix["macos-latest"] == "" || !slices.Equal(tmuxVersions, []string{"3.6"}) {
		t.Fatalf("test matrix os = %v, tmux versions = %v", matrix, tmuxVersions)
	}
	// Every platform .goreleaser.yaml builds for is tested. macos-latest is
	// arm64, so darwin/amd64 needs an Intel image of its own: internal/procx
	// reads a sysctl whose layout depends on the architecture.
	if matrix["macos-15-intel"] == "" {
		t.Fatalf("test matrix os = %v, darwin/amd64 is a release target with no runner", matrix)
	}
	sums := values(find(entries, "jobs", "test", "strategy", "matrix", "include", "-", "tmux-sha256"))
	if len(sums) != 1 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sums[0]) {
		t.Fatalf("tmux source checksum = %v", sums)
	}
}

func TestReleaseWorkflow(t *testing.T) {
	entries := parseYAML(t, repoFile(t, ".github/workflows/release.yml"))
	if got := values(find(entries, "on", "push", "tags", "-")); !slices.Equal(got, []string{"v*"}) {
		t.Fatalf("release tags = %v", got)
	}
	if got := jobNames(entries); !slices.Equal(got, []string{"verify", "release"}) {
		t.Fatalf("jobs = %v", got)
	}
	var release, attest map[string]string
	for _, step := range steps(entries, "release") {
		switch {
		case strings.HasPrefix(step["uses"], "goreleaser/goreleaser-action@"):
			release = step
		case strings.HasPrefix(step["uses"], "actions/attest-build-provenance@"):
			attest = step
		}
	}
	if release == nil || release["with.args"] != "release --clean --draft" || release["env.GITHUB_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatalf("goreleaser step = %v", release)
	}
	if attest == nil {
		t.Fatal("no build provenance attestation")
	}
	subjects := strings.Fields(attest["with.subject-path"])
	for _, want := range []string{"dist/*.tar.gz", "dist/checksums.txt"} {
		if !slices.Contains(subjects, want) {
			t.Fatalf("attested subjects %v miss %s", subjects, want)
		}
	}
}

// TestReleaseVerifiesTheTaggedCommit joins CONTRIBUTING.md's "release on a
// green main" rule to the workflow: the tagged commit runs the CI gate itself,
// and the publishing job waits for it.
func TestReleaseVerifiesTheTaggedCommit(t *testing.T) {
	entries := parseYAML(t, repoFile(t, ".github/workflows/release.yml"))
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "the gate is the CI workflow", got: values(find(entries, "jobs", "verify", "uses")), want: []string{"./.github/workflows/ci.yml"}},
		{name: "the gate reads only", got: values(find(entries, "jobs", "verify", "permissions", "contents")), want: []string{"read"}},
		{name: "publishing waits for the gate", got: values(find(entries, "jobs", "release", "needs")), want: []string{"verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Fatalf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
	// A reusable workflow runs only when it accepts the call.
	if !strings.Contains(string(mustRead(t, repoFile(t, ".github/workflows/ci.yml"))), "\n  workflow_call:\n") {
		t.Fatal("CI cannot be called by the release workflow")
	}
}

// TestReleasePublishesOnlyAfterAttesting covers the promise SECURITY.md makes:
// every public release has a build provenance attestation. The release is
// created as a draft, attested, and made public last, so a failed attestation
// leaves nothing published.
func TestReleasePublishesOnlyAfterAttesting(t *testing.T) {
	all := steps(parseYAML(t, repoFile(t, ".github/workflows/release.yml")), "release")
	at := func(what string, match func(map[string]string) bool) int {
		for i, step := range all {
			if match(step) {
				return i
			}
		}
		t.Fatalf("the release job has no %s step", what)
		return -1
	}
	build := at("goreleaser", func(s map[string]string) bool { return strings.HasPrefix(s["uses"], "goreleaser/goreleaser-action@") })
	attest := at("attestation", func(s map[string]string) bool {
		return strings.HasPrefix(s["uses"], "actions/attest-build-provenance@")
	})
	publish := at("publish", func(s map[string]string) bool { return strings.Contains(s["run"], "gh release edit") })

	cases := []struct {
		name          string
		first, second int
	}{
		{name: "the artifacts exist before they are attested", first: build, second: attest},
		{name: "the release is attested before it is public", first: attest, second: publish},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.first >= tc.second {
				t.Fatalf("step %d does not run before step %d", tc.first, tc.second)
			}
		})
	}
	if args := all[build]["with.args"]; !strings.Contains(args, "--draft") {
		t.Fatalf("goreleaser args %q do not create a draft release", args)
	}
	if run := all[publish]["run"]; !strings.Contains(run, "--draft=false") {
		t.Fatalf("the publish step does not undraft the release: %q", run)
	}
	// The tag reaches the shell through the environment, never expanded into
	// the script.
	if got := all[publish]["env.TAG"]; got != "${{ github.ref_name }}" {
		t.Fatalf("the publish step takes the tag from %q", got)
	}
}

// makeRecipe returns the recipe lines of one Makefile target.
func makeRecipe(t *testing.T, target string) string {
	t.Helper()
	var b strings.Builder
	in := false
	for _, line := range strings.Split(string(mustRead(t, repoFile(t, "Makefile"))), "\n") {
		switch {
		case strings.HasPrefix(line, target+":"):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			b.WriteString(strings.TrimPrefix(line, "\t") + "\n")
		case in:
			return b.String()
		}
	}
	if !in {
		t.Fatalf("the Makefile has no %s target", target)
	}
	return b.String()
}

// TestLintRunsTheSameToolsEverywhere keeps `make lint` and the CI lint job from
// drifting apart: a tool that runs in only one of them leaves either
// contributors or the merge gate blind.
func TestLintRunsTheSameToolsEverywhere(t *testing.T) {
	recipe := makeRecipe(t, "lint") + makeRecipe(t, "tidy-check")
	ci := jobRuns(parseYAML(t, repoFile(t, ".github/workflows/ci.yml")), "lint")
	cases := []struct {
		name         string
		makefile, ci string
	}{
		{name: "module verification", makefile: "mod verify", ci: "go mod verify"},
		{name: "module tidiness", makefile: "mod tidy -diff", ci: "go mod tidy -diff"},
		{name: "go vet", makefile: "vet ./...", ci: "go vet ./..."},
		{name: "golangci-lint", makefile: "golangci-lint run", ci: "golangci/golangci-lint-action@"},
		{name: "shellcheck", makefile: "shellcheck -x scripts/*.sh scripts/lib/*.sh", ci: "shellcheck -x scripts/*.sh scripts/lib/*.sh"},
		{name: "actionlint", makefile: "actionlint", ci: "actionlint"},
		{name: "text guard", makefile: "scripts/check-text.sh", ci: "scripts/check-text.sh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(recipe, tc.makefile) {
				t.Fatalf("make lint does not run %q:\n%s", tc.makefile, recipe)
			}
			if !strings.Contains(ci, tc.ci) {
				t.Fatalf("the CI lint job does not run %q:\n%s", tc.ci, ci)
			}
		})
	}
	// A contributor without actionlint still gets the rest of the gate.
	if !strings.Contains(recipe, "command -v actionlint") {
		t.Fatalf("make lint requires actionlint to be installed:\n%s", recipe)
	}
}

// TestContributingPinsMatchCI keeps the prerequisites table from drifting away
// from the versions CI runs, which is what a contributor installs to reproduce
// a failure locally.
func TestContributingPinsMatchCI(t *testing.T) {
	ci := parseYAML(t, repoFile(t, ".github/workflows/ci.yml"))
	lines := strings.Split(string(mustRead(t, repoFile(t, "CONTRIBUTING.md"))), "\n")
	cases := []struct {
		tool, env string
	}{
		{tool: "golangci-lint", env: "GOLANGCI_LINT_VERSION"},
		{tool: "actionlint", env: "ACTIONLINT_VERSION"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			pinned := values(find(ci, "env", tc.env))
			if len(pinned) != 1 {
				t.Fatalf("ci.yml sets %s = %v", tc.env, pinned)
			}
			var row string
			for _, line := range lines {
				if strings.HasPrefix(line, "| "+tc.tool+" |") {
					row = line
				}
			}
			if row == "" {
				t.Fatalf("CONTRIBUTING.md lists no %s prerequisite", tc.tool)
			}
			if !strings.Contains(row, pinned[0]) {
				t.Fatalf("CONTRIBUTING.md asks for a different %s than CI runs (%s):\n%s", tc.tool, pinned[0], row)
			}
		})
	}
}

// TestCoverageFloorIsDocumented joins the enforced coverage gate to AGENTS.md,
// the binding document: the packages it names are the packages the Makefile
// measures, in both directions.
func TestCoverageFloorIsDocumented(t *testing.T) {
	makefile := string(mustRead(t, repoFile(t, "Makefile")))
	pkgs := regexp.MustCompile(`(?s)COVER_PKGS \?=(.*?)\n[A-Z]`).FindStringSubmatch(makefile)
	if pkgs == nil {
		t.Fatal("the Makefile has no COVER_PKGS")
	}
	var gated []string
	for _, field := range strings.Fields(pkgs[1]) {
		if strings.HasPrefix(field, "./") {
			gated = append(gated, strings.TrimPrefix(field, "./"))
		}
	}
	if len(gated) == 0 {
		t.Fatalf("no packages in COVER_PKGS: %q", pkgs[1])
	}

	var line string
	for _, l := range strings.Split(string(mustRead(t, repoFile(t, "AGENTS.md"))), "\n") {
		if strings.Contains(l, "Coverage gate") {
			line = l
		}
	}
	if line == "" {
		t.Fatal("AGENTS.md documents no coverage gate")
	}
	for _, pkg := range gated {
		t.Run(pkg, func(t *testing.T) {
			if !strings.Contains(line, "`"+pkg+"`") {
				t.Fatalf("AGENTS.md does not list %s as gated:\n%s", pkg, line)
			}
		})
	}
	for _, m := range regexp.MustCompile("`(internal/[^`]+)`").FindAllStringSubmatch(line, -1) {
		if !slices.Contains(gated, m[1]) {
			t.Fatalf("AGENTS.md claims %s is gated, COVER_PKGS gates %v", m[1], gated)
		}
	}
	floor := regexp.MustCompile(`COVER_MIN \?= (\d+)`).FindStringSubmatch(makefile)
	if floor == nil || !strings.Contains(line, floor[1]+"%") {
		t.Fatalf("AGENTS.md does not state the enforced floor %v:\n%s", floor, line)
	}
}

func TestDependabot(t *testing.T) {
	entries := parseYAML(t, repoFile(t, ".github/dependabot.yml"))
	ecosystems := values(find(entries, "updates", "-", "package-ecosystem"))
	intervals := values(find(entries, "updates", "-", "schedule", "interval"))
	if !slices.Equal(ecosystems, []string{"gomod", "github-actions"}) || !slices.Equal(intervals, []string{"weekly", "weekly"}) {
		t.Fatalf("ecosystems %v, intervals %v", ecosystems, intervals)
	}
}

// TestReleaseArtifactsMatchInstaller joins the release configuration to its
// consumers: the installer's asset name and checksum file, the version
// variables stamped by -ldflags, and the installer flags the dev container
// image uses.
func TestReleaseArtifactsMatchInstaller(t *testing.T) {
	entries := parseYAML(t, repoFile(t, ".goreleaser.yaml"))
	installer := string(mustRead(t, scriptPath(t, "install.sh")))

	checks := []struct {
		name string
		ok   bool
	}{
		{"project name", slices.Equal(values(find(entries, "project_name")), []string{"lyna-tmux"})},
		{"archive name", slices.Equal(values(find(entries, "archives", "-", "name_template")), []string{"{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"}) &&
			strings.Contains(installer, "asset=lyna-tmux_${version#v}_${os}_${arch}.tar.gz")},
		{"archive format", slices.Equal(values(find(entries, "archives", "-", "formats")), []string{"[tar.gz]"})},
		{"flat archive", slices.Equal(values(find(entries, "archives", "-", "wrap_in_directory")), []string{"false"}) &&
			strings.Contains(installer, `-C "$tmp/extract" lyna-tmux`)},
		{"checksums", slices.Equal(values(find(entries, "checksum", "name_template")), []string{"checksums.txt"}) &&
			slices.Equal(values(find(entries, "checksum", "algorithm")), []string{"sha256"}) &&
			strings.Contains(installer, `/download/$version/checksums.txt"`)},
		{"platforms", slices.Equal(values(find(entries, "builds", "-", "goos")), []string{"[darwin, linux]"}) &&
			slices.Equal(values(find(entries, "builds", "-", "goarch")), []string{"[amd64, arm64]"})},
		{"static build", slices.Contains(values(find(entries, "builds", "-", "env", "-")), "CGO_ENABLED=0") &&
			slices.Contains(values(find(entries, "builds", "-", "flags", "-")), "-trimpath")},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s does not match between .goreleaser.yaml and scripts/install.sh", c.name)
		}
	}

	version := string(mustRead(t, repoFile(t, "internal/version/version.go")))
	for _, flag := range values(find(entries, "builds", "-", "ldflags", "-")) {
		m := regexp.MustCompile(`^-X github.com/bayoudhdev/lyna-claude-tmux/internal/version\.(\w+)=`).FindStringSubmatch(flag)
		if m == nil {
			continue
		}
		if !regexp.MustCompile(`(?m)^\s+` + m[1] + `\s+= ""`).MatchString(version) {
			t.Errorf("ldflags stamp internal/version.%s, which is not a string variable", m[1])
		}
	}

	// The image runs the installer with its own flags: run install.sh with the
	// same flags and an http base URL, which the script refuses only after
	// every flag has been parsed.
	files, err := devcontainer.Render(devcontainer.Options{Project: "api", Source: devcontainer.Source{Version: "v1.2.3"}})
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`sh /tmp/install-lyna-tmux\.sh ([^\\\n&]+)`).FindStringSubmatch(string(files[devcontainer.FileDockerfile]))
	if m == nil {
		t.Fatal("the dev container Dockerfile does not run install.sh")
	}
	args := append([]string{scriptPath(t, "install.sh")}, strings.Fields(m[1])...)
	cmd := exec.Command("/bin/sh", append(args, "--base-url", "http://127.0.0.1:1/releases")...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "refusing non-https URL") {
		t.Errorf("install.sh %v did not accept the dev container flags:\n%s", m[1], out)
	}
}
