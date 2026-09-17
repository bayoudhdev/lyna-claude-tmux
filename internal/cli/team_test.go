package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
)

// teamSettings reports the teammateMode of a recorded launch's settings.
func teamSettings(t *testing.T, inv fakeclaude.Invocation) string {
	t.Helper()
	var doc struct {
		TeammateMode string `json:"teammateMode"`
	}
	if err := json.Unmarshal(inv.Settings, &doc); err != nil {
		t.Fatalf("settings of %q: %v (%s)", inv.Args, err, inv.SettingsError)
	}
	return doc.TeammateMode
}

func TestTeamCLI(t *testing.T) {
	w := newWsEnv(t)
	web := filepath.Join(w.host.Home, "src", "web")
	tools := filepath.Join(w.host.Home, "src", "tools")
	for _, d := range []string{web, tools} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	teams := func(t *testing.T, inv fakeclaude.Invocation, want bool) {
		t.Helper()
		env := inv.Env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] == "1"
		mode := teamSettings(t, inv) == "tmux"
		if env != want || mode != want {
			t.Fatalf("agent teams environment %v, teammateMode tmux %v; want %v", env, mode, want)
		}
	}
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		errHas   []string
		check    func(t *testing.T)
	}{
		{name: "two directories", args: []string{"team", "a", "b"}, wantCode: 1, errHas: []string{"at most one directory"}},
		{
			name: "not a terminal needs detach", args: []string{"team", w.project}, wantCode: 1, errHas: []string{"pass --detach"},
			setup: func(*testing.T) { w.term.Interactive = false },
			check: func(t *testing.T) {
				t.Helper()
				w.term.Interactive = true
				if _, err := os.Stat(w.record); err == nil {
					t.Fatal("claude started without a terminal to attach")
				}
			},
		},
		{name: "invalid effort", args: []string{"team", "-d", w.project, "--effort", "extreme"}, wantCode: 1, errHas: []string{"extreme"}},
		{
			name: "detached from the working directory", setup: func(*testing.T) { w.cwd = w.project }, args: []string{"team", "-d"},
			outHas: []string{"Workspace api is running with agent teams. Attach with: lmux attach api"},
			check: func(t *testing.T) {
				t.Helper()
				inv := w.invocations(t, 1)[0]
				teams(t, inv, true)
				if inv.Cwd != w.project || len(w.execs) != 0 {
					t.Fatalf("launch cwd %q, execs %q", inv.Cwd, w.execs)
				}
			},
		},
		{
			name: "running project is attached as it is", args: []string{"team", "."},
			outHas: []string{"Workspace api is already open for " + w.project + "; agent teams apply to workspaces this command starts"},
			check: func(t *testing.T) {
				t.Helper()
				if len(w.execs) != 1 || !slices.Contains(w.execs[0], "attach-session") || w.execs[0][len(w.execs[0])-1] != "=api:" {
					t.Fatalf("exec %q", w.execs)
				}
			},
		},
		{
			name: "flags and claude arguments", args: []string{"team", "-d", "-n", "crew", "-l", "solo", "--model", "opus", "--effort", "high", "--mode", "plan", "--sandbox", "strict", "--isolation", "bash", "-c", "~/src/web", "--", "--verbose"},
			outHas: []string{"Workspace crew is running with agent teams"},
			check: func(t *testing.T) {
				t.Helper()
				inv := w.invocations(t, 2)[1]
				teams(t, inv, true)
				n := len(inv.Args)
				for _, want := range []string{"--name=crew", "--model=opus", "--effort=high", "--permission-mode=plan"} {
					if !slices.Contains(inv.Args, want) {
						t.Fatalf("args %q missing %q", inv.Args, want)
					}
				}
				if inv.Cwd != web || inv.Args[n-2] != "--continue" || inv.Args[n-1] != "--verbose" || inv.Env["LYNA_TMUX_SANDBOX"] != "strict" {
					t.Fatalf("launch cwd %q args %q env %v", inv.Cwd, inv.Args, inv.Env)
				}
				if got := w.roles(w.panes(t, "=crew:")); !slices.Equal(got, []string{"claude"}) {
					t.Fatalf("solo roles %q", got)
				}
			},
		},
		{
			name: "create keeps agent teams off", args: []string{"create", "-d", tools},
			check: func(t *testing.T) { t.Helper(); teams(t, w.invocations(t, 3)[2], false) },
		},
		{name: "effort completion", args: []string{"__complete", "team", "--effort", ""}, outHas: []string{"low\n", "ultracode\n"}},
		{name: "mode completion", args: []string{"__complete", "team", "--mode", ""}, outHas: []string{"plan\n", "bypassPermissions\n"}},
		{name: "sandbox completion", args: []string{"__complete", "team", "--sandbox", ""}, outHas: []string{"standard\n", "strict\n", "off\n"}},
		{name: "isolation completion", args: []string{"__complete", "team", "--isolation", ""}, outHas: []string{"bash\n"}},
		{name: "layout completion", args: []string{"__complete", "team", "--layout", ""}, outHas: []string{"solo\n", "auto\n"}},
	}
	for _, st := range steps {
		if !t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := w.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t)
			}
		}) {
			t.Fatalf("step %q failed; later steps depend on it", st.name)
		}
	}
}
