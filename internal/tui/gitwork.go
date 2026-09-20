package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Widths the workstation lays its regions out at: the refs on the left and
// the detail on the right give way to the history as the frame narrows.
const (
	workThreeRegions = 104
	workTwoRegions   = 72
	workRefsMin      = 18
	workRefsMax      = 26
	workDetailMin    = 34
	workDetailMax    = 56
)

// GitState is one reading of a repository: everything the regions draw.
type GitState struct {
	// Commits are the history the graph draws, newest first, and More says
	// there is a page before the oldest of them.
	Commits []vcs.Commit
	More    bool
	// Refs are the branches, remotes, worktrees, stashes and tags.
	Refs GitRefs
	// Changes is the working tree, Progress what git stopped in the middle
	// of.
	Changes  vcs.Changes
	Progress vcs.InProgress
	// Err is what went wrong reading the repository, which the command bar
	// says rather than leaving a frame that is quietly out of date.
	Err error
	At  time.Time
}

// GitCommitRead is one commit read in full, the answer to ReadCommit.
type GitCommitRead struct {
	Rev    string
	Detail vcs.CommitDetail
	Err    error
}

// GitRegion names one of the three regions of the workstation.
type GitRegion int

const (
	// RegionRefs is the pane on the left, RegionGraph the history in the
	// middle and RegionDetail what is in front of you on the right.
	RegionRefs GitRegion = iota
	RegionGraph
	RegionDetail
	gitRegionCount
)

// String names a region, for a message about where the keys are.
func (r GitRegion) String() string {
	switch r {
	case RegionRefs:
		return "refs"
	case RegionGraph:
		return "history"
	case RegionDetail:
		return "detail"
	}
	return "unknown"
}

// GitWorkOptions configure the workstation.
type GitWorkOptions struct {
	Styles Styles
	// States delivers the readings of the repository; the workstation waits
	// on it for its whole life. A closed channel keeps the last reading on
	// screen.
	States <-chan GitState
	// ReadCommit reads one commit in full for the detail region and ReadMore
	// the page of history before a commit. Refresh asks for a reading now.
	// They are nil where there is nothing to read with, and the keys then do
	// nothing rather than reporting a failure the user cannot act on.
	ReadCommit func(rev string) tea.Cmd
	ReadMore   func(before string) tea.Cmd
	Refresh    func() tea.Cmd
	// Filter reads the history of one ref, for a ref the page on screen does
	// not reach. It is nil where the whole history is all there is, and the
	// workstation then says the ref is out of reach rather than moving the
	// cursor nowhere.
	Filter func(rev string) tea.Cmd
	// Open opens the diff of a file: rev is the commit it belongs to, empty
	// for the working tree.
	Open func(path, rev string) tea.Cmd
	// Root is the project the workstation is about and Home the directory
	// paths are shortened against.
	Root, Home string
	// Width and Height size the first frame.
	Width, Height int
	// Popup makes q and esc close the workstation; in a pane they do nothing,
	// so a stray key never takes it out of a layout.
	Popup bool
	// Now is the clock ages and double clicks are measured with.
	Now func() time.Time
}

type (
	gitStateMsg    struct{ state GitState }
	gitStatesDone  struct{}
	gitCommitReadM struct{ read GitCommitRead }
)

// GitReadCommit is the message a ReadCommit command reports.
func GitReadCommit(read GitCommitRead) tea.Msg { return gitCommitReadM{read: read} }

// GitWorkNoteMsg is one line for the command bar: what something the
// workstation started has to say when it could not be carried out. The next
// key press clears it, so a line nobody was looking at is still there when
// they look.
type GitWorkNoteMsg struct{ Text string }

// GitWorkNote builds the note a command reports a failure with.
func GitWorkNote(text string) tea.Msg { return GitWorkNoteMsg{Text: sanitize.Line(text)} }

