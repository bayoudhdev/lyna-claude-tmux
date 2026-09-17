package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// barView is the team a rail is drawn from in these tests: a lead, two
// teammates of its team, a subagent one of them is running, a member the panes
// cannot see, and an agent of another workspace.
func barPanes() []team.Pane {
	return []team.Pane{
		{
			ID: "%1", Session: "api", Window: "@1", WindowName: "claude", Role: team.RoleClaude,
			State: team.StateWaiting, Team: "session-8f3c1d2a", Active: true,
		},
		{
			ID: "%2", Session: "api", Window: "@1", WindowName: "claude", Role: team.RoleTeammate,
			State: team.StateBusy, Agent: "review-api", AgentType: "api-developer",
			Team: "session-8f3c1d2a", Subagents: 1,
			Running: []team.Subagent{{ID: "ag-1", Type: "security-auditor"}},
		},
		{
			ID: "%3", Session: "api", Window: "@2", WindowName: "build-api", Role: team.RoleTeammate,
			State: team.StateIdle, Agent: "build-api", AgentType: "react-specialist", Team: "session-8f3c1d2a",
		},
		{ID: "%9", Session: "web", Window: "@4", Role: team.RoleClaude, State: team.StateBusy},
	}
}

func barView() team.View {
	return team.Build(team.Input{
		Session: "api",
		Panes:   barPanes(),
		Config: team.Config{Name: "session-8f3c1d2a", Members: []team.Member{
			{Name: "api", Lead: true, Pane: team.LeadPane, Backend: team.BackendTmux},
			{Name: "review-api", Pane: "%2", Backend: team.BackendTmux},
			{Name: "build-api", Pane: "%3", Backend: team.BackendTmux},
			{Name: "write-docs", AgentType: "docs", Backend: team.BackendInProcess},
		}},
		Tasks: []team.Task{
			{ID: "t-1", Subject: "review the router", ActiveForm: "reviewing the router", Owner: "review-api", Status: team.StatusRunning},
			{ID: "t-2", Subject: "write the tests", Owner: "build-api", Status: team.StatusPending, BlockedBy: []string{"t-1"}},
			{ID: "t-3", Subject: "read the plan", Owner: "api", Status: team.StatusDone},
		},
	})
}

// barUpdate is a reading of the agents, delivered the way the rail's own
// command delivers it.
func barUpdate(v team.View, err error) agentBarUpdateMsg {
	return agentBarUpdateMsg{update: AgentBarUpdate{View: v, Err: err, At: fixedNow}}
}

// newBar builds a rail of the given size with a view already delivered. The
// messages are applied without running the commands they return, since the
// rail's own command waits on its channel.
func newBar(t *testing.T, opts AgentBarOptions, view team.View) *AgentBarModel {
	t.Helper()
	if opts.Now == nil {
		opts.Now = clock
	}
	if opts.Styles.Text.String() == "" {
		opts.Styles = goldenStyles(t)
	}
	m := NewAgentBar(opts)
	apply(m, barUpdate(view, nil))
	return m
}

// TestAgentBarFrames pins the rail at the sizes it is drawn at: the rail of a
// workspace, one that has read nothing yet, one with no agent at all, and one
// whose watcher failed.
func TestAgentBarFrames(t *testing.T) {
	failed := team.Build(team.Input{Session: "api", Panes: []team.Pane{
		{ID: "%1", Session: "api", Role: team.RoleClaude, State: team.StateBusy},
		{
			ID: "%2", Session: "api", Role: team.RoleTeammate, State: team.StateBusy,
			Agent: "review-api", AgentType: "api-developer", Dead: true,
		},
	}})
	cases := []struct {
		name          string
		width, height int
		view          team.View
		styles        Styles
		ansi          bool
		msgs          []tea.Msg
	}{
		{name: "rail-28x30", width: 28, height: 30, view: barView(), ansi: true},
		{name: "rail-ascii", width: 28, height: 14, view: barView(), styles: testStyles(t, "lyna", theme.Depth16, "ascii")},
		{name: "rail-failed", width: 28, height: 10, view: failed, msgs: []tea.Msg{press("down"), press("down"), press("down")}},
		{name: "rail-20x14", width: 20, height: 14, view: barView()},
		{name: "rail-folded", width: 28, height: 30, view: barView(), msgs: []tea.Msg{press("down"), press("space")}},
		{name: "rail-filtered", width: 28, height: 30, view: barView(), msgs: []tea.Msg{press("/"), press("a"), press("p"), press("i")}},
		{name: "rail-alone", width: 28, height: 12, view: team.View{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newBar(t, AgentBarOptions{Width: tc.width, Height: tc.height, Styles: tc.styles}, tc.view)
			drive(t, m, nil, tc.msgs...)
			assertFrame(t, tc.name, m, tc.width, tc.height, tc.ansi)
		})
	}
}

