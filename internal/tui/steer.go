package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// steerForm is what the message and the stop forms share: a form drawn under
// the bar and above the keys, sized to the frame, and ended by esc and ctrl+c
// with nothing done.
type steerForm struct {
	styles        Styles
	title         string
	width, height int
	form          *huh.Form
}

func newSteerForm(s Styles, title string, width, height int) steerForm {
	w, h := sizeOr(width, height)
	return steerForm{styles: s, title: title, width: w, height: h}
}

func (f *steerForm) formWidth() int { return max(min(f.width-4, 76), 20) }

// resize takes a new frame size and hands the form what is left of it once
// the bar, the keys and the room for an error are taken.
func (f *steerForm) resize(msg tea.WindowSizeMsg) tea.Cmd {
	f.width, f.height = max(msg.Width, 1), max(msg.Height, 1)
	f.form = f.form.WithWidth(f.formWidth())
	msg.Height = max(f.height-formChrome, 3)
	return f.forward(msg)
}

// forward hands the form one message.
func (f *steerForm) forward(msg tea.Msg) tea.Cmd {
	model, cmd := f.form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		f.form = form
	}
	return cmd
}

// take hands the form one message, then measures it again.
//
// huh sizes every step from what it held when it was last measured, and a
// step can show text typed on the step before it. That text reaches the step
// only once the step is on screen, a message or two after it opened: a step
// measured before then draws its last answers under its own bottom edge.
func (f *steerForm) take(msg tea.Msg) tea.Cmd {
	cmd := f.forward(msg)
	if f.form.State != huh.StateNormal {
		return cmd
	}
	return tea.Batch(cmd, f.resize(tea.WindowSizeMsg{Width: f.width, Height: f.height}))
}

// steerCancel are the keys that end a form with nothing done.
var steerCancel = key.NewBinding(key.WithKeys("ctrl+c", "esc"))

// canceled reports a key that ends the form with nothing done.
func canceled(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyPressMsg)
	return ok && key.Matches(k, steerCancel)
}

// render draws the form, or the lines that say what it did once it is done.
func (f *steerForm) render(done []string) string {
	s := f.styles
	lines := []string{s.bar(f.width, []segment{seg(" "+s.Theme.Icons.Agents+" "+f.title+" ", s.Title)}, nil), ""}
	if done != nil {
		return screen(append(lines, done...), f.width, f.height)
	}
	for l := range strings.SplitSeq(f.form.View(), "\n") {
		lines = append(lines, "  "+l)
	}
	for len(lines) < f.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(f.height-1, 0))], s.helpLine(f.width,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))))
	return screen(lines, f.width, f.height)
}

// steerView shows a frame the way the spawn form shows its own.
func steerView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// MessageOptions configure the message form.
type MessageOptions struct {
	Styles Styles
	// Agent is the agent the message is typed at, and Workspace the workspace
	// it runs in, as the form names them.
	Agent, Workspace string
	// Width and Height size the first frame.
	Width, Height int
}

// MessageResult is the outcome of the message form.
type MessageResult struct {
	// Text is the message as the form showed it before it was sent, which is
	// the text the agent's pane is typed.
	Text string
	// Sent is false when the user canceled; the caller does nothing.
	Sent bool
}

// MessageText is the text a message reaches the agent as: one line of
// printable text, which is all a pane is ever typed (a control sequence would
// be read as keys, and a newline would submit half of the message). The form
// shows this rather than what was typed into it, so the text the user reads
// is the text the agent is sent.
func MessageText(input string) string { return sanitize.Line(input) }

// ErrMessageEmpty reports a message with nothing to send.
var ErrMessageEmpty = errors.New("type the message to send")

// messageLimit bounds a message: it is one line typed into a form, and a
// message past this is a file rather than a sentence.
const messageLimit = spawnPromptLimit

// The answers of the step that sends a message.
const (
	answerSend   = "send"
	answerEdit   = "edit"
	answerCancel = "cancel"
)

// messageValues are the form's bound values. They are exported so the send
// step's description sees every change, which is how huh decides to redraw it.
type messageValues struct {
	Text, Answer string
}

// MessageModel is the message form: one line for the agent, then the exact
// text it is sent, to send, edit or cancel.
type MessageModel struct {
	opts   MessageOptions
	f      steerForm
	values *messageValues
	done   bool
	result MessageResult
}

// NewMessage builds the form.
func NewMessage(opts MessageOptions) *MessageModel {
	m := &MessageModel{
		opts:   opts,
		f:      newSteerForm(opts.Styles, "message", opts.Width, opts.Height),
		values: &messageValues{Answer: answerSend},
	}
	m.f.form = m.buildForm()
	return m
}

// Result returns the outcome once the form finished.
func (m *MessageModel) Result() (MessageResult, bool) { return m.result, m.done }

