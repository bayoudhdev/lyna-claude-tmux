// Package watch keeps the changes of a working tree current: a runner that
// executes git safely, a refresh loop, and a watcher that redraws on file
// events, on the tmux signal the edit hooks of an agent send, and on an
// interval when nothing else says anything. What it reads is parsed and
// modeled in internal/domain/vcs.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
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

// Source reads a working tree. A git.Runner implements it.
type Source interface {
	Repo(ctx context.Context, dir string) (git.Repo, error)
	Changes(ctx context.Context, dir string) (vcs.Changes, error)
}

// Update is one refresh result.
type Update struct {
	Changes vcs.Changes
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
	// Idle refreshes when nothing else has refreshed for that long, catching
	// edits no event reports (a file saved deep in the tree from a shell).
	// Every refresh puts the fallback off again, so a working tree that
	// reports its changes is never read on a timer; 0 disables it.
	Idle time.Duration
}

// TmuxSignal returns a Signal that waits on the changes channel of the
// session named session: `tmux wait-for lt-changes-<id>` returns each time a
// hook signals it, and tmux latches a signal sent while nobody waits. The
// name is resolved to the session id before every wait, so a workspace
// killed and started again under the same name is followed by its new id.
func TmuxSignal(client *tmux.Client, session string) func(ctx context.Context) error {
	return waitOn(client, tmux.ExactSession(session), tmux.ChangesChannel)
}

// ErrNoSession reports a target whose session id could not be read.
var ErrNoSession = errors.New("watch: the target belongs to no session")

// TmuxPaneSignal returns a Signal that waits on the changes channel of the
// session pane belongs to, reading the session id before every wait. The id
// does not change when the session is renamed, so a rename needs no help
// from the waiter; reading it each time only matters when the pane moves to
// another session.
func TmuxPaneSignal(client *tmux.Client, pane string) func(ctx context.Context) error {
	return waitOn(client, pane, tmux.ChangesChannel)
}

// TmuxAgentsSignal returns a Signal that waits on the agents channel of the
// session target belongs to, which the hooks notify whenever an agent of that
// workspace starts, stops, changes state or moves a task. The target is a pane
// id or a session, resolved to the session id before every wait for the
// reasons TmuxPaneSignal resolves it.
func TmuxAgentsSignal(client *tmux.Client, target string) func(ctx context.Context) error {
	return waitOn(client, target, tmux.AgentsChannel)
}

// waitOn waits on the changes channel of the session that target resolves
// to. display-message answers an empty id for a target that does not exist
// on tmux 3.7 instead of failing, so the empty answer is an error too.
func waitOn(client *tmux.Client, target string, channel func(sessionID string) string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		id, err := client.Display(ctx, target, "#{session_id}")
		if err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("%w: %s", ErrNoSession, target)
		}
		_, err = client.Run(ctx, "wait-for", channel(id))
		return err
	}
}

// Run refreshes once immediately, then after every debounced trigger, and
// returns when ctx is done. emit is called from Run's goroutine only. Every
// goroutine Run starts has exited when it returns.
func (w *Watcher) Run(ctx context.Context, emit func(Update)) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		// Without file events the signal and the idle fallback still refresh.
		fsw = nil
	} else {
		defer func() { _ = fsw.Close() }()
	}
	var repo *git.Repo
	watched := make(map[string]bool)
	loop := Loop{
		Signal:   w.Signal,
		Debounce: w.Debounce,
		Idle:     w.Idle,
		Refresh: func(ctx context.Context) {
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
		},
	}
	if fsw != nil {
		loop.Watch = func(ctx context.Context, poke func()) { forwardEvents(ctx, fsw, poke) }
	}
	loop.Run(ctx)
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
// watched: Claude's edits arrive through Signal and the rest through Idle.
func addWatches(fsw *fsnotify.Watcher, repo git.Repo, watched map[string]bool) {
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