// TestAgentBarReadsItsChannel drives the rail the way its program does: the
// first command waits for a reading, every reading is followed by another
// wait, and a watcher that stops is reported rather than waited on forever.
func TestAgentBarReadsItsChannel(t *testing.T) {
	ch := make(chan AgentBarUpdate, 1)
	m := NewAgentBar(AgentBarOptions{Styles: goldenStyles(t), Updates: ch, Width: 30, Height: 16, Now: clock})
	ch <- AgentBarUpdate{View: barView(), At: fixedNow}
	msg, ok := m.Init()().(agentBarUpdateMsg)
	if !ok {
		t.Fatalf("the first command returned %T", msg)
	}
	next, cmd := m.Update(msg)
	if !strings.Contains(plain(next.(viewer)), "review-api") {
		t.Fatalf("the reading was not drawn:\n%s", plain(next.(viewer)))
	}
	close(ch)
	// The rail keeps its animation going beside its readings, so what comes
	// back is a batch: the reading is the part of it that is not a timer.
	if !readsAgain(t, cmd) {
		t.Fatal("a closed channel is not reported as the end of the readings")
	}
	// A rail with nothing to wait on waits for nothing.
	alone := NewAgentBar(AgentBarOptions{Styles: goldenStyles(t), Width: 30, Height: 16, Now: clock})
	if cmd := alone.Init(); cmd != nil {
		t.Fatalf("a rail with no channel waits: %v", cmd())
	}
}

// TestAgentBarBeforeAnyReading draws the rail with nothing delivered yet and
// after the watcher stopped, which are the two states it cannot act in.
func TestAgentBarBeforeAnyReading(t *testing.T) {
	m := NewAgentBar(AgentBarOptions{Styles: goldenStyles(t), Width: 28, Height: 12, Now: clock})
	assertFrame(t, "rail-reading", m, 28, 12, false)
	apply(m, agentBarClosedMsg{})
	if !strings.Contains(plain(m), "watcher stopped") {
		t.Fatalf("a closed watcher is not reported:\n%s", plain(m))
	}
}

// TestAgentBarError keeps the rows it has when a reading fails, and says what
// went wrong.
func TestAgentBarError(t *testing.T) {
	m := newBar(t, AgentBarOptions{Width: 40, Height: 16}, barView())
	apply(m, barUpdate(barView(), errRead))
	out := plain(m)
	if !strings.Contains(out, "review-api") || !strings.Contains(out, "no team directory") {
		t.Fatalf("frame\n%s", out)
	}
}

var errRead = &readError{}

type readError struct{}

func (*readError) Error() string { return "no team directory" }

