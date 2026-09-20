package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// fakeGit answers by subcommand and records every call. The runner runs its
// commands concurrently, so recording is locked.
type fakeGit struct {
	outputs map[string]Result
	err     error
	block   bool

	mu     sync.Mutex
	calls  [][]string
	limits []int64
}

// nul joins records the way git -z terminates them.
func nul(records ...string) []byte {
	if len(records) == 0 {
		return nil
	}
	return []byte(strings.Join(records, "\x00") + "\x00")
}

const (
	hashA = "422c2b7ab3b3c668038da977e4e93a5fc623169c"
	zero  = "0000000000000000000000000000000000000000"
)

func (f *fakeGit) key(args []string) string {
	// argv is --no-pager -c core.fsmonitor=false -C <dir> <subcommand> ...
	sub := args[5]
	if sub == "diff" && slices.Contains(args, "--cached") {
		return "diff --cached"
	}
	return sub
}

func (f *fakeGit) Exec(ctx context.Context, _ string, args, _ []string, limit int64) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.limits = append(f.limits, limit)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	if f.err != nil {
		return Result{}, f.err
	}
	return f.outputs[f.key(args)], nil
}

func TestRunnerChangesFake(t *testing.T) {
	okOutputs := map[string]Result{
		"status":        {Stdout: nul("# branch.oid "+hashA, "# branch.head main", "1 MM N... 100644 100644 100644 "+hashA+" "+hashA+" a.go")},
		"diff":          {Stdout: nul("3\t1\ta.go")},
		"diff --cached": {Stdout: nul("1\t0\ta.go")},
	}
	notRepo := map[string]Result{
		"status":        {ExitCode: 128, Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git\n")},
		"diff":          {ExitCode: 128, Stderr: []byte("fatal: not a git repository\n")},
		"diff --cached": {ExitCode: 128, Stderr: []byte("fatal: not a git repository\n")},
	}
	cases := []struct {
		name    string
		outputs map[string]Result
		err     error
		block   bool
		timeout time.Duration
		want    vcs.Changes
		wantErr error
		errText string
	}{
		{
			name:    "joined result",
			outputs: okOutputs,
			want: vcs.Changes{
				Head:    vcs.Head{OID: hashA, Name: "main"},
				Files:   []vcs.File{{Kind: vcs.KindChanged, Path: "a.go", Index: 'M', Worktree: 'M', Added: 4, Deleted: 1}},
				Added:   4,
				Deleted: 1,
			},
		},
		{name: "not a repository", outputs: notRepo, wantErr: ErrNotRepository},
		{
			name: "diff failure alone",
			outputs: map[string]Result{
				"status":        okOutputs["status"],
				"diff":          {ExitCode: 129, Stderr: []byte("usage: git diff")},
				"diff --cached": okOutputs["diff --cached"],
			},
			errText: "exit status 129: usage: git diff",
		},
		{name: "git missing", err: ErrNotInstalled, wantErr: ErrNotInstalled},
		{name: "timeout", block: true, timeout: time.Millisecond, wantErr: context.DeadlineExceeded},
		{name: "output cap", err: ErrOutputTooLarge, wantErr: ErrOutputTooLarge},
		{
			name: "malformed status",
			outputs: map[string]Result{
				"status": {Stdout: nul("9 nonsense")}, "diff": okOutputs["diff"], "diff --cached": okOutputs["diff --cached"],
			},
			wantErr: vcs.ErrMalformed,
		},
		{
			name: "malformed unstaged numstat",
			outputs: map[string]Result{
				"status": okOutputs["status"], "diff": {Stdout: nul("x")}, "diff --cached": okOutputs["diff --cached"],
			},
			wantErr: vcs.ErrMalformed,
		},
		{
			name: "malformed staged numstat",
			outputs: map[string]Result{
				"status": okOutputs["status"], "diff": okOutputs["diff"], "diff --cached": {Stdout: nul("x")},
			},
			wantErr: vcs.ErrMalformed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeGit{outputs: tc.outputs, err: tc.err, block: tc.block}
			r := Runner{Executor: fake, Timeout: tc.timeout}
			got, err := r.Changes(context.Background(), "/repo")
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			case tc.errText != "":
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("error = %v, want text %q", err, tc.errText)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Changes = %+v, want %+v", got, tc.want)
			}
			if len(fake.calls) != 3 {
				t.Fatalf("%d git calls, want 3", len(fake.calls))
			}
			for i, args := range fake.calls {
				if want := []string{"--no-pager", "-c", "core.fsmonitor=false", "-C", "/repo"}; !slices.Equal(args[:5], want) {
					t.Errorf("call %d argv prefix = %q, want %q", i, args[:5], want)
				}
				if fake.limits[i] != DefaultMaxOutput {
					t.Errorf("call %d limit = %d, want %d", i, fake.limits[i], DefaultMaxOutput)
				}
			}
		})
	}
}

