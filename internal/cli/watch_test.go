package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// watchProgramTimeout bounds a changes view run by a test.
const watchProgramTimeout = 15 * time.Second

// watchBuffer is a program output safe to read while the program writes.
type watchBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *watchBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *watchBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// watchTerm is the input pipe and output of a program under test.
type watchTerm struct {
	in     *os.File
	keys   *os.File
	out    *watchBuffer
	errOut *watchBuffer
}

func newWatchTerm(t *testing.T) *watchTerm {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return &watchTerm{in: r, keys: w, out: &watchBuffer{}, errOut: &watchBuffer{}}
}

func (w *watchTerm) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := w.keys.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

func (w *watchTerm) waitScreen(t *testing.T, text string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("output:\n%s\nerrors:\n%s", ansi.Strip(w.out.String()), w.errOut.String())
		}
	})
	tmuxtest.WaitFor(t, "screen to show "+text, func() bool { return strings.Contains(ansi.Strip(w.out.String()), text) })
}

// watchWait returns the result of a program run, failing after the timeout.
func watchWait[T any](t *testing.T, done <-chan T) T {
	t.Helper()
	select {
	case v := <-done:
		return v
	case <-time.After(watchProgramTimeout):
		t.Fatal("the changes view did not exit")
		var zero T
		return zero
	}
}

// watchRunCLI runs the command tree with the terminal's streams in the
// background and returns a function that waits for its exit code.
func watchRunCLI(t *testing.T, e *cliEnv, term *watchTerm, args ...string) func() int {
	t.Helper()
	d := Deps{
		Host:     func() (app.Host, error) { return e.host, nil },
		Exec:     func(string, []string, []string) error { return errors.New("no process replacement in this test") },
		LookPath: func(name string) (string, error) { return name, nil },
		Getwd:    func() (string, error) { return e.cwd, nil },
		Terminal: func() Terminal { return e.term },
		Now:      time.Now,
	}
	ctx, cancel := context.WithTimeout(t.Context(), watchProgramTimeout)
	t.Cleanup(cancel)
	root := NewRootWith(Streams{In: term.in, Out: term.out, Err: term.errOut}, d)
	done := make(chan int, 1)
	go func() { done <- run(ctx, root, args) }()
	return func() int { return watchWait(t, done) }
}

// watchFakeSource serves a settable list of changed files.
type watchFakeSource struct {
	mu    sync.Mutex
	files []watch.File
	calls atomic.Int64
}

func (s *watchFakeSource) Repo(context.Context, string) (watch.Repo, error) {
	return watch.Repo{}, watch.ErrNotRepository
}

func (s *watchFakeSource) Changes(context.Context, string) (watch.Changes, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return watch.Changes{Branch: watch.Branch{Head: "main"}, Files: append([]watch.File(nil), s.files...)}, nil
}

func (s *watchFakeSource) set(paths ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = s.files[:0]
	for _, p := range paths {
		s.files = append(s.files, watch.File{Kind: watch.KindUntracked, Path: p, Index: '?', Worktree: '?'})
	}
}

