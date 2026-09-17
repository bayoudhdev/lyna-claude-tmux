package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestPopupStartRecordsSandbox pins the session options a popup session
// carries: the sandbox profile and the isolation level it was started at, the
// same pair a workspace records, so the sandbox status and the commands run
// inside it read the launch that happened.
func TestPopupStartRecordsSandbox(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	profile, isolation := s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation
	cases := []struct {
		name string
		// profile and isolation replace the configuration for the case.
		profile, isolation string
		// inside runs the case as if this process were in a container.
		inside                     bool
		dir                        string
		wantSandbox, wantIsolation string
	}{
		{name: "configured defaults", dir: "plain", wantSandbox: "standard", wantIsolation: "bash"},
		{name: "strict profile", profile: "strict", dir: "strict", wantSandbox: "strict", wantIsolation: "bash"},
		{name: "container isolation", isolation: "container", inside: true, dir: "container", wantSandbox: "standard", wantIsolation: "container"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolationInsideContainer(t, tc.inside)
			s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation = pick(tc.profile, profile), pick(tc.isolation, isolation)
			t.Cleanup(func() { s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation = profile, isolation })
			dir := filepath.Join(e.root, "popup", tc.dir)
			mkdir(t, dir)
			ctx := tmuxtest.Context(t)
			name := session.PopupName(s.Config.Popup.SessionPrefix, dir)
			created, err := s.popupStart(ctx, e.Host, name, dir, tmux.PluginUserOptions{})
			if err != nil || !created {
				t.Fatalf("popupStart created %v: %v", created, err)
			}
			target := tmux.ExactSession(name)
			got := map[string]string{}
			for _, opt := range []string{tmux.OptSandbox, tmux.OptIsolation} {
				if got[opt], err = s.Client.ShowOption(ctx, "", target, opt); err != nil {
					t.Fatal(err)
				}
			}
			if got[tmux.OptSandbox] != tc.wantSandbox || got[tmux.OptIsolation] != tc.wantIsolation {
				t.Fatalf("session options %v, want sandbox %q isolation %q", got, tc.wantSandbox, tc.wantIsolation)
			}
		})
	}
}

// TestPopupStartClaudeOptions starts real popup sessions and reads what the
// fake claude was run as, so the @claude_command and @claude_args options are
// checked where they land: argv[0] and the arguments of the process.
func TestPopupStartClaudeOptions(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	fake, err := e.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	// Another name for the same executable stands in for the user's own agent
	// binary; argv[0] tells the two apart.
	agent := filepath.Join(e.root, "bin", "my-agent")
	if err := os.Symlink(fake, agent); err != nil {
		t.Fatal(err)
	}
	s.Config.Claude.Args = []string{"--verbose"}
	cases := []struct {
		name string
		dir  string
		user tmux.PluginUserOptions
		// wantProgram is argv[0]; wantTail the arguments after the managed
		// flags, which is where claude.args or @claude_args land.
		wantProgram string
		wantTail    []string
		wantErr     string
	}{
		{name: "both empty", dir: "defaults", wantProgram: fake, wantTail: []string{"--verbose"}},
		{name: "command option", dir: "command", user: tmux.PluginUserOptions{Command: agent}, wantProgram: agent, wantTail: []string{"--verbose"}},
		{
			name: "args option replaces the configured arguments", dir: "args",
			user:        tmux.PluginUserOptions{Args: `--append-system-prompt "be brief"`},
			wantProgram: fake, wantTail: []string{"--append-system-prompt", "be brief"},
		},
		{
			name: "both options", dir: "both",
			user:        tmux.PluginUserOptions{Command: agent, Args: `--append-system-prompt 'be brief' --debug`},
			wantProgram: agent, wantTail: []string{"--append-system-prompt", "be brief", "--debug"},
		},
		{name: "unterminated quote", dir: "quote", user: tmux.PluginUserOptions{Args: `--model "opus`}, wantErr: tmux.OptClaudeArgs + `: an opening " has no closing "`},
		{name: "control character", dir: "control", user: tmux.PluginUserOptions{Args: "--debug\x1b[2J"}, wantErr: tmux.OptClaudeArgs + " must be a single-line value"},
		{name: "command does not resolve", dir: "missing", user: tmux.PluginUserOptions{Command: filepath.Join(e.root, "bin", "absent")}, wantErr: tmux.OptClaudeCommand + ": claude: Claude Code executable not found at " + filepath.Join(e.root, "bin", "absent")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(e.root, "popup", tc.dir)
			mkdir(t, dir)
			name := session.PopupName(s.Config.Popup.SessionPrefix, dir)
			created, err := s.popupStart(ctx, e.Host, name, dir, tc.user)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("popupStart = %v, %v; want error %q", created, err, tc.wantErr)
				}
				if running, err := s.Client.HasSession(ctx, name); err != nil || running {
					t.Fatalf("session %s running %v after a refused launch (%v)", name, running, err)
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("popupStart created %v: %v", created, err)
			}
			var inv fakeclaude.Invocation
			tmuxtest.WaitFor(t, "claude started in "+name, func() bool {
				for _, r := range fakeclaude.ReadRecords(t, e.record) {
					if r.Kind == "invocation" && r.Env[session.EnvSession] == name {
						inv = r
						return true
					}
				}
				return false
			})
			if inv.Program != tc.wantProgram {
				t.Fatalf("program %q, want %q", inv.Program, tc.wantProgram)
			}
			n := len(inv.Args) - len(tc.wantTail)
			if n < 0 || !slices.Equal(inv.Args[n:], tc.wantTail) {
				t.Fatalf("args %q, want ending with %q", inv.Args, tc.wantTail)
			}
			// The configured list is replaced, never appended to.
			if slices.Contains(inv.Args[:n], "--verbose") {
				t.Fatalf("args %q still carry the configured arguments", inv.Args)
			}
		})
	}
}