// GitWorkModel is the git workstation: the refs of the project, its history,
// the commit or the working tree in front of you, and the bar that says what
// the keys do here.
type GitWorkModel struct {
	opts   GitWorkOptions
	refs   *GitRefsModel
	graph  *GitGraphModel
	detail *GitDetailModel
	form   *GitFormModel

	nav    navKeys
	nextK  key.Binding
	prevK  key.Binding
	treeK  key.Binding
	readK  key.Binding
	keysK  key.Binding
	quitK  key.Binding
	ops    []gitOpKey
	width  int
	height int

	region GitRegion
	state  GitState
	have   bool
	closed bool
	note   string
	// keysOpen says the list of keys is over the regions and keysTop how far
	// it is scrolled; asked is the operation the form now up stands before.
	keysOpen bool
	keysTop  int
	asked    GitOp
	// rev is the commit the detail region stands on, empty when it is on the
	// working tree.
	rev string
}

// NewGitWork builds the workstation. Its regions are built here rather than
// handed in, so one options record configures the whole of it.
func NewGitWork(opts GitWorkOptions) *GitWorkModel {
	w, h := sizeOr(opts.Width, opts.Height)
	m := &GitWorkModel{
		opts:   opts,
		nav:    newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		nextK:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "region")),
		prevK:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),
		treeK:  key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "working tree")),
		readK:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "read again")),
		keysK:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),
		quitK:  key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		ops:    gitOpKeys(),
		width:  w,
		height: h,
		region: RegionGraph,
	}
	m.refs = NewGitRefs(GitRefsOptions{
		Styles: opts.Styles, Root: opts.Root, Home: opts.Home, Now: opts.Now,
	})
	m.graph = NewGitGraph(GitGraphOptions{Styles: opts.Styles, Now: opts.Now})
	m.detail = NewGitDetail(GitDetailOptions{Styles: opts.Styles, Now: opts.Now})
	m.form = NewGitForm(GitFormOptions{Styles: opts.Styles})
	m.focus(RegionGraph)
	m.resize()
	return m
}

// Init waits for the first reading.
func (m *GitWorkModel) Init() tea.Cmd { return m.waitState() }

// waitState takes the next reading off the channel. A closed channel leaves
// the last one on screen and says so.
func (m *GitWorkModel) waitState() tea.Cmd {
	ch := m.opts.States
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		state, ok := <-ch
		if !ok {
			return gitStatesDone{}
		}
		return gitStateMsg{state: state}
	}
}

// Region is which of the three the keys reach, for a caller that drives the
// workstation from outside.
func (m *GitWorkModel) Region() GitRegion { return m.region }

// focus moves the keys to one region and tells all three where they stand.
func (m *GitWorkModel) focus(r GitRegion) {
	m.region = r
	m.refs.SetFocused(r == RegionRefs)
	m.graph.SetFocused(r == RegionGraph)
	m.detail.SetFocused(r == RegionDetail)
}

// typing reports whether the focused region has a line open, which is when
// every printable key belongs to it.
func (m *GitWorkModel) typing() bool {
	switch m.region {
	case RegionRefs:
		return m.refs.Typing()
	case RegionGraph:
		return m.graph.Typing()
	}
	return false
}

// Update handles messages.
func (m *GitWorkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.resize()
		return m, nil
	case gitStateMsg:
		m.apply(msg.state)
		return m, m.waitState()
	case gitStatesDone:
		m.closed = true
		return m, nil
	case gitCommitReadM:
		m.showCommitRead(msg.read)
		return m, nil
	case GitWorkNoteMsg:
		m.note = sanitize.Line(msg.Text)
		return m, nil
	case GitAskMsg:
		m.asked = msg.Op
		m.form.Ask(msg.Form)
		return m, nil
	case GitFormDoneMsg:
		return m, m.answered(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		return m, m.mouse(msg, msg.X, msg.Y)
	case tea.MouseWheelMsg:
		return m, m.mouse(msg, msg.X, msg.Y)
	}
	return m, nil
}

// apply puts a reading on screen, keeping every cursor where it was. It sizes
// the regions again: an operation left in the middle takes a line of the
// frame that was theirs a moment ago.
func (m *GitWorkModel) apply(state GitState) {
	m.state, m.have = state, true
	m.graph.SetCommits(state.Commits, state.More)
	m.refs.SetRefs(state.Refs)
	if m.rev == "" {
		m.detail.ShowChanges(state.Changes)
	}
	m.resize()
}

// showCommitRead puts a commit read in full in the detail region, unless the
// cursor has moved on since the reading was asked for.
func (m *GitWorkModel) showCommitRead(read GitCommitRead) {
	if read.Rev != m.rev {
		return
	}
	if read.Err != nil {
		m.detail.ShowError(read.Err)
		return
	}
	m.detail.ShowCommit(read.Detail)
}

