// Package layout turns a layout name or a custom pane list into a plan: the
// ordered panes of a window, which earlier pane each one splits, in which
// direction and by how much. The tmux adapter executes plans; this package
// only decides them.
package layout

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Role is what runs in a pane.
type Role string

// Pane roles. They match the @lt_role pane option values.
const (
	RoleClaude  Role = "claude"
	RoleShell   Role = "shell"
	RoleChanges Role = "changes"
	RoleReview  Role = "review"
	RoleCommand Role = "command"
	// RoleAgents is the rail: every agent of the workspace, what it is doing
	// and where it runs.
	RoleAgents Role = "agents"
)

// Roles lists every pane role, the Claude role first.
func Roles() []string {
	return []string{
		string(RoleClaude), string(RoleShell), string(RoleChanges),
		string(RoleReview), string(RoleCommand), string(RoleAgents),
	}
}

// Split is the direction a new pane is created in.
type Split string

const (
	SplitRight Split = "right"
	SplitDown  Split = "down"
)

// Pane is one pane of a plan. The first pane is the window itself and has no
// split; every later pane splits an earlier one.
type Pane struct {
	Role  Role
	Split Split
	// Size is the new pane's share of the split pane in percent (10-90).
	Size int
	// Parent is the 0-based index of the earlier pane this one splits.
	Parent int
	// Command is the shell command of a command pane.
	Command string
	// Worktree names the git worktree a Claude pane runs in; empty runs in the
	// project directory.
	Worktree string
}

// Plan is the pane arrangement of one window.
type Plan struct {
	Name  string
	Panes []Pane
	// Focus is the index of the pane selected after creation.
	Focus int
}

// Built-in layout names.
const (
	Solo   = "solo"
	Duo    = "duo"
	Trio   = "trio"
	Quad   = "quad"
	Review = "review"
	// Team is the layout a team is run in: the rail on the left, the lead
	// beside it, and the room the teammates open into.
	Team = "team"
	Auto = "auto"
)

// Names lists the built-in layouts, auto last.
func Names() []string { return []string{Solo, Duo, Trio, Quad, Review, Team, Auto} }

// IsBuiltin reports whether name is a built-in layout.
func IsBuiltin(name string) bool { return slices.Contains(Names(), name) }

// Options parameterize the built-in layouts.
type Options struct {
	// SplitRatio is the Claude pane's share of the window width in percent.
	SplitRatio int
	// Session names the worktrees of multi-agent layouts ("<session>-1").
	Session string
	// Width and Height are the client size in cells, used by auto.
	Width, Height int
}

// Size thresholds for auto, in terminal cells.
const (
	AutoTrioWidth  = 200
	AutoTrioHeight = 40
	AutoDuoWidth   = 120
)

// Resolve maps auto to a concrete layout for the client size and returns
// other names unchanged. A size of zero (unknown) picks duo.
func Resolve(name string, width, height int) string {
	if name != Auto {
		return name
	}
	switch {
	// A size that is missing in either direction is a size nobody measured:
	// create runs without a terminal, or a caller knows only one of the two.
	// That is a reason to take the default layout, not the smallest one.
	case width <= 0 || height <= 0:
		return Duo
	case width >= AutoTrioWidth && height >= AutoTrioHeight:
		return Trio
	case width >= AutoDuoWidth:
		return Duo
	}
	return Solo
}

// ErrUnknown reports a layout name that is neither built in nor custom.
var ErrUnknown = errors.New("unknown layout")

