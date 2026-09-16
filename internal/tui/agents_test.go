package tui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
)

const testHome = "/home/dev"

// sampleAgents covers every status and source.
func sampleAgents() []agent.Agent {
	started := func(d time.Duration) int64 { return fixedNow.Add(-d).UnixMilli() }
	return []agent.Agent{
		{
			Record:   agent.Record{PID: 5151, CWD: testHome + "/src/web", Kind: agent.KindInteractive, StartedAt: started(3 * time.Hour), Status: "busy"},
			Location: &agent.Location{Session: "web", WindowIndex: 0, WindowName: "main", PaneID: "%7"},
			Source:   agent.SourcePane, Status: agent.StatusBusy,
		},
		{
			Record: agent.Record{
				PID: 4242, CWD: testHome + "/src/api", Kind: agent.KindInteractive, StartedAt: started(42 * time.Minute),
				Name: "api-refactor", Status: "waiting", WaitingFor: "approve Bash: go test ./...",
			},
			Location: &agent.Location{Session: "api", WindowIndex: 1, WindowName: "claude", PaneID: "%3"},
			Source:   agent.SourceManaged, Status: agent.StatusWaiting,
		},
		{
			Record: agent.Record{ID: "bg-7", CWD: testHome + "/src/docs", Kind: agent.KindBackground, StartedAt: started(50 * time.Hour), Name: "docs sweep", State: agent.JobDone},
			Source: agent.SourceBackground, Status: agent.StatusIdle,
		},
		{
			Record: agent.Record{PID: 6060, CWD: "/srv/tools", Kind: agent.KindInteractive, StartedAt: started(20 * time.Second)},
			Source: agent.SourceExternal, Status: agent.StatusUnknown,
		},
	}
}

const samplePreview = "\x1b[32m✻ Welcome back\x1b[0m\n\n> run the api tests\n\x1b]52;c;ZXZpbA==\x07\x1b[2J  Bash(go test ./...)\n\x1b[1;33m  Do you want to proceed?\x1b[0m\n  1. Yes\n  2. No\n\n"

type fakeAgentSource struct {
	cached    agent.Snapshot
	hasCached bool
	snap      agent.Snapshot
	err       error
	refreshes int
}

func (f *fakeAgentSource) Cached() (agent.Snapshot, bool) { return f.cached, f.hasCached }

func (f *fakeAgentSource) Refresh(context.Context) (agent.Snapshot, error) {
	f.refreshes++
	return f.snap, f.err
}

type fakePreview struct {
	content map[string]string
	err     error
	calls   []string
	lines   int
}

func (f *fakePreview) Preview(_ context.Context, a agent.Agent, lines int) (string, error) {
	f.calls = append(f.calls, a.Record.Key())
	f.lines = lines
	return f.content[a.Record.Key()], f.err
}

type fakeAgentActions struct {
	exec      tea.ExecCommand
	err       error
	killErr   error
	jumped    []string
	attached  []string
	killed    []string
	killedCtx context.Context
}

func (f *fakeAgentActions) Jump(_ context.Context, a agent.Agent) (tea.ExecCommand, error) {
	f.jumped = append(f.jumped, a.Record.Key())
	return f.exec, f.err
}

func (f *fakeAgentActions) Attach(_ context.Context, a agent.Agent) (tea.ExecCommand, error) {
	f.attached = append(f.attached, a.Record.Key())
	return f.exec, f.err
}

func (f *fakeAgentActions) Kill(ctx context.Context, a agent.Agent) error {
	f.killed = append(f.killed, a.Record.Key())
	f.killedCtx = ctx
	return f.killErr
}

type agentsFixture struct {
	source  *fakeAgentSource
	preview *fakePreview
	actions *fakeAgentActions
	opts    AgentsOptions
}

