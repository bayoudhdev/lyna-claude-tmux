package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// AgentSource lists Claude agents.
type AgentSource interface {
	// Cached returns the last saved snapshot without blocking, so the first
	// frame draws immediately; ok is false when there is none.
	Cached() (snap agent.Snapshot, ok bool)
	// Refresh lists the agents now (and typically saves the snapshot).
	Refresh(ctx context.Context) (agent.Snapshot, error)
}

// PreviewSource captures what an agent's pane shows.
type PreviewSource interface {
	// Preview returns up to lines recent lines of the agent's pane. The text
	// is untrusted and may carry escape sequences; the view sanitizes it.
	Preview(ctx context.Context, a agent.Agent, lines int) (string, error)
}

// AgentActions are the effects of the agents picker.
type AgentActions interface {
	// Jump brings the client to the agent's pane. A non-nil command is run
	// with the terminal handed over (for example attaching a session inside
	// a popup); the picker quits afterwards either way.
	Jump(ctx context.Context, a agent.Agent) (tea.ExecCommand, error)
	// Attach opens a background agent that has no pane.
	Attach(ctx context.Context, a agent.Agent) (tea.ExecCommand, error)
	// Kill terminates the agent's process. Implementations re-verify the
	// process identity (PID and start time) before signaling.
	Kill(ctx context.Context, a agent.Agent) error
}

// AgentAction is an extra picker action bound to a key.
type AgentAction struct {
	// Key is the key as tea.KeyPressMsg.String spells it, for example "R".
	Key   string
	Label string
	// Enabled reports whether the action applies to the selected agent; nil
	// means always.
	Enabled func(agent.Agent) bool
	Run     func(ctx context.Context, a agent.Agent) (ActionResult, error)
}

// ActionResult tells the picker what to do after an extra action.
type ActionResult struct {
	// Exec is run with the terminal handed over, then the picker quits.
	Exec tea.ExecCommand
	// Quit closes the picker.
	Quit bool
	// Refresh lists the agents again.
	Refresh bool
	// Message is shown in the footer.
	Message string
}

// AgentsOptions configure the agents picker.
type AgentsOptions struct {
	Styles  Styles
	Source  AgentSource
	Preview PreviewSource
	Actions AgentActions
	Extra   []AgentAction
	// Popup draws the picker as the overlay it is in a tmux popup: quitting
	// takes the pane it was opened from back, which the footer names, and a
	// jump closes the overlay on the client it switched.
	Popup bool
	// Context bounds every source and action call; nil means background.
	Context context.Context
	// Width and Height size the first frame, before the terminal reports
	// its size.
	Width, Height int
	// Now is the clock for ages; nil means time.Now.
	Now func() time.Time
	// Home shortens paths to "~".
	Home string
	// RefreshEvery re-lists agents while the picker is open; 0 disables it.
	RefreshEvery time.Duration
	// PreviewEvery re-captures the selected pane; 0 disables it.
	PreviewEvery time.Duration
}

type (
	agentsLoadedMsg struct {
		snap agent.Snapshot
		err  error
	}
	previewLoadedMsg struct {
		key     string
		content string
		err     error
	}
	agentsTickMsg  struct{}
	previewTickMsg struct{}
	agentsDoneMsg  struct {
		agent  agent.Agent
		result ActionResult
		err    error
		verb   string
	}
	execDoneMsg struct{ err error }
)

type preview struct {
	content string
	err     error
}

// AgentsModel is the agents picker.
type AgentsModel struct {
	opts    AgentsOptions
	ctx     context.Context
	now     func() time.Time
	keys    agentKeys
	nav     navKeys
	width   int
	height  int
	all     []agent.Agent
	visible []int
	list    listView
	selKey  string
	takenAt time.Time
	live    bool
	loaded  bool
	loading bool
	err     error
	message string
	msgErr  bool
	filter  lineInput
	editing bool
	confirm *agent.Agent
	removed map[string]bool
	preview map[string]preview
	clicks  clicks
	chosen  *agent.Agent
	quit    bool
}

type agentKeys struct {
	Enter, Kill, Filter, Refresh, Quit, Esc, Yes, No key.Binding
}