func (m *GitWorkModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.form.Asking() {
		// The answer of the form is read here rather than left to the program
		// loop, for the same reason a region's messages are: it belongs to
		// the workstation, not to the form that produced it.
		_, cmd := m.form.Update(msg)
		return m.answer(cmd)
	}
	if m.keysOpen {
		// The list is read, not typed at: it scrolls, and every other key
		// closes it and does nothing else, so nothing is run by a key meant
		// to dismiss it.
		if d, ok := m.nav.delta(msg, len(m.keyLines()), m.keysHeight()); ok {
			m.keysTop = min(max(m.keysTop+d, 0), max(len(m.keyLines())-m.keysHeight(), 0))
			return nil
		}
		m.keysOpen, m.keysTop = false, 0
		return nil
	}
	m.note = ""
	if !m.typing() {
		switch {
		case key.Matches(msg, m.nextK):
			m.focus(m.step(1))
			m.resize()
			return nil
		case key.Matches(msg, m.prevK):
			m.focus(m.step(-1))
			m.resize()
			return nil
		case key.Matches(msg, m.treeK):
			return m.showWorkingTree()
		case key.Matches(msg, m.keysK):
			m.keysOpen, m.keysTop = true, 0
			return nil
		case key.Matches(msg, m.readK):
			if m.opts.Refresh == nil {
				return nil
			}
			return m.opts.Refresh()
		case m.opts.Popup && key.Matches(msg, m.quitK):
			return tea.Quit
		}
		if cmd, ok := m.opKey(msg); ok {
			return cmd
		}
	}
	return m.toRegion(msg)
}

// opKey asks for the operation a key stands for, false when the key is none
// of them. A key of this region with nothing under it says so rather than
// falling through to a region that would do something else with it.
func (m *GitWorkModel) opKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	var matched *gitOpKey
	for i, k := range m.ops {
		if k.region != anyRegion && k.region != m.region {
			continue
		}
		if !key.Matches(msg, k.binding) {
			continue
		}
		if matched == nil {
			matched = &m.ops[i]
		}
		op, ok := m.target(k.need)
		if !ok {
			continue
		}
		op.Kind = k.kind
		return func() tea.Msg { return GitOpMsg{Op: op} }, true
	}
	if matched == nil {
		return nil, false
	}
	m.note = "nothing to " + matched.kind.String() + " here"
	return nil, true
}

// target is the operation filled in with what it applies to, false when there
// is nothing there to apply it to.
func (m *GitWorkModel) target(need gitOpNeed) (GitOp, bool) {
	op := GitOp{Index: -1}
	switch need {
	case needNothing:
		return op, true
	case needRunning:
		return op, m.state.Progress.Running()
	case needRef:
		ref, ok := m.refs.Selected()
		if !ok {
			return op, false
		}
		op.Rev, op.Name, op.Path, op.Index = ref.Rev, ref.Name, ref.Path, ref.Index
		return op, true
	case needCommit:
		c, ok := m.graph.Selected()
		if !ok {
			return op, false
		}
		op.Rev = c.OID
		return op, true
	case needFile:
		file, ok := m.detail.Selected()
		if !ok {
			return op, false
		}
		op.Path, op.Rev = file.Path, file.Rev
		return op, true
	}
	return op, false
}

// answered carries the answer of a form back to the operation that stands
// behind it: confirmed, it is asked for again with what was typed.
func (m *GitWorkModel) answered(msg GitFormDoneMsg) tea.Cmd {
	op := m.asked
	m.asked = GitOp{}
	if !msg.OK {
		return nil
	}
	op.Confirmed, op.Text = true, msg.Value
	return func() tea.Msg { return GitOpMsg{Op: op} }
}

// step is the region the keys move to, skipping the ones the frame is too
// narrow to draw beside the others.
func (m *GitWorkModel) step(delta int) GitRegion {
	r := m.region
	for range int(gitRegionCount) {
		r = GitRegion((int(r) + delta + int(gitRegionCount)) % int(gitRegionCount))
		if r == RegionRefs && m.width < workTwoRegions {
			continue
		}
		return r
	}
	return m.region
}