// TestAgentBarKeys drives every key of the rail and reads back what it did.
func TestAgentBarKeys(t *testing.T) {
	cases := []struct {
		name string
		msgs []tea.Msg
		// wantAction is the action the keys asked for, and wantRow the agent it
		// was asked for.
		wantAction string
		wantRow    string
		wantLine   string
	}{
		{name: "the cursor starts on the first section", wantLine: "lead 1"},
		{name: "down moves to the agent under it", msgs: []tea.Msg{press("down")}, wantLine: "api"},
		{
			name: "enter focuses the selected agent",
			msgs: []tea.Msg{press("down"), press("down"), press("down"), press("enter")},
			// The sections are drawn in order: lead, its agent, teammates, and
			// the first teammate.
			wantAction: "focus", wantRow: "build-api",
		},
		{
			name: "z zooms it", msgs: []tea.Msg{press("down"), press("z")},
			wantAction: "zoom", wantRow: "api",
		},
		{
			name: "w moves it to a window of its own", msgs: []tea.Msg{press("down"), press("w")},
			wantAction: "window", wantRow: "api",
		},
		{name: "a section does nothing to act on", msgs: []tea.Msg{press("enter")}},
		{
			name: "the filter keeps the agents that match",
			msgs: []tea.Msg{press("/"), press("d"), press("o"), press("c"), press("s")},
			// write-docs is the only agent whose name or type carries "docs".
			wantLine: "write-docs",
		},
		{
			name: "a key of the rail is typed into the filter",
			msgs: []tea.Msg{press("/"), press("z"), press("enter")},
			// The rail is left with no match rather than a zoomed pane.
			wantLine: "no agent matches",
		},
		{
			name:     "esc gives the whole team back",
			msgs:     []tea.Msg{press("/"), press("z"), press("esc")},
			wantLine: "review-api",
		},
		{name: "g and G run to the ends", msgs: []tea.Msg{press("G")}, wantLine: "web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAction, gotRow string
			act := func(name string) func(team.Row) tea.Cmd {
				return func(r team.Row) tea.Cmd {
					gotAction, gotRow = name, r.Name
					return nil
				}
			}
			m := newBar(t, AgentBarOptions{
				Width: 30, Height: 20,
				Actions: AgentBarActions{Focus: act("focus"), Zoom: act("zoom"), Window: act("window")},
			}, barView())
			drive(t, m, nil, tc.msgs...)
			if gotAction != tc.wantAction || gotRow != tc.wantRow {
				t.Fatalf("action %q on %q, want %q on %q", gotAction, gotRow, tc.wantAction, tc.wantRow)
			}
			if tc.wantLine != "" && !strings.Contains(plain(m), tc.wantLine) {
				t.Fatalf("frame does not show %q:\n%s", tc.wantLine, plain(m))
			}
		})
	}
}

// TestAgentBarMouse drives the pointer: a click selects the line under it, a
// double click on an agent focuses its pane and one on a section folds it.
func TestAgentBarMouse(t *testing.T) {
	var focused string
	now := fixedNow
	m := newBar(t, AgentBarOptions{
		Width: 30, Height: 20, Now: func() time.Time { return now },
		Actions: AgentBarActions{Focus: func(r team.Row) tea.Cmd { focused = r.Name; return nil }},
	}, barView())

	click := func(y int) tea.Msg { return tea.MouseClickMsg{Button: tea.MouseLeft, Y: y} }
	// The second body line is the lead, drawn under its section heading.
	drive(t, m, nil, click(2))
	if row, ok := m.Selected(); !ok || row.Name != "api" {
		t.Fatalf("selected %+v", row)
	}
	if focused != "" {
		t.Fatalf("one click focused %q", focused)
	}
	drive(t, m, nil, click(2))
	if focused != "api" {
		t.Fatalf("a double click focused %q", focused)
	}
	// A double click on a section heading folds it.
	focused = ""
	drive(t, m, nil, click(1), click(1))
	if !m.folded[team.GroupLead] || focused != "" {
		t.Fatalf("folded %v, focused %q", m.folded, focused)
	}
	// Two clicks far apart are two single clicks.
	now = fixedNow.Add(time.Second)
	drive(t, m, nil, click(3))
	now = fixedNow.Add(2 * time.Second)
	drive(t, m, nil, click(3))
	if focused != "" {
		t.Fatalf("two slow clicks focused %q", focused)
	}
	// A click below the rows changes nothing.
	before, _ := m.Selected()
	drive(t, m, nil, click(19))
	if after, _ := m.Selected(); after != before {
		t.Fatalf("a click under the rows moved the cursor to %+v", after)
	}
}