// NewAgents builds the picker and draws the cached snapshot, if any, in its
// first frame; Init starts the live refresh.
func NewAgents(opts AgentsOptions) *AgentsModel {
	w, h := sizeOr(opts.Width, opts.Height)
	m := &AgentsModel{
		opts:    opts,
		ctx:     ctxOr(opts.Context),
		now:     nowOr(opts.Now),
		nav:     newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		width:   w,
		height:  h,
		removed: map[string]bool{},
		preview: map[string]preview{},
		filter:  lineInput{limit: 128},
		keys: agentKeys{
			Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "jump")),
			Kill:    key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "kill")),
			Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
			Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", agentsQuitWord(opts.Popup))),
			Esc:     key.NewBinding(key.WithKeys("esc")),
			Yes:     key.NewBinding(key.WithKeys("y", "Y", "enter")),
			No:      key.NewBinding(key.WithKeys("n", "N", "esc", "q")),
		},
	}
	if opts.Source == nil {
		// Nothing to list from: an empty, settled picker.
		m.loaded = true
		return m
	}
	if snap, ok := opts.Source.Cached(); ok {
		m.setSnapshot(snap)
		m.live = false
	}
	m.loading = !m.loaded
	return m
}

// agentsQuitWord names what the quit key does where the picker draws: a popup
// is an overlay that goes away, a terminal keeps the program's screen.
func agentsQuitWord(popup bool) string {
	if popup {
		return "close"
	}
	return "quit"
}

// Chosen returns the agent the user jumped to or attached, if any.
func (m *AgentsModel) Chosen() (agent.Agent, bool) {
	if m.chosen == nil {
		return agent.Agent{}, false
	}
	return *m.chosen, true
}

// Init starts the live refresh, the preview of the cached selection and the
// periodic ticks.
func (m *AgentsModel) Init() tea.Cmd {
	m.loading = m.opts.Source != nil
	return tea.Batch(m.refreshCmd(), m.previewCmd(), m.agentsTick(), m.previewTick())
}

func (m *AgentsModel) refreshCmd() tea.Cmd {
	src, ctx := m.opts.Source, m.ctx
	if src == nil {
		return nil
	}
	return func() tea.Msg {
		snap, err := src.Refresh(ctx)
		return agentsLoadedMsg{snap: snap, err: err}
	}
}

// startRefresh lists the agents again unless a listing is already running.
func (m *AgentsModel) startRefresh() tea.Cmd {
	if m.loading || m.opts.Source == nil {
		return nil
	}
	m.loading = true
	return m.refreshCmd()
}

func (m *AgentsModel) previewCmd() tea.Cmd {
	a, ok := m.selected()
	if !ok || m.opts.Preview == nil || a.Location == nil {
		return nil
	}
	src, ctx, lines, k := m.opts.Preview, m.ctx, max(m.previewHeight(), 1), a.Record.Key()
	return func() tea.Msg {
		content, err := src.Preview(ctx, a, lines)
		return previewLoadedMsg{key: k, content: content, err: err}
	}
}

func (m *AgentsModel) agentsTick() tea.Cmd {
	if m.opts.RefreshEvery <= 0 {
		return nil
	}
	return tea.Tick(m.opts.RefreshEvery, func(time.Time) tea.Msg { return agentsTickMsg{} })
}

func (m *AgentsModel) previewTick() tea.Cmd {
	if m.opts.PreviewEvery <= 0 || m.opts.Preview == nil {
		return nil
	}
	return tea.Tick(m.opts.PreviewEvery, func(time.Time) tea.Msg { return previewTickMsg{} })
}

func (m *AgentsModel) setSnapshot(snap agent.Snapshot) {
	m.all = m.all[:0]
	for _, a := range snap.Agents {
		if !m.removed[a.Record.Key()] {
			m.all = append(m.all, a)
		}
	}
	agent.Sort(m.all)
	m.takenAt = snap.TakenAt
	m.loaded = true
	m.live = true
	m.applyFilter()
}

// applyFilter recomputes the visible rows and keeps the selected agent
// selected when it is still visible.
func (m *AgentsModel) applyFilter() {
	terms := strings.Fields(strings.ToLower(m.filter.value))
	m.visible = m.visible[:0]
	for i, a := range m.all {
		if matchAgent(a, terms) {
			m.visible = append(m.visible, i)
		}
	}
	m.list.cursor = 0
	for vi, i := range m.visible {
		if m.all[i].Record.Key() == m.selKey {
			m.list.cursor = vi
			break
		}
	}
	m.list.clamp(len(m.visible), m.listHeight())
	m.syncSelection()
}

func (m *AgentsModel) syncSelection() {
	if a, ok := m.selected(); ok {
		m.selKey = a.Record.Key()
	}
}

