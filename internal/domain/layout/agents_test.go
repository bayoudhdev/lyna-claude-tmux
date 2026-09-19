package layout_test

import (
	"slices"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

func TestPlaceAgent(t *testing.T) {
	// A wide client: two columns of a hundred cells and room to stack.
	wide := layout.AgentWindow{Width: 240, Height: 60, Teammates: 1, Max: layout.DefaultAgentPanes}
	cases := []struct {
		name   string
		window layout.AgentWindow
		want   layout.Placement
	}{
		{name: "the first teammate of a wide window", window: wide, want: layout.PlaceHere},
		{
			name:   "the third teammate the workspace allows",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 3, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "one teammate past what the workspace allows",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 4, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "a workspace that allows more of them",
			window: layout.AgentWindow{Width: 240, Height: 90, Teammates: 4, Max: 6},
			want:   layout.PlaceHere,
		},
		{
			name:   "a workspace that allows none of them",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 1},
			want:   layout.PlaceWindow,
		},
		{
			name:   "a window the user has a shell in",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 1, Others: 1, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "a window too narrow for two readable columns",
			window: layout.AgentWindow{Width: 130, Height: 60, Teammates: 1, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "the widest window that is still too narrow",
			window: layout.AgentWindow{Width: 133, Height: 60, Teammates: 1, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "the narrowest window that fits",
			window: layout.AgentWindow{Width: 134, Height: 60, Teammates: 1, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "a window too short to stack three of them",
			window: layout.AgentWindow{Width: 240, Height: 40, Teammates: 3, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "the shortest window that stacks three of them",
			window: layout.AgentWindow{Width: 240, Height: 44, Teammates: 3, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "a size nobody measured",
			window: layout.AgentWindow{Teammates: 2, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "a height nobody measured",
			window: layout.AgentWindow{Width: 240, Teammates: 2, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "a size nobody measured, past what the workspace allows",
			window: layout.AgentWindow{Teammates: 5, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "a window with no teammate in it",
			window: layout.AgentWindow{Width: 240, Height: 60, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "the rail is not a pane of the user's",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 1, Rail: layout.RailWidth, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "the lead stacks with the teammates beside the rail",
			window: layout.AgentWindow{Width: 240, Height: 60, Teammates: 3, Rail: layout.RailWidth, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceHere,
		},
		{
			name:   "a window too short to stack the lead and three teammates",
			window: layout.AgentWindow{Width: 240, Height: 56, Teammates: 3, Rail: layout.RailWidth, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
		{
			name:   "a rail wide enough to leave no room beside it",
			window: layout.AgentWindow{Width: 140, Height: 60, Teammates: 1, Rail: 60, Max: layout.DefaultAgentPanes},
			want:   layout.PlaceWindow,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := layout.PlaceAgent(tc.window); got != tc.want {
				t.Fatalf("PlaceAgent(%+v) = %q, want %q", tc.window, got, tc.want)
			}
		})
	}
}

// FuzzPlaceAgent holds the two properties the policy is there for: a window
// that is not the lead's alone is never reflowed for an agent, and a teammate
// is never left in a pane too small to read.
func FuzzPlaceAgent(f *testing.F) {
	f.Add(240, 60, 1, 0, 0, 0)
	f.Add(80, 24, 2, 1, 3, 0)
	f.Add(0, 0, 9, 0, 2, 0)
	f.Add(240, 60, 3, 0, 3, layout.RailWidth)
	f.Fuzz(func(t *testing.T, width, height, teammates, others, maxPanes, rail int) {
		w := layout.AgentWindow{Width: width, Height: height, Teammates: teammates, Others: others, Max: maxPanes, Rail: rail}
		got := layout.PlaceAgent(w)
		if got != layout.PlaceHere && got != layout.PlaceWindow {
			t.Fatalf("PlaceAgent(%+v) = %q", w, got)
		}
		if got == layout.PlaceHere && (others > 0 || teammates > maxPanes) {
			t.Fatalf("PlaceAgent(%+v) keeps a teammate the workspace has no room for", w)
		}
		if got != layout.PlaceHere || width <= 0 || height <= 0 {
			return
		}
		column, stacked := width-width*layout.AgentLeadRatio/100-1, teammates
		if rail > 0 {
			column, stacked = width-rail-1, teammates+1
		}
		each := (height - (stacked - 1)) / stacked
		if column < layout.MinAgentWidth || each < layout.MinAgentHeight {
			t.Fatalf("PlaceAgent(%+v) leaves a teammate in %dx%d", w, column, each)
		}
	})
}

// TestRailCells holds a rail to the widths it is drawn at, and gives a caller
// that was handed no width the default one.
func TestRailCells(t *testing.T) {
	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "no width reached the caller", width: 0, want: layout.RailWidth},
		{name: "a width below nothing", width: -3, want: layout.RailWidth},
		{name: "a cell narrower than the narrowest rail", width: layout.MinRailWidth - 1, want: layout.MinRailWidth},
		{name: "the narrowest rail", width: layout.MinRailWidth, want: layout.MinRailWidth},
		{name: "the default", width: layout.RailWidth, want: layout.RailWidth},
		{name: "a wider rail", width: 45, want: 45},
		{name: "the widest rail", width: layout.MaxRailWidth, want: layout.MaxRailWidth},
		{name: "a cell wider than the widest rail", width: layout.MaxRailWidth + 1, want: layout.MaxRailWidth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := layout.RailCells(tc.width); got != tc.want {
				t.Fatalf("RailCells(%d) = %d, want %d", tc.width, got, tc.want)
			}
		})
	}
}

// TestTeamLeadShare keeps the rail the width it asks for whatever the client
// is, and leaves both panes something to draw in when it cannot have it.
func TestTeamLeadShare(t *testing.T) {
	cases := []struct {
		name        string
		width, rail int
		want        int
	}{
		{name: "a client nobody measured", want: 86},
		{name: "a wide client keeps the rail a tenth", width: 400, want: 100 - layout.MinRailShare},
		{name: "a client a team is worked on", width: 200, want: 86},
		{name: "a client that rounds the rail up", width: 120, want: 76},
		{name: "a narrow client keeps the lead a tenth", width: 30, want: layout.MinRailShare},
		{name: "the default asked for by name", width: 200, rail: layout.RailWidth, want: 86},
		{name: "a wider rail on a client a team is worked on", width: 200, rail: 60, want: 70},
		{name: "a wider rail on a client nobody measured", rail: 60, want: 70},
		{name: "the narrowest rail rounded up", width: 120, rail: layout.MinRailWidth, want: 83},
		{name: "a rail past the widest takes the widest", width: 200, rail: 90, want: 70},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := layout.TeamLeadShare(tc.width, tc.rail)
			if got != tc.want {
				t.Fatalf("TeamLeadShare(%d, %d) = %d, want %d", tc.width, tc.rail, got, tc.want)
			}
			// Whatever the client, the share is one a plan can carry.
			if got < 10 || got > 90 {
				t.Fatalf("TeamLeadShare(%d, %d) = %d, which no split takes", tc.width, tc.rail, got)
			}
			rail := layout.RailCells(tc.rail)
			if tc.width >= rail*2 {
				// The rail gets the cells it asked for, never fewer.
				if cells := tc.width - tc.width*got/100; cells < rail {
					t.Fatalf("the rail is %d cells of %d, want %d", cells, tc.width, rail)
				}
			}
		})
	}
}

// TestWithRail checks the rail joins a plan as its own pane, in front of every
// other one, and that the plan it makes is one a window can be built from.
func TestWithRail(t *testing.T) {
	trio, err := layout.Builtin(layout.Trio, layout.Options{Width: 240, Height: 60})
	if err != nil {
		t.Fatal(err)
	}
	full := layout.Plan{Name: "full", Panes: make([]layout.Pane, layout.MaxPanes)}
	for i := range full.Panes {
		full.Panes[i] = layout.Pane{Role: layout.RoleClaude, Split: layout.SplitDown, Size: 50, Parent: max(i-1, 0)}
	}
	full.Panes[0] = layout.Pane{Role: layout.RoleClaude}
	cases := []struct {
		name    string
		plan    layout.Plan
		width   int
		rail    int
		want    []layout.Role
		parents []int
		focus   int
		// wantRail is the width the plan gives its rail afterwards.
		wantRail int
	}{
		{
			name:     "a plan of one pane",
			plan:     layout.Plan{Name: "solo", Panes: []layout.Pane{{Role: layout.RoleClaude}}},
			width:    200,
			want:     []layout.Role{layout.RoleAgents, layout.RoleClaude},
			parents:  []int{0, 0},
			focus:    1,
			wantRail: layout.RailWidth,
		},
		{
			name:     "every later pane keeps the pane it splits",
			plan:     trio,
			width:    240,
			want:     []layout.Role{layout.RoleAgents, layout.RoleClaude, layout.RoleShell, layout.RoleChanges},
			parents:  []int{0, 0, 1, 2},
			focus:    1,
			wantRail: layout.RailWidth,
		},
		{
			name:     "a rail of the width the workspace asks for",
			plan:     layout.Plan{Name: "solo", Panes: []layout.Pane{{Role: layout.RoleClaude}}},
			width:    200,
			rail:     44,
			want:     []layout.Role{layout.RoleAgents, layout.RoleClaude},
			parents:  []int{0, 0},
			focus:    1,
			wantRail: 44,
		},
		{
			name: "a plan that carries the rail already",
			plan: layout.Plan{
				Name: "team", Panes: []layout.Pane{{Role: layout.RoleAgents}, {Role: layout.RoleClaude, Split: layout.SplitRight, Size: 80}},
				Focus: 1, RailWidth: 30,
			},
			width:    240,
			rail:     50,
			want:     []layout.Role{layout.RoleAgents, layout.RoleClaude},
			parents:  []int{0, 0},
			focus:    1,
			wantRail: 30,
		},
		{
			name:    "a plan with no room for another pane",
			plan:    full,
			width:   240,
			want:    slices.Repeat([]layout.Role{layout.RoleClaude}, layout.MaxPanes),
			parents: []int{0, 0, 1, 2, 3, 4, 5, 6, 7},
			focus:   0,
		},
		{
			name:    "a plan with no pane at all",
			plan:    layout.Plan{Name: "none"},
			width:   240,
			want:    nil,
			parents: nil,
			focus:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := layout.WithRail(tc.plan, tc.width, tc.rail)
			roles := make([]layout.Role, len(got.Panes))
			parents := make([]int, len(got.Panes))
			for i, pane := range got.Panes {
				roles[i], parents[i] = pane.Role, pane.Parent
			}
			if !slices.Equal(roles, tc.want) {
				t.Fatalf("roles %v, want %v", roles, tc.want)
			}
			if !slices.Equal(parents, tc.parents) {
				t.Fatalf("parents %v, want %v", parents, tc.parents)
			}
			if got.Focus != tc.focus {
				t.Fatalf("focus %d, want %d", got.Focus, tc.focus)
			}
			if len(got.Panes) > 0 {
				if err := got.Validate(); err != nil {
					t.Fatalf("the plan cannot be built: %v", err)
				}
			}
			if got.RailWidth != tc.wantRail {
				t.Fatalf("the plan gives its rail %d cells, want %d", got.RailWidth, tc.wantRail)
			}
			if len(got.Panes) > len(tc.plan.Panes) {
				if share, want := got.Panes[1].Size, layout.TeamLeadShare(tc.width, tc.rail); share != want {
					t.Fatalf("the pane beside the rail takes %d%%, want %d%%", share, want)
				}
				if got.Panes[1].Split != layout.SplitRight {
					t.Fatalf("the rail is split %q, want right", got.Panes[1].Split)
				}
			}
		})
	}
}

// TestWithRailKeepsItsInput guards the plan the caller passed: a plan is a
// value, and the one that goes in is still the one it was after a rail was
// made from it.
func TestWithRailKeepsItsInput(t *testing.T) {
	in := layout.Plan{Name: "duo", Panes: []layout.Pane{
		{Role: layout.RoleClaude},
		{Role: layout.RoleShell, Split: layout.SplitRight, Size: 38, Parent: 0},
	}}
	before := slices.Clone(in.Panes)
	layout.WithRail(in, 200, layout.RailWidth)
	if !slices.Equal(in.Panes, before) || in.Focus != 0 {
		t.Fatalf("the plan was modified: %+v, want %+v", in.Panes, before)
	}
}
