package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// SetupOptions configure the setup wizard.
type SetupOptions struct {
	// Styles draw the wizard itself (the current configuration's look).
	Styles Styles
	// Config is the starting configuration; the wizard edits a copy.
	Config config.Config
	// Getenv resolves "auto" icons for the preview; nil previews unicode.
	Getenv func(string) string
	// Width and Height size the first frame.
	Width, Height int
}

// ConfigChange is one value the wizard changed.
type ConfigChange struct {
	Key  string
	From string
	To   string
}

// SetupResult is the outcome of the wizard.
type SetupResult struct {
	// Config is the edited configuration; it passed config.Validate.
	Config config.Config
	// Changes lists the edited values in wizard order.
	Changes []ConfigChange
	// Saved is false when the user canceled; the caller writes nothing.
	Saved bool
}

// setupValues are the form's bound values.
type setupValues struct {
	// Fields are exported so the review step's dynamic description sees
	// every change (huh hashes exported fields of its bindings).
	Theme, Icons, Color   string
	StatusStyle           string
	Layout                string
	AltKeys, Mouse        bool
	Model, Effort, Status string
	Profile, Isolation    string
	Save                  bool
}

// SetupModel is the setup wizard.
type SetupModel struct {
	opts   SetupOptions
	width  int
	height int
	values *setupValues
	form   *huh.Form
	done   bool
	result SetupResult
}

// NewSetup builds the wizard over a configuration.
func NewSetup(opts SetupOptions) *SetupModel {
	w, h := sizeOr(opts.Width, opts.Height)
	c := opts.Config
	m := &SetupModel{
		opts:   opts,
		width:  w,
		height: h,
		values: &setupValues{
			Theme: c.UI.Theme, Icons: c.UI.Icons, Color: c.UI.Color, StatusStyle: c.UI.StatusStyle,
			Layout: c.Workspace.Layout, AltKeys: c.UI.AltKeys, Mouse: c.UI.Mouse,
			Model: c.Claude.Model, Effort: c.Claude.Effort, Status: c.Claude.Statusline,
			Profile: c.Sandbox.Profile, Isolation: c.Sandbox.Isolation,
			Save: true,
		},
	}
	m.form = m.buildForm()
	return m
}

// Result returns the outcome once the wizard finished.
func (m *SetupModel) Result() (SetupResult, bool) {
	return m.result, m.done
}

func (m *SetupModel) candidate() config.Config {
	c := m.opts.Config
	v := m.values
	c.UI.Theme, c.UI.Icons, c.UI.Color = v.Theme, v.Icons, v.Color
	c.UI.StatusStyle = v.StatusStyle
	c.Workspace.Layout, c.UI.AltKeys, c.UI.Mouse = v.Layout, v.AltKeys, v.Mouse
	c.Claude.Model, c.Claude.Effort, c.Claude.Statusline = strings.TrimSpace(v.Model), v.Effort, v.Status
	c.Sandbox.Profile, c.Sandbox.Isolation = v.Profile, v.Isolation
	return c
}

// problemsFor returns the validation problems of the candidate for one key
// prefix, so a field reports only its own errors.
func (m *SetupModel) problemsFor(c config.Config, prefix string) error {
	err := c.Validate()
	var problems config.Problems
	if !errors.As(err, &problems) {
		return err
	}
	var msgs []string
	for _, p := range problems {
		if strings.HasPrefix(p.Key, prefix) {
			msgs = append(msgs, p.Message)
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "; "))
}

