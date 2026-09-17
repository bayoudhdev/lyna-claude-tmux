package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// ErrNoWorkspace reports a workspace session that does not exist.
var ErrNoWorkspace = errors.New("no such workspace")

// ErrNestedTmux reports an attach from a pane of another tmux server.
var ErrNestedTmux = errors.New("already inside another tmux session")

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
// Changes views block on the channel of the session id, which the rename
// leaves untouched, so nothing else needs to happen: a view waiting before
// the rename keeps waiting and is woken by the next hook signal.
func (s *Server) Rename(ctx context.Context, from, to string) error {
	for _, n := range []string{from, to} {
		if err := session.Validate(n); err != nil {
			return err
		}
	}
	_, err := s.Client.Run(ctx, "rename-session", "-t", tmux.ExactSession(from), to)
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

// AttachOption changes how a workspace is attached.
type AttachOption func(*attachOptions)

type attachOptions struct{ nested bool }

// AllowNested attaches even from inside another tmux server. It is what the
// --nested flag passes: the caller has been told what nesting costs and wants
// it anyway.
func AllowNested() AttachOption { return func(o *attachOptions) { o.nested = true } }

// AttachCommand checks that the workspace exists and returns how to show it
// in this terminal, with the environment scrubbed as for the server. From a
// pane of the lyna-tmux server itself it switches the current client instead:
// tmux refuses a nested attach to its own server. From another tmux server it
// refuses, because that terminal would run one tmux inside another, unless
// AllowNested says to go ahead.
func (s *Server) AttachCommand(ctx context.Context, h Host, name string, opts ...AttachOption) (Attach, error) {
	var o attachOptions
	for _, opt := range opts {
		opt(&o)
	}
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
	if socket, ok := s.ForeignTmux(h); ok && !o.nested {
		return Attach{}, fmt.Errorf("%w (%s): the workspace would run as a tmux inside a tmux, "+
			"where the outer session takes the prefix key first and the answers your terminal sends to the "+
			"inner one are typed into whatever pane has the focus, agent prompt included; "+
			"detach the outer session and run this again from the terminal, "+
			"or pass --nested to attach anyway", ErrNestedTmux, socket)
	}
	return Attach{Argv: s.Client.Argv(tmux.Command{"attach-session", "-t", tmux.ExactSession(name)}), Env: env}, nil
}

// ForeignTmux returns the socket of the tmux server this process runs in when
// that server is not the lyna-tmux one. Its own server is not foreign: a
// workspace opened from a pane of it switches the client rather than nesting.
func (s *Server) ForeignTmux(h Host) (string, bool) {
	socket, ok := foreignSocket(h, s.SocketName)
	if !ok {
		return "", false
	}
	// A variable inherited from a server that has since exited names nothing
	// to nest inside, so the socket has to still be there.
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		return "", false
	}
	return socket, true
}

// foreignSocket reads the socket path tmux exports and reports whether it
// belongs to a server other than the one named.
func foreignSocket(h Host, name string) (string, bool) {
	socket, _, ok := strings.Cut(h.Getenv("TMUX"), ",")
	if !ok || socket == "" {
		return "", false
	}
	socket = filepath.Clean(socket)
	if socket == SocketPath(h.Getenv, name) {
		return "", false
	}
	return socket, true
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
