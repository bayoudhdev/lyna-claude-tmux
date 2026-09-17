package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// teamConfig is the team file Claude Code writes for the scene the tests run:
// a lead and one teammate, the teammate in the pane the scene opened.
const teamConfig = `{
  "name": "session-8f3c1d2a",
  "leadAgentId": "team-lead@session-8f3c1d2a",
  "members": [
    {"agentId": "team-lead@session-8f3c1d2a", "name": "team-lead", "agentType": "team-lead", "backendType": "in-process"},
    {"agentId": "review-api@session-8f3c1d2a", "name": "review-api", "agentType": "api-developer", "backendType": "tmux"},
    {"agentId": "write-docs@session-8f3c1d2a", "name": "write-docs", "agentType": "general", "backendType": "in-process"}
  ]
}`

// writeTeam lays the team and its tasks out under the host's own Claude
// configuration directory, which is inside the test's temporary home.
func writeTeam(t *testing.T, h *testHost, name, config string, tasks map[string]string) {
	t.Helper()
	home := filepath.Join(h.root, ".claude")
	write := func(dir, file, body string) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if config != "" {
		write(filepath.Join(home, "teams", name), "config.json", config)
	}
	for file, body := range tasks {
		write(filepath.Join(home, "tasks", name), file, body)
	}
}

// rowNamed finds a row of the view by its name.
func rowNamed(v team.View, name string) (team.Row, bool) {
	for _, r := range v.Rows {
		if r.Name == name {
			return r, true
		}
	}
	return team.Row{}, false
}

// note runs a command the rail's keys return and reads the line it reports,
// which is empty when the command reported nothing.
func note(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("the action returned no command")
	}
	msg := cmd()
	if msg == nil {
		return ""
	}
	n, ok := msg.(tui.AgentBarNoteMsg)
	if !ok {
		t.Fatalf("the action reported %T", msg)
	}
	return n.Text
}

// TestOpenAgentBarWithoutAWorkspace covers every way the rail can be asked to
// follow a workspace it cannot find.
func TestOpenAgentBarWithoutAWorkspace(t *testing.T) {
	cases := []struct {
		name    string
		tmux    string
		pane    string
		session string
		// want is the error the caller matches on, or says is the error a
		// message carries when the failure is one nobody matches on.
		want error
		says string
	}{
		{name: "outside tmux with no workspace named", want: ErrAgentBarNoWorkspace},
		{name: "in tmux with no pane and no workspace named", tmux: "/tmp/outer,1,0", want: ErrAgentBarNoWorkspace},
		{name: "a workspace that is not running", session: "gone", want: ErrNoWorkspace},
		// A name no workspace can carry is refused by the name itself, before
		// anything at all is asked of the server.
		{name: "a name no workspace can carry", session: "../etc", says: "invalid session name"},
		{name: "a pane of no server", tmux: "/tmp/outer,1,0", pane: "%404", says: "cannot read the workspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHost(t)
			openServer(t, h)
			h.env["TMUX"], h.env["TMUX_PANE"] = c.tmux, c.pane
			h.refreshEnviron()
			_, err := OpenAgentBar(tmuxtest.Context(t), h.Host, AgentBarRequest{Session: c.session})
			switch {
			case err == nil:
				t.Fatal("the rail opened on no workspace")
			case c.says != "" && !strings.Contains(err.Error(), c.says):
				t.Fatalf("error %v, want one saying %q", err, c.says)
			case c.want != nil && !errors.Is(err, c.want):
				t.Fatalf("error %v, want %v", err, c.want)
			}
		})
	}
}

