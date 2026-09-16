package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// pluginTmuxBindings returns the prefix keys bound to each plugin command.
func pluginTmuxBindings(t *testing.T, srv *tmuxtest.Server) (launch, agents []string) {
	t.Helper()
	out, err := srv.Client.Run(tmuxtest.Context(t), "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		switch {
		case strings.Contains(line, "popup launch --pane #{pane_id} --client #{q:client_name}"):
			launch = append(launch, f[3])
		case strings.Contains(line, "popup agents --pane #{pane_id} --client #{q:client_name}"):
			agents = append(agents, f[3])
		}
	}
	return launch, agents
}

// pluginTmuxHooks returns the alert-bell hooks of a server.
func pluginTmuxHooks(t *testing.T, srv *tmuxtest.Server) []string {
	t.Helper()
	out, err := srv.Client.Run(tmuxtest.Context(t), "show-hooks", "-g")
	if err != nil {
		t.Fatal(err)
	}
	var hooks []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "alert-bell") {
			hooks = append(hooks, line)
		}
	}
	return hooks
}

func TestPluginTmuxCLI(t *testing.T) {
	e := newCLIEnv(t)
	// user stands for the tmux server the user runs, with an alert-bell hook
	// of their own that plugin mode must keep.
	user := tmuxtest.Start(t)
	if _, err := user.Client.Run(tmuxtest.Context(t), "set-hook", "-g", "alert-bell", "display-message mine"); err != nil {
		t.Fatal(err)
	}
	userTMUX := tmuxtest.SocketPath(user.Name) + ",1,0"
	defaults := tmux.PluginOptions{Bin: e.host.Exe, LaunchKey: "y", ListKey: "u", ForwardBell: true}
	setOption := func(name, value string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			args := []string{"-g", name, value}
			if value == "" {
				args = []string{"-gu", name}
			}
			if _, err := user.Client.Run(tmuxtest.Context(t), "set-option", args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantState := func(launch, agents string, bell bool) func(*testing.T, string) {
		return func(t *testing.T, _ string) {
			t.Helper()
			gotLaunch, gotAgents := pluginTmuxBindings(t, user)
			if !strings.Contains(strings.Join(gotLaunch, " "), launch) || !strings.Contains(strings.Join(gotAgents, " "), agents) {
				t.Fatalf("launch bound to %q, agents to %q; want %q and %q", gotLaunch, gotAgents, launch, agents)
			}
			for _, keys := range [][]string{gotLaunch, gotAgents} {
				seen := map[string]bool{}
				for _, k := range keys {
					if seen[k] {
						t.Fatalf("key %s bound twice: %q", k, keys)
					}
					seen[k] = true
				}
			}
			hooks := pluginTmuxHooks(t, user)
			if len(hooks) == 0 || !strings.HasPrefix(hooks[0], "alert-bell[0] display-message mine") {
				t.Fatalf("the user's own hook is gone: %q", hooks)
			}
			ours := len(hooks) == 2 && strings.HasPrefix(hooks[1], tmux.PluginHook+" run-shell -b ") && strings.Contains(hooks[1], "bell-forward #{q:hook_session}")
			if ours != bell || !bell && len(hooks) != 1 {
				t.Fatalf("alert-bell hooks %q, want bell forwarding %v", hooks, bell)
			}
		}
	}

	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		wantOut  *string
		errHas   []string
		check    func(t *testing.T, stdout string)
	}{
		{name: "prints the configuration", args: []string{"plugin", "tmux"}, wantOut: new(tmux.PluginConf(defaults))},
		{
			name: "keys and bell flags", args: []string{"plugin", "tmux", "--launch-key", "C-y", "--list-key", "", "--no-bell"},
			wantOut: new(tmux.PluginConf(tmux.PluginOptions{Bin: e.host.Exe, LaunchKey: "C-y"})),
		},
		{name: "invalid key", args: []string{"plugin", "tmux", "--launch-key", "a b"}, wantCode: 1, errHas: []string{`launch key "a b" is not a tmux key name`}},
		{name: "same key twice", args: []string{"plugin", "tmux", "--list-key", "y"}, wantCode: 1, errHas: []string{`both "y"`}},
		{name: "write and apply exclude each other", args: []string{"plugin", "tmux", "--write", "--apply"}, wantCode: 1, errHas: []string{"[apply write] were all set"}},
		{name: "no arguments", args: []string{"plugin", "tmux", "on"}, wantCode: 1, errHas: []string{"unknown command"}},
		{
			name: "write saves the file and prints the line that loads it",
			setup: func(t *testing.T) {
				t.Helper()
				e.setenv("LYNA_TMUX_HOME", filepath.Join(e.host.Home, "it's [a]*? #{x}"))
			},
			args: []string{"plugin", "tmux", "--write"},
			check: func(t *testing.T, stdout string) {
				t.Helper()
				path := filepath.Join(e.env["LYNA_TMUX_HOME"], "state", app.PluginConfName)
				lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
				if len(lines) != 2 || lines[0] != "Wrote "+path+". Add this line to your tmux configuration:" || lines[1] != app.PluginSourceLine(path) {
					t.Fatalf("stdout %q", stdout)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("written file %v (%v)", info, err)
				}
				// The printed line loads the bindings into a server.
				srv := tmuxtest.Start(t)
				conf := filepath.Join(e.host.Home, "user.conf")
				if err := os.WriteFile(conf, []byte(lines[1]+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := srv.Client.Run(tmuxtest.Context(t), "source-file", conf); err != nil {
					t.Fatal(err)
				}
				if launch, agents := pluginTmuxBindings(t, srv); strings.Join(launch, "") != "y" || strings.Join(agents, "") != "u" {
					t.Fatalf("sourced bindings launch %q agents %q", launch, agents)
				}
				e.setenv("LYNA_TMUX_HOME", e.host.Home)
			},
		},
		{name: "apply outside tmux", setup: func(*testing.T) { e.setenv("TMUX", "") }, args: []string{"plugin", "tmux", "--apply"}, wantCode: 1, errHas: []string{"$TMUX is not set"}},
		{
			name: "apply without a server", setup: func(*testing.T) { e.setenv("TMUX", filepath.Join(e.host.Home, "no-socket")+",1,0") },
			args: []string{"plugin", "tmux", "--apply"}, wantCode: 1, errHas: []string{"no-socket"},
		},
		{
			name: "apply installs bindings and the bell hook", setup: func(*testing.T) { e.setenv("TMUX", userTMUX) },
			args: []string{"plugin", "tmux", "--apply"}, wantOut: new(""), check: wantState("y", "u", true),
		},
		{name: "apply again changes nothing", args: []string{"plugin", "tmux", "--apply"}, wantOut: new(""), check: wantState("y", "u", true)},
		{
			name: "apply honors the plugin options",
			setup: func(t *testing.T) {
				t.Helper()
				setOption(tmux.OptClaudeLaunchKey, "Y")(t)
				setOption(tmux.OptClaudeListKey, "U")(t)
				setOption(tmux.OptClaudeForwardBell, "off")(t)
			},
			args: []string{"plugin", "tmux", "--apply"}, check: wantState("Y", "U", false),
		},
		{name: "flags override the plugin options", setup: setOption(tmux.OptClaudeForwardBell, ""), args: []string{"plugin", "tmux", "--apply", "--launch-key", "C-t"}, check: wantState("C-t", "U", true)},
		{name: "no-bell removes only the plugin hook", args: []string{"plugin", "tmux", "--apply", "--no-bell"}, check: wantState("C-t", "U", false)},
		{name: "invalid plugin option fails", setup: setOption(tmux.OptClaudeListKey, "a b"), args: []string{"plugin", "tmux", "--apply"}, wantCode: 1, errHas: []string{`list key "a b"`}},
	}
	for _, st := range steps {
		if !t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := e.run(t, st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			if st.wantOut != nil && stdout != *st.wantOut {
				t.Fatalf("stdout:\n%s\nwant:\n%s", stdout, *st.wantOut)
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t, stdout)
			}
		}) {
			t.Fatalf("step %q failed; later steps depend on it", st.name)
		}
	}
}
