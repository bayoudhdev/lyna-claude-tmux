package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Bounds of a form: how wide its box is drawn and how much text it takes.
const (
	formMaxWidth = 72
	formMinWidth = 28
	// formLimit bounds a text field. A commit message longer than this is
	// written in an editor, not typed into a one-line field.
	formLimit = 512
)

// GitAsk is what a form wants back.
type GitAsk int

const (
	// AskConfirm runs the command on one key. AskName runs it only once the
	// name it names has been typed back, which is what stands between a key
	// pressed by habit and work that cannot be recovered. AskText collects a
	// line: a message, a branch name, a tag.
	AskConfirm GitAsk = iota
	AskName
	AskText
)

// GitForm is a question the workstation puts before it runs a command.
type GitForm struct {
	// ID says what the answer is for. It is carried back untouched, so the
	// caller reads it rather than matching on the wording.
	ID string
	// Ask is the kind of answer wanted.
	Ask GitAsk
	// Title is the heading of the box, Question the sentence under it.
	Title    string
	Question string
	// Lose names what the command cannot give back, one line each. A form
	// that names nothing is not a destructive one.
	Lose []string
	// Command is the argv about to run. The form draws it as it is given,
	// quoted so what is read is what runs, and never runs it.
	Command []string
	// Expect is the name that has to be typed back for AskName.
	Expect string
	// Value is what a text field starts with and Placeholder what it shows
	// while empty.
	Value       string
	Placeholder string
	// Required refuses an empty text field.
	Required bool
	// Check refuses a value before the command is proposed, so a branch name
	// git would not take is caught in the form rather than in a command that
	// failed.
	Check func(string) error
}

// GitFormDoneMsg is a form that was answered or given up on.
type GitFormDoneMsg struct {
	ID string
	// Value is what was typed, empty for a confirmation.
	Value string
	// OK is false for a form the user backed out of.
	OK bool
}

// GitFormOptions configure the form view.
type GitFormOptions struct {
	Styles Styles
	// Width and Height size the first frame.
	Width, Height int
}

// GitFormModel asks one question at a time.
type GitFormModel struct {
	opts    GitFormOptions
	yesK    key.Binding
	noK     key.Binding
	okK     key.Binding
	cancelK key.Binding
	width   int
	height  int
	form    GitForm
	in      lineInput
	err     string
	asking  bool
}

// NewGitForm builds the form view, asking nothing yet.
func NewGitForm(opts GitFormOptions) *GitFormModel {
	w, h := sizeOr(opts.Width, opts.Height)
	return &GitFormModel{
		opts:    opts,
		yesK:    key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "run it")),
		noK:     key.NewBinding(key.WithKeys("n", "esc", "q"), key.WithHelp("n", "cancel")),
		okK:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run it")),
		cancelK: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		width:   w,
		height:  h,
	}
}

// Ask puts a form up, replacing whatever was being asked.
func (m *GitFormModel) Ask(f GitForm) {
	m.form, m.asking, m.err = f, true, ""
	m.in = lineInput{limit: formLimit}
	if f.Ask == AskText {
		m.in.insert(f.Value)
	}
}

// Asking reports whether a form is up, which is what tells the view under it
// that the keys are not its own.
func (m *GitFormModel) Asking() bool { return m.asking }

// Value is what has been typed so far.
func (m *GitFormModel) Value() string { return m.in.value }

// Init has nothing to wait for.
func (m *GitFormModel) Init() tea.Cmd { return nil }

// Update handles messages.
func (m *GitFormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *GitFormModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return m.done(false)
	}
	if !m.asking {
		return nil
	}
	if m.form.Ask == AskConfirm {
		switch {
		case key.Matches(msg, m.yesK):
			return m.done(true)
		case key.Matches(msg, m.noK):
			return m.done(false)
		}
		return nil
	}
	if key.Matches(msg, m.cancelK) {
		return m.done(false)
	}
	if key.Matches(msg, m.okK) {
		return m.answer()
	}
	if m.in.update(msg) {
		m.err = ""
	}
	return nil
}

