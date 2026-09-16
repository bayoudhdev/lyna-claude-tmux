package tmux

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// fieldSep separates format fields in list output. The ASCII unit separator
// does not occur in session names, paths or titles in practice; a row whose
// field count does not match is skipped rather than misparsed.
const fieldSep = "\x1f"

// Session is one row of list-sessions.
type Session struct {
	ID       string
	Name     string
	Windows  int
	Attached int
	Created  time.Time
	Path     string
	Managed  bool
	Project  string
	Sandbox  string
	Layout   string
}

var sessionFields = []string{
	"#{session_id}",
	"#{session_name}",
	"#{session_windows}",
	"#{session_attached}",
	"#{session_created}",
	"#{session_path}",
	"#{" + OptManaged + "}",
	"#{" + OptProject + "}",
	"#{" + OptSandbox + "}",
	"#{" + OptLayout + "}",
}

// ListSessions returns every session. A server that is not running has no
// sessions and is not an error.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	out, err := c.Run(ctx, "list-sessions", "-F", strings.Join(sessionFields, fieldSep))
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil
		}
		return nil, err
	}
	return parseSessions(out), nil
}

func parseSessions(out string) []Session {
	var sessions []Session
	for _, line := range splitLines(out) {
		f := strings.Split(line, fieldSep)
		if len(f) != len(sessionFields) {
			continue
		}
		created, _ := strconv.ParseInt(f[4], 10, 64)
		sessions = append(sessions, Session{
			ID:       f[0],
			Name:     f[1],
			Windows:  atoi(f[2]),
			Attached: atoi(f[3]),
			Created:  time.Unix(created, 0),
			Path:     f[5],
			Managed:  f[6] == "1",
			Project:  f[7],
			Sandbox:  f[8],
			Layout:   f[9],
		})
	}
	return sessions
}

// Pane is one row of list-panes.
type Pane struct {
	ID             string
	SessionID      string
	SessionName    string
	WindowID       string
	WindowIndex    int
	WindowName     string
	PaneIndex      int
	TTY            string
	PID            int
	CurrentPath    string
	CurrentCommand string
	Title          string
	Active         bool
	WindowActive   bool
	Dead           bool
	Width          int
	Height         int
	Role           string
	State          string
}

// Location renders "session:window.pane".
func (p Pane) Location() string {
	return p.SessionName + ":" + strconv.Itoa(p.WindowIndex) + "." + strconv.Itoa(p.PaneIndex)
}

var paneFields = []string{
	"#{pane_id}",
	"#{session_id}",
	"#{session_name}",
	"#{window_id}",
	"#{window_index}",
	"#{window_name}",
	"#{pane_index}",
	"#{pane_tty}",
	"#{pane_pid}",
	"#{pane_current_path}",
	"#{pane_current_command}",
	"#{pane_title}",
	"#{pane_active}",
	"#{window_active}",
	"#{pane_dead}",
	"#{pane_width}",
	"#{pane_height}",
	"#{" + OptRole + "}",
	"#{" + OptState + "}",
}

// ListPanes lists panes. An empty target lists every pane on the server;
// otherwise target names a session (use ExactSession) or window.
func (c *Client) ListPanes(ctx context.Context, target string) ([]Pane, error) {
	args := []string{"-F", strings.Join(paneFields, fieldSep)}
	if target == "" {
		args = append([]string{"-a"}, args...)
	} else {
		args = append([]string{"-s", "-t", target}, args...)
	}
	out, err := c.Run(ctx, "list-panes", args...)
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil
		}
		return nil, err
	}
	return parsePanes(out), nil
}

func parsePanes(out string) []Pane {
	var panes []Pane
	for _, line := range splitLines(out) {
		f := strings.Split(line, fieldSep)
		if len(f) != len(paneFields) {
			continue
		}
		panes = append(panes, Pane{
			ID:             f[0],
			SessionID:      f[1],
			SessionName:    f[2],
			WindowID:       f[3],
			WindowIndex:    atoi(f[4]),
			WindowName:     f[5],
			PaneIndex:      atoi(f[6]),
			TTY:            f[7],
			PID:            atoi(f[8]),
			CurrentPath:    f[9],
			CurrentCommand: f[10],
			Title:          f[11],
			Active:         f[12] == "1",
			WindowActive:   f[13] == "1",
			Dead:           f[14] == "1",
			Width:          atoi(f[15]),
			Height:         atoi(f[16]),
			Role:           f[17],
			State:          f[18],
		})
	}
	return panes
}

// HasSession reports whether a session with exactly this name exists.
func (c *Client) HasSession(ctx context.Context, name string) (bool, error) {
	_, err := c.Run(ctx, "has-session", "-t", ExactSession(name))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNoServer) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, err
}

// CapturePane returns the visible content of a pane with SGR attributes, plus
// up to history lines of scrollback. The output is untrusted: pass it through
// sanitize.Terminal before rendering.
func (c *Client) CapturePane(ctx context.Context, paneID string, history int) (string, error) {
	args := []string{"-p", "-e", "-t", paneID}
	if history > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(history))
	}
	return c.Run(ctx, "capture-pane", args...)
}

// ShowOption returns the value of an option on a target, or "" when unset.
// scope is one of "-g", "-s", "-w", "-p" (optionally combined, e.g. "-gw").
func (c *Client) ShowOption(ctx context.Context, scope, target, name string) (string, error) {
	args := []string{"-qv"}
	if scope != "" {
		args[0] = scope + "qv"
	}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, name)
	out, err := c.Run(ctx, "show-options", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// Display expands a format against a target (display-message -p).
func (c *Client) Display(ctx context.Context, target, format string) (string, error) {
	args := []string{"-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, format)
	out, err := c.Run(ctx, "display-message", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

func splitLines(out string) []string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