func matchAgent(a agent.Agent, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{
		a.Title(), a.Record.Name, a.Record.CWD, string(a.Source), string(a.Status), a.Record.WaitingFor, locationText(a, ""),
	}, " "))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (m *AgentsModel) selected() (agent.Agent, bool) {
	if m.list.cursor < 0 || m.list.cursor >= len(m.visible) {
		return agent.Agent{}, false
	}
	return m.all[m.visible[m.list.cursor]], true
}

// Update handles messages.
func (m *AgentsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.list.clamp(len(m.visible), m.listHeight())
		return m, nil
	case agentsLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		prev := m.selKey
		m.removed = map[string]bool{}
		m.setSnapshot(msg.snap)
		if _, have := m.preview[m.selKey]; m.selKey != prev || !have {
			return m, m.previewCmd()
		}
		return m, nil
	case previewLoadedMsg:
		m.preview[msg.key] = preview{content: msg.content, err: msg.err}
		return m, nil
	case agentsTickMsg:
		return m, tea.Batch(m.startRefresh(), m.agentsTick())
	case previewTickMsg:
		return m, tea.Batch(m.previewCmd(), m.previewTick())
	case agentsDoneMsg:
		return m.actionDone(msg)
	case execDoneMsg:
		if msg.err != nil {
			m.chosen = nil
			m.setMessage(msg.err.Error(), true)
			return m, nil
		}
		m.quit = true
		return m, tea.Quit
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.MouseClickMsg:
		return m.mouseClick(msg)
	case tea.MouseWheelMsg:
		if m.confirm == nil {
			switch msg.Button {
			case tea.MouseWheelUp:
				return m, m.move(-1)
			case tea.MouseWheelDown:
				return m, m.move(1)
			}
		}
	}
	return m, nil
}

func (m *AgentsModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		m.quit = true
		return m, tea.Quit
	}
	m.message = ""
	if m.confirm != nil {
		switch {
		case key.Matches(msg, m.keys.Yes):
			a := *m.confirm
			m.confirm = nil
			return m, m.killCmd(a)
		case key.Matches(msg, m.keys.No):
			m.confirm = nil
		}
		return m, nil
	}
	if m.editing {
		switch msg.String() {
		case "esc":
			m.editing = false
			m.filter.value = ""
			m.applyFilter()
			return m, m.previewCmd()
		case "enter":
			m.editing = false
			return m, nil
		case "up", "down", "pgup", "pgdown":
			d, _ := m.nav.delta(msg, len(m.visible), m.listHeight())
			return m, m.move(d)
		}
		if m.filter.update(msg) {
			m.applyFilter()
			return m, m.previewCmd()
		}
		return m, nil
	}
	if d, ok := m.nav.delta(msg, len(m.visible), m.listHeight()); ok {
		return m, m.move(d)
	}
	switch {
	case key.Matches(msg, m.keys.Enter):
		return m, m.activate()
	case key.Matches(msg, m.keys.Kill):
		a, ok := m.selected()
		switch {
		case !ok:
		case a.Record.PID <= 0:
			m.setMessage("this background job has no process to kill", true)
		default:
			m.confirm = &a
		}
		return m, nil
	case key.Matches(msg, m.keys.Filter):
		m.editing = true
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		return m, m.startRefresh()
	case key.Matches(msg, m.keys.Esc):
		if m.filter.value != "" {
			m.filter.value = ""
			m.applyFilter()
			return m, m.previewCmd()
		}
		m.quit = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Quit):
		m.quit = true
		return m, tea.Quit
	}
	for _, act := range m.opts.Extra {
		if msg.String() != act.Key || act.Run == nil {
			continue
		}
		a, ok := m.selected()
		if !ok || (act.Enabled != nil && !act.Enabled(a)) {
			return m, nil
		}
		run, ctx := act.Run, m.ctx
		return m, func() tea.Msg {
			res, err := run(ctx, a)
			return agentsDoneMsg{agent: a, result: res, err: err, verb: act.Label}
		}
	}
	return m, nil
}

func (m *AgentsModel) move(delta int) tea.Cmd {
	before := m.selKey
	m.list.move(delta, len(m.visible), m.listHeight())
	m.syncSelection()
	if m.selKey != before {
		return m.previewCmd()
	}
	return nil
}

func (m *AgentsModel) mouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil || msg.Button != tea.MouseLeft {
		return m, nil
	}
	row := msg.Y - 1
	if row < 0 || row >= m.listHeight() {
		return m, nil
	}
	idx := m.list.offset + row
	if idx >= len(m.visible) {
		return m, nil
	}
	double := m.clicks.click(m.now(), idx)
	cmd := m.move(idx - m.list.cursor)
	if double {
		return m, m.activate()
	}
	return m, cmd
}