func (m *SetupModel) buildForm() *huh.Form {
	v := m.values
	layouts := config.Choices("workspace.layout")
	custom := make([]string, 0, len(m.opts.Config.Layouts))
	for name := range m.opts.Config.Layouts {
		custom = append(custom, name)
	}
	slices.Sort(custom)
	layoutOpts := make([]huh.Option[string], 0, len(layouts)+len(custom))
	for _, name := range layouts {
		layoutOpts = append(layoutOpts, huh.NewOption(optionLabel(name, layoutHelp[name]), name))
	}
	for _, name := range custom {
		layoutOpts = append(layoutOpts, huh.NewOption(optionLabel(name, "custom layout from config.toml"), name))
	}
	if !slices.Contains(layouts, v.Layout) && !slices.Contains(custom, v.Layout) {
		v.Layout = "auto"
	}

	const steps = 5
	step := func(n int, title string) string { return strconv.Itoa(n) + "/" + strconv.Itoa(steps) + "  " + title }

	return huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("Theme").Options(
				huh.NewOption("monokai the colors a workspace opens with", "monokai"),
				huh.NewOption("lyna    dark with a green accent", "lyna"),
				huh.NewOption("light   for light terminal backgrounds", "light"),
				huh.NewOption("ansi    your terminal's own 16 colors", "ansi"),
			).Value(&v.Theme),
			huh.NewSelect[string]().Title("Icons").Options(
				huh.NewOption("auto      unicode on UTF-8 locales, ascii otherwise", "auto"),
				huh.NewOption("unicode   one-cell symbols that draw in common fonts", "unicode"),
				huh.NewOption("nerd      glyphs from a patched icon font", "nerd"),
				huh.NewOption("ascii     plain characters only", "ascii"),
			).Value(&v.Icons),
			huh.NewSelect[string]().Title("Status bar").Options(
				huh.NewOption("auto        pointed separators with a patched icon font", theme.StatusAuto),
				huh.NewOption("powerline   pointed separators, whatever the icons", theme.StatusPowerline),
				huh.NewOption("plain       no separators", theme.StatusPlain),
			).Value(&v.StatusStyle),
			huh.NewSelect[string]().Title("Color depth").Options(
				huh.NewOption("auto        detected from COLORTERM and TERM", "auto"),
				huh.NewOption("truecolor   24-bit color", "truecolor"),
				huh.NewOption("256         the xterm 256-color palette", "256"),
				huh.NewOption("16          the basic terminal palette", "16"),
			).Value(&v.Color),
		).Title(step(1, "Look")),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Default layout").Options(layoutOpts...).Value(&v.Layout),
			huh.NewConfirm().Title("Alt keys").
				Description("Alt+key bindings that need no prefix (terminals must send Option as Meta)").
				Affirmative("On").Negative("Off").Value(&v.AltKeys),
			huh.NewConfirm().Title("Mouse").
				Description("click panes, tabs and status buttons; scroll with the wheel").
				Affirmative("On").Negative("Off").Value(&v.Mouse),
		).Title(step(2, "Workspace")),
		huh.NewGroup(
			huh.NewInput().Title("Model").
				Description("model alias or id; leave empty to let Claude decide").
				Placeholder("opus").CharLimit(128).Value(&v.Model).
				Validate(func(model string) error {
					c := m.candidate()
					c.Claude.Model = strings.TrimSpace(model)
					return m.problemsFor(c, "claude.model")
				}),
			huh.NewSelect[string]().Title("Effort").
				Options(effortOptions("default     let Claude decide")...).
				Value(&v.Effort),
			huh.NewSelect[string]().Title("Status line").Options(
				huh.NewOption("auto   lyna status line unless you configured your own", "auto"),
				huh.NewOption("lyna   always the lyna status line", "lyna"),
				huh.NewOption("off    no status line from lyna-tmux", "off"),
			).Value(&v.Status),
		).Title(step(3, "Claude")),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Sandbox profile").Options(sandboxOptions()...).Value(&v.Profile),
			huh.NewSelect[string]().Title("Isolation").Options(
				huh.NewOption("bash        only the Bash tool is sandboxed", "bash"),
				huh.NewOption("process     the whole Claude process", "process"),
				huh.NewOption("container   everything in a dev container", "container"),
			).Value(&v.Isolation),
		).Title(step(4, "Sandbox")),
		huh.NewGroup(
			huh.NewConfirm().Title("Write config.toml?").
				DescriptionFunc(func() string { return m.summary() }, v).
				Affirmative("Save").Negative("Cancel").Value(&v.Save).
				Validate(func(save bool) error {
					if !save {
						return nil
					}
					return m.candidate().Validate()
				}),
		).Title(step(5, "Review")),
	).
		WithTheme(m.opts.Styles.huhTheme()).
		WithShowHelp(false).
		WithWidth(m.formWidth())
}

// effortOptions are the effort levels a form offers, after the choice that
// leaves the level to what is already in place, which unset describes.
func effortOptions(unset string) []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption(unset, ""),
		huh.NewOption("low         quickest answers", "low"),
		huh.NewOption("medium      balanced", "medium"),
		huh.NewOption("high        more reasoning on hard steps", "high"),
		huh.NewOption("xhigh       extended reasoning", "xhigh"),
		huh.NewOption("max         the most reasoning per step", "max"),
		huh.NewOption("ultracode   long autonomous coding runs", "ultracode"),
	}
}

// optionLabel aligns a choice's help after its name; a long name keeps one
// space instead of being cut.
func optionLabel(name, help string) string {
	const column = 7
	if ansi.StringWidth(name) < column {
		return fit(name, column, "") + help
	}
	return name + " " + help
}

// layoutHelp describes the built-in layouts in the layout choice.
var layoutHelp = map[string]string{
	"solo":   "Claude only",
	"duo":    "Claude and a shell",
	"trio":   "Claude, a shell and live changes",
	"quad":   "four Claude agents in their own worktrees",
	"review": "Claude beside a review pane",
	"team":   "the agents rail, the lead and room for its teammates",
	"git":    "Claude and the git workstation",
	"auto":   "chosen from the terminal size",
}

func (m *SetupModel) formWidth() int { return max(min(m.width-4, 76), 20) }