// TestAgentBarReadsTheWorkspace takes one reading on a real server: the lead
// and the teammate of the workspace, the task each one holds, and the member
// the team file names that runs in no pane here.
func TestAgentBarReadsTheWorkspace(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	if _, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3}); err != nil {
		t.Fatal(err)
	}
	writeTeam(t, h, "session-8f3c1d2a", teamConfig, map[string]string{
		"1.json": `{"id":"1","subject":"review the router","owner":"review-api","status":"in_progress"}`,
		"2.json": `{"id":"2","subject":"write the tests","owner":"build-api","status":"pending"}`,
	})

	view, err := OpenAgentBar(ctx, h.Host, AgentBarRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := view.Read(ctx)
	if got.Err != nil {
		t.Fatalf("the reading failed: %v", got.Err)
	}
	if got.View.Team != "session-8f3c1d2a" {
		t.Fatalf("the rail read team %q", got.View.Team)
	}
	if got.View.Tasks.Total != 2 || got.View.Tasks.Running != 1 {
		t.Fatalf("tasks %+v", got.View.Tasks)
	}
	lead, ok := rowNamed(got.View, "team-lead")
	if !ok || lead.Group != team.GroupLead || lead.Pane != scene.lead {
		t.Fatalf("the lead row is %+v, %v", lead, ok)
	}
	mate, ok := rowNamed(got.View, "review-api")
	if !ok || mate.Group != team.GroupTeammates || mate.Pane != scene.pane {
		t.Fatalf("the teammate row is %+v, %v", mate, ok)
	}
	if mate.Type != "api-developer" || mate.State != team.StateBusy || mate.Task != "review the router" {
		t.Fatalf("the teammate row is %+v", mate)
	}
	// The member Claude Code started in the lead's own process runs in no pane
	// of this server, and is still on the rail.
	gone, ok := rowNamed(got.View, "write-docs")
	if !ok || gone.State != team.StateGone {
		t.Fatalf("the member with no pane is %+v, %v", gone, ok)
	}
	if _, ok := gone.Target(); ok {
		t.Fatal("a member with no pane offers a pane to jump to")
	}
}

// TestAgentBarReadsAWorkspaceWithoutATeam covers the workspace the rail opens
// in most of the time: one agent, no team file, nothing to fail on.
func TestAgentBarReadsAWorkspaceWithoutATeam(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	h.env["TMUX_PANE"] = scene.lead
	h.refreshEnviron()

	view, err := OpenAgentBar(ctx, h.Host, AgentBarRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := view.Read(ctx)
	if got.Err != nil || got.View.Team != "" || got.View.Tasks.Total != 0 {
		t.Fatalf("the reading is %+v, %v", got.View, got.Err)
	}
	// A lead with no team is named after the workspace it was opened for.
	if row, ok := rowNamed(got.View, "api"); !ok || row.Group != team.GroupLead {
		t.Fatalf("the lead row is %+v, %v", row, ok)
	}
}

// TestAgentBarActs drives the keys of the rail against a real server.
func TestAgentBarActs(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	if _, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3}); err != nil {
		t.Fatal(err)
	}
	// A rail outside the server it reads: the actions are the same commands,
	// and none of them needs a client attached to this server.
	bar := &agentBar{client: s.Client, session: "api", claudeHome: filepath.Join(h.root, ".claude")}
	row := team.Row{Name: "review-api", Pane: scene.pane, Window: scene.window}

	if got := note(t, bar.focus(row)); got != "" {
		t.Fatalf("focus reported %q", got)
	}
	if active, err := s.Client.Display(ctx, scene.window, "#{pane_id}"); err != nil || active != scene.pane {
		t.Fatalf("the active pane is %q, %v", active, err)
	}
	if got := note(t, bar.zoom(row)); got != "" {
		t.Fatalf("zoom reported %q", got)
	}
	if zoomed, err := s.Client.Display(ctx, scene.window, "#{window_zoomed_flag}"); err != nil || zoomed != "1" {
		t.Fatalf("the window is zoomed %q, %v", zoomed, err)
	}
	if got := note(t, bar.zoom(row)); got != "" {
		t.Fatalf("the second zoom reported %q", got)
	}

	// A window of its own, and the window it leaves back as the user had it.
	if got := note(t, bar.window(row)); got != "" {
		t.Fatalf("the move to a window reported %q", got)
	}
	panes, err := s.Client.ListPanes(ctx, tmux.ExactSession("api"))
	if err != nil {
		t.Fatal(err)
	}
	var moved tmux.Pane
	for _, p := range panes {
		if p.ID == scene.pane {
			moved = p
		}
	}
	if moved.WindowID == scene.window || moved.WindowName != "review-api" {
		t.Fatalf("the teammate is in window %s named %q", moved.WindowID, moved.WindowName)
	}
	if got, err := s.Client.Display(ctx, scene.window, "#{window_layout}"); err != nil || got != scene.remembered {
		t.Fatalf("the window it left is %q, %v; want %q", got, err, scene.remembered)
	}
}

// TestAgentBarActsOnNoPane covers the rows that name a member of the team
// rather than a pane of this server: every action says so, and none of them
// sends tmux a target it would have to guess at.
func TestAgentBarActsOnNoPane(t *testing.T) {
	bar := &agentBar{}
	row := team.Row{Name: "write-docs"}
	for _, action := range []struct {
		name string
		cmd  func(team.Row) tea.Cmd
	}{
		{"focus", bar.focus},
		{"zoom", bar.zoom},
		{"window", bar.window},
	} {
		t.Run(action.name, func(t *testing.T) {
			if got := note(t, action.cmd(row)); got != "write-docs runs in no pane of this server" {
				t.Fatalf("%s reported %q", action.name, got)
			}
		})
	}
}