// answer reads the field, and refuses it here rather than letting a command
// fail on it.
func (m *GitFormModel) answer() tea.Cmd {
	value := strings.TrimSpace(m.in.value)
	if m.form.Ask == AskName {
		if value != m.form.Expect {
			m.err = "that is not " + m.form.Expect
			return nil
		}
		return m.done(true)
	}
	if value == "" && m.form.Required {
		m.err = "nothing typed yet"
		return nil
	}
	if m.form.Check != nil {
		if err := m.form.Check(value); err != nil {
			m.err = sanitize.Line(err.Error())
			return nil
		}
	}
	return m.done(true)
}

// done closes the form and reports the answer.
func (m *GitFormModel) done(ok bool) tea.Cmd {
	if !m.asking {
		return nil
	}
	answer := GitFormDoneMsg{ID: m.form.ID, OK: ok}
	if ok && m.form.Ask != AskConfirm {
		answer.Value = strings.TrimSpace(m.in.value)
	}
	m.form, m.asking, m.err = GitForm{}, false, ""
	m.in = lineInput{}
	return func() tea.Msg { return answer }
}

// View draws the form on a frame of its own. A workstation that has the form
// over its own regions draws Box instead.
func (m *GitFormModel) View() tea.View {
	v := tea.NewView(screen(m.frame(), m.width, m.height))
	v.AltScreen = true
	return v
}

// frame centers the box on an empty screen.
func (m *GitFormModel) frame() []string {
	box := m.Box(m.width, m.height)
	if len(box) == 0 {
		return nil
	}
	top := max((m.height-len(box))/2, 0)
	pad := strings.Repeat(" ", max((m.width-boxWidth(m.width))/2, 0))
	out := make([]string, 0, top+len(box))
	for range top {
		out = append(out, "")
	}
	for _, l := range box {
		out = append(out, pad+l)
	}
	return out
}

// boxWidth is how wide the box is drawn inside a frame of width cells.
func boxWidth(width int) int {
	return max(min(width-4, formMaxWidth), min(width, formMinWidth))
}

// Box draws the form as a box of its own, for a view that puts it over what
// it was already drawing. It returns nothing when no form is up.
//
// A box taller than the frame would lose its own last lines, which are the
// ones the answer is given on, so it gives up its text instead: first the end
// of the question, then the command, then the end of what is lost. What is
// asked for and how to answer it are never dropped.
func (m *GitFormModel) Box(width, height int) []string {
	if !m.asking {
		return nil
	}
	s := m.opts.Styles
	w := boxWidth(width)
	inner := max(w-4, 1)
	question := m.wrap(m.form.Question, inner, s.Text)
	lose := m.loseLines(inner)
	command := m.commandLines(inner)
	tail := m.tailLines(inner, false)
	tight := false
	for {
		lines := make([]string, 0, len(question)+len(lose)+len(command)+len(tail))
		lines = append(lines, question...)
		lines = append(lines, lose...)
		lines = append(lines, command...)
		lines = append(lines, tail...)
		switch {
		case height <= 0 || len(lines)+2 <= height:
			return s.box(w, m.form.Title, lines)
		case len(question) > 1:
			question = m.shorten(question, inner)
		case len(command) > 0:
			command = nil
		case len(lose) > 1:
			lose = m.shorten(lose, inner)
		case len(lose) > 0:
			lose = nil
		case len(question) > 0:
			question = nil
		case !tight:
			tight, tail = true, m.tailLines(inner, true)
		default:
			return s.box(w, m.form.Title, lines)
		}
	}
}

// shorten drops the last line of a block and marks the one before it, so what
// is left reads as the beginning of a sentence rather than as the whole of
// one. The mark takes the place of the last cell, which is why the line is
// cut without one of its own.
func (m *GitFormModel) shorten(lines []string, inner int) []string {
	lines = lines[:len(lines)-1]
	last := len(lines) - 1
	s := m.opts.Styles
	lines[last] = ansi.Truncate(lines[last], max(inner-1, 0), "") + s.Muted.Render(s.Ellipsis)
	return lines
}

// loseLines name what the command cannot give back, marked one by one.
func (m *GitFormModel) loseLines(inner int) []string {
	if len(m.form.Lose) == 0 {
		return nil
	}
	s := m.opts.Styles
	mark := s.Theme.Icons.Failed
	out := []string{""}
	for _, l := range m.form.Lose {
		for i, w := range m.wrap(l, max(inner-2, 1), s.Danger) {
			head := "  "
			if i == 0 {
				head = s.Danger.Render(mark + " ")
			}
			out = append(out, head+w)
		}
	}
	return out
}

