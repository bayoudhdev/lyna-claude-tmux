package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// writeTeammateTranscript writes a transcript under the Claude configuration
// directory of the test and names it on a pane, the way the hooks name it, and
// returns the path the pane carries.
func writeTeammateTranscript(t *testing.T, h *testHost, s *Server, pane, body string) string {
	t.Helper()
	dir := filepath.Join(h.root, ".claude", "projects", "-work-api")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "teammate.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.Run(tmuxtest.Context(t), "set-option", "-p", "-t", pane, tmux.OptTranscript, path); err != nil {
		t.Fatal(err)
	}
	return path
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
	// The transcript the hooks name on the teammate's pane, which is what the
	// rail reads the usage of that agent from.
	transcriptPath := writeTeammateTranscript(t, h, s, scene.pane, usageLine("m1", 5)+usageLine("m2", 7))

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
	// The row names the transcript of its pane and carries what that transcript
	// says the agent has spent: two messages, twelve tokens of output.
	if mate.Transcript != transcriptPath {
		t.Fatalf("the teammate row reads transcript %q, want %q", mate.Transcript, transcriptPath)
	}
	if mate.Usage.Sum.Output != 12 || mate.Usage.Last.Output != 7 {
		t.Fatalf("the teammate spent %+v, want the two messages of its transcript", mate.Usage)
	}
	if lead.Usage.Sum.Total() != 0 {
		t.Fatalf("the lead names no transcript and spent %+v", lead.Usage.Sum)
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

// TestAgentBarSpawnAction covers which rail opens forms, the one that starts
// an agent and the ones that message and stop one: a rail drawn in a pane of
// the workspace opens them in a popup over itself, and no other rail does.
func TestAgentBarSpawnAction(t *testing.T) {
	cases := []struct {
		name  string
		popup bool
		// where is the server the rail runs in: ours, one of the user's own,
		// or none, for a rail following the workspace from outside tmux.
		where string
		noExe bool
		want  bool
	}{
		{name: "a rail in a pane of the workspace", where: "ours", want: true},
		{name: "a rail that is itself a popup", popup: true, where: "ours"},
		{name: "a rail on a tmux server of the user's own", where: "theirs"},
		{name: "a rail following the workspace from outside", where: "none"},
		{name: "a rail with no binary to run", where: "ours", noExe: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHost(t)
			// The scene leaves the host in a pane of the workspace.
			openTeammateScene(t, h, openServer(t, h), 240, 60)
			switch tc.where {
			case "theirs":
				// A server of the user's own, running a session named like
				// the workspace on ours.
				theirs := tmuxtest.Start(t)
				ctx := tmuxtest.Context(t)
				if _, err := theirs.Client.Run(ctx, "new-session", "-d", "-s", "api", "sleep 3600"); err != nil {
					t.Fatal(err)
				}
				pane, err := theirs.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
				if err != nil {
					t.Fatal(err)
				}
				h.env["TMUX"], h.env["TMUX_PANE"] = tmuxtest.SocketPath(theirs.Name)+",1,0", pane
			case "none":
				h.env["TMUX"], h.env["TMUX_PANE"] = "", ""
			}
			if tc.noExe {
				h.Exe = ""
			}
			h.refreshEnviron()
			view, err := OpenAgentBar(tmuxtest.Context(t), h.Host, AgentBarRequest{Session: "api", Popup: tc.popup})
			if err != nil {
				t.Fatal(err)
			}
			a := view.Options.Actions
			for name, got := range map[string]bool{"spawn": a.Spawn != nil, "message": a.Message != nil, "stop": a.Stop != nil} {
				if got != tc.want {
					t.Fatalf("the rail offers %s %v, want %v", name, got, tc.want)
				}
			}
		})
	}
}