func newAgentsFixture(t *testing.T, width, height int) *agentsFixture {
	t.Helper()
	f := &agentsFixture{
		source:  &fakeAgentSource{snap: agent.Snapshot{TakenAt: fixedNow, Agents: sampleAgents()}},
		preview: &fakePreview{content: map[string]string{"pid:4242": samplePreview, "pid:5151": "$ npm run dev\n  ready in 312 ms"}},
		actions: &fakeAgentActions{},
	}
	f.opts = AgentsOptions{
		Styles: goldenStyles(t), Source: f.source, Preview: f.preview, Actions: f.actions,
		Width: width, Height: height, Now: clock, Home: testHome,
	}
	return f
}

// start builds the picker and runs Init to completion.
func (f *agentsFixture) start(t *testing.T, msgs ...tea.Msg) (*AgentsModel, run) {
	t.Helper()
	m := NewAgents(f.opts)
	r := drive(t, m, m.Init(), msgs...)
	return m, r
}

func keys(names ...string) []tea.Msg {
	msgs := make([]tea.Msg, len(names))
	for i, n := range names {
		msgs[i] = press(n)
	}
	return msgs
}

func TestAgentsFrames(t *testing.T) {
	t.Parallel()
	states := []struct {
		name  string
		build func(t *testing.T, f *agentsFixture) *AgentsModel
	}{
		{name: "loading", build: func(_ *testing.T, f *agentsFixture) *AgentsModel { return NewAgents(f.opts) }},
		{name: "cached", build: func(_ *testing.T, f *agentsFixture) *AgentsModel {
			f.source.cached, f.source.hasCached = agent.Snapshot{TakenAt: fixedNow.Add(-5 * time.Minute), Agents: sampleAgents()}, true
			return NewAgents(f.opts)
		}},
		{name: "empty", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			f.source.snap = agent.Snapshot{TakenAt: fixedNow}
			m, _ := f.start(t)
			return m
		}},
		{name: "populated", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t)
			return m
		}},
		{name: "selected-down", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t, keys("j", "j")...)
			return m
		}},
		{name: "filtering", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t, keys("/", "w", "e")...)
			return m
		}},
		{name: "filtered", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t, keys("/", "w", "e", "b", "enter")...)
			return m
		}},
		{name: "filtered-none", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t, keys("/", "z", "z", "enter")...)
			return m
		}},
		{name: "confirm-kill", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			m, _ := f.start(t, press("ctrl+x"))
			return m
		}},
		{name: "error", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			f.source.err = errors.New("claude agents --json: exit status 1")
			m, _ := f.start(t)
			return m
		}},
		{name: "error-cached", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			f.source.cached, f.source.hasCached = agent.Snapshot{TakenAt: fixedNow.Add(-time.Hour), Agents: sampleAgents()}, true
			f.source.err = errors.New("claude agents --json: exit status 1")
			m, _ := f.start(t)
			return m
		}},
		{name: "action-failed", build: func(t *testing.T, f *agentsFixture) *AgentsModel {
			f.actions.err = errors.New("pane %3 is gone")
			m, _ := f.start(t, press("enter"))
			return m
		}},
	}
	for _, size := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				f := newAgentsFixture(t, size.width, size.height)
				m := st.build(t, f)
				assertFrame(t, "agents/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestAgentsFirstFrameUsesCache(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	f.source.cached, f.source.hasCached = agent.Snapshot{TakenAt: fixedNow.Add(-5 * time.Minute), Agents: sampleAgents()}, true
	m := NewAgents(f.opts)
	frame := ansi.Strip(m.View().Content)
	if f.source.refreshes != 0 {
		t.Fatalf("the first frame listed agents %d times", f.source.refreshes)
	}
	for _, want := range []string{"api-refactor", "cached 5m"} {
		if !strings.Contains(frame, want) {
			t.Errorf("first frame lacks %q", want)
		}
	}
	drive(t, m, m.Init())
	if f.source.refreshes != 1 {
		t.Errorf("Init refreshed %d times, want 1", f.source.refreshes)
	}
	if frame := ansi.Strip(m.View().Content); strings.Contains(frame, "cached") || strings.Contains(frame, "refreshing") {
		t.Errorf("live frame still marks the snapshot stale:\n%s", frame)
	}
}

