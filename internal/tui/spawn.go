package tui

import (
	"errors"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agentdef"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// SpawnTarget is who runs the agent a spawn form asks for.
type SpawnTarget string

const (
	// SpawnLead asks the lead of the workspace to start the agent, which is
	// what keeps it in the conversation the user is already having.
	SpawnLead SpawnTarget = "lead"
	// SpawnOwn starts an agent of our own in a window of the workspace, with a
	// conversation and a worktree of its own.
	SpawnOwn SpawnTarget = "own"
)

// SpawnOptions configure the spawn form.
type SpawnOptions struct {
	Styles Styles
	// Agents are the definitions the workspace offers beyond the built-in one.
	Agents []agentdef.Def
	// Lead says the workspace has a lead to ask. Without one the form starts
	// an agent of our own and does not offer the choice.
	Lead bool
	// Worktree is the answer the worktree question starts on, which is what
	// claude.agent_worktree decides.
	Worktree bool
	// Model and Effort are what the workspace launches agents with, offered as
	// the starting answers.
	Model, Effort string
	// Width and Height size the first frame.
	Width, Height int
}

// SpawnRequest is the agent a form asked for.
type SpawnRequest struct {
	Target SpawnTarget
	// Agent is the agent definition, Model and Effort what it runs with, all
	// three empty when the form was left on what the workspace already uses:
	// the built-in agent is the one a session runs when it names none.
	Agent, Model, Effort string
	// Worktree asks for a git worktree of the agent's own, and Name names it
	// and the window an agent of our own opens in.
	Worktree bool
	Name     string
	Prompt   string
}

// AgentName is the agent the request is for, as it is shown: the definition
// it names, or the built-in agent.
func (r SpawnRequest) AgentName() string {
	if r.Agent == "" {
		return agentdef.Default
	}
	return r.Agent
}

// SpawnResult is the outcome of the form.
type SpawnResult struct {
	Request SpawnRequest
	// Message is the text an ask-the-lead request types into the lead's pane,
	// which the form shows in full before anything is sent.
	Message string
	// Sent is false when the user canceled; the caller does nothing.
	Sent bool
}

// spawnValues are the form's bound values. They are exported so the review
// step's description sees every change, which is how huh decides to redraw it.
type spawnValues struct {
	Target                string
	Agent, Model, Effort  string
	Worktree              bool
	Name, Prompt, Confirm string
	Go                    bool
}

// SpawnModel is the spawn form.
type SpawnModel struct {
	opts   SpawnOptions
	width  int
	height int
	values *spawnValues
	form   *huh.Form
	done   bool
	result SpawnResult
}

// NewSpawn builds the form.
func NewSpawn(opts SpawnOptions) *SpawnModel {
	w, h := sizeOr(opts.Width, opts.Height)
	target := SpawnOwn
	if opts.Lead {
		target = SpawnLead
	}
	m := &SpawnModel{
		opts:   opts,
		width:  w,
		height: h,
		values: &spawnValues{
			Target:   string(target),
			Agent:    agentdef.Default,
			Model:    opts.Model,
			Effort:   opts.Effort,
			Worktree: opts.Worktree,
			Name:     "",
			Go:       true,
		},
	}
	m.form = m.buildForm()
	return m
}

// Result returns the outcome once the form finished.
func (m *SpawnModel) Result() (SpawnResult, bool) { return m.result, m.done }

// request is the form's values as a request.
func (m *SpawnModel) request() SpawnRequest {
	v := m.values
	agent := strings.TrimSpace(v.Agent)
	if agent == agentdef.Default {
		agent = ""
	}
	return SpawnRequest{
		Target:   SpawnTarget(v.Target),
		Agent:    agent,
		Model:    strings.TrimSpace(v.Model),
		Effort:   v.Effort,
		Worktree: v.Worktree,
		Name:     strings.TrimSpace(v.Name),
		Prompt:   strings.TrimSpace(v.Prompt),
	}
}

// ErrSpawnPrompt reports a request with nothing to ask the agent.
var ErrSpawnPrompt = errors.New("the agent needs something to do")

// SpawnMessage is the text an ask-the-lead request types into the lead's pane.
// It is one line, in the words a user would have typed themselves, and it is
// shown in full before it is sent: nothing reaches an agent that the user has
// not read first.
func SpawnMessage(req SpawnRequest) string {
	var b strings.Builder
	b.WriteString("Start a ")
	b.WriteString(sanitize.Line(req.AgentName()))
	b.WriteString(" agent")
	if req.Worktree {
		b.WriteString(" in a git worktree of its own")
		if req.Name != "" {
			b.WriteString(" named ")
			b.WriteString(sanitize.Line(req.Name))
		}
	}
	if req.Model != "" {
		b.WriteString(", on ")
		b.WriteString(sanitize.Line(req.Model))
	}
	if req.Effort != "" {
		b.WriteString(", with ")
		b.WriteString(sanitize.Line(req.Effort))
		b.WriteString(" effort")
	}
	b.WriteString(", and tell it: ")
	b.WriteString(sanitize.Line(req.Prompt))
	return b.String()
}

// spawnAsksTheLead reports a request the lead carries out.
func (m *SpawnModel) spawnAsksTheLead() bool { return m.values.Target == string(SpawnLead) }

func (m *SpawnModel) buildForm() *huh.Form {
	v := m.values
	const steps = 3
	step := func(n int, title string) string { return strconv.Itoa(n) + "/" + strconv.Itoa(steps) + "  " + title }

	where := huh.NewGroup(
		huh.NewSelect[string]().Title("Who runs it").Options(
			huh.NewOption("the lead   the agent joins the conversation you are having", string(SpawnLead)),
			huh.NewOption("its own    a window of its own, with its own conversation", string(SpawnOwn)),
		).Value(&v.Target),
	).Title(step(1, "Target")).WithHide(!m.opts.Lead)

	agent := huh.NewGroup(
		huh.NewSelect[string]().Title("Agent").Options(m.agentOptions()...).Value(&v.Agent),
		huh.NewInput().Title("Model").
			Description("model alias or id; empty runs it on what the workspace runs").
			Placeholder("opus").CharLimit(128).Value(&v.Model),
		huh.NewSelect[string]().Title("Effort").
			Options(effortOptions("workspace   what the workspace already uses")...).
			Value(&v.Effort),
	).Title(step(2, "Agent"))

	work := huh.NewGroup(
		huh.NewConfirm().Title("Worktree").
			Description("give the agent a git worktree and a branch of its own").
			Affirmative("Yes").Negative("No").Value(&v.Worktree),
		huh.NewInput().Title("Name").
			Description("names the window, and the worktree when it has one").
			Placeholder("fix-login").CharLimit(64).Value(&v.Name).
			Validate(m.checkName),
		huh.NewText().Title("Prompt").
			Description("what the agent is being asked to do").
			CharLimit(spawnPromptLimit).Value(&v.Prompt).
			Validate(m.checkPrompt),
		huh.NewConfirm().Title("Start it?").
			DescriptionFunc(m.summary, v).
			Affirmative("Start").Negative("Cancel").Value(&v.Go),
	).Title(step(3, "Work"))

	return huh.NewForm(where, agent, work).
		WithTheme(m.opts.Styles.huhTheme()).
		WithShowHelp(false).
		WithWidth(m.formWidth())
}

// spawnPromptLimit bounds the prompt. It is typed into a form, and a prompt
// past this is a file rather than a sentence.
const spawnPromptLimit = 4000

// agentOptions are the agents the form offers: the built-in one, then the
// definitions the workspace carries, each with what it is for.
func (m *SpawnModel) agentOptions() []huh.Option[string] {
	opts := []huh.Option[string]{
		huh.NewOption(optionLabel(agentdef.Default, "the agent with no definition of its own"), agentdef.Default),
	}
	for _, d := range m.opts.Agents {
		if d.Name == agentdef.Default {
			continue
		}
		opts = append(opts, huh.NewOption(optionLabel(sanitize.Line(d.Name), sanitize.Line(d.Description)), d.Name))
	}
	return opts
}

// checkName refuses a name no window or worktree could take. A request the
// lead carries out names nothing of ours, so an empty name is one the lead
// picks itself; an agent of our own opens in a window, and a window is named.
func (m *SpawnModel) checkName(name string) error {
	name = strings.TrimSpace(name)
	switch {
	case name != "":
		return layout.ValidateWorktree(name)
	case m.spawnAsksTheLead():
		return nil
	case m.values.Worktree:
		return errors.New("a worktree needs a name")
	}
	return errors.New("a window of its own needs a name")
}

// checkPrompt refuses a request with nothing to ask, and one the agent would
// not read as a prompt. The second rule is the launch's, and applies to the
// agent we start ourselves: what the lead is asked is text in a conversation
// that is already running.
func (m *SpawnModel) checkPrompt(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return ErrSpawnPrompt
	}
	if m.spawnAsksTheLead() {
		return nil
	}
	return claudecfg.ValidatePrompt(p)
}

