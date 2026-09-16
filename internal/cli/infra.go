package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// infraCommands are doctor, sandbox, init and uninstall.
func infraCommands(d Deps) []*cobra.Command {
	return []*cobra.Command{doctorCommand(d), sandboxCommand(d), initCommand(d), uninstallCommand(d)}
}

// createIsolated runs `create` for an isolation level whose workspace lives
// outside this host's tmux server. handled is false when the workspace is
// created here as usual.
//
// At container isolation the workspace runs in the project's dev container:
// `lyna-tmux create` is started inside the running container with the same
// flags, attached to this terminal, or without a terminal when detached, in
// which case this host reports where the workspace runs and how to reach it
// from here.
func (d Deps) createIsolated(cmd *cobra.Command, s *app.Server, req app.CreateRequest, detach bool) (handled bool, err error) {
	cc, handled, err := s.ContainerCreateFor(req, detach)
	if !handled || err != nil {
		return handled, err
	}
	argv, err := d.containerExecArgv(cmd, cc, d.Terminal().Interactive)
	if err != nil {
		return true, err
	}
	out := streams(cmd)
	if detach {
		// The create that runs in the container reports the workspace of the
		// container's own tmux server, which this host cannot attach to. The
		// user reads it here, so the message comes from here too.
		out.Out = io.Discard
	}
	if err := d.Run(cmd.Context(), argv, out); err != nil || !detach {
		return true, err
	}
	// Running the same subcommand again, create or team, attaches this
	// terminal to the workspace that is now open in the container.
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "The workspace for %s runs in the dev container %s. Attach with: lyna-tmux %s --isolation container\n",
		sanitize.Line(cc.Target.Dir), cc.Target.Container(), cc.Args[0])
	return true, err
}

// containerCreateArgv is the command that opens req's workspace inside the
// project's dev container, taking the terminal over when interactive says the
// caller has one to give. handled is false when the workspace belongs on this
// host's tmux server, and the returned command is then empty.
func (d Deps) containerCreateArgv(cmd *cobra.Command, s *app.Server, req app.CreateRequest, detach, interactive bool) (argv []string, handled bool, err error) {
	cc, handled, err := s.ContainerCreateFor(req, detach)
	if !handled || err != nil {
		return nil, handled, err
	}
	argv, err = d.containerExecArgv(cmd, cc, interactive)
	return argv, true, err
}

// containerExecArgv is the docker command line that runs cc in the project's
// dev container, which must be running, taking the terminal over when
// interactive says the caller has one to give.
func (d Deps) containerExecArgv(cmd *cobra.Command, cc app.ContainerCreate, interactive bool) ([]string, error) {
	docker, err := d.devcontainerDocker(cmd)
	if err != nil {
		return nil, err
	}
	state, err := docker.State(cmd.Context(), cc.Target)
	if err != nil {
		return nil, err
	}
	if state != devcontainer.StateRunning {
		return nil, fmt.Errorf("container isolation opens the workspace in %s for %s: %w",
			cc.Target.Container(), sanitize.Line(cc.Target.Dir), devcontainer.ErrNotRunning)
	}
	if !interactive {
		return devcontainer.CreateDetachedArgv(docker.Bin, cc.Target, cc.Args), nil
	}
	return devcontainer.CreateArgv(docker.Bin, cc.Target, cc.Args), nil
}

// infraDir resolves a directory argument: the working directory when empty,
// a leading ~ as the home directory, and a relative path against the working
// directory.
func (d Deps) infraDir(h app.Host, arg string) (string, error) {
	return expandDir(arg, h.Home, d.Getwd)
}

// infraConfirm asks a yes or no question on the terminal; anything but y or
// yes is a no. Without a terminal nobody can answer, so it fails with
// noTerminal, which names the flag that skips the question.
func (d Deps) infraConfirm(cmd *cobra.Command, question, noTerminal string) (bool, error) {
	if !d.Terminal().Interactive {
		return false, errors.New(noTerminal)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
