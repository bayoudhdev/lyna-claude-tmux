package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// sandboxWriteConfig writes the lyna-tmux configuration of the test home.
func sandboxWriteConfig(t *testing.T, e *infraEnv, content string) {
	t.Helper()
	dir := filepath.Join(e.host.Home, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxProfilesCLI(t *testing.T) {
	e := newInfraEnv(t)
	code, stdout, stderr := e.run(t, "sandbox", "profiles")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	golden.Assert(t, "sandbox/profiles.txt", []byte(stdout))
	if code, _, stderr := e.run(t, "sandbox", "profiles", "extra"); code != 1 || !containsFolded(stderr, "unknown command") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestSandboxShowCLI(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
	}{
		{name: "defaults", args: []string{"sandbox", "show"}, outHas: []string{`"sandbox"`, `"enabled": true`, `"failIfUnavailable": true`}},
		{name: "profile argument", args: []string{"sandbox", "show", "strict"}, outHas: []string{`"strictAllowlist": true`, "raw.githubusercontent.com"}},
		{name: "isolation flag", args: []string{"sandbox", "show", "--isolation", "process"}, outHas: []string{`"enabled": false`, "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"}},
		{name: "directory flag", args: []string{"sandbox", "show", "strict", "--dir", "src/node"}, outHas: []string{"registry.npmjs.org"}},
		{name: "off in the configuration", config: "[sandbox]\nprofile = \"off\"\n", args: []string{"sandbox", "show"}, wantCode: 1, errHas: []string{"--sandbox off"}},
		{name: "unknown profile", args: []string{"sandbox", "show", "loose"}, wantCode: 1, errHas: []string{"profile must be one of"}},
		{name: "unknown isolation", args: []string{"sandbox", "show", "--isolation", "vm"}, wantCode: 1, errHas: []string{"isolation must be one of"}},
		{name: "too many arguments", args: []string{"sandbox", "show", "strict", "extra"}, wantCode: 1, errHas: []string{"accepts at most 1 arg"}},
		{name: "profile completion", args: []string{"__complete", "sandbox", "show", ""}, outHas: []string{"standard\nstrict\noff"}},
		{name: "isolation completion", args: []string{"__complete", "sandbox", "show", "--isolation", ""}, outHas: []string{"bash\nprocess\ncontainer"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			node := filepath.Join(e.host.Home, "src", "node")
			if err := os.MkdirAll(filepath.Join(node, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(node, "package.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				sandboxWriteConfig(t, e, tc.config)
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
			if tc.wantCode == 0 && !strings.HasPrefix(tc.args[0], "__") {
				var doc map[string]any
				if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
					t.Fatalf("not JSON: %v", err)
				}
			}
		})
	}
}

func TestSandboxStatusCLI(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		args     []string
		term     bool
		stdin    string
		wantCode int
		outHas   []string
		errHas   []string
		leftIn   int
	}{
		{name: "text", args: []string{"sandbox", "status"}, outHas: []string{"from the configuration", "Profile", "standard", "Readiness on this machine", "1 ok"}},
		{name: "strict configuration", config: "[sandbox]\nprofile = \"strict\"\n", args: []string{"sandbox", "status"}, outHas: []string{"strict", "only allowed hosts", "Unsandboxed        never"}},
		{name: "off in the configuration", config: "[sandbox]\nprofile = \"off\"\n", args: []string{"sandbox", "status"}, wantCode: 1, errHas: []string{"--sandbox off"}},
		{name: "popup waits for a key on a terminal", args: []string{"sandbox", "status", "--popup"}, term: true, stdin: "x", outHas: []string{"Press any key to close"}},
		{name: "popup without a terminal returns", args: []string{"sandbox", "status", "--popup"}, stdin: "x", leftIn: 1},
		{name: "popup keeps an error on screen", config: "[sandbox]\nprofile = \"off\"\n", args: []string{"sandbox", "status", "--popup"}, term: true, stdin: "x", wantCode: 1, outHas: []string{"lyna-tmux sandbox status:", "Press any key"}},
		{name: "rejects arguments", args: []string{"sandbox", "status", "extra"}, wantCode: 1, errHas: []string{"unknown command"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.bins["sandbox-exec"] = "/usr/bin/sandbox-exec"
			e.term = Terminal{Interactive: tc.term}
			e.stdin = strings.NewReader(tc.stdin)
			if tc.config != "" {
				sandboxWriteConfig(t, e, tc.config)
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
			if e.stdin.Len() != tc.leftIn {
				t.Fatalf("%d bytes left on standard input, want %d", e.stdin.Len(), tc.leftIn)
			}
		})
	}
}

func TestSandboxStatusJSONCLI(t *testing.T) {
	e := newInfraEnv(t)
	e.bins["sandbox-exec"] = "/usr/bin/sandbox-exec"
	project := infraProject(t, e, "api")
	code, stdout, stderr := e.run(t, "sandbox", "status", "--json", "--dir", project)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var st app.SandboxState
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if st.Source != app.SandboxSourceConfig || st.Project != project || st.Sandbox.Profile != "standard" || !st.Ready {
		t.Fatalf("state %+v", st)
	}
	if len(st.Readiness.Results) != 1 || st.Readiness.Results[0].ID != "sandbox" || len(st.Sandbox.DeniedFiles) == 0 {
		t.Fatalf("readiness %+v", st.Readiness)
	}
}

// TestSandboxStatusInPaneCLI runs the status the way the generated tmux
// configuration does: from a popup of a workspace pane, with $TMUX and
// $TMUX_PANE set to the test server and a real pane.
func TestSandboxStatusInPaneCLI(t *testing.T) {
	e := newInfraEnv(t)
	e.bins["sandbox-exec"] = "/usr/bin/sandbox-exec"
	project := infraProject(t, e, "api")
	e.start(t, "api", project)
	ctx := tmuxtest.Context(t)
	client := tmux.New(tmux.Options{Bin: e.bins["tmux"], Socket: tmux.Socket{Name: e.env["LYNA_TMUX_SOCKET_NAME"]}})
	if _, err := client.Run(ctx, "set-option", "-t", tmux.ExactSession("api"), tmux.OptSandbox, "strict"); err != nil {
		t.Fatal(err)
	}
	pane, err := client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	e.setenv("TMUX", app.SocketPath(e.host.Getenv, e.env["LYNA_TMUX_SOCKET_NAME"])+",1,0")
	e.setenv("TMUX_PANE", pane)
	e.term = Terminal{Interactive: true}
	e.stdin = strings.NewReader("q")
	e.cwd = project
	code, stdout, stderr := e.run(t, "sandbox", "status", "--popup")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"Sandbox of workspace api", project, "strict", "Press any key to close"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if e.stdin.Len() != 0 {
		t.Fatalf("the popup did not consume the key press")
	}
}
