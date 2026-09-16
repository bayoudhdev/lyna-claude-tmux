package watch

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
		want    Changes
		wantErr error
		errText string
	}{
		{
			name:    "joined result",
			outputs: okOutputs,
			want: Changes{
				Branch:  Branch{OID: hashA, Head: "main"},
				Files:   []File{{Kind: KindChanged, Path: "a.go", Index: 'M', Worktree: 'M', Added: 4, Deleted: 1}},
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
		{name: "git missing", err: ErrGitNotFound, wantErr: ErrGitNotFound},
		{name: "timeout", block: true, timeout: time.Millisecond, wantErr: context.DeadlineExceeded},
		{name: "output cap", err: ErrOutputTooLarge, wantErr: ErrOutputTooLarge},
		{
			name: "malformed status",
			outputs: map[string]Result{
				"status": {Stdout: nul("9 nonsense")}, "diff": okOutputs["diff"], "diff --cached": okOutputs["diff --cached"],
			},
			wantErr: ErrMalformed,
		},
		{
			name: "malformed unstaged numstat",
			outputs: map[string]Result{
				"status": okOutputs["status"], "diff": {Stdout: nul("x")}, "diff --cached": okOutputs["diff --cached"],
			},
			wantErr: ErrMalformed,
		},
		{
			name: "malformed staged numstat",
			outputs: map[string]Result{
				"status": okOutputs["status"], "diff": okOutputs["diff"], "diff --cached": {Stdout: nul("x")},
			},
			wantErr: ErrMalformed,
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
		{name: "two lines", out: dir + "\n" + dir + "\n", wantErr: ErrMalformed},
		{name: "relative line", out: dir + "\n.git\n" + dir + "\n", wantErr: ErrMalformed},
		{name: "missing directory", out: dir + "\n" + dir + "/nope\n" + dir + "\n", wantErr: ErrMalformed},
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
		{name: "missing binary", bin: filepath.Join(t.TempDir(), "git"), limit: 64, wantErr: ErrGitNotFound},
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
