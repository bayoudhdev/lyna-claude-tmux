package app

import (
	"errors"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

func sessionNames(t *testing.T, s *Server) []string {
	t.Helper()
	list, err := s.Sessions(tmuxtest.Context(t))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, x := range list {
		names = append(names, x.Name)
	}
	slices.Sort(names)
	return names
}

// TestRenameKeepsChangesViewsWaiting pins the contract between a rename and
// the changes views of the workspace: their channel is keyed on the session
// id, which the rename does not touch, so a view waiting before the rename
// is still waiting after it, and the next hook signal on that same channel
// wakes it. The same holds when the rename is refused.
func TestRenameKeepsChangesViewsWaiting(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
		wantErr  error
	}{
		{name: "renamed", from: "api", to: "backend"},
		{name: "rename onto a taken name", from: "api", to: "web", wantErr: tmux.ErrExists},
		{name: "missing workspace", from: "nope", to: "other", wantErr: ErrNoWorkspace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHost(t)
			s := openServer(t, h)
			ctx := tmuxtest.Context(t)
			startWorkspace(t, s, "api")
			startWorkspace(t, s, "web")
			pane, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.Client.Display(ctx, pane, "#{session_id}")
			if err != nil || id == "" {
				t.Fatalf("session id: %q, %v", id, err)
			}
			// The view's own signal source; wait-for latches, so whether the
			// wait or the rename reaches tmux first changes nothing below.
			done := make(chan error, 1)
			signal := watch.TmuxPaneSignal(s.Client, pane)
			go func() { done <- signal(ctx) }()

			if err := s.Rename(ctx, tc.from, tc.to); !errors.Is(err, tc.wantErr) {
				t.Fatalf("rename: err = %v, want %v", err, tc.wantErr)
			}
			select {
			case err := <-done:
				t.Fatalf("the rename woke the view (err %v)", err)
			case <-time.After(300 * time.Millisecond):
			}
			if got, err := s.Client.Display(ctx, pane, "#{session_id}"); err != nil || got != id {
				t.Fatalf("session id after the rename = %q, %v; want %q", got, err, id)
			}
			if _, err := s.Client.Run(ctx, "wait-for", "-S", tmux.ChangesChannel(id)); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("the wait ended with %v", err)
				}
			case <-ctx.Done():
				t.Fatal("the signal on the session's channel did not wake the view")
			}
		})
	}
}

func TestSessionCommands(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)

	if names := sessionNames(t, s); len(names) != 0 {
		t.Fatalf("stopped server lists %q", names)
	}
	if err := s.KillAll(ctx); err != nil {
		t.Fatalf("KillAll on a stopped server: %v", err)
	}
	startWorkspace(t, s, "api")
	startWorkspace(t, s, "web")

	steps := []struct {
		name      string
		run       func() error
		wantIs    error
		wantNames []string
	}{
		{name: "rename", run: func() error { return s.Rename(ctx, "api", "backend") }, wantNames: []string{"backend", "web"}},
		{name: "rename onto an existing name", run: func() error { return s.Rename(ctx, "backend", "web") }, wantIs: tmux.ErrExists, wantNames: []string{"backend", "web"}},
		{name: "rename a missing workspace", run: func() error { return s.Rename(ctx, "nope", "x") }, wantIs: ErrNoWorkspace, wantNames: []string{"backend", "web"}},
		{name: "rename to an invalid name", run: func() error { return s.Rename(ctx, "web", "a:b") }, wantIs: session.ErrInvalidName, wantNames: []string{"backend", "web"}},
		{name: "prefix does not match another session", run: func() error { return s.Kill(ctx, "back") }, wantIs: ErrNoWorkspace, wantNames: []string{"backend", "web"}},
		{name: "kill", run: func() error { return s.Kill(ctx, "backend") }, wantNames: []string{"web"}},
		{name: "kill an invalid name", run: func() error { return s.Kill(ctx, "-t") }, wantIs: session.ErrInvalidName, wantNames: []string{"web"}},
		{name: "kill all", run: func() error { return s.KillAll(ctx) }, wantNames: []string{}},
		{name: "kill on a stopped server", run: func() error { return s.Kill(ctx, "web") }, wantIs: ErrNoWorkspace, wantNames: []string{}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			err := st.run()
			if st.wantIs == nil && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if st.wantIs != nil && !errors.Is(err, st.wantIs) {
				t.Fatalf("err = %v, want %v", err, st.wantIs)
			}
			if got := sessionNames(t, s); !slices.Equal(got, st.wantNames) {
				t.Fatalf("sessions = %q, want %q", got, st.wantNames)
			}
		})
	}
}

