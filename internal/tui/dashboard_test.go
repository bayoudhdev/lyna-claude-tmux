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
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func sampleSessions() []tmux.Session {
	return []tmux.Session{
		{Name: "api", Windows: 3, Attached: 1, Managed: true, Layout: "trio", Sandbox: "standard", Project: testHome + "/src/api", Path: testHome + "/src/api"},
		{Name: "web", Windows: 2, Managed: true, Layout: "duo", Sandbox: "strict", Project: testHome + "/src/web"},
		{Name: "scratch", Windows: 1, Managed: true, Layout: "solo", Sandbox: "off", Project: "/tmp/scratch"},
		{Name: "a-very-long-session-name-that-overflows", Windows: 12, Managed: true, Layout: "quad"},
		{Name: "misc", Windows: 1, Path: testHome},
	}
}

type fakeSessions struct {
	sessions []tmux.Session
	err      error
	calls    int
}

func (f *fakeSessions) Sessions(context.Context) ([]tmux.Session, error) {
	f.calls++
	return f.sessions, f.err
}

type fakeDashActions struct {
	exec     tea.ExecCommand
	err      error
	killErr  error
	attached []string
	killed   []string
	created  []CreateRequest
}

func (f *fakeDashActions) Attach(_ context.Context, s tmux.Session) (tea.ExecCommand, error) {
	f.attached = append(f.attached, s.Name)
	return f.exec, f.err
}

func (f *fakeDashActions) Kill(_ context.Context, s tmux.Session) error {
	f.killed = append(f.killed, s.Name)
	return f.killErr
}

func (f *fakeDashActions) Create(_ context.Context, req CreateRequest) (tea.ExecCommand, error) {
	f.created = append(f.created, req)
	return f.exec, f.err
}

type dashFixture struct {
	sessions *fakeSessions
	agents   *fakeAgentSource
	actions  *fakeDashActions
	opts     DashboardOptions
}

func newDashFixture(t *testing.T, width, height int) *dashFixture {
	t.Helper()
	f := &dashFixture{
		sessions: &fakeSessions{sessions: sampleSessions()},
		agents:   &fakeAgentSource{snap: agent.Snapshot{TakenAt: fixedNow, Agents: sampleAgents()}},
		actions:  &fakeDashActions{},
	}
	f.opts = DashboardOptions{
		Styles: goldenStyles(t), Sessions: f.sessions, Agents: f.agents, Actions: f.actions,
		Width: width, Height: height, Now: clock, Home: testHome,
	}
	return f
}

func (f *dashFixture) start(t *testing.T, msgs ...tea.Msg) (*DashboardModel, run) {
	t.Helper()
	m := NewDashboard(f.opts)
	r := drive(t, m, m.Init(), msgs...)
	return m, r
}

func typeText(s string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return msgs
}

