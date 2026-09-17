package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Errors of a launch.
var (
	// ErrNotInstalled reports an isolated launch without an installed plugin.
	ErrNotInstalled = errors.New("review: codediff.nvim is not installed (run: lmux review install)")
	// ErrNotPrepared reports an isolated launch before Prepare wrote the init file.
	ErrNotPrepared = errors.New("review: the review editor init file has not been written")
	// ErrNvimMissing reports that Neovim was not found.
	ErrNvimMissing = errors.New("review: Neovim (nvim) was not found on PATH")
	// ErrUnavailable reports that the editor has no :CodeDiff command.
	ErrUnavailable = errors.New("review: the :CodeDiff command is not available")
	// ErrLaunchFailed reports that the review could not open.
	ErrLaunchFailed = errors.New("review: the review could not open")
)

// maxInitBytes bounds reading back an existing init file.
const maxInitBytes = 1 << 20

// Prepare writes the isolated editor's init file for the installed plugin
// location. The file is rewritten, atomically and privately, only when its
// content changes, so launches do not touch the disk in the common case.
func Prepare(paths xdg.Paths, opts domain.InitOptions) (string, error) {
	opts.PluginDir = PluginDir(paths)
	content, err := domain.RenderInit(opts)
	if err != nil {
		return "", err
	}
	if err := fsx.EnsurePrivateDir(paths.ReviewDir()); err != nil {
		return "", err
	}
	path := InitFile(paths)
	current, err := fsx.ReadFileNoFollow(path, maxInitBytes)
	if err == nil && bytes.Equal(current, content) {
		return path, nil
	}
	if errors.Is(err, fsx.ErrSymlink) {
		return "", err
	}
	if err := fsx.WriteFileAtomic(path, content, fsx.PrivateFile); err != nil {
		return "", err
	}
	return path, nil
}

// LaunchOptions configure Command and TmuxShellCommand.
type LaunchOptions struct {
	Paths xdg.Paths
	// Mode selects the isolated or the user's own Neovim setup.
	Mode domain.EditorMode
	// Dir is the repository directory the review runs in.
	Dir string
	// Nvim is the Neovim executable name or path; empty means "nvim".
	Nvim string
	// LookPath resolves Nvim; nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Environ is the environment the editor inherits; nil means os.Environ().
	Environ []string
}

// Launch is a ready-to-run review editor.
type Launch struct {
	// Path is the absolute Neovim binary.
	Path string
	// Args is the full argument vector, Args[0] included.
	Args []string
	// Env is the complete environment: the inherited one with the review
	// variables set.
	Env []string
	// Set holds only the review variables, KEY=VALUE.
	Set []string
	// Dir is the working directory.
	Dir string
}

// Command builds the editor launch for a request.
func Command(req domain.Request, opts LaunchOptions) (Launch, error) {
	if opts.Mode == "" {
		opts.Mode = domain.EditorIsolated
	}
	if opts.Nvim == "" {
		opts.Nvim = "nvim"
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	if err := req.Validate(); err != nil {
		return Launch{}, err
	}
	if !filepath.IsAbs(opts.Dir) {
		return Launch{}, fmt.Errorf("review: directory %q is not absolute", opts.Dir)
	}
	if err := checkRealDirFollow(opts.Dir); err != nil {
		return Launch{}, err
	}
	if opts.Mode == domain.EditorIsolated {
		if checkRealDir(PluginDir(opts.Paths)) != nil {
			return Launch{}, ErrNotInstalled
		}
		if !regularFile(InitFile(opts.Paths)) {
			return Launch{}, ErrNotPrepared
		}
	}
	nvim, err := opts.LookPath(opts.Nvim)
	if err != nil {
		return Launch{}, fmt.Errorf("%w: %w", ErrNvimMissing, err)
	}
	if nvim, err = filepath.Abs(nvim); err != nil {
		return Launch{}, fmt.Errorf("%w: %w", ErrNvimMissing, err)
	}
	plan, err := domain.NewPlan(req, opts.Mode, domain.PlanOptions{
		Nvim:     nvim,
		InitFile: InitFile(opts.Paths),
		LogFile:  LogFile(opts.Paths),
		Dir:      opts.Dir,
	})
	if err != nil {
		return Launch{}, err
	}
	return Launch{
		Path: nvim,
		Args: plan.Argv,
		Env:  mergeEnv(opts.Environ, plan.Env),
		Set:  plan.Env,
		Dir:  opts.Dir,
	}, nil
}

// checkRealDirFollow fails unless path is an existing directory; links are
// followed because a repository may well be reached through one.
func checkRealDirFollow(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("review: %s: %w", path, fsx.ErrNotDir)
	}
	return nil
}

// mergeEnv returns base with every variable of set replaced or added.
func mergeEnv(base, set []string) []string {
	names := make(map[string]bool, len(set))
	for _, kv := range set {
		name, _, _ := strings.Cut(kv, "=")
		names[name] = true
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !names[name] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// Cmd returns the process for the launch; the caller attaches the terminal.
func (l Launch) Cmd(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, l.Path, l.Args[1:]...)
	cmd.Args = l.Args
	cmd.Env = l.Env
	cmd.Dir = l.Dir
	return cmd
}

// Run runs the editor attached to the given streams and maps the launcher's
// exit codes to ErrUnavailable and ErrLaunchFailed. The editor has already
// shown the reason by then.
func (l Launch) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := l.Cmd(ctx)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return exitError(cmd.Run())
}

func exitError(err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	switch exitErr.ExitCode() {
	case domain.ExitUnavailable:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	case domain.ExitFailed:
		return fmt.Errorf("%w: %w", ErrLaunchFailed, err)
	}
	return fmt.Errorf("review: Neovim exited: %w", err)
}

// TmuxShellCommand renders the launch as one shell command for the body of
// a tmux new-window, split-window or display-popup. The editor enters Dir
// itself; passing it with -c (or -d for display-popup), FormatEscape'd, also
// starts the shell there.
// display-popup and run-shell expand formats in the command, so wrap the
// result in tmux.FormatEscape there; new-window and split-window do not.
func TmuxShellCommand(req domain.Request, opts LaunchOptions) (string, error) {
	l, err := Command(req, opts)
	if err != nil {
		return "", err
	}
	words := make([]string, 0, 2+len(l.Set)+len(l.Args))
	words = append(words, "exec", "env")
	for _, kv := range l.Set {
		words = append(words, tmux.ShellQuote(kv))
	}
	for _, a := range l.Args {
		words = append(words, tmux.ShellQuote(a))
	}
	return strings.Join(words, " "), nil
}