// TestAgentBarKeepsSelection holds the cursor on the agent it is on when the
// rows change under it, which is what a team spawning agents does.
func TestAgentBarKeepsSelection(t *testing.T) {
	m := newBar(t, AgentBarOptions{Width: 30, Height: 20}, barView())
	drive(t, m, nil, press("down"), press("down"), press("down"))
	before, ok := m.Selected()
	if !ok || before.Name != "build-api" {
		t.Fatalf("selected %+v", before)
	}
	// A teammate that sorts before it arrives, which moves every row under it
	// down a line.
	view := team.Build(team.Input{Session: "api", Panes: append(barPanes(), team.Pane{
		ID: "%4", Session: "api", Window: "@3", Role: team.RoleTeammate,
		State: team.StateBusy, Agent: "audit-api", Team: "session-8f3c1d2a",
	})})
	if view.Rows[1].Name != "audit-api" {
		t.Fatalf("the new teammate did not sort above the others: %+v", view.Rows)
	}
	apply(m, barUpdate(view, nil))
	if after, _ := m.Selected(); after.Name != before.Name {
		t.Fatalf("the cursor moved from %q to %q", before.Name, after.Name)
	}
}

// TestAgentBarAges reads the age a row shows: it is how long the agent has
// been doing what it is doing, and it starts again when that changes.
func TestAgentBarAges(t *testing.T) {
	now := fixedNow
	view := team.Build(team.Input{Session: "api", Panes: []team.Pane{
		{ID: "%1", Session: "api", Role: team.RoleClaude, State: team.StateBusy},
	}})
	m := newBar(t, AgentBarOptions{Width: 30, Height: 10, Now: func() time.Time { return now }}, view)
	now = fixedNow.Add(90 * time.Second)
	apply(m, barUpdate(view, nil))
	if !strings.Contains(plain(m), "1m") {
		t.Fatalf("frame does not age the row:\n%s", plain(m))
	}
	// The same agent, now waiting: the age is the age of the waiting.
	waiting := team.Build(team.Input{Session: "api", Panes: []team.Pane{
		{ID: "%1", Session: "api", Role: team.RoleClaude, State: team.StateWaiting},
	}})
	apply(m, barUpdate(waiting, nil))
	if !strings.Contains(plain(m), "0s") {
		t.Fatalf("a state change does not start the age again:\n%s", plain(m))
	}
}

// TestAgentBarNote shows what an action had to say, until the next key.
func TestAgentBarNote(t *testing.T) {
	m := newBar(t, AgentBarOptions{Width: 40, Height: 16}, barView())
	drive(t, m, nil, AgentBarNoteMsg{Text: "its pane is gone"})
	if !strings.Contains(plain(m), "its pane is gone") {
		t.Fatalf("the note is not drawn:\n%s", plain(m))
	}
	drive(t, m, nil, press("down"))
	if strings.Contains(plain(m), "its pane is gone") {
		t.Fatalf("the note outlived the key press:\n%s", plain(m))
	}
	if got := AgentBarNote("two\nlines"); got != (AgentBarNoteMsg{Text: "two lines"}) {
		t.Fatalf("AgentBarNote = %+v", got)
	}
}

// TestAgentBarQuit covers the one key that closes the rail, and the one place
// it closes it: a rail in a pane is part of the layout and stays.
func TestAgentBarQuit(t *testing.T) {
	cases := []struct {
		name     string
		popup    bool
		key      string
		wantQuit bool
	}{
		{name: "q closes a popup", popup: true, key: "q", wantQuit: true},
		{name: "esc closes a popup", popup: true, key: "esc", wantQuit: true},
		{name: "q in a pane does nothing", key: "q"},
		{name: "ctrl+c always ends the program", key: "ctrl+c", wantQuit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newBar(t, AgentBarOptions{Width: 28, Height: 16, Popup: tc.popup}, barView())
			r := drive(t, m, nil, press(tc.key))
			if r.quit != tc.wantQuit {
				t.Fatalf("quit = %v, want %v", r.quit, tc.wantQuit)
			}
		})
	}
}