func TestWatchCLI(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", e.host.Home)
	repo := filepath.Join(e.host.Home, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", repo)
	git.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.host.Home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "hello-watch.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := tmuxtest.Context(t)
	s, err := app.OpenServer(ctx, e.host)
	if err != nil {
		t.Fatal(err)
	}
	pane, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	channel := tmux.ChangesChannel(watchSessionID(t, s, pane))
	inPane := map[string]string{"TMUX": app.SocketPath(e.host.Getenv, e.env["LYNA_TMUX_SOCKET_NAME"]) + ",1,0", "TMUX_PANE": pane}

	cases := []struct {
		name        string
		env         map[string]string
		nonTerminal bool
		inRepo      bool
		args        []string
		// keys drives a running view; nil runs the command to completion.
		keys     func(t *testing.T, term *watchTerm)
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{
			name: "outside tmux a popup follows the named workspace and closes on q",
			env:  map[string]string{"TMUX": ""}, args: []string{"watch", "--session", "api", "--dir", "repo", "--popup"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "hello-watch.txt")
				term.send(t, "q")
			},
		},
		{
			name: "the current directory is the default",
			env:  map[string]string{"TMUX": ""}, inRepo: true, args: []string{"watch", "--session", "api", "--popup"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "hello-watch.txt")
				term.send(t, "\x03")
			},
		},
		{
			name: "a pane follows its workspace channel, ignores q and closes on ctrl+c",
			env:  inPane, inRepo: true, args: []string{"watch"},
			keys: func(t *testing.T, term *watchTerm) {
				t.Helper()
				term.waitScreen(t, "hello-watch.txt")
				term.send(t, "q")
				if err := os.WriteFile(filepath.Join(repo, "second-watch.txt"), []byte("hi\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(filepath.Join(repo, "second-watch.txt")) })
				if _, err := s.Client.Run(tmuxtest.Context(t), "wait-for", "-S", channel); err != nil {
					t.Fatal(err)
				}
				term.waitScreen(t, "second-watch.txt")
				term.send(t, "\x03")
			},
		},
		{name: "no terminal", nonTerminal: true, args: []string{"watch", "--session", "api"}, wantCode: 1, errHas: []string{"watch draws a live view and needs a terminal"}},
		{name: "outside tmux without a session", env: map[string]string{"TMUX": ""}, args: []string{"watch"}, wantCode: 1, errHas: []string{"no workspace to follow: outside tmux, pass --session with a workspace name"}},
		{name: "in tmux without a pane or session", args: []string{"watch"}, wantCode: 1, errHas: []string{"no workspace to follow: pass --session"}},
		{name: "missing workspace", env: map[string]string{"TMUX": ""}, args: []string{"watch", "--session", "nope"}, wantCode: 1, errHas: []string{"no such workspace: nope"}},
		{name: "invalid session name", env: map[string]string{"TMUX": ""}, args: []string{"watch", "--session", "a:b"}, wantCode: 1, errHas: []string{`invalid session name: "a:b" contains ':'`}},
		{name: "missing directory", env: map[string]string{"TMUX": ""}, args: []string{"watch", "--session", "api", "--dir", "gone"}, wantCode: 1, errHas: []string{"changes directory", "no such file"}},
		{name: "arguments are rejected", args: []string{"watch", "extra"}, wantCode: 1, errHas: []string{"unknown command \"extra\""}},
		{name: "hidden from the command list", args: []string{"--help"}, outHas: []string{"\n    review [command]"}, outLacks: []string{"\n    watch"}},
		{name: "own help", args: []string{"watch", "--help"}, outHas: []string{"--session", "--dir", "--popup"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := map[string]string{}
			for k, v := range tc.env {
				saved[k] = e.env[k]
				e.setenv(k, v)
			}
			e.cwd, e.term.Interactive = e.host.Home, !tc.nonTerminal
			if tc.inRepo {
				e.cwd = repo
			}
			t.Cleanup(func() {
				for k, v := range saved {
					e.setenv(k, v)
				}
			})
			var (
				code           int
				stdout, stderr string
			)
			// Every case runs on the bounded harness, so a command that draws
			// a view where it should have refused fails instead of hanging.
			term := newWatchTerm(t)
			wait := watchRunCLI(t, e, term, tc.args...)
			if tc.keys != nil {
				tc.keys(t, term)
			}
			code, stdout, stderr = wait(), term.out.String(), term.errOut.String()
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
}

// TestWatchRun runs the changes view in a real program: a popup closes on q,
// a pane ignores q and keeps refreshing, and ctrl+c closes both. The watcher
// stops with the view.
func TestWatchRun(t *testing.T) {
	cases := []struct {
		name  string
		popup bool
		keys  func(t *testing.T, term *watchTerm, refresh func(paths ...string), done <-chan error)
	}{
		{
			name: "popup closes on q", popup: true,
			keys: func(t *testing.T, term *watchTerm, _ func(...string), _ <-chan error) {
				t.Helper()
				term.waitScreen(t, "alpha.go")
				term.send(t, "q")
			},
		},
		{
			name: "pane ignores q and keeps refreshing until ctrl+c",
			keys: func(t *testing.T, term *watchTerm, refresh func(...string), done <-chan error) {
				t.Helper()
				term.waitScreen(t, "alpha.go")
				term.send(t, "q")
				refresh("alpha.go", "zulu.txt")
				term.waitScreen(t, "zulu.txt")
				select {
				case err := <-done:
					t.Fatalf("the pane view exited on q: %v", err)
				default:
				}
				term.send(t, "\x03")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			look, err := tui.ThemeFromConfig(config.Default().UI, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			src := &watchFakeSource{}
			src.set("alpha.go")
			signal := make(chan struct{})
			var signalStopped atomic.Bool
			view := app.WatchView{
				Watcher: &watch.Watcher{
					Dir: t.TempDir(), Source: src, Debounce: time.Millisecond,
					Signal: func(ctx context.Context) error {
						select {
						case <-signal:
							return nil
						case <-ctx.Done():
							signalStopped.Store(true)
							return ctx.Err()
						}
					},
				},
				Options: tui.ChangesOptions{Styles: tui.NewStyles(look), Popup: tc.popup, Dir: "/src/api", Width: 80, Height: 20},
			}
			term := newWatchTerm(t)
			ctx, cancel := context.WithTimeout(t.Context(), watchProgramTimeout)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- watchRun(ctx, view, Streams{In: term.in, Out: term.out, Err: term.errOut}, tea.WithoutSignalHandler())
			}()
			refresh := func(paths ...string) {
				src.set(paths...)
				select {
				case signal <- struct{}{}:
				case <-time.After(watchProgramTimeout):
					t.Fatal("the watcher is not waiting for a signal")
				}
			}
			tc.keys(t, term, refresh, done)
			if err := watchWait(t, done); err != nil {
				t.Fatalf("watchRun: %v", err)
			}
			if !signalStopped.Load() {
				t.Fatal("the watcher outlived the view")
			}
		})
	}
}

// TestWatchRunPanicStopsTheWatcher pins the order of the deferred stop and
// wait of watchRun: a panic out of the program has to stop the watcher before
// waiting for it. Waiting first blocks forever, since the watcher goroutine
// ends only on the canceled context.
func TestWatchRunPanicStopsTheWatcher(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "a string panic", value: "the program could not start"},
		{name: "an error panic", value: errors.New("the program could not start")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			look, err := tui.ThemeFromConfig(config.Default().UI, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			var signalStopped atomic.Bool
			view := app.WatchView{
				Watcher: &watch.Watcher{
					Dir: t.TempDir(), Source: &watchFakeSource{}, Debounce: time.Millisecond,
					Signal: func(ctx context.Context) error {
						<-ctx.Done()
						signalStopped.Store(true)
						return ctx.Err()
					},
				},
				Options: tui.ChangesOptions{Styles: tui.NewStyles(look), Dir: "/src/api", Width: 80, Height: 20},
			}
			term := newWatchTerm(t)
			ctx, cancel := context.WithTimeout(t.Context(), watchProgramTimeout)
			defer cancel()
			done := make(chan any, 1)
			go func() {
				defer func() { done <- recover() }()
				// The option panics where the program is built: after the
				// deferred stop and wait, before the stop on the happy path.
				_ = watchRun(ctx, view, Streams{In: term.in, Out: term.out, Err: term.errOut},
					func(*tea.Program) { panic(tc.value) })
			}()
			if got := watchWait(t, done); got != tc.value {
				t.Fatalf("recovered %v, want %v", got, tc.value)
			}
			if !signalStopped.Load() {
				t.Fatal("the watcher outlived the call")
			}
		})
	}
}

// watchSessionID reads the id of the session holding pane from the live server.
func watchSessionID(t *testing.T, s *app.Server, pane string) string {
	t.Helper()
	id, err := s.Client.Display(tmuxtest.Context(t), pane, "#{session_id}")
	if err != nil || id == "" {
		t.Fatalf("session id of pane %s: %q, %v", pane, id, err)
	}
	return id
}

// TestWatchFollowsRenamedWorkspace runs the signal loop of a pane's changes
// view on an isolated server, renames the workspace with `lyna-tmux rename`
// and checks that signals on the workspace's channel, keyed on a session id
// the rename keeps, still refresh the view afterwards.
func TestWatchFollowsRenamedWorkspace(t *testing.T) {
	e := newCLIEnv(t)
	e.start(t, "api", e.host.Home)
	ctx := tmuxtest.Context(t)
	s, err := app.OpenServer(ctx, e.host)
	if err != nil {
		t.Fatal(err)
	}
	pane, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	channel := tmux.ChangesChannel(watchSessionID(t, s, pane))
	// The view runs in that pane: its host sees the pane's TMUX and TMUX_PANE.
	viewEnv := map[string]string{}
	for k, v := range e.env {
		viewEnv[k] = v
	}
	viewEnv["TMUX"] = app.SocketPath(e.host.Getenv, e.env["LYNA_TMUX_SOCKET_NAME"]) + ",1,0"
	viewEnv["TMUX_PANE"] = pane
	h := e.host
	h.Getenv = func(k string) string { return viewEnv[k] }

	view, err := app.OpenWatch(ctx, h, app.WatchRequest{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	src := &watchFakeSource{}
	w := view.Watcher
	w.Source, w.Interval, w.Debounce = src, 0, time.Millisecond
	runCtx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Go(func() { w.Run(runCtx, func(watch.Update) {}) })
	tmuxtest.WaitFor(t, "the first refresh", func() bool { return src.calls.Load() >= 1 })

	if code, _, stderr := e.run(t, "rename", "api", "backend"); code != 0 {
		t.Fatalf("rename: %s", stderr)
	}
	if got := tmux.ChangesChannel(watchSessionID(t, s, pane)); got != channel {
		t.Fatalf("channel %q after the rename, was %q", got, channel)
	}
	// Two rounds: the first proves the wait outlived the rename, the second
	// that the loop went back to waiting on the same channel.
	for round := 1; round <= 2; round++ {
		before := src.calls.Load()
		deadline := time.Now().Add(watchProgramTimeout)
		for src.calls.Load() <= before {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: no refresh after a signal on %s", round, channel)
			}
			// Signaling again is harmless: tmux latches one wake per channel.
			if _, err := s.Client.Run(tmuxtest.Context(t), "wait-for", "-S", channel); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
