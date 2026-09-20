package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// refsSizes are the frame sizes of the refs pane: the width it opens at in a
// workspace, and the narrowest it is allowed to be.
var refsSizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "28x24", width: 28, height: 24, ansi: true},
	{name: "20x12", width: 20, height: 12},
}

const refsOID = "1c2a5f3b9d7e4a6c8b0d2f4a6c8e0b2d4f6a8c0e"

func sampleGitRefs() GitRefs {
	ago := func(d time.Duration) time.Time { return fixedNow.Add(-d) }
	return GitRefs{
		Branches: []vcs.LocalBranch{
			{Name: "main", Ref: "refs/heads/main", OID: refsOID, Head: true, Upstream: "refs/remotes/origin/main", Tip: ago(time.Hour)},
			{Name: "feat/git-workstation", Ref: "refs/heads/feat/git-workstation", OID: refsOID, Upstream: "refs/remotes/origin/feat/git-workstation", Ahead: 12, Behind: 1, Tip: ago(3 * time.Hour)},
			{Name: "fix/border", Ref: "refs/heads/fix/border", OID: refsOID, Upstream: "refs/remotes/origin/fix/border", Gone: true, Tip: ago(72 * time.Hour)},
			{Name: "spike", Ref: "refs/heads/spike", OID: refsOID, Tip: ago(240 * time.Hour)},
		},
		Remotes: []vcs.RemoteBranch{
			{Name: "origin/HEAD", Ref: "refs/remotes/origin/HEAD", Remote: "origin", OID: refsOID, Target: "refs/remotes/origin/main"},
			{Name: "origin/main", Ref: "refs/remotes/origin/main", Remote: "origin", OID: refsOID, Tip: ago(time.Hour)},
			{Name: "origin/feat/git-workstation", Ref: "refs/remotes/origin/feat/git-workstation", Remote: "origin", OID: refsOID, Tip: ago(4 * time.Hour)},
		},
		Worktrees: []vcs.Worktree{
			{Path: testHome + "/src/api", Head: refsOID, Branch: "refs/heads/main"},
			{Path: testHome + "/src/api/.claude/worktrees/task-a", Head: refsOID, Branch: "refs/heads/task-a"},
			{Path: testHome + "/src/api/.claude/worktrees/spike", Head: refsOID, Detached: true},
			{Path: testHome + "/src/gone", Head: refsOID, Branch: "refs/heads/gone", Prunable: true, PruneReason: "gitdir file points to non-existent location"},
		},
		Stashes: []vcs.Stash{
			{Index: 0, Ref: "stash@{0}", OID: refsOID, Base: refsOID, Created: ago(20 * time.Minute), Branch: "main", Message: "the border of the graph", WIP: true},
			{Index: 1, Ref: "stash@{1}", OID: refsOID, Base: refsOID, Created: ago(50 * time.Hour), Branch: "main", Message: "half a rebase", Untracked: true},
		},
		Tags: []vcs.Tag{
			{Name: "v1.1.0", Ref: "refs/tags/v1.1.0", OID: refsOID, Commit: refsOID, Annotated: true, Created: ago(100 * time.Hour), Subject: "the agents workstation"},
			{Name: "v1.0.1", Ref: "refs/tags/v1.0.1", OID: refsOID, Commit: refsOID, Created: ago(400 * time.Hour), Subject: "a fix"},
		},
	}
}

// newTestRefs builds a pane over the sample refs at a size.
func newTestRefs(t *testing.T, width, height int) *GitRefsModel {
	t.Helper()
	return NewGitRefs(GitRefsOptions{
		Styles: goldenStyles(t), Refs: sampleGitRefs(), Home: testHome, Root: testHome + "/src/api",
		Width: width, Height: height, Now: clock,
	})
}

// refsKeys sends keys and returns the last message the commands produced,
// which is how the pane reports the ref that was chosen.
func refsKeys(m *GitRefsModel, keys ...string) tea.Msg {
	var last tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(press(k))
		if cmd == nil {
			continue
		}
		if msg := cmd(); msg != nil {
			last = msg
		}
	}
	return last
}

