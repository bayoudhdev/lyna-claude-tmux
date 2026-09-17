package layout

// Placement is where the pane of a teammate that just opened belongs.
type Placement string

const (
	// PlaceHere keeps the teammate in the window it was opened in, beside the
	// lead, and the window is tiled again for the two of them.
	PlaceHere Placement = "here"
	// PlaceWindow moves the teammate to a window of its own, named after it,
	// which is where the sidebar then points.
	PlaceWindow Placement = "window"
)

// Geometry of a window shared by a lead and its teammates.
const (
	// AgentLeadRatio is the lead's share of the width of such a window, in
	// percent. The lead is the pane the user types in, so it keeps the larger
	// half of what a two-column window can give, and the teammates share the
	// rest: they are read far more often than they are typed in.
	AgentLeadRatio = 40
	// MinAgentWidth and MinAgentHeight are the smallest pane a teammate is
	// left in. Below them an agent's transcript, its input box and its status
	// line wrap into something nobody can follow, and a window of its own,
	// which the sidebar reaches with one key, is worth more than a sliver.
	MinAgentWidth  = 80
	MinAgentHeight = 14
	// DefaultAgentPanes is the workspace.agent_panes default: how many
	// teammates share the lead's window before the next one opens as a window
	// of its own.
	DefaultAgentPanes = 3
	// MaxAgentPanes bounds the configuration value, since past it no client is
	// wide enough for the panes it asks for.
	MaxAgentPanes = 8
	// RailWidth is the width the agents rail is opened at, in cells. It holds a
	// state glyph, a name long enough to tell two agents apart and how long the
	// agent has been doing what it is doing.
	RailWidth = 28
	// MinRailWidth is the narrowest rail a window is given one at, and
	// MinRailShare the smallest share of a window the lead is left with.
	MinRailWidth = 20
	MinRailShare = 10
)

// TeamLeadShare is the lead's share of a window that carries the rail, in
// percent: what is left of the window once the rail has its cells. A width
// nobody measured takes the share a window of the size a team is worked in
// would have given.
func TeamLeadShare(width int) int {
	if width <= 0 {
		return 100 - RailWidth*100/AutoTrioWidth
	}
	// The rail is rounded up, so it is never a cell narrower than it asked for.
	rail := (RailWidth*100 + width - 1) / width
	return min(max(100-rail, MinRailShare), 100-MinRailShare)
}

// WithRail puts the agents rail in front of a plan: the rail becomes the
// window and the first pane of the plan splits it, so the rail is the leftmost
// pane and the one main-vertical then keeps a column of its own for.
//
// A plan that already carries a rail is returned unchanged, and so is one with
// no room left for a pane: the rail is worth a pane of its own, never the pane
// of something else.
func WithRail(p Plan, width int) Plan {
	if len(p.Panes) == 0 || len(p.Panes) >= MaxPanes {
		return p
	}
	for _, pane := range p.Panes {
		if pane.Role == RoleAgents {
			return p
		}
	}
	panes := make([]Pane, 0, len(p.Panes)+1)
	panes = append(panes, Pane{Role: RoleAgents})
	for i, pane := range p.Panes {
		if i == 0 {
			// The pane that was the window becomes the split of the rail, with
			// what the rail leaves of the width.
			pane.Split, pane.Size, pane.Parent = SplitRight, TeamLeadShare(width), 0
		} else {
			pane.Parent++
		}
		panes = append(panes, pane)
	}
	p.Panes = panes
	p.Focus++
	return p
}

// AgentWindow is the window a teammate has just been opened in, described as
// it is once that pane exists.
type AgentWindow struct {
	// Width and Height are the window size in cells. Zero is a size nobody
	// measured, which decides nothing on its own.
	Width, Height int
	// Teammates counts the teammate panes in the window, this one included.
	Teammates int
	// Others counts the panes that are neither the lead nor a teammate: a
	// shell, the changes view, a review, anything the user opened.
	Others int
	// Max is workspace.agent_panes: how many teammates the workspace lets a
	// window hold beside the lead. Zero opens every one of them in a window
	// of its own.
	Max int
	// Rail is the width of the agents rail in the window, its border left out,
	// or zero in a window that carries none. The rail is a pane of ours rather
	// than one of the user's: it keeps a column, and the agents share what it
	// leaves instead of giving up the window to it.
	Rail int
}

// PlaceAgent decides where a teammate's pane goes.
//
// It stays beside the lead while the window is the lead's alone, while the
// workspace allows one more teammate there, and while the panes that would
// result are still readable. It moves to a window of its own otherwise, which
// is what keeps a layout the user chose, a shell they opened or a changes view
// from being reflowed into a sliver by an agent that arrived on its own.
func PlaceAgent(w AgentWindow) Placement {
	if w.Others > 0 || w.Teammates < 1 || w.Teammates > w.Max {
		return PlaceWindow
	}
	// A size nobody measured judges nothing: the count above is the whole
	// policy then, as it is for a layout resolved without a client.
	if w.Width <= 0 || w.Height <= 0 {
		return PlaceHere
	}
	// The teammates share the column the lead leaves, one border between the
	// two columns and one between every two of them.
	column, stacked := w.Width-w.Width*AgentLeadRatio/100-1, w.Teammates
	if w.Rail > 0 {
		// The rail takes the first column of such a window, so the lead stacks
		// with the teammates in what is left rather than keeping a column.
		column, stacked = w.Width-w.Rail-1, w.Teammates+1
	}
	each := (w.Height - (stacked - 1)) / stacked
	if column < MinAgentWidth || each < MinAgentHeight {
		return PlaceWindow
	}
	return PlaceHere
}
