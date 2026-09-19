package tmux

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
)

// PaneProcess is what one pane of a workspace runs. The zero value runs the
// default shell.
type PaneProcess struct {
	// Argv is executed directly, without a shell.
	Argv []string
	// Shell is a command line for the default shell, used by layout command
	// panes. It is ignored when Argv is set.
	Shell string
	// Env holds KEY=VALUE pairs set for this pane's process only.
	Env []string
	// Options are pane options set when the pane is created, in the same tmux
	// invocation: the user options this workspace reads back (names starting
	// with @) and the tmux options a single pane needs.
	Options map[string]string
}

// WindowSpec is a window built from a layout plan.
type WindowSpec struct {
	// Name is the window name; empty keeps tmux's automatic naming.
	Name string
	// Dir is the absolute start directory of every pane.
	Dir string
	// Plan arranges the panes.
	Plan layout.Plan
	// Procs holds one process per plan pane, in plan order.
	Procs []PaneProcess
}

// WorkspaceSpec is a managed session with one window.
type WorkspaceSpec struct {
	Session string
	// Project, Sandbox and Isolation are recorded in the session options read
	// by the status line, the pickers and the sandbox status.
	Project   string
	Sandbox   string
	Isolation string
	// Width and Height size the detached session in cells until a client
	// attaches; zero keeps tmux's default.
	Width, Height int
	Window        WindowSpec
}

// Built identifies what a workspace build created.
type Built struct {
	SessionID string
	WindowID  string
	// Panes are pane ids in plan order.
	Panes []string
}

// cleanupTimeout bounds the removal of a partially built workspace, which runs
// even when the caller's context is already done.
const cleanupTimeout = 5 * time.Second

// createdFields is printed by new-session and new-window -P.
var createdFields = []string{"#{session_id}", "#{window_id}", "#{pane_id}"}

// CreateWorkspace creates a detached session laid out by spec.Window.Plan and
// tags it with the lyna-tmux session and pane options. A session that
// already exists is left untouched and the error matches ErrExists. When a
// later step fails, the half-built session is killed so a retry starts clean.
//
// Every pane's options are set in the same tmux invocation that starts its
// process, so they are in place before tmux can notice the process exit: a
// Claude pane that fails at once keeps its exit status on screen.
func (c *Client) CreateWorkspace(ctx context.Context, spec WorkspaceSpec) (Built, error) {
	if err := spec.validate(); err != nil {
		return Built{}, err
	}
	w := spec.Window
	target := ExactSession(spec.Session)
	first := Command{"new-session", "-d", "-s", spec.Session}
	first = append(first, w.createArgs()...)
	if spec.Width > 0 && spec.Height > 0 {
		first = append(first, "-x", strconv.Itoa(spec.Width), "-y", strconv.Itoa(spec.Height))
	}
	first = append(first, w.Procs[0].args()...)
	cmds := []Command{first}
	// new-session -e fills the session environment, which every later pane
	// would inherit; the first pane already has its copy.
	for _, kv := range w.Procs[0].Env {
		key, _, _ := strings.Cut(kv, "=")
		cmds = append(cmds, Command{"set-environment", "-u", "-t", target, key})
	}
	cmds = append(cmds,
		Command{"set-option", "-t", target, OptManaged, "1"},
		Command{"set-option", "-t", target, OptProject, spec.Project},
		Command{"set-option", "-t", target, OptSandbox, spec.Sandbox},
		Command{"set-option", "-t", target, OptIsolation, spec.Isolation},
		Command{"set-option", "-t", target, OptLayout, w.Plan.Name},
	)
	cmds = append(cmds, paneOptions(target, w.Plan.Panes[0].Role, w.Procs[0])...)

	out, err := c.Batch(ctx, cmds...)
	built, parsed := parseCreated(out)
	if err != nil {
		// Without the -P output, new-session itself failed (for example with
		// ErrExists) and there is nothing of ours to remove.
		if !parsed {
			return Built{}, err
		}
		return Built{}, c.discard(ctx, Command{"kill-session", "-t", target}, err)
	}
	if !parsed {
		return Built{}, c.discard(ctx, Command{"kill-session", "-t", target}, fmt.Errorf("tmux new-session: unexpected output %q", out))
	}
	if err := c.split(ctx, &built, w, nil); err != nil {
		return Built{}, c.discard(ctx, Command{"kill-session", "-t", target}, err)
	}
	return built, nil
}

