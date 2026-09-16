package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// WindowTarget names the workspace a window or pane command acts on. Pane
// wins over Session; with neither, the pane the command runs in is used when
// it belongs to the lyna-tmux server.
type WindowTarget struct {
	// Pane is a pane id such as %3.
	Pane string
	// Session is a workspace name; its current window's active pane is used.
	Session string
}

// ErrNoTarget reports a window command run outside a pane of the lyna-tmux
// server without naming a pane or workspace.
var ErrNoTarget = errors.New("not running in a lyna-tmux workspace pane")

// WindowResult reports a window a command opened.
type WindowResult struct {
	Session string
	Built   tmux.Built
	// Existing is set when the workspace already had a window of that name,
	// which is selected instead of a second one. Only Built.WindowID is then
	// filled in: the panes of a window built earlier are not described again.
	Existing bool
	// Warnings describe housekeeping that failed without affecting the window.
	Warnings []string
}

// wsSpot is the pane a window command resolved to and its project root.
type wsSpot struct {
	pane tmux.PaneContext
	root string
}

// launch completes the options of a command run inside a workspace with the
// sandbox the workspace recorded when it was opened, so a pane added later
// runs like the panes already there. The configuration is read only for a
// session that recorded nothing, and an option the command asked for wins.
func (w wsSpot) launch(o LaunchOptions) LaunchOptions {
	o.Sandbox = pick(o.Sandbox, w.pane.Sandbox)
	o.Isolation = pick(o.Isolation, w.pane.Isolation)
	return o
}

// wsLocate resolves a target to a pane of the lyna-tmux server and the
// project root of its workspace: the session's @lt_project, or the root of
// the pane's directory for a session without one.
func (s *Server) wsLocate(ctx context.Context, h Host, t WindowTarget) (wsSpot, error) {
	var target string
	switch {
	case t.Pane != "":
		if !tmux.ValidPaneID(t.Pane) {
			return wsSpot{}, fmt.Errorf("%q is not a pane id such as %%3", t.Pane)
		}
		target = t.Pane
	case t.Session != "":
		if err := session.Validate(t.Session); err != nil {
			return wsSpot{}, err
		}
		target = tmux.ExactSession(t.Session)
	case s.Inside(h) && tmux.ValidPaneID(h.Getenv("TMUX_PANE")):
		target = h.Getenv("TMUX_PANE")
	default:
		return wsSpot{}, ErrNoTarget
	}
	pane, err := s.Client.DescribePane(ctx, target)
	if errors.Is(err, tmux.ErrNotFound) || errors.Is(err, tmux.ErrNoServer) {
		if t.Pane == "" && t.Session != "" {
			return wsSpot{}, fmt.Errorf("%w: %s", ErrNoWorkspace, t.Session)
		}
		return wsSpot{}, fmt.Errorf("%w: no pane %s on the lyna-tmux server", ErrNoWorkspace, target)
	}
	if err != nil {
		return wsSpot{}, err
	}
	root := pane.Project
	if !filepath.IsAbs(root) {
		if root, err = projectRoot(pane.Path); err != nil {
			return wsSpot{}, err
		}
	} else if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return wsSpot{}, fmt.Errorf("the project directory %s of workspace %s no longer exists", root, pane.Session)
	}
	return wsSpot{pane: pane, root: root}, nil
}

// wsOpenWindow adds a window laid out by plan to the workspace of spot, with
// the processes create would start for the same plan, and selects it. A
// window of that name already open is selected as it is: window names carry
// meaning here (the task and its worktree, the conversation picker, the
// layout), so a second one would run a second Claude on the same files.
func (s *Server) wsOpenWindow(ctx context.Context, h Host, spot wsSpot, window string, plan layout.Plan, o LaunchOptions) (WindowResult, error) {
	name := spot.pane.Session
	if window != "" {
		id, found, err := s.Client.FindWindow(ctx, name, window)
		if err != nil {
			return WindowResult{}, workspaceErr(name, err)
		}
		if found {
			if _, err := s.Client.Run(ctx, "select-window", "-t", id); err != nil {
				return WindowResult{}, workspaceErr(name, err)
			}
			return WindowResult{Session: name, Built: tmux.Built{WindowID: id}, Existing: true}, nil
		}
	}
	lp, err := s.prepareLaunch(h, spot.root, name, spot.launch(o))
	if err != nil {
		return WindowResult{}, err
	}
	procs, err := lp.paneProcs(h, plan, spot.root, name)
	if err != nil {
		return WindowResult{}, err
	}
	built, err := s.Client.AddWindow(ctx, name, tmux.WindowSpec{Name: window, Dir: spot.root, Plan: plan, Procs: procs}, false)
	if err != nil {
		return WindowResult{}, workspaceErr(name, err)
	}
	res := WindowResult{Session: name, Built: built}
	if err := s.pruneSettings(ctx, lp.settings); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("remove old Claude settings files: %v", err))
	}
	return res, nil
}

// TaskRequest describes a task window: Claude working in its own git
// worktree.
type TaskRequest struct {
	Target WindowTarget
	// Name names the window and the worktree.
	Name string
	// Prompt, when set, is the first prompt of the conversation.
	Prompt string
}

// taskPlan names the plan of a task window.
const taskPlan = "task"

// OpenTask opens a window named after the task running Claude in the
// worktree of that name, with per-launch settings.
func (s *Server) OpenTask(ctx context.Context, h Host, req TaskRequest) (WindowResult, error) {
	if err := layout.ValidateWorktree(req.Name); err != nil {
		return WindowResult{}, err
	}
	if err := taskCheckPrompt(req.Prompt); err != nil {
		return WindowResult{}, err
	}
	spot, err := s.wsLocate(ctx, h, req.Target)
	if err != nil {
		return WindowResult{}, err
	}
	plan := layout.Plan{Name: taskPlan, Panes: []layout.Pane{{Role: layout.RoleClaude, Worktree: req.Name}}}
	var o LaunchOptions
	if req.Prompt != "" {
		o.ExtraArgs = []string{req.Prompt}
	}
	return s.wsOpenWindow(ctx, h, spot, req.Name, plan, o)
}

// ErrPrompt reports a prompt claude would not read as a prompt.
var ErrPrompt = errors.New("invalid prompt")

// taskCheckPrompt refuses prompts claude parses as something else: a leading
// '-' makes an option, and a single word may name a claude subcommand (such
// as update or install), which would run instead of a session.
func taskCheckPrompt(p string) error {
	switch {
	case p == "":
		return nil
	case strings.HasPrefix(p, "-"):
		return fmt.Errorf("%w: it must not start with '-', which claude reads as an option", ErrPrompt)
	case !strings.ContainsFunc(p, unicode.IsSpace):
		return fmt.Errorf("%w: %q is one word, which claude may run as a subcommand; write the prompt as a sentence", ErrPrompt, p)
	}
	return nil
}
