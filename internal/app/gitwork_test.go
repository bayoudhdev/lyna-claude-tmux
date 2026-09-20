package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/gittest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// readerOn is a reader of a repository of commits in a line, with a page
// small enough that a test can walk off the end of it.
func readerOn(t *testing.T, page int, subjects ...string) (*GitReader, *gittest.Repo) {
	t.Helper()
	r := gittest.New(t)
	for _, s := range subjects {
		r.Commit(s, filepath.Join("f", s+".txt"), s+"\n")
	}
	return &GitReader{Runner: git.Runner{Environ: r.Environ()}, Dir: r.Dir, Page: page}, r
}

// reading is one whole reading taken now, the way the workstation gets one.
func reading(t *testing.T, r *GitReader) tui.GitState {
	t.Helper()
	got := make(chan tui.GitState, 1)
	r.mu.Lock()
	r.emit = func(st tui.GitState) { got <- st }
	if r.pages < 1 {
		r.pages = 1
	}
	r.mu.Unlock()
	r.read(t.Context())
	select {
	case st := <-got:
		return st
	default:
		t.Fatal("the reader published nothing")
		return tui.GitState{}
	}
}

// TestGitReaderReadsTheWholeRepository holds one reading to everything the
// regions draw, so no region is ever drawn from a reading another one has
// moved past.
func TestGitReaderReadsTheWholeRepository(t *testing.T) {
	r, repo := readerOn(t, 0, "one", "two")
	repo.Git("branch", "side")
	repo.Git("tag", "v1.0.0")
	repo.Write("dirty.txt", "not committed\n")
	repo.Git("stash", "--include-untracked")
	repo.Write("now.txt", "in the working tree\n")

	st := reading(t, r)
	if st.Err != nil {
		t.Fatalf("the reading failed: %v", st.Err)
	}
	if len(st.Commits) != 2 || st.Commits[0].Subject != "two" {
		t.Fatalf("the history is %+v, want the two commits newest first", st.Commits)
	}
	if st.More {
		t.Fatal("it says there is a page before the oldest commit, and there is not")
	}
	names := []string{}
	for _, b := range st.Refs.Branches {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != "main,side" {
		t.Fatalf("the branches are %v", names)
	}
	if len(st.Refs.Tags) != 1 || st.Refs.Tags[0].Name != "v1.0.0" {
		t.Fatalf("the tags are %+v", st.Refs.Tags)
	}
	if len(st.Refs.Stashes) != 1 {
		t.Fatalf("the stashes are %+v", st.Refs.Stashes)
	}
	if len(st.Refs.Worktrees) != 1 {
		t.Fatalf("the worktrees are %+v", st.Refs.Worktrees)
	}
	if st.Changes.Head.Name != "main" || len(st.Changes.Files) != 1 {
		t.Fatalf("the working tree is %+v", st.Changes)
	}
	if st.Progress.Running() {
		t.Fatalf("it says git stopped in the middle of %v", st.Progress.Kind)
	}
	if st.At.IsZero() {
		t.Fatal("the reading is not dated")
	}
}

// TestGitReaderReadsAPageAtATime says there is more before it reads it, and
// reads it when asked.
func TestGitReaderReadsAPageAtATime(t *testing.T) {
	r, _ := readerOn(t, 2, "one", "two", "three", "four", "five")
	st := reading(t, r)
	if len(st.Commits) != 2 || !st.More {
		t.Fatalf("the first page holds %d commits, more=%v, want two and a page before them", len(st.Commits), st.More)
	}
	if cmd := r.ReadMore(t.Context())(st.Commits[len(st.Commits)-1].OID); cmd == nil {
		t.Fatal("it would not read the page before the oldest commit")
	} else {
		cmd()
	}
	if st = reading(t, r); len(st.Commits) != 4 || !st.More {
		t.Fatalf("the second page holds %d commits, more=%v, want four and a page before them", len(st.Commits), st.More)
	}
	r.ReadMore(t.Context())(st.Commits[len(st.Commits)-1].OID)()
	if st = reading(t, r); len(st.Commits) != 5 || st.More {
		t.Fatalf("the last page holds %d commits, more=%v, want all five and no page before them", len(st.Commits), st.More)
	}
	// A page before nothing is no reading at all.
	if cmd := r.ReadMore(t.Context())(""); cmd != nil {
		t.Fatal("it read a page before no commit")
	}
}

// TestGitReaderFiltersToARef reads the history of a branch the page on screen
// does not reach.
func TestGitReaderFiltersToARef(t *testing.T) {
	r, repo := readerOn(t, 0, "one")
	repo.Git("checkout", "-q", "-b", "side")
	repo.Commit("only on the side", "side.txt", "side\n")
	repo.Git("checkout", "-q", "main")

	if st := reading(t, r); len(st.Commits) != 1 {
		t.Fatalf("the history of HEAD is %+v, want the one commit of main", st.Commits)
	}
	r.Filter(t.Context())("refs/heads/side")()
	st := reading(t, r)
	if len(st.Commits) != 2 || st.Commits[0].Subject != "only on the side" {
		t.Fatalf("the history read is %+v, want the one of the branch that was asked for", st.Commits)
	}
	// The working tree is still the one of the repository, whatever history
	// is being read.
	if st.Changes.Head.Name != "main" {
		t.Fatalf("the working tree says %q", st.Changes.Head.Name)
	}
}

// TestGitReaderReadsOneCommitInFull is what the detail region draws.
func TestGitReaderReadsOneCommitInFull(t *testing.T) {
	r, _ := readerOn(t, 0, "one", "two")
	st := reading(t, r)
	rev := st.Commits[0].OID
	msg := r.ReadCommit(t.Context())(rev)()
	read, ok := tui.GitCommitReadOf(msg)
	if !ok {
		t.Fatalf("it answered with %T", msg)
	}
	if read.Rev != rev || read.Err != nil || read.Detail.Commit.Subject != "two" {
		t.Fatalf("it read %+v", read)
	}
	// A commit that is not there is a failure the detail region says, not a
	// reading nobody can tell from an empty one.
	msg = r.ReadCommit(t.Context())("nope")()
	if read, _ = tui.GitCommitReadOf(msg); read.Err == nil {
		t.Fatalf("a commit that is not there read as %+v", read)
	}
}

// TestGitReaderReportsWhatItCouldNotRead leaves the failure on the state
// rather than publishing a frame that is quietly out of date, whichever of
// the readings it is that failed.
func TestGitReaderReportsWhatItCouldNotRead(t *testing.T) {
	t.Run("a directory that is no repository", func(t *testing.T) {
		r := &GitReader{Runner: git.Runner{}, Dir: t.TempDir()}
		st := reading(t, r)
		if st.Err == nil {
			t.Fatalf("a directory that is no repository read as %+v", st)
		}
		if len(st.Commits) != 0 {
			t.Fatalf("it read %d commits out of nothing", len(st.Commits))
		}
	})
	// The working tree reads, and one of the readings behind it does not.
	cases := []string{"log", "for-each-ref", "worktree", "stash"}
	for _, sub := range cases {
		t.Run("a repository whose "+sub+" cannot be read", func(t *testing.T) {
			exe := git.ExecutorFunc(func(_ context.Context, _ string, args, _ []string, _ int64) (git.Result, error) {
				if args[5] == sub {
					return git.Result{ExitCode: 128, Stderr: []byte("fatal: " + sub + " is beyond it")}, nil
				}
				return git.Result{}, nil
			})
			r := &GitReader{Runner: git.Runner{Executor: exe}, Dir: t.TempDir()}
			st := reading(t, r)
			if st.Err == nil {
				t.Fatalf("a reading that failed came back as %+v", st)
			}
			if !strings.Contains(st.Err.Error(), sub) {
				t.Fatalf("the failure is %q, want the one of %s", st.Err, sub)
			}
		})
	}
}

// TestGitReaderReadsWhatStopped is what the banner of the workstation offers
// to carry on, skip or put back.
func TestGitReaderReadsWhatStopped(t *testing.T) {
	r, repo := readerOn(t, 0, "one")
	repo.Git("checkout", "-q", "-b", "side")
	repo.Commit("theirs", "clash.txt", "theirs\n")
	repo.Git("checkout", "-q", "main")
	repo.Commit("ours", "clash.txt", "ours\n")
	if _, err := repo.Try("rebase", "side"); err == nil {
		t.Fatal("the rebase went through, want it stopped on the clash")
	}
	st := reading(t, r)
	if !st.Progress.Running() || st.Progress.Kind != vcs.OperationRebase {
		t.Fatalf("it says git stopped in the middle of %+v", st.Progress)
	}
}

// TestGitReaderRunPublishesUntilItIsDone reads once at the start and stops
// with the context, leaving no goroutine behind.
func TestGitReaderRunPublishesUntilItIsDone(t *testing.T) {
	r, _ := readerOn(t, 0, "one")
	r.Idle = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	states := make(chan tui.GitState, 4)
	var wg sync.WaitGroup
	wg.Go(func() {
		r.Run(ctx, func(st tui.GitState) {
			select {
			case states <- st:
			case <-ctx.Done():
			}
		})
	})
	select {
	case st := <-states:
		if len(st.Commits) != 1 {
			t.Fatalf("the first reading is %+v", st.Commits)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reader published nothing")
	}
	cancel()
	wg.Wait()
	// With nobody to publish to, a reading of its own is dropped rather than
	// blocking whoever asked for it.
	r.Poke(t.Context())
}

// TestOpenGitWork resolves the workstation of a pane, and says why it cannot
// when it cannot.
func TestOpenGitWork(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	startWorkspace(t, s, "api")
	ctx := tmuxtest.Context(t)
	pane, err := s.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	inTmux := SocketPath(h.Getenv, s.SocketName) + ",1,0"
	repo := gittest.New(t)
	repo.Commit("one", "a.txt", "a\n")
	inside := filepath.Join(repo.Dir, "f")
	repo.Write("f/deep.txt", "deep\n")

	cases := []struct {
		name   string
		env    map[string]string
		req    GitWorkRequest
		errHas string
		check  func(t *testing.T, w GitWork)
	}{
		{
			name: "a pane of a workspace",
			env:  map[string]string{"TMUX": inTmux, "TMUX_PANE": pane},
			req:  GitWorkRequest{Dir: repo.Dir},
			check: func(t *testing.T, w GitWork) {
				o := w.Options
				for name, fn := range map[string]any{
					"ReadCommit": o.ReadCommit, "ReadMore": o.ReadMore,
					"Refresh": o.Refresh, "Filter": o.Filter, "Run": o.Run,
				} {
					if fn == nil {
						t.Fatalf("%s is nil, so the key that uses it does nothing", name)
					}
				}
				if o.Root != repo.Dir {
					t.Fatalf("the workstation is about %q, want %q", o.Root, repo.Dir)
				}
				if o.Home != h.Home {
					t.Fatalf("paths are shortened against %q", o.Home)
				}
				if w.Reader == nil || w.Reader.Dir != repo.Dir || w.Reader.Signal == nil {
					t.Fatalf("the reader is %+v, want one on the repository following the workspace", w.Reader)
				}
			},
		},
		{
			name: "a directory inside the working tree is the working tree",
			env:  map[string]string{"TMUX": inTmux, "TMUX_PANE": pane},
			req:  GitWorkRequest{Dir: inside},
			check: func(t *testing.T, w GitWork) {
				if w.Options.Root != repo.Dir || w.Reader.Dir != repo.Dir {
					t.Fatalf("the workstation is about %q and reads %q, want the top of the working tree %q",
						w.Options.Root, w.Reader.Dir, repo.Dir)
				}
				// An operation applies to a path of the status, which is
				// relative to the top of the working tree and to nowhere
				// else.
				repo.Write("at-the-top.txt", "top\n")
				op := tui.GitOp{Kind: tui.OpStage, On: tui.OnFile, Path: "at-the-top.txt", Index: -1}
				cmd := w.Options.Run(op, tui.GitState{})
				if cmd == nil {
					t.Fatal("the operation answered with nothing at all")
				}
				note, ok := cmd().(tui.GitWorkNoteMsg)
				if !ok || note.Text != "staged at-the-top.txt" {
					t.Fatalf("the bar says %+v", note)
				}
			},
		},
		{
			name:   "a directory that is not absolute",
			env:    map[string]string{"TMUX": inTmux, "TMUX_PANE": pane},
			req:    GitWorkRequest{Dir: "src"},
			errHas: "is not absolute",
		},
		{
			name:   "a directory that is not there",
			env:    map[string]string{"TMUX": inTmux, "TMUX_PANE": pane},
			req:    GitWorkRequest{Dir: filepath.Join(repo.Dir, "nowhere")},
			errHas: "git directory:",
		},
		{
			name:   "a directory that is no repository",
			env:    map[string]string{"TMUX": inTmux, "TMUX_PANE": pane},
			req:    GitWorkRequest{Dir: h.Home},
			errHas: "repository",
		},
		{
			name:   "outside tmux with no workspace named",
			env:    map[string]string{},
			req:    GitWorkRequest{Dir: repo.Dir},
			errHas: "outside tmux",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := h.Host
			env := map[string]string{}
			for k, v := range h.env {
				env[k] = v
			}
			delete(env, "TMUX")
			for k, v := range tc.env {
				env[k] = v
			}
			host.Getenv = func(k string) string { return env[k] }
			got, err := OpenGitWork(tmuxtest.Context(t), host, tc.req)
			if tc.errHas != "" {
				if err == nil {
					t.Fatalf("it opened a workstation, want the failure carrying %q", tc.errHas)
				}
				if !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("the failure is %q, want it to carry %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenGitWork() error = %v", err)
			}
			tc.check(t, got)
		})
	}
}
