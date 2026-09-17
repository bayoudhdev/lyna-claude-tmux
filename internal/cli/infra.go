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
func (d Deps) createIsolated(cmd *cobra.Command, s *app.Server, req app.CreateRequest, detach bool, start containerStart) (handled bool, err error) {
	cc, handled, err := s.ContainerCreateFor(req, detach)
	if !handled || err != nil {
		return handled, err
	}
	argv, err := d.containerExecArgv(cmd, cc, d.Terminal().Interactive, start)
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
	// The dashboard draws the whole screen, so there is no line to ask a
	// question on and no place for the output of a build: a container that is
	// not running is reported with the command that starts it.
	argv, err = d.containerExecArgv(cmd, cc, interactive, containerStartNever)
	return argv, true, err
}

// containerStart says what a command does about a dev container that is not
// running when a workspace is asked for inside it.
type containerStart int

const (
	// containerStartNever reports the command that starts the container.
	containerStartNever containerStart = iota
	// containerStartAsk asks on the terminal, and reports the flag that
	// answers the question when there is no terminal to ask on.
	containerStartAsk
	// containerStartAlways builds and starts the container without asking.
	containerStartAlways
)

// containerExecArgv is the docker command line that runs cc in the project's
// dev container, taking the terminal over when interactive says the caller has
// one to give. A container that is not running is built and started first,
// which start decides.
func (d Deps) containerExecArgv(cmd *cobra.Command, cc app.ContainerCreate, interactive bool, start containerStart) ([]string, error) {
	docker, err := d.devcontainerDocker(cmd)
	if err != nil {
		return nil, err
	}
	if err := d.containerReady(cmd, docker, cc.Target, start); err != nil {
		return nil, err
	}
	if !interactive {
		return devcontainer.CreateDetachedArgv(docker.Bin, cc.Target, cc.Args), nil
	}
	return devcontainer.CreateArgv(docker.Bin, cc.Target, cc.Args), nil
}

// containerReady makes sure the project's dev container is running before a
// workspace opens in it. Container isolation is the level that needs a
// container built and started before anything can run, and the failure it
// produced was a command for the user to run by hand in the middle of opening
// a workspace. It is built and started here instead, with the build streaming
// to the terminal, so `create --isolation container` works on the first try on
// a machine where the container has never run.
func (d Deps) containerReady(cmd *cobra.Command, docker devcontainer.Docker, t devcontainer.Target, start containerStart) error {
	state, err := docker.State(cmd.Context(), t)
	if err != nil {
		return err
	}
	if state == devcontainer.StateRunning {
		return nil
	}
	notRunning := fmt.Errorf("container isolation opens the workspace in %s for %s: %w",
		t.Container(), sanitize.Line(t.Dir), devcontainer.ErrNotRunning)
	switch start {
	case containerStartNever:
		return notRunning
	case containerStartAsk:
		question := fmt.Sprintf("The dev container %s for %s is not running. Build and start it now?", t.Container(), sanitize.Line(t.Dir))
		ok, err := d.infraConfirm(cmd, question, notRunning.Error()+"; pass --start-container to build and start it without a terminal")
		if err != nil {
			return err
		}
		if !ok {
			return notRunning
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Starting the dev container %s. The first build takes a few minutes.\n", t.Container())
	if err := docker.Up(cmd.Context(), t); err != nil {
		return err
	}
	state, err = docker.State(cmd.Context(), t)
	if err != nil {
		return err
	}
	if state != devcontainer.StateRunning {
		return fmt.Errorf("%s was built and started but reports state %q: %w", t.Container(), string(state), devcontainer.ErrNotRunning)
	}
	return nil
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