// TestAgentBarPopups pins the popups the rail opens: our own binary over the
// rail's own pane, for the workspace the rail follows, kept on screen when the
// form fails so its error can be read.
func TestAgentBarPopups(t *testing.T) {
	bar := &agentBar{session: "api", exe: "/opt/lmux", pane: "%4"}
	head := func(title string) tmux.Command {
		return tmux.Command{
			"display-popup", "-t", "%4", "-E", "-E", "-w", agentBarPopupWidth, "-h", agentBarPopupHeight,
			"-T", " " + title + " ", "--", "/opt/lmux",
		}
	}
	cases := []struct {
		name  string
		popup func() (tmux.Command, error)
		want  tmux.Command
	}{
		{name: "spawn", popup: bar.spawnPopup, want: append(head("spawn"), "spawn", "--session", "api")},
		{
			name:  "message",
			popup: func() (tmux.Command, error) { return bar.steerPopup(popupMessageTitle, "%7") },
			want:  append(head("message"), "message", "--session", "api", "--to", "%7"),
		},
		{
			name:  "stop",
			popup: func() (tmux.Command, error) { return bar.steerPopup(popupStopTitle, "%7") },
			want:  append(head("stop"), "stop", "--session", "api", "--to", "%7"),
		},
		{name: "tasks", popup: bar.tasksPopup, want: append(head("tasks"), "tasks", "--session", "api", "--popup")},
		{
			name:  "transcript",
			popup: func() (tmux.Command, error) { return bar.transcriptPopup("%7", "") },
			want:  append(head("transcript"), "transcript", "--session", "api", "--to", "%7"),
		},
		{
			name:  "the transcript of a subagent",
			popup: func() (tmux.Command, error) { return bar.transcriptPopup("%7", "a3f2e1d0c9b8a7f6e") },
			want: append(head("transcript"),
				"transcript", "--session", "api", "--to", "%7", "--agent", "a3f2e1d0c9b8a7f6e"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.popup()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cmd, tc.want) {
				t.Fatalf("the popup is\n%q\nwant\n%q", cmd, tc.want)
			}
		})
	}
	// A rail with no pane of its own opens nothing, and says why.
	if _, err := (&agentBar{session: "api", exe: "/opt/lmux"}).steerPopup(popupMessageTitle, "%7"); err == nil {
		t.Fatal("a popup opened over no pane")
	}
}

// TestAgentBarOpensItsPopups runs the actions the rail hands its keys and
// reads what reached tmux: each key opens its own form, about the row it was
// pressed on.
func TestAgentBarOpensItsPopups(t *testing.T) {
	row := team.Row{Group: team.GroupTeammates, Name: "review-api", Pane: "%7"}
	cases := []struct {
		name string
		run  func(a tui.AgentBarActions) tea.Cmd
		want []string
	}{
		{name: "s", run: func(a tui.AgentBarActions) tea.Cmd { return a.Spawn() }, want: []string{"spawn", "--session", "api"}},
		{name: "m", run: func(a tui.AgentBarActions) tea.Cmd { return a.Message(row) }, want: []string{"message", "--session", "api", "--to", "%7"}},
		{name: "x", run: func(a tui.AgentBarActions) tea.Cmd { return a.Stop(row) }, want: []string{"stop", "--session", "api", "--to", "%7"}},
		{name: "t", run: func(a tui.AgentBarActions) tea.Cmd { return a.Tasks() }, want: []string{"tasks", "--session", "api", "--popup"}},
		{
			name: "r", run: func(a tui.AgentBarActions) tea.Cmd { return a.Transcript(row) },
			want: []string{"transcript", "--session", "api", "--to", "%7"},
		},
		{
			name: "r on a subagent",
			run: func(a tui.AgentBarActions) tea.Cmd {
				return a.Transcript(team.Row{
					Group: team.GroupSubagents, Name: "explore", Pane: "%7", AgentID: "a3f2e1d0c9b8a7f6e",
				})
			},
			want: []string{"transcript", "--session", "api", "--to", "%7", "--agent", "a3f2e1d0c9b8a7f6e"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			client := tmux.New(tmux.Options{Executor: tmux.ExecutorFunc(func(_ context.Context, _ string, args []string) (tmux.Result, error) {
				got = args
				return tmux.Result{}, nil
			})})
			bar := &agentBar{client: client, session: "api", exe: "/opt/lmux", pane: "%4"}
			if n := note(t, tc.run(bar.actions())); n != "" {
				t.Fatalf("the popup reported %q", n)
			}
			i := slices.Index(got, "display-popup")
			end := slices.Index(got, "--")
			if i < 0 || end < i {
				t.Fatalf("no popup reached tmux: %q", got)
			}
			if !slices.Equal(got[end+1:], append([]string{"/opt/lmux"}, tc.want...)) {
				t.Fatalf("the popup runs %q, want %q", got[end+1:], tc.want)
			}
		})
	}
}

// TestAgentBarActsOnNoPane covers the rows that name a member of the team
// rather than a pane of this server: every action says so, and none of them
// sends tmux a target it would have to guess at.
func TestAgentBarActsOnNoPane(t *testing.T) {
	bar := &agentBar{session: "api", exe: "/opt/lmux", pane: "%4"}
	row := team.Row{Name: "write-docs"}
	for _, action := range []struct {
		name string
		cmd  func(team.Row) tea.Cmd
	}{
		{"focus", bar.focus},
		{"zoom", bar.zoom},
		{"window", bar.window},
		{"message", bar.message},
		{"stop", bar.stop},
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
		Active: true, WindowActive: true, Transcript: "/work/.claude/projects/-work-api/s1.jsonl",
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
	// Without the transcript the rows carry no usage, whatever the agents spend.
	if first.Transcript != "/work/.claude/projects/-work-api/s1.jsonl" {
		t.Fatalf("the transcript of the pane is %q", first.Transcript)
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
