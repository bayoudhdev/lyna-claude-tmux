package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// GitWorkIdle is how long the workstation waits, with nothing else having
// refreshed it, before reading the repository anyway: a commit made in
// another terminal produces neither a file event this process sees nor a hook
// signal.
const GitWorkIdle = 20 * time.Second

// GitWorkPage is how many commits one page of the history holds. It is what
// the graph draws through without asking for more, and what every key that
// walks off the end asks for again.
const GitWorkPage = 400

// GitWorkPatches is where a patch written from the workstation lands, under
// the project it was written from.
var GitWorkPatches = filepath.Join(".claude", "patches")

// GitWorkRequest describes a git workstation.
type GitWorkRequest struct {
	// Dir is a directory inside the working tree the workstation is about.
	Dir string
	// Session is the workspace whose changes channel the workstation waits
	// on when the process does not run in a tmux pane.
	Session string
	// Popup lets q and esc close it.
	Popup bool
}

// GitWork is a workstation ready to run: the reader that feeds it and the
// view options, without the channel of readings the caller connects.
type GitWork struct {
	Reader  *GitReader
	Options tui.GitWorkOptions
}

// GitReader keeps one reading of a repository current: the working tree, the
// history, the refs and what git stopped in the middle of. It publishes a
// whole reading at a time, so the regions of the workstation are never drawn
// from two readings at once.
type GitReader struct {
	Runner git.Runner
	// Dir is the working tree it reads and Page how many commits one page of
	// the history holds.
	Dir  string
	Page int
	// Signal blocks until something says the repository moved; nil disables
	// that source, leaving the file events and the idle reading.
	Signal func(ctx context.Context) error
	// Idle reads when nothing else has for that long.
	Idle time.Duration

	mu sync.Mutex
	// rev is the history being read, empty for the history of HEAD, and
	// pages how many pages of it. emit is where a reading goes while the
	// reader runs.
	rev   string
	pages int
	emit  func(tui.GitState)
}

// Run publishes a reading at the start and after every change, and returns
// when ctx is done. Every goroutine it starts has exited by then.
func (r *GitReader) Run(ctx context.Context, emit func(tui.GitState)) {
	r.mu.Lock()
	r.emit = emit
	if r.pages < 1 {
		r.pages = 1
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.emit = nil
		r.mu.Unlock()
	}()
	w := &watch.Watcher{Dir: r.Dir, Source: r.Runner, Signal: r.Signal, Idle: r.idle()}
	w.Run(ctx, func(u watch.Update) { emit(r.state(ctx, u)) })
}

// ReadCommit reads one commit in full for the detail region.
func (r *GitReader) ReadCommit(ctx context.Context) func(rev string) tea.Cmd {
	return func(rev string) tea.Cmd {
		return func() tea.Msg {
			detail, err := r.Runner.Show(ctx, r.Dir, rev)
			return tui.GitReadCommit(tui.GitCommitRead{Rev: rev, Detail: detail, Err: err})
		}
	}
}

// ReadMore reads one page of history more. The page already read is read
// again with it rather than the older commits being read on their own: a
// history that moved between the two readings would otherwise be drawn as two
// halves that never stood together.
func (r *GitReader) ReadMore(ctx context.Context) func(before string) tea.Cmd {
	return func(before string) tea.Cmd {
		if before == "" {
			return nil
		}
		return func() tea.Msg {
			r.mu.Lock()
			r.pages++
			r.mu.Unlock()
			r.read(ctx)
			return nil
		}
	}
}

// Filter reads the history of one ref instead of the history of HEAD, which
// is how a branch the page on screen does not reach is reached.
func (r *GitReader) Filter(ctx context.Context) func(rev string) tea.Cmd {
	return func(rev string) tea.Cmd {
		return func() tea.Msg {
			r.mu.Lock()
			r.rev, r.pages = rev, 1
			r.mu.Unlock()
			r.read(ctx)
			return nil
		}
	}
}

// Refresh reads the repository now.
func (r *GitReader) Refresh(ctx context.Context) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg {
			r.read(ctx)
			return nil
		}
	}
}

// Poke reads the repository now, for whoever changed it and is not a command
// of the view. It waits for the reading to be published, so the frame that
// follows an operation is the one the operation left behind.
func (r *GitReader) Poke(ctx context.Context) { r.read(ctx) }

// read takes a reading of its own and publishes it.
func (r *GitReader) read(ctx context.Context) {
	changes, err := r.Runner.Changes(ctx, r.Dir)
	if ctx.Err() != nil {
		return
	}
	st := r.state(ctx, watch.Update{Changes: changes, Err: err, At: time.Now()})
	r.mu.Lock()
	emit := r.emit
	r.mu.Unlock()
	if emit != nil {
		emit(st)
	}
}