// refsLines is the frame with its colors and its padding taken off.
func refsLines(m *GitRefsModel) []string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

func TestGitRefsFrames(t *testing.T) {
	t.Parallel()
	many := sampleGitRefs()
	for i := range 40 {
		many.Branches = append(many.Branches, vcs.LocalBranch{
			Name: "gen/branch-" + strconv.Itoa(i), Ref: "refs/heads/gen/branch-" + strconv.Itoa(i), OID: refsOID,
		})
	}
	states := []struct {
		name  string
		refs  *GitRefs
		popup bool
		msgs  []tea.Msg
	}{
		{name: "empty", refs: &GitRefs{}},
		{name: "populated"},
		{name: "selected", msgs: []tea.Msg{press("down"), press("down")}},
		{name: "folded", msgs: []tea.Msg{press("space")}},
		{name: "folded-all", msgs: []tea.Msg{press("enter"), press("down"), press("enter"), press("down"), press("enter"), press("down"), press("enter"), press("down"), press("enter")}},
		{name: "filtering", msgs: []tea.Msg{press("/"), press("o"), press("r")}},
		{name: "filtered", msgs: []tea.Msg{press("/"), press("m"), press("a"), press("i"), press("n"), press("enter")}},
		{name: "scrolled", refs: &many, msgs: []tea.Msg{press("pgdown"), press("j")}},
		{name: "popup", popup: true},
	}
	for _, size := range refsSizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				refs := sampleGitRefs()
				if st.refs != nil {
					refs = *st.refs
				}
				m := NewGitRefs(GitRefsOptions{
					Styles: goldenStyles(t), Refs: refs, Home: testHome, Root: testHome + "/src/api", Popup: st.popup,
					Width: size.width, Height: size.height, Now: clock,
				})
				apply(m, st.msgs...)
				assertFrame(t, "gitrefs/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestGitRefsChoose(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []string
		want GitRef
		// none says the keys report nothing, which is what a heading does.
		none bool
	}{
		{name: "a heading reports nothing", keys: []string{"enter"}, none: true},
		{
			name: "the branch the working tree is on",
			keys: []string{"down", "enter"},
			want: GitRef{Kind: GitRefBranch, Name: "main", Rev: "refs/heads/main", Index: -1},
		},
		{
			name: "a branch further down",
			keys: []string{"down", "down", "enter"},
			want: GitRef{Kind: GitRefBranch, Name: "feat/git-workstation", Rev: "refs/heads/feat/git-workstation", Index: -1},
		},
		{
			name: "a branch of a remote",
			keys: []string{"down", "down", "down", "down", "down", "down", "enter"},
			want: GitRef{Kind: GitRefRemote, Name: "origin/HEAD", Rev: "refs/remotes/origin/HEAD", Index: -1},
		},
		{
			name: "a stash, which carries its entry",
			keys: []string{"G", "up", "up", "up", "enter"},
			want: GitRef{Kind: GitRefStash, Name: "stash@{1}", Rev: refsOID, Index: 1},
		},
		{
			name: "a tag",
			keys: []string{"G", "enter"},
			want: GitRef{Kind: GitRefTag, Name: "v1.0.1", Rev: "refs/tags/v1.0.1", Index: -1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestRefs(t, 28, 24)
			msg := refsKeys(m, tc.keys...)
			chosen, ok := msg.(GitRefChosenMsg)
			if tc.none {
				if ok {
					t.Fatalf("the pane reported %+v, want nothing", chosen.Ref)
				}
				return
			}
			if !ok {
				t.Fatalf("the pane reported %T, want a ref", msg)
			}
			if chosen.Ref != tc.want {
				t.Fatalf("the pane reported %+v, want %+v", chosen.Ref, tc.want)
			}
			// What the keys chose is what the pane says is selected.
			sel, ok := m.Selected()
			if !ok || sel != tc.want {
				t.Fatalf("Selected() = %+v %v, want %+v", sel, ok, tc.want)
			}
		})
	}
}