func TestAttachCommand(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)

	if _, err := s.AttachCommand(ctx, h.Host, "api"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("stopped server: err = %v", err)
	}
	startWorkspace(t, s, "api")
	if _, err := s.AttachCommand(ctx, h.Host, "ap"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("prefix name: err = %v", err)
	}
	att, err := s.AttachCommand(ctx, h.Host, "api")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range att.Env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "CLAUDE_CODE_MESSAGING_TOKEN=") {
			t.Fatalf("attach environment has %q", kv)
		}
	}

	// Run the attach command in a pane of a separate terminal server, as a
	// user's terminal would, and wait for the client to appear.
	term := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: tmuxtest.Socket(t, h.TmuxBin)}, Config: "/dev/null", Env: att.Env})
	if _, err := term.Run(ctx, "new-session", "-d", "-s", "term", "-x", "120", "-y", "30", tmux.ShellJoin(att.Argv...)); err != nil {
		t.Fatal(err)
	}
	tmuxtest.WaitFor(t, "a client attached to api", func() bool {
		out, err := s.Client.Run(ctx, "list-clients", "-F", "#{session_name}")
		return err == nil && strings.TrimSpace(out) == "api"
	})

	// From a pane of the same server, the command switches that client.
	startWorkspace(t, s, "web")
	socket, err := s.Client.Display(ctx, "", "#{socket_path}")
	if err != nil {
		t.Fatal(err)
	}
	pane, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	inner := *h
	inner.env = maps.Clone(h.env)
	inner.env["TMUX"] = socket + ",1,0"
	inner.env["TMUX_PANE"] = pane
	inner.Getenv = func(k string) string { return inner.env[k] }
	if !s.Inside(inner.Host) || s.Inside(h.Host) {
		t.Fatalf("Inside: pane %v, outer %v", s.Inside(inner.Host), s.Inside(h.Host))
	}
	sw, err := s.AttachCommand(ctx, inner.Host, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sw.Argv, "switch-client") || !slices.Contains(sw.Env, "TMUX_PANE="+pane) {
		t.Fatalf("switch command %q env %q", sw.Argv, sw.Env)
	}
	cmd := exec.CommandContext(ctx, sw.Argv[0], sw.Argv[1:]...)
	cmd.Env = sw.Env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("switch-client: %v: %s", err, out)
	}
	tmuxtest.WaitFor(t, "the client switched to web", func() bool {
		out, err := s.Client.Run(ctx, "list-clients", "-F", "#{session_name}")
		return err == nil && strings.TrimSpace(out) == "web"
	})
}

func TestInside(t *testing.T) {
	s := &Server{SocketName: "lyna-tmux"}
	path := SocketPath(func(string) string { return "" }, "lyna-tmux")
	cases := []struct {
		name string
		tmux string
		want bool
	}{
		{"own server", path + ",42,0", true},
		{"own server unclean path", filepath.Dir(path) + "/./lyna-tmux,42,0", true},
		{"user tmux", filepath.Join(filepath.Dir(path), "default") + ",42,0", false},
		{"not in tmux", "", false},
		{"no fields", path, false},
		{"empty socket", ",42,0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Getenv: func(k string) string {
				if k == "TMUX" {
					return tc.tmux
				}
				return ""
			}}
			if got := s.Inside(h); got != tc.want {
				t.Fatalf("Inside(%q) = %v, want %v", tc.tmux, got, tc.want)
			}
		})
	}
}