// showWorkingTree puts the working tree in the detail region and moves the
// keys to it, which is the one thing the history cannot lead to.
func (m *GitWorkModel) showWorkingTree() tea.Cmd {
	m.rev = ""
	m.detail.ShowChanges(m.state.Changes)
	m.focus(RegionDetail)
	m.resize()
	return nil
}

// toRegion hands a message to the focused region and reads what it reports
// back: a ref chosen moves the history to it, a commit chosen reads it in
// full, a file chosen opens its diff.
func (m *GitWorkModel) toRegion(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.region {
	case RegionRefs:
		_, cmd = m.refs.Update(msg)
	case RegionGraph:
		_, cmd = m.graph.Update(msg)
	case RegionDetail:
		_, cmd = m.detail.Update(msg)
	}
	return m.answer(cmd)
}

// answer runs what a region reported and turns it into what the workstation
// does about it. A command of a region is a message and nothing else, so it
// is run here rather than left to the program loop, which would send it back
// to the region it came from. None of the regions is built as a popup, so
// none of them ever asks to close; only the workstation does.
func (m *GitWorkModel) answer(cmd tea.Cmd) tea.Cmd {
	var out []tea.Cmd
	for _, msg := range flatten(cmd) {
		switch msg := msg.(type) {
		case GitRefChosenMsg:
			out = append(out, m.showRef(msg.Ref))
		case GitCommitSelectedMsg:
			out = append(out, m.readCommit(msg.Commit.OID))
		case GitCommitChosenMsg:
			m.focus(RegionDetail)
			m.resize()
		case GitGraphMoreMsg:
			out = append(out, m.readMore(m.oldest()))
		case GitFileChosenMsg:
			if m.opts.Open != nil {
				out = append(out, m.opts.Open(msg.Path, msg.Rev))
			}
		case GitFormDoneMsg:
			out = append(out, m.answered(msg))
		default:
			out = append(out, func() tea.Msg { return msg })
		}
	}
	return tea.Batch(out...)
}

// showRef follows a ref into the history: the cursor moves to the commit it
// stands on when the page on screen reaches it, and the history is read from
// the ref when it does not.
func (m *GitWorkModel) showRef(ref GitRef) tea.Cmd {
	m.graph.SetTitle(ref.Name)
	if ref.Rev == "" {
		return nil
	}
	if !m.graph.GoTo(ref.Rev) {
		if m.opts.Filter == nil {
			m.note = ref.Name + " is on no commit of the history read so far"
			return nil
		}
		m.focus(RegionGraph)
		m.resize()
		return m.opts.Filter(ref.Rev)
	}
	m.focus(RegionGraph)
	m.resize()
	c, ok := m.graph.Selected()
	if !ok {
		return nil
	}
	return m.readCommit(c.OID)
}

// readCommit asks for one commit in full and says the detail region is
// waiting for it.
func (m *GitWorkModel) readCommit(rev string) tea.Cmd {
	if rev == "" || m.opts.ReadCommit == nil {
		return nil
	}
	m.rev = rev
	m.detail.Reading()
	return m.opts.ReadCommit(rev)
}

// oldest is the commit the history read so far ends on, which is where the
// page after it begins.
func (m *GitWorkModel) oldest() string {
	if len(m.state.Commits) == 0 {
		return ""
	}
	return m.state.Commits[len(m.state.Commits)-1].OID
}

// readMore asks for the page of history before the oldest commit read.
func (m *GitWorkModel) readMore(before string) tea.Cmd {
	if before == "" || m.opts.ReadMore == nil {
		return nil
	}
	return m.opts.ReadMore(before)
}

// mouse hands a pointer message to the region it fell in, moving the keys
// there first, so clicking a region is the same as tabbing to it.
func (m *GitWorkModel) mouse(msg tea.Msg, x, y int) tea.Cmd {
	if m.form.Asking() || m.keysOpen {
		return nil
	}
	refs, graph, detail := m.layout()
	top := m.bannerHeight()
	if y < top || y >= top+m.bodyHeight() {
		return nil
	}
	region, at := m.region, x
	switch {
	case refs > 0 && x < refs:
		region, at = RegionRefs, x
	case graph > 0 && x < refs+graph:
		region, at = RegionGraph, x-refs
	case detail > 0:
		region, at = RegionDetail, x-refs-graph
	}
	if region != m.region {
		m.focus(region)
		m.resize()
	}
	return m.toRegion(moveMouse(msg, at, y-top))
}