// activate jumps to a pane agent or attaches a background one.
func (m *AgentsModel) activate() tea.Cmd {
	a, ok := m.selected()
	if !ok || m.opts.Actions == nil {
		return nil
	}
	actions, ctx := m.opts.Actions, m.ctx
	switch {
	case a.Location != nil:
		return func() tea.Msg {
			exe, err := actions.Jump(ctx, a)
			return agentsDoneMsg{agent: a, result: ActionResult{Exec: exe, Quit: true}, err: err, verb: "jump"}
		}
	case a.Source == agent.SourceBackground:
		return func() tea.Msg {
			exe, err := actions.Attach(ctx, a)
			return agentsDoneMsg{agent: a, result: ActionResult{Exec: exe, Quit: true}, err: err, verb: "attach"}
		}
	}
	m.setMessage("this agent runs outside the lyna-tmux server; open its terminal to reach it", true)
	return nil
}

func (m *AgentsModel) killCmd(a agent.Agent) tea.Cmd {
	if m.opts.Actions == nil {
		return nil
	}
	actions, ctx := m.opts.Actions, m.ctx
	return func() tea.Msg {
		err := actions.Kill(ctx, a)
		return agentsDoneMsg{
			agent:  a,
			result: ActionResult{Refresh: true, Message: "sent SIGTERM to pid " + strconv.Itoa(a.Record.PID)},
			err:    err,
			verb:   "kill",
		}
	}
}

func (m *AgentsModel) actionDone(msg agentsDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setMessage(msg.verb+" failed: "+msg.err.Error(), true)
		return m, nil
	}
	res := msg.result
	if msg.verb == "kill" {
		m.removed[msg.agent.Record.Key()] = true
		m.setSnapshot(agent.Snapshot{TakenAt: m.takenAt, Agents: m.all})
	}
	if res.Message != "" {
		m.setMessage(res.Message, false)
	}
	if msg.verb == "jump" || msg.verb == "attach" {
		a := msg.agent
		m.chosen = &a
	}
	var cmds []tea.Cmd
	if res.Refresh {
		cmds = append(cmds, m.startRefresh())
	}
	switch {
	case res.Exec != nil:
		cmds = append(cmds, tea.Exec(res.Exec, func(err error) tea.Msg { return execDoneMsg{err: err} }))
	case res.Quit:
		m.quit = true
		cmds = append(cmds, tea.Quit)
	}
	return m, tea.Batch(cmds...)
}

func (m *AgentsModel) setMessage(text string, isErr bool) {
	m.message, m.msgErr = sanitize.Line(text), isErr
}

func (m *AgentsModel) bodyHeight() int { return max(m.height-2, 1) }

func (m *AgentsModel) hasPreview() bool {
	return m.opts.Preview != nil && m.bodyHeight() >= 8
}

func (m *AgentsModel) listHeight() int {
	body := m.bodyHeight()
	if !m.hasPreview() {
		return body
	}
	return min(max(len(m.visible), 3), max(body*2/5, 3))
}

func (m *AgentsModel) previewHeight() int {
	if !m.hasPreview() {
		return 0
	}
	return m.bodyHeight() - m.listHeight() - 1
}

// View draws the picker.
func (m *AgentsModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *AgentsModel) render() string {
	lines := make([]string, 0, m.height)
	lines = append(lines, m.header())
	lines = append(lines, m.listLines()...)
	if m.hasPreview() {
		lines = append(lines, m.previewLines()...)
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], m.footer())
	return screen(lines, m.width, m.height)
}

