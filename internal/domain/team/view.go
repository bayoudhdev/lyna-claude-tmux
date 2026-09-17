package team

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Group is one section of the agents view, numbered in the order the sections
// are drawn.
type Group int

// The sections of the view.
const (
	// GroupLead is the agent the workspace was opened for, the one that leads
	// a team when there is one.
	GroupLead Group = iota
	// GroupTeammates holds the agents of the lead's team.
	GroupTeammates
	// GroupSubagents holds the subagents an agent of this workspace started.
	GroupSubagents
	// GroupElsewhere holds the agents of the other workspaces on the same
	// server, which are the ones a jump can reach.
	GroupElsewhere
)

// String is the section's heading.
func (g Group) String() string {
	switch g {
	case GroupLead:
		return "lead"
	case GroupTeammates:
		return "teammates"
	case GroupSubagents:
		return "subagents"
	case GroupElsewhere:
		return "elsewhere"
	}
	return "agents"
}

// Groups lists the sections in drawing order.
func Groups() []Group { return []Group{GroupLead, GroupTeammates, GroupSubagents, GroupElsewhere} }

// Roles the view reads. They are the values of the pane option the workspace
// writes, repeated here so the rows are built without an adapter.
const (
	RoleClaude   = "claude"
	RoleTeammate = "teammate"
)

// Row states. The first three are the states a pane of the workspace carries;
// the last two are read from the pane and the team rather than set by a hook.
const (
	// StateBusy is an agent working, StateWaiting one waiting on the user and
	// StateIdle one with nothing to do. They are the values of the pane option
	// the hooks maintain.
	StateBusy    = "busy"
	StateWaiting = "waiting"
	StateIdle    = "idle"
	// StateFailed is an agent whose pane holds the exit status of a process
	// that stopped on its own.
	StateFailed = "failed"
	// StateGone is a member of the team that runs in no pane of this server:
	// an agent Claude Code opened its own way, or one whose pane has closed.
	StateGone = "gone"
)

// Row is one agent of the view.
type Row struct {
	Group Group
	// Name is what the agent is called: the name it was spawned under, or the
	// workspace it leads when a lead has no team.
	Name string
	// Type is the agent definition it was spawned from, empty when it has none.
	Type string
	// State is what the agent is doing.
	State string
	// Pane is the pane it runs in, empty for an agent that runs in none of
	// ours, and Window the window that pane is in.
	Pane, Window, WindowName string
	// Session is the workspace the pane belongs to, which is only drawn for
	// the agents of other workspaces.
	Session string
	// Team is the team the agent belongs to, empty when it belongs to none.
	Team string
	// Task is the work the agent holds, taken from the shared task list.
	Task string
	// Since is when the agent last changed state; zero when nothing stamped it.
	Since time.Time
	// Subagents is how many subagents the agent is running.
	Subagents int
	// Active marks the pane the user is on.
	Active bool
}

// Target reports the pane a row can be jumped to.
func (r Row) Target() (string, bool) {
	if strings.HasPrefix(r.Pane, "%") {
		return r.Pane, true
	}
	return "", false
}

// View is the whole agents view: the rows in drawing order and what the team
// has to show under them.
type View struct {
	Rows []Row
	// Tasks counts the shared task list.
	Tasks Counts
	// Team is the name of the team the lead runs, empty without one.
	Team string
}

// Count returns how many rows a section holds.
func (v View) Count(g Group) int {
	n := 0
	for _, r := range v.Rows {
		if r.Group == g {
			n++
		}
	}
	return n
}

// Pane is what the view reads about one pane of a tmux server: the labels the
// workspace writes on it, and where it is.
type Pane struct {
	ID, Session, Window, WindowName string
	Role, State                     string
	Agent, AgentType, Team          string
	// Subagents is the running subagent count the hooks keep on the pane, and
	// Running the subagents themselves, when they were named.
	Subagents int
	Running   []Subagent
	// Since is when the pane last changed state.
	Since time.Time
	// Active marks the pane the client is on, Dead a pane whose process
	// stopped and whose exit status is still on screen.
	Active, Dead bool
}

// Subagent is one subagent an agent of the workspace is running.
type Subagent struct {
	ID, Type string
}

// Input is everything the view is built from.
type Input struct {
	// Session is the workspace the view belongs to. Panes of other sessions
	// are the agents elsewhere.
	Session string
	// Panes are the panes of the whole server, in any order.
	Panes []Pane
	// Config is the team file Claude Code writes, zero when there is no team
	// or it could not be read.
	Config Config
	// Tasks is the shared task list, empty when there is none.
	Tasks []Task
}