func TestPopupLaunchOptions(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := Host{
		Home:   root,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "my-agent" {
				return agent, nil
			}
			return "", os.ErrNotExist
		},
	}
	cases := []struct {
		name    string
		user    tmux.PluginUserOptions
		want    LaunchOptions
		wantErr string
		wantIs  error
	}{
		{name: "both empty"},
		{name: "blank args are unset", user: tmux.PluginUserOptions{Args: "  "}},
		{name: "absolute command", user: tmux.PluginUserOptions{Command: agent}, want: LaunchOptions{Command: agent}},
		{name: "command on PATH", user: tmux.PluginUserOptions{Command: "my-agent"}, want: LaunchOptions{Command: agent}},
		{name: "args split without a shell", user: tmux.PluginUserOptions{Args: `--append-system-prompt "be brief" '$HOME' *`}, want: LaunchOptions{Args: []string{"--append-system-prompt", "be brief", "$HOME", "*"}}},
		{name: "command with arguments", user: tmux.PluginUserOptions{Command: "claude --verbose"}, wantErr: tmux.OptClaudeCommand + ` must be a command name on PATH or an absolute path (got "claude --verbose")`},
		{name: "relative command", user: tmux.PluginUserOptions{Command: "bin/agent"}, wantErr: tmux.OptClaudeCommand + " must be a command name"},
		{name: "control character in the command", user: tmux.PluginUserOptions{Command: agent + "\n"}, wantErr: tmux.OptClaudeCommand + " must be a command name"},
		{name: "command not on PATH", user: tmux.PluginUserOptions{Command: "absent"}, wantErr: tmux.OptClaudeCommand + ": claude: Claude Code executable not found at absent", wantIs: claude.ErrNotFound},
		{name: "command file missing", user: tmux.PluginUserOptions{Command: filepath.Join(root, "absent")}, wantErr: "not found at " + filepath.Join(root, "absent"), wantIs: claude.ErrNotFound},
		{name: "unterminated quote", user: tmux.PluginUserOptions{Args: "--model 'opus"}, wantErr: tmux.OptClaudeArgs + ": an opening ' has no closing '"},
		{name: "trailing backslash", user: tmux.PluginUserOptions{Args: `--model opus\`}, wantErr: tmux.OptClaudeArgs + ": a backslash at the end escapes nothing"},
		{name: "control character in the args", user: tmux.PluginUserOptions{Args: "--debug\x1b[2J"}, wantErr: tmux.OptClaudeArgs + " must be a single-line value without control characters"},
		{name: "control character inside quotes", user: tmux.PluginUserOptions{Args: "'a\x07b'"}, wantErr: tmux.OptClaudeArgs + " must be a single-line value"},
		{name: "invalid UTF-8 in the args", user: tmux.PluginUserOptions{Args: "--debug\xff"}, wantErr: tmux.OptClaudeArgs + " must be a single-line value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := popupLaunchOptions(h, tc.user)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("popupLaunchOptions = %+v, %v; want error containing %q", got, err, tc.wantErr)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Fatalf("err = %v, want %v", err, tc.wantIs)
				}
				if got.Command != "" || got.Args != nil {
					t.Fatalf("refused options still returned %+v", got)
				}
				return
			}
			if err != nil || got.Command != tc.want.Command || !slices.Equal(got.Args, tc.want.Args) || (got.Args == nil) != (tc.want.Args == nil) {
				t.Fatalf("popupLaunchOptions = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPopupRequestValidate(t *testing.T) {
	cases := []struct {
		name    string
		req     PopupRequest
		wantErr string
	}{
		{name: "terminal client", req: PopupRequest{Pane: "%3", Client: "/dev/ttys003"}},
		{name: "control mode client", req: PopupRequest{Pane: "%0", Client: "client-1234"}},
		{name: "pane name", req: PopupRequest{Pane: "main", Client: "c"}, wantErr: "not a pane id"},
		{name: "pane pattern", req: PopupRequest{Pane: "%*", Client: "c"}, wantErr: "not a pane id"},
		{name: "no client", req: PopupRequest{Pane: "%3"}, wantErr: "not a tmux client name"},
		{name: "control character in client", req: PopupRequest{Pane: "%3", Client: "/dev/tty\n"}, wantErr: "not a tmux client name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.popupValidate()
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("popupValidate = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPopupTmuxPath(t *testing.T) {
	cases := []struct {
		name    string
		bin     string
		look    func(string) (string, error)
		want    string
		wantErr string
	}{
		{name: "absolute binary", bin: "/opt/homebrew/bin/tmux", want: "/opt/homebrew/bin/tmux"},
		{name: "found on PATH", bin: "tmux", look: func(string) (string, error) { return "/usr/bin/tmux", nil }, want: "/usr/bin/tmux"},
		{name: "relative PATH entry", bin: "tmux", look: func(string) (string, error) { return "bin/tmux", nil }, want: filepath.Join(popupCwd(t), "bin/tmux")},
		{name: "missing", bin: "tmux", look: func(string) (string, error) { return "", os.ErrNotExist }, wantErr: "find tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := popupTmuxPath(Host{LookPath: tc.look}, tc.bin)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("popupTmuxPath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func popupCwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