func TestRunnerEnvironment(t *testing.T) {
	cases := []struct {
		name    string
		base    []string
		present []string
		absent  []string
	}{
		{
			name:    "redirecting variables are removed and safety variables forced",
			base:    []string{"HOME=/h", "GIT_DIR=/elsewhere/.git", "GIT_WORK_TREE=/elsewhere", "GIT_INDEX_FILE=/x", "GIT_OPTIONAL_LOCKS=1", "GIT_PAGER=less", "PAGER=less", "LC_ALL=fr_FR.UTF-8", "GIT_TERMINAL_PROMPT=1"},
			present: []string{"HOME=/h", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"},
			absent:  []string{"GIT_DIR=/elsewhere/.git", "GIT_WORK_TREE=/elsewhere", "GIT_INDEX_FILE=/x", "GIT_OPTIONAL_LOCKS=1", "GIT_PAGER=less", "PAGER=less", "LC_ALL=fr_FR.UTF-8", "GIT_TERMINAL_PROMPT=1"},
		},
		{
			name:    "unrelated variables are kept",
			base:    []string{"PATH=/bin", "GIT_AUTHOR_NAME=me"},
			present: []string{"PATH=/bin", "GIT_AUTHOR_NAME=me", "GIT_OPTIONAL_LOCKS=0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Runner{Environ: func() []string { return tc.base }}.environ()
			for _, kv := range tc.present {
				if !slices.Contains(env, kv) {
					t.Errorf("env lacks %q: %q", kv, env)
				}
			}
			for _, kv := range tc.absent {
				if slices.Contains(env, kv) {
					t.Errorf("env keeps %q", kv)
				}
			}
		})
	}
	if env := (Runner{}).environ(); !slices.Contains(env, "GIT_OPTIONAL_LOCKS=0") {
		t.Errorf("default environment lacks GIT_OPTIONAL_LOCKS=0")
	}
}

func TestRunnerRepoFake(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		out     string
		code    int
		want    Repo
		wantErr error
	}{
		{name: "three directories", out: dir + "\n" + dir + "/.\n" + dir + "\n", want: Repo{Root: dir, GitDir: dir + "/.", CommonDir: dir}},
		{name: "two lines", out: dir + "\n" + dir + "\n", wantErr: vcs.ErrMalformed},
		{name: "relative line", out: dir + "\n.git\n" + dir + "\n", wantErr: vcs.ErrMalformed},
		{name: "missing directory", out: dir + "\n" + dir + "/nope\n" + dir + "\n", wantErr: vcs.ErrMalformed},
		{name: "not a repository", code: 128, wantErr: ErrNotRepository},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe := ExecutorFunc(func(_ context.Context, _ string, args, _ []string, _ int64) (Result, error) {
				if args[5] != "rev-parse" {
					t.Errorf("subcommand = %q", args[5])
				}
				if tc.code != 0 {
					return Result{ExitCode: tc.code, Stderr: []byte("fatal: not a git repository")}, nil
				}
				return Result{Stdout: []byte(tc.out)}, nil
			})
			got, err := Runner{Executor: exe}.Repo(context.Background(), dir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Repo = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOSExecutor(t *testing.T) {
	yes, err := exec.LookPath("yes")
	if err != nil {
		t.Skip("yes not installed")
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false not installed")
	}
	cases := []struct {
		name     string
		bin      string
		args     []string
		limit    int64
		ctx      func() context.Context
		wantErr  error
		wantCode int
		wantOut  string
	}{
		{name: "endless output is capped and stopped", bin: yes, limit: 64, wantErr: ErrOutputTooLarge, wantOut: strings.Repeat("y\n", 32)},
		{name: "exit code is reported", bin: falseBin, limit: 64, wantCode: 1},
		{name: "missing binary", bin: filepath.Join(t.TempDir(), "git"), limit: 64, wantErr: ErrNotInstalled},
		{
			name: "canceled context", bin: yes, limit: 1 << 40,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: context.Canceled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx()
			}
			res, err := osExecutor{}.Exec(ctx, tc.bin, tc.args, os.Environ(), tc.limit)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != tc.wantCode {
				t.Errorf("exit code = %d, want %d", res.ExitCode, tc.wantCode)
			}
			if tc.wantOut != "" && string(res.Stdout) != tc.wantOut {
				t.Errorf("stdout = %q, want %q", res.Stdout, tc.wantOut)
			}
		})
	}
}

func TestCappedWriter(t *testing.T) {
	cases := []struct {
		name     string
		limit    int64
		truncate bool
		writes   []string
		wantBuf  string
		wantOver bool
		wantErr  bool
	}{
		{name: "under limit", limit: 10, writes: []string{"abc", "def"}, wantBuf: "abcdef"},
		{name: "exact limit", limit: 6, writes: []string{"abc", "def"}, wantBuf: "abcdef"},
		{name: "over limit fails", limit: 4, writes: []string{"abc", "def"}, wantBuf: "abcd", wantOver: true, wantErr: true},
		{name: "over limit truncates", limit: 4, truncate: true, writes: []string{"abc", "def", "ghi"}, wantBuf: "abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &capped{limit: tc.limit, truncate: tc.truncate}
			var lastErr error
			for _, w := range tc.writes {
				n, err := c.Write([]byte(w))
				if err == nil && n != len(w) {
					t.Errorf("short write %d of %d without error", n, len(w))
				}
				if err != nil {
					lastErr = err
				}
			}
			if c.buf.String() != tc.wantBuf || c.over != tc.wantOver || (lastErr != nil) != tc.wantErr {
				t.Errorf("buf %q over %v err %v; want %q %v %v", c.buf.String(), c.over, lastErr, tc.wantBuf, tc.wantOver, tc.wantErr)
			}
		})
	}
}

