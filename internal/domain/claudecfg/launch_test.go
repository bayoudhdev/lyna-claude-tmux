package claudecfg

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

func baseLaunch(t *testing.T) Launch {
	t.Helper()
	return Launch{
		ClaudePath:   "/home/dev/.local/bin/claude",
		SettingsPath: "/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json",
		Home:         "/home/dev",
		SessionName:  "api-server",
		SocketPath:   "/tmp/tmux-1000/lyna-tmux",
		Sandbox:      resolve(t, sandbox.Input{}),
	}
}

func renderCommand(c Command) []byte {
	var b strings.Builder
	b.WriteString("argv:\n")
	for _, a := range c.Argv {
		b.WriteString("  " + a + "\n")
	}
	b.WriteString("env:\n")
	for _, e := range c.Env {
		b.WriteString("  " + e + "\n")
	}
	return []byte(b.String())
}

func TestBuildLaunchGolden(t *testing.T) {
	cases := []struct {
		name   string
		golden string
		edit   func(l *Launch)
	}{
		{name: "minimal standard", golden: "launch/standard.golden", edit: func(*Launch) {}},
		{
			name:   "fullscreen teams strict with every option",
			golden: "launch/fullscreen-teams-strict.golden",
			edit: func(l *Launch) {
				l.Model = "opus"
				l.Effort = "ultracode"
				l.PermissionMode = "bypassPermissions"
				l.AddDirs = []string{"~/src/shared-lib", "/srv/docs"}
				l.MCPConfigs = []string{"~/.config/mcp/servers.json"}
				l.PluginDirs = []string{"/opt/plugins"}
				l.Worktree = "feature-1"
				l.Fullscreen = true
				l.Teams = true
				l.Sandbox = resolve(t, sandbox.Input{Profile: sandbox.Strict})
				l.ExtraArgs = []string{"--verbose", "review the diff"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := baseLaunch(t)
			tc.edit(&l)
			cmd, err := BuildLaunch(l)
			if err != nil {
				t.Fatalf("BuildLaunch: %v", err)
			}
			golden.Assert(t, tc.golden, renderCommand(cmd))
		})
	}
}

func TestBuildLaunchArgv(t *testing.T) {
	cases := []struct {
		name string
		edit func(l *Launch)
		want []string
	}{
		{
			name: "managed flags first",
			edit: func(*Launch) {},
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server"},
		},
		{
			name: "values that start with a dash stay one argument",
			edit: func(l *Launch) { l.Model = "claude-opus-5[1m]"; l.AddDirs = []string{"/tmp/-odd"} },
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server", "--model=claude-opus-5[1m]", "--add-dir=/tmp/-odd"},
		},
		{
			name: "repeatable flags keep order and expand home",
			edit: func(l *Launch) {
				l.AddDirs = []string{"~/a", "/b"}
				l.MCPConfigs = []string{"~/m1.json", "~/m2.json"}
				l.PluginDirs = []string{"~/p"}
			},
			want: []string{
				"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server",
				"--add-dir=/home/dev/a", "--add-dir=/b", "--mcp-config=/home/dev/m1.json", "--mcp-config=/home/dev/m2.json", "--plugin-dir=/home/dev/p",
			},
		},
		{
			name: "extra args after worktree",
			edit: func(l *Launch) { l.Worktree = "task.2"; l.ExtraArgs = []string{"-c"} },
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server", "--worktree=task.2", "-c"},
		},
		{
			name: "an agent definition runs the session",
			edit: func(l *Launch) { l.Agent = "api-developer"; l.Worktree = "review.1" },
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server", "--agent=api-developer", "--worktree=review.1"},
		},
		{
			name: "manual permission mode passes through",
			edit: func(l *Launch) { l.PermissionMode = "manual"; l.Effort = "low" },
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server", "--effort=low", "--permission-mode=manual"},
		},
		{
			name: "forbidden looking words after -- are prompt text",
			edit: func(l *Launch) { l.ExtraArgs = []string{"--", "--settings"} },
			want: []string{"/home/dev/.local/bin/claude", "--settings=/home/dev/.local/state/lyna-tmux/settings/0123456789abcdef.json", "--name=api-server", "--", "--settings"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := baseLaunch(t)
			tc.edit(&l)
			cmd, err := BuildLaunch(l)
			if err != nil {
				t.Fatalf("BuildLaunch: %v", err)
			}
			if !slices.Equal(cmd.Argv, tc.want) {
				t.Fatalf("argv =\n%q\nwant\n%q", cmd.Argv, tc.want)
			}
		})
	}
}

func TestBuildLaunchEnv(t *testing.T) {
	cases := []struct {
		name string
		edit func(l *Launch)
		want []string
	}{
		{
			name: "standard",
			edit: func(*Launch) {},
			want: []string{
				"CLAUDE_CODE_TMUX_TRUECOLOR=1",
				"LYNA_TMUX_MANAGED=1",
				"LYNA_TMUX_SANDBOX=standard",
				"LYNA_TMUX_SESSION=api-server",
				"LYNA_TMUX_SOCKET=/tmp/tmux-1000/lyna-tmux",
			},
		},
		{
			name: "fullscreen teams strict",
			edit: func(l *Launch) {
				l.Fullscreen = true
				l.Teams = true
				l.Sandbox = resolve(t, sandbox.Input{Profile: sandbox.Strict})
			},
			want: []string{
				"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1",
				"CLAUDE_CODE_NO_FLICKER=1",
				"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1",
				"CLAUDE_CODE_TMUX_TRUECOLOR=1",
				"LYNA_TMUX_MANAGED=1",
				"LYNA_TMUX_SANDBOX=strict",
				"LYNA_TMUX_SESSION=api-server",
				"LYNA_TMUX_SOCKET=/tmp/tmux-1000/lyna-tmux",
			},
		},
		{
			name: "off profile",
			edit: func(l *Launch) { l.Sandbox = resolve(t, sandbox.Input{Profile: sandbox.Off}) },
			want: []string{
				"CLAUDE_CODE_TMUX_TRUECOLOR=1",
				"LYNA_TMUX_MANAGED=1",
				"LYNA_TMUX_SANDBOX=off",
				"LYNA_TMUX_SESSION=api-server",
				"LYNA_TMUX_SOCKET=/tmp/tmux-1000/lyna-tmux",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := baseLaunch(t)
			tc.edit(&l)
			cmd, err := BuildLaunch(l)
			if err != nil {
				t.Fatalf("BuildLaunch: %v", err)
			}
			if !slices.Equal(cmd.Env, tc.want) {
				t.Fatalf("env =\n%q\nwant\n%q", cmd.Env, tc.want)
			}
		})
	}
}

func TestBuildLaunchErrors(t *testing.T) {
	cases := []struct {
		name string
		edit func(l *Launch)
		want string
	}{
		{name: "relative claude path", edit: func(l *Launch) { l.ClaudePath = "claude" }, want: "claude path must be an absolute path"},
		{name: "missing settings path", edit: func(l *Launch) { l.SettingsPath = "" }, want: "settings path must be an absolute path"},
		{name: "missing socket", edit: func(l *Launch) { l.SocketPath = "" }, want: "socket path must be an absolute path"},
		{name: "control byte in settings path", edit: func(l *Launch) { l.SettingsPath = "/tmp/a\x1bb" }, want: "settings path"},
		{name: "bad session name", edit: func(l *Launch) { l.SessionName = "a:b" }, want: "session name"},
		{name: "bad model", edit: func(l *Launch) { l.Model = "opus; rm -rf" }, want: "model must be"},
		{name: "bad effort", edit: func(l *Launch) { l.Effort = "extreme" }, want: "effort must be one of"},
		{name: "bad permission mode", edit: func(l *Launch) { l.PermissionMode = "yolo" }, want: "permission mode must be one of"},
		{name: "bypass refused on standard", edit: func(l *Launch) { l.PermissionMode = "bypassPermissions" }, want: "bypassPermissions requires"},
		{name: "empty sandbox resolution", edit: func(l *Launch) { l.Sandbox = sandbox.Resolution{} }, want: "no valid profile"},
		{name: "agent with a path in it", edit: func(l *Launch) { l.Agent = "../other" }, want: "agent must be"},
		{name: "agent leading dash", edit: func(l *Launch) { l.Agent = "-x" }, want: "agent must be"},
		{name: "agent with a space", edit: func(l *Launch) { l.Agent = "api developer" }, want: "agent must be"},
		{name: "worktree leading dash", edit: func(l *Launch) { l.Worktree = "-x" }, want: "worktree name"},
		{name: "worktree dot dot", edit: func(l *Launch) { l.Worktree = ".." }, want: "worktree name"},
		{name: "worktree slash", edit: func(l *Launch) { l.Worktree = "a/b" }, want: "worktree name"},
		{name: "relative add dir", edit: func(l *Launch) { l.AddDirs = []string{"lib"} }, want: "add dir must be"},
		{name: "relative mcp config", edit: func(l *Launch) { l.MCPConfigs = []string{"./m.json"} }, want: "mcp config must be"},
		{name: "relative plugin dir", edit: func(l *Launch) { l.PluginDirs = []string{"~plugins"} }, want: "plugin dir must be"},
		{name: "home needed for tilde", edit: func(l *Launch) { l.Home = ""; l.AddDirs = []string{"~/x"} }, want: "home directory must be absolute"},
		{name: "extra settings flag", edit: func(l *Launch) { l.ExtraArgs = []string{"--settings", "/tmp/x.json"} }, want: "cannot pass --settings"},
		{name: "extra settings equals form", edit: func(l *Launch) { l.ExtraArgs = []string{"--settings={}"} }, want: "cannot pass --settings"},
		{name: "extra permission mode", edit: func(l *Launch) { l.ExtraArgs = []string{"--permission-mode=plan"} }, want: "cannot pass --permission-mode"},
		{name: "extra skip permissions", edit: func(l *Launch) { l.ExtraArgs = []string{"--dangerously-skip-permissions"} }, want: "requires the strict sandbox profile"},
		{name: "extra allow skip permissions", edit: func(l *Launch) { l.ExtraArgs = []string{"--allow-dangerously-skip-permissions"} }, want: "requires the strict sandbox profile"},
		{name: "extra NUL", edit: func(l *Launch) { l.ExtraArgs = []string{"a\x00b"} }, want: "NUL"},
		// Each of these undoes the launch while the status line keeps
		// reporting the profile the settings file was built for.
		{name: "extra mcp config", edit: func(l *Launch) { l.ExtraArgs = []string{"--mcp-config", "/tmp/mcp.json"} }, want: "cannot pass --mcp-config"},
		{name: "extra mcp config equals form", edit: func(l *Launch) { l.ExtraArgs = []string{"--mcp-config={}"} }, want: "cannot pass --mcp-config"},
		{name: "extra plugin dir", edit: func(l *Launch) { l.ExtraArgs = []string{"--plugin-dir=/tmp/plugins"} }, want: "cannot pass --plugin-dir"},
		{name: "extra plugin url", edit: func(l *Launch) { l.ExtraArgs = []string{"--plugin-url", "https://example.com/p.zip"} }, want: "cannot pass --plugin-url"},
		{name: "extra add dir", edit: func(l *Launch) { l.ExtraArgs = []string{"--add-dir=/"} }, want: "cannot pass --add-dir"},
		{
			name: "every denied flag is reported at once",
			edit: func(l *Launch) {
				l.ExtraArgs = []string{"--add-dir=/", "--plugin-url=x", "--mcp-config=y", "--settings=z"}
			},
			want: "cannot pass --add-dir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := baseLaunch(t)
			tc.edit(&l)
			_, err := BuildLaunch(l)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildLaunch error = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildLaunchAllowsBypassFlagsWhereBypassIsAllowed(t *testing.T) {
	cases := []struct {
		name string
		in   sandbox.Input
	}{
		{name: "strict", in: sandbox.Input{Profile: sandbox.Strict}},
		{name: "container", in: sandbox.Input{Profile: sandbox.Standard, Isolation: sandbox.IsolationContainer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := baseLaunch(t)
			l.Sandbox = resolve(t, tc.in)
			l.PermissionMode = "bypassPermissions"
			l.ExtraArgs = []string{"--allow-dangerously-skip-permissions"}
			if _, err := BuildLaunch(l); err != nil {
				t.Fatalf("BuildLaunch: %v", err)
			}
		})
	}
}

func TestBuildLaunchRejectsSandboxEnvConflict(t *testing.T) {
	l := baseLaunch(t)
	l.Sandbox.Env = map[string]string{"LYNA_TMUX_MANAGED": "0"}
	if _, err := BuildLaunch(l); !errors.Is(err, ErrInvalid) {
		t.Fatalf("BuildLaunch error = %v, want ErrInvalid", err)
	}
	l.Sandbox.Env = map[string]string{"bad name": "1"}
	if _, err := BuildLaunch(l); !errors.Is(err, ErrInvalid) {
		t.Fatalf("BuildLaunch error = %v, want ErrInvalid for a bad name", err)
	}
}

func TestAllowedValueLists(t *testing.T) {
	if got := Efforts(); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max", "ultracode"}) {
		t.Fatalf("Efforts() = %v", got)
	}
	if got := PermissionModes(); !slices.Equal(got, []string{"default", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}) {
		t.Fatalf("PermissionModes() = %v", got)
	}
}

// TestBuildLaunchExtraArgsAfterSeparator keeps the prompt intact: everything
// after a bare "--" is text for Claude, not an option, whatever it spells.
func TestBuildLaunchExtraArgsAfterSeparator(t *testing.T) {
	flags := []string{
		"--settings", "--permission-mode", "--mcp-config", "--plugin-dir", "--plugin-url",
		"--add-dir", "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
	}
	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			l := baseLaunch(t)
			l.ExtraArgs = []string{"--", "explain what " + flag + " does", flag}
			cmd, err := BuildLaunch(l)
			if err != nil {
				t.Fatalf("BuildLaunch = %v, want the prompt to pass through", err)
			}
			if got := cmd.Argv[len(cmd.Argv)-3:]; !slices.Equal(got, l.ExtraArgs) {
				t.Fatalf("argv tail %q, want %q", got, l.ExtraArgs)
			}
		})
	}
}

