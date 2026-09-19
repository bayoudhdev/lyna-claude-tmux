package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// mainWindowName names the first window of a workspace.
const mainWindowName = "claude"

// CreateRequest describes a workspace to open.
type CreateRequest struct {
	// Dir is the directory to work in; its project root (the nearest
	// repository root) identifies the workspace.
	Dir string
	// Name is the session name; empty derives it from the project root.
	Name string
	// Layout is a built-in or custom layout name. Empty opens the team layout
	// for a launch with agent teams turned on, and the configured one
	// otherwise.
	Layout string
	// Width and Height are the terminal size, used by the auto layout.
	Width, Height int
	Launch        LaunchOptions
}

// CreateResult reports the workspace a create request resolved to.
type CreateResult struct {
	Name    string
	Project string
	// Existing is set when the project already had a running workspace, which
	// is returned unchanged instead of starting a second one.
	Existing bool
	Built    tmux.Built
	// Warnings describe housekeeping that failed without affecting the
	// workspace.
	Warnings []string
}

// ErrNameTaken reports a requested session name that another project or a
// session that is not a workspace already uses.
var ErrNameTaken = errors.New("workspace name is taken")

// createAttempts bounds retries when a concurrent create takes the chosen
// session name first.
const createAttempts = 3

// Create opens the workspace for a project, or returns the running one. A
// name that is already used by another project is an error.
func (s *Server) Create(ctx context.Context, h Host, req CreateRequest) (CreateResult, error) {
	root, err := projectRoot(req.Dir)
	if err != nil {
		return CreateResult{}, err
	}
	running, err := s.Sync(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	for attempt := 1; ; attempt++ {
		res, err := s.createOnce(ctx, h, req, root, running)
		if !errors.Is(err, tmux.ErrExists) || attempt == createAttempts {
			return res, err
		}
		// Another create won the name: look again, it may be this project.
		running = true
	}
}

func (s *Server) createOnce(ctx context.Context, h Host, req CreateRequest, root string, running bool) (CreateResult, error) {
	var sessions []tmux.Session
	if running {
		var err error
		if sessions, err = s.Sessions(ctx); err != nil {
			return CreateResult{}, err
		}
	}
	name, existing, err := s.resolveName(req.Name, root, sessions)
	if err != nil {
		return CreateResult{}, err
	}
	res := CreateResult{Name: name, Project: root, Existing: existing}
	if existing {
		return res, nil
	}
	plan, err := s.plan(req, name)
	if err != nil {
		return CreateResult{}, err
	}
	lp, err := s.prepareLaunch(h, root, name, req.Launch)
	if err != nil {
		return CreateResult{}, err
	}
	procs, err := lp.paneProcs(h, plan, root, name)
	if err != nil {
		return CreateResult{}, err
	}
	res.Built, err = s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: name, Project: root, Sandbox: string(lp.base.Sandbox.Profile), Isolation: string(lp.base.Sandbox.Isolation),
		Width: req.Width, Height: req.Height,
		Window: tmux.WindowSpec{Name: mainWindowName, Dir: root, Plan: plan, Procs: procs},
	})
	if err != nil {
		return CreateResult{}, err
	}
	if !running {
		if err := s.MarkLoaded(ctx); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("record the loaded configuration: %v", err))
		}
	}
	if err := s.pruneSettings(ctx, lp.settings); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("remove old Claude settings files: %v", err))
	}
	if warning := trustWarning(h, root); warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}
	return res, nil
}

// maxClaudeState bounds the read of the Claude Code state file, which keeps a
// per-project history and grows with use.
const maxClaudeState = 32 << 20

// trustWarning describes an untrusted project, or is empty when there is
// nothing to say. Claude Code asks about a directory the first time it starts
// there and runs no hooks until the prompt is accepted, so the workspace opens
// on that question and its status bar shows no agent state: saying so here
// turns a puzzling first screen into an expected one. A state file that cannot
// be read is not worth a warning, because the answer would be a guess.
func trustWarning(h Host, root string) string {
	if h.Home == "" {
		return ""
	}
	data, err := fsx.ReadFileLimited(claudecfg.ConfigFile(xdg.ClaudeHome(h.Getenv, h.Home), h.Home), maxClaudeState)
	if err != nil {
		return ""
	}
	trust, err := claudecfg.ProjectTrust(data, root)
	if err != nil || trust == claudecfg.TrustAccepted {
		return ""
	}
	return "Claude Code has not been trusted in " + root + " yet: it opens with its own trust question," +
		" and until that is accepted it runs no hooks, so the status bar shows no agent state"
}