// TestRunnerLogArgv holds the command a history reading builds, and proves a
// revision or a path that could be read as an option never reaches git: the
// reading is refused before a process is started.
func TestRunnerLogArgv(t *testing.T) {
	const format = "--format=" + vcs.LogFormat
	cases := []struct {
		name    string
		opt     LogOptions
		want    []string
		wantErr bool
	}{
		{
			name: "every ref of the repository",
			want: []string{"log", "--decorate=full", "-z", format, "--topo-order", "--max-count=4096", "--all", "--"},
		},
		{
			name: "a page of it",
			opt:  LogOptions{Max: 20, Skip: 40},
			want: []string{"log", "--decorate=full", "-z", format, "--topo-order", "--max-count=20", "--skip=40", "--all", "--"},
		},
		{
			name: "more than the domain reads",
			opt:  LogOptions{Max: vcs.MaxCommits + 1},
			want: []string{"log", "--decorate=full", "-z", format, "--topo-order", "--max-count=4096", "--all", "--"},
		},
		{
			name: "branches and paths, each on its own side of the separator",
			opt:  LogOptions{Max: 5, Revs: []string{"main", "side"}, Paths: []string{"internal/git", "a.txt"}, FirstParent: true},
			want: []string{
				"log", "--decorate=full", "-z", format, "--topo-order", "--max-count=5",
				"--first-parent", "main", "side", "--", "internal/git", "a.txt",
			},
		},
		{name: "a revision git would read as an option", opt: LogOptions{Revs: []string{"--output=/tmp/x"}}, wantErr: true},
		{name: "a revision that is empty", opt: LogOptions{Revs: []string{""}}, wantErr: true},
		{name: "a path leaving the repository", opt: LogOptions{Paths: []string{"../etc/passwd"}}, wantErr: true},
		{name: "an absolute path", opt: LogOptions{Paths: []string{"/etc/passwd"}}, wantErr: true},
		{name: "a page counting backwards", opt: LogOptions{Skip: -1}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{"log": {}}}
			got, err := Runner{Executor: f}.Log(context.Background(), "/repo", tc.opt)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Log() = %+v, want the reading refused", got)
				}
				if len(f.calls) != 0 {
					t.Fatalf("Log() ran %v, want nothing run at all", f.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Log() error = %v", err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("Log() ran %d commands, want one", len(f.calls))
			}
			if args := f.calls[0][5:]; !slices.Equal(args, tc.want) {
				t.Fatalf("Log() ran %v, want %v", args, tc.want)
			}
		})
	}
}

