package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// SessionSource lists the sessions of the lyna-tmux server.
type SessionSource interface {
	Sessions(ctx context.Context) ([]tmux.Session, error)
}

// CreateRequest is what the create form collects.
type CreateRequest struct {
	Dir     string
	Layout  string
	Sandbox string
}

// DashboardActions are the effects of the dashboard.
type DashboardActions interface {
	// Attach attaches the terminal to a session; the command runs with the
	// terminal handed over and the dashboard quits afterwards.
	Attach(ctx context.Context, s tmux.Session) (tea.ExecCommand, error)
	// Kill kills a session on the lyna-tmux server.
	Kill(ctx context.Context, s tmux.Session) error
	// Create creates (or finds) the workspace for a directory and returns
	// the command that attaches to it.
	Create(ctx context.Context, req CreateRequest) (tea.ExecCommand, error)
}

// DashboardNext is the screen the caller opens after the dashboard quits.
type DashboardNext int

const (
	// NextNone means the dashboard finished on its own.
	NextNone DashboardNext = iota
	// NextAgents opens the agents picker.
	NextAgents
	// NextSetup opens the setup wizard.
	NextSetup
)

// DashboardOptions configure the dashboard.
type DashboardOptions struct {
	Styles   Styles
	Sessions SessionSource
	Agents   AgentSource
	Actions  DashboardActions
	Context  context.Context
	// Width and Height size the first frame.
	Width, Height int
	Now           func() time.Time
	Home          string
	// Layouts are the create form's layout choices (built-in and custom).
	Layouts []string
	// Defaults prefill the create form.
	Defaults CreateRequest
	// ValidateDir checks the create form's directory; nil accepts any
	// non-empty value.
	ValidateDir func(string) error
	// RefreshEvery re-lists sessions and agents; 0 disables it.
	RefreshEvery time.Duration
}

type (
	sessionsLoadedMsg struct {
		sessions []tmux.Session
		err      error
	}
	dashAgentsMsg struct {
		snap agent.Snapshot
		err  error
	}
	dashTickMsg struct{}
	dashDoneMsg struct {
		verb    string
		exec    tea.ExecCommand
		err     error
		session string
	}
)

// DashboardModel is the start screen: sessions, an agents summary and quick
// actions.
type DashboardModel struct {
	opts           DashboardOptions
	ctx            context.Context
	now            func() time.Time
	keys           dashKeys
	nav            navKeys
	width, height  int
	sessions       []tmux.Session
	list           listView
	sessionsLoaded bool
	sessionsErr    error
	agents         []agent.Agent
	agentsLoaded   bool
	agentsErr      error
	loading        int
	confirm        *tmux.Session
	message        string
	msgErr         bool
	form           *huh.Form
	request        *CreateRequest
	clicks         clicks
	next           DashboardNext
}

type dashKeys struct {
	Attach, New, Kill, Agents, Setup, Refresh, Quit, Yes, No, Cancel key.Binding
}

// NewDashboard builds the dashboard; the cached agents snapshot draws in the
// first frame.
func NewDashboard(opts DashboardOptions) *DashboardModel {
	w, h := sizeOr(opts.Width, opts.Height)
	m := &DashboardModel{
		opts:   opts,
		ctx:    ctxOr(opts.Context),
		now:    nowOr(opts.Now),
		nav:    newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		width:  w,
		height: h,
		keys: dashKeys{
			Attach:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
			New:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
			Kill:    key.NewBinding(key.WithKeys("x", "ctrl+x"), key.WithHelp("x", "kill")),
			Agents:  key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agents")),
			Setup:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "setup")),
			Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Quit:    key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
			Yes:     key.NewBinding(key.WithKeys("y", "Y", "enter")),
			No:      key.NewBinding(key.WithKeys("n", "N", "esc", "q")),
			Cancel:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		},
	}
	if opts.Agents != nil {
		if snap, ok := opts.Agents.Cached(); ok {
			m.setAgents(snap)
		}
	}
	return m
}

// Next reports which screen to open after the dashboard quit.
func (m *DashboardModel) Next() DashboardNext { return m.next }

// Init loads sessions and agents.
func (m *DashboardModel) Init() tea.Cmd {
	return tea.Batch(m.loadCmds(), m.tick())
}