func (m *AgentsModel) header() string {
	s := m.opts.Styles
	ic := s.Theme.Icons
	brand := []segment{seg(" "+ic.Brand+" ", s.Title), seg("agents ", s.Title)}
	counts := map[agent.Status]int{}
	for _, a := range m.all {
		counts[a.Status]++
	}
	// Status counts with their words, or icons and numbers only when the
	// words would push the refresh state off a narrow header.
	left, compact := brand, brand
	for _, st := range []agent.Status{agent.StatusWaiting, agent.StatusIdle, agent.StatusBusy, agent.StatusUnknown} {
		if counts[st] == 0 {
			continue
		}
		style, icon := s.Status(st)
		n := strconv.Itoa(counts[st])
		left = append(left[:len(left):len(left)], seg(" "+icon+" "+n+" "+string(st)+" ", style))
		compact = append(compact[:len(compact):len(compact)], seg(" "+icon+n, style))
	}
	var right []segment
	if m.filter.value != "" && !m.editing {
		right = append(right, seg("filter: "+sanitize.Line(m.filter.value)+" ", s.Accent2))
	}
	switch {
	case m.err != nil && m.loaded:
		right = append(right, seg("refresh failed ", s.Danger))
	case m.loading:
		right = append(right, seg("refreshing ", s.Muted))
	case m.loaded && !m.live && !m.takenAt.IsZero():
		right = append(right, seg("cached "+formatAge(m.now().Sub(m.takenAt))+" ", s.Muted))
	}
	if segsWidth(left)+segsWidth(right)+1 > m.width {
		left = compact
	}
	return s.bar(m.width, left, right)
}

type agentColumns struct {
	title, location, source int
}

func (m *AgentsModel) columns() agentColumns {
	const fixed = 1 + 2 + 8 + 5 // marker, icon, status, age
	c := agentColumns{}
	switch {
	case m.width >= 90:
		c.location, c.source = 24, 8
	case m.width >= 70:
		c.location = 20
	case m.width >= 50:
		c.location = 14
	}
	rest := m.width - fixed
	if c.location > 0 {
		rest -= c.location + 1
	}
	if c.source > 0 {
		rest -= c.source + 1
	}
	c.title = max(rest, 4)
	return c
}