// Build turns the panes of a server, the team file and the shared task list
// into the rows of the agents view.
//
// The panes decide: an agent the workspace runs is a pane of the workspace,
// labeled by its own hooks, and that is what the rows are built from. The team
// file only adds what the panes cannot say, which is the members Claude Code
// opened somewhere this server cannot see, and the task each agent holds.
func Build(in Input) View {
	v := View{Tasks: Count(in.Tasks), Team: in.Config.Name}
	held := heldTasks(in.Tasks)
	seen := make(map[string]bool, len(in.Panes))
	for _, p := range in.Panes {
		switch {
		case p.Session != in.Session:
			if row, ok := elsewhereRow(p); ok {
				v.Rows = append(v.Rows, row)
			}
			continue
		case p.Role == RoleClaude:
			v.Rows = append(v.Rows, leadRow(p, in, held))
		case p.Role == RoleTeammate:
			seen[p.Agent] = true
			v.Rows = append(v.Rows, teammateRow(p, held))
		default:
			continue
		}
		v.Rows = append(v.Rows, subagentRows(p)...)
	}
	// A teammate Claude Code opened its own way runs in no pane of ours, and
	// is still a teammate of the team: the file is the only place it is
	// written down.
	for _, m := range in.Config.Teammates() {
		if seen[m.Name] || m.Name == "" {
			continue
		}
		v.Rows = append(v.Rows, Row{
			Group: GroupTeammates, Name: m.Name, Type: m.AgentType, State: StateGone,
			Team: in.Config.Name, Task: held[m.Name], Since: m.JoinedAt,
		})
	}
	sortRows(v.Rows)
	return v
}

func leadRow(p Pane, in Input, held map[string]string) Row {
	name := p.Agent
	if name == "" {
		// A lead is not spawned by name: without a team it is the workspace
		// the user opened, and with one it is the name the team file gives it.
		name = p.Session
		if lead, ok := leadMember(in.Config); ok && lead.Name != "" {
			name = lead.Name
		}
	}
	r := paneRow(p, GroupLead, name)
	r.Task = held[name]
	if r.Team == "" {
		r.Team = in.Config.Name
	}
	return r
}

func teammateRow(p Pane, held map[string]string) Row {
	name := p.Agent
	if name == "" {
		name = p.WindowName
	}
	r := paneRow(p, GroupTeammates, name)
	r.Task = held[name]
	return r
}

// elsewhereRow is an agent of another workspace on the same server. Only the
// agents of that workspace are listed, its lead and its teammates alike; a
// shell or a changes pane of another workspace is not an agent.
func elsewhereRow(p Pane) (Row, bool) {
	if p.Role != RoleClaude && p.Role != RoleTeammate {
		return Row{}, false
	}
	name := p.Agent
	if name == "" {
		name = p.Session
	}
	return paneRow(p, GroupElsewhere, name), true
}

func subagentRows(p Pane) []Row {
	rows := make([]Row, 0, len(p.Running))
	for _, s := range p.Running {
		name := s.Type
		if name == "" {
			name = s.ID
		}
		rows = append(rows, Row{
			Group: GroupSubagents, Name: name, Type: s.Type, State: StateBusy,
			Pane: p.ID, Window: p.Window, WindowName: p.WindowName, Session: p.Session,
			Team: p.Team, Since: p.Since,
		})
	}
	return rows
}

// paneRow is the part of a row every pane answers.
func paneRow(p Pane, g Group, name string) Row {
	return Row{
		Group: g, Name: name, Type: p.AgentType, State: paneState(p),
		Pane: p.ID, Window: p.Window, WindowName: p.WindowName, Session: p.Session,
		Team: p.Team, Since: p.Since, Subagents: p.Subagents, Active: p.Active,
	}
}

// paneState reads the state of a pane: a pane whose process stopped shows what
// happened to it whatever the last hook wrote, and a pane no hook has labeled
// yet is an agent that has not answered, which is an agent with nothing to do.
func paneState(p Pane) string {
	switch {
	case p.Dead:
		return StateFailed
	case p.State == StateBusy || p.State == StateWaiting || p.State == StateIdle:
		return p.State
	}
	return StateIdle
}

func leadMember(c Config) (Member, bool) {
	for _, m := range c.Members {
		if m.Lead {
			return m, true
		}
	}
	return Member{}, false
}

// heldTasks maps an owner to the task it is working on. An owner working on
// more than one task holds the first in task order, which is the order the
// shared list is read in.
func heldTasks(tasks []Task) map[string]string {
	sorted := slices.Clone(tasks)
	SortTasks(sorted)
	held := make(map[string]string, len(sorted))
	for _, t := range sorted {
		if t.Owner == "" || t.Status != StatusRunning {
			continue
		}
		if _, ok := held[t.Owner]; !ok {
			held[t.Owner] = t.Label()
		}
	}
	return held
}

// sortRows puts the rows in drawing order: the sections in their own order,
// then the agents of a section by name, and panes of the same name by pane id
// so a redraw never reorders two rows that look alike.
func sortRows(rows []Row) {
	slices.SortStableFunc(rows, func(a, b Row) int {
		if c := cmp.Compare(a.Group, b.Group); c != 0 {
			return c
		}
		if a.Group == GroupElsewhere {
			if c := cmp.Compare(a.Session, b.Session); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Pane, b.Pane)
	})
}
