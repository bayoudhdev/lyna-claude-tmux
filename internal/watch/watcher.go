package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Watcher defaults.
const (
	// DefaultDebounce coalesces the burst of events one save or one git
	// command produces (lock file, rename, index write) into one refresh.
	DefaultDebounce = 150 * time.Millisecond
	// maxWatchedDirs bounds the ref directories watched; each costs a file
	// descriptor or an inotify watch.
	maxWatchedDirs = 256
	// Backoff bounds for a failing signal source (for example a tmux server
	// that is restarting).
	signalBackoffMin = 250 * time.Millisecond
	signalBackoffMax = 5 * time.Second
)

// Source reads a working tree. Runner implements it.
type Source interface {
	Repo(ctx context.Context, dir string) (Repo, error)
	Changes(ctx context.Context, dir string) (Changes, error)
}

// Update is one refresh result.
type Update struct {
	Changes Changes
	// Err is set when the refresh failed; Changes is then empty.
	Err error
	At  time.Time
}

// Watcher keeps the changes of one working tree current.
type Watcher struct {
	// Dir is any directory inside the working tree.
	Dir    string
	Source Source
	// Signal blocks until an external change notification arrives (nil
	// return) or fails. It is called in a loop; nil disables the source.
	// TmuxSignal and TmuxPaneSignal build the one Claude's PostToolUse
	// hook drives.
	Signal func(ctx context.Context) error
	// Debounce is the quiet period after an event before refreshing;
	// 0 means DefaultDebounce.
	Debounce time.Duration
	// Interval refreshes periodically, catching edits no event reports (a
	// file saved deep in the tree from a shell); 0 disables it.
	Interval time.Duration
}

// TmuxSignal returns a Signal that waits on the session's changes channel:
// `tmux wait-for lt-changes-<session>` returns each time a hook signals it,
// and tmux latches a signal sent while nobody waits.
func TmuxSignal(client *tmux.Client, session string) func(ctx context.Context) error {
	channel := tmux.ChangesChannel(session)
	return func(ctx context.Context) error {
		_, err := client.Run(ctx, "wait-for", channel)
		return err
	}
}

// ErrNoSession reports a pane whose session name could not be read.
var ErrNoSession = errors.New("watch: the pane belongs to no session")

// TmuxPaneSignal returns a Signal that waits on the changes channel of the
// session pane belongs to, reading the session name before every wait. A
// renamed session signals its old channel as part of the rename, so the wait
// in progress returns and the next one uses the new name; a name kept from an
// earlier wait would miss every later signal.
func TmuxPaneSignal(client *tmux.Client, pane string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		session, err := client.Display(ctx, pane, "#{session_name}")
		if err != nil {
			return err
		}
		if session == "" {
			return fmt.Errorf("%w: %s", ErrNoSession, pane)
		}
		_, err = client.Run(ctx, "wait-for", tmux.ChangesChannel(session))
		return err
	}
}

// Run refreshes once immediately, then after every debounced trigger, and
// returns when ctx is done. emit is called from Run's goroutine only. Every
// goroutine Run starts has exited when it returns.
func (w *Watcher) Run(ctx context.Context, emit func(Update)) {
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		// Without file events the signal and the interval still refresh.
		fsw = nil
	} else {
		defer func() { _ = fsw.Close() }()
		wg.Go(func() { forwardEvents(ctx, fsw, poke) })
	}
	if w.Signal != nil {
		wg.Go(func() { w.signalLoop(ctx, poke) })
	}

	debounce := w.Debounce
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	var tick <-chan time.Time
	if w.Interval > 0 {
		ticker := time.NewTicker(w.Interval)
		defer ticker.Stop()
		tick = ticker.C
	}

	var repo *Repo
	watched := make(map[string]bool)
	refresh := func() {
		if fsw != nil {
			if repo == nil {
				if r, err := w.Source.Repo(ctx, w.Dir); err == nil {
					repo = &r
				} else {
					// Watch the directory itself so `git init` or a clone
					// into it is noticed.
					_ = fsw.Add(w.Dir)
				}
			}
			if repo != nil {
				// Re-walked each time: a new branch can create a ref
				// directory.
				addWatches(fsw, *repo, watched)
			}
		}
		ch, err := w.Source.Changes(ctx, w.Dir)
		if ctx.Err() != nil {
			return
		}
		emit(Update{Changes: ch, Err: err, At: time.Now()})
	}

	refresh()
	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			if timer == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			}
		case <-fire:
			timer, fire = nil, nil
			refresh()
		case <-tick:
			refresh()
		}
	}
}

func (w *Watcher) signalLoop(ctx context.Context, poke func()) {
	backoff := signalBackoffMin
	for {
		err := w.Signal(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = signalBackoffMin
			poke()
			continue
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, signalBackoffMax)
	}
}

func forwardEvents(ctx context.Context, fsw *fsnotify.Watcher, poke func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			if relevant(ev) {
				poke()
			}
		case _, ok := <-fsw.Errors:
			if !ok {
				return
			}
			// An overflow loses events; refreshing recovers the state.
			poke()
		}
	}
}

// relevant filters events that cannot change what the pane shows: attribute
// changes and git lock files (the rename that replaces a lock file is itself
// an event).
func relevant(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	return !strings.HasSuffix(ev.Name, ".lock")
}

// addWatches watches the worktree root (top-level edits), the git directory
// (index, HEAD), the common directory (packed-refs) and the local and remote
// ref directories (commits, fetches). Nested working tree directories are not
// watched: Claude's edits arrive through Signal and the rest through Interval.
func addWatches(fsw *fsnotify.Watcher, repo Repo, watched map[string]bool) {
	add := func(dir string) {
		if watched[dir] || len(watched) >= maxWatchedDirs {
			return
		}
		if err := fsw.Add(dir); err == nil {
			watched[dir] = true
		}
	}
	add(repo.Root)
	add(repo.GitDir)
	add(repo.CommonDir)
	for _, refs := range []string{"refs/heads", "refs/remotes"} {
		root := filepath.Join(repo.CommonDir, refs)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// A missing or unreadable refs directory only means
				// nothing to watch there.
				return filepath.SkipDir
			}
			if d.IsDir() {
				if len(watched) >= maxWatchedDirs {
					return filepath.SkipAll
				}
				add(path)
			}
			return nil
		})
	}
}