// summary is the review step's description: the exact text an ask-the-lead
// request sends, or what an agent of our own is opened as.
func (m *SpawnModel) summary() string {
	req := m.request()
	if req.Target == SpawnLead {
		return "the lead is sent, exactly:\n" + SpawnMessage(req)
	}
	lines := []string{"agent:    " + sanitize.Line(req.AgentName())}
	if req.Model != "" {
		lines = append(lines, "model:    "+req.Model)
	}
	if req.Effort != "" {
		lines = append(lines, "effort:   "+req.Effort)
	}
	if req.Name != "" {
		lines = append(lines, "window:   "+req.Name)
	}
	where := "the project directory"
	if req.Worktree {
		where = "a worktree of its own"
	}
	return strings.Join(append(lines, "runs in:  "+where), "\n")
}

func (m *SpawnModel) formWidth() int { return max(min(m.width-4, 76), 20) }

// Init starts the form.
func (m *SpawnModel) Init() tea.Cmd { return m.form.Init() }

// Update handles messages.
func (m *SpawnModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.form = m.form.WithWidth(m.formWidth())
		msg.Height = max(m.height-formChrome, 3)
		return m.forward(msg)
	case tea.KeyPressMsg:
		if key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+c", "esc"))) {
			m.finish(false)
			return m, tea.Quit
		}
	}
	return m.forward(msg)
}

