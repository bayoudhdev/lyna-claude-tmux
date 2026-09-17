package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// PopupRequest identifies what a plugin mode key binding ran for: the pane
// the key was pressed in and the client that pressed it.
type PopupRequest struct {
	Pane   string
	Client string
}

// PopupResult reports what a popup launch did.
type PopupResult struct {
	// Session is the popup session.
	Session string
	// Created is set when the launch started the session.
	Created bool
	// Closed is set when the key was pressed inside a popup session: its
	// client was detached, which closes the popup, instead of nesting another.
	Closed bool
}

// Popup titles.
const (
	popupClaudeTitle = "claude"
	popupAgentsTitle = "agents"
)

// popupLook is the popup configuration on a server the user runs: the
// @claude_* options set there, else the lyna-tmux configuration. The server
// has no generated configuration, so nothing is read from @lt_* options.
type popupLook struct {
	prefix, width, height string
}

func (s *Server) popupLook(user tmux.PluginUserOptions) popupLook {
	return popupLook{
		prefix: pick(user.SessionPrefix, s.Config.Popup.SessionPrefix),
		width:  pick(user.PopupWidth, s.Config.Popup.Width),
		height: pick(user.PopupHeight, s.Config.Popup.Height),
	}
}

// popupLaunchOptions turns the @claude_command and @claude_args options of the
// user's server into the launch of a popup session. A set option replaces its
// configuration counterpart, the rule the look options follow: the plugin
// this project derives from knew only these two options, so a user who
// migrated set them as the whole launch, and appending would run those
// arguments together with claude.args from a configuration file they never
// edited, with no way to take one back from tmux. The command is checked and
// resolved the way claude.command is, so an option naming a binary that does
// not exist fails here, naming the option, instead of falling back to the
// configured one. The arguments are split without a shell: a user wrote them
// for one, but nothing here is expanded.
func popupLaunchOptions(h Host, user tmux.PluginUserOptions) (LaunchOptions, error) {
	var o LaunchOptions
	if msg := config.ClaudeCommandProblem(user.Command); msg != "" {
		return LaunchOptions{}, fmt.Errorf("%s %s (got %q)", tmux.OptClaudeCommand, msg, user.Command)
	}
	if user.Command != "" {
		path, err := claude.ResolveCommand(user.Command, h.lookPath(), h.Getenv, h.Home, claude.IsExecutable)
		if err != nil {
			return LaunchOptions{}, fmt.Errorf("%s: %w", tmux.OptClaudeCommand, err)
		}
		o.Command = path
	}
	if !config.CleanText(user.Args) {
		return LaunchOptions{}, fmt.Errorf("%s must be a single-line value without control characters (got %q)", tmux.OptClaudeArgs, user.Args)
	}
	args, err := tmux.ShellSplit(user.Args)
	if err != nil {
		return LaunchOptions{}, fmt.Errorf("%s: %w", tmux.OptClaudeArgs, err)
	}
	// No words, from an unset option or one holding only blanks, is nil: the
	// configuration wins, as for an empty look option.
	o.Args = args
	return o, nil
}

func (r PopupRequest) popupValidate() error {
	if !tmux.ValidPaneID(r.Pane) {
		return fmt.Errorf("pane %q is not a pane id such as %%3", r.Pane)
	}
	if r.Client == "" || strings.ContainsFunc(r.Client, unicode.IsControl) {
		return fmt.Errorf("client %q is not a tmux client name", r.Client)
	}
	return nil
}

// popupPane reads the directory, window and session of the pane a key was
// pressed in from tmux, never from a shell word.
func (s *Server) popupPane(ctx context.Context, pane string) (tmux.PaneContext, error) {
	pc, err := s.Client.DescribePane(ctx, pane)
	if errors.Is(err, tmux.ErrNotFound) {
		return tmux.PaneContext{}, fmt.Errorf("pane %s: %w", pane, err)
	}
	return pc, err
}