// moveMouse puts a pointer message into the coordinates of the region it fell
// in, since a region draws as though it were the whole frame.
func moveMouse(msg tea.Msg, x, y int) tea.Msg {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		msg.X, msg.Y = x, y
		return msg
	case tea.MouseWheelMsg:
		msg.X, msg.Y = x, y
		return msg
	}
	return msg
}

// layout is how wide each region is drawn, zero for one the frame has no room
// for beside the others.
func (m *GitWorkModel) layout() (refs, graph, detail int) {
	switch {
	case m.width >= workThreeRegions:
		refs = min(max(m.width/6, workRefsMin), workRefsMax)
		detail = min(max(m.width*2/5, workDetailMin), workDetailMax)
		return refs, m.width - refs - detail, detail
	case m.width >= workTwoRegions:
		refs = min(max(m.width/5, workRefsMin), workRefsMax)
		// The detail takes the place of the history while it is the region
		// being typed at: two regions fit, three do not.
		if m.region == RegionDetail {
			return refs, 0, m.width - refs
		}
		return refs, m.width - refs, 0
	}
	switch m.region {
	case RegionRefs:
		return m.width, 0, 0
	case RegionDetail:
		return 0, 0, m.width
	}
	return 0, m.width, 0
}

// bannerHeight is the line an operation left in the middle takes.
func (m *GitWorkModel) bannerHeight() int {
	if m.state.Progress.Running() {
		return 1
	}
	return 0
}

// bodyHeight is what the regions are drawn in, the banner and the command bar
// taken off.
func (m *GitWorkModel) bodyHeight() int { return max(m.height-m.bannerHeight()-1, 1) }

// resize gives every region the frame it draws in.
func (m *GitWorkModel) resize() {
	refs, graph, detail := m.layout()
	h := m.bodyHeight()
	m.refs.Update(tea.WindowSizeMsg{Width: max(refs, 1), Height: h})
	m.graph.Update(tea.WindowSizeMsg{Width: max(graph, 1), Height: h})
	m.detail.Update(tea.WindowSizeMsg{Width: max(detail, 1), Height: h})
	m.form.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
}

// View draws the workstation.
func (m *GitWorkModel) View() tea.View {
	v := tea.NewView(screen(m.frame(), m.width, m.height))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *GitWorkModel) frame() []string {
	var out []string
	if b := m.banner(); b != "" {
		out = append(out, b)
	}
	out = append(out, m.body()...)
	out = append(out, m.bar())
	box := m.form.Box(m.width, m.height)
	if m.keysOpen {
		box = m.keysBox()
	}
	if len(box) > 0 {
		x := max((m.width-ansi.StringWidth(box[0]))/2, 0)
		out = overlay(out, box, x, max((m.height-len(box))/2, 0))
	}
	return out
}

// body draws the regions side by side.
func (m *GitWorkModel) body() []string {
	refs, graph, detail := m.layout()
	var columns [][]string
	if refs > 0 {
		columns = append(columns, lines(m.refs.View().Content))
	}
	if graph > 0 {
		columns = append(columns, lines(m.graph.View().Content))
	}
	if detail > 0 {
		columns = append(columns, lines(m.detail.View().Content))
	}
	return beside(columns...)
}

// lines splits a frame back into the lines it was drawn as.
func lines(frame string) []string { return strings.Split(frame, "\n") }

// keyLines are every key of the workstation where the cursor stands, the
// ones of the region first and the ones that work anywhere under them.
func (m *GitWorkModel) keyLines() []string {
	s := m.opts.Styles
	inner := max(boxWidth(m.width)-4, 1)
	const keyColumn = 10
	var lines []string
	add := func(title string, keys []gitOpKey) {
		if len(keys) == 0 {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, s.Title.Render(title))
		for _, k := range keys {
			name := fit(k.binding.Help().Key, keyColumn, s.Ellipsis)
			lines = append(lines, s.Key.Render(name)+
				s.Text.Render(truncate(k.kind.String(), max(inner-keyColumn, 1), s.Ellipsis)))
		}
	}
	var here, every []gitOpKey
	for _, k := range m.ops {
		switch k.region {
		case m.region:
			here = append(here, k)
		case anyRegion:
			every = append(every, k)
		}
	}
	add("IN THE "+strings.ToUpper(m.region.String()), here)
	add("ANYWHERE", every)
	return lines
}