func (m *DashboardModel) loadCmds() tea.Cmd {
	var cmds []tea.Cmd
	ctx := m.ctx
	if src := m.opts.Sessions; src != nil {
		m.loading++
		cmds = append(cmds, func() tea.Msg {
			s, err := src.Sessions(ctx)
			return sessionsLoadedMsg{sessions: s, err: err}
		})
	} else {
		m.sessionsLoaded = true
	}
	if src := m.opts.Agents; src != nil {
		m.loading++
		cmds = append(cmds, func() tea.Msg {
			snap, err := src.Refresh(ctx)
			return dashAgentsMsg{snap: snap, err: err}
		})
	} else {
		m.agentsLoaded = true
	}
	return tea.Batch(cmds...)
}

func (m *DashboardModel) tick() tea.Cmd {
	if m.opts.RefreshEvery <= 0 {
		return nil
	}
	return tea.Tick(m.opts.RefreshEvery, func(time.Time) tea.Msg { return dashTickMsg{} })
}

func (m *DashboardModel) setAgents(snap agent.Snapshot) {
	m.agents = append(m.agents[:0], snap.Agents...)
	agent.Sort(m.agents)
	m.agentsLoaded = true
}

// Update handles messages.
func (m *DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.list.clamp(len(m.sessions), m.sessionRows())
		if m.form != nil {
			m.sizeForm()
		}
		return m, nil
	case sessionsLoadedMsg:
		m.loading = max(m.loading-1, 0)
		m.sessionsLoaded = true
		m.sessionsErr = msg.err
		if msg.err == nil {
			selected := m.selectedName()
			m.sessions = msg.sessions
			m.list.cursor = 0
			for i, s := range m.sessions {
				if s.Name == selected {
					m.list.cursor = i
				}
			}
			m.list.clamp(len(m.sessions), m.sessionRows())
		}
		return m, nil
	case dashAgentsMsg:
		m.loading = max(m.loading-1, 0)
		m.agentsErr = msg.err
		if msg.err == nil {
			m.setAgents(msg.snap)
		}
		return m, nil
	case dashTickMsg:
		var load tea.Cmd
		if m.loading == 0 {
			load = m.loadCmds()
		}
		return m, tea.Batch(load, m.tick())
	case dashDoneMsg:
		return m.done(msg)
	case execDoneMsg:
		if msg.err != nil {
			m.setMessage(msg.err.Error(), true)
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.form != nil {
			return m.updateForm(msg)
		}
		return m.key(msg)
	case tea.MouseClickMsg:
		return m.click(msg)
	case tea.MouseWheelMsg:
		if m.form == nil && m.confirm == nil {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.list.move(-1, len(m.sessions), m.sessionRows())
			case tea.MouseWheelDown:
				m.list.move(1, len(m.sessions), m.sessionRows())
			}
		}
		return m, nil
	}
	if m.form != nil {
		return m.updateForm(msg)
	}
	return m, nil
}

func (m *DashboardModel) selectedName() string {
	if s, ok := m.selected(); ok {
		return s.Name
	}
	return ""
}

func (m *DashboardModel) selected() (tmux.Session, bool) {
	if m.list.cursor < 0 || m.list.cursor >= len(m.sessions) {
		return tmux.Session{}, false
	}
	return m.sessions[m.list.cursor], true
}

func (m *DashboardModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.message = ""
	if m.confirm != nil {
		switch {
		case key.Matches(msg, m.keys.Yes):
			s := *m.confirm
			m.confirm = nil
			return m, m.actionCmd("kill", s.Name, func() (tea.ExecCommand, error) {
				return nil, m.opts.Actions.Kill(m.ctx, s)
			})
		case key.Matches(msg, m.keys.No):
			m.confirm = nil
		}
		return m, nil
	}
	if d, ok := m.nav.delta(msg, len(m.sessions), m.sessionRows()); ok {
		m.list.move(d, len(m.sessions), m.sessionRows())
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Attach):
		return m, m.attach()
	case key.Matches(msg, m.keys.New):
		return m, m.openForm()
	case key.Matches(msg, m.keys.Kill):
		if s, ok := m.selected(); ok && m.opts.Actions != nil {
			m.confirm = &s
		}
	case key.Matches(msg, m.keys.Agents):
		m.next = NextAgents
		return m, tea.Quit
	case key.Matches(msg, m.keys.Setup):
		m.next = NextSetup
		return m, tea.Quit
	case key.Matches(msg, m.keys.Refresh):
		if m.loading == 0 {
			return m, m.loadCmds()
		}
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	}
	return m, nil
}

