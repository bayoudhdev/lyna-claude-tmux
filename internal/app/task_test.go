package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestWsSpotLaunch checks that a window or pane command launches Claude with
// the sandbox the workspace recorded, and keeps the options of the command
// itself.
func TestWsSpotLaunch(t *testing.T) {
	workspace := tmux.PaneContext{Session: "api", Sandbox: "strict", Isolation: "container"}
	cases := []struct {
		name string
		pane tmux.PaneContext
		in   LaunchOptions
		want LaunchOptions
	}{
		{name: "recorded sandbox and isolation", pane: workspace, want: LaunchOptions{Sandbox: "strict", Isolation: "container"}},
		{name: "session without recorded options takes the configuration", pane: tmux.PaneContext{Session: "plain"}},
		{
			name: "half recorded", pane: tmux.PaneContext{Session: "api", Sandbox: "off"},
			want: LaunchOptions{Sandbox: "off"},
		},
		{
			name: "explicit options win", pane: workspace, in: LaunchOptions{Sandbox: "standard", Isolation: "bash"},
			want: LaunchOptions{Sandbox: "standard", Isolation: "bash"},
		},
		{
			name: "the command's own options are kept", pane: workspace, in: LaunchOptions{ExtraArgs: []string{"--resume"}, Continue: true},
			want: LaunchOptions{Sandbox: "strict", Isolation: "container", ExtraArgs: []string{"--resume"}, Continue: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wsSpot{pane: tc.pane}.launch(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("launch = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestWindowCommandsUseTheWorkspaceSandbox runs every command that adds panes
// to a running workspace after the configuration changed under it: they launch
// Claude with the profile and isolation level the workspace was opened with,
// not with the ones the configuration names now.
func TestWindowCommandsUseTheWorkspaceSandbox(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	if _, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: "solo", Launch: LaunchOptions{Sandbox: "strict"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.invocations(t, 1)[0].Env["LYNA_TMUX_SANDBOX"]; got != "strict" {
		t.Fatalf("workspace launched at %q, want strict", got)
	}
	// The configuration now names a profile that is refused without an
	// explicit request and an isolation level this host cannot run.
	s.Config.Sandbox.Profile, s.Config.Sandbox.Isolation = "off", "container"
	target := WindowTarget{Session: "api"}
	cases := []struct {
		name string
		open func() error
	}{
		{name: "split", open: func() error {
			_, err := s.SplitPane(ctx, e.Host, SplitRequest{Target: target, Role: layout.RoleClaude})
			return err
		}},
		{name: "task", open: func() error {
			_, err := s.OpenTask(ctx, e.Host, TaskRequest{Target: target, Name: "fix-login"})
			return err
		}},
		{name: "resume", open: func() error {
			_, err := s.OpenResume(ctx, e.Host, target)
			return err
		}},
		{name: "layout", open: func() error {
			_, err := s.OpenLayout(ctx, e.Host, LayoutRequest{Target: target, Layout: layout.Solo})
			return err
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.open(); err != nil {
				t.Fatal(err)
			}
			inv := e.invocations(t, i+2)[i+1]
			if inv.Env["LYNA_TMUX_SANDBOX"] != "strict" {
				t.Fatalf("%s launched at %q, want strict", tc.name, inv.Env["LYNA_TMUX_SANDBOX"])
			}
		})
	}
}

// TestWindowCommandsSelectTheOpenWindow runs each window command twice: the
// second run selects the window the first opened instead of building a second
// one, the way create returns the running workspace of a project.
func TestWindowCommandsSelectTheOpenWindow(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	if _, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo}); err != nil {
		t.Fatal(err)
	}
	target := WindowTarget{Session: "api"}
	windows := func(t *testing.T) []string {
		t.Helper()
		out, err := s.Client.Run(ctx, "list-windows", "-t", tmux.ExactSession("api"), "-F", "#{window_name}")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(out)
	}
	cases := []struct {
		name   string
		window string
		open   func() (WindowResult, error)
	}{
		{name: "task", window: "fix-login", open: func() (WindowResult, error) {
			return s.OpenTask(ctx, e.Host, TaskRequest{Target: target, Name: "fix-login"})
		}},
		{name: "resume", window: resumeWindow, open: func() (WindowResult, error) {
			return s.OpenResume(ctx, e.Host, target)
		}},
		{name: "layout", window: layout.Trio, open: func() (WindowResult, error) {
			return s.OpenLayout(ctx, e.Host, LayoutRequest{Target: target, Layout: layout.Trio})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := tc.open()
			if err != nil {
				t.Fatal(err)
			}
			if first.Existing || first.Session != "api" {
				t.Fatalf("first run %+v", first)
			}
			before := windows(t)
			again, err := tc.open()
			if err != nil {
				t.Fatal(err)
			}
			if !again.Existing || again.Built.WindowID != first.Built.WindowID {
				t.Fatalf("second run %+v, want the window of %+v", again, first)
			}
			if after := windows(t); !slices.Equal(after, before) {
				t.Fatalf("windows %q, want %q unchanged", after, before)
			}
			if n := strings.Count(strings.Join(before, " "), tc.window); n != 1 {
				t.Fatalf("%d windows named %s in %q", n, tc.window, before)
			}
			current, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{window_name}")
			if err != nil || current != tc.window {
				t.Fatalf("current window %q (%v), want the reused %s", current, err, tc.window)
			}
		})
	}
}

// TestWsLocate resolves every kind of window command target against a real
// workspace, a session that is not one, and a workspace whose project is gone.
func TestWsLocate(t *testing.T) {
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	ctx := tmuxtest.Context(t)
	res, err := s.Create(ctx, e.Host, CreateRequest{Dir: e.project, Layout: "duo"})
	if err != nil {
		t.Fatal(err)
	}
	claudePane, shellPane := res.Built.Panes[0], res.Built.Panes[1]
	// A plain session in a subdirectory of the project: no @lt_project, so the
	// root comes from the pane's directory.
	sub := filepath.Join(e.project, "src")
	if _, err := s.Client.Run(ctx, "new-session", "-d", "-s", "plain", "-c", sub, "--", "/bin/sh", "-c", "exec sleep 3600"); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(e.root, "gone")
	mkdir(t, gone)
	if _, err := s.Client.Batch(ctx,
		tmux.Command{"new-session", "-d", "-s", "orphan", "-c", gone, "--", "/bin/sh", "-c", "exec sleep 3600"},
		tmux.Command{"set-option", "-t", "=orphan:", tmux.OptProject, gone},
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	inside := SocketPath(e.Getenv, s.SocketName) + ",1,0"

	cases := []struct {
		name        string
		target      WindowTarget
		tmuxEnv     string
		tmuxPane    string
		wantPane    string
		wantSession string
		wantRoot    string
		wantErr     error
		errHas      string
	}{
		{name: "pane id", target: WindowTarget{Pane: shellPane}, wantPane: shellPane, wantSession: "api", wantRoot: e.project},
		{name: "pane wins over session", target: WindowTarget{Pane: shellPane, Session: "plain"}, wantPane: shellPane, wantSession: "api", wantRoot: e.project},
		{name: "session uses its active pane", target: WindowTarget{Session: "api"}, wantPane: claudePane, wantSession: "api", wantRoot: e.project},
		{name: "calling pane inside the server", tmuxEnv: inside, tmuxPane: shellPane, wantPane: shellPane, wantSession: "api", wantRoot: e.project},
		{name: "session without a project", target: WindowTarget{Session: "plain"}, wantSession: "plain", wantRoot: e.project},
		{name: "pane of another tmux server is ignored", tmuxEnv: "/tmp/outer,1,0", tmuxPane: shellPane, wantErr: ErrNoTarget},
		{name: "no target", wantErr: ErrNoTarget},
		{name: "invalid calling pane", tmuxEnv: inside, tmuxPane: "api", wantErr: ErrNoTarget},
		{name: "pane name refused", target: WindowTarget{Pane: "=api:"}, errHas: "not a pane id"},
		{name: "invalid session name", target: WindowTarget{Session: "a.b"}, errHas: "invalid session name"},
		{name: "missing pane", target: WindowTarget{Pane: "%99999"}, wantErr: ErrNoWorkspace, errHas: "no pane %99999"},
		{name: "missing session", target: WindowTarget{Session: "nope"}, wantErr: ErrNoWorkspace, errHas: "nope"},
		{name: "project removed", target: WindowTarget{Session: "orphan"}, errHas: "no longer exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.env["TMUX"], e.env["TMUX_PANE"] = tc.tmuxEnv, tc.tmuxPane
			spot, err := s.wsLocate(ctx, e.Host, tc.target)
			if tc.wantErr != nil || tc.errHas != "" {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantPane != "" && spot.pane.ID != tc.wantPane || spot.pane.Session != tc.wantSession || spot.root != tc.wantRoot {
				t.Fatalf("spot %+v root %q, want pane %s session %s root %s", spot.pane, spot.root, tc.wantPane, tc.wantSession, tc.wantRoot)
			}
		})
	}
}