func (m *MessageModel) buildForm() *huh.Form {
	v := m.values
	write := huh.NewGroup(
		huh.NewInput().Title("Message to " + sanitize.Line(m.opts.Agent)).
			Description("one line, typed into its pane and submitted as if you typed it").
			CharLimit(messageLimit).Value(&v.Text).
			Validate(checkMessage),
	)
	send := huh.NewGroup(
		huh.NewSelect[string]().Title("Send it?").
			DescriptionFunc(m.summary, v).
			Options(
				huh.NewOption("send     type it into the pane and submit it", answerSend),
				huh.NewOption("edit     change the message first", answerEdit),
				huh.NewOption("cancel   send nothing", answerCancel),
			).Value(&v.Answer),
	)
	return huh.NewForm(write, send).
		WithTheme(m.opts.Styles.huhTheme()).
		WithShowHelp(false).
		WithWidth(m.f.formWidth())
}

// checkMessage refuses a message that would type nothing at all.
func checkMessage(input string) error {
	if MessageText(input) == "" {
		return ErrMessageEmpty
	}
	return nil
}

// summary is the send step's description: the exact text the agent is sent.
func (m *MessageModel) summary() string {
	return sanitize.Line(m.opts.Agent) + " in workspace " + sanitize.Line(m.opts.Workspace) +
		" is sent, exactly:\n" + MessageText(m.values.Text)
}

// Init starts the form.
func (m *MessageModel) Init() tea.Cmd { return m.f.form.Init() }

// Update handles messages.
func (m *MessageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return m, m.after(m.f.resize(size))
	}
	if canceled(msg) {
		m.finish(false)
		return m, tea.Quit
	}
	return m, m.after(m.f.take(msg))
}

// after reads the form once it has taken a message: a send or a cancel ends
// it, and an edit takes the user back to the message with what they typed
// kept.
func (m *MessageModel) after(cmd tea.Cmd) tea.Cmd {
	switch m.f.form.State {
	case huh.StateCompleted:
		if m.values.Answer == answerEdit {
			return m.edit()
		}
		m.finish(m.values.Answer == answerSend)
		return tea.Quit
	case huh.StateAborted:
		m.finish(false)
		return tea.Quit
	}
	return cmd
}

// edit opens the form again on the message. A completed huh form takes no
// more keys, so the form is built again over the same values.
func (m *MessageModel) edit() tea.Cmd {
	m.values.Answer = answerSend
	m.f.form = m.buildForm()
	start := m.f.form.Init()
	return tea.Batch(start, m.f.resize(tea.WindowSizeMsg{Width: m.f.width, Height: m.f.height}))
}

func (m *MessageModel) finish(sent bool) {
	m.done = true
	m.result = MessageResult{Sent: sent}
	if sent {
		m.result.Text = MessageText(m.values.Text)
	}
}

// View draws the form.
func (m *MessageModel) View() tea.View { return steerView(m.render()) }

func (m *MessageModel) render() string {
	if !m.done {
		return m.f.render(nil)
	}
	s := m.opts.Styles
	if !m.result.Sent {
		return m.f.render([]string{" " + s.Muted.Render("nothing was sent")})
	}
	return m.f.render([]string{
		" " + s.Success.Render("sending to "+sanitize.Line(m.opts.Agent)),
		"   " + s.Text.Render(truncate(m.result.Text, max(m.f.width-4, 0), s.Ellipsis)),
	})
}

// StopWay is how a teammate is stopped.
type StopWay string

const (
	// StopAskLead asks the lead to shut the teammate down, which keeps the
	// team in order: the lead gave the teammate its work and takes it back.
	StopAskLead StopWay = "lead"
	// StopNow closes the teammate's pane: the agent ends at once, with
	// whatever it was doing.
	StopNow StopWay = "now"
)

// StopOptions configure the stop form.
type StopOptions struct {
	Styles Styles
	// Teammate is the teammate to stop, and Workspace the workspace it runs
	// in, as the form names them.
	Teammate, Workspace string
	// Lead says the workspace has a lead to ask. Without one the form offers
	// only to stop the teammate now.
	Lead bool
	// Width and Height size the first frame.
	Width, Height int
}

// StopResult is the outcome of the stop form.
type StopResult struct {
	Way StopWay
	// Message is the text the lead is sent, which the form shows in full
	// before anything is sent. It is empty for a teammate stopped now.
	Message string
	// Typed is the name the user typed to stop the teammate now, empty when
	// the lead is asked.
	Typed string
	// Sent is false when the user canceled; the caller does nothing.
	Sent bool
}

// StopMessage is the text the lead is sent to shut a teammate down, in the
// words a user would have typed themselves.
func StopMessage(teammate string) string {
	return "Shut down the teammate " + sanitize.Line(teammate) + ": its work is done."
}