func TestGitRefsWorktreeRowsCarryTheirDirectory(t *testing.T) {
	t.Parallel()
	m := newTestRefs(t, 40, 40)
	var found []GitRef
	for i := range 40 {
		if i > 0 {
			refsKeys(m, "down")
		}
		if ref, ok := m.Selected(); ok && ref.Kind == GitRefWorktree {
			found = append(found, ref)
		}
	}
	if len(found) != 4 {
		t.Fatalf("the pane lists %d worktrees, want the four of the fixture", len(found))
	}
	for _, ref := range found {
		if ref.Path == "" {
			t.Fatalf("worktree row %+v carries no directory", ref)
		}
	}
	if found[2].Rev != refsOID {
		t.Fatalf("the detached worktree is read from %q, want the commit it is on", found[2].Rev)
	}
}

func TestGitRefsFold(t *testing.T) {
	t.Parallel()
	// fix/border is a branch of the project alone, so it says whether the
	// LOCAL section is drawn without matching a branch of a remote.
	m := newTestRefs(t, 44, 24)
	if !strings.Contains(strings.Join(refsLines(m), "\n"), "fix/border") {
		t.Fatalf("the pane opens without its branches:\n%s", strings.Join(refsLines(m), "\n"))
	}
	refsKeys(m, "space")
	folded := strings.Join(refsLines(m), "\n")
	if strings.Contains(folded, "fix/border") {
		t.Fatalf("the folded section still draws its branches:\n%s", folded)
	}
	if !strings.Contains(folded, "LOCAL") || !strings.Contains(folded, "REMOTE") {
		t.Fatalf("folding took a heading away:\n%s", folded)
	}
	// The cursor stays on the heading of the section it folded, so the same
	// key opens it again.
	refsKeys(m, "space")
	if got := strings.Join(refsLines(m), "\n"); !strings.Contains(got, "fix/border") {
		t.Fatalf("the section did not open again:\n%s", got)
	}
	// Folding from a row inside the section folds that section too.
	refsKeys(m, "down", "down", "space")
	if got := strings.Join(refsLines(m), "\n"); strings.Contains(got, "fix/border") {
		t.Fatalf("folding from inside the section left it open:\n%s", got)
	}
	if _, ok := m.Selected(); ok {
		t.Fatal("the cursor stayed on a row of the folded section")
	}
}

func TestGitRefsFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		keys  []string
		want  []string
		empty bool
	}{
		{name: "nothing typed yet", keys: []string{"/"}, want: []string{"LOCAL", "main", "REMOTE", "STASHES", "TAGS"}},
		{name: "a branch by part of its name", keys: []string{"/", "b", "o", "r"}, want: []string{"LOCAL", "fix/border"}},
		{name: "the same letters in any case", keys: []string{"/", "M", "A", "I", "N"}, want: []string{"LOCAL", "main", "REMOTE", "origin/main"}},
		{name: "a stash by its ref", keys: []string{"/", "s", "t", "a", "s", "h", "@"}, want: []string{"STASHES", "stash@{0}", "stash@{1}"}},
		{name: "nothing at all", keys: []string{"/", "z", "z", "z"}, empty: true},
		{name: "what was typed taken back", keys: []string{"/", "z", "z", "z", "ctrl+u"}, want: []string{"LOCAL", "main", "TAGS"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestRefs(t, 28, 30)
			refsKeys(m, tc.keys...)
			frame := strings.Join(refsLines(m), "\n")
			if tc.empty {
				if !strings.Contains(frame, "no ref matches") {
					t.Fatalf("the pane kept rows for a filter that matches nothing:\n%s", frame)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(frame, want) {
					t.Fatalf("the filtered pane lacks %q:\n%s", want, frame)
				}
			}
		})
	}
}

