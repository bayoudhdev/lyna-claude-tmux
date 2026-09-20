package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// GitRefs is everything the refs pane lists: what the project points at, what
// it has put aside, and where else it is checked out.
type GitRefs struct {
	Branches  []vcs.LocalBranch
	Remotes   []vcs.RemoteBranch
	Worktrees []vcs.Worktree
	Stashes   []vcs.Stash
	Tags      []vcs.Tag
}

// GitRefKind is what a row of the refs pane stands for.
type GitRefKind int

// The sections of the pane, in the order they are drawn.
const (
	GitRefBranch GitRefKind = iota
	GitRefRemote
	GitRefWorktree
	GitRefStash
	GitRefTag
	gitRefSections
)

// String is the heading the section is drawn under.
func (k GitRefKind) String() string {
	switch k {
	case GitRefBranch:
		return "LOCAL"
	case GitRefRemote:
		return "REMOTE"
	case GitRefWorktree:
		return "WORKTREES"
	case GitRefStash:
		return "STASHES"
	case GitRefTag:
		return "TAGS"
	}
	return "REFS"
}

// GitRef is one row of the refs pane: what it is, what it is called and what
// the history is read from when it is chosen.
type GitRef struct {
	Kind GitRefKind
	// Name is what the row is called ("main", "origin/main", "stash@{0}"),
	// untrusted display data.
	Name string
	// Rev is what the history of the row is read from: a ref for a branch or
	// a tag, a commit for a stash or a detached worktree.
	Rev string
	// Index is the stash entry a stash row stands for, -1 everywhere else.
	Index int
	// Path is the directory of a worktree row, empty everywhere else.
	Path string
}

// GitRefChosenMsg is the ref the user picked. The host filters the history to
// it; the pane itself only says which one it is.
type GitRefChosenMsg struct{ Ref GitRef }

// GitRefsOptions configure the refs pane.
type GitRefsOptions struct {
	Styles Styles
	// Refs is what the pane lists. SetRefs replaces it on every refresh.
	Refs GitRefs
	// Width and Height size the first frame.
	Width, Height int
	// Root is the project the pane was opened on, and Home shortens the
	// worktree paths that live outside it.
	Root string
	Home string
	// Popup makes q and esc close the pane; in a layout they do nothing, so a
	// stray key never takes the pane out of the window.
	Popup bool
	// Now is the clock ages and double clicks are measured with; time.Now
	// when nil.
	Now func() time.Time
}

// gitRefRow is one drawn line: a section heading, or one ref under it.
type gitRefRow struct {
	kind GitRefKind
	// head marks the heading of a section; ref is filled on every other row.
	head bool
	ref  GitRef
	// detail is what the row says on its right: what a branch owes its
	// upstream, the age of a stash, where a worktree is.
	detail string
	// style picks the color of the detail, and marked draws the row as the
	// one the working tree is on.
	style  func(s Styles) lipgloss.Style
	marked bool
}

// GitRefsModel is the refs pane of the git workstation.
type GitRefsModel struct {
	opts      GitRefsOptions
	nav       navKeys
	quitK     key.Binding
	chooseK   key.Binding
	foldK     key.Binding
	filterK   key.Binding
	width     int
	height    int
	folded    [gitRefSections]bool
	rows      []gitRefRow
	list      listView
	filter    lineInput
	filtering bool
	clicks    clicks
}

// NewGitRefs builds the refs pane.
func NewGitRefs(opts GitRefsOptions) *GitRefsModel {
	w, h := sizeOr(opts.Width, opts.Height)
	m := &GitRefsModel{
		opts:    opts,
		nav:     newNavKeys(opts.Styles.Theme.Icons.Name == "ascii"),
		quitK:   key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q", "close")),
		chooseK: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "show")),
		foldK:   key.NewBinding(key.WithKeys(" ", "space"), key.WithHelp("space", "fold")),
		filterK: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		width:   w,
		height:  h,
		filter:  lineInput{limit: 64},
	}
	m.build()
	return m
}

// SetRefs puts a new reading on screen, keeping the cursor on the row it was
// on when that row is still there: a refresh must not move the selection.
func (m *GitRefsModel) SetRefs(refs GitRefs) {
	var on GitRef
	if row, ok := m.row(); ok {
		on = row.ref
	}
	m.opts.Refs = refs
	m.build()
	if on.Name == "" {
		return
	}
	for i, row := range m.rows {
		if !row.head && row.ref.Kind == on.Kind && row.ref.Name == on.Name {
			m.list.move(i-m.list.cursor, len(m.rows), m.bodyHeight())
			return
		}
	}
}

// Selected is the ref the cursor is on, false on a heading and on an empty
// pane.
func (m *GitRefsModel) Selected() (GitRef, bool) {
	row, ok := m.row()
	if !ok || row.head {
		return GitRef{}, false
	}
	return row.ref, true
}