func TestDashboardFrames(t *testing.T) {
	t.Parallel()
	states := []struct {
		name  string
		build func(t *testing.T, f *dashFixture) *DashboardModel
	}{
		{name: "loading", build: func(_ *testing.T, f *dashFixture) *DashboardModel {
			m := NewDashboard(f.opts)
			m.Init()
			return m
		}},
		{name: "cached-agents", build: func(_ *testing.T, f *dashFixture) *DashboardModel {
			f.agents.cached, f.agents.hasCached = agent.Snapshot{TakenAt: fixedNow, Agents: sampleAgents()[:2]}, true
			return NewDashboard(f.opts)
		}},
		{name: "empty", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			f.sessions.sessions, f.agents.snap = nil, agent.Snapshot{TakenAt: fixedNow}
			m, _ := f.start(t)
			return m
		}},
		{name: "populated", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			m, _ := f.start(t, press("j"))
			return m
		}},
		{name: "confirm-kill", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			m, _ := f.start(t, press("x"))
			return m
		}},
		{name: "killed", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			m, _ := f.start(t, press("x"), press("y"))
			return m
		}},
		{name: "error", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			f.sessions.err = errors.New("no server running on /tmp/tmux-501/lyna")
			f.agents.err = errors.New("claude: executable file not found in $PATH")
			m, _ := f.start(t)
			return m
		}},
		{name: "create-form", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			m, _ := f.start(t, press("n"))
			return m
		}},
		{name: "create-invalid", build: func(t *testing.T, f *dashFixture) *DashboardModel {
			m, _ := f.start(t, press("n"), press("enter"))
			return m
		}},
	}
	for _, size := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				f := newDashFixture(t, size.width, size.height)
				m := st.build(t, f)
				assertFrame(t, "dashboard/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestDashboardKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		msgs     []tea.Msg
		exec     tea.ExecCommand
		err      error
		killErr  error
		selected string
		attached []string
		killed   []string
		execs    int
		quit     bool
		next     DashboardNext
		confirm  bool
		message  string
		loads    int
	}{
		{name: "starts on the first session", selected: "api", loads: 1},
		{name: "down", msgs: keys("j"), selected: "web", loads: 1},
		{name: "end", msgs: keys("G"), selected: "misc", loads: 1},
		{name: "wheel", msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}}, selected: "web", loads: 1},
		{name: "click selects", msgs: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 4}}, selected: "scratch", loads: 1},
		{name: "click on the column header is ignored", msgs: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 1}}, selected: "api", loads: 1},
		{name: "click past the rows is ignored", msgs: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 12}}, selected: "api", loads: 1},
		{
			name: "double click attaches", selected: "web", attached: []string{"web"}, quit: true, loads: 1,
			msgs: []tea.Msg{tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}, tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3}},
		},
		{name: "enter attaches", msgs: keys("enter"), selected: "api", attached: []string{"api"}, quit: true, loads: 1},
		{name: "enter hands the terminal over", msgs: keys("enter"), exec: fakeExec{name: "attach"}, selected: "api", attached: []string{"api"}, execs: 1, loads: 1},
		{name: "attach failure", msgs: keys("enter"), err: errors.New("session gone"), selected: "api", attached: []string{"api"}, message: "attach failed: session gone", loads: 1},
		{name: "a opens agents", msgs: keys("a"), selected: "api", quit: true, next: NextAgents, loads: 1},
		{name: "s opens setup", msgs: keys("s"), selected: "api", quit: true, next: NextSetup, loads: 1},
		{name: "q quits", msgs: keys("q"), selected: "api", quit: true, loads: 1},
		{name: "esc quits", msgs: keys("esc"), selected: "api", quit: true, loads: 1},
		{name: "r reloads", msgs: keys("r"), selected: "api", loads: 2},
		{name: "x asks", msgs: keys("j", "x"), selected: "web", confirm: true, loads: 1},
		{name: "ctrl+x asks", msgs: keys("ctrl+x"), selected: "api", confirm: true, loads: 1},
		{name: "n cancels the kill", msgs: keys("x", "n"), selected: "api", loads: 1},
		{name: "mouse is ignored while asking", msgs: []tea.Msg{press("x"), tea.MouseClickMsg{Button: tea.MouseLeft, Y: 4}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}}, selected: "api", confirm: true, loads: 1},
		{name: "y kills and reloads", msgs: keys("j", "x", "y"), selected: "web", killed: []string{"web"}, message: "killed session web", loads: 2},
		{name: "kill failure", msgs: keys("x", "y"), killErr: errors.New("permission denied"), selected: "api", killed: []string{"api"}, message: "kill failed: permission denied", loads: 1},
		{name: "ctrl+c quits while asking", msgs: keys("x", "ctrl+c"), selected: "api", confirm: true, quit: true, loads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDashFixture(t, 100, 30)
			f.actions.exec, f.actions.err, f.actions.killErr = tc.exec, tc.err, tc.killErr
			m, r := f.start(t, tc.msgs...)
			if s, _ := m.selected(); s.Name != tc.selected {
				t.Errorf("selected %q, want %q", s.Name, tc.selected)
			}
			if !slices.Equal(f.actions.attached, tc.attached) || !slices.Equal(f.actions.killed, tc.killed) {
				t.Errorf("attached %v killed %v, want %v %v", f.actions.attached, f.actions.killed, tc.attached, tc.killed)
			}
			if len(r.execs) != tc.execs || r.quit != tc.quit || m.Next() != tc.next || (m.confirm != nil) != tc.confirm {
				t.Errorf("execs %d quit %v next %v confirm %v, want %d %v %v %v", len(r.execs), r.quit, m.Next(), m.confirm != nil, tc.execs, tc.quit, tc.next, tc.confirm)
			}
			if m.message != tc.message {
				t.Errorf("message %q, want %q", m.message, tc.message)
			}
			if f.sessions.calls != tc.loads || f.agents.refreshes != tc.loads {
				t.Errorf("loads sessions %d agents %d, want %d", f.sessions.calls, f.agents.refreshes, tc.loads)
			}
		})
	}
}