// commandLines draw the argv about to run.
func (m *GitFormModel) commandLines(inner int) []string {
	if len(m.form.Command) == 0 {
		return nil
	}
	s := m.opts.Styles
	return append([]string{"", s.Muted.Render("runs")},
		m.wrap(commandLine(m.form.Command), inner, s.Accent2)...)
}

// tailLines are what the answer is given on: the field, why the last one was
// refused, and the keys. A tight tail gives up the blank lines around them,
// which is the last thing a box too small for its text has left to give.
func (m *GitFormModel) tailLines(inner int, tight bool) []string {
	s := m.opts.Styles
	var out []string
	if !tight {
		out = append(out, "")
	}
	field := m.fieldLines(inner)
	out = append(out, field...)
	if m.err != "" {
		out = append(out, s.Danger.Render(truncate(sanitize.Line(m.err), inner, s.Ellipsis)))
	}
	if !tight && len(field) > 0 {
		out = append(out, "")
	}
	return append(out, s.helpLine(inner, m.keys()...))
}

// fieldLines are what the form asks with: nothing for a confirmation, the
// name to type back and the field for the rest.
func (m *GitFormModel) fieldLines(inner int) []string {
	s := m.opts.Styles
	if m.form.Ask == AskConfirm {
		return nil
	}
	var out []string
	if m.form.Ask == AskName {
		out = append(out, s.Muted.Render("type ")+
			s.Warning.Render(truncate(sanitize.Line(m.form.Expect), max(inner-14, 1), s.Ellipsis))+
			s.Muted.Render(" to go on"))
	}
	prompt := "> "
	if s.Theme.Icons.Name != "ascii" {
		prompt = "› "
	}
	body, style, typed := m.in.value, s.Text, true
	if body == "" && m.form.Placeholder != "" {
		body, style, typed = sanitize.Line(m.form.Placeholder), s.Muted, false
	}
	// The field scrolls with what is typed, so the end of a long value stays
	// under the cursor rather than the beginning of it.
	field := max(inner-ansi.StringWidth(prompt)-1, 1)
	if w := ansi.StringWidth(body); w > field && typed {
		body = ansi.TruncateLeft(body, w-field, s.Ellipsis)
	}
	cursor := "▌"
	if s.Theme.Icons.Name == "ascii" {
		cursor = "_"
	}
	return append(out, s.Accent.Render(prompt)+style.Render(fit(body, field, s.Ellipsis))+s.Accent.Render(cursor))
}

func (m *GitFormModel) keys() []key.Binding {
	if m.form.Ask == AskConfirm {
		return []key.Binding{m.yesK, m.noK}
	}
	return []key.Binding{m.okK, m.cancelK}
}

// wrap breaks text into lines of the box, each rendered in one style.
func (m *GitFormModel) wrap(text string, width int, style lipgloss.Style) []string {
	var out []string
	for _, para := range strings.Split(sanitize.Line(text), "\n") {
		for _, l := range strings.Split(ansi.Wrap(para, max(width, 1), ""), "\n") {
			out = append(out, style.Render(l))
		}
	}
	return out
}

// commandLine draws an argv the way a shell would read it back: an argument
// holding anything but the safe characters is quoted, so what is on screen is
// the command that runs. It is drawn and never executed; every command of the
// workstation is run as argv.
func commandLine(argv []string) string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		out = append(out, shellDisplay(sanitize.Line(a)))
	}
	return strings.Join(out, " ")
}

