// Package git runs the git commands of a workspace: one adapter, argv only,
// with a timeout, a cap on what it reads back and an environment that cannot
// point a command at another repository. Everything it reads is parsed by
// internal/domain/vcs.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// Defaults for Runner.
const (
	DefaultTimeout   = 10 * time.Second
	DefaultMaxOutput = 16 << 20
	maxStderr        = 64 << 10
)

var (
	// ErrNotInstalled reports that the git binary is not installed.
	ErrNotInstalled = errors.New("git: not installed")
	// ErrNotRepository reports a directory outside any git working tree.
	ErrNotRepository = errors.New("git: not a git repository")
	// ErrOutputTooLarge reports git output over the runner's cap.
	ErrOutputTooLarge = errors.New("git: output too large")
)

// Result is the captured outcome of one git invocation.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor runs a program with an explicit environment, keeping at most limit
// bytes of standard output (ErrOutputTooLarge past it). Tests substitute a fake.
type Executor interface {
	Exec(ctx context.Context, bin string, args, env []string, limit int64) (Result, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, bin string, args, env []string, limit int64) (Result, error)

// Exec implements Executor.
func (f ExecutorFunc) Exec(ctx context.Context, bin string, args, env []string, limit int64) (Result, error) {
	return f(ctx, bin, args, env, limit)
}

// Runner runs the git commands behind the change model. The zero value uses
// git from PATH with the default timeout and output cap.
type Runner struct {
	Bin       string
	Executor  Executor
	Timeout   time.Duration
	MaxOutput int64
	// Environ is the base environment; nil means os.Environ().
	Environ func() []string
}

// Repo locates a working tree.
type Repo struct {
	// Root is the top-level directory of the working tree.
	Root string
	// GitDir holds this worktree's index and HEAD.
	GitDir string
	// CommonDir holds refs and packed-refs (differs from GitDir in linked
	// worktrees).
	CommonDir string
}

// Repo resolves the working tree containing dir.
func (r Runner) Repo(ctx context.Context, dir string) (Repo, error) {
	out, err := r.git(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return Repo{}, fmt.Errorf("%w: rev-parse printed %d lines", vcs.ErrMalformed, len(lines))
	}
	for _, l := range lines {
		// A path with a newline would shift the lines; each must stand alone.
		if !filepath.IsAbs(l) {
			return Repo{}, fmt.Errorf("%w: rev-parse path %q", vcs.ErrMalformed, l)
		}
		if info, err := os.Stat(l); err != nil || !info.IsDir() {
			return Repo{}, fmt.Errorf("%w: rev-parse path %q is not a directory", vcs.ErrMalformed, l)
		}
	}
	return Repo{Root: lines[0], GitDir: lines[1], CommonDir: lines[2]}, nil
}

// Changes reads the status and both line counts of the working tree
// containing dir. The three git commands run concurrently; they only read.
func (r Runner) Changes(ctx context.Context, dir string) (vcs.Changes, error) {
	var (
		wg                           sync.WaitGroup
		statusOut, unstaged, staged  []byte
		statusErr, unstagedErr, sErr error
	)
	diff := []string{"diff", "--numstat", "-z", "--no-ext-diff", "--no-textconv", "--no-color"}
	wg.Go(func() {
		statusOut, statusErr = r.git(ctx, dir, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal")
	})
	wg.Go(func() { unstaged, unstagedErr = r.git(ctx, dir, diff...) })
	wg.Go(func() { staged, sErr = r.git(ctx, dir, append(diff, "--cached")...) })
	wg.Wait()
	if err := errors.Join(statusErr, unstagedErr, sErr); err != nil {
		// Report the status failure alone when there is one: it names the
		// cause (not a repository) that also broke the diffs.
		if statusErr != nil {
			return vcs.Changes{}, statusErr
		}
		return vcs.Changes{}, err
	}
	st, err := vcs.ParseStatus(statusOut)
	if err != nil {
		return vcs.Changes{}, err
	}
	un, err := vcs.ParseNumstat(unstaged)
	if err != nil {
		return vcs.Changes{}, err
	}
	sg, err := vcs.ParseNumstat(staged)
	if err != nil {
		return vcs.Changes{}, err
	}
	return vcs.Build(st, un, sg), nil
}

// Worktrees lists the worktrees of the repository containing dir, the one it
// is in included. It needs git 2.36 or newer, which is where `worktree list`
// learned to separate its records with NUL: a path holding a newline is
// otherwise read as two worktrees.
func (r Runner) Worktrees(ctx context.Context, dir string) ([]vcs.Worktree, error) {
	out, err := r.git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return vcs.ParseWorktrees(out)
}

// Branches lists the branches of the repository containing dir, in the order
// git keeps its refs, with what each one owes its upstream.
func (r Runner) Branches(ctx context.Context, dir string) ([]vcs.LocalBranch, error) {
	out, err := r.git(ctx, dir, "for-each-ref", "--format="+vcs.BranchFormat, "refs/heads/")
	if err != nil {
		return nil, err
	}
	return vcs.ParseBranches(out)
}

// git runs one command. Global options make the run side-effect free and safe
// in an untrusted repository: no pager, no optional index lock (so refreshes
// never trigger another file event), no fsmonitor hook program, and an
// environment that cannot redirect -C to another repository.
func (r Runner) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	limit := r.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	bin := r.Bin
	if bin == "" {
		bin = "git"
	}
	exe := r.Executor
	if exe == nil {
		exe = osExecutor{}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"--no-pager", "-c", "core.fsmonitor=false", "-C", dir}, args...)
	res, err := exe.Exec(ctx, bin, argv, r.environ(), limit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ErrOutputTooLarge) {
			return nil, fmt.Errorf("git %s: %w", args[0], ctxErr)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if strings.Contains(msg, "not a git repository") {
			return nil, fmt.Errorf("%s: %w", dir, ErrNotRepository)
		}
		return nil, fmt.Errorf("git %s: exit status %d: %s", args[0], res.ExitCode, msg)
	}
	return res.Stdout, nil
}

