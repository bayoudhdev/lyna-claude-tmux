package team_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

// rowLine is one row as the tests read it: the section, the name, the state
// and the pane, which is what every case is about.
func rowLine(r team.Row) string {
	return r.Group.String() + " " + r.Name + " " + r.State + " " + r.Pane
}

func rowLines(rows []team.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowLine(r))
	}
	return out
}

func TestBuild(t *testing.T) {
	lead := team.Pane{
		ID: "%1", Session: "api", Window: "@1", WindowName: "claude",
		Role: team.RoleClaude, State: team.StateBusy, Active: true,
	}
	mate := func(id, name string) team.Pane {
		return team.Pane{
			ID: id, Session: "api", Window: "@1", WindowName: "claude",
			Role: team.RoleTeammate, State: team.StateBusy, Agent: name,
			AgentType: "api-developer", Team: "session-8f3c1d2a",
		}
	}
	cases := []struct {
		name string
		in   team.Input
		want []string
	}{
		{
			name: "a workspace working alone",
			in:   team.Input{Session: "api", Panes: []team.Pane{lead}},
			want: []string{"lead api busy %1"},
		},
		{
			name: "a workspace with no agent at all",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{ID: "%2", Session: "api", Role: "shell"},
			}},
		},
		{
			name: "a lead and its teammates",
			in: team.Input{Session: "api", Panes: []team.Pane{
				mate("%3", "review-api"), lead, mate("%2", "build-api"),
			}},
			want: []string{
				"lead api busy %1",
				"teammates build-api busy %2",
				"teammates review-api busy %3",
			},
		},
		{
			name: "a teammate the team knows and no pane runs",
			in: team.Input{
				Session: "api", Panes: []team.Pane{lead, mate("%2", "build-api")},
				Config: team.Config{Name: "session-8f3c1d2a", Members: []team.Member{
					{Name: "lead", Lead: true, Pane: team.LeadPane, Backend: team.BackendTmux},
					{Name: "build-api", Backend: team.BackendTmux, Pane: "%2"},
					{Name: "write-docs", AgentType: "docs", Backend: team.BackendInProcess},
				}},
			},
			want: []string{
				"lead lead busy %1",
				"teammates build-api busy %2",
				"teammates write-docs gone ",
			},
		},
		{
			name: "an agent whose pane stopped",
			in: team.Input{Session: "api", Panes: []team.Pane{
				lead, {
					ID: "%2", Session: "api", Role: team.RoleTeammate, State: team.StateBusy,
					Agent: "review-api", Dead: true,
				},
			}},
			want: []string{"lead api busy %1", "teammates review-api failed %2"},
		},
		{
			name: "a pane no hook has labeled yet",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{ID: "%1", Session: "api", Role: team.RoleClaude},
			}},
			want: []string{"lead api idle %1"},
		},
		{
			name: "a state a newer release writes",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{ID: "%1", Session: "api", Role: team.RoleClaude, State: "compacting"},
			}},
			want: []string{"lead api idle %1"},
		},
		{
			name: "the subagents an agent is running",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{
					ID: "%1", Session: "api", Role: team.RoleClaude, State: team.StateBusy,
					Subagents: 2, Running: []team.Subagent{
						{ID: "ag-2", Type: "security-auditor"}, {ID: "ag-1"},
					},
				},
			}},
			want: []string{
				"lead api busy %1",
				"subagents ag-1 busy %1",
				"subagents security-auditor busy %1",
			},
		},
		{
			name: "the agents of the other workspaces",
			in: team.Input{Session: "api", Panes: []team.Pane{
				lead,
				{ID: "%9", Session: "web", Role: team.RoleClaude, State: team.StateWaiting},
				{ID: "%8", Session: "docs", Role: team.RoleTeammate, State: team.StateIdle, Agent: "write-docs"},
				{ID: "%7", Session: "web", Role: "shell"},
			}},
			want: []string{
				"lead api busy %1",
				"elsewhere write-docs idle %8",
				"elsewhere web waiting %9",
			},
		},
		{
			name: "agents elsewhere are grouped by their workspace",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{ID: "%9", Session: "web", Role: team.RoleClaude, State: team.StateIdle, Agent: "alpha"},
				{ID: "%8", Session: "docs", Role: team.RoleClaude, State: team.StateIdle, Agent: "zeta"},
			}},
			want: []string{"elsewhere zeta idle %8", "elsewhere alpha idle %9"},
		},
		{
			name: "two agents answering to the same name",
			in: team.Input{Session: "api", Panes: []team.Pane{
				mate("%4", "review-api"), mate("%2", "review-api"),
			}},
			want: []string{"teammates review-api busy %2", "teammates review-api busy %4"},
		},
		{
			name: "a teammate whose arguments carried no name",
			in: team.Input{Session: "api", Panes: []team.Pane{
				{
					ID: "%2", Session: "api", Window: "@2", WindowName: "review-api",
					Role: team.RoleTeammate, State: team.StateBusy,
				},
			}},
			want: []string{"teammates review-api busy %2"},
		},
		{
			name: "nothing at all",
			in:   team.Input{Session: "api"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rowLines(team.Build(tc.in).Rows)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Build rows:\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestBuildTasks covers what the shared task list adds to the rows: every
// agent shows the task it is working on, and the footer counts the list.
func TestBuildTasks(t *testing.T) {
	tasks := []team.Task{
		{ID: "t-3", Subject: "ship it", Owner: "review-api", Status: team.StatusPending},
		{ID: "t-2", Subject: "review the router", ActiveForm: "reviewing the router", Owner: "review-api", Status: team.StatusRunning},
		{ID: "t-1", Subject: "write the tests", Owner: "build-api", Status: team.StatusRunning},
		{ID: "t-0", Subject: "read the plan", Owner: "build-api", Status: team.StatusDone},
	}
	in := team.Input{
		Session: "api", Tasks: tasks,
		Panes: []team.Pane{
			{ID: "%1", Session: "api", Role: team.RoleClaude, Agent: "lead"},
			{ID: "%2", Session: "api", Role: team.RoleTeammate, Agent: "review-api"},
			{ID: "%3", Session: "api", Role: team.RoleTeammate, Agent: "build-api"},
		},
	}
	v := team.Build(in)
	want := map[string]string{
		"lead":       "",
		"review-api": "reviewing the router",
		"build-api":  "write the tests",
	}
	for _, r := range v.Rows {
		if got := r.Task; got != want[r.Name] {
			t.Fatalf("%s holds %q, want %q", r.Name, got, want[r.Name])
		}
	}
	if v.Tasks != (team.Counts{Total: 4, Pending: 1, Running: 2, Done: 1}) {
		t.Fatalf("task counts %+v", v.Tasks)
	}
	if v.Count(team.GroupTeammates) != 2 || v.Count(team.GroupLead) != 1 || v.Count(team.GroupSubagents) != 0 {
		t.Fatalf("sections %d lead, %d teammates", v.Count(team.GroupLead), v.Count(team.GroupTeammates))
	}
}

// TestBuildRowDetails reads back everything a row carries beyond its name, so
// the view can draw a pane without going to the server for it again.
func TestBuildRowDetails(t *testing.T) {
	since := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC)
	v := team.Build(team.Input{
		Session: "api",
		Panes: []team.Pane{{
			ID: "%4", Session: "api", Window: "@2", WindowName: "review-api",
			Role: team.RoleTeammate, State: team.StateWaiting, Agent: "review-api",
			AgentType: "api-developer", Team: "session-8f3c1d2a", Subagents: 3,
			Since: since, Active: true,
		}},
	})
	if len(v.Rows) != 1 {
		t.Fatalf("rows %+v", v.Rows)
	}
	want := team.Row{
		Group: team.GroupTeammates, Name: "review-api", Type: "api-developer",
		State: team.StateWaiting, Pane: "%4", Window: "@2", WindowName: "review-api",
		Session: "api", Team: "session-8f3c1d2a", Subagents: 3, Since: since, Active: true,
	}
	if v.Rows[0] != want {
		t.Fatalf("row\n got %+v\nwant %+v", v.Rows[0], want)
	}
}

func TestRowTarget(t *testing.T) {
	cases := []struct {
		name string
		row  team.Row
		want string
	}{
		{name: "an agent in a pane of ours", row: team.Row{Pane: "%12"}, want: "%12"},
		{name: "an agent in no pane of ours", row: team.Row{}},
		{name: "a pane named instead of identified", row: team.Row{Pane: "review-api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.row.Target()
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("Target() = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestGroups(t *testing.T) {
	var names []string
	for _, g := range team.Groups() {
		names = append(names, g.String())
	}
	if got := strings.Join(names, " "); got != "lead teammates subagents elsewhere" {
		t.Fatalf("Groups() = %q", got)
	}
	if got := team.Group(9).String(); got != "agents" {
		t.Fatalf("a section this release does not draw is %q", got)
	}
}