// state is one whole reading: the working tree that was just read, the page
// of history in front of it, the refs and what git stopped in the middle of.
// A part that cannot be read leaves the state carrying the first failure,
// which the command bar says, rather than a frame quietly out of date.
func (r *GitReader) state(ctx context.Context, u watch.Update) tui.GitState {
	st := tui.GitState{Changes: u.Changes, Err: u.Err, At: u.At}
	if u.Err != nil {
		return st
	}
	fail := func(err error) {
		if st.Err == nil && err != nil {
			st.Err = err
		}
	}
	rev, pages := r.where()
	revs := []string{"HEAD"}
	if rev != "" {
		revs = []string{rev}
	}
	// One commit over the page says there is a page before it, without
	// reading that page.
	room := pages * r.page()
	commits, err := r.Runner.Log(ctx, r.Dir, git.LogOptions{Revs: revs, Max: room + 1})
	fail(err)
	if len(commits) > room {
		st.Commits, st.More = commits[:room], true
	} else {
		st.Commits = commits
	}
	branches, err := r.Runner.Branches(ctx, r.Dir)
	fail(err)
	remotes, err := r.Runner.RemoteBranches(ctx, r.Dir)
	fail(err)
	worktrees, err := r.Runner.Worktrees(ctx, r.Dir)
	fail(err)
	stashes, err := r.Runner.Stashes(ctx, r.Dir)
	fail(err)
	tags, err := r.Runner.Tags(ctx, r.Dir)
	fail(err)
	st.Refs = tui.GitRefs{
		Branches: branches, Remotes: remotes, Worktrees: worktrees, Stashes: stashes, Tags: tags,
	}
	progress, err := r.Runner.InProgress(ctx, r.Dir)
	fail(err)
	st.Progress = progress
	return st
}

// where is the history being read and how many pages of it.
func (r *GitReader) where() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rev, max(r.pages, 1)
}

func (r *GitReader) page() int {
	if r.Page > 0 {
		return r.Page
	}
	return GitWorkPage
}

func (r *GitReader) idle() time.Duration {
	if r.Idle > 0 {
		return r.Idle
	}
	return GitWorkIdle
}

// OpenGitWork resolves a git workstation: the repository it is about, the
// workspace it follows, the theme it draws with and the operations its keys
// run.
func OpenGitWork(ctx context.Context, h Host, req GitWorkRequest) (GitWork, error) {
	if !filepath.IsAbs(req.Dir) {
		return GitWork{}, fmt.Errorf("git directory %q is not absolute", req.Dir)
	}
	if info, err := os.Stat(req.Dir); err != nil {
		return GitWork{}, fmt.Errorf("git directory: %w", err)
	} else if !info.IsDir() {
		return GitWork{}, fmt.Errorf("git directory %s is not a directory", sanitize.Line(req.Dir))
	}
	runner := git.Runner{Environ: func() []string { return h.Environ }}
	repo, err := runner.Repo(ctx, req.Dir)
	if err != nil {
		return GitWork{}, err
	}
	follow, err := followLive(ctx, h, req.Session)
	if err != nil {
		return GitWork{}, err
	}
	look, err := tui.ThemeFromConfig(follow.Config.UI, h.Getenv)
	if err != nil {
		return GitWork{}, err
	}

	reader := &GitReader{Runner: runner, Dir: repo.Root, Signal: follow.Signal}
	ops := GitOps{
		Runner:  runner,
		Dir:     repo.Root,
		Bin:     h.Exe,
		Remote:  git.DefaultRemote,
		Patches: filepath.Join(repo.Root, GitWorkPatches),
		Refresh: func() { reader.Poke(ctx) },
	}
	if follow.Client != nil {
		client := follow.Client
		ops.Copy = func(ctx context.Context, text string) error {
			cmds := tmux.CopyText(text)
			if len(cmds) == 0 {
				return fmt.Errorf("there is nothing to copy")
			}
			_, err := client.Batch(ctx, cmds...)
			return err
		}
	}
	return GitWork{
		Reader: reader,
		Options: tui.GitWorkOptions{
			Styles:     tui.NewStyles(look),
			ReadCommit: reader.ReadCommit(ctx),
			ReadMore:   reader.ReadMore(ctx),
			Refresh:    reader.Refresh(ctx),
			Filter:     reader.Filter(ctx),
			Run:        func(op tui.GitOp, st tui.GitState) tea.Cmd { return ops.Run(ctx, op, st) },
			Root:       repo.Root,
			Home:       h.Home,
			Popup:      req.Popup,
		},
	}, nil
}