func TestAgentBarFocus(t *testing.T) {
	cases := []struct {
		name   string
		inside bool
		want   []tmux.Command
	}{
		{
			name:   "a rail in a pane of the server moves its own client",
			inside: true,
			want:   []tmux.Command{{"switch-client", "-t", "%7"}, {"select-pane", "-t", "%7"}},
		},
		{
			name: "a rail outside the server only marks the pane",
			want: []tmux.Command{{"select-window", "-t", "%7"}, {"select-pane", "-t", "%7"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := agentBarFocus("%7", c.inside)
			if len(got) != len(c.want) {
				t.Fatalf("agentBarFocus() = %q", got)
			}
			for i := range got {
				if strings.Join(got[i], " ") != strings.Join(c.want[i], " ") {
					t.Fatalf("agentBarFocus() = %q, want %q", got, c.want)
				}
			}
		})
	}
}

func TestAgentBarTeam(t *testing.T) {
	cases := []struct {
		name  string
		panes []team.Pane
		want  string
	}{
		{name: "no panes at all"},
		{
			name:  "a workspace running no team",
			panes: []team.Pane{{ID: "%1", Session: "api", Role: team.RoleClaude}},
		},
		{
			name: "the team of another workspace is not ours",
			panes: []team.Pane{
				{ID: "%1", Session: "docs", Role: team.RoleTeammate, Team: "session-other"},
				{ID: "%2", Session: "api", Role: team.RoleClaude},
			},
		},
		{
			name: "the team our own panes belong to",
			panes: []team.Pane{
				{ID: "%1", Session: "docs", Role: team.RoleTeammate, Team: "session-other"},
				{ID: "%2", Session: "api", Role: team.RoleTeammate, Team: "session-8f3c1d2a"},
			},
			want: "session-8f3c1d2a",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentBarTeam(team.Input{Session: "api", Panes: c.panes}); got != c.want {
				t.Fatalf("agentBarTeam() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAgentBarPanes(t *testing.T) {
	got := agentBarPanes([]tmux.Pane{{
		ID: "%3", SessionName: "api", WindowID: "@2", WindowName: "claude",
		Role: "claude", State: "busy", Agent: "team-lead", AgentType: "team-lead",
		Team: "session-8f3c1d2a", Subagents: "2", Running: "a1=explore,b2=review",
		Active: true, WindowActive: true,
	}, {
		// An active pane of a window nobody is on is not the pane the user is
		// on, and a count that is not a number is a count nothing wrote.
		ID: "%9", SessionName: "api", Subagents: "many", Active: true, Dead: true,
	}})
	if len(got) != 2 {
		t.Fatalf("agentBarPanes() = %+v", got)
	}
	first := got[0]
	if first.ID != "%3" || first.Session != "api" || first.Window != "@2" || first.Role != "claude" {
		t.Fatalf("the pane is %+v", first)
	}
	if first.Subagents != 2 || len(first.Running) != 2 || first.Running[1].Type != "review" || !first.Active {
		t.Fatalf("the subagents of the pane are %d %+v", first.Subagents, first.Running)
	}
	if second := got[1]; second.Active || second.Subagents != 0 || !second.Dead {
		t.Fatalf("the second pane is %+v", second)
	}
}

func TestAgentBarEnviron(t *testing.T) {
	h := newTestHost(t)
	h.env["TMUX"], h.env["TMUX_PANE"] = "/tmp/outer,1,0", "%7"
	h.refreshEnviron()
	env := agentBarEnviron(h.Host)
	for _, want := range []string{"TMUX=/tmp/outer,1,0", "TMUX_PANE=%7"} {
		if !containsEnv(env, want) {
			t.Fatalf("the rail runs without %s: %q", want, env)
		}
	}
	// A rail outside tmux carries neither, and never an empty assignment.
	h.env["TMUX"], h.env["TMUX_PANE"] = "", ""
	h.refreshEnviron()
	for _, unwanted := range []string{"TMUX=", "TMUX_PANE="} {
		if containsEnv(agentBarEnviron(h.Host), unwanted) {
			t.Fatalf("the rail runs with an empty %s", unwanted)
		}
	}
}

// containsEnv reports an exact assignment in an environment.
func containsEnv(env []string, assignment string) bool {
	for _, kv := range env {
		if kv == assignment {
			return true
		}
	}
	return false
}
