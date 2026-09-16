package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// SplitRequest describes a pane added next to a workspace pane.
type SplitRequest struct {
	Target WindowTarget
	// Down stacks the new pane below; otherwise it opens to the right.
	Down bool
	// Role is one of SplitRoles; empty is a shell.
	Role layout.Role
}

// SplitResult reports the pane a split created.
type SplitResult struct {
	Pane     string
	Session  string
	Warnings []string
}

// SplitRoles lists the roles a split pane can take.
func SplitRoles() []string {
	return []string{string(layout.RoleShell), string(layout.RoleClaude), string(layout.RoleChanges)}
}

// SplitPane splits the target pane and starts the role's process in the new
// pane: a shell in the pane's current directory, Claude with per-launch
// settings or the live changes view in the project root.
func (s *Server) SplitPane(ctx context.Context, h Host, req SplitRequest) (SplitResult, error) {
	role := req.Role
	if role == "" {
		role = layout.RoleShell
	}
	if !slices.Contains(SplitRoles(), string(role)) {
		return SplitResult{}, fmt.Errorf("unknown pane role %q: choose one of %s", role, strings.Join(SplitRoles(), ", "))
	}
	spot, err := s.wsLocate(ctx, h, req.Target)
	if err != nil {
		return SplitResult{}, err
	}
	name := spot.pane.Session
	dir := spot.root
	var lp launchPlan
	switch role {
	case layout.RoleShell:
		if filepath.IsAbs(spot.pane.Path) {
			dir = spot.pane.Path
		}
	case layout.RoleClaude:
		if lp, err = s.prepareLaunch(h, spot.root, name, spot.launch(LaunchOptions{})); err != nil {
			return SplitResult{}, err
		}
	}
	procs, err := lp.paneProcs(h, layout.Plan{Panes: []layout.Pane{{Role: role}}}, spot.root, name)
	if err != nil {
		return SplitResult{}, err
	}
	id, err := s.Client.SplitPane(ctx, tmux.SplitSpec{
		Pane: spot.pane.ID, Window: spot.pane.WindowID, Down: req.Down, Dir: dir, Role: role, Proc: procs[0],
	})
	if err != nil {
		return SplitResult{}, workspaceErr(name, err)
	}
	res := SplitResult{Pane: id, Session: name}
	if role == layout.RoleClaude {
		if err := s.pruneSettings(ctx, lp.settings); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("remove old Claude settings files: %v", err))
		}
	}
	return res, nil
}
