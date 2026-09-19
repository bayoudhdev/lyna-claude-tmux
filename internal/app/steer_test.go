package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// steerPanes is a reading of a server in the pure tests: a workspace with its
// lead, a teammate, a shell, the changes pane and the rail, a teammate that
// stopped, and the lead of another workspace.
func steerPanes() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%1", SessionName: "api", Role: tmux.RoleClaude},
		{ID: "%2", SessionName: "api", Role: tmux.RoleTeammate, Agent: "review-api", WindowName: "claude"},
		{ID: "%3", SessionName: "api", Role: tmux.RoleShell},
		{ID: "%4", SessionName: "api", Role: tmux.RoleChanges},
		{ID: "%5", SessionName: "api", Role: tmux.RoleAgents},
		{ID: "%6", SessionName: "api", Role: tmux.RoleTeammate, Agent: "build-api", Dead: true},
		{ID: "%7", SessionName: "api"},
		{ID: "%9", SessionName: "web", Role: tmux.RoleClaude},
	}
}

func TestMessageTarget(t *testing.T) {
	cases := []struct {
		name, id string
		// want is the agent found, and says what the refusal says.
		want, says string
	}{
		{name: "the lead", id: "%1", want: "api"},
		{name: "a teammate", id: "%2", want: "review-api"},
		{name: "a shell, which would run the message", id: "%3", says: "%3 runs no agent"},
		{name: "the changes pane", id: "%4", says: "%4 runs no agent"},
		{name: "the rail", id: "%5", says: "%5 runs no agent"},
		{name: "a pane of no role", id: "%7", says: "%7 runs no agent"},
		{name: "a teammate that stopped", id: "%6", says: "build-api has stopped"},
		{name: "the agent of another workspace", id: "%9", says: "%9 is not a pane of workspace api"},
		{name: "a pane the server does not have", id: "%40", says: "%40 is not a pane of workspace api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := messageTarget(steerPanes(), "api", tc.id)
			if tc.says != "" {
				if !errors.Is(err, ErrMessage) || !strings.Contains(err.Error(), tc.says) {
					t.Fatalf("messageTarget = %v, want %q", err, tc.says)
				}
				return
			}
			if err != nil || agentName(p) != tc.want {
				t.Fatalf("messageTarget = %q, %v; want %q", agentName(p), err, tc.want)
			}
		})
	}
}

func TestStopTarget(t *testing.T) {
	cases := []struct {
		name, id   string
		want, says string
	}{
		{name: "a teammate", id: "%2", want: "review-api"},
		// A teammate whose agent stopped is closed like any other: its pane is
		// what is left of it.
		{name: "a teammate that stopped", id: "%6", want: "build-api"},
		{name: "the lead", id: "%1", says: "api is the lead of workspace api; only a teammate is stopped here"},
		{name: "a shell", id: "%3", says: "%3 runs no teammate"},
		{name: "the rail", id: "%5", says: "%5 runs no teammate"},
		{name: "the lead of another workspace", id: "%9", says: "%9 is not a pane of workspace api"},
		{name: "a pane the server does not have", id: "%40", says: "%40 is not a pane of workspace api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := stopTarget(steerPanes(), "api", tc.id)
			if tc.says != "" {
				if !errors.Is(err, ErrStop) || !strings.Contains(err.Error(), tc.says) {
					t.Fatalf("stopTarget = %v, want %q", err, tc.says)
				}
				return
			}
			if err != nil || agentName(p) != tc.want {
				t.Fatalf("stopTarget = %q, %v; want %q", agentName(p), err, tc.want)
			}
		})
	}
	// A teammate is stopped by its name, so one with none is refused.
	nameless := []tmux.Pane{{ID: "%2", SessionName: "api", Role: tmux.RoleTeammate}}
	if _, err := stopTarget(nameless, "api", "%2"); !errors.Is(err, ErrStop) || !strings.Contains(err.Error(), "no name") {
		t.Fatalf("stopTarget on a nameless teammate = %v", err)
	}
}

