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

// messageFinished is a message form that ended on a result, in place of the
// questions.
type messageFinished struct {
	res  tui.MessageResult
	done bool
}

func (m messageFinished) Init() tea.Cmd                       { return tea.Quit }
func (m messageFinished) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m messageFinished) View() tea.View                      { return tea.NewView("") }
func (m messageFinished) Result() (tui.MessageResult, bool)   { return m.res, m.done }

// stopFinished is the same for the stop form.
type stopFinished struct {
	res  tui.StopResult
	done bool
}

func (m stopFinished) Init() tea.Cmd                       { return tea.Quit }
func (m stopFinished) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m stopFinished) View() tea.View                      { return tea.NewView("") }
func (m stopFinished) Result() (tui.StopResult, bool)      { return m.res, m.done }

// runSteerForm runs one of the forms on a terminal of the test's own, and
// returns what it printed.
func runSteerForm(t *testing.T, form func(d Deps, cmd *cobra.Command, ui rootUI) error) (out string, err error) {
	t.Helper()
	e := newCLIEnv(t)
	term := newAgentsTerm(t)
	ui := rootUI{host: e.host, config: config.Default(), term: e.term}
	d := ProcessDeps()
	wait := term.start(t, func(cmd *cobra.Command) error { return form(d, cmd, ui) })
	err = wait()
	return term.out.String(), err
}

// TestMessageForm drives the command's own half of message: what it sends,
// what it reports, and what it leaves alone.
func TestMessageForm(t *testing.T) {
	cases := []struct {
		name      string
		model     messageFinished
		sendErr   error
		wantSend  bool
		wantErr   string
		outHas    string
		outHasNot string
	}{
		{
			name:     "a message sent",
			model:    messageFinished{res: tui.MessageResult{Text: "read the router", Sent: true}, done: true},
			wantSend: true,
			outHas:   "Sent to review-api in workspace api",
		},
		{
			name:      "a canceled form",
			model:     messageFinished{res: tui.MessageResult{Text: "read the router"}, done: true},
			outHas:    "Nothing was sent",
			outHasNot: "Sent to",
		},
		{
			name:   "a form that ended before it was answered",
			model:  messageFinished{res: tui.MessageResult{Text: "read the router", Sent: true}},
			outHas: "Nothing was sent",
		},
		{
			name:      "an agent that refuses it",
			model:     messageFinished{res: tui.MessageResult{Text: "read the router", Sent: true}, done: true},
			sendErr:   errors.New("review-api is waiting on you"),
			wantSend:  true,
			wantErr:   "is waiting on you",
			outHasNot: "Sent to",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			view := app.MessageView{
				Send: func(_ context.Context, res tui.MessageResult) (app.SteerOutcome, error) {
					sent = true
					if res != tc.model.res {
						t.Errorf("sent %+v, want %+v", res, tc.model.res)
					}
					return app.SteerOutcome{Session: "api", Agent: "review-api", Pane: "%2"}, tc.sendErr
				},
			}
			out, err := runSteerForm(t, func(d Deps, cmd *cobra.Command, ui rootUI) error {
				return d.messageForm(cmd, ui, view, tc.model)
			})
			checkSteerRun(t, err, tc.wantErr, sent, tc.wantSend, out, tc.outHas, tc.outHasNot)
		})
	}
}

// TestStopForm drives the command's own half of stop.
func TestStopForm(t *testing.T) {
	asked := tui.StopResult{Way: tui.StopAskLead, Message: tui.StopMessage("review-api"), Sent: true}
	now := tui.StopResult{Way: tui.StopNow, Typed: "review-api", Sent: true}
	cases := []struct {
		name      string
		model     stopFinished
		out       app.SteerOutcome
		stopErr   error
		wantStop  bool
		wantErr   string
		outHas    string
		outHasNot string
	}{
		{
			name:     "the lead is asked",
			model:    stopFinished{res: asked, done: true},
			out:      app.SteerOutcome{Session: "api", Agent: "review-api", Pane: "%1"},
			wantStop: true,
			outHas:   "Asked the lead of workspace api to shut down review-api:\nShut down the teammate review-api: its work is done.",
		},
		{
			name:     "stopped now",
			model:    stopFinished{res: now, done: true},
			out:      app.SteerOutcome{Session: "api", Agent: "review-api", Stopped: true},
			wantStop: true,
			outHas:   "Stopped review-api in workspace api: its pane is closed",
		},
		{
			name:      "a canceled form",
			model:     stopFinished{res: tui.StopResult{Way: tui.StopNow}, done: true},
			outHas:    "Nothing was stopped",
			outHasNot: "Stopped review-api",
		},
		{
			name:   "a form that ended before it was answered",
			model:  stopFinished{res: now},
			outHas: "Nothing was stopped",
		},
		{
			name:      "a teammate that is no longer there",
			model:     stopFinished{res: now, done: true},
			stopErr:   errors.New("review-api is gone from workspace api"),
			wantStop:  true,
			wantErr:   "is gone from workspace api",
			outHasNot: "Stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stopped := false
			view := app.StopView{
				Stop: func(_ context.Context, res tui.StopResult) (app.SteerOutcome, error) {
					stopped = true
					if res != tc.model.res {
						t.Errorf("stopped %+v, want %+v", res, tc.model.res)
					}
					return tc.out, tc.stopErr
				},
			}
			out, err := runSteerForm(t, func(d Deps, cmd *cobra.Command, ui rootUI) error {
				return d.stopForm(cmd, ui, view, tc.model)
			})
			checkSteerRun(t, err, tc.wantErr, stopped, tc.wantStop, out, tc.outHas, tc.outHasNot)
		})
	}
}

// checkSteerRun reads back one run of a form: its error, whether it acted,
// and what it printed.
func checkSteerRun(t *testing.T, err error, wantErr string, acted, wantActed bool, out, has, hasNot string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("the form failed: %v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("the form = %v, want %q", err, wantErr)
	}
	if acted != wantActed {
		t.Fatalf("acted %v, want %v", acted, wantActed)
	}
	if has != "" && !strings.Contains(out, has) {
		t.Fatalf("stdout missing %q:\n%s", has, out)
	}
	if hasNot != "" && strings.Contains(out, hasNot) {
		t.Fatalf("stdout carries %q:\n%s", hasNot, out)
	}
}

// TestSteerCommandsRefuse covers what both commands refuse before any form
// opens: a run with no terminal to answer it on, a run that names no agent,
// and an agent named in a way that is not a pane id.
func TestSteerCommandsRefuse(t *testing.T) {
	cases := []struct {
		name        string
		interactive bool
		args        []string
		says        string
	}{
		{name: "message with no terminal", args: []string{"message", "--to", "%1"}, says: "needs a terminal"},
		{name: "stop with no terminal", args: []string{"stop", "--to", "%1"}, says: "needs a terminal"},
		{name: "message to nobody", interactive: true, args: []string{"message"}, says: `"to" not set`},
		{name: "stop of nobody", interactive: true, args: []string{"stop"}, says: `"to" not set`},
		{name: "message to a name", interactive: true, args: []string{"message", "--session", "api", "--to", "review-api"}, says: "is not a pane id"},
		{name: "stop of a pattern", interactive: true, args: []string{"stop", "--session", "api", "--to", "%"}, says: "is not a pane id"},
		{name: "message outside a workspace", interactive: true, args: []string{"message", "--to", "%1"}, says: "pass --session <name>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCLIEnv(t)
			e.term.Interactive = tc.interactive
			code, out, errOut := e.run(t, tc.args...)
			if code != 1 || !containsFolded(errOut, tc.says) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, tc.says)
			}
		})
	}
}