// The first frame draws only visible rows: rendering a huge snapshot costs the
// same allocations as rendering a small one.
func TestAgentsFrameCostIndependentOfAgentCount(t *testing.T) {
	cost := func(n int) float64 {
		all := make([]agent.Agent, n)
		base := sampleAgents()
		for i := range all {
			a := base[i%len(base)]
			a.Record.PID = 10000 + i
			a.Record.ID = ""
			all[i] = a
		}
		f := newAgentsFixture(t, 100, 30)
		f.source.cached, f.source.hasCached = agent.Snapshot{TakenAt: fixedNow, Agents: all}, true
		m := NewAgents(f.opts)
		return testing.AllocsPerRun(20, func() { _ = m.View() })
	}
	small, large := cost(40), cost(20000)
	if large > small*1.1+5 {
		t.Errorf("rendering 20000 agents allocates %.0f times, 40 agents %.0f: the frame is not bounded by visible rows", large, small)
	}
}

func TestAgentsNavigation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []tea.Msg
		want string // selected record key
	}{
		{name: "starts on the most urgent agent", want: "pid:4242"},
		{name: "down", keys: keys("down"), want: "job:bg-7"},
		{name: "j twice", keys: keys("j", "j"), want: "pid:5151"},
		{name: "end", keys: keys("G"), want: "pid:6060"},
		{name: "end then top", keys: keys("end", "g"), want: "pid:4242"},
		{name: "up stops at the top", keys: keys("up", "k"), want: "pid:4242"},
		{name: "ctrl+n", keys: keys("ctrl+n", "ctrl+n", "ctrl+p"), want: "job:bg-7"},
		{name: "page down", keys: keys("pgdown"), want: "pid:6060"},
		{name: "wheel down", keys: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}}, want: "job:bg-7"},
		{name: "wheel up", keys: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}}, want: "pid:4242"},
		{name: "click third row", keys: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}}, want: "pid:5151"},
		{name: "click the header is ignored", keys: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 0}}, want: "pid:4242"},
		{name: "click below the rows is ignored", keys: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 20}}, want: "pid:4242"},
		{name: "right click is ignored", keys: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseRight, Y: 2}}, want: "pid:4242"},
		{name: "arrows move while filtering", keys: keys("/", "down"), want: "job:bg-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			m, r := f.start(t, tc.keys...)
			if r.quit {
				t.Fatal("navigation quit the picker")
			}
			a, ok := m.selected()
			if !ok || a.Record.Key() != tc.want {
				t.Fatalf("selected %q, want %q", a.Record.Key(), tc.want)
			}
			if last := f.preview.calls[len(f.preview.calls)-1]; a.Location != nil && last != tc.want {
				t.Errorf("preview captured %q, want the selection %q", last, tc.want)
			}
		})
	}
}

