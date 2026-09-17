package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
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

// noteText reads the line a failed review action reported to the view.
func noteText(t *testing.T, msg tea.Msg) string {
	t.Helper()
	note, ok := msg.(tui.ChangesNoteMsg)
	if !ok {
		t.Fatalf("message %#v is not a changes note", msg)
	}
	return note.Text
}

// fakeSource is a working tree that answers a fixed root, or an error.
type fakeSource struct {
	root string
	err  error
}

func (s fakeSource) Repo(context.Context, string) (watch.Repo, error) {
	if s.err != nil {
		return watch.Repo{}, s.err
	}
	return watch.Repo{Root: s.root, GitDir: filepath.Join(s.root, ".git"), CommonDir: filepath.Join(s.root, ".git")}, nil
}

func (s fakeSource) Changes(context.Context, string) (watch.Changes, error) {
	return watch.Changes{}, s.err
}

func TestWatchReviewOpen(t *testing.T) {
	t.Parallel()
	const root = "/src/api"
	cases := []struct {
		name    string
		review  watchReview
		path    string
		want    tmux.Command
		wantMsg string
	}{
		{
			name:   "the whole working tree",
			review: watchReview{src: fakeSource{root: root}, exe: "/opt/bin/lyna-tmux", pane: "%7", dir: root + "/internal"},
			want: tmux.Command{
				"display-popup", "-t", "%7", "-E", "-w", "95%", "-h", "95%", "-d", root, "-T", " review ",
				"--", "/opt/bin/lyna-tmux", "review", "--popup", "--dir", root,
			},
		},
		{
			// The path is relative to the top of the working tree, not to the
			// watched directory, and the review is opened at the top.
			name:   "one file of a watched subdirectory",
			review: watchReview{src: fakeSource{root: root}, exe: "/opt/bin/lyna-tmux", pane: "%7", dir: root + "/internal"},
			path:   "internal/tui/changes.go",
			want: tmux.Command{
				"display-popup", "-t", "%7", "-E", "-w", "95%", "-h", "95%", "-d", root, "-T", " review ",
				"--", "/opt/bin/lyna-tmux", "review", "--popup", "--dir", root, "--", "internal/tui/changes.go",
			},
		},
		{
			name:    "a directory that is no repository",
			review:  watchReview{src: fakeSource{err: watch.ErrNotRepository}, exe: "/opt/bin/lyna-tmux", pane: "%7", dir: root},
			wantMsg: "review: " + watch.ErrNotRepository.Error(),
		},
		{
			name:    "a pane that is no pane",
			review:  watchReview{src: fakeSource{root: root}, exe: "/opt/bin/lyna-tmux", pane: "review", dir: root},
			wantMsg: "review: popup: pane \"review\" is not a pane id such as %3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []tmux.Command
			r := tc.review
			r.run = func(context.Context, tmux.Command) error { return nil }
			if tc.wantMsg == "" {
				r.run = func(_ context.Context, cmd tmux.Command) error {
					got = append(got, cmd)
					return nil
				}
			}
			msg := r.open(context.Background(), tc.path)()
			if tc.wantMsg != "" {
				if text := noteText(t, msg); text != tc.wantMsg {
					t.Fatalf("note = %q, want %q", text, tc.wantMsg)
				}
				if len(got) != 0 {
					t.Fatalf("ran %q after failing", got)
				}
				return
			}
			if msg != nil {
				t.Fatalf("message %#v after opening the review", msg)
			}
			if len(got) != 1 || !slices.Equal(got[0], tc.want) {
				t.Fatalf("commands =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestWatchReviewReportsTheServer checks that a tmux failure reaches the view
// instead of being swallowed by the command that opened the popup.
func TestWatchReviewReportsTheServer(t *testing.T) {
	t.Parallel()
	r := watchReview{
		src: fakeSource{root: "/src/api"}, exe: "/opt/bin/lyna-tmux", pane: "%1", dir: "/src/api",
		run: func(context.Context, tmux.Command) error { return errors.New("no server running") },
	}
	if text := noteText(t, r.open(context.Background(), "")()); text != "review: no server running" {
		t.Fatalf("note = %q", text)
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
	// channel is the changes channel of a workspace, read live: it is keyed on
	// the session id, which a rename keeps.
	channel := func(session string) string {
		id, err := s.Client.Display(tmuxtest.Context(t), tmux.ExactSession(session), "#{session_id}")
		if err != nil || id == "" {
			t.Fatalf("session id of %s: %q, %v", session, id, err)
		}
		return tmux.ChangesChannel(id)
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
				before := channel("moving")
				watchExpectWake(t, s, v.Watcher.Signal, before)
				if err := s.Rename(tmuxtest.Context(t), "moving", "moved"); err != nil {
					t.Fatal(err)
				}
				if after := channel("moved"); after != before {
					t.Fatalf("channel %q after the rename, was %q", after, before)
				}
				watchExpectWake(t, s, v.Watcher.Signal, before)
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
				if o.Open == nil || o.OpenReview == nil {
					t.Error("a pane opens no review")
				}
				w := v.Watcher
				if w.Dir != dir || w.Idle != WatchIdle || w.Signal == nil {
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
				// A second popup would close this one.
				if v.Options.Open != nil || v.Options.OpenReview != nil {
					t.Error("a popup opens a review over itself")
				}
			},
		},
		{
			name: "tmux without a pane follows --session",
			env:  map[string]string{"TMUX": inTmux},
			req:  WatchRequest{Dir: dir, Session: "api"},
			check: func(t *testing.T, v WatchView) {
				watchExpectWake(t, s, v.Watcher.Signal, channel("api"))
			},
		},
		{name: "tmux without a pane or session", env: map[string]string{"TMUX": inTmux}, req: WatchRequest{Dir: dir}, wantErr: ErrWatchNoSession},
		{name: "unreachable pane", env: map[string]string{"TMUX": inTmux, "TMUX_PANE": "%9999"}, req: WatchRequest{Dir: dir}, errHas: "cannot read the workspace of pane %9999"},
		{
			name: "outside tmux follows the named workspace",
			env:  map[string]string{"TMUX": ""},
			req:  WatchRequest{Dir: dir, Session: "api"},
			check: func(t *testing.T, v WatchView) {
				watchExpectWake(t, s, v.Watcher.Signal, channel("api"))
				// There is no pane to draw a popup over.
				if v.Options.Open != nil || v.Options.OpenReview != nil {
					t.Error("a view outside tmux opens a review")
				}
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