// keysHeight is how many lines of the list the frame has room for, the two
// edges of the box and the line under it taken off.
func (m *GitWorkModel) keysHeight() int { return max(m.height-4, 1) }

// keysBox lists what the keys do where the cursor stands, since a region has
// more of them than a bar can hold. A list longer than the frame scrolls
// rather than losing its end.
func (m *GitWorkModel) keysBox() []string {
	s := m.opts.Styles
	all := m.keyLines()
	h := m.keysHeight()
	end := min(m.keysTop+h, len(all))
	lines := append([]string(nil), all[min(m.keysTop, end):end]...)
	help := "any key closes this"
	if len(all) > h {
		help = strconv.Itoa(end) + " of " + strconv.Itoa(len(all)) +
			", " + m.nav.Down.Help().Key + " scrolls, any other key closes this"
	}
	lines = append(lines, "", s.Muted.Render(truncate(help, max(boxWidth(m.width)-4, 1), s.Ellipsis)))
	return s.box(boxWidth(m.width), "Keys", lines)
}

// banner says what git stopped in the middle of, which holds the branch where
// it is and makes most commands refuse.
func (m *GitWorkModel) banner() string {
	s := m.opts.Styles
	p := m.state.Progress
	if !p.Running() {
		return ""
	}
	segs := []segment{
		seg(" "+s.Theme.Icons.Waiting+" ", s.Warning),
		seg(gitOperationName(p.Kind), s.Warning.Bold(true)),
	}
	if p.Branch != "" {
		segs = append(segs, seg(" of "+sanitize.Line(p.Branch), s.Warning))
	}
	if p.Total > 0 {
		segs = append(segs, seg(" at step "+strconv.Itoa(p.Step)+" of "+strconv.Itoa(p.Total), s.Warning))
	}
	if len(p.Heads) > 0 {
		segs = append(segs, seg(" stopped on "+vcs.Commit{OID: p.Heads[0]}.Short(), s.Muted))
	}
	return s.line(m.width, &s.Bar, segs...)
}

// gitOperationName names what git is in the middle of.
func gitOperationName(k vcs.Operation) string {
	switch k {
	case vcs.OperationMerge:
		return "a merge"
	case vcs.OperationRebase:
		return "a rebase"
	case vcs.OperationApply:
		return "a patch"
	case vcs.OperationCherryPick:
		return "a cherry pick"
	case vcs.OperationRevert:
		return "a revert"
	case vcs.OperationBisect:
		return "a bisection"
	}
	return "an operation"
}

// bar is the command bar: what went wrong or what was just said, and the keys
// that work wherever the cursor stands.
func (m *GitWorkModel) bar() string {
	s := m.opts.Styles
	help := s.helpLine(max(m.width/2, 12), m.keys()...)
	var left []segment
	switch {
	case m.note != "":
		left = append(left, seg(" "+truncate(m.note, max(m.width/2, 8), s.Ellipsis), s.Warning))
	case m.state.Err != nil:
		left = append(left, seg(" "+truncate(sanitize.Line(m.state.Err.Error()), max(m.width/2, 8), s.Ellipsis), s.Danger))
	case !m.have:
		left = append(left, seg(" reading the repository", s.Muted))
	case m.closed:
		left = append(left, seg(" the repository is no longer watched", s.Muted))
	default:
		left = append(left, seg(" "+shortPath(m.opts.Root, m.opts.Home), s.Muted))
	}
	if ansi.StringWidth(help) >= m.width {
		return help
	}
	return s.line(m.width-ansi.StringWidth(help), &s.Bar, left...) + help
}

func (m *GitWorkModel) keys() []key.Binding {
	keys := []key.Binding{m.nextK, m.treeK}
	if m.opts.Refresh != nil {
		keys = append(keys, m.readK)
	}
	keys = append(keys, m.keysK)
	if m.opts.Popup {
		keys = append(keys, m.quitK)
	}
	return keys
}
