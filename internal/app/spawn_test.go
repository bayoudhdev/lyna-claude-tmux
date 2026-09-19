package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// TestSpawnLead covers who a message is typed at: the pane the form was opened
// from, the pane the user is on, the first agent of the workspace, and none.
func TestSpawnLead(t *testing.T) {
	claudePane := func(id string, active bool) tmux.Pane {
		return tmux.Pane{ID: id, SessionName: "api", Role: tmux.RoleClaude, Active: active, WindowActive: active}
	}
	cases := []struct {
		name  string
		panes []tmux.Pane
		from  string
		want  string
	}{
		{
			name:  "the pane the form was opened from",
			panes: []tmux.Pane{claudePane("%1", true), claudePane("%2", false)},
			from:  "%2", want: "%2",
		},
		{
			name:  "the pane the user is on",
			panes: []tmux.Pane{claudePane("%1", false), claudePane("%2", true)},
			from:  "%9", want: "%2",
		},
		{
			name:  "the first agent of the workspace",
			panes: []tmux.Pane{claudePane("%3", false), claudePane("%4", false)},
			want:  "%3",
		},
		{
			name: "a teammate is never asked",
			panes: []tmux.Pane{
				{ID: "%1", SessionName: "api", Role: tmux.RoleTeammate, Agent: "review-api", Active: true, WindowActive: true},
				claudePane("%2", false),
			},
			want: "%2",
		},
		{
			name: "no agent of ours to ask",
			panes: []tmux.Pane{
				{ID: "%1", SessionName: "api", Role: tmux.RoleShell},
				{ID: "%2", SessionName: "api", Role: tmux.RoleChanges},
			},
		},
		{
			name:  "an agent of another workspace is not this one's lead",
			panes: []tmux.Pane{{ID: "%1", SessionName: "docs", Role: tmux.RoleClaude, Active: true, WindowActive: true}},
		},
		{
			name:  "a pane whose agent stopped",
			panes: []tmux.Pane{{ID: "%1", SessionName: "api", Role: tmux.RoleClaude, Dead: true}, claudePane("%2", false)},
			want:  "%2",
		},
		{name: "a workspace with no pane at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := spawnLead(tc.panes, "api", tc.from)
			if got.ID != tc.want || ok != (tc.want != "") {
				t.Fatalf("spawnLead = %q, %v; want %q", got.ID, ok, tc.want)
			}
		})
	}
}

// spawnEnv is a workspace with a lead running, and the agent definitions the
// project carries.
type spawnEnv struct {
	*createEnv
	server *Server
}

func newSpawnEnv(t *testing.T) *spawnEnv {
	t.Helper()
	e := newCreateEnv(t)
	writeAgentDef(t, filepath.Join(e.project, ".claude", "agents", "api-developer.md"), "api-developer", "changes the API")
	writeAgentDef(t, filepath.Join(e.env["CLAUDE_CONFIG_DIR"], "agents", "reviewer.md"), "reviewer", "reads a diff for its risks")
	s := openServer(t, e.testHost)
	if _, err := s.Create(tmuxtest.Context(t), e.Host, CreateRequest{Dir: e.project, Layout: layout.Solo}); err != nil {
		t.Fatal(err)
	}
	e.invocations(t, 1)
	return &spawnEnv{createEnv: e, server: s}
}