// summary lists the pending changes for the review step.
func (m *SetupModel) summary() string {
	changes := diffConfig(m.opts.Config, m.candidate())
	if len(changes) == 0 {
		return "no changes"
	}
	lines := make([]string, len(changes))
	for i, c := range changes {
		lines[i] = c.Key + ": " + display(c.From) + " -> " + display(c.To)
	}
	return strings.Join(lines, "\n")
}

func display(v string) string {
	if v == "" {
		return "(default)"
	}
	return v
}

// wizardKeys are the configuration values the wizard edits, in wizard order.
var wizardKeys = []struct {
	key string
	get func(config.Config) string
}{
	{"ui.theme", func(c config.Config) string { return c.UI.Theme }},
	{"ui.icons", func(c config.Config) string { return c.UI.Icons }},
	{"ui.status_style", func(c config.Config) string { return c.UI.StatusStyle }},
	{"ui.color", func(c config.Config) string { return c.UI.Color }},
	{"workspace.layout", func(c config.Config) string { return c.Workspace.Layout }},
	{"ui.alt_keys", func(c config.Config) string { return strconv.FormatBool(c.UI.AltKeys) }},
	{"ui.mouse", func(c config.Config) string { return strconv.FormatBool(c.UI.Mouse) }},
	{"claude.model", func(c config.Config) string { return c.Claude.Model }},
	{"claude.effort", func(c config.Config) string { return c.Claude.Effort }},
	{"claude.statusline", func(c config.Config) string { return c.Claude.Statusline }},
	{"sandbox.profile", func(c config.Config) string { return c.Sandbox.Profile }},
	{"sandbox.isolation", func(c config.Config) string { return c.Sandbox.Isolation }},
}

func diffConfig(before, after config.Config) []ConfigChange {
	var out []ConfigChange
	for _, k := range wizardKeys {
		if from, to := k.get(before), k.get(after); from != to {
			out = append(out, ConfigChange{Key: k.key, From: from, To: to})
		}
	}
	return out
}

// Init starts the form.
func (m *SetupModel) Init() tea.Cmd { return m.form.Init() }

// Update handles messages.
func (m *SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.form = m.form.WithWidth(m.formWidth())
		// The preview line adds one row to the dashboard's form chrome.
		msg.Height = max(m.height-formChrome-1, 3)
		return m.forward(msg)
	case tea.KeyPressMsg:
		if key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+c"))) {
			m.finish(false)
			return m, tea.Quit
		}
	}
	return m.forward(msg)
}

func (m *SetupModel) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		m.finish(m.values.Save)
		return m, tea.Quit
	case huh.StateAborted:
		m.finish(false)
		return m, tea.Quit
	}
	return m, cmd
}

func (m *SetupModel) finish(saved bool) {
	m.done = true
	cfg := m.candidate()
	if !saved {
		cfg = m.opts.Config
	}
	m.result = SetupResult{Config: cfg, Saved: saved}
	if saved {
		m.result.Changes = diffConfig(m.opts.Config, cfg)
	}
}

// View draws the wizard.
func (m *SetupModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *SetupModel) render() string {
	s := m.opts.Styles
	lines := []string{s.bar(m.width, []segment{seg(" "+s.Theme.Icons.Brand+" setup ", s.Title)}, nil)}
	if m.done {
		lines = append(lines, "")
		switch {
		case !m.result.Saved:
			lines = append(lines, " "+s.Muted.Render("setup canceled; nothing was written"))
		case len(m.result.Changes) == 0:
			lines = append(lines, " "+s.Success.Render("configuration unchanged"))
		default:
			lines = append(lines, " "+s.Success.Render("saving "+plural(len(m.result.Changes), "change", "changes")))
			for _, c := range m.result.Changes {
				lines = append(lines, "   "+s.Text.Render(truncate(c.Key+": "+display(c.From)+" -> "+display(c.To), m.width-4, s.Ellipsis)))
			}
		}
		return screen(lines, m.width, m.height)
	}
	lines = append(lines, m.preview(), "")
	for l := range strings.SplitSeq(m.form.View(), "\n") {
		lines = append(lines, "  "+l)
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], s.helpLine(m.width,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
		key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))))
	return screen(lines, m.width, m.height)
}

// preview shows the selected theme's colors and icons.
func (m *SetupModel) preview() string {
	s := m.opts.Styles
	line := s.Muted.Render(" theme ")
	if p, err := theme.Get(m.values.Theme); err == nil {
		line += s.Swatch(p)
	}
	getenv := m.opts.Getenv
	if getenv == nil {
		getenv = func(string) string { return "en_US.UTF-8" }
	}
	if ic, err := theme.GetIcons(theme.ResolveIcons(m.values.Icons, getenv)); err == nil {
		line += s.Muted.Render("  icons ") + s.Text.Render(strings.Join([]string{ic.Busy, ic.Waiting, ic.Idle, ic.Branch, ic.Shield, ic.Changes}, " "))
	}
	return line
}