// scrubbedGitEnv lists variables that would make git ignore -C (or read
// another index); a changes pane launched from a hook context must still read
// the directory it was given.
var scrubbedGitEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
	"GIT_PREFIX", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

func (r Runner) environ() []string {
	base := r.Environ
	if base == nil {
		base = os.Environ
	}
	env := make([]string, 0, 32)
	for _, kv := range base() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case isScrubbed(name), name == "GIT_OPTIONAL_LOCKS", name == "GIT_PAGER", name == "PAGER",
			name == "GIT_TERMINAL_PROMPT", name == "LC_ALL":
			continue
		}
		env = append(env, kv)
	}
	// LC_ALL=C keeps diagnostics in English so ErrNotRepository is detected.
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

func isScrubbed(name string) bool {
	for _, s := range scrubbedGitEnv {
		if name == s {
			return true
		}
	}
	return false
}

type osExecutor struct{}

func (osExecutor) Exec(ctx context.Context, bin string, args, env []string, limit int64) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdin = nil
	// A helper that inherited the pipes must not keep Wait blocked after git
	// itself exited or was killed.
	cmd.WaitDelay = time.Second
	stdout := &capped{limit: limit}
	stderr := &capped{limit: maxStderr, truncate: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if stdout.over {
		return res, ErrOutputTooLarge
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && ctx.Err() == nil {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return res, ErrNotInstalled
		}
		return res, err
	}
	return res, nil
}

// capped is a writer that keeps at most limit bytes. Past the limit it either
// silently drops the rest (truncate) or fails the write, which closes the pipe
// and stops the process.
type capped struct {
	buf      bytes.Buffer
	limit    int64
	truncate bool
	over     bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.limit - int64(c.buf.Len())
	if int64(len(p)) <= room {
		return c.buf.Write(p)
	}
	if room > 0 {
		c.buf.Write(p[:room])
	}
	if c.truncate {
		return len(p), nil
	}
	c.over = true
	return 0, ErrOutputTooLarge
}
