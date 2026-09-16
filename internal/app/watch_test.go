package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// watchExpectWake signals channel, then checks that one wait of signal
// returns: tmux latches the signal until the wait starts.
func watchExpectWake(t *testing.T, s *Server, signal func(context.Context) error, channel string) {
	t.Helper()
	ctx := tmuxtest.Context(t)
	if _, err := s.Client.Run(ctx, "wait-for", "-S", channel); err != nil {
		t.Fatal(err)
	}
	if err := signal(ctx); err != nil {
		t.Fatalf("wait after a signal on %s: %v", channel, err)
	}
}

func TestOpenWatch(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	startWorkspace(t, s, "api")
	startWorkspace(t, s, "moving")
	ctx := tmuxtest.Context(t)
	pane := func(session string) string {
		id, err := s.Client.Display(ctx, tmux.ExactSession(session), "#{pane_id}")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	inTmux := SocketPath(h.Getenv, s.SocketName) + ",1,0"
	dir := t.TempDir()

	cases := []struct {
		name    string
		env     map[string]string
		config  string
		req     WatchRequest
		wantErr error
		errHas  string
		check   func(t *testing.T, v WatchView)
	}{
		{
			name: "pane follows its workspace across a rename",
			env:  map[string]string{"TMUX": inTmux, "TMUX_PANE": pane("moving")},
			req:  WatchRequest{Dir: dir, Session: "ignored-in-a-pane"},
			check: func(t *testing.T, v WatchView) {
				watchExpectWake(t, s, v.Watcher.Signal, "lt-changes-moving")
				if err := s.Rename(tmuxtest.Context(t), "moving", "moved"); err != nil {
					t.Fatal(err)
				}
				watchExpectWake(t, s, v.Watcher.Signal, "lt-changes-moved")
			},
		},
		{
			name: "view options and watcher",
			env:  map[string]string{"TMUX": inTmux, "TMUX_PANE": pane("api")},
			req:  WatchRequest{Dir: dir},
			check: func(t *testing.T, v WatchView) {
				o := v.Options
				if o.Popup || o.Dir != dir || o.Home != h.Home || o.Styles.Theme.Palette.Name != "lyna" || o.Updates != nil {
					t.Fatalf("options %+v", o)
				}
				w := v.Watcher
				if w.Dir != dir || w.Interval != WatchInterval || w.Signal == nil {
					t.Fatalf("watcher %+v", w)
				}
				if _, ok := w.Source.(watch.Runner); !ok {
					t.Fatalf("source %T, want the git runner", w.Source)
				}
			},
		},
		{
			name: "popup and configured theme",
			env:  map[string]string{"TMUX": inTmux, "TMUX_PANE": pane("api")}, config: "[ui]\ntheme = \"light\"\n",
			req: WatchRequest{Dir: dir, Popup: true},
			check: func(t *testing.T, v WatchView) {
				if !v.Options.Popup || v.Options.Styles.Theme.Palette.Name != "light" {
					t.Fatalf("options %+v", v.Options)
				}
			},
		},
		{
			name: "tmux without a pane follows --session",
			env:  map[string]string{"TMUX": inTmux},
			req:  WatchRequest{Dir: dir, Session: "api"},
			check: func(t *testing.T, v WatchView) {
				watchExpectWake(t, s, v.Watcher.Signal, "lt-changes-api")
			},
		},
		{name: "tmux without a pane or session", env: map[string]string{"TMUX": inTmux}, req: WatchRequest{Dir: dir}, wantErr: ErrWatchNoSession},
		{name: "unreachable pane", env: map[string]string{"TMUX": inTmux, "TMUX_PANE": "%9999"}, req: WatchRequest{Dir: dir}, errHas: "cannot read the workspace of pane %9999"},
		{
			name: "outside tmux follows the named workspace",
			env:  map[string]string{"TMUX": ""},
			req:  WatchRequest{Dir: dir, Session: "api"},
			check: func(t *testing.T, v WatchView) {
				watchExpectWake(t, s, v.Watcher.Signal, "lt-changes-api")
			},
		},
		{name: "outside tmux without a session", env: map[string]string{"TMUX": ""}, req: WatchRequest{Dir: dir}, wantErr: ErrWatchNoSession, errHas: "pass --session"},
		{name: "outside tmux with a missing workspace", env: map[string]string{"TMUX": ""}, req: WatchRequest{Dir: dir, Session: "nope"}, wantErr: ErrNoWorkspace},
		{name: "invalid session name", env: map[string]string{"TMUX": ""}, req: WatchRequest{Dir: dir, Session: "a;b"}, errHas: "invalid"},
		{name: "relative directory", req: WatchRequest{Dir: "src", Session: "api"}, errHas: "is not absolute"},
		{name: "missing directory", req: WatchRequest{Dir: filepath.Join(dir, "gone"), Session: "api"}, errHas: "no such file"},
		{name: "file instead of a directory", req: WatchRequest{Dir: "/dev/null", Session: "api"}, errHas: "is not a directory"},
		{name: "invalid configuration", env: map[string]string{"TMUX": inTmux, "TMUX_PANE": pane("api")}, config: "[ui]\ntheme = \"nope\"\n", req: WatchRequest{Dir: dir}, errHas: "theme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := map[string]string{}
			for k, v := range tc.env {
				saved[k] = h.env[k]
				h.env[k] = v
			}
			h.refreshEnviron()
			h.writeConfig(t, tc.config)
			t.Cleanup(func() {
				for k, v := range saved {
					h.env[k] = v
				}
				h.refreshEnviron()
			})
			v, err := OpenWatch(tmuxtest.Context(t), h.Host, tc.req)
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.errHas)
			case tc.wantErr == nil && tc.errHas == "" && err != nil:
				t.Fatalf("err = %v", err)
			}
			if tc.check != nil {
				tc.check(t, v)
			}
		})
	}
}