func TestAgentsActivate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		keys     []tea.Msg
		exec     tea.ExecCommand
		err      error
		jumped   []string
		attached []string
		execs    int
		quit     bool
		chosen   string
		message  string
	}{
		{name: "enter jumps to a pane", keys: keys("enter"), jumped: []string{"pid:4242"}, quit: true, chosen: "pid:4242"},
		{
			name: "enter hands the terminal over", keys: keys("enter"), exec: fakeExec{name: "attach"},
			jumped: []string{"pid:4242"}, execs: 1, chosen: "pid:4242",
		},
		{name: "enter attaches a background job", keys: keys("j", "enter"), attached: []string{"job:bg-7"}, quit: true, chosen: "job:bg-7"},
		{name: "external agents cannot be reached", keys: keys("G", "enter"), message: "runs outside the lyna-tmux server"},
		{name: "jump errors stay in the picker", keys: keys("enter"), err: errors.New("no such pane"), jumped: []string{"pid:4242"}, message: "jump failed: no such pane"},
		{
			name: "double click jumps", jumped: []string{"pid:5151"}, quit: true, chosen: "pid:5151",
			keys: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}, tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			f.actions.exec, f.actions.err = tc.exec, tc.err
			m, r := f.start(t, tc.keys...)
			if !slices.Equal(f.actions.jumped, tc.jumped) || !slices.Equal(f.actions.attached, tc.attached) {
				t.Errorf("jumped %v attached %v, want %v %v", f.actions.jumped, f.actions.attached, tc.jumped, tc.attached)
			}
			if len(r.execs) != tc.execs || r.quit != tc.quit {
				t.Errorf("execs %d quit %v, want %d %v", len(r.execs), r.quit, tc.execs, tc.quit)
			}
			if a, ok := m.Chosen(); ok != (tc.chosen != "") || a.Record.Key() != tc.chosen && tc.chosen != "" {
				t.Errorf("chosen %q, %v, want %q", a.Record.Key(), ok, tc.chosen)
			}
			if tc.message != "" && !strings.Contains(m.message, tc.message) {
				t.Errorf("message %q, want %q", m.message, tc.message)
			}
			if tc.execs > 0 {
				if name := r.execs[0].(fakeExec).name; name != "attach" {
					t.Errorf("exec %q", name)
				}
			}
		})
	}
}

func TestAgentsExecDone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		quit   bool
		chosen bool
	}{
		{name: "success quits", quit: true, chosen: true},
		{name: "failure stays", err: errors.New("attach: exit status 1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			f.actions.exec = fakeExec{name: "attach"}
			m, r := f.start(t, press("enter"))
			if len(r.execs) != 1 {
				t.Fatalf("execs = %d", len(r.execs))
			}
			r = drive(t, m, nil, execDoneMsg{err: tc.err})
			if r.quit != tc.quit {
				t.Errorf("quit = %v, want %v", r.quit, tc.quit)
			}
			if _, ok := m.Chosen(); ok != tc.chosen {
				t.Errorf("chosen = %v, want %v", ok, tc.chosen)
			}
			if tc.err != nil && m.message != tc.err.Error() {
				t.Errorf("message = %q", m.message)
			}
		})
	}
}

func TestAgentsKill(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		keys      []tea.Msg
		killErr   error
		killed    []string
		remaining int
		confirm   bool
		message   string
		refreshes int
	}{
		{name: "ctrl+x asks first", keys: keys("ctrl+x"), confirm: true, remaining: 4, refreshes: 1},
		{name: "n cancels", keys: keys("ctrl+x", "n"), remaining: 4, refreshes: 1},
		{name: "esc cancels without quitting", keys: keys("ctrl+x", "esc"), remaining: 4, refreshes: 1},
		{name: "other keys keep asking", keys: keys("ctrl+x", "j"), confirm: true, remaining: 4, refreshes: 1},
		{name: "y kills and refreshes", keys: keys("ctrl+x", "y"), killed: []string{"pid:4242"}, remaining: 4, message: "sent SIGTERM to pid 4242", refreshes: 2},
		{name: "enter confirms", keys: keys("j", "j", "ctrl+x", "enter"), killed: []string{"pid:5151"}, remaining: 4, refreshes: 2},
		{name: "kill failure keeps the agent", keys: keys("ctrl+x", "y"), killErr: errors.New("process identity changed"), killed: []string{"pid:4242"}, remaining: 4, message: "kill failed: process identity changed", refreshes: 1},
		{name: "background jobs have no process", keys: keys("j", "ctrl+x"), remaining: 4, message: "no process to kill", refreshes: 1},
		{name: "mouse is ignored while asking", keys: []tea.Msg{press("ctrl+x"), tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}}, confirm: true, remaining: 4, refreshes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			f.actions.killErr = tc.killErr
			m, r := f.start(t, tc.keys...)
			if r.quit {
				t.Fatal("kill flow quit the picker")
			}
			if !slices.Equal(f.actions.killed, tc.killed) {
				t.Errorf("killed %v, want %v", f.actions.killed, tc.killed)
			}
			if (m.confirm != nil) != tc.confirm {
				t.Errorf("confirming = %v, want %v", m.confirm != nil, tc.confirm)
			}
			if len(m.all) != tc.remaining {
				t.Errorf("%d agents listed, want %d", len(m.all), tc.remaining)
			}
			if tc.message != "" && !strings.Contains(m.message, tc.message) {
				t.Errorf("message %q, want %q", m.message, tc.message)
			}
			if f.source.refreshes != tc.refreshes {
				t.Errorf("refreshes = %d, want %d", f.source.refreshes, tc.refreshes)
			}
		})
	}
}