func TestAgentName(t *testing.T) {
	cases := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{name: "a teammate by the name it was spawned under", pane: tmux.Pane{Role: tmux.RoleTeammate, Agent: "review-api", WindowName: "claude"}, want: "review-api"},
		{name: "a teammate that carries no name", pane: tmux.Pane{Role: tmux.RoleTeammate, WindowName: "review-api"}, want: "review-api"},
		{name: "a lead by its workspace", pane: tmux.Pane{Role: tmux.RoleClaude, SessionName: "api", WindowName: "claude"}, want: "api"},
		{name: "a lead named by its team", pane: tmux.Pane{Role: tmux.RoleClaude, SessionName: "api", Agent: "team-lead"}, want: "team-lead"},
		{name: "a name carrying a sequence", pane: tmux.Pane{Role: tmux.RoleTeammate, Agent: "review\x1b]0;x\x07-api"}, want: "review-api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentName(tc.pane); got != tc.want {
				t.Fatalf("agentName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSameAgent covers the reading taken right before a form acts: the pane
// has to be the agent the form was opened for, still acceptable to the form.
func TestSameAgent(t *testing.T) {
	was := tmux.Pane{ID: "%2", SessionName: "api", Role: tmux.RoleTeammate, Agent: "review-api"}
	with := func(change func(p *tmux.Pane)) []tmux.Pane {
		p := was
		change(&p)
		return []tmux.Pane{{ID: "%1", SessionName: "api", Role: tmux.RoleClaude}, p}
	}
	cases := []struct {
		name  string
		panes []tmux.Pane
		check func(tmux.Pane) error
		says  string
	}{
		{name: "the same teammate", panes: with(func(*tmux.Pane) {}), check: messageable},
		{name: "a teammate at work in a new state", panes: with(func(p *tmux.Pane) { p.State = "busy" }), check: stoppable},
		{name: "gone", panes: []tmux.Pane{{ID: "%1", SessionName: "api", Role: tmux.RoleClaude}}, check: messageable, says: "review-api is gone from workspace api"},
		{name: "moved to another workspace", panes: with(func(p *tmux.Pane) { p.SessionName = "web" }), check: messageable, says: "review-api moved to workspace web"},
		{name: "running something else", panes: with(func(p *tmux.Pane) { p.Role = tmux.RoleShell }), check: messageable, says: "runs something else now"},
		{name: "running another agent", panes: with(func(p *tmux.Pane) { p.Agent = "build-api" }), check: messageable, says: "the pane of review-api runs build-api now"},
		{name: "stopped since", panes: with(func(p *tmux.Pane) { p.Dead = true }), check: messageable, says: "review-api has stopped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sameAgent(tc.panes, was, tc.check, ErrMessage)
			if tc.says == "" {
				if err != nil || got.ID != was.ID {
					t.Fatalf("sameAgent = %+v, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("sameAgent = %v, want %q", err, tc.says)
			}
		})
	}
}

// steerScene is a workspace whose agents write down what they are typed: the
// lead and a teammate each run cat into a file of their own, beside a shell
// pane, which no message may reach. A second workspace runs on the same
// server.
type steerScene struct {
	*testHost
	server           *Server
	lead, mate       string
	shell, elsewhere string
	leadOut, mateOut string
}

func newSteerScene(t *testing.T) *steerScene {
	t.Helper()
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)
	sc := &steerScene{
		testHost: h, server: s,
		leadOut: filepath.Join(h.root, "lead.txt"), mateOut: filepath.Join(h.root, "mate.txt"),
	}
	writeDown := func(out string) []string { return []string{"/bin/sh", "-c", `exec cat > "$1"`, "sh", out} }
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	built, err := s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: "api", Project: h.root, Width: 200, Height: 50,
		Window: tmux.WindowSpec{Name: "claude", Dir: h.root, Plan: plan, Procs: []tmux.PaneProcess{{Argv: writeDown(sc.leadOut)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.lead = built.Panes[0]
	split := func(argv ...string) string {
		t.Helper()
		out, err := s.Client.Run(ctx, "split-window", append([]string{"-d", "-t", sc.lead, "-P", "-F", "#{pane_id}", "--"}, argv...)...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	sc.mate = split(writeDown(sc.mateOut)...)
	sc.shell = split("sleep", "3600")
	if _, err := s.Client.Run(ctx, "new-session", "-d", "-s", "web", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	if sc.elsewhere, err = s.Client.Display(ctx, tmux.ExactSession("web"), "#{pane_id}"); err != nil {
		t.Fatal(err)
	}
	sc.set(t, sc.mate, tmux.OptRole, tmux.RoleTeammate)
	sc.set(t, sc.mate, tmux.OptAgent, "review-api")
	sc.set(t, sc.shell, tmux.OptRole, tmux.RoleShell)
	sc.set(t, sc.elsewhere, tmux.OptRole, tmux.RoleClaude)
	return sc
}

// set writes a pane option the way the hooks and the workspace write them.
func (sc *steerScene) set(t *testing.T, pane, option, value string) {
	t.Helper()
	if _, err := sc.server.Client.Run(tmuxtest.Context(t), "set-option", "-p", "-t", pane, option, value); err != nil {
		t.Fatal(err)
	}
}

// typed is what the program of a pane has been typed so far.
func typed(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// panesOf lists the pane ids of a workspace.
func (sc *steerScene) panesOf(t *testing.T, session string) []string {
	t.Helper()
	panes, err := sc.server.Client.ListPanes(tmuxtest.Context(t), tmux.ExactSession(session))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(panes))
	for _, p := range panes {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestOpenMessage covers which panes the form opens on: an agent of the
// workspace, and nothing else.
func TestOpenMessage(t *testing.T) {
	sc := newSteerScene(t)
	ctx := tmuxtest.Context(t)
	cases := []struct {
		name string
		to   func() string
		// want is the agent the form is drawn for, and says what the refusal
		// says.
		want, says string
	}{
		{name: "the lead", to: func() string { return sc.lead }, want: "api"},
		{name: "a teammate", to: func() string { return sc.mate }, want: "review-api"},
		{name: "a shell", to: func() string { return sc.shell }, says: "runs no agent"},
		{name: "the agent of another workspace", to: func() string { return sc.elsewhere }, says: "is not a pane of workspace api"},
		{name: "an agent named instead of identified", to: func() string { return "review-api" }, says: `"review-api" is not a pane id`},
		{name: "a pane selected by pattern", to: func() string { return "%" }, says: "is not a pane id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := sc.server.OpenMessage(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: tc.to()})
			if tc.says != "" {
				if !errors.Is(err, ErrMessage) || !strings.Contains(err.Error(), tc.says) {
					t.Fatalf("OpenMessage = %v, want %q", err, tc.says)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if view.Options.Agent != tc.want || view.Options.Workspace != "api" {
				t.Fatalf("the form is drawn for %q of %q", view.Options.Agent, view.Options.Workspace)
			}
		})
	}
	// A workspace that is not running has no agent to message.
	_, err := sc.server.OpenMessage(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "gone"}, To: sc.mate})
	if !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("OpenMessage on no workspace = %v", err)
	}
}

// TestIntegrationMessageTypesAtTheAgent sends a message on a real server and
// reads what the teammate was typed: the whole line and the Enter that submits
// it, and nothing typed at any other pane.
func TestIntegrationMessageTypesAtTheAgent(t *testing.T) {
	sc := newSteerScene(t)
	ctx := tmuxtest.Context(t)
	view, err := sc.server.OpenMessage(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
	if err != nil {
		t.Fatal(err)
	}
	const line = "read the router again, and check the rate limiter"
	out, err := view.Send(ctx, tui.MessageResult{Text: line, Sent: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != (SteerOutcome{Session: "api", Agent: "review-api", Pane: sc.mate}) {
		t.Fatalf("outcome %+v", out)
	}
	tmuxtest.WaitFor(t, "the teammate to be typed at", func() bool {
		return strings.Contains(typed(t, sc.mateOut), line+"\n")
	})
	if got := typed(t, sc.leadOut); got != "" {
		t.Fatalf("the lead was typed %q", got)
	}
}

// TestSendMessageRefuses covers every message the workspace will not type,
// and checks nothing reached the teammate for any of them.
func TestSendMessageRefuses(t *testing.T) {
	cases := []struct {
		name string
		// change is what happens to the workspace while the form is open.
		change func(t *testing.T, sc *steerScene)
		res    tui.MessageResult
		says   string
	}{
		{name: "a form that sent nothing", res: tui.MessageResult{Text: "read the router"}, says: "the form sent nothing"},
		{
			name: "a text that is not what would be typed",
			res:  tui.MessageResult{Text: "read the router\nrm -rf ~", Sent: true},
			says: "the text shown is not the text that would be sent",
		},
		{name: "no text at all", res: tui.MessageResult{Sent: true}, says: "the text shown is not the text that would be sent"},
		{
			name:   "a teammate waiting on the user",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptState, "waiting") },
			res:    tui.MessageResult{Text: "read the router", Sent: true},
			says:   "review-api is waiting on you; answer it in its pane, then send again",
		},
		{
			name:   "a pane that runs another agent now",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptAgent, "build-api") },
			res:    tui.MessageResult{Text: "read the router", Sent: true},
			says:   "runs build-api now",
		},
		{
			name:   "a pane that runs a shell now",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptRole, tmux.RoleShell) },
			res:    tui.MessageResult{Text: "read the router", Sent: true},
			says:   "runs something else now",
		},
		{
			name: "a teammate that stopped",
			change: func(t *testing.T, sc *steerScene) {
				sc.set(t, sc.mate, "remain-on-exit", "on")
				if _, err := sc.server.Client.Run(tmuxtest.Context(t), "send-keys", "-t", sc.mate, "C-d"); err != nil {
					t.Fatal(err)
				}
				tmuxtest.WaitFor(t, "the teammate to stop", func() bool {
					dead, err := sc.server.Client.Display(tmuxtest.Context(t), sc.mate, "#{pane_dead}")
					return err == nil && dead == "1"
				})
			},
			res:  tui.MessageResult{Text: "read the router", Sent: true},
			says: "review-api has stopped",
		},
		{
			name: "a teammate whose pane closed",
			change: func(t *testing.T, sc *steerScene) {
				if _, err := sc.server.Client.Run(tmuxtest.Context(t), "kill-pane", "-t", sc.mate); err != nil {
					t.Fatal(err)
				}
			},
			res:  tui.MessageResult{Text: "read the router", Sent: true},
			says: "review-api is gone from workspace api",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newSteerScene(t)
			ctx := tmuxtest.Context(t)
			view, err := sc.server.OpenMessage(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, sc)
			}
			_, err = view.Send(ctx, tc.res)
			if !errors.Is(err, ErrMessage) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("Send = %v, want %q", err, tc.says)
			}
			if got := typed(t, sc.mateOut); got != "" {
				t.Fatalf("the teammate was typed %q", got)
			}
		})
	}
}

// TestOpenStop covers which panes the stop form opens on, and the lead it
// offers to ask.
func TestOpenStop(t *testing.T) {
	sc := newSteerScene(t)
	ctx := tmuxtest.Context(t)
	view, err := sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
	if err != nil {
		t.Fatal(err)
	}
	if o := view.Options; o.Teammate != "review-api" || o.Workspace != "api" || !o.Lead {
		t.Fatalf("the form is drawn with %+v", o)
	}
	cases := []struct {
		name string
		to   string
		says string
	}{
		{name: "the lead", to: sc.lead, says: "api is the lead of workspace api"},
		{name: "a shell", to: sc.shell, says: "runs no teammate"},
		{name: "the agent of another workspace", to: sc.elsewhere, says: "is not a pane of workspace api"},
		{name: "a teammate named instead of identified", to: "review-api", says: "is not a pane id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: tc.to})
			if !errors.Is(err, ErrStop) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("OpenStop = %v, want %q", err, tc.says)
			}
		})
	}
	// A workspace that runs no lead has none to ask.
	sc.set(t, sc.lead, tmux.OptRole, tmux.RoleShell)
	view, err = sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
	if err != nil || view.Options.Lead {
		t.Fatalf("OpenStop = %+v, %v; want no lead to ask", view.Options, err)
	}
}

// TestIntegrationStopAsksTheLead asks the lead to shut the teammate down and
// reads what the lead was typed. The teammate is left running: shutting it
// down is the lead's to do.
func TestIntegrationStopAsksTheLead(t *testing.T) {
	sc := newSteerScene(t)
	ctx := tmuxtest.Context(t)
	view, err := sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
	if err != nil {
		t.Fatal(err)
	}
	want := tui.StopMessage("review-api")
	out, err := view.Stop(ctx, tui.StopResult{Way: tui.StopAskLead, Message: want, Sent: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != (SteerOutcome{Session: "api", Agent: "review-api", Pane: sc.lead}) {
		t.Fatalf("outcome %+v", out)
	}
	tmuxtest.WaitFor(t, "the lead to be typed at", func() bool {
		return strings.Contains(typed(t, sc.leadOut), want+"\n")
	})
	if got := typed(t, sc.mateOut); got != "" {
		t.Fatalf("the teammate was typed %q", got)
	}
	if !slices.Contains(sc.panesOf(t, "api"), sc.mate) {
		t.Fatal("asking the lead closed the teammate's pane")
	}
}

// TestIntegrationStopNowClosesOnlyTheTeammate stops a teammate on its name
// typed out, and reads the server after: its pane is closed, and every other
// pane, of this workspace and of the other one, is still running.
func TestIntegrationStopNowClosesOnlyTheTeammate(t *testing.T) {
	sc := newSteerScene(t)
	ctx := tmuxtest.Context(t)
	view, err := sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
	if err != nil {
		t.Fatal(err)
	}
	out, err := view.Stop(ctx, tui.StopResult{Way: tui.StopNow, Typed: "review-api", Sent: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != (SteerOutcome{Session: "api", Agent: "review-api", Stopped: true}) {
		t.Fatalf("outcome %+v", out)
	}
	want := []string{sc.lead, sc.shell}
	slices.Sort(want)
	if got := sc.panesOf(t, "api"); !slices.Equal(got, want) {
		t.Fatalf("workspace api has panes %q, want %q", got, want)
	}
	if got := sc.panesOf(t, "web"); !slices.Equal(got, []string{sc.elsewhere}) {
		t.Fatalf("workspace web has panes %q", got)
	}
	if got := typed(t, sc.leadOut) + typed(t, sc.mateOut); got != "" {
		t.Fatalf("stopping it now typed %q", got)
	}
}

// TestE2ESteerFromTheRail runs what the rail's m and x keys do, on a real
// server with a client attached: the popup opens over the rail's pane, the
// real lyna-tmux binary draws the form in it, the keys a user types answer
// it, and the teammate is typed at or closed.
func TestE2ESteerFromTheRail(t *testing.T) {
	lmux := fakeclaude.BuildProgram(t, "github.com/bayoudhdev/lyna-claude-tmux/cmd/lmux", "lmux")
	row := func(sc *steerScene) team.Row {
		return team.Row{Group: team.GroupTeammates, Name: "review-api", Pane: sc.mate}
	}
	cases := []struct {
		name   string
		action func(b *agentBar, sc *steerScene) tea.Cmd
		// answer types the form's answers, and done waits for what they did.
		answer func(t *testing.T, n *tmuxtest.Nested)
		done   func(t *testing.T, sc *steerScene)
	}{
		{
			name:   "m types the message into the teammate's pane",
			action: func(b *agentBar, sc *steerScene) tea.Cmd { return b.message(row(sc)) },
			answer: func(t *testing.T, n *tmuxtest.Nested) {
				n.WaitScreen(t, "Message to review-api", false)
				n.Literal(t, "check the rate limiter")
				n.Keys(t, "Enter")
				n.WaitScreen(t, "review-api in workspace api is sent, exactly:", false)
				n.Keys(t, "Enter")
			},
			done: func(t *testing.T, sc *steerScene) {
				tmuxtest.WaitFor(t, "the teammate to be typed at", func() bool {
					return typed(t, sc.mateOut) == "check the rate limiter\n"
				})
			},
		},
		{
			name:   "x closes the teammate's pane on its name typed out",
			action: func(b *agentBar, sc *steerScene) tea.Cmd { return b.stop(row(sc)) },
			answer: func(t *testing.T, n *tmuxtest.Nested) {
				n.WaitScreen(t, "How to stop review-api", false)
				n.Keys(t, "Down", "Enter")
				n.WaitScreen(t, "Type review-api to stop it now", false)
				n.Literal(t, "review-api")
				n.Keys(t, "Enter")
			},
			done: func(t *testing.T, sc *steerScene) {
				want := []string{sc.lead, sc.shell}
				slices.Sort(want)
				tmuxtest.WaitFor(t, "the teammate's pane to close", func() bool {
					return slices.Equal(sc.panesOf(t, "api"), want)
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newSteerScene(t)
			inner := &tmuxtest.Server{Client: sc.server.Client, Name: sc.env[session.EnvSocketName], Bin: sc.TmuxBin}
			n := tmuxtest.Attach(t, inner, "api", 200, 50)
			bar := &agentBar{client: sc.server.Client, session: "api", exe: lmux, pane: sc.lead}
			// display-popup returns when the popup closes, which is when the
			// form is done with.
			closed := make(chan tea.Msg, 1)
			go func() { closed <- tc.action(bar, sc)() }()
			tc.answer(t, n)
			tc.done(t, sc)
			select {
			case msg := <-closed:
				if msg != nil {
					t.Fatalf("the popup reported %v", msg)
				}
			case <-tmuxtest.Context(t).Done():
				t.Fatalf("the popup stayed open:\n%s", n.Screen(t))
			}
			if got := typed(t, sc.leadOut); got != "" {
				t.Fatalf("the lead was typed %q", got)
			}
		})
	}
}

// TestStopRefuses covers every stop the workspace will not carry out, and
// checks that the teammate's pane is still there and nothing was typed.
func TestStopRefuses(t *testing.T) {
	asked := tui.StopMessage("review-api")
	cases := []struct {
		name   string
		change func(t *testing.T, sc *steerScene)
		res    tui.StopResult
		says   string
	}{
		{name: "a form that stopped nothing", res: tui.StopResult{Way: tui.StopNow, Typed: "review-api"}, says: "the form stopped nothing"},
		{name: "a form that chose no way", res: tui.StopResult{Sent: true}, says: "the form chose no way to stop it"},
		{
			name: "a lead sent something else",
			res:  tui.StopResult{Way: tui.StopAskLead, Message: "Shut down every teammate: their work is done.", Sent: true},
			says: "the text shown is not the text that would be sent",
		},
		{
			name:   "a lead waiting on the user",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.lead, tmux.OptState, "waiting") },
			res:    tui.StopResult{Way: tui.StopAskLead, Message: asked, Sent: true},
			says:   "the lead of workspace api is waiting on you",
		},
		{
			name:   "a workspace whose lead is gone",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.lead, tmux.OptRole, tmux.RoleShell) },
			res:    tui.StopResult{Way: tui.StopAskLead, Message: asked, Sent: true},
			says:   "workspace api runs no agent to ask",
		},
		{
			name:   "a lead asked about a pane that runs another agent now",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptAgent, "build-api") },
			res:    tui.StopResult{Way: tui.StopAskLead, Message: asked, Sent: true},
			says:   "runs build-api now",
		},
		{name: "a name not typed out", res: tui.StopResult{Way: tui.StopNow, Typed: "review", Sent: true}, says: "the name typed is not review-api"},
		{name: "no name typed at all", res: tui.StopResult{Way: tui.StopNow, Sent: true}, says: "the name typed is not review-api"},
		{
			name:   "a pane that runs another agent now",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptAgent, "build-api") },
			res:    tui.StopResult{Way: tui.StopNow, Typed: "review-api", Sent: true},
			says:   "runs build-api now",
		},
		{
			name:   "a pane that became the lead",
			change: func(t *testing.T, sc *steerScene) { sc.set(t, sc.mate, tmux.OptRole, tmux.RoleClaude) },
			res:    tui.StopResult{Way: tui.StopNow, Typed: "review-api", Sent: true},
			says:   "runs something else now",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newSteerScene(t)
			ctx := tmuxtest.Context(t)
			view, err := sc.server.OpenStop(ctx, sc.Host, SteerRequest{Target: WindowTarget{Session: "api"}, To: sc.mate})
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, sc)
			}
			_, err = view.Stop(ctx, tc.res)
			if !errors.Is(err, ErrStop) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("Stop = %v, want %q", err, tc.says)
			}
			if !slices.Contains(sc.panesOf(t, "api"), sc.mate) {
				t.Fatal("a refused stop closed the teammate's pane")
			}
			if got := typed(t, sc.leadOut) + typed(t, sc.mateOut); got != "" {
				t.Fatalf("a refused stop typed %q", got)
			}
		})
	}
}