func (m *SpawnModel) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		m.finish(m.values.Go)
		return m, tea.Quit
	case huh.StateAborted:
		m.finish(false)
		return m, tea.Quit
	}
	return m, cmd
}

func (m *SpawnModel) finish(sent bool) {
	m.done = true
	req := m.request()
	m.result = SpawnResult{Request: req, Sent: sent}
	if sent && req.Target == SpawnLead {
		m.result.Message = SpawnMessage(req)
	}
}

// View draws the form.
func (m *SpawnModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *SpawnModel) render() string {
	s := m.opts.Styles
	lines := []string{s.bar(m.width, []segment{seg(" "+s.Theme.Icons.Agents+" spawn ", s.Title)}, nil)}
	if m.done {
		lines = append(lines, "")
		if !m.result.Sent {
			return screen(append(lines, " "+s.Muted.Render("nothing was started")), m.width, m.height)
		}
		if m.result.Request.Target == SpawnLead {
			lines = append(lines, " "+s.Success.Render("asking the lead"),
				"   "+s.Text.Render(truncate(m.result.Message, max(m.width-4, 0), s.Ellipsis)))
			return screen(lines, m.width, m.height)
		}
		lines = append(lines, " "+s.Success.Render("starting "+sanitize.Line(m.result.Request.AgentName())))
		return screen(lines, m.width, m.height)
	}
	lines = append(lines, "")
	for l := range strings.SplitSeq(m.form.View(), "\n") {
		lines = append(lines, "  "+l)
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], s.helpLine(m.width,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))))
	return screen(lines, m.width, m.height)
}
