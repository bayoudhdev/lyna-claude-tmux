// Package doctor inspects the machine for everything lyna-tmux and Claude Code
// need: tmux, Claude Code, git, the sandbox prerequisites of the platform,
// terminal capabilities, key binding collisions, Docker for container
// isolation and Neovim for the review popup.
//
// Doctor only reads. Every problem it reports carries the exact command or
// setting that fixes it; it never installs or changes anything itself. All
// access to the system goes through Deps so every check runs against fakes in
// tests.
package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// Status is the outcome of one check.
type Status string

// Check outcomes. Fail means lyna-tmux or Claude Code cannot work as
// configured; warn means a feature is degraded; skip means the check does not
// apply to this configuration or platform.
const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Result is one finding.
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Fix is the exact command or setting that resolves a warn or fail.
	Fix string `json:"fix,omitempty"`
	// Action is how `doctor --fix` applies Fix, absent when the fix is
	// something only the user can carry out.
	Action *Action `json:"action,omitempty"`
}

// FixKind says how a fix is applied.
type FixKind string

// Kinds of fix. Manual fixes are shown and never run: a terminal setting, an
// account, a package manager that asks for a password. A command is an
// argument vector run as it stands. A builtin is a step lyna-tmux carries out
// itself, named by ID.
const (
	FixManual  FixKind = "manual"
	FixCommand FixKind = "command"
	FixBuiltin FixKind = "builtin"
)

// Builtin fix identifiers. The doctor package names them; the command layer
// carries them out, since that is where the rest of lyna-tmux is reachable.
const (
	// FixReviewInstall installs the pinned review plugin.
	FixReviewInstall = "review-install"
	// FixReviewReinstall reinstalls it over an installation that does not
	// match its pin.
	FixReviewReinstall = "review-reinstall"
)

// Action is a fix that can be applied without the user typing it.
type Action struct {
	Kind FixKind `json:"kind"`
	// What names what applying it does, as one sentence in the imperative.
	What string `json:"what"`
	// Argv is the command of a FixCommand action.
	Argv []string `json:"argv,omitempty"`
	// ID names the step of a FixBuiltin action.
	ID string `json:"id,omitempty"`
}

// CommandFix is an action that runs argv as it stands.
func CommandFix(what string, argv ...string) *Action {
	return &Action{Kind: FixCommand, What: what, Argv: argv}
}

// BuiltinFix is an action lyna-tmux carries out itself.
func BuiltinFix(what, id string) *Action { return &Action{Kind: FixBuiltin, What: what, ID: id} }

// DefaultTimeout bounds every external command a check runs. A hung docker
// daemon or a slow network mount must not freeze the report.
const DefaultTimeout = 5 * time.Second

// maxOutputBytes caps what doctor keeps from a command's output; version
// strings are a few bytes.
const maxOutputBytes = 64 << 10

// Deps is everything doctor reads from the system, plus the configuration
// values that change what a check expects.
type Deps struct {
	// GOOS is the platform, runtime.GOOS in production.
	GOOS string
	// LookPath finds an executable on PATH.
	LookPath func(file string) (string, error)
	// Getenv reads an environment variable.
	Getenv func(key string) string
	// ReadFile reads at most limit bytes of a file, failing past the limit.
	ReadFile func(path string, limit int64) ([]byte, error)
	// Run executes an argument vector and returns its standard output. The
	// context carries the per-command timeout.
	Run func(ctx context.Context, argv []string) (string, error)
	// Exists reports whether a path exists.
	Exists func(path string) bool

	// ClaudeHome is the Claude Code configuration directory (xdg.ClaudeHome).
	ClaudeHome string
	// Home is the user's home directory, which locates the Claude Code state
	// file next to the default configuration directory.
	Home string
	// ProjectDir is the project the report is about, empty outside one. Claude
	// Code records trust per project directory.
	ProjectDir string
	// ClaudeMinVersion, when set, is the oldest Claude Code release accepted.
	ClaudeMinVersion string
	// AltKeys mirrors ui.alt_keys: the root-table Alt bindings are installed.
	AltKeys bool
	// Prefix is the tmux prefix key (workspace.prefix), "C-b" when empty.
	Prefix string
	// SandboxProfile mirrors sandbox.profile; "off" skips sandbox prerequisites.
	SandboxProfile string
	// Isolation mirrors sandbox.isolation; "container" makes Docker required.
	Isolation string
	// Timeout bounds each command; DefaultTimeout when zero.
	Timeout time.Duration
}

// System returns Deps backed by the running process: PATH lookups, the
// process environment, bounded file reads and argv execution.
func System() Deps {
	return Deps{
		GOOS:     runtime.GOOS,
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		ReadFile: fsx.ReadFileLimited,
		Run:      runCommand,
		Exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		Prefix:  "C-b",
		Timeout: DefaultTimeout,
	}
}

// Run executes every check concurrently and returns the results in a fixed
// order. The Deps functions must be safe for concurrent use.
func Run(ctx context.Context, d Deps) []Result {
	d = d.withDefaults()
	checks := []func(context.Context, Deps) []Result{
		checkTmux,
		checkClaude,
		checkProjectTrust,
		checkGit,
		checkSandbox,
		checkSandboxRuntime,
		checkTruecolor,
		checkClipboard,
		checkOptionAsMeta,
		checkKeybindings,
		checkDocker,
		checkNeovim,
	}
	slots := make([][]Result, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Go(func() { slots[i] = check(ctx, d) })
	}
	wg.Wait()
	return slices.Concat(slots...)
}

var errUnavailable = errors.New("doctor: not available")

// withDefaults replaces missing functions with inert ones, so a partially
// filled Deps reports tools as missing instead of panicking.
func (d Deps) withDefaults() Deps {
	if d.LookPath == nil {
		d.LookPath = func(string) (string, error) { return "", errUnavailable }
	}
	if d.Getenv == nil {
		d.Getenv = func(string) string { return "" }
	}
	if d.ReadFile == nil {
		d.ReadFile = func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist }
	}
	if d.Run == nil {
		d.Run = func(context.Context, []string) (string, error) { return "", errUnavailable }
	}
	if d.Exists == nil {
		d.Exists = func(string) bool { return false }
	}
	if d.Prefix == "" {
		d.Prefix = "C-b"
	}
	if d.Timeout <= 0 {
		d.Timeout = DefaultTimeout
	}
	return d
}

// output runs argv under the per-command timeout.
func (d Deps) output(ctx context.Context, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	return d.Run(ctx, argv)
}

// runCommand executes argv without a shell and keeps a bounded amount of its
// output. Standard error is folded into the returned error.
func runCommand(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("doctor: empty command")
	}
	var stdout, stderr cappedBuffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A child that leaves a grandchild holding the pipes must not outlive
	// the timeout.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%s: %w: %s", argv[0], err, msg)
		}
		return stdout.String(), fmt.Errorf("%s: %w", argv[0], err)
	}
	return stdout.String(), nil
}

// cappedBuffer keeps the first maxOutputBytes written and discards the rest
// while still reporting full writes, so the child never blocks on a full pipe.
type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxOutputBytes - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
