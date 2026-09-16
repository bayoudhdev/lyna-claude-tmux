package app

import (
	"context"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

// resumeWindow names the window a resume opens.
const resumeWindow = "resume"

// OpenResume opens a window in the workspace running Claude's conversation
// picker, so a past conversation continues inside the workspace with
// per-launch settings.
func (s *Server) OpenResume(ctx context.Context, h Host, t WindowTarget) (WindowResult, error) {
	spot, err := s.wsLocate(ctx, h, t)
	if err != nil {
		return WindowResult{}, err
	}
	plan := layout.Plan{Name: resumeWindow, Panes: []layout.Pane{{Role: layout.RoleClaude}}}
	return s.wsOpenWindow(ctx, h, spot, resumeWindow, plan, LaunchOptions{ExtraArgs: []string{"--resume"}})
}