func TestGitRefsFilterIsGivenUpWithEsc(t *testing.T) {
	t.Parallel()
	m := newTestRefs(t, 28, 30)
	refsKeys(m, "/", "b", "o", "r", "esc")
	frame := strings.Join(refsLines(m), "\n")
	if !strings.Contains(frame, "main") || !strings.Contains(frame, "v1.1.0") {
		t.Fatalf("esc did not give the whole list back:\n%s", frame)
	}
	// A filter kept with enter is taken back by esc outside the filter line.
	refsKeys(m, "/", "b", "o", "r", "enter")
	if got := strings.Join(refsLines(m), "\n"); strings.Contains(got, "v1.1.0") {
		t.Fatalf("enter dropped the filter instead of keeping it:\n%s", got)
	}
	refsKeys(m, "esc")
	if got := strings.Join(refsLines(m), "\n"); !strings.Contains(got, "v1.1.0") {
		t.Fatalf("esc did not clear the filter that was kept:\n%s", got)
	}
}

func TestGitRefsSetRefsKeepsTheCursor(t *testing.T) {
	t.Parallel()
	m := newTestRefs(t, 28, 30)
	refsKeys(m, "down", "down")
	before, ok := m.Selected()
	if !ok || before.Name != "feat/git-workstation" {
		t.Fatalf("Selected() = %+v %v, want the second branch", before, ok)
	}
	// A refresh that adds a branch above the one selected keeps the cursor on
	// the branch, not on the row number it had.
	next := sampleGitRefs()
	next.Branches = append([]vcs.LocalBranch{{
		Name: "aaa-new", Ref: "refs/heads/aaa-new", OID: refsOID,
	}}, next.Branches...)
	m.SetRefs(next)
	after, ok := m.Selected()
	if !ok || after.Name != before.Name {
		t.Fatalf("Selected() = %+v %v after the refresh, want %+v", after, ok, before)
	}
	// A refresh that takes the branch away leaves the cursor where it is
	// rather than on nothing.
	gone := sampleGitRefs()
	gone.Branches = gone.Branches[:1]
	m.SetRefs(gone)
	if _, ok := m.Selected(); !ok {
		t.Fatal("the cursor landed on no row at all after the branch was gone")
	}
}

func TestGitRefsMouse(t *testing.T) {
	t.Parallel()
	click := func(y int) tea.MouseClickMsg {
		return tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: y}
	}
	t.Run("a click puts the cursor on the row", func(t *testing.T) {
		t.Parallel()
		m := newTestRefs(t, 28, 30)
		m.Update(click(3))
		got, ok := m.Selected()
		if !ok || got.Name != "feat/git-workstation" {
			t.Fatalf("Selected() = %+v %v, want the row that was clicked", got, ok)
		}
	})
	t.Run("a double click chooses it", func(t *testing.T) {
		t.Parallel()
		m := newTestRefs(t, 28, 30)
		m.Update(click(2))
		_, cmd := m.Update(click(2))
		if cmd == nil {
			t.Fatal("the second click reported nothing")
		}
		chosen, ok := cmd().(GitRefChosenMsg)
		if !ok || chosen.Ref.Name != "main" {
			t.Fatalf("the double click reported %+v, want the branch it was on", chosen.Ref)
		}
	})
	t.Run("two slow clicks are two clicks", func(t *testing.T) {
		t.Parallel()
		at := fixedNow
		m := NewGitRefs(GitRefsOptions{
			Styles: goldenStyles(t), Refs: sampleGitRefs(), Home: testHome,
			Width: 28, Height: 30, Now: func() time.Time { return at },
		})
		m.Update(click(2))
		at = at.Add(2 * doubleClick)
		if _, cmd := m.Update(click(2)); cmd != nil {
			t.Fatalf("two clicks a second apart reported %+v", cmd())
		}
	})
	t.Run("a click past the last row changes nothing", func(t *testing.T) {
		t.Parallel()
		m := newTestRefs(t, 28, 30)
		before, _ := m.Selected()
		m.Update(click(28))
		after, _ := m.Selected()
		if before != after {
			t.Fatalf("Selected() = %+v, want it left at %+v", after, before)
		}
	})
	t.Run("the wheel scrolls", func(t *testing.T) {
		t.Parallel()
		m := newTestRefs(t, 28, 8)
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		got, ok := m.Selected()
		if !ok || got.Name != "feat/git-workstation" {
			t.Fatalf("Selected() = %+v %v, want the wheel to have moved the cursor", got, ok)
		}
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		if got, _ := m.Selected(); got.Name != "main" {
			t.Fatalf("Selected() = %+v, want the wheel to have moved back", got)
		}
	})
}

func TestGitRefsQuitKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		popup bool
		keys  []string
		want  bool
	}{
		{name: "ctrl+c always closes", keys: []string{"ctrl+c"}, want: true},
		{name: "q closes a popup", popup: true, keys: []string{"q"}, want: true},
		{name: "q does nothing in a pane", keys: []string{"q"}},
		{name: "esc does nothing in a pane", keys: []string{"esc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewGitRefs(GitRefsOptions{
				Styles: goldenStyles(t), Refs: sampleGitRefs(), Popup: tc.popup,
				Width: 28, Height: 24, Now: clock,
			})
			var quit bool
			for _, k := range tc.keys {
				_, cmd := m.Update(press(k))
				if cmd == nil {
					continue
				}
				if _, ok := cmd().(tea.QuitMsg); ok {
					quit = true
				}
			}
			if quit != tc.want {
				t.Fatalf("the pane quit = %v, want %v", quit, tc.want)
			}
		})
	}
}

func TestGitRefsResizes(t *testing.T) {
	t.Parallel()
	m := newTestRefs(t, 28, 30)
	refsKeys(m, "G")
	for _, size := range []struct{ w, h int }{{20, 6}, {60, 40}, {1, 1}, {28, 24}} {
		m.Update(resize(size.w, size.h))
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size.h {
			t.Fatalf("a %dx%d pane drew %d lines", size.w, size.h, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size.w {
				t.Fatalf("a %dx%d pane drew line %d %d cells wide", size.w, size.h, i+1, w)
			}
		}
	}
}

func TestGitRefsASCII(t *testing.T) {
	t.Parallel()
	m := NewGitRefs(GitRefsOptions{
		Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Refs: sampleGitRefs(),
		Home: testHome, Width: 28, Height: 30, Now: clock,
	})
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"@ refs", "v LOCAL", "^12 v1", "gone", "annotated"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the ascii frame lacks %q:\n%s", want, frame)
		}
	}
	apply(m, press("space"))
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "+ LOCAL") {
		t.Errorf("the ascii frame draws no fold mark:\n%s", got)
	}
	if strings.ContainsFunc(frame, func(r rune) bool { return r > 0x7e }) {
		t.Errorf("the ascii frame draws glyphs that are not ascii:\n%s", frame)
	}
}

// TestGitRefsDrawsNothingItWasGiven proves what comes out of a repository is
// display data and nothing else: a branch named with an escape sequence is
// drawn as text, and a name too long for the pane is cut rather than wrapped.
func TestGitRefsDrawsNothingItWasGiven(t *testing.T) {
	t.Parallel()
	refs := GitRefs{
		Branches: []vcs.LocalBranch{
			{Name: "red\x1b[31mtext", Ref: "refs/heads/red", OID: refsOID},
			{Name: "back\rspace", Ref: "refs/heads/back", OID: refsOID},
			{Name: strings.Repeat("long-", 40), Ref: "refs/heads/long", OID: refsOID},
		},
		Tags: []vcs.Tag{{Name: "bell\a", Ref: "refs/tags/bell", OID: refsOID, Commit: refsOID}},
	}
	m := NewGitRefs(GitRefsOptions{Styles: goldenStyles(t), Refs: refs, Width: 28, Height: 20, Now: clock})
	content := m.View().Content
	for i, l := range strings.Split(content, "\n") {
		if strings.ContainsAny(ansi.Strip(l), "\x1b\a\r\t") {
			t.Fatalf("line %d carries control characters: %q", i+1, l)
		}
		if w := ansi.StringWidth(l); w > 28 {
			t.Fatalf("line %d is %d cells wide, over the pane", i+1, w)
		}
	}
	if strings.Contains(ansi.Strip(content), "[31m") {
		t.Fatalf("the escape sequence of a branch name reached the screen:\n%s", ansi.Strip(content))
	}
}