func (m *DashboardModel) click(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.form != nil || m.confirm != nil || msg.Button != tea.MouseLeft {
		return m, nil
	}
	// Rows start below the header bar and the column header.
	row := msg.Y - 2
	if row < 0 || row >= m.sessionRows() {
		return m, nil
	}
	idx := m.list.offset + row
	if idx >= len(m.sessions) {
		return m, nil
	}
	m.list.move(idx-m.list.cursor, len(m.sessions), m.sessionRows())
	if m.clicks.click(m.now(), idx) {
		return m, m.attach()
	}
	return m, nil
}

func (m *DashboardModel) attach() tea.Cmd {
	s, ok := m.selected()
	if !ok || m.opts.Actions == nil {
		return nil
	}
	return m.actionCmd("attach", s.Name, func() (tea.ExecCommand, error) {
		return m.opts.Actions.Attach(m.ctx, s)
	})
}

func (m *DashboardModel) actionCmd(verb, session string, run func() (tea.ExecCommand, error)) tea.Cmd {
	if m.opts.Actions == nil {
		return nil
	}
	return func() tea.Msg {
		exe, err := run()
		return dashDoneMsg{verb: verb, exec: exe, err: err, session: session}
	}
}

func (m *DashboardModel) done(msg dashDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setMessage(msg.verb+" failed: "+msg.err.Error(), true)
		return m, nil
	}
	if msg.verb == "kill" {
		m.setMessage("killed session "+msg.session, false)
		if m.loading == 0 {
			return m, m.loadCmds()
		}
		return m, nil
	}
	if msg.exec != nil {
		return m, tea.Exec(msg.exec, func(err error) tea.Msg { return execDoneMsg{err: err} })
	}
	return m, tea.Quit
}

func (m *DashboardModel) setMessage(text string, isErr bool) {
	m.message, m.msgErr = sanitize.Line(text), isErr
}

func (m *DashboardModel) formWidth() int { return max(min(m.width-4, 72), 20) }

func (m *DashboardModel) openForm() tea.Cmd {
	if m.opts.Actions == nil {
		return nil
	}
	req := m.opts.Defaults
	if req.Sandbox == "" {
		req.Sandbox = config.Default().Sandbox.Profile
	}
	layouts := m.opts.Layouts
	if len(layouts) == 0 {
		layouts = config.Choices("workspace.layout")
	}
	if req.Layout == "" {
		req.Layout = layouts[0]
	}
	m.request = &req
	validate := m.opts.ValidateDir
	m.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Directory").Description("project to open; an existing workspace for it is reused").
			Value(&m.request.Dir).CharLimit(4096).
			Validate(func(dir string) error {
				if strings.TrimSpace(dir) == "" {
					return errors.New("a directory is required")
				}
				if validate != nil {
					return validate(dir)
				}
				return nil
			}),
		huh.NewSelect[string]().Title("Layout").Options(huh.NewOptions(layouts...)...).Value(&m.request.Layout),
		huh.NewSelect[string]().Title("Sandbox").Options(sandboxOptions()...).Value(&m.request.Sandbox),
	).Title("New workspace")).
		WithTheme(m.opts.Styles.huhTheme()).
		WithShowHelp(false).
		WithWidth(m.formWidth())
	cmd := m.form.Init()
	m.sizeForm()
	return cmd
}

// formChrome is the rows around a form: the header, the blank line above the
// form, the footer, and the blank row plus up to two wrapped rows a
// validation error adds below the form.
const formChrome = 6

// sizeForm fits the form below the header and above the footer; a form taller
// than that scrolls to its focused field and keeps its errors in view.
func (m *DashboardModel) sizeForm() {
	m.form = m.form.WithWidth(m.formWidth())
	if f, ok := formModel(m.form.Update(tea.WindowSizeMsg{Width: m.formWidth(), Height: max(m.height-formChrome, 3)})); ok {
		m.form = f
	}
}

// formModel unwraps the form from a huh update result.
func formModel(model huh.Model, _ tea.Cmd) (*huh.Form, bool) {
	f, ok := model.(*huh.Form)
	return f, ok
}

// sandboxOptions explain each profile in one line.
func sandboxOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("standard  sandboxed Bash, credentials denied", "standard"),
		huh.NewOption("strict    adds a network allowlist, no escape hatch", "strict"),
		huh.NewOption("off       no sandbox (a red shield)", "off"),
	}
}