func (m *AgentsModel) listLines() []string {
	s := m.opts.Styles
	h := m.listHeight()
	lines := make([]string, 0, h)
	switch {
	case len(m.visible) == 0 && !m.loaded && m.err != nil:
		lines = append(lines, "", " "+s.Danger.Render(truncate("could not list agents: "+sanitize.Line(m.err.Error()), m.width-2, s.Ellipsis)))
	case len(m.visible) == 0 && !m.loaded:
		lines = append(lines, "", s.Muted.Render(center("listing Claude agents", m.width, s.Ellipsis)))
	case len(m.visible) == 0 && m.filter.value != "":
		lines = append(lines, "", s.Muted.Render(center("no agent matches "+strconv.Quote(sanitize.Line(m.filter.value)), m.width, s.Ellipsis)))
	case len(m.visible) == 0:
		lines = append(lines, "",
			s.Text.Render(center("no Claude agents running", m.width, s.Ellipsis)),
			s.Muted.Render(center("start one with: lyna-tmux create", m.width, s.Ellipsis)))
	default:
		cols := m.columns()
		end := min(m.list.offset+h, len(m.visible))
		for vi := m.list.offset; vi < end; vi++ {
			lines = append(lines, m.row(m.all[m.visible[vi]], cols, vi == m.list.cursor))
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

func (m *AgentsModel) row(a agent.Agent, cols agentColumns, selected bool) string {
	s := m.opts.Styles
	style, icon := s.Status(a.Status)
	marker := " "
	var bg *lipgloss.Style
	if selected {
		marker = "▌"
		if s.Theme.Icons.Name == "ascii" {
			marker = ">"
		}
		bg = &s.Selected
	}
	title := sanitize.Line(a.Title())
	if title == "" {
		title = "(unnamed)"
	}
	titleText := fit(title, cols.title, s.Ellipsis)
	segs := []segment{seg(marker, s.Accent), seg(icon+" ", style), seg(fit(statusWord(a.Status), 8, s.Ellipsis), style)}
	if wf := sanitize.Line(a.Record.WaitingFor); wf != "" && a.Status == agent.StatusWaiting && ansi.StringWidth(title)+2 < cols.title {
		titleText = title + " "
		segs = append(segs, seg(titleText, s.Text), seg(fit(wf, cols.title-ansi.StringWidth(titleText), s.Ellipsis), s.Warning))
	} else {
		segs = append(segs, seg(titleText, s.Text))
	}
	if cols.location > 0 {
		segs = append(segs, seg(" "+fit(locationText(a, m.opts.Home), cols.location, s.Ellipsis), s.Accent2))
	}
	if cols.source > 0 {
		segs = append(segs, seg(" "+fit(sourceWord(a.Source), cols.source, s.Ellipsis), s.Muted))
	}
	age := "-"
	if a.Record.StartedAt > 0 {
		age = formatAge(m.now().Sub(a.Record.Started()))
	}
	segs = append(segs, seg(" "+fitRight(age, 4, s.Ellipsis), s.Muted))
	return s.line(m.width, bg, segs...)
}

func statusWord(st agent.Status) string {
	if st == agent.StatusUnknown {
		return "?"
	}
	return string(st)
}

func sourceWord(src agent.Source) string {
	switch src {
	case agent.SourceManaged:
		return "lyna"
	case agent.SourcePane:
		return "pane"
	case agent.SourceBackground:
		return "bg"
	}
	return "outside"
}

func locationText(a agent.Agent, home string) string {
	if l := a.Location; l != nil {
		return sanitize.Line(fmt.Sprintf("%s:%d %s", l.Session, l.WindowIndex, l.WindowName))
	}
	if a.Source == agent.SourceBackground {
		if a.Record.State != "" {
			return "job " + sanitize.Line(string(a.Record.State))
		}
		return "background"
	}
	return shortPath(sanitize.Line(a.Record.CWD), home)
}

func (m *AgentsModel) previewLines() []string {
	s := m.opts.Styles
	h := m.previewHeight()
	a, ok := m.selected()
	title := "preview"
	if ok && a.Location != nil {
		title += " " + sanitize.Line(a.Location.Session+":"+strconv.Itoa(a.Location.WindowIndex)+" "+a.Location.PaneID)
	}
	lines := []string{s.rule(m.width, title)}
	body := make([]string, 0, h)
	switch {
	case !ok:
	case a.Location == nil && a.Source == agent.SourceBackground:
		body = append(body, " "+s.Muted.Render(truncate("background agent without a pane: enter attaches to it", m.width-1, s.Ellipsis)))
	case a.Location == nil:
		body = append(body, " "+s.Muted.Render(truncate("not in a pane of this tmux server: "+shortPath(sanitize.Line(a.Record.CWD), m.opts.Home), m.width-1, s.Ellipsis)))
	default:
		p, have := m.preview[a.Record.Key()]
		switch {
		case !have:
			body = append(body, " "+s.Muted.Render("capturing pane"))
		case p.err != nil:
			body = append(body, " "+s.Danger.Render(truncate("capture failed: "+sanitize.Line(p.err.Error()), m.width-1, s.Ellipsis)))
		default:
			body = append(body, previewContent(p.content, m.width, h)...)
		}
	}
	for len(body) < h {
		body = append(body, "")
	}
	return append(lines, body[:h]...)
}

// previewContent sanitizes a capture and keeps its last height lines, each
// clipped to width and closed with an attribute reset so pane colors never
// bleed into the next line.
func previewContent(raw string, width, height int) []string {
	text := strings.ReplaceAll(sanitize.Terminal(raw), "\t", " ")
	all := strings.Split(strings.TrimRight(text, "\n "), "\n")
	for len(all) > 0 && strings.TrimSpace(ansi.Strip(all[len(all)-1])) == "" {
		all = all[:len(all)-1]
	}
	if len(all) > height {
		all = all[len(all)-height:]
	}
	out := make([]string, len(all))
	for i, l := range all {
		l = ansi.Truncate(l, width, "")
		if strings.Contains(l, "\x1b") {
			l += "\x1b[m"
		}
		out[i] = l
	}
	return out
}

func (m *AgentsModel) footer() string {
	s := m.opts.Styles
	switch {
	case m.confirm != nil:
		a := m.confirm
		prompt := fmt.Sprintf(" kill %s (pid %d)? ", sanitize.Line(a.Title()), a.Record.PID)
		return s.line(m.width, nil, seg(prompt, s.Danger.Bold(true)), seg("y", s.Key), seg(" confirm  ", s.Muted), seg("n", s.Key), seg(" cancel", s.Muted))
	case m.editing:
		return s.line(m.width, nil, seg(" / ", s.Accent), seg(m.filter.value, s.Text), seg("_", s.Accent), seg("  enter apply  esc clear", s.Muted))
	case m.message != "":
		style := s.Success
		if m.msgErr {
			style = s.Danger
		}
		return s.line(m.width, nil, seg(" "+m.message, style))
	}
	bindings := []key.Binding{m.keys.Enter, m.keys.Kill, m.keys.Filter, m.keys.Refresh}
	if a, ok := m.selected(); ok {
		for _, act := range m.opts.Extra {
			if act.Enabled == nil || act.Enabled(a) {
				bindings = append(bindings, key.NewBinding(key.WithKeys(act.Key), key.WithHelp(act.Key, act.Label)))
			}
		}
	}
	bindings = append(bindings, m.keys.Quit)
	return s.helpLine(m.width, bindings...)
}
