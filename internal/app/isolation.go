package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// containerMarkerExists reports whether a container runtime marker file
// exists. Tests replace it to run as if inside or outside a container.
var containerMarkerExists = func(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// isolationContainerEvidence reports what the filesystem says about the
// container this process runs in.
func isolationContainerEvidence() sandbox.ContainerEvidence {
	return sandbox.DetectContainer(containerMarkerExists)
}

// isolationInContainer reports whether this process runs inside the dev
// container lyna-tmux renders, the only container it can say anything about.
// A container runtime marker alone would let a CI job, a privileged container
// or one with the Docker socket mounted unlock bypassPermissions and turn off
// the fail-closed sandbox check, none of which that container confines.
func isolationInContainer() bool { return isolationContainerEvidence().DevContainer() }

// isolationAvailable reports whether Claude panes can start at an isolation
// level on this host.
func isolationAvailable(h Host, isolation sandbox.Isolation) error {
	switch isolation {
	case sandbox.IsolationBash:
		return nil
	case sandbox.IsolationProcess:
		_, err := srtPath(h)
		return err
	case sandbox.IsolationContainer:
		ev := isolationContainerEvidence()
		switch {
		case ev.DevContainer():
			return nil
		case ev.Runtime:
			return fmt.Errorf("%w: %s; container isolation runs the workspace inside the project's dev container, which `lyna-tmux sandbox devcontainer up` builds and starts",
				ErrIsolationUnsupported, ev.Reason())
		}
		return fmt.Errorf("%w: container isolation runs the workspace inside the project's dev container; start it with `lyna-tmux sandbox devcontainer up`, then run `lyna-tmux create --isolation container`", ErrIsolationUnsupported)
	}
	return fmt.Errorf("%w: %s", ErrIsolationUnsupported, isolation)
}

// srtPath finds the sandbox runtime. A PATH hit relative to the working
// directory is refused, since a checkout could plant one.
func srtPath(h Host) (string, error) {
	p, err := h.lookPath()(sandbox.RuntimeBinary)
	if err != nil || !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: process isolation runs Claude inside the sandbox runtime, and %s is not on PATH; install it with `%s`",
			ErrIsolationUnsupported, sandbox.RuntimeBinary, sandbox.RuntimeInstall)
	}
	return filepath.Clean(p), nil
}

// isolate adapts the Claude command of a pane to the isolation level. At
// process isolation the command runs under the sandbox runtime with a
// settings file for this launch in the private settings directory, where the
// settings pruning of workspaces also removes it once it is stale (the runtime
// reads it only when it starts).
func isolate(h Host, res sandbox.Resolution, root string, cmd claudecfg.Command) (claudecfg.Command, error) {
	if res.Isolation != sandbox.IsolationProcess {
		return cmd, nil
	}
	if res.Runtime == nil {
		return claudecfg.Command{}, errors.New("process isolation resolved without sandbox runtime settings")
	}
	bin, err := srtPath(h)
	if err != nil {
		return claudecfg.Command{}, err
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return claudecfg.Command{}, err
	}
	socket, ok := srtEnvValue(cmd.Env, session.EnvSocket)
	if !ok {
		return claudecfg.Command{}, fmt.Errorf("process isolation needs %s in the Claude environment", session.EnvSocket)
	}
	rt, err := res.Runtime.ForLaunch(srtLaunch(h, paths, root, socket))
	if err != nil {
		return claudecfg.Command{}, err
	}
	data, err := sandbox.MarshalRuntime(rt)
	if err != nil {
		return claudecfg.Command{}, err
	}
	file, err := claude.WriteSettings(paths.SettingsDir(), data)
	if err != nil {
		return claudecfg.Command{}, err
	}
	argv := make([]string, 0, len(cmd.Argv)+4)
	argv = append(argv, bin, "--settings", file, "--")
	argv = append(argv, cmd.Argv...)
	return claudecfg.Command{Argv: argv, Env: cmd.Env}, nil
}

// srtLaunch lists what the Claude process writes and connects to: the
// project, the Claude configuration directory and the ~/.claude.json file
// Claude keeps next to it, the lyna-tmux state directory the hooks log to,
// and the tmux server socket the hooks and status line reach. The home
// directory travels with them: the credential files the policy denies are
// named under "~/", and the settings file carries them expanded.
func srtLaunch(h Host, paths xdg.Paths, root, socket string) sandbox.RuntimeLaunch {
	claudeHome := xdg.ClaudeHome(h.Getenv, h.Home)
	writable := []string{claudeHome}
	if filepath.IsAbs(h.Home) {
		writable = append(writable, filepath.Join(h.Home, ".claude.json"))
	}
	writable = append(writable, paths.State)
	return sandbox.RuntimeLaunch{Home: h.Home, Project: root, Writable: writable, Sockets: []string{socket}}
}

// srtEnvValue returns the value of key in KEY=VALUE entries.
func srtEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}