func TestDashboardCreate(t *testing.T) {
	t.Parallel()
	dirErr := errors.New("not a directory")
	cases := []struct {
		name     string
		defaults CreateRequest
		layouts  []string
		validate func(string) error
		msgs     []tea.Msg
		exec     tea.ExecCommand
		created  []CreateRequest
		formOpen bool
		quit     bool
		execs    int
	}{
		{
			name: "defaults", msgs: append(typeText("~/src/api"), keys("enter", "enter", "enter")...),
			created: []CreateRequest{{Dir: "~/src/api", Layout: "solo", Sandbox: "standard"}}, quit: true,
		},
		{
			name: "prefilled", defaults: CreateRequest{Dir: "/srv/app", Layout: "trio", Sandbox: "strict"}, exec: fakeExec{name: "attach"},
			msgs: keys("enter", "enter", "enter"), created: []CreateRequest{{Dir: "/srv/app", Layout: "trio", Sandbox: "strict"}}, execs: 1,
		},
		{
			name: "choices move", layouts: []string{"duo", "trio"},
			msgs:    append(typeText("/x"), keys("enter", "down", "enter", "down", "down", "enter")...),
			created: []CreateRequest{{Dir: "/x", Layout: "trio", Sandbox: "off"}}, quit: true,
		},
		{name: "empty directory is refused", msgs: keys("enter"), formOpen: true},
		{name: "blank directory is refused", msgs: append(typeText("   "), keys("enter")...), formOpen: true},
		{name: "validator refuses", validate: func(string) error { return dirErr }, msgs: append(typeText("/nope"), keys("enter")...), formOpen: true},
		{name: "esc cancels", msgs: append(typeText("/x"), keys("esc")...)},
		{name: "letters are typed, not bound", msgs: typeText("qaxsrn"), formOpen: true},
		{name: "ctrl+c quits", msgs: keys("ctrl+c"), formOpen: true, quit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDashFixture(t, 100, 30)
			f.opts.Defaults, f.opts.Layouts, f.opts.ValidateDir = tc.defaults, tc.layouts, tc.validate
			f.actions.exec = tc.exec
			m, _ := f.start(t, press("n"))
			if m.form == nil {
				t.Fatal("n did not open the form")
			}
			r := drive(t, m, nil, tc.msgs...)
			if !slices.Equal(f.actions.created, tc.created) {
				t.Errorf("created %+v, want %+v", f.actions.created, tc.created)
			}
			if (m.form != nil) != tc.formOpen || r.quit != tc.quit || len(r.execs) != tc.execs {
				t.Errorf("form open %v quit %v execs %d, want %v %v %d", m.form != nil, r.quit, len(r.execs), tc.formOpen, tc.quit, tc.execs)
			}
			if m.Next() != NextNone || len(f.actions.attached) != 0 || len(f.actions.killed) != 0 {
				t.Error("form keys reached the dashboard bindings")
			}
		})
	}
}

func TestDashboardCreateFailure(t *testing.T) {
	t.Parallel()
	f := newDashFixture(t, 100, 30)
	f.actions.err = errors.New("git worktree add: exit status 128")
	m, r := f.start(t, append(append([]tea.Msg{press("n")}, typeText("/srv")...), keys("enter", "enter", "enter")...)...)
	if r.quit || m.form != nil || m.message != "create failed: git worktree add: exit status 128" {
		t.Errorf("quit %v form %v message %q", r.quit, m.form != nil, m.message)
	}
}

func TestDashboardExecDone(t *testing.T) {
	t.Parallel()
	f := newDashFixture(t, 100, 30)
	m, _ := f.start(t)
	r := drive(t, m, nil, execDoneMsg{err: errors.New("attach: lost server")})
	if r.quit || m.message != "attach: lost server" {
		t.Errorf("quit %v message %q", r.quit, m.message)
	}
	if r = drive(t, m, nil, execDoneMsg{}); !r.quit {
		t.Error("finished attach did not quit")
	}
}