// A killed agent disappears at once, even when the refresh that follows is
// slower than the next frame.
func TestAgentsKillRemovesBeforeRefresh(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	m, _ := f.start(t, press("ctrl+x"))
	_, cmd := m.Update(press("y"))
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(*AgentsModel)
	if strings.Contains(ansi.Strip(m.View().Content), "api-refactor") {
		t.Error("killed agent still drawn before the refresh")
	}
	if !m.loading {
		t.Error("kill did not start a refresh")
	}
}

func TestAgentsFilterAndQuit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		keys    []tea.Msg
		filter  string
		editing bool
		visible int
		quit    bool
	}{
		{name: "slash starts editing", keys: keys("/"), editing: true, visible: 4},
		{name: "typing filters live", keys: keys("/", "d", "o", "c"), filter: "doc", editing: true, visible: 1},
		{name: "terms match independently", keys: keys("/", "b", "u", "s", "y", "space", "w", "e", "b"), filter: "busy web", editing: true, visible: 1},
		{name: "matches the waiting reason", keys: keys("/", "a", "p", "p", "r", "o", "v", "e"), filter: "approve", editing: true, visible: 1},
		{name: "enter applies", keys: keys("/", "w", "e", "b", "enter"), filter: "web", visible: 1},
		{name: "q types while editing", keys: keys("/", "q"), filter: "q", editing: true},
		{name: "esc while editing clears", keys: keys("/", "w", "esc"), visible: 4},
		{name: "esc clears an applied filter", keys: keys("/", "w", "e", "b", "enter", "esc"), visible: 4},
		{name: "esc without filter quits", keys: keys("esc"), visible: 4, quit: true},
		{name: "q quits", keys: keys("q"), visible: 4, quit: true},
		{name: "ctrl+c quits while editing", keys: keys("/", "ctrl+c"), editing: true, visible: 4, quit: true},
		{name: "ctrl+c quits while confirming", keys: keys("ctrl+x", "ctrl+c"), visible: 4, quit: true},
		{name: "unbound keys do nothing", keys: keys("z", "tab"), visible: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			m, r := f.start(t, tc.keys...)
			if m.filter.value != tc.filter || m.editing != tc.editing || len(m.visible) != tc.visible || r.quit != tc.quit {
				t.Errorf("filter %q editing %v visible %d quit %v, want %q %v %d %v",
					m.filter.value, m.editing, len(m.visible), r.quit, tc.filter, tc.editing, tc.visible, tc.quit)
			}
		})
	}
}

func TestAgentsFilterKeepsSelection(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	m, _ := f.start(t, keys("j", "j", "/", "s", "r", "c")...)
	if a, _ := m.selected(); a.Record.Key() != "pid:5151" {
		t.Fatalf("filter moved the selection to %q", a.Record.Key())
	}
	drive(t, m, nil, press("esc"))
	if a, _ := m.selected(); a.Record.Key() != "pid:5151" {
		t.Errorf("clearing the filter moved the selection to %q", a.Record.Key())
	}
}