// AddWindow adds a window laid out by w to an existing session. With detached
// set, the session's current window stays selected.
func (c *Client) AddWindow(ctx context.Context, sessionName string, w WindowSpec, detached bool) (Built, error) {
	if err := session.Validate(sessionName); err != nil {
		return Built{}, err
	}
	if err := w.validate(); err != nil {
		return Built{}, err
	}
	target := ExactSession(sessionName)
	// Without -d the new window becomes current, so the session target
	// resolves to its first pane for the options below; -d would tag the pane
	// of the previously current window instead.
	first := Command{"new-window", "-t", target}
	first = append(first, w.createArgs()...)
	first = append(first, w.Procs[0].args()...)
	cmds := append([]Command{first}, paneOptions(target, w.Plan.Panes[0].Role, w.Procs[0])...)

	out, err := c.Batch(ctx, cmds...)
	built, parsed := parseCreated(out)
	if err != nil || !parsed {
		if err == nil {
			err = fmt.Errorf("tmux new-window: unexpected output %q", out)
		}
		if parsed {
			return Built{}, c.discard(ctx, Command{"kill-window", "-t", built.WindowID}, err)
		}
		return Built{}, err
	}
	var last []Command
	if detached {
		last = []Command{{"last-window", "-t", target}}
	}
	if err := c.split(ctx, &built, w, last); err != nil {
		return Built{}, c.discard(ctx, Command{"kill-window", "-t", built.WindowID}, err)
	}
	return built, nil
}

// split creates the remaining panes of w, one invocation each because every
// split names its parent by the id an earlier split printed, then focuses the
// plan's focus pane and runs tail in the same invocation as the last step.
func (c *Client) split(ctx context.Context, built *Built, w WindowSpec, tail []Command) error {
	for i := 1; i < len(w.Plan.Panes); i++ {
		pane := w.Plan.Panes[i]
		dir := "-v"
		if pane.Split == layout.SplitRight {
			dir = "-h"
		}
		cmd := Command{
			"split-window", "-t", built.Panes[pane.Parent], dir,
			"-l", strconv.Itoa(pane.Size) + "%",
			"-c", FormatEscape(w.Dir),
			"-P", "-F", "#{pane_id}",
		}
		cmd = append(cmd, w.Procs[i].args()...)
		// split-window without -d makes the new pane active, so the window
		// target resolves to it.
		cmds := append([]Command{cmd}, paneOptions(built.WindowID, pane.Role, w.Procs[i])...)
		out, err := c.Batch(ctx, cmds...)
		if err != nil {
			return err
		}
		id := strings.TrimSpace(out)
		if !isPaneID(id) {
			return fmt.Errorf("tmux split-window: unexpected output %q", out)
		}
		built.Panes = append(built.Panes, id)
	}
	// The rail is a number of cells, not a share: a share of a wide client
	// would give it a column far past anything it has to draw. The split that
	// made it could only ask for a share, so the width the plan carries is set
	// here.
	var cmds []Command
	for i, pane := range w.Plan.Panes {
		if pane.Role == layout.RoleAgents {
			cmds = append(cmds, Command{"resize-pane", "-t", built.Panes[i], "-x", strconv.Itoa(layout.RailCells(w.Plan.RailWidth))})
		}
	}
	cmds = append(cmds, Command{"select-pane", "-t", built.Panes[w.Plan.Focus]})
	_, err := c.Batch(ctx, append(cmds, tail...)...)
	return err
}

// discard runs a cleanup command for a failed build with its own deadline and
// returns the original error, joined with the cleanup error when that failed
// for any reason other than the target being gone already.
func (c *Client) discard(ctx context.Context, cleanup Command, cause error) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := c.Batch(cctx, cleanup); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNoServer) {
		return errors.Join(cause, fmt.Errorf("cleanup: %w", err))
	}
	return cause
}

// createArgs are the new-session and new-window flags shared by both.
func (w WindowSpec) createArgs() []string {
	args := []string{"-c", FormatEscape(w.Dir), "-P", "-F", strings.Join(createdFields, fieldSep)}
	if w.Name != "" {
		// Window names are format-expanded when the window is created.
		args = append(args, "-n", FormatEscape(w.Name))
	}
	return args
}

// args renders the -e flags and the command. tmux passes a single command
// argument to the default shell and executes several directly, so a
// one-element Argv is wrapped in a POSIX sh exec that keeps it one word.
func (p PaneProcess) args() []string {
	var args []string
	for _, kv := range p.Env {
		args = append(args, "-e", kv)
	}
	switch {
	case len(p.Argv) > 1:
		args = append(append(args, "--"), p.Argv...)
	case len(p.Argv) == 1:
		args = append(args, "--", "/bin/sh", "-c", `exec "$0"`, p.Argv[0])
	case p.Shell != "":
		args = append(args, "--", p.Shell)
	}
	return args
}