// TestAgentBarScrolls moves through more agents than the rail can draw, with
// the keys and with the wheel.
func TestAgentBarScrolls(t *testing.T) {
	view := barView()
	m := newBar(t, AgentBarOptions{Width: 28, Height: 8}, view)
	drive(t, m, nil, press("G"))
	if !strings.Contains(plain(m), "elsewhere") {
		t.Fatalf("the end of the list is not drawn:\n%s", plain(m))
	}
	drive(t, m, nil, tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	drive(t, m, nil, press("g"))
	if !strings.Contains(plain(m), "lead") {
		t.Fatalf("the top of the list is not drawn:\n%s", plain(m))
	}
	drive(t, m, nil, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.list.cursor == 0 {
		t.Fatal("the wheel did not move the cursor")
	}
}

// plain is the frame with its styling removed.
func plain(m viewer) string {
	lines := strings.Split(m.View().Content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(lines, "\n")
}

// TestAgentBarSizes drives the rail through the sizes a pane takes: a resize
// keeps the cursor on a drawn line, a rail with no room for a note draws none,
// and a pointer that is not the left button changes nothing.
func TestAgentBarSizes(t *testing.T) {
	m := newBar(t, AgentBarOptions{Width: 28, Height: 30}, barView())
	drive(t, m, nil, press("G"))
	bottom, _ := m.Selected()
	drive(t, m, nil, resize(24, 8))
	if m.width != 24 || m.height != 8 {
		t.Fatalf("size %dx%d", m.width, m.height)
	}
	if after, _ := m.Selected(); after.Name != bottom.Name {
		t.Fatalf("the resize moved the cursor from %q to %q", bottom.Name, after.Name)
	}
	assertFrame(t, "rail-24x8", m, 24, 8, false)
	// A rail this short has room for its rows and nothing else.
	drive(t, m, nil, resize(24, 5))
	if m.notes() != 0 {
		t.Fatal("a rail with no room draws a note line")
	}
	assertFrame(t, "rail-24x5", m, 24, 5, false)
	// A resize to nothing is still one line of one cell.
	drive(t, m, nil, resize(0, 0))
	if m.width != 1 || m.height != 1 {
		t.Fatalf("size %dx%d, want 1x1", m.width, m.height)
	}
}

// TestAgentBarIgnoresOtherPointers leaves the rail alone for the buttons it
// has nothing to do with.
func TestAgentBarIgnoresOtherPointers(t *testing.T) {
	var focused string
	m := newBar(t, AgentBarOptions{
		Width: 30, Height: 20,
		Actions: AgentBarActions{Focus: func(r team.Row) tea.Cmd { focused = r.Name; return nil }},
	}, barView())
	before, _ := m.Selected()
	drive(t, m, nil,
		tea.MouseClickMsg{Button: tea.MouseRight, Y: 2},
		tea.MouseClickMsg{Button: tea.MouseMiddle, Y: 3},
		tea.MouseWheelMsg{Button: tea.MouseWheelLeft},
		tea.MouseClickMsg{Button: tea.MouseLeft, Y: 0},
	)
	if after, _ := m.Selected(); after != before || focused != "" {
		t.Fatalf("selected %+v, focused %q", after, focused)
	}
}

// TestAgentBarWithoutActions offers nothing to do with an agent when there is
// nothing that could be done with it, and the keys are quiet.
func TestAgentBarWithoutActions(t *testing.T) {
	m := newBar(t, AgentBarOptions{Width: 30, Height: 16}, barView())
	r := drive(t, m, nil, press("down"), press("enter"), press("z"), press("w"))
	if r.quit {
		t.Fatal("a rail with no actions quit")
	}
	if strings.Contains(plain(m), "focus") || strings.Contains(plain(m), "window") {
		t.Fatalf("the footer offers actions the rail does not have:\n%s", plain(m))
	}
}

// TestAgentBarClosesByItself covers the rail a workspace opens on its own: it
// is a pane, so leaving is how it takes itself off the screen once the agents
// it opened for are gone. A rail the user asked for stays.
func TestAgentBarClosesByItself(t *testing.T) {
	alone := team.Build(team.Input{Session: "api", Panes: barPanes()[:1]})
	cases := []struct {
		name     string
		auto     bool
		readings []team.View
		wantQuit bool
	}{
		{
			name: "the team ends and the rail goes with it",
			auto: true, readings: []team.View{barView(), alone}, wantQuit: true,
		},
		{
			name: "a rail open before the first teammate waits for it",
			auto: true, readings: []team.View{alone, alone},
		},
		{
			name:     "a rail the user asked for stays",
			readings: []team.View{barView(), alone},
		},
		{
			name: "a rail closes on the reading that empties it and not before",
			auto: true, readings: []team.View{barView(), barView()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewAgentBar(AgentBarOptions{
				Width: 28, Height: 16, CloseWhenEmpty: tc.auto,
				Styles: goldenStyles(t), Now: clock,
			})
			msgs := make([]tea.Msg, 0, len(tc.readings))
			for _, v := range tc.readings {
				msgs = append(msgs, barUpdate(v, nil))
			}
			if r := drive(t, m, nil, msgs...); r.quit != tc.wantQuit {
				t.Fatalf("quit = %v, want %v", r.quit, tc.wantQuit)
			}
		})
	}
}

// movingClock is a clock the test moves by hand, so an animation is driven
// frame by frame instead of waited for.
type movingClock struct{ at time.Time }

func (c *movingClock) now() time.Time { return c.at }
func (c *movingClock) step(n int)     { c.at = c.at.Add(time.Duration(n) * AgentBarFrame) }
func newClock() *movingClock          { return &movingClock{at: fixedNow} }

// quietView is a workspace working alone: a lead that is waiting, no team and
// nothing that moves.
func quietView() team.View {
	return team.Build(team.Input{Session: "api", Panes: []team.Pane{
		{ID: "%1", Session: "api", Window: "@1", Role: team.RoleClaude, State: team.StateWaiting, Active: true},
	}})
}

// TestAgentBarSpinnerTurns turns the spinner of a working agent frame by frame
// and checks the rail asks for the next frame exactly while something moves.
func TestAgentBarSpinnerTurns(t *testing.T) {
	clk := newClock()
	m := newBar(t, AgentBarOptions{Width: 28, Height: 20, Now: clk.now}, barView())
	for i, want := range []string{spinnerFrames[0], spinnerFrames[1], spinnerFrames[2]} {
		if got := plain(m); !strings.Contains(got, want) {
			t.Fatalf("frame %d lacks %q:\n%s", i, want, got)
		}
		_, cmd := m.Update(agentBarTickMsg{at: clk.at})
		if cmd == nil {
			t.Fatalf("frame %d asked for no next frame while an agent works", i)
		}
		clk.step(1)
	}
	// A rail with nothing moving draws once and waits: no frame is scheduled,
	// and none is scheduled twice either.
	quiet := newBar(t, AgentBarOptions{Width: 28, Height: 20, Now: clk.now}, quietView())
	if cmd := quiet.tick(); cmd != nil {
		t.Fatal("a rail with nothing moving asked for a frame")
	}
	if _, cmd := m.Update(agentBarTickMsg{at: clk.at}); cmd == nil {
		t.Fatal("the working rail stopped asking for frames")
	}
	if cmd := m.tick(); cmd != nil {
		t.Fatal("a frame was asked for twice")
	}
}

// TestAgentBarDrawsArrivals covers what the rail draws for a row that arrived
// while it was on screen, and for one that took a new state: the rows the rail
// opened on are not drawn arriving, since it did not watch them arrive.
func TestAgentBarDrawsArrivals(t *testing.T) {
	clk := newClock()
	m := newBar(t, AgentBarOptions{Width: 28, Height: 30, Now: clk.now}, quietView())
	if m.moving(m.update.View.Rows[0]) {
		t.Fatal("the rail plays back the team it opened on")
	}
	apply(m, barUpdate(barView(), nil))
	arrived := rowNamedInView(t, m.update.View, "review-api")
	for step := range AgentBarSteps {
		if !m.moving(arrived) {
			t.Fatalf("step %d: the row that arrived is not moving", step)
		}
		if got, want := m.step(m.since[rowKey(arrived)].born), step; got != want {
			t.Fatalf("arrival step %d, want %d", got, want)
		}
		clk.step(1)
	}
	if m.moving(arrived) {
		t.Fatal("the arrival never ends")
	}

	// The same agent in a new state is drawn changing, once, for as long as the
	// change is worth noticing.
	busy := barView()
	for i := range busy.Rows {
		if busy.Rows[i].Name == "review-api" {
			busy.Rows[i].State = team.StateIdle
		}
	}
	apply(m, barUpdate(busy, nil))
	changed := rowNamedInView(t, m.update.View, "review-api")
	if m.step(m.since[rowKey(changed)].changed) != 0 {
		t.Fatal("the state change is not drawn")
	}
	clk.step(AgentBarSteps)
	if m.moving(changed) {
		t.Fatal("the state change never ends")
	}
}

// rowNamedInView finds a row of a view by name.
func rowNamedInView(t *testing.T, v team.View, name string) team.Row {
	t.Helper()
	for _, r := range v.Rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q", name)
	return team.Row{}
}

// TestAgentBarFoldsOverFrames takes a section away a few rows at a time and
// brings it back the same way.
func TestAgentBarFoldsOverFrames(t *testing.T) {
	clk := newClock()
	m := newBar(t, AgentBarOptions{Width: 28, Height: 30, Now: clk.now}, barView())
	// The cursor starts on the lead: two lines down is the teammates section,
	// which holds three agents.
	apply(m, press("down"), press("down"), press("space"))
	for _, want := range []int{2, 1, 0, 0} {
		if got := teammatesDrawn(m); got != want {
			t.Fatalf("closing: %d teammates drawn, want %d", got, want)
		}
		clk.step(1)
		apply(m, agentBarTickMsg{at: clk.at})
	}
	apply(m, press("space"))
	for _, want := range []int{1, 2, 3, 3} {
		if got := teammatesDrawn(m); got != want {
			t.Fatalf("opening: %d teammates drawn, want %d", got, want)
		}
		clk.step(1)
		apply(m, agentBarTickMsg{at: clk.at})
	}
	// The heading says what the section holds whatever it is drawing.
	if !strings.Contains(plain(m), "teammates 3") {
		t.Fatalf("the heading lost its count:\n%s", plain(m))
	}
}

// teammatesDrawn counts the agent rows of the teammates section on screen.
func teammatesDrawn(m *AgentBarModel) int {
	n := 0
	for _, it := range m.items {
		if !it.header && it.group == team.GroupTeammates {
			n++
		}
	}
	return n
}

// TestArrivingStyle pins the colors a row comes up through.
func TestArrivingStyle(t *testing.T) {
	s := goldenStyles(t)
	cases := []struct {
		name string
		step int
		want lipgloss.Style
	}{
		{name: "the first frame is barely there", step: 0, want: s.Border},
		{name: "then it comes up", step: 1, want: s.Muted},
		{name: "then it is a row like the others", step: 2, want: s.Text},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := arrivingStyle(s, tc.step, s.Text)
			if got.Render("agent") != tc.want.Render("agent") {
				t.Fatalf("step %d renders %q, want %q", tc.step, got.Render("agent"), tc.want.Render("agent"))
			}
		})
	}
}

// TestAgentBarMovingFrames pins what a movement looks like on screen: the rail
// drawing a team as it arrives, and a section on its way closed.
func TestAgentBarMovingFrames(t *testing.T) {
	clk := newClock()
	arriving := newBar(t, AgentBarOptions{Width: 28, Height: 16, Now: clk.now}, quietView())
	apply(arriving, barUpdate(barView(), nil))
	assertFrame(t, "rail-arriving", arriving, 28, 16, true)

	folding := newBar(t, AgentBarOptions{Width: 28, Height: 16, Now: clk.now}, barView())
	apply(folding, press("down"), press("down"), press("space"))
	assertFrame(t, "rail-folding", folding, 28, 16, false)
}

// readsAgain reports whether a command waits on the readings again, through
// however many commands the rail batched around that one.
func readsAgain(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil || timerCmd(cmd) {
		return false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if readsAgain(t, c) {
				return true
			}
		}
		return false
	}
	_, ok := msg.(agentBarClosedMsg)
	return ok
}