func TestAgentsRefresh(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	m, _ := f.start(t, press("r"))
	if f.source.refreshes != 2 {
		t.Fatalf("r refreshed %d times in total, want 2", f.source.refreshes)
	}
	// A refresh already in flight is not duplicated by r or a tick.
	m.loading = true
	drive(t, m, nil, press("r"), agentsTickMsg{})
	if f.source.refreshes != 2 {
		t.Errorf("refresh while loading ran again: %d", f.source.refreshes)
	}
	m.loading = false
	drive(t, m, nil, agentsTickMsg{})
	if f.source.refreshes != 3 {
		t.Errorf("tick did not refresh: %d", f.source.refreshes)
	}
	calls := len(f.preview.calls)
	drive(t, m, nil, previewTickMsg{})
	if len(f.preview.calls) != calls+1 {
		t.Errorf("preview tick did not capture")
	}
	// A new snapshot that keeps the selection keeps its preview.
	drive(t, m, nil, agentsLoadedMsg{snap: agent.Snapshot{TakenAt: fixedNow, Agents: sampleAgents()}})
	if len(f.preview.calls) != calls+1 {
		t.Errorf("unchanged selection re-captured the pane")
	}
}

func TestAgentsTicksScheduled(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	f.opts.RefreshEvery, f.opts.PreviewEvery = time.Second, 500*time.Millisecond
	m := NewAgents(f.opts)
	if m.agentsTick() == nil || m.previewTick() == nil {
		t.Error("ticks not scheduled")
	}
	f.opts.Preview = nil
	if NewAgents(f.opts).previewTick() != nil {
		t.Error("preview tick scheduled without a preview source")
	}
}

func TestAgentsExtraActions(t *testing.T) {
	t.Parallel()
	managedOnly := func(a agent.Agent) bool { return a.Source == agent.SourceManaged }
	cases := []struct {
		name      string
		keys      []tea.Msg
		result    ActionResult
		err       error
		ran       []string
		quit      bool
		execs     int
		message   string
		refreshes int
	}{
		{name: "runs on the selection", keys: keys("R"), result: ActionResult{Message: "renamed"}, ran: []string{"pid:4242"}, message: "renamed", refreshes: 1},
		{name: "disabled for the selection", keys: keys("j", "R"), refreshes: 1},
		{name: "refresh after", keys: keys("R"), result: ActionResult{Refresh: true}, ran: []string{"pid:4242"}, refreshes: 2},
		{name: "quit after", keys: keys("R"), result: ActionResult{Quit: true}, ran: []string{"pid:4242"}, quit: true, refreshes: 1},
		{name: "exec after", keys: keys("R"), result: ActionResult{Exec: fakeExec{name: "rename"}}, ran: []string{"pid:4242"}, execs: 1, refreshes: 1},
		{name: "error", keys: keys("R"), err: errors.New("name taken"), ran: []string{"pid:4242"}, message: "rename failed: name taken", refreshes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			var ran []string
			f.opts.Extra = []AgentAction{
				{Key: "X", Label: "no run"},
				{Key: "R", Label: "rename", Enabled: managedOnly, Run: func(ctx context.Context, a agent.Agent) (ActionResult, error) {
					if ctx == nil {
						t.Error("nil context")
					}
					ran = append(ran, a.Record.Key())
					return tc.result, tc.err
				}},
			}
			m, r := f.start(t, tc.keys...)
			if !slices.Equal(ran, tc.ran) || r.quit != tc.quit || len(r.execs) != tc.execs || f.source.refreshes != tc.refreshes {
				t.Errorf("ran %v quit %v execs %d refreshes %d, want %v %v %d %d", ran, r.quit, len(r.execs), f.source.refreshes, tc.ran, tc.quit, tc.execs, tc.refreshes)
			}
			if m.message != tc.message {
				t.Errorf("message %q, want %q", m.message, tc.message)
			}
			if _, ok := m.Chosen(); ok {
				t.Error("extra action chose an agent")
			}
		})
	}
	f := newAgentsFixture(t, 100, 30)
	f.opts.Extra = []AgentAction{{Key: "R", Label: "rename", Enabled: managedOnly}}
	m, _ := f.start(t)
	if !strings.Contains(ansi.Strip(m.View().Content), "R rename") {
		t.Error("enabled extra action missing from the help line")
	}
	drive(t, m, nil, press("j"))
	if strings.Contains(ansi.Strip(m.View().Content), "R rename") {
		t.Error("disabled extra action shown in the help line")
	}
}

func TestAgentsPreviewStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(f *agentsFixture)
		keys  []tea.Msg
		want  string
	}{
		{name: "capture failure", setup: func(f *agentsFixture) { f.preview.err = errors.New("can't find pane: %3") }, want: "capture failed: can't find pane: %3"},
		{name: "background job", keys: keys("j"), want: "background agent without a pane: enter attaches to it"},
		{name: "outside the server", keys: keys("G"), want: "not in a pane of this tmux server: /srv/tools"},
		{name: "follows the tail", want: "2. No"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			if tc.setup != nil {
				tc.setup(f)
			}
			m, _ := f.start(t, tc.keys...)
			if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, tc.want) {
				t.Errorf("frame lacks %q:\n%s", tc.want, frame)
			}
		})
	}
	f := newAgentsFixture(t, 100, 30)
	f.start(t)
	if f.preview.lines != 23 {
		t.Errorf("preview asked for %d lines, want the 23 visible ones", f.preview.lines)
	}
	f.opts.Preview = nil
	m, _ := f.start(t)
	if strings.Contains(ansi.Strip(m.View().Content), "preview") {
		t.Error("preview drawn without a preview source")
	}
}

func TestPreviewContent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		raw    string
		width  int
		height int
		want   []string
	}{
		{name: "keeps the tail", raw: "a\nb\nc\nd\n", width: 10, height: 2, want: []string{"c", "d"}},
		{name: "drops trailing blank lines", raw: "a\nb\n\n   \n\x1b[0m\n", width: 10, height: 5, want: []string{"a", "b"}},
		{name: "clips wide lines", raw: "abcdefgh", width: 4, height: 1, want: []string{"abcd"}},
		{name: "tabs become spaces", raw: "a\tb", width: 10, height: 1, want: []string{"a b"}},
		{name: "strips hostile sequences", raw: "\x1b]0;title\x07ok\x1b[2J\x1b]8;;http://x\x1b\\link", width: 20, height: 1, want: []string{"oklink"}},
		{name: "resets colors per line", raw: "\x1b[31mred", width: 10, height: 1, want: []string{"\x1b[31mred\x1b[m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := previewContent(tc.raw, tc.width, tc.height)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentsSanitizesUntrustedText(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	hostile := agent.Agent{
		Record: agent.Record{
			PID: 7777, CWD: "/tmp/\x1b]52;c;cHduZWQ=\x07repo", Kind: agent.KindInteractive, StartedAt: fixedNow.UnixMilli(),
			Name: "evil\x1b]0;owned\x07\u202e title", Status: "waiting", WaitingFor: "\x1b[2Jclick\x1b]8;;http://x\x1b\\here",
		},
		Location: &agent.Location{Session: "s\x1b[1A", WindowName: "w\x07", PaneID: "%9"},
		Source:   agent.SourcePane, Status: agent.StatusWaiting,
	}
	f.source.snap = agent.Snapshot{TakenAt: fixedNow, Agents: []agent.Agent{hostile}}
	f.preview.content = map[string]string{"pid:7777": samplePreview}
	m, _ := f.start(t, keys("/", "e", "v", "i", "l", "enter", "ctrl+x")...)
	frame := m.View().Content
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x1b[1A", "\u202e", "\x1b\\"} {
		if strings.Contains(frame, bad) {
			t.Errorf("frame carries %q", bad)
		}
	}
	for _, want := range []string{"evil title", "kill evil title (pid 7777)?"} {
		if !strings.Contains(ansi.Strip(frame), want) {
			t.Errorf("frame lacks %q:\n%s", want, ansi.Strip(frame))
		}
	}
}

func TestAgentsResize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		width, height int
		preview       bool
	}{
		{width: 100, height: 30, preview: true},
		{width: 60, height: 20, preview: true},
		{width: 40, height: 10, preview: true},
		{width: 40, height: 9},
		{width: 20, height: 4},
		{width: 1, height: 1},
		{width: 0, height: 0},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.width)+"x"+strconv.Itoa(tc.height), func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			m, _ := f.start(t, keys("G")...)
			drive(t, m, nil, resize(tc.width, tc.height))
			w, h := max(tc.width, 1), max(tc.height, 1)
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != h {
				t.Fatalf("%d lines, want %d", len(lines), h)
			}
			for i, l := range lines {
				if ansi.StringWidth(l) > w {
					t.Errorf("line %d is %d cells wide", i, ansi.StringWidth(l))
				}
			}
			if m.hasPreview() != tc.preview {
				t.Errorf("preview = %v, want %v", m.hasPreview(), tc.preview)
			}
			if m.list.cursor < m.list.offset || m.list.cursor >= m.list.offset+m.listHeight() {
				t.Errorf("selection scrolled out of view: cursor %d offset %d height %d", m.list.cursor, m.list.offset, m.listHeight())
			}
		})
	}
}