// TestForeignTmux pins which servers count as another tmux. Only those nest a
// second server in one terminal; the lyna-tmux server switches its own client
// and no tmux at all attaches normally.
func TestForeignTmux(t *testing.T) {
	s := &Server{SocketName: "lyna-tmux"}
	path := SocketPath(func(string) string { return "" }, "lyna-tmux")
	other := filepath.Join(filepath.Dir(path), "default")
	cases := []struct {
		name       string
		tmux       string
		wantSocket string
		want       bool
	}{
		{name: "another server", tmux: other + ",42,0", wantSocket: other, want: true},
		{name: "another server unclean path", tmux: filepath.Dir(path) + "/./default,42,0", wantSocket: other, want: true},
		{name: "own server", tmux: path + ",42,0"},
		{name: "not in tmux"},
		{name: "empty socket", tmux: ",42,0"},
		// Without the comma tmux never wrote the variable, so nothing is known
		// about a server and nothing is refused.
		{name: "no fields", tmux: path},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Host{Getenv: func(k string) string {
				if k == "TMUX" {
					return tc.tmux
				}
				return ""
			}}
			socket, got := foreignSocket(h, s.SocketName)
			if got != tc.want || socket != tc.wantSocket {
				t.Fatalf("foreignSocket(%q) = %q, %v, want %q, %v", tc.tmux, socket, got, tc.wantSocket, tc.want)
			}
		})
	}
}

// TestForeignTmuxNeedsALiveSocket covers the variable a shell keeps after the
// server it came from exited: there is nothing left to nest inside, so that
// terminal attaches normally.
func TestForeignTmuxNeedsALiveSocket(t *testing.T) {
	s := &Server{SocketName: "lyna-tmux"}
	path := filepath.Join(t.TempDir(), "default")
	host := func() Host {
		return Host{Getenv: func(k string) string {
			if k == "TMUX" {
				return path + ",42,0"
			}
			return ""
		}}
	}
	if socket, ok := s.ForeignTmux(host()); ok {
		t.Fatalf("missing socket reported as %q", socket)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if socket, ok := s.ForeignTmux(host()); !ok || socket != path {
		t.Fatalf("live socket = %q, %v", socket, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if socket, ok := s.ForeignTmux(host()); ok {
		t.Fatalf("a plain file at the socket path reported as %q", socket)
	}
}

// TestAttachCommandRefusesNesting covers the terminal that already runs a tmux
// of its own: attaching there puts one server inside another, which is what
// makes the outer session eat the prefix key and the terminal's own answers
// land in the agent pane as typed text.
func TestAttachCommandRefusesNesting(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)
	startWorkspace(t, s, "api")

	// A second tmux server, as a terminal already running one would have.
	other := tmuxtest.Socket(t, h.TmuxBin)
	if _, err := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: tmux.Socket{Name: other}, Config: "/dev/null"}).
		Run(ctx, "new-session", "-d", "-s", "outer", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	socket := tmuxtest.SocketPath(other)
	outer := *h
	outer.env = maps.Clone(h.env)
	outer.env["TMUX"] = socket + ",42,0"
	outer.Getenv = func(k string) string { return outer.env[k] }

	_, err := s.AttachCommand(ctx, outer.Host, "api")
	if !errors.Is(err, ErrNestedTmux) {
		t.Fatalf("nested attach: err = %v", err)
	}
	for _, want := range []string{socket, "--nested"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	att, err := s.AttachCommand(ctx, outer.Host, "api", AllowNested())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(att.Argv, "attach-session") {
		t.Fatalf("forced attach %q", att.Argv)
	}
}
