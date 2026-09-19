package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// spawnFinished is a form that ended on a result, in place of the questions.
type spawnFinished struct {
	res  tui.SpawnResult
	done bool
}

func (m spawnFinished) Init() tea.Cmd                       { return tea.Quit }
func (m spawnFinished) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m spawnFinished) View() tea.View                      { return tea.NewView("") }
func (m spawnFinished) Result() (tui.SpawnResult, bool)     { return m.res, m.done }

// TestSpawnForm drives the command's own half: what it starts, what it
// reports, and what it leaves alone.
func TestSpawnForm(t *testing.T) {
	asked := tui.SpawnRequest{Target: tui.SpawnLead, Agent: "api-developer", Prompt: "read the router"}
	own := tui.SpawnRequest{Target: tui.SpawnOwn, Name: "spike", Prompt: "try the other parser"}
	cases := []struct {
		name      string
		model     spawnFinished
		out       app.SpawnOutcome
		startErr  error
		wantStart bool
		wantErr   string
		outHas    []string
		errHas    []string
	}{
		{
			name:      "the lead is asked",
			model:     spawnFinished{res: tui.SpawnResult{Request: asked, Message: tui.SpawnMessage(asked), Sent: true}, done: true},
			out:       app.SpawnOutcome{Session: "api", Lead: "%1"},
			wantStart: true,
			outHas:    []string{"Asked the agent of workspace api", "Start a api-developer agent, and tell it: read the router"},
		},
		{
			name:      "an agent of its own",
			model:     spawnFinished{res: tui.SpawnResult{Request: own, Sent: true}, done: true},
			out:       app.SpawnOutcome{Session: "api", Window: app.WindowResult{Session: "api"}},
			wantStart: true,
			outHas:    []string{"Agent general-purpose is running in window spike of workspace api"},
		},
		{
			name:  "a window that was already open",
			model: spawnFinished{res: tui.SpawnResult{Request: own, Sent: true}, done: true},
			out: app.SpawnOutcome{Session: "api", Window: app.WindowResult{
				Session: "api", Existing: true, Warnings: []string{"remove old Claude settings files"},
			}},
			wantStart: true,
			outHas:    []string{"Window spike of workspace api is already open"},
			errHas:    []string{"remove old Claude settings files"},
		},
		{
			name:   "a canceled form",
			model:  spawnFinished{res: tui.SpawnResult{Request: asked}, done: true},
			outHas: []string{"Nothing was started"},
		},
		{
			name:   "a form that ended before it was answered",
			model:  spawnFinished{res: tui.SpawnResult{Request: asked, Sent: true}},
			outHas: []string{"Nothing was started"},
		},
		{
			name:      "a workspace that refuses the request",
			model:     spawnFinished{res: tui.SpawnResult{Request: asked, Message: tui.SpawnMessage(asked), Sent: true}, done: true},
			startErr:  errors.New("workspace api runs no agent to ask"),
			wantStart: true,
			wantErr:   "runs no agent to ask",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			started := false
			view := app.SpawnView{
				Start: func(_ context.Context, res tui.SpawnResult) (app.SpawnOutcome, error) {
					started = true
					if res != tc.model.res {
						t.Errorf("started %+v, want %+v", res, tc.model.res)
					}
					return tc.out, tc.startErr
				},
			}
			term := newAgentsTerm(t)
			ui := rootUI{host: e.host, config: config.Default(), term: e.term}
			d := ProcessDeps()
			wait := term.start(t, func(cmd *cobra.Command) error {
				return d.spawnForm(cmd, ui, view, tc.model)
			})
			err := wait()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("spawnForm: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("spawnForm = %v, want %q", err, tc.wantErr)
			}
			if started != tc.wantStart {
				t.Fatalf("started %v, want %v", started, tc.wantStart)
			}
			out, errOut := term.out.String(), term.errOut.String()
			for _, s := range tc.outHas {
				if !strings.Contains(out, s) {
					t.Fatalf("stdout missing %q:\n%s", s, out)
				}
			}
			for _, s := range tc.errHas {
				if !strings.Contains(errOut, s) {
					t.Fatalf("stderr missing %q:\n%s", s, errOut)
				}
			}
		})
	}
}

// TestSpawnNeedsATerminal keeps the form from being answered by a pipe: it is
// a form, and the workspace starts it in a popup.
func TestSpawnNeedsATerminal(t *testing.T) {
	e := newCLIEnv(t)
	e.term.Interactive = false
	code, out, errOut := e.run(t, "spawn")
	if code != 1 || !strings.Contains(errOut, "needs a terminal") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
