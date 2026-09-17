package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// countingSource reports how many refreshes ran through the branch OID.
type countingSource struct {
	n       atomic.Int64
	repoErr error
	dir     string
}

func (s *countingSource) Repo(context.Context, string) (Repo, error) {
	if s.repoErr != nil {
		return Repo{}, s.repoErr
	}
	return Repo{Root: s.dir, GitDir: s.dir, CommonDir: s.dir}, nil
}

func (s *countingSource) Changes(context.Context, string) (Changes, error) {
	n := s.n.Add(1)
	return Changes{Branch: Branch{Ahead: int(n)}}, nil
}

// runWatcher starts w and returns its updates and a stop function that
// cancels it and waits for Run to return.
func runWatcher(t *testing.T, w *Watcher) (<-chan Update, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan Update, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, func(u Update) {
			select {
			case updates <- u:
			default:
				t.Error("update buffer full")
			}
		})
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return updates, stop
}

// next waits for an update matching cond, failing at the deadline.
func next(t *testing.T, updates <-chan Update, what string, cond func(Update) bool) Update {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case u := <-updates:
			if cond(u) {
				return u
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestWatcherSignalsAndCancellation(t *testing.T) {
	src := &countingSource{dir: t.TempDir()}
	signals := make(chan error)
	signalReturned := make(chan struct{})
	var signalCalls atomic.Int64
	w := &Watcher{
		Dir:      src.dir,
		Source:   src,
		Debounce: time.Millisecond,
		Signal: func(ctx context.Context) error {
			signalCalls.Add(1)
			select {
			case err := <-signals:
				return err
			case <-ctx.Done():
				select {
				case <-signalReturned:
				default:
					close(signalReturned)
				}
				return ctx.Err()
			}
		},
	}
	baseline := runtime.NumGoroutine()
	updates, stop := runWatcher(t, w)

	first := next(t, updates, "initial refresh", func(Update) bool { return true })
	if first.Changes.Branch.Ahead != 1 || first.Err != nil || first.At.IsZero() {
		t.Fatalf("initial update = %+v", first)
	}
	signals <- nil
	next(t, updates, "refresh after signal", func(u Update) bool { return u.Changes.Branch.Ahead >= 2 })
	// A failing signal backs off, then the loop keeps waiting and a later
	// signal still refreshes.
	signals <- errors.New("server restarting")
	signals <- nil
	next(t, updates, "refresh after recovered signal", func(u Update) bool { return u.Changes.Branch.Ahead >= 3 })

	stop()
	select {
	case <-signalReturned:
	default:
		t.Fatal("Run returned before the signal goroutine observed cancellation")
	}
	tmuxtest.WaitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
	if signalCalls.Load() < 3 {
		t.Errorf("signal called %d times, want at least 3", signalCalls.Load())
	}
}

func TestWatcherIdleFallback(t *testing.T) {
	src := &countingSource{dir: t.TempDir(), repoErr: ErrNotRepository}
	w := &Watcher{Dir: src.dir, Source: src, Idle: 5 * time.Millisecond}
	updates, _ := runWatcher(t, w)
	next(t, updates, "three idle refreshes", func(u Update) bool { return u.Changes.Branch.Ahead >= 3 })
}

// TestWatcherIdleResetsOnRefresh proves the fallback is a fallback: a refresh
// that a signal caused puts it off by a whole idle period instead of leaving
// it to fire on the schedule it had. A signal at 2/3 of the period would be
// followed 1/3 of a period later by a timer refresh if it did not.
func TestWatcherIdleResetsOnRefresh(t *testing.T) {
	const idle = 300 * time.Millisecond
	src := &countingSource{dir: t.TempDir(), repoErr: ErrNotRepository}
	signals := make(chan error)
	w := &Watcher{
		Dir: src.dir, Source: src, Idle: idle, Debounce: time.Millisecond,
		Signal: func(ctx context.Context) error {
			select {
			case err := <-signals:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	updates, _ := runWatcher(t, w)
	first := next(t, updates, "initial refresh", func(Update) bool { return true })

	time.Sleep(idle * 2 / 3)
	signals <- nil
	signaled := next(t, updates, "refresh after the signal", func(u Update) bool { return u.Changes.Branch.Ahead >= 2 })
	if gap := signaled.At.Sub(first.At); gap >= idle {
		t.Fatalf("the signal refresh came %v after the first one, at or past the idle period %v: the fallback may have caused it", gap, idle)
	}
	fallback := next(t, updates, "the idle refresh", func(u Update) bool { return u.Changes.Branch.Ahead >= 3 })
	if gap := fallback.At.Sub(signaled.At); gap < idle*4/5 {
		t.Errorf("the idle refresh came %v after the signal refresh, less than %v: the fallback was not put off by it", gap, idle*4/5)
	}
}

func TestRelevantEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{name: "write", ev: fsnotify.Event{Name: "/r/a.go", Op: fsnotify.Write}, want: true},
		{name: "index rename", ev: fsnotify.Event{Name: "/r/.git/index", Op: fsnotify.Create}, want: true},
		{name: "chmod only", ev: fsnotify.Event{Name: "/r/a.go", Op: fsnotify.Chmod}},
		{name: "write with chmod", ev: fsnotify.Event{Name: "/r/a.go", Op: fsnotify.Write | fsnotify.Chmod}, want: true},
		{name: "index lock", ev: fsnotify.Event{Name: "/r/.git/index.lock", Op: fsnotify.Create}},
		{name: "ref lock", ev: fsnotify.Event{Name: "/r/.git/refs/heads/main.lock", Op: fsnotify.Remove}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relevant(tc.ev); got != tc.want {
				t.Errorf("relevant(%v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}

func hasFile(ch Changes, path string, staged bool) bool {
	for _, f := range ch.Files {
		if f.Path == path && f.Staged() == staged {
			return true
		}
	}
	return false
}

func TestIntegrationWatcherFileEvents(t *testing.T) {
	r := newTestRepo(t)
	r.write("a.txt", "1\n")
	r.git("add", ".")
	r.git("commit", "-qm", "one")
	runner := r.runner()
	w := &Watcher{Dir: r.dir, Source: runner, Debounce: 20 * time.Millisecond}
	updates, _ := runWatcher(t, w)
	next(t, updates, "clean initial state", func(u Update) bool { return u.Err == nil && u.Changes.Clean() })

	cases := []struct {
		name   string
		act    func()
		what   string
		expect func(Update) bool
	}{
		{
			name:   "top-level edit",
			act:    func() { r.write("a.txt", "2\n") },
			what:   "unstaged a.txt",
			expect: func(u Update) bool { return hasFile(u.Changes, "a.txt", false) },
		},
		{
			name:   "staging writes the index",
			act:    func() { r.git("add", "a.txt") },
			what:   "staged a.txt",
			expect: func(u Update) bool { return hasFile(u.Changes, "a.txt", true) },
		},
		{
			name:   "commit moves the branch",
			act:    func() { r.git("commit", "-qm", "two") },
			what:   "clean after commit",
			expect: func(u Update) bool { return u.Err == nil && u.Changes.Clean() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.act()
			next(t, updates, tc.what, tc.expect)
		})
	}
}

func TestIntegrationWatcherInitInWatchedDirectory(t *testing.T) {
	requireGit(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &testRepo{t: t, dir: base, env: hermeticGitEnv(t.TempDir())}
	w := &Watcher{Dir: base, Source: r.runner(), Debounce: 20 * time.Millisecond}
	updates, _ := runWatcher(t, w)
	next(t, updates, "not a repository", func(u Update) bool { return errors.Is(u.Err, ErrNotRepository) })
	r.git("init", "-q", "-b", "main")
	r.write("new.txt", "x\n")
	next(t, updates, "untracked file after init", func(u Update) bool { return u.Err == nil && hasFile(u.Changes, "new.txt", false) })
}

func TestIntegrationWatcherTmuxSignal(t *testing.T) {
	srv := tmuxtest.Start(t)
	r := newTestRepo(t)
	r.write("sub/deep/a.txt", "1\n")
	r.git("add", ".")
	r.git("commit", "-qm", "one")
	const session = "base"
	w := &Watcher{Dir: r.dir, Source: r.runner(), Debounce: 20 * time.Millisecond, Signal: TmuxSignal(srv.Client, session)}
	updates, _ := runWatcher(t, w)
	next(t, updates, "clean initial state", func(u Update) bool { return u.Err == nil && u.Changes.Clean() })

	// A nested edit produces no watched file event; only the hook signal
	// reports it. tmux latches the signal if the waiter is not waiting yet.
	if err := os.WriteFile(filepath.Join(r.dir, "sub", "deep", "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(tmuxtest.Context(t), "wait-for", "-S", tmux.ChangesChannel(sessionID(t, srv, tmux.ExactSession(session)))); err != nil {
		t.Fatal(err)
	}
	next(t, updates, "nested edit after the tmux signal", func(u Update) bool { return hasFile(u.Changes, "sub/deep/a.txt", false) })
}
