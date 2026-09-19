package tui

import (
	"strconv"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

// benchRows is how many rows the rail is measured at: more agents than a team
// is given in practice, so the number says what the worst redraw costs rather
// than the usual one.
const benchRows = 40

// benchView builds a workspace of twenty-six teammates, thirteen of them
// running a subagent, each holding a task: one lead, its teammates and their
// subagents come to benchRows rows.
func benchView() team.View {
	panes := []team.Pane{{
		ID: "%1", Session: "api", Window: "@1", WindowName: "claude", Role: team.RoleClaude,
		State: team.StateWaiting, Team: "session-8f3c1d2a", Active: true,
	}}
	members := []team.Member{{Name: "api", Lead: true, Pane: team.LeadPane, Backend: team.BackendTmux}}
	tasks := make([]team.Task, 0, 26)
	for i := range 26 {
		id := strconv.Itoa(i)
		pane := team.Pane{
			ID: "%" + strconv.Itoa(i+2), Session: "api", Window: "@" + strconv.Itoa(i+2),
			WindowName: "review-" + id, Role: team.RoleTeammate, State: team.StateBusy,
			Agent: "review-" + id, AgentType: "api-developer", Team: "session-8f3c1d2a",
		}
		if i%2 == 0 {
			pane.Subagents = 1
			pane.Running = []team.Subagent{{ID: "ag-" + id, Type: "security-auditor"}}
		}
		panes = append(panes, pane)
		members = append(members, team.Member{Name: "review-" + id, Pane: pane.ID, Backend: team.BackendTmux})
		tasks = append(tasks, team.Task{
			ID: "t-" + id, Subject: "review the router of service " + id,
			ActiveForm: "reviewing the router of service " + id,
			Owner:      "review-" + id, Status: team.StatusRunning,
		})
	}
	return team.Build(team.Input{
		Session: "api",
		Panes:   panes,
		Config:  team.Config{Name: "session-8f3c1d2a", Members: members},
		Tasks:   tasks,
	})
}

// BenchmarkAgentBarRender measures one redraw of a full rail, which is what a
// hook from any agent of the workspace costs the pane that draws it.
func BenchmarkAgentBarRender(b *testing.B) {
	view := benchView()
	if len(view.Rows) != benchRows {
		b.Fatalf("the view has %d rows, want %d", len(view.Rows), benchRows)
	}
	m := NewAgentBar(AgentBarOptions{Styles: goldenStyles(b), Width: 28, Height: 44, Now: clock})
	apply(m, barUpdate(view, nil))
	b.ReportAllocs()
	for b.Loop() {
		if m.View().Content == "" {
			b.Fatal("the rail drew nothing")
		}
	}
}

// BenchmarkAgentBarUpdate measures a reading arriving: the rows are rebuilt,
// the states stamped and the frame drawn again, which is the whole cost of one
// agent changing state.
func BenchmarkAgentBarUpdate(b *testing.B) {
	view := benchView()
	m := NewAgentBar(AgentBarOptions{Styles: goldenStyles(b), Width: 28, Height: 44, Now: clock})
	b.ReportAllocs()
	for b.Loop() {
		apply(m, barUpdate(view, nil))
		if m.View().Content == "" {
			b.Fatal("the rail drew nothing")
		}
	}
}