func (m *DashboardModel) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && key.Matches(k, m.keys.Cancel) {
		m.form, m.request = nil, nil
		return m, nil
	}
	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		req := *m.request
		m.form, m.request = nil, nil
		return m, m.actionCmd("create", "", func() (tea.ExecCommand, error) {
			return m.opts.Actions.Create(m.ctx, req)
		})
	case huh.StateAborted:
		m.form, m.request = nil, nil
		return m, nil
	}
	return m, cmd
}

func (m *DashboardModel) bodyHeight() int { return max(m.height-2, 1) }

func (m *DashboardModel) agentRows() int {
	return min(max(len(m.agents), 1), 5)
}

func (m *DashboardModel) sessionRows() int {
	return max(m.bodyHeight()-1-1-m.agentRows(), 1)
}

// View draws the dashboard.
func (m *DashboardModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *DashboardModel) render() string {
	lines := []string{m.header()}
	if m.form != nil {
		lines = append(lines, "")
		for l := range strings.SplitSeq(m.form.View(), "\n") {
			lines = append(lines, "  "+l)
		}
	} else {
		lines = append(lines, m.sessionLines()...)
		lines = append(lines, m.opts.Styles.rule(m.width, "agents"))
		lines = append(lines, m.agentLines()...)
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], m.footer())
	return screen(lines, m.width, m.height)
}

func (m *DashboardModel) header() string {
	s := m.opts.Styles
	left := []segment{seg(" "+s.Theme.Icons.Brand+" lyna-tmux ", s.Title)}
	if m.sessionsLoaded && m.sessionsErr == nil {
		attached := 0
		for _, ss := range m.sessions {
			if ss.Attached > 0 {
				attached++
			}
		}
		left = append(left, seg(" "+plural(len(m.sessions), "session", "sessions"), s.Text))
		if attached > 0 {
			left = append(left, seg("  "+strconv.Itoa(attached)+" attached", s.Muted))
		}
	}
	var right []segment
	if m.form != nil {
		right = append(right, seg("new workspace ", s.Accent2))
	} else if m.loading > 0 {
		right = append(right, seg("refreshing ", s.Muted))
	}
	return s.bar(m.width, left, right)
}

type sessionColumns struct {
	name, layout, sandbox, project int
}

func (m *DashboardModel) sessionColumns() sessionColumns {
	c := sessionColumns{name: 20}
	const fixed = 1 + 2 + 5 // marker, attached icon, windows
	if m.width >= 72 {
		c.layout, c.sandbox = 8, 11
	}
	rest := m.width - fixed - c.name - 1
	if c.layout > 0 {
		rest -= c.layout + c.sandbox + 2
	}
	if rest >= 12 {
		c.project = rest - 1
	} else {
		c.name = max(c.name+rest, 8)
	}
	return c
}

func (m *DashboardModel) sessionLines() []string {
	s := m.opts.Styles
	rows := m.sessionRows()
	cols := m.sessionColumns()
	head := []segment{seg("   "+fit("SESSION", cols.name, s.Ellipsis), s.Muted), seg(" "+fitRight("WIN", 4, s.Ellipsis), s.Muted)}
	if cols.layout > 0 {
		head = append(head, seg(" "+fit("LAYOUT", cols.layout, s.Ellipsis), s.Muted), seg(" "+fit("SANDBOX", cols.sandbox, s.Ellipsis), s.Muted))
	}
	if cols.project > 0 {
		head = append(head, seg(" "+fit("PROJECT", cols.project, s.Ellipsis), s.Muted))
	}
	lines := []string{s.line(m.width, nil, head...)}
	body := make([]string, 0, rows)
	switch {
	case !m.sessionsLoaded:
		body = append(body, s.Muted.Render(center("listing sessions", m.width, s.Ellipsis)))
	case m.sessionsErr != nil:
		body = append(body, " "+s.Danger.Render(truncate("could not list sessions: "+sanitize.Line(m.sessionsErr.Error()), m.width-2, s.Ellipsis)))
	case len(m.sessions) == 0:
		body = append(body,
			s.Text.Render(center("no sessions on the lyna-tmux server", m.width, s.Ellipsis)),
			s.Muted.Render(center("press n to open a workspace", m.width, s.Ellipsis)))
	default:
		end := min(m.list.offset+rows, len(m.sessions))
		for i := m.list.offset; i < end; i++ {
			body = append(body, m.sessionRow(m.sessions[i], cols, i == m.list.cursor))
		}
	}
	for len(body) < rows {
		body = append(body, "")
	}
	return append(lines, body[:rows]...)
}