// TestCheckExtraArgsDenyList pins which flags are refused before the
// separator, and that a flag refused there is not refused as prompt text.
func TestCheckExtraArgsDenyList(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		bypassAllowed bool
		wantProblems  int
	}{
		{name: "nothing", args: nil},
		{name: "an ordinary flag", args: []string{"--verbose"}},
		{name: "a prompt", args: []string{"review the diff"}},
		{name: "one denied flag", args: []string{"--add-dir=/"}, wantProblems: 1},
		{name: "several denied flags", args: []string{"--add-dir=/", "--mcp-config=y", "--plugin-dir=z"}, wantProblems: 3},
		{name: "the same flag twice", args: []string{"--settings=a", "--settings=b"}, wantProblems: 2},
		{name: "denied flags after the separator", args: []string{"--", "--add-dir=/", "--settings=a"}},
		{name: "a denied flag before the separator", args: []string{"--settings=a", "--", "--settings=b"}, wantProblems: 1},
		{name: "skip permissions without a boundary", args: []string{"--dangerously-skip-permissions"}, wantProblems: 1},
		{name: "skip permissions with a boundary", args: []string{"--dangerously-skip-permissions"}, bypassAllowed: true},
		{name: "a flag with a denied prefix", args: []string{"--add-dirs=/", "--settings-file=x"}},
		{name: "a NUL byte", args: []string{"a\x00b"}, wantProblems: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkExtraArgs(tc.args, tc.bypassAllowed)
			if len(got) != tc.wantProblems {
				t.Fatalf("checkExtraArgs(%q) = %q, want %d problems", tc.args, got, tc.wantProblems)
			}
		})
	}
}
