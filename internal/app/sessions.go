package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// ErrNoWorkspace reports a workspace session that does not exist.
var ErrNoWorkspace = errors.New("no such workspace")

// Sessions lists the workspace sessions; a stopped server has none.
func (s *Server) Sessions(ctx context.Context) ([]tmux.Session, error) {
	return s.Client.ListSessions(ctx)
}

// Kill ends one workspace session. Only the lyna-tmux server is ever
// addressed, so a session of the user's own tmux cannot be hit.
func (s *Server) Kill(ctx context.Context, name string) error {
	if err := session.Validate(name); err != nil {
		return err
	}
	_, err := s.Client.Run(ctx, "kill-session", "-t", tmux.ExactSession(name))
	return workspaceErr(name, err)
}

// KillAll stops the lyna-tmux server and every workspace on it. A server that
// is not running is not an error.
func (s *Server) KillAll(ctx context.Context) error {
	_, err := s.Client.Run(ctx, "kill-server")
	if errors.Is(err, tmux.ErrNoServer) {
		return nil
	}
	return err
}

// Rename renames a workspace session. The new name must be valid and free.
// Changes views block on the channel named after the session, so the old
// channel is signaled in the same invocation: they wake and wait on the new
// name. tmux stops the batch when the rename fails.
func (s *Server) Rename(ctx context.Context, from, to string) error {
	for _, n := range []string{from, to} {
		if err := session.Validate(n); err != nil {
			return err
		}
	}
	_, err := s.Client.Batch(ctx,
		tmux.Command{"rename-session", "-t", tmux.ExactSession(from), to},
		tmux.Command{"wait-for", "-S", tmux.ChangesChannel(from)},
	)
	if errors.Is(err, tmux.ErrExists) {
		return fmt.Errorf("%w: %s", tmux.ErrExists, to)
	}
	return workspaceErr(from, err)
}

// Attach is the program a terminal runs to attach to a workspace: the caller
// replaces its process with Argv and Env so tmux owns the terminal.
type Attach struct {
	Argv []string
	Env  []string
}

// AttachCommand checks that the workspace exists and returns how to show it
// in this terminal, with the environment scrubbed as for the server. From a
// pane of the lyna-tmux server itself it switches the current client instead:
// tmux refuses a nested attach to its own server. From the user's own tmux an
// attach works as usual.
func (s *Server) AttachCommand(ctx context.Context, h Host, name string) (Attach, error) {
	if err := session.Validate(name); err != nil {
		return Attach{}, err
	}
	ok, err := s.Client.HasSession(ctx, name)
	if err != nil {
		return Attach{}, err
	}
	if !ok {
		return Attach{}, fmt.Errorf("%w: %s", ErrNoWorkspace, name)
	}
	env := ServerEnviron(h.Environ)
	if s.Inside(h) {
		// switch-client finds the client to switch from the calling pane.
		for _, key := range []string{"TMUX", "TMUX_PANE"} {
			if v := h.Getenv(key); v != "" {
				env = append(env, key+"="+v)
			}
		}
		return Attach{Argv: s.Client.Argv(tmux.Command{"switch-client", "-t", tmux.ExactSession(name)}), Env: env}, nil
	}
	return Attach{Argv: s.Client.Argv(tmux.Command{"attach-session", "-t", tmux.ExactSession(name)}), Env: env}, nil
}

// Inside reports whether the process runs in a pane of this lyna-tmux server,
// from the socket path tmux exports in TMUX.
func (s *Server) Inside(h Host) bool {
	socket, _, ok := strings.Cut(h.Getenv("TMUX"), ",")
	return ok && socket != "" && filepath.Clean(socket) == SocketPath(h.Getenv, s.SocketName)
}

// workspaceErr maps a missing session or a stopped server to ErrNoWorkspace.
func workspaceErr(name string, err error) error {
	if errors.Is(err, tmux.ErrNotFound) || errors.Is(err, tmux.ErrNoServer) {
		return fmt.Errorf("%w: %s", ErrNoWorkspace, name)
	}
	return err
}