func (m *GitRefsModel) row() (gitRefRow, bool) {
	if m.list.cursor < 0 || m.list.cursor >= len(m.rows) {
		return gitRefRow{}, false
	}
	return m.rows[m.list.cursor], true
}

func (m *GitRefsModel) now() time.Time { return nowOr(m.opts.Now)() }

// build lays the sections out into the rows drawn, leaving out what the
// filter does not match and the contents of a folded section. A section with
// nothing in it keeps its heading: an empty stash list is worth saying.
func (m *GitRefsModel) build() {
	needle := strings.ToLower(m.filter.value)
	rows := make([]gitRefRow, 0, m.count())
	for kind := GitRefBranch; kind < gitRefSections; kind++ {
		items := m.section(kind)
		if needle != "" {
			kept := items[:0:0]
			for _, r := range items {
				if strings.Contains(strings.ToLower(r.ref.Name), needle) {
					kept = append(kept, r)
				}
			}
			items = kept
			if len(items) == 0 {
				continue
			}
		}
		rows = append(rows, gitRefRow{kind: kind, head: true, detail: strconv.Itoa(len(items))})
		if m.folded[kind] {
			continue
		}
		rows = append(rows, items...)
	}
	m.rows = rows
	m.list.clamp(len(m.rows), m.bodyHeight())
}

// count is how many rows an unfiltered, unfolded pane draws.
func (m *GitRefsModel) count() int {
	r := m.opts.Refs
	return int(gitRefSections) + len(r.Branches) + len(r.Remotes) + len(r.Worktrees) + len(r.Stashes) + len(r.Tags)
}

func (m *GitRefsModel) section(kind GitRefKind) []gitRefRow {
	switch kind {
	case GitRefBranch:
		return m.branchRows()
	case GitRefRemote:
		return m.remoteRows()
	case GitRefWorktree:
		return m.worktreeRows()
	case GitRefStash:
		return m.stashRows()
	case GitRefTag:
		return m.tagRows()
	}
	return nil
}

func (m *GitRefsModel) branchRows() []gitRefRow {
	rows := make([]gitRefRow, 0, len(m.opts.Refs.Branches))
	for _, b := range m.opts.Refs.Branches {
		row := gitRefRow{
			kind:   GitRefBranch,
			ref:    GitRef{Kind: GitRefBranch, Name: sanitize.Line(b.Name), Rev: b.Ref, Index: -1},
			marked: b.Head,
		}
		switch {
		case b.Gone:
			row.detail, row.style = "gone", danger
		case b.Ahead > 0 || b.Behind > 0:
			row.detail, row.style = m.track(b.Ahead, b.Behind), warning
		}
		rows = append(rows, row)
	}
	return rows
}

// track is what a branch owes its upstream, drawn with the arrows of the icon
// set.
func (m *GitRefsModel) track(ahead, behind int) string {
	up, down := "↑", "↓"
	if m.opts.Styles.Theme.Icons.Name == "ascii" {
		up, down = "^", "v"
	}
	var parts []string
	if ahead > 0 {
		parts = append(parts, up+strconv.Itoa(ahead))
	}
	if behind > 0 {
		parts = append(parts, down+strconv.Itoa(behind))
	}
	return strings.Join(parts, " ")
}

func (m *GitRefsModel) remoteRows() []gitRefRow {
	rows := make([]gitRefRow, 0, len(m.opts.Refs.Remotes))
	for _, b := range m.opts.Refs.Remotes {
		row := gitRefRow{
			kind: GitRefRemote,
			ref:  GitRef{Kind: GitRefRemote, Name: sanitize.Line(b.Name), Rev: b.Ref, Index: -1},
		}
		// The symbolic ref of a remote is not a branch of its own: it says
		// which branch that remote is on.
		if b.Symbolic() {
			row.detail, row.style = sanitize.Line(strings.TrimPrefix(b.Target, "refs/remotes/")), muted
		}
		rows = append(rows, row)
	}
	return rows
}

func (m *GitRefsModel) worktreeRows() []gitRefRow {
	rows := make([]gitRefRow, 0, len(m.opts.Refs.Worktrees))
	for _, w := range m.opts.Refs.Worktrees {
		rev := w.Branch
		if rev == "" {
			rev = w.Head
		}
		row := gitRefRow{
			kind: GitRefWorktree,
			ref: GitRef{
				Kind: GitRefWorktree, Name: sanitize.Line(w.Name()), Rev: rev,
				Index: -1, Path: w.Path,
			},
			detail: m.where(w.Path),
			style:  muted,
		}
		switch {
		case w.Prunable:
			row.detail, row.style = "gone", danger
		case w.Locked:
			row.detail, row.style = "locked", warning
		case w.Detached:
			row.detail, row.style = "detached", warning
		}
		rows = append(rows, row)
	}
	return rows
}