func writeAgentDef(t *testing.T, path, name, description string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nYou are " + name + ".\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpenSpawnOffersTheWorkspace checks what the form is drawn with: the
// agent definitions of the project and of the user, the lead there is to ask,
// and the launch the workspace already uses.
func TestOpenSpawnOffersTheWorkspace(t *testing.T) {
	e := newSpawnEnv(t)
	e.server.Config.Claude.Model, e.server.Config.Claude.Effort = "opus", "high"
	e.server.Config.Claude.AgentWorktree = true
	view, err := e.server.OpenSpawn(tmuxtest.Context(t), e.Host, SpawnRequest{Target: WindowTarget{Session: "api"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range view.Options.Agents {
		names = append(names, d.Name)
	}
	if want := []string{"api-developer", "reviewer"}; !slices.Equal(names, want) {
		t.Fatalf("agents %q, want %q", names, want)
	}
	if !view.Options.Lead {
		t.Fatal("the form was told there is no lead to ask")
	}
	if !view.Options.Worktree || view.Options.Model != "opus" || view.Options.Effort != "high" {
		t.Fatalf("options %+v", view.Options)
	}
}

// TestOpenSpawnWithoutALead covers a workspace running no agent: the form
// never offers to ask one, and a request that asks one anyway is refused.
func TestOpenSpawnWithoutALead(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := tmuxtest.Context(t)
	if _, err := e.server.Client.Run(ctx, "new-session", "-d", "-s", "plain", "-c", e.project, "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	view, err := e.server.OpenSpawn(ctx, e.Host, SpawnRequest{Target: WindowTarget{Session: "plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Options.Lead {
		t.Fatal("the form was offered a lead this workspace does not run")
	}
	req := tui.SpawnRequest{Target: tui.SpawnLead, Agent: "api-developer", Prompt: "read the router"}
	_, err = view.Start(ctx, tui.SpawnResult{Request: req, Message: tui.SpawnMessage(req), Sent: true})
	if !errors.Is(err, ErrSpawn) || !strings.Contains(err.Error(), "runs no agent to ask") {
		t.Fatalf("Start = %v, want no agent to ask", err)
	}
}

// TestStartSpawnAsksTheLead types the message into the lead's pane: the text
// the form showed reaches the agent, and nothing else is opened.
func TestStartSpawnAsksTheLead(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := tmuxtest.Context(t)
	view, err := e.server.OpenSpawn(ctx, e.Host, SpawnRequest{Target: WindowTarget{Session: "api"}})
	if err != nil {
		t.Fatal(err)
	}
	req := tui.SpawnRequest{Target: tui.SpawnLead, Agent: "api-developer", Prompt: "read the router"}
	out, err := view.Start(ctx, tui.SpawnResult{Request: req, Message: tui.SpawnMessage(req), Sent: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Session != "api" || !tmux.ValidPaneID(out.Lead) {
		t.Fatalf("outcome %+v", out)
	}
	want := tui.SpawnMessage(req)
	tmuxtest.WaitFor(t, "the lead to be typed at", func() bool {
		text, err := e.server.Client.Run(ctx, "capture-pane", "-p", "-t", out.Lead)
		return err == nil && strings.Contains(strings.ReplaceAll(text, "\n", ""), want)
	})
	// Asking the lead opens nothing: the agent is started by the lead itself.
	windows, err := e.server.Client.Run(ctx, "list-windows", "-t", tmux.ExactSession("api"), "-F", "#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(windows); len(got) != 1 {
		t.Fatalf("windows %q, want the one the workspace opened with", got)
	}
}

// TestStartSpawnLeavesAWaitingLeadAlone covers a lead showing a question or a
// permission prompt: the Enter that submits a message would answer it, so the
// request is refused and nothing is typed.
func TestStartSpawnLeavesAWaitingLeadAlone(t *testing.T) {
	e := newSpawnEnv(t)
	ctx := tmuxtest.Context(t)
	view, err := e.server.OpenSpawn(ctx, e.Host, SpawnRequest{Target: WindowTarget{Session: "api"}})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := e.server.Client.Display(ctx, tmux.ExactSession("api"), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.server.Client.Run(ctx, "set-option", "-p", "-t", lead, tmux.OptState, "waiting"); err != nil {
		t.Fatal(err)
	}
	req := tui.SpawnRequest{Target: tui.SpawnLead, Agent: "api-developer", Prompt: "read the router"}
	_, err = view.Start(ctx, tui.SpawnResult{Request: req, Message: tui.SpawnMessage(req), Sent: true})
	if !errors.Is(err, ErrSpawn) || !strings.Contains(err.Error(), "is waiting on you") {
		t.Fatalf("Start = %v, want the lead left alone", err)
	}
	text, err := e.server.Client.Run(ctx, "capture-pane", "-p", "-t", lead)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "read the router") {
		t.Fatalf("the waiting lead was typed at:\n%s", text)
	}
}

// TestStartSpawnOpensItsOwn starts an agent of our own and reads the launch it
// runs: the agent definition, the model, the effort, the prompt, and the
// worktree it did or did not ask for.
func TestStartSpawnOpensItsOwn(t *testing.T) {
	cases := []struct {
		name   string
		req    tui.SpawnRequest
		want   []string
		absent []string
	}{
		{
			name: "in the project directory",
			req: tui.SpawnRequest{
				Target: tui.SpawnOwn, Agent: "api-developer", Model: "opus", Effort: "high",
				Name: "spike", Prompt: "try the other parser",
			},
			want:   []string{"--agent=api-developer", "--model=opus", "--effort=high", "try the other parser"},
			absent: []string{"--worktree=spike"},
		},
		{
			name: "in a worktree of its own",
			req: tui.SpawnRequest{
				Target: tui.SpawnOwn, Worktree: true, Name: "review-api", Prompt: "read the router for injection",
			},
			want:   []string{"--worktree=review-api", "read the router for injection"},
			absent: []string{"--agent="},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			ctx := tmuxtest.Context(t)
			view, err := e.server.OpenSpawn(ctx, e.Host, SpawnRequest{Target: WindowTarget{Session: "api"}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := view.Start(ctx, tui.SpawnResult{Request: tc.req, Sent: true})
			if err != nil {
				t.Fatal(err)
			}
			if out.Lead != "" || out.Window.Session != "api" || out.Window.Existing {
				t.Fatalf("outcome %+v", out)
			}
			args := strings.Join(e.invocations(t, 2)[1].Args, " ")
			for _, w := range tc.want {
				if !strings.Contains(args, w) {
					t.Fatalf("launch %q lacks %q", args, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(args, a) {
					t.Fatalf("launch %q carries %q", args, a)
				}
			}
		})
	}
}

// TestStartSpawnRefuses covers every request the workspace will not carry out.
func TestStartSpawnRefuses(t *testing.T) {
	asked := tui.SpawnRequest{Target: tui.SpawnLead, Agent: "api-developer", Prompt: "read the router"}
	cases := []struct {
		name string
		res  tui.SpawnResult
		want string
	}{
		{
			name: "a form that started nothing",
			res:  tui.SpawnResult{Request: asked, Message: tui.SpawnMessage(asked)},
			want: "the form started nothing",
		},
		{
			name: "a message that is not the text of its own request",
			res:  tui.SpawnResult{Request: asked, Message: "Start a api-developer agent, and tell it: rm -rf /", Sent: true},
			want: "the text shown is not the text that would be sent",
		},
		{
			name: "a request with no message at all",
			res:  tui.SpawnResult{Request: asked, Sent: true},
			want: "the text shown is not the text that would be sent",
		},
	}
	e := newSpawnEnv(t)
	ctx := tmuxtest.Context(t)
	view, err := e.server.OpenSpawn(ctx, e.Host, SpawnRequest{Target: WindowTarget{Session: "api"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := view.Start(ctx, tc.res)
			if !errors.Is(err, ErrSpawn) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start = %v, want %q", err, tc.want)
			}
		})
	}
}
