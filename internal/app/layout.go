package app

import (
	"context"
	"errors"
)

// LayoutRequest describes a window laid out by a built-in or custom layout.
type LayoutRequest struct {
	Target WindowTarget
	Layout string
}

// OpenLayout opens a new window in the workspace laid out as req.Layout, with
// the processes create would start for it, and selects it. The windows
// already open are left as they are. The auto layout resolves for the size
// of the target pane's window.
func (s *Server) OpenLayout(ctx context.Context, h Host, req LayoutRequest) (WindowResult, error) {
	if req.Layout == "" {
		return WindowResult{}, errors.New("no layout given")
	}
	spot, err := s.wsLocate(ctx, h, req.Target)
	if err != nil {
		return WindowResult{}, err
	}
	plan, err := s.plan(CreateRequest{Layout: req.Layout, Width: spot.pane.WindowWidth, Height: spot.pane.WindowHeight}, spot.pane.Session)
	if err != nil {
		return WindowResult{}, err
	}
	return s.wsOpenWindow(ctx, h, spot, plan.Name, plan, LaunchOptions{})
}