// where is what a worktree row says of its directory: nothing for a worktree
// of the project's own worktree directory, where the name is the directory,
// "project" for the project itself, and the path for anywhere else.
func (m *GitRefsModel) where(path string) string {
	clean := sanitize.Line(path)
	if root := strings.TrimSuffix(m.opts.Root, "/"); root != "" {
		if clean == root {
			return "project"
		}
		if rest, ok := strings.CutPrefix(clean, root+"/"); ok {
			if strings.HasPrefix(rest, vcs.WorktreeDir+"/") {
				return ""
			}
			return rest
		}
	}
	return shortPath(clean, m.opts.Home)
}

func (m *GitRefsModel) stashRows() []gitRefRow {
	rows := make([]gitRefRow, 0, len(m.opts.Refs.Stashes))
	for _, st := range m.opts.Refs.Stashes {
		rows = append(rows, gitRefRow{
			kind: GitRefStash,
			ref: GitRef{
				Kind: GitRefStash, Name: sanitize.Line(st.Ref), Rev: st.OID, Index: st.Index,
			},
			detail: m.age(st.Created),
			style:  muted,
		})
	}
	return rows
}

func (m *GitRefsModel) tagRows() []gitRefRow {
	rows := make([]gitRefRow, 0, len(m.opts.Refs.Tags))
	for _, t := range m.opts.Refs.Tags {
		row := gitRefRow{
			kind:   GitRefTag,
			ref:    GitRef{Kind: GitRefTag, Name: sanitize.Line(t.Name), Rev: t.Ref, Index: -1},
			detail: m.age(t.Created),
			style:  muted,
		}
		if t.Annotated {
			row.detail, row.style = "annotated", accent2
		}
		rows = append(rows, row)
	}
	return rows
}

// age is how long ago something was made, empty for a date git did not give.
func (m *GitRefsModel) age(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return formatAge(m.now().Sub(at))
}

// The detail styles, as functions so a row carries its color without carrying
// the styles it is drawn with.
func muted(s Styles) lipgloss.Style   { return s.Muted }
func danger(s Styles) lipgloss.Style  { return s.Danger }
func warning(s Styles) lipgloss.Style { return s.Warning }
func accent2(s Styles) lipgloss.Style { return s.Accent2 }

// Init has nothing to wait for: the pane draws what it is given.
func (m *GitRefsModel) Init() tea.Cmd { return nil }

// Update handles messages.
func (m *GitRefsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width, 1), max(msg.Height, 1)
		m.list.clamp(len(m.rows), m.bodyHeight())
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.MouseClickMsg:
		return m, m.click(msg)
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.move(-1, len(m.rows), m.bodyHeight())
		case tea.MouseWheelDown:
			m.list.move(1, len(m.rows), m.bodyHeight())
		}
	}
	return m, nil
}

func (m *GitRefsModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return tea.Quit
	}
	if m.filtering {
		return m.filterKey(msg)
	}
	if d, ok := m.nav.delta(msg, len(m.rows), m.bodyHeight()); ok {
		m.list.move(d, len(m.rows), m.bodyHeight())
		return nil
	}
	switch {
	case key.Matches(msg, m.filterK):
		m.filtering = true
		return nil
	case key.Matches(msg, m.foldK):
		m.fold()
		return nil
	case key.Matches(msg, m.chooseK):
		return m.choose()
	case m.opts.Popup && key.Matches(msg, m.quitK):
		return tea.Quit
	case msg.String() == "esc" && m.filter.value != "":
		m.filter.value = ""
		m.build()
		return nil
	}
	return nil
}

// filterKey handles the keys of the filter line: esc gives up what was typed,
// enter keeps it and leaves the line.
func (m *GitRefsModel) filterKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filtering, m.filter.value = false, ""
		m.build()
		return nil
	case "enter":
		m.filtering = false
		return nil
	}
	if m.filter.update(msg) {
		m.build()
	}
	return nil
}

// fold closes or opens the section the cursor is in, from its heading as well
// as from any row under it.
func (m *GitRefsModel) fold() {
	row, ok := m.row()
	if !ok {
		return
	}
	m.folded[row.kind] = !m.folded[row.kind]
	m.build()
	// A folded section leaves the cursor on its heading rather than on
	// whatever row slid up into its place.
	for i, r := range m.rows {
		if r.head && r.kind == row.kind {
			m.list.move(i-m.list.cursor, len(m.rows), m.bodyHeight())
			return
		}
	}
}

