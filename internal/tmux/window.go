package tmux

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
)

// PaneContext is what commands started for a pane (menu items, key bindings,
// popups) read about that pane before they act.
type PaneContext struct {
	ID       string
	Session  string
	WindowID string
	// Path is the pane's current directory.
	Path string
	// Project is the session's @lt_project option, empty outside a workspace.
	Project string
	// Sandbox and Isolation are the session's @lt_sandbox and @lt_isolation
	// options: the sandbox profile and isolation level the workspace was
	// opened with. They are empty outside a workspace.
	Sandbox, Isolation string
	// WindowWidth and WindowHeight are the window size in cells.
	WindowWidth, WindowHeight int
}

var paneContextFields = []string{
	"#{pane_id}",
	"#{session_name}",
	"#{window_id}",
	"#{pane_current_path}",
	"#{" + OptProject + "}",
	"#{" + OptSandbox + "}",
	"#{" + OptIsolation + "}",
	"#{window_width}",
	"#{window_height}",
}

// DescribePane reads the context of the pane target resolves to: a pane id,
// or a session target (ExactSession) for the active pane of its current
// window. Values inserted into the format are never expanded again, so paths
// and option values come back verbatim.
func (c *Client) DescribePane(ctx context.Context, target string) (PaneContext, error) {
	if target == "" {
		return PaneContext{}, errors.New("tmux: describe pane: no target")
	}
	out, err := c.Display(ctx, target, strings.Join(paneContextFields, fieldSep))
	if err != nil {
		return PaneContext{}, err
	}
	f := strings.Split(out, fieldSep)
	// display-message does not fail on a missing target: it expands the
	// format without one, so every field comes back empty.
	if len(f) == len(paneContextFields) && f[0] == "" {
		return PaneContext{}, fmt.Errorf("%w: %s", ErrNotFound, target)
	}
	if len(f) != len(paneContextFields) || !ValidPaneID(f[0]) || !validWindowID(f[2]) {
		return PaneContext{}, fmt.Errorf("tmux display-message: unexpected pane description %q", out)
	}
	return PaneContext{
		ID: f[0], Session: f[1], WindowID: f[2], Path: f[3], Project: f[4],
		Sandbox: f[5], Isolation: f[6], WindowWidth: atoi(f[7]), WindowHeight: atoi(f[8]),
	}, nil
}

// FindWindow returns the id of the window named name in a session, and
// reports whether the session has one. Window names are compared exactly:
// a target such as ":name" would also match by prefix and by pattern.
func (c *Client) FindWindow(ctx context.Context, sessionName, name string) (string, bool, error) {
	if err := session.Validate(sessionName); err != nil {
		return "", false, err
	}
	out, err := c.Run(ctx, "list-windows", "-t", ExactSession(sessionName), "-F", "#{window_id}"+fieldSep+"#{window_name}")
	if err != nil {
		return "", false, err
	}
	for _, line := range splitLines(out) {
		if id, window, ok := strings.Cut(line, fieldSep); ok && window == name && validWindowID(id) {
			return id, true, nil
		}
	}
	return "", false, nil
}

// ValidPaneID reports whether s is a tmux pane id: '%' followed by digits.
// Commands that receive a pane from a key binding check it before using it as
// a target, so a flag value can never select a pane by name or pattern.
func ValidPaneID(s string) bool { return len(s) > 1 && s[0] == '%' && digits(s[1:]) }

func validWindowID(s string) bool { return len(s) > 1 && s[0] == '@' && digits(s[1:]) }

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// SplitSpec is a pane added next to an existing pane.
type SplitSpec struct {
	// Pane is the id of the pane to split and Window the id of its window.
	Pane, Window string
	// Down stacks the new pane below Pane; otherwise it opens to the right.
	Down bool
	// Dir is the absolute start directory of the new pane.
	Dir  string
	Role layout.Role
	Proc PaneProcess
}

// SplitPane splits a pane in half, starts spec.Proc in the new pane and tags
// it with its role and options in the same invocation, then returns the new
// pane id. When tagging fails the new pane is removed again.
func (c *Client) SplitPane(ctx context.Context, spec SplitSpec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	dir := "-h"
	if spec.Down {
		dir = "-v"
	}
	split := Command{"split-window", "-t", spec.Pane, dir, "-c", FormatEscape(spec.Dir), "-P", "-F", "#{pane_id}"}
	split = append(split, spec.Proc.args()...)
	// split-window without -d makes the new pane the active pane of its
	// window, so the window target resolves to it for the options.
	cmds := append([]Command{split}, paneOptions(spec.Window, spec.Role, spec.Proc)...)
	out, err := c.Batch(ctx, cmds...)
	id := strings.TrimSpace(out)
	if err != nil {
		if ValidPaneID(id) {
			return "", c.discard(ctx, Command{"kill-pane", "-t", id}, err)
		}
		return "", err
	}
	if !ValidPaneID(id) {
		return "", fmt.Errorf("tmux split-window: unexpected output %q", out)
	}
	return id, nil
}

func (s SplitSpec) validate() error {
	if !ValidPaneID(s.Pane) {
		return fmt.Errorf("split: %q is not a pane id", s.Pane)
	}
	if !validWindowID(s.Window) {
		return fmt.Errorf("split: %q is not a window id", s.Window)
	}
	if !filepath.IsAbs(s.Dir) || strings.ContainsRune(s.Dir, 0) {
		return fmt.Errorf("split: directory %q is not absolute", s.Dir)
	}
	switch s.Role {
	case layout.RoleClaude, layout.RoleShell, layout.RoleChanges, layout.RoleReview, layout.RoleCommand:
	default:
		return fmt.Errorf("split: unknown role %q", s.Role)
	}
	return s.Proc.validate()
}