// stopValues are the form's bound values.
type stopValues struct {
	Way, Typed string
	Go         bool
}

// StopModel is the stop form: how the teammate is stopped, then the exact
// text the lead is sent, or the teammate's name typed out to close its pane.
type StopModel struct {
	opts   StopOptions
	f      steerForm
	values *stopValues
	done   bool
	result StopResult
}

// NewStop builds the form. Asking the lead is the first answer when there is
// a lead to ask.
func NewStop(opts StopOptions) *StopModel {
	way := StopNow
	if opts.Lead {
		way = StopAskLead
	}
	m := &StopModel{
		opts:   opts,
		f:      newSteerForm(opts.Styles, "stop", opts.Width, opts.Height),
		values: &stopValues{Way: string(way), Go: true},
	}
	m.f.form = m.buildForm()
	return m
}

// Result returns the outcome once the form finished.
func (m *StopModel) Result() (StopResult, bool) { return m.result, m.done }

// teammate is the teammate's name as the form shows it, which is also the
// name a stop now asks the user to type.
func (m *StopModel) teammate() string { return sanitize.Line(m.opts.Teammate) }

func (m *StopModel) buildForm() *huh.Form {
	v := m.values
	name := m.teammate()
	way := huh.NewGroup(
		huh.NewSelect[string]().Title("How to stop "+name).Options(
			huh.NewOption("ask the lead   the lead shuts it down and keeps its team in order", string(StopAskLead)),
			huh.NewOption("stop it now    close its pane, with whatever it was doing", string(StopNow)),
		).Value(&v.Way),
	).WithHide(!m.opts.Lead)

	ask := huh.NewGroup(
		huh.NewConfirm().Title("Ask the lead?").
			Description("the lead of workspace " + sanitize.Line(m.opts.Workspace) + " is sent, exactly:\n" + StopMessage(m.opts.Teammate)).
			Affirmative("Send").Negative("Cancel").Value(&v.Go),
	).WithHideFunc(func() bool { return v.Way != string(StopAskLead) })

	now := huh.NewGroup(
		huh.NewInput().Title("Type " + name + " to stop it now").
			Description("its pane is closed, and what it was doing is lost").
			CharLimit(128).Value(&v.Typed).
			Validate(m.checkTyped),
	).WithHideFunc(func() bool { return v.Way != string(StopNow) })

	return huh.NewForm(way, ask, now).
		WithTheme(m.opts.Styles.huhTheme()).
		WithShowHelp(false).
		WithWidth(m.f.formWidth())
}

// checkTyped is the typed confirmation of a stop now: the name exactly as the
// form shows it, so a pane is closed only by a user who read whose it is.
func (m *StopModel) checkTyped(typed string) error {
	if name := m.teammate(); name == "" || typed != name {
		return fmt.Errorf("type %s exactly to stop it", name)
	}
	return nil
}

// Init starts the form.
func (m *StopModel) Init() tea.Cmd { return m.f.form.Init() }

// Update handles messages.
func (m *StopModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return m, m.after(m.f.resize(size))
	}
	if canceled(msg) {
		m.finish(false)
		return m, tea.Quit
	}
	return m, m.after(m.f.take(msg))
}

// after reads the form once it has taken a message. A stop now completes only
// on the name typed out; asking the lead completes on the answer to it.
func (m *StopModel) after(cmd tea.Cmd) tea.Cmd {
	switch m.f.form.State {
	case huh.StateCompleted:
		m.finish(m.values.Way == string(StopNow) || m.values.Go)
		return tea.Quit
	case huh.StateAborted:
		m.finish(false)
		return tea.Quit
	}
	return cmd
}

func (m *StopModel) finish(sent bool) {
	m.done = true
	m.result = StopResult{Way: StopWay(m.values.Way), Sent: sent}
	if !sent {
		return
	}
	switch m.result.Way {
	case StopAskLead:
		m.result.Message = StopMessage(m.opts.Teammate)
	case StopNow:
		m.result.Typed = m.values.Typed
	}
}

// View draws the form.
func (m *StopModel) View() tea.View { return steerView(m.render()) }

func (m *StopModel) render() string {
	if !m.done {
		return m.f.render(nil)
	}
	s := m.opts.Styles
	switch {
	case !m.result.Sent:
		return m.f.render([]string{" " + s.Muted.Render("nothing was stopped")})
	case m.result.Way == StopAskLead:
		return m.f.render([]string{
			" " + s.Success.Render("asking the lead"),
			"   " + s.Text.Render(truncate(m.result.Message, max(m.f.width-4, 0), s.Ellipsis)),
		})
	}
	return m.f.render([]string{" " + s.Warning.Render("stopping "+m.teammate())})
}
