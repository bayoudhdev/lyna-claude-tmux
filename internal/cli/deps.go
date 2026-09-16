package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
)

// Deps are the process facts and effects commands reach outside the Go
// runtime. Main uses the real process; tests substitute isolated servers and
// recorders.
type Deps struct {
	// Host describes the running process for the use cases.
	Host func() (app.Host, error)
	// Exec replaces the process with the program at path (attaching hands the
	// terminal to tmux). It returns only on failure.
	Exec func(path string, argv, env []string) error
	// LookPath resolves a program name on PATH.
	LookPath func(name string) (string, error)
	// Run starts an interactive program on the command's streams and waits
	// for it to exit.
	Run func(ctx context.Context, argv []string, s Streams) error
	// Getwd returns the working directory.
	Getwd func() (string, error)
	// Terminal reports whether standard input and output are a terminal, and
	// its size in cells (zero when unknown).
	Terminal func() Terminal
	// Now is the clock for ages, cache freshness and status rendering.
	Now func() time.Time
}

// Terminal describes the terminal a command runs in.
type Terminal struct {
	Interactive   bool
	Width, Height int
}

// ProcessDeps returns the dependencies of the real process.
func ProcessDeps() Deps {
	return Deps{Host: processHost, Exec: syscall.Exec, LookPath: exec.LookPath, Run: runProgram, Getwd: os.Getwd, Terminal: processTerminal, Now: time.Now}
}

func processTerminal() Terminal {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	t := Terminal{Interactive: term.IsTerminal(in) && term.IsTerminal(out)}
	if w, h, err := term.GetSize(out); err == nil {
		t.Width, t.Height = w, h
	}
	return t
}

func runProgram(ctx context.Context, argv []string, s Streams) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = s.In, s.Out, s.Err
	return c.Run()
}

// now returns the current time from the injected clock.
func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// streams returns the command's standard streams.
func streams(cmd *cobra.Command) Streams {
	return Streams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
}

func processHost() (app.Host, error) {
	exe, err := os.Executable()
	if err != nil {
		return app.Host{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return app.Host{}, err
	}
	// A missing home is reported by path resolution unless LYNA_TMUX_HOME
	// replaces it.
	home, _ := os.UserHomeDir()
	return app.Host{Getenv: os.Getenv, Environ: os.Environ(), Home: home, Exe: exe, LookPath: exec.LookPath}, nil
}

// openServer resolves the host and opens the workspace server for a command.
func (d Deps) openServer(cmd *cobra.Command) (context.Context, app.Host, *app.Server, error) {
	ctx := cmd.Context()
	h, err := d.Host()
	if err != nil {
		return ctx, app.Host{}, nil, err
	}
	s, err := app.OpenServer(ctx, h)
	return ctx, h, s, err
}