// Builtin returns the plan of a built-in layout.
func Builtin(name string, o Options) (Plan, error) {
	ratio := o.SplitRatio
	if ratio < 20 || ratio > 80 {
		ratio = 62
	}
	resolved := Resolve(name, o.Width, o.Height)
	var p Plan
	switch resolved {
	case Solo:
		p = Plan{Panes: []Pane{{Role: RoleClaude}}}
	case Duo:
		p = Plan{Panes: []Pane{
			{Role: RoleClaude},
			{Role: RoleShell, Split: SplitRight, Size: 100 - ratio, Parent: 0},
		}}
	case Trio:
		p = Plan{Panes: []Pane{
			{Role: RoleClaude},
			{Role: RoleShell, Split: SplitRight, Size: 100 - ratio, Parent: 0},
			{Role: RoleChanges, Split: SplitDown, Size: 60, Parent: 1},
		}}
	case Quad:
		session := o.Session
		if session == "" {
			session = "agent"
		}
		wt := func(i int) string { return session + "-" + strconv.Itoa(i) }
		p = Plan{Panes: []Pane{
			{Role: RoleClaude, Worktree: wt(1)},
			{Role: RoleClaude, Split: SplitRight, Size: 50, Parent: 0, Worktree: wt(2)},
			{Role: RoleClaude, Split: SplitDown, Size: 50, Parent: 0, Worktree: wt(3)},
			{Role: RoleClaude, Split: SplitDown, Size: 50, Parent: 1, Worktree: wt(4)},
		}}
	case Review:
		p = Plan{Panes: []Pane{
			{Role: RoleClaude},
			{Role: RoleReview, Split: SplitRight, Size: 50, Parent: 0},
		}}
	case Team:
		// The rail is the window, and the lead splits it: the rail is then the
		// leftmost pane of the window, which is the one the agent area is
		// tiled beside rather than over.
		p = WithRail(Plan{Panes: []Pane{{Role: RoleClaude}}}, o.Width)
	default:
		return Plan{}, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	p.Name = resolved
	return p, p.Validate()
}

// MaxPanes bounds a plan; more panes than this are unreadable on any screen.
const MaxPanes = 9

// Validate checks a plan's structure.
func (p Plan) Validate() error {
	if len(p.Panes) == 0 || len(p.Panes) > MaxPanes {
		return fmt.Errorf("layout %q: needs 1 to %d panes, has %d", p.Name, MaxPanes, len(p.Panes))
	}
	claude := 0
	for i, pane := range p.Panes {
		where := fmt.Sprintf("layout %q pane %d", p.Name, i+1)
		switch pane.Role {
		case RoleClaude:
			claude++
		case RoleShell, RoleChanges, RoleReview, RoleAgents:
		case RoleCommand:
			if pane.Command == "" {
				return fmt.Errorf("%s: a command pane needs a command", where)
			}
		default:
			return fmt.Errorf("%s: unknown role %q", where, pane.Role)
		}
		if pane.Role != RoleCommand && pane.Command != "" {
			return fmt.Errorf("%s: only command panes take a command", where)
		}
		if pane.Worktree != "" {
			if pane.Role != RoleClaude {
				return fmt.Errorf("%s: only claude panes run in a worktree", where)
			}
			if err := ValidateWorktree(pane.Worktree); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		if i == 0 {
			if pane.Split != "" || pane.Size != 0 || pane.Parent != 0 {
				return fmt.Errorf("%s: the first pane is the window and takes no split", where)
			}
			continue
		}
		if pane.Split != SplitRight && pane.Split != SplitDown {
			return fmt.Errorf("%s: split must be right or down, got %q", where, pane.Split)
		}
		if pane.Size < 10 || pane.Size > 90 {
			return fmt.Errorf("%s: size must be 10 to 90 percent, got %d", where, pane.Size)
		}
		if pane.Parent < 0 || pane.Parent >= i {
			return fmt.Errorf("%s: parent must be an earlier pane, got %d", where, pane.Parent+1)
		}
	}
	if claude == 0 {
		return fmt.Errorf("layout %q: needs at least one claude pane", p.Name)
	}
	if p.Focus < 0 || p.Focus >= len(p.Panes) {
		return fmt.Errorf("layout %q: focus %d is not a pane", p.Name, p.Focus)
	}
	return nil
}

// ValidateWorktree checks a worktree name: 1-64 characters of letters,
// digits, '.', '_' and '-', not starting with '-' or '.', and no "..".
func ValidateWorktree(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("worktree name must be 1 to 64 characters, got %q", name)
	}
	if name[0] == '-' || name[0] == '.' {
		return fmt.Errorf("worktree name %q must not start with '-' or '.'", name)
	}
	for i := range len(name) {
		c := name[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("worktree name %q contains %q (allowed: letters, digits, '.', '_', '-')", name, c)
		}
		if c == '.' && i+1 < len(name) && name[i+1] == '.' {
			return fmt.Errorf("worktree name %q must not contain \"..\"", name)
		}
	}
	return nil
}

// CustomPane is one pane of a user-defined layout as written in config.toml:
// Parent is 1-based and 0 means the previous pane, Size 0 means an even split,
// and Worktree is a flag rather than a name.
type CustomPane struct {
	Role     string
	Split    string
	Size     int
	Parent   int
	Command  string
	Worktree bool
}

// Custom builds and validates a plan from a user-defined layout. Worktree
// panes get "<session>-<pane number>" worktrees.
func Custom(name, session string, panes []CustomPane) (Plan, error) {
	p := Plan{Name: name, Panes: make([]Pane, 0, len(panes))}
	for i, c := range panes {
		pane := Pane{Role: Role(c.Role), Split: Split(c.Split), Size: c.Size, Command: c.Command}
		if i > 0 {
			if pane.Size == 0 {
				pane.Size = 50
			}
			pane.Parent = i - 1
			if c.Parent > 0 {
				pane.Parent = c.Parent - 1
			}
		}
		if c.Worktree {
			pane.Worktree = session + "-" + strconv.Itoa(i+1)
		}
		p.Panes = append(p.Panes, pane)
	}
	for i, pane := range p.Panes {
		if pane.Role == RoleClaude {
			p.Focus = i
			break
		}
	}
	return p, p.Validate()
}