func TestRunnerStashesArgv(t *testing.T) {
	f := &fakeGit{outputs: map[string]Result{"stash": {}}}
	got, err := Runner{Executor: f}.Stashes(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("Stashes() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Stashes() = %+v, want none from an empty list", got)
	}
	want := []string{"stash", "list", "-z", "--format=" + vcs.StashFormat}
	if args := f.calls[0][5:]; !slices.Equal(args, want) {
		t.Fatalf("Stashes() ran %v, want %v", args, want)
	}
}

// TestRunnerStagingArgv holds the commands the working tree actions build,
// and proves a path that leaves the project never reaches git: the refusal
// comes before a process is started.
func TestRunnerStagingArgv(t *testing.T) {
	cases := []struct {
		name    string
		act     func(r Runner) error
		want    []string
		wantErr bool
	}{
		{
			name: "one file staged",
			act:  func(r Runner) error { return r.Stage(context.Background(), "/repo", "a.txt", "sub/b.txt") },
			want: []string{"add", "-A", "--", "a.txt", "sub/b.txt"},
		},
		{
			name: "every change staged",
			act:  func(r Runner) error { return r.StageAll(context.Background(), "/repo") },
			want: []string{"add", "-A", "--", ":/"},
		},
		{
			name: "one file taken out of the index",
			act:  func(r Runner) error { return r.Unstage(context.Background(), "/repo", "a.txt") },
			want: []string{"reset", "-q", "--", "a.txt"},
		},
		{
			name: "the index emptied",
			act:  func(r Runner) error { return r.UnstageAll(context.Background(), "/repo") },
			want: []string{"reset", "-q"},
		},
		{
			name: "one change thrown away",
			act:  func(r Runner) error { return r.Discard(context.Background(), "/repo", "a.txt") },
			want: []string{"restore", "--worktree", "--", "a.txt"},
		},
		{
			name: "every change thrown away",
			act:  func(r Runner) error { return r.DiscardAll(context.Background(), "/repo") },
			want: []string{"restore", "--worktree", "--", ":/"},
		},
		{
			name: "one untracked file deleted",
			act:  func(r Runner) error { return r.CleanUntracked(context.Background(), "/repo", "new/") },
			want: []string{"clean", "-f", "-d", "-q", "--", "new/"},
		},
		{
			name: "every untracked file deleted, the ignored ones kept",
			act:  func(r Runner) error { return r.CleanAllUntracked(context.Background(), "/repo") },
			want: []string{"clean", "-f", "-d", "-q", "--", ":/"},
		},
		{name: "staging nothing", act: func(r Runner) error { return r.Stage(context.Background(), "/repo") }, wantErr: true},
		{
			name:    "a path leaving the project",
			act:     func(r Runner) error { return r.Stage(context.Background(), "/repo", "../outside.txt") },
			wantErr: true,
		},
		{
			name:    "a path leaving it halfway",
			act:     func(r Runner) error { return r.Discard(context.Background(), "/repo", "sub/../../outside") },
			wantErr: true,
		},
		{
			name:    "an absolute path",
			act:     func(r Runner) error { return r.CleanUntracked(context.Background(), "/repo", "/etc/passwd") },
			wantErr: true,
		},
		{
			name:    "one good path beside one that leaves",
			act:     func(r Runner) error { return r.Stage(context.Background(), "/repo", "a.txt", "../outside.txt") },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{}}
			err := tc.act(Runner{Executor: f})
			if tc.wantErr {
				if err == nil {
					t.Fatal("the command went through, want it refused")
				}
				if len(f.calls) != 0 {
					t.Fatalf("the command ran %v, want nothing run at all", f.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("the command failed: %v", err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("the command ran %d times, want once", len(f.calls))
			}
			if args := f.calls[0][5:]; !slices.Equal(args, tc.want) {
				t.Fatalf("the command ran %v, want %v", args, tc.want)
			}
		})
	}
}

// TestRunnerWorktreeArgv holds the commands a worktree action builds, and
// proves a name or a branch that would be read as something else never
// reaches the worktree command.
func TestRunnerWorktreeArgv(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		act     func(r Runner) error
		want    []string
		wantErr bool
	}{
		{
			name: "a worktree on a branch of its own",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "task-a"})
				return err
			},
			want: []string{"worktree", "add", "-b", "task-a", "--", filepath.Join(root, ".claude", "worktrees", "task-a")},
		},
		{
			name: "a branch starting where another one stands",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "b", Branch: "feat/b", Start: "main"})
				return err
			},
			want: []string{"worktree", "add", "-b", "feat/b", "--", filepath.Join(root, ".claude", "worktrees", "b"), "main"},
		},
		{
			name: "a branch that already exists",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "c", Branch: "side", Checkout: true})
				return err
			},
			want: []string{"worktree", "add", "--", filepath.Join(root, ".claude", "worktrees", "c"), "side"},
		},
		{
			name: "a commit with no branch at all",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "d", Detach: true, Start: "HEAD~1"})
				return err
			},
			want: []string{"worktree", "add", "--detach", "--", filepath.Join(root, ".claude", "worktrees", "d"), "HEAD~1"},
		},
		{
			name: "a name that is a path",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "../x"})
				return err
			},
			wantErr: true,
		},
		{
			name: "a name that would be an option",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "-f"})
				return err
			},
			wantErr: true,
		},
		{
			name: "a branch git refuses",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "e", Branch: "feat/.x"})
				return err
			},
			wantErr: true,
		},
		{
			name: "a revision that would be an option",
			act: func(r Runner) error {
				_, err := r.AddWorktree(context.Background(), root, AddWorktree{Name: "f", Start: "--output=/tmp/x"})
				return err
			},
			wantErr: true,
		},
		{
			name:    "removing a name that is a path",
			act:     func(r Runner) error { return r.RemoveWorktree(context.Background(), root, "../x", false) },
			wantErr: true,
		},
		{
			name: "pruning what is gone",
			act:  func(r Runner) error { return r.PruneWorktrees(context.Background(), root) },
			want: []string{"worktree", "prune"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGit{outputs: map[string]Result{
				"rev-parse": {Stdout: []byte(root + "\n" + root + "\n" + root + "\n")},
				"worktree":  {},
			}}
			err := tc.act(Runner{Executor: f})
			var ran [][]string
			for _, call := range f.calls {
				if call[5] == "worktree" {
					ran = append(ran, call[5:])
				}
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("the command went through, want it refused")
				}
				if len(ran) != 0 {
					t.Fatalf("the command ran %v, want no worktree command at all", ran)
				}
				return
			}
			if len(ran) == 0 {
				t.Fatalf("no worktree command ran (error: %v)", err)
			}
			if !slices.Equal(ran[0], tc.want) {
				t.Fatalf("the command ran %v, want %v", ran[0], tc.want)
			}
		})
	}
}