func TestDashboardRefreshTick(t *testing.T) {
	t.Parallel()
	f := newDashFixture(t, 100, 30)
	f.opts.RefreshEvery = time.Second
	m, _ := f.start(t)
	if m.tick() == nil {
		t.Fatal("tick not scheduled")
	}
	drive(t, m, nil, dashTickMsg{})
	if f.sessions.calls != 2 || f.agents.refreshes != 2 {
		t.Errorf("tick loads %d %d", f.sessions.calls, f.agents.refreshes)
	}
	m.loading = 1
	drive(t, m, nil, dashTickMsg{}, press("r"))
	if f.sessions.calls != 2 {
		t.Error("tick or r reloaded while a load was in flight")
	}
}

func TestDashboardKeepsSelectionAcrossReloads(t *testing.T) {
	t.Parallel()
	f := newDashFixture(t, 100, 30)
	m, _ := f.start(t, keys("j", "j")...)
	f.sessions.sessions = append([]tmux.Session{{Name: "new", Windows: 1}}, sampleSessions()...)
	drive(t, m, nil, press("r"))
	if s, _ := m.selected(); s.Name != "scratch" {
		t.Errorf("selection moved to %q after reload", s.Name)
	}
	f.sessions.err = errors.New("server exited")
	drive(t, m, nil, press("r"))
	if len(m.sessions) != 6 || !strings.Contains(ansi.Strip(m.View().Content), "could not list sessions: server exited") {
		t.Errorf("failed reload frame:\n%s", ansi.Strip(m.View().Content))
	}
}

func TestDashboardWithoutSources(t *testing.T) {
	t.Parallel()
	m := NewDashboard(DashboardOptions{Styles: goldenStyles(t)})
	r := drive(t, m, m.Init(), keys("enter", "x", "n", "r")...)
	if r.quit || m.form != nil || m.confirm != nil {
		t.Errorf("quit %v form %v confirm %v", r.quit, m.form != nil, m.confirm != nil)
	}
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"no sessions on the lyna-tmux server", "no Claude agents running"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q", want)
		}
	}
	v := m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("view alt %v mouse %v", v.AltScreen, v.MouseMode)
	}
}

func TestDashboardSanitizesUntrustedText(t *testing.T) {
	t.Parallel()
	f := newDashFixture(t, 100, 30)
	f.sessions.sessions = []tmux.Session{{
		Name: "evil\x1b]0;owned\x07name", Windows: 1, Managed: true, Layout: "\x1b[2Jtrio", Sandbox: "cust\x07om", Project: "/tmp/\x1b]8;;http://x\x1b\\p",
	}}
	f.agents.snap = agent.Snapshot{Agents: []agent.Agent{{
		Record: agent.Record{PID: 1, Name: "a\x1b]52;c;eA==\x07b"}, Source: agent.SourceExternal, Status: agent.StatusUnknown,
	}}}
	m, _ := f.start(t, press("x"))
	frame := m.View().Content
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x1b\\"} {
		if strings.Contains(frame, bad) {
			t.Errorf("frame carries %q", bad)
		}
	}
	if !strings.Contains(ansi.Strip(frame), "kill session evilname and its 1 window?") {
		t.Errorf("confirm prompt:\n%s", ansi.Strip(frame))
	}
}

func TestDashboardResize(t *testing.T) {
	t.Parallel()
	for _, form := range []bool{false, true} {
		for _, size := range []struct{ w, h int }{{100, 30}, {60, 20}, {40, 8}, {12, 3}, {1, 1}, {0, 0}} {
			t.Run(strconv.FormatBool(form)+"-"+strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h), func(t *testing.T) {
				t.Parallel()
				f := newDashFixture(t, 100, 30)
				var msgs []tea.Msg
				if form {
					msgs = append(msgs, press("n"))
				}
				msgs = append(msgs, press("G"), resize(size.w, size.h))
				m, _ := f.start(t, msgs...)
				w, h := max(size.w, 1), max(size.h, 1)
				lines := strings.Split(m.View().Content, "\n")
				if len(lines) != h {
					t.Fatalf("%d lines, want %d", len(lines), h)
				}
				for i, l := range lines {
					if ansi.StringWidth(l) > w {
						t.Errorf("line %d is %d cells wide, over %d", i, ansi.StringWidth(l), w)
					}
				}
				if !form && (m.list.cursor < m.list.offset || m.list.cursor >= m.list.offset+m.sessionRows()) {
					t.Errorf("selection out of view: cursor %d offset %d rows %d", m.list.cursor, m.list.offset, m.sessionRows())
				}
			})
		}
	}
}