// PopupLaunch shows the Claude session of the pane's directory in a popup on
// the client, starting it with per-launch settings when it is not running,
// and records the window it was opened from. Pressed inside a popup session,
// it detaches that client instead, which closes the popup and keeps Claude
// running. It returns when the popup closes.
func (s *Server) PopupLaunch(ctx context.Context, h Host, req PopupRequest) (PopupResult, error) {
	if err := req.popupValidate(); err != nil {
		return PopupResult{}, err
	}
	user, err := s.Client.ReadPluginOptions(ctx)
	if err != nil {
		return PopupResult{}, err
	}
	look := s.popupLook(user)
	pane, err := s.popupPane(ctx, req.Pane)
	if err != nil {
		return PopupResult{}, err
	}
	if session.IsPopup(look.prefix, pane.Session) {
		_, err := s.Client.Run(ctx, "detach-client", "-t", req.Client)
		return PopupResult{Session: pane.Session, Closed: true}, err
	}
	dir := pane.Path
	if info, err := os.Stat(dir); !filepath.IsAbs(dir) || err != nil || !info.IsDir() {
		return PopupResult{}, fmt.Errorf("the pane directory %q no longer exists", dir)
	}
	name := session.PopupName(look.prefix, dir)
	if err := session.Validate(name); err != nil {
		return PopupResult{}, fmt.Errorf("popup session prefix %q: %w", look.prefix, err)
	}
	res := PopupResult{Session: name}
	running, err := s.Client.HasSession(ctx, name)
	if err != nil {
		return PopupResult{}, err
	}
	if !running {
		if res.Created, err = s.popupStart(ctx, h, name, dir, user); err != nil {
			return PopupResult{}, err
		}
	}
	bin, err := popupTmuxPath(h, s.Client.Bin())
	if err != nil {
		return res, err
	}
	target := tmux.ExactSession(name)
	popup, err := tmux.PopupSpec{
		Client: req.Client, Width: look.width, Height: look.height, Title: popupClaudeTitle,
		Argv: []string{bin, "-S", s.Client.Socket().Path, "attach-session", "-t", target},
	}.Command()
	if err != nil {
		return res, err
	}
	// The popup's attach is not refused as nested: tmux only refuses clients
	// whose terminal is one of its own panes, and a popup is not a pane.
	_, err = s.Client.Batch(ctx,
		tmux.Command{"set-option", "-t", target, tmux.OptOrigin, pane.WindowID},
		tmux.Command{"set-option", "-t", target, tmux.OptClaudeOrigin, pane.WindowID},
		popup,
	)
	return res, err
}

// popupStart creates the detached popup session running Claude in dir, as the
// user's @claude_command and @claude_args options say. It reports false
// without an error when a concurrent launch created it first.
func (s *Server) popupStart(ctx context.Context, h Host, name, dir string, user tmux.PluginUserOptions) (bool, error) {
	root, err := session.ProjectRoot(dir)
	if err != nil {
		return false, err
	}
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		return false, err
	}
	o, err := popupLaunchOptions(h, user)
	if err != nil {
		return false, err
	}
	lp, err := s.prepareLaunch(h, root, name, o)
	if err != nil {
		return false, err
	}
	procs, err := lp.paneProcs(h, plan, root, name)
	if err != nil {
		return false, err
	}
	_, err = s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: name, Project: root, Sandbox: string(lp.sandbox.Profile), Isolation: string(lp.sandbox.Isolation),
		Window: tmux.WindowSpec{Name: mainWindowName, Dir: dir, Plan: plan, Procs: procs},
	})
	if errors.Is(err, tmux.ErrExists) {
		return false, nil
	}
	return err == nil, err
}

// PopupAgents opens the agents picker in a popup on the client, started in
// the pane's directory.
func (s *Server) PopupAgents(ctx context.Context, h Host, req PopupRequest) error {
	if err := req.popupValidate(); err != nil {
		return err
	}
	user, err := s.Client.ReadPluginOptions(ctx)
	if err != nil {
		return err
	}
	look := s.popupLook(user)
	pane, err := s.popupPane(ctx, req.Pane)
	if err != nil {
		return err
	}
	spec := tmux.PopupSpec{Client: req.Client, Width: look.width, Height: look.height, Title: popupAgentsTitle, Argv: []string{h.Exe, "agents", "--popup"}}
	if filepath.IsAbs(pane.Path) {
		spec.Dir = pane.Path
	}
	popup, err := spec.Command()
	if err != nil {
		return err
	}
	_, err = s.Client.Batch(ctx, popup)
	return err
}

// popupTmuxPath returns the absolute tmux path the popup runs: the popup
// starts with the session's environment, whose PATH may differ.
func popupTmuxPath(h Host, bin string) (string, error) {
	if filepath.IsAbs(bin) {
		return bin, nil
	}
	path, err := h.lookPath()(bin)
	if err != nil {
		return "", fmt.Errorf("find %s: %w", bin, err)
	}
	return filepath.Abs(path)
}