func TestAgentsWithoutSources(t *testing.T) {
	t.Parallel()
	m := NewAgents(AgentsOptions{Styles: goldenStyles(t)})
	r := drive(t, m, m.Init(), keys("enter", "ctrl+x", "y", "r")...)
	if r.quit || m.loading || !m.loaded {
		t.Errorf("quit %v loading %v loaded %v", r.quit, m.loading, m.loaded)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "no Claude agents running") {
		t.Error("empty picker frame")
	}
	if m.width != defaultWidth || m.height != defaultHeight {
		t.Errorf("default size %dx%d", m.width, m.height)
	}

	f := newAgentsFixture(t, 100, 30)
	f.opts.Actions = nil
	m, r = f.start(t, keys("enter", "ctrl+x", "y")...)
	if r.quit || len(m.all) != 4 {
		t.Errorf("actions ran without an Actions implementation")
	}
}

func TestAgentsContextPassedToActions(t *testing.T) {
	t.Parallel()
	f := newAgentsFixture(t, 100, 30)
	ctx := context.WithValue(context.Background(), ctxKey{}, "picker")
	f.opts.Context = ctx
	f.start(t, keys("ctrl+x", "y")...)
	if f.actions.killedCtx != ctx {
		t.Error("kill did not receive the picker context")
	}
}

func TestAgentsView(t *testing.T) {
	t.Parallel()
	m := NewAgents(AgentsOptions{Styles: goldenStyles(t)})
	v := m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("view alt %v mouse %v", v.AltScreen, v.MouseMode)
	}
}

// TestAgentsPopupNamesTheQuitKey pins what the footer calls the quit key
// where the picker draws: a popup is an overlay the key closes.
func TestAgentsPopupNamesTheQuitKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		popup      bool
		want, lack string
	}{
		{name: "terminal", want: "q quit", lack: "q close"},
		{name: "popup", popup: true, want: "q close", lack: "q quit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newAgentsFixture(t, 100, 30)
			f.opts.Popup = tc.popup
			m, _ := f.start(t)
			frame := ansi.Strip(m.View().Content)
			if !strings.Contains(frame, tc.want) || strings.Contains(frame, tc.lack) {
				t.Fatalf("footer does not say %q alone:\n%s", tc.want, frame)
			}
		})
	}
}

func TestSourceAndStatusWords(t *testing.T) {
	t.Parallel()
	for src, want := range map[agent.Source]string{
		agent.SourceManaged: "lyna", agent.SourcePane: "pane", agent.SourceBackground: "bg", agent.SourceExternal: "outside",
	} {
		if got := sourceWord(src); got != want {
			t.Errorf("sourceWord(%q) = %q, want %q", src, got, want)
		}
	}
	if statusWord(agent.StatusUnknown) != "?" || statusWord(agent.StatusBusy) != "busy" {
		t.Error("statusWord")
	}
	bg := agent.Agent{Source: agent.SourceBackground}
	if got := locationText(bg, ""); got != "background" {
		t.Errorf("locationText = %q", got)
	}
}