func (m *DashboardModel) sessionRow(ss tmux.Session, cols sessionColumns, selected bool) string {
	s := m.opts.Styles
	ic := s.Theme.Icons
	marker := " "
	var bg *lipgloss.Style
	if selected {
		marker = "▌"
		if ic.Name == "ascii" {
			marker = ">"
		}
		bg = &s.Selected
	}
	attached := "  "
	if ss.Attached > 0 {
		attached = ic.Idle + " "
	}
	segs := []segment{
		seg(marker, s.Accent),
		seg(attached, s.Idle),
		seg(fit(sanitize.Line(ss.Name), cols.name, s.Ellipsis), s.Text),
		seg(" "+fitRight(strconv.Itoa(ss.Windows), 4, s.Ellipsis), s.Muted),
	}
	if cols.layout > 0 {
		layout, sandbox, sandboxStyle := "-", "-", s.Muted
		if ss.Managed {
			layout = sanitize.Line(ss.Layout)
			switch ss.Sandbox {
			case "off":
				sandbox, sandboxStyle = ic.ShieldOff+" OFF", s.Danger
			case "strict":
				sandbox, sandboxStyle = ic.Shield+" strict", s.Success
			case "":
				sandbox = "-"
			default:
				sandbox = ic.Shield + " " + sanitize.Line(ss.Sandbox)
			}
		}
		segs = append(segs, seg(" "+fit(layout, cols.layout, s.Ellipsis), s.Accent2), seg(" "+fit(sandbox, cols.sandbox, s.Ellipsis), sandboxStyle))
	}
	if cols.project > 0 {
		project := ss.Project
		if project == "" {
			project = ss.Path
		}
		segs = append(segs, seg(" "+fit(shortPath(sanitize.Line(project), m.opts.Home), cols.project, s.Ellipsis), s.Muted))
	}
	return s.line(m.width, bg, segs...)
}

func (m *DashboardModel) agentLines() []string {
	s := m.opts.Styles
	rows := m.agentRows()
	var lines []string
	switch {
	case !m.agentsLoaded && m.agentsErr != nil:
		lines = append(lines, " "+s.Danger.Render(truncate("could not list agents: "+sanitize.Line(m.agentsErr.Error()), m.width-2, s.Ellipsis)))
	case !m.agentsLoaded:
		lines = append(lines, " "+s.Muted.Render("listing agents"))
	case len(m.agents) == 0:
		lines = append(lines, " "+s.Muted.Render("no Claude agents running"))
	default:
		for _, a := range m.agents[:min(rows, len(m.agents))] {
			style, icon := s.Status(a.Status)
			title := sanitize.Line(a.Title())
			loc := locationText(a, m.opts.Home)
			locWidth := min(max(m.width/3, 10), 30)
			lines = append(lines, s.line(m.width, nil,
				seg(" "+icon+" ", style),
				seg(fit(statusWord(a.Status), 8, s.Ellipsis), style),
				seg(fit(title, max(m.width-14-locWidth, 4), s.Ellipsis), s.Text),
				seg(" "+fit(loc, locWidth-1, s.Ellipsis), s.Accent2)))
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines[:rows]
}

func (m *DashboardModel) footer() string {
	s := m.opts.Styles
	switch {
	case m.form != nil:
		return s.helpLine(m.width,
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
			key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
			m.keys.Cancel)
	case m.confirm != nil:
		prompt := " kill session " + sanitize.Line(m.confirm.Name) + " and its " + plural(m.confirm.Windows, "window", "windows") + "? "
		return s.line(m.width, nil, seg(prompt, s.Danger.Bold(true)), seg("y", s.Key), seg(" confirm  ", s.Muted), seg("n", s.Key), seg(" cancel", s.Muted))
	case m.message != "":
		style := s.Success
		if m.msgErr {
			style = s.Danger
		}
		return s.line(m.width, nil, seg(" "+m.message, style))
	}
	return s.helpLine(m.width, m.keys.Attach, m.keys.New, m.keys.Kill, m.keys.Agents, m.keys.Setup, m.keys.Refresh, m.keys.Quit)
}
