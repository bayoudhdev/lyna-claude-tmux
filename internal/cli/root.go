// Package cli defines the lmux command tree. Commands parse flags and
// arguments, then delegate to internal/app; they hold no business logic.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

// Streams are the standard streams a command reads and writes. Tests pass
// buffers; main passes the process streams.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// exitError carries a specific process exit code through cobra.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// Main runs the command line and returns the process exit code.
func Main(ctx context.Context, args []string, s Streams) int {
	return run(ctx, NewRoot(s), args)
}

func run(ctx context.Context, root *cobra.Command, args []string) int {
	root.SetArgs(args)
	return exitCode(fang.Execute(ctx, root,
		fang.WithVersion(version.Get().Version),
		fang.WithNotifySignal(os.Interrupt, syscall.SIGTERM),
		// The man command is registered by NewRootWith with a reproducible date.
		fang.WithoutManpage(),
	))
}

// exitCode maps a command error to a process exit code: 0 on success, the
// carried code for an exitError anywhere in the chain, 1 otherwise.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := errors.AsType[*exitError](err); ok {
		return ee.code
	}
	return 1
}

// NewRoot builds the full command tree bound to the given streams and the
// real process.
func NewRoot(s Streams) *cobra.Command { return NewRootWith(s, ProcessDeps()) }

// NewRootWith builds the full command tree with explicit dependencies.
func NewRootWith(s Streams, d Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "lmux",
		Short: "Claude Code workspaces on tmux, preconfigured and sandboxed",
		Long: "lmux turns tmux into a ready-made Claude Code workspace: styled split layouts,\n" +
			"an agent picker, per-launch sandbox and hook settings, all on a dedicated tmux server\n" +
			"that never reads or writes your own tmux or Claude settings.",
		// A root without RunE is not runnable, and cobra then skips argument
		// validation: a mistyped command would print help and exit 0.
		Args:          cobra.NoArgs,
		RunE:          rootRunE(d),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(s.In)
	root.SetOut(s.Out)
	root.SetErr(s.Err)

	root.AddCommand(
		newVersionCmd(),
		newCreateCmd(d),
		newLsCmd(d),
		newAttachCmd(d),
		newKillCmd(d),
		newRenameCmd(d),
		newKeysCmd(d),
		newConfigCmd(d),
		newPluginCmd(d),
		newManCmd(version.Get().Date, os.Getenv, d.now),
	)
	// Command groups owned by separate files, so each area grows without
	// editing the tree here.
	for _, group := range [][]*cobra.Command{
		glueCommands(d),
		reviewCommands(d),
		agentCommands(d),
		steerCommands(d),
		taskCommands(d),
		transcriptCommands(d),
		workspaceCommands(d),
		infraCommands(d),
	} {
		root.AddCommand(group...)
	}
	return root
}