// resolveName picks the session for a project: the requested name, the running
// workspace of the project, or a fresh name derived from the project directory.
func (s *Server) resolveName(requested, root string, sessions []tmux.Session) (name string, existing bool, err error) {
	byName := make(map[string]tmux.Session, len(sessions))
	for _, x := range sessions {
		byName[x.Name] = x
	}
	if requested != "" {
		if err := session.Validate(requested); err != nil {
			return "", false, err
		}
		x, ok := byName[requested]
		switch {
		case !ok:
			return requested, false, nil
		case x.Managed && x.Project == root:
			return requested, true, nil
		default:
			return "", false, fmt.Errorf("%w: %s is used by %s; pick another name with --name", ErrNameTaken, requested, describeProject(x))
		}
	}
	for _, x := range sessions {
		if x.Managed && x.Project == root && !session.IsPopup(s.Config.Popup.SessionPrefix, x.Name) {
			return x.Name, true, nil
		}
	}
	// Sanitize always returns a usable name, session.DefaultName for a
	// directory without a single usable character.
	base := session.Sanitize(filepath.Base(root))
	return session.Unique(base, func(n string) bool { _, ok := byName[n]; return ok }), false, nil
}

func describeProject(x tmux.Session) string {
	if x.Project == "" {
		return "a session that is not a workspace"
	}
	return "the workspace of " + x.Project
}

// plan builds the window layout for a request. The rail is asked for by the
// configuration rather than by the layout, so a workspace that always carries
// one gets it whichever layout it opens, and the layout that carries it
// already keeps the one it has.
func (s *Server) plan(req CreateRequest, name string) (layout.Plan, error) {
	p, err := s.layoutPlan(req, name)
	if err != nil {
		return layout.Plan{}, err
	}
	if s.Config.UI.AgentsSidebar != config.SidebarAlways {
		return p, nil
	}
	p = layout.WithRail(p, req.Width)
	return p, p.Validate()
}

// layoutName is the layout a request opens: the one it names, then the team
// layout for a workspace started to run a team, then the configured default.
func (s *Server) layoutName(req CreateRequest) string {
	switch {
	case req.Layout != "":
		return req.Layout
	case req.Launch.Teams:
		return layout.Team
	}
	return s.Config.Workspace.Layout
}

// layoutPlan builds the plan of the layout a request opens.
func (s *Server) layoutPlan(req CreateRequest, name string) (layout.Plan, error) {
	layoutName := s.layoutName(req)
	if custom, ok := s.Config.Layouts[layoutName]; ok {
		return layout.Custom(layoutName, name, customPanes(custom))
	}
	if !layout.IsBuiltin(layoutName) {
		names := append(layout.Names(), slices.Sorted(maps.Keys(s.Config.Layouts))...)
		return layout.Plan{}, fmt.Errorf("%w %q: choose one of %v", layout.ErrUnknown, layoutName, names)
	}
	return layout.Builtin(layout.Resolve(layoutName, req.Width, req.Height), layout.Options{
		SplitRatio: s.Config.Workspace.SplitRatio,
		Session:    name,
		Width:      req.Width,
		Height:     req.Height,
	})
}

func customPanes(l config.Layout) []layout.CustomPane {
	panes := make([]layout.CustomPane, len(l.Panes))
	for i, p := range l.Panes {
		panes[i] = layout.CustomPane{Role: p.Role, Split: p.Split, Size: p.Size, Parent: p.Parent, Command: p.Command, Worktree: p.Worktree}
	}
	return panes
}

// projectRoot validates the working directory and returns its project root.
func projectRoot(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("no working directory given")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return session.ProjectRoot(dir)
}
