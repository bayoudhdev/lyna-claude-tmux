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
)

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
	column := w.Width - w.Width*AgentLeadRatio/100 - 1
	each := (w.Height - (w.Teammates - 1)) / w.Teammates
	if column < MinAgentWidth || each < MinAgentHeight {
		return PlaceWindow
	}
	return PlaceHere
}