func shellDisplay(a string) string {
	if a == "" {
		return "''"
	}
	safe := true
	for i := range len(a) {
		switch c := a[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-', c == '/', c == '=', c == ':', c == ',', c == '+', c == '@', c == '^':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// DiscardFileForm asks before a file of the working tree goes back to what
// HEAD holds.
func DiscardFileForm(path string, command []string) GitForm {
	return GitForm{
		ID: "discard-file", Ask: AskConfirm,
		Title:    "Discard a file",
		Question: path + " goes back to what HEAD holds.",
		Lose:     []string{"the changes made to it since are in no commit and no stash"},
		Command:  command,
	}
}

// DiscardAllForm asks before the whole working tree goes back to HEAD, which
// is the same thing over every file at once and so asks for the branch to be
// typed.
func DiscardAllForm(branch string, files int, command []string) GitForm {
	return GitForm{
		ID: "discard-all", Ask: AskName, Expect: branch,
		Title:    "Discard every change",
		Question: plural(files, "file goes", "files go") + " back to what HEAD holds on " + branch + ".",
		Lose:     []string{"every change made since the last commit, in no commit and no stash"},
		Command:  command,
	}
}

// ResetHardForm asks before the branch is moved and the working tree written
// over with it.
func ResetHardForm(branch, rev string, behind int, command []string) GitForm {
	f := GitForm{
		ID: "reset-hard", Ask: AskName, Expect: branch,
		Title:    "Move the branch and write the tree over",
		Question: branch + " moves to " + rev + ", and the working tree is written over with it.",
		Lose:     []string{"every change of the working tree and of the index"},
		Command:  command,
	}
	if behind > 0 {
		f.Lose = append(f.Lose, plural(behind, "commit is", "commits are")+
			" left behind, reachable only through the reflog")
	}
	return f
}

// DeleteBranchForm asks before a branch is deleted. A branch whose commits
// are in no other branch asks for its name back; one already merged is one
// key, since what it holds stays reachable.
func DeleteBranchForm(name string, merged bool, command []string) GitForm {
	f := GitForm{
		ID:       "delete-branch",
		Title:    "Delete a branch",
		Question: name + " is deleted here; a branch of the same name on a remote is left alone.",
		Command:  command,
	}
	if merged {
		f.Ask = AskConfirm
		f.Question += " Its commits are in the branch it was merged into."
		return f
	}
	f.Ask, f.Expect = AskName, name
	f.Lose = []string{"the commits of " + name + " are in no other branch, and only the reflog still names them"}
	return f
}

// DropStashForm asks before one stash is thrown away.
func DropStashForm(index int, subject string, command []string) GitForm {
	return GitForm{
		ID: "drop-stash", Ask: AskConfirm,
		Title:    "Drop a stash",
		Question: "stash@{" + strconv.Itoa(index) + "} " + subject + " is thrown away.",
		Lose:     []string{"the changes it holds are in no commit and no branch"},
		Command:  command,
	}
}

// ClearStashesForm asks before every stash is thrown away at once.
func ClearStashesForm(n int, command []string) GitForm {
	return GitForm{
		ID: "clear-stashes", Ask: AskName, Expect: "clear",
		Title:    "Drop every stash",
		Question: plural(n, "stash is", "stashes are") + " thrown away.",
		Lose:     []string{"the changes they hold are in no commit and no branch"},
		Command:  command,
	}
}

// RemoveWorktreeForm asks before a worktree is removed. One holding changes
// asks for its name back, since those changes are on no branch.
func RemoveWorktreeForm(name, path string, dirty bool, command []string) GitForm {
	f := GitForm{
		ID: "remove-worktree", Ask: AskConfirm,
		Title:    "Remove a worktree",
		Question: path + " is removed; the branch it stands on is left alone.",
		Command:  command,
	}
	if dirty {
		f.Ask, f.Expect = AskName, name
		f.Lose = []string{"the changes in it are in no commit and no stash"}
	}
	return f
}

// ForcePushForm asks before a remote branch is written over.
func ForcePushForm(remote, branch string, lease bool, command []string) GitForm {
	f := GitForm{
		ID: "force-push", Ask: AskName, Expect: branch,
		Title:    "Write over a branch of a remote",
		Question: branch + " on " + remote + " is replaced by the one here.",
		Command:  command,
	}
	if lease {
		f.Question += " The push stops if the remote has moved since it was last read."
		f.Lose = []string{"the commits the remote holds and this repository read are written over"}
		return f
	}
	f.Lose = []string{"every commit the remote holds and this repository has not, whoever pushed it"}
	return f
}

// TextForm builds a form that collects a line: a commit message, a branch
// name, a tag. check refuses a value before any command is proposed.
func TextForm(id, title, question, placeholder string, required bool, check func(string) error) GitForm {
	return GitForm{
		ID: id, Ask: AskText,
		Title: title, Question: question, Placeholder: placeholder,
		Required: required, Check: check,
	}
}