// choose folds a section from its heading and reports a ref from any other
// row.
func (m *GitRefsModel) choose() tea.Cmd {
	row, ok := m.row()
	if !ok {
		return nil
	}
	if row.head {
		m.fold()
		return nil
	}
	ref := row.ref
	return func() tea.Msg { return GitRefChosenMsg{Ref: ref} }
}

// click moves the cursor to the row under the pointer; a double click on the
// same row chooses it.
func (m *GitRefsModel) click(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	// The first body row is drawn under the header.
	row := msg.Y - 1
	if row < 0 || row >= m.bodyHeight() {
		return nil
	}
	idx := m.list.offset + row
	if idx >= len(m.rows) {
		return nil
	}
	double := m.clicks.click(m.now(), idx)
	m.list.move(idx-m.list.cursor, len(m.rows), m.bodyHeight())
	if double {
		return m.choose()
	}
	return nil
}

func (m *GitRefsModel) bodyHeight() int { return max(m.height-2, 1) }

// View draws the pane.
func (m *GitRefsModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *GitRefsModel) render() string {
	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines[:min(len(lines), max(m.height-1, 0))], m.footer())
	return screen(lines, m.width, m.height)
}

func (m *GitRefsModel) header() string {
	s := m.opts.Styles
	left := []segment{seg(" "+s.Theme.Icons.Branch+" refs ", s.Title)}
	var right []segment
	if n := m.count() - int(gitRefSections); n > 0 {
		right = append(right, seg(strconv.Itoa(n)+" ", s.Muted))
	}
	return s.bar(m.width, left, right)
}

func (m *GitRefsModel) body() []string {
	s := m.opts.Styles
	h := m.bodyHeight()
	if len(m.rows) == 0 {
		return []string{"", s.Muted.Render(center("no ref matches", m.width, s.Ellipsis))}
	}
	end := min(m.list.offset+h, len(m.rows))
	lines := make([]string, 0, h)
	for i, row := range m.rows[m.list.offset:end] {
		lines = append(lines, m.line(row, m.list.offset+i == m.list.cursor))
	}
	return lines
}

// line draws one row: a heading with its fold mark and its count, or a ref
// with what it owes on the right. The first column is the cursor, so a frame
// says where it is without its colors.
func (m *GitRefsModel) line(row gitRefRow, selected bool) string {
	s := m.opts.Styles
	ascii := s.Theme.Icons.Name == "ascii"
	var bg *lipgloss.Style
	cursor := " "
	if selected {
		bg = &s.Selected
		if cursor = "▌"; ascii {
			cursor = ">"
		}
	}
	if row.head {
		mark := "▾ "
		if m.folded[row.kind] {
			mark = "▸ "
		}
		if ascii {
			mark = "v "
			if m.folded[row.kind] {
				mark = "+ "
			}
		}
		width := max(m.width-ansi.StringWidth(cursor+mark+row.detail)-1, 1)
		return s.line(m.width, bg,
			seg(cursor, s.Accent), seg(mark, s.Border),
			seg(fit(row.kind.String(), width, s.Ellipsis), s.Title),
			seg(row.detail, s.Muted))
	}
	icon := " "
	nameStyle := s.Text
	if row.marked {
		icon = s.Theme.Icons.Busy
		nameStyle = s.Accent
	}
	// The name is what the row is looked up by, so the detail never takes
	// more than half the pane away from it.
	detail := truncate(row.detail, max(m.width/2, 6), s.Ellipsis)
	detailStyle := s.Muted
	if row.style != nil {
		detailStyle = row.style(s)
	}
	width := max(m.width-ansi.StringWidth(cursor+icon+detail)-2, 1)
	return s.line(m.width, bg,
		seg(cursor+icon+" ", s.Accent),
		seg(fit(row.ref.Name, width, s.Ellipsis), nameStyle),
		seg(" "+detail, detailStyle))
}

func (m *GitRefsModel) footer() string {
	s := m.opts.Styles
	if m.filtering || m.filter.value != "" {
		cursor := ""
		if m.filtering {
			cursor = "_"
		}
		return s.line(m.width, nil,
			seg(" /", s.Key),
			seg(truncate(m.filter.value, max(m.width-3, 1), s.Ellipsis), s.Text),
			seg(cursor, s.Accent))
	}
	return s.helpLine(m.width, m.helpKeys()...)
}

// helpKeys are the keys the footer offers: movement when there is more than
// one row, choosing when the cursor is on a ref, and closing only in a popup.
func (m *GitRefsModel) helpKeys() []key.Binding {
	var keys []key.Binding
	if len(m.rows) > 1 {
		keys = append(keys, m.nav.Down)
	}
	if _, ok := m.Selected(); ok {
		keys = append(keys, m.chooseK)
	}
	keys = append(keys, m.foldK, m.filterK)
	if m.opts.Popup {
		keys = append(keys, m.quitK)
	}
	return keys
}