// paneOptions tag the pane target resolves to with its role and the process's
// user options, in name order. A pane running a program this workspace chose
// stays open after a failed exit so the reason remains readable: one that
// closes on the way up takes its own error message with it, and the layout
// then looks as if the pane had never been asked for. A shell pane is the
// user's own and keeps tmux's behavior, so a prompt left with a failing status
// does not leave a pane to close by hand.
func paneOptions(target string, role layout.Role, p PaneProcess) []Command {
	cmds := []Command{{"set-option", "-p", "-t", target, OptRole, string(role)}}
	if keepsFailure(role) {
		cmds = append(cmds, Command{"set-option", "-p", "-t", target, "remain-on-exit", "failed"})
	}
	for _, name := range slices.Sorted(maps.Keys(p.Options)) {
		cmds = append(cmds, Command{"set-option", "-p", "-t", target, name, p.Options[name]})
	}
	return cmds
}

// keepsFailure reports whether a dead pane of this role is kept on screen.
func keepsFailure(role layout.Role) bool {
	switch role {
	case layout.RoleClaude, layout.RoleChanges, layout.RoleReview, layout.RoleCommand, layout.RoleAgents:
		return true
	default:
		return false
	}
}

func parseCreated(out string) (Built, bool) {
	f := SplitFields(strings.TrimSpace(out))
	if len(f) != len(createdFields) || !strings.HasPrefix(f[0], "$") || !strings.HasPrefix(f[1], "@") || !isPaneID(f[2]) {
		return Built{}, false
	}
	return Built{SessionID: f[0], WindowID: f[1], Panes: []string{f[2]}}, true
}

func isPaneID(s string) bool {
	if len(s) < 2 || s[0] != '%' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func (s WorkspaceSpec) validate() error {
	if err := session.Validate(s.Session); err != nil {
		return err
	}
	if s.Width < 0 || s.Height < 0 {
		return fmt.Errorf("workspace size %dx%d is negative", s.Width, s.Height)
	}
	for _, v := range []string{s.Project, s.Sandbox, s.Isolation} {
		if strings.ContainsRune(v, 0) {
			return errors.New("workspace options must not contain NUL")
		}
	}
	return s.Window.validate()
}

func (w WindowSpec) validate() error {
	if err := w.Plan.Validate(); err != nil {
		return err
	}
	if len(w.Procs) != len(w.Plan.Panes) {
		return fmt.Errorf("layout %q has %d panes but %d processes", w.Plan.Name, len(w.Plan.Panes), len(w.Procs))
	}
	if !filepath.IsAbs(w.Dir) {
		return fmt.Errorf("workspace directory %q is not absolute", w.Dir)
	}
	if strings.ContainsRune(w.Dir, 0) || strings.ContainsRune(w.Name, 0) {
		return errors.New("workspace directory and window name must not contain NUL")
	}
	for i, p := range w.Procs {
		if err := p.validate(); err != nil {
			return fmt.Errorf("pane %d: %w", i+1, err)
		}
	}
	return nil
}

func (p PaneProcess) validate() error {
	if len(p.Argv) > 0 && p.Shell != "" {
		return errors.New("a pane runs either Argv or Shell, not both")
	}
	if len(p.Argv) > 0 && p.Argv[0] == "" {
		return errors.New("empty program name")
	}
	for _, a := range p.Argv {
		if strings.ContainsRune(a, 0) {
			return errors.New("argument contains NUL")
		}
	}
	if strings.ContainsRune(p.Shell, 0) {
		return errors.New("shell command contains NUL")
	}
	for name, value := range p.Options {
		switch {
		case name == OptRole:
			// paneOptions writes the role itself, from the layout.
			return fmt.Errorf("pane option %q is not a user option name", name)
		case isUserOption(name):
		case paneOptionValues[name] != nil:
			if !slices.Contains(paneOptionValues[name], value) {
				return fmt.Errorf("pane option %s does not take %q", name, value)
			}
		default:
			return fmt.Errorf("pane option %q is not a user option name", name)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("pane option %s contains NUL", name)
		}
	}
	for _, kv := range p.Env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !isEnvKey(key) {
			return fmt.Errorf("environment entry %q is not KEY=VALUE", kv)
		}
		if strings.ContainsRune(kv, 0) {
			return fmt.Errorf("environment entry for %s contains NUL", key)
		}
	}
	return nil
}

// paneOptionValues are the tmux options a pane may carry besides its user
// options, and the values each of them takes. A caller hands the options over
// as a map, so a name it could choose freely would let it set any option on
// the pane, and a pane option that a window or the server inherits from would
// reach further than the pane.
var paneOptionValues = map[string][]string{OptPassthrough: {"on", "off", "all"}}

// isUserOption reports whether s is a tmux user option name: @ followed by
// letters, digits, '_' or '-'.
func isUserOption(s string) bool {
	if len(s) < 2 || s[0] != '@' {
		return false
	}
	for _, c := range s[1:] {
		if c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func isEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
