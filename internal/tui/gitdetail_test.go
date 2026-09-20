package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// detailSizes are the frame sizes of the detail pane: the column it opens at
// beside the history, and a narrow one.
var detailSizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "48x24", width: 48, height: 24, ansi: true},
	{name: "30x14", width: 30, height: 14},
}

// sampleDetail is a commit read in full: a body, a committer of its own, two
// parents, refs, and files of every kind.
func sampleDetail() vcs.CommitDetail {
	return vcs.CommitDetail{
		Commit: vcs.Commit{
			OID:       graphOID("a"),
			Parents:   []string{graphOID("b"), graphOID("d")},
			Author:    "Ada Lovelace",
			Authored:  fixedNow.Add(-26 * time.Hour),
			Committed: fixedNow.Add(-time.Hour),
			Refs: []vcs.Ref{
				{Name: "main", Full: "refs/heads/main", Kind: vcs.RefBranch, Head: true},
				{Name: "v1.2.0", Full: "refs/tags/v1.2.0", Kind: vcs.RefTag},
			},
			Subject: "merge the git workstation into the release",
		},
		AuthorEmail: "ada@example.invalid",
		Committer:   "Grace Hopper", CommitterEmail: "grace@example.invalid",
		Body: "The history, the refs and the detail of a commit,\nwith every operation reachable from the keys.",
		Files: []vcs.NumStat{
			{Path: "internal/tui/gitgraph.go", Added: 420, Deleted: 12},
			{Path: "docs/git.md", Added: 88},
			{Path: "internal/tui/testdata/gitgraph/logo.png", Binary: true},
			{Path: "internal/tui/gitrefs.go", OrigPath: "internal/tui/refs.go", Added: 4, Deleted: 4},
		},
	}
}

func newTestDetail(t *testing.T, width, height int) *GitDetailModel {
	t.Helper()
	m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: width, Height: height, Now: clock})
	m.ShowCommit(sampleDetail())
	return m
}

func detailFrame(m *GitDetailModel) string { return ansi.Strip(m.View().Content) }

// detailKeys sends keys and returns the messages the commands produced.
func detailKeys(m *GitDetailModel, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(press(k))
		out = append(out, collect(cmd)...)
	}
	return out
}

func TestGitDetailFrames(t *testing.T) {
	t.Parallel()
	long := sampleDetail()
	long.Body = strings.Repeat("a message that goes on and on, ", 20)
	many := sampleDetail()
	for i := range 30 {
		many.Files = append(many.Files, vcs.NumStat{Path: "internal/gen/file" + strconv.Itoa(i) + ".go", Added: i})
	}
	states := []struct {
		name string
		show func(m *GitDetailModel)
		msgs []tea.Msg
	}{
		{name: "nothing", show: func(*GitDetailModel) {}},
		{name: "reading", show: func(m *GitDetailModel) { m.Reading() }},
		{name: "failed", show: func(m *GitDetailModel) { m.ShowError(errors.New("git show: exit status 128: fatal: bad object")) }},
		{name: "commit", show: func(m *GitDetailModel) { m.ShowCommit(sampleDetail()) }},
		{name: "commit-selected", show: func(m *GitDetailModel) { m.ShowCommit(sampleDetail()) }, msgs: []tea.Msg{press("j"), press("j")}},
		{name: "commit-long", show: func(m *GitDetailModel) { m.ShowCommit(long) }},
		{name: "commit-many", show: func(m *GitDetailModel) { m.ShowCommit(many) }, msgs: []tea.Msg{press("G")}},
		{
			name: "commit-plain",
			show: func(m *GitDetailModel) {
				d := vcs.CommitDetail{
					Commit: vcs.Commit{
						OID: graphOID("c"), Parents: []string{graphOID("d")}, Author: "Ada Lovelace",
						Authored: fixedNow.Add(-time.Hour), Committed: fixedNow.Add(-time.Hour),
						Subject: "one line and nothing else",
					},
					AuthorEmail: "ada@example.invalid", Committer: "Ada Lovelace", CommitterEmail: "ada@example.invalid",
					Files: []vcs.NumStat{{Path: "a.txt", Added: 1}},
				}
				m.ShowCommit(d)
			},
		},
		{name: "changes", show: func(m *GitDetailModel) { m.ShowChanges(sampleChanges()) }},
		{
			name: "changes-clean",
			show: func(m *GitDetailModel) { m.ShowChanges(vcs.Changes{Head: vcs.Head{Name: "main"}}) },
		},
		{name: "changes-selected", show: func(m *GitDetailModel) { m.ShowChanges(sampleChanges()) }, msgs: []tea.Msg{press("j")}},
	}
	for _, size := range detailSizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				m := NewGitDetail(GitDetailOptions{
					Styles: goldenStyles(t), Width: size.width, Height: size.height, Now: clock,
				})
				st.show(m)
				apply(m, st.msgs...)
				assertFrame(t, "gitdetail/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestGitDetailDrawsTheCommit(t *testing.T) {
	t.Parallel()
	m := newTestDetail(t, 60, 40)
	frame := detailFrame(m)
	for _, want := range []string{
		"merge the git workstation into the release",
		"every operation reachable from the keys",
		"Ada Lovelace <ada@example.invalid>",
		"Grace Hopper <grace@example.invalid>",
		"parents", "b000000 d000000",
		"main", "v1.2.0",
		"4 files", "+512", "-16",
		"internal/tui/gitgraph.go", "+420", "bin",
		"internal/tui/refs.go → internal/tui/gitrefs.go",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame lacks %q:\n%s", want, frame)
		}
	}
}

// TestGitDetailSaysWhoLandedACommitOnlyWhenItDiffers keeps the pane quiet
// about the committer when it is the author at the same moment, which is
// every commit that was not rebased, amended or applied from a patch.
func TestGitDetailSaysWhoLandedACommitOnlyWhenItDiffers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		committer string
		at        time.Time
		want      bool
	}{
		{name: "the author, at the same moment", committer: "Ada Lovelace", at: fixedNow.Add(-time.Hour)},
		{name: "someone else", committer: "Grace Hopper", at: fixedNow.Add(-time.Hour), want: true},
		{name: "the author, later", committer: "Ada Lovelace", at: fixedNow, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := vcs.CommitDetail{
				Commit: vcs.Commit{
					OID: graphOID("a"), Author: "Ada Lovelace",
					Authored: fixedNow.Add(-time.Hour), Committed: tc.at, Subject: "a commit",
				},
				Committer: tc.committer,
			}
			m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 20, Now: clock})
			m.ShowCommit(d)
			if got := strings.Contains(detailFrame(m), "landed by"); got != tc.want {
				t.Fatalf("the frame says who landed it = %v, want %v:\n%s", got, tc.want, detailFrame(m))
			}
		})
	}
}

func TestGitDetailDrawsTheWorkingTree(t *testing.T) {
	t.Parallel()
	m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 40, Now: clock})
	m.ShowChanges(sampleChanges())
	frame := detailFrame(m)
	for _, want := range []string{"working tree", "STAGED", "UNSTAGED", "api/handler.go", "notes/"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame lacks %q:\n%s", want, frame)
		}
	}
	// A file that is staged and changed again since is in both lists.
	var staged, unstaged int
	for _, l := range strings.Split(frame, "\n") {
		if strings.Contains(l, "docs/guide.md") {
			staged++
		}
		if strings.Contains(l, "internal/merge.go") {
			unstaged++
		}
	}
	if staged != 2 {
		t.Fatalf("the file staged and changed again is drawn %d times, want twice:\n%s", staged, frame)
	}
	if unstaged == 0 {
		t.Fatalf("the unmerged file is drawn nowhere:\n%s", frame)
	}
}

func TestGitDetailOpensAFile(t *testing.T) {
	t.Parallel()
	t.Run("a file of a commit carries the commit", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 40)
		msgs := detailKeys(m, "enter")
		if len(msgs) != 1 {
			t.Fatalf("enter reported %v, want the file", msgs)
		}
		file, ok := msgs[0].(GitFileChosenMsg)
		if !ok || file.Path != "internal/tui/gitgraph.go" || file.Rev != graphOID("a") {
			t.Fatalf("enter reported %+v, want the first file of the commit", msgs[0])
		}
	})
	t.Run("a file of the working tree carries no commit", func(t *testing.T) {
		t.Parallel()
		m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 40, Now: clock})
		m.ShowChanges(sampleChanges())
		msgs := detailKeys(m, "enter")
		file, ok := msgs[0].(GitFileChosenMsg)
		if !ok || file.Rev != "" || file.Path == "" {
			t.Fatalf("enter reported %+v, want a file of the working tree", msgs[0])
		}
	})
	t.Run("a pane with no file reports nothing", func(t *testing.T) {
		t.Parallel()
		m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 40, Now: clock})
		if msgs := detailKeys(m, "enter", "j", "enter"); len(msgs) != 0 {
			t.Fatalf("a pane showing nothing reported %v", msgs)
		}
	})
}

func TestGitDetailMovesOverTheFiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{name: "the first file is the one it opens on", want: "internal/tui/gitgraph.go"},
		{name: "one down", keys: []string{"j"}, want: "docs/git.md"},
		{name: "to the bottom", keys: []string{"G"}, want: "internal/tui/gitrefs.go"},
		{name: "past the bottom stops there", keys: []string{"G", "j", "j"}, want: "internal/tui/gitrefs.go"},
		{name: "past the top stops there", keys: []string{"k", "k"}, want: "internal/tui/gitgraph.go"},
		{name: "back to the top", keys: []string{"G", "g"}, want: "internal/tui/gitgraph.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestDetail(t, 60, 12)
			detailKeys(m, tc.keys...)
			got, ok := m.Selected()
			if !ok || got.Path != tc.want {
				t.Fatalf("Selected() = %+v %v, want %q", got, ok, tc.want)
			}
			// Whatever the keys did, the file the cursor is on was brought
			// into view. A pane that has not been walked yet opens on the
			// message instead, so there is nothing to look for.
			if len(tc.keys) > 0 && !strings.Contains(detailFrame(m), tc.want) {
				t.Fatalf("the file the cursor is on is off screen:\n%s", detailFrame(m))
			}
		})
	}
}

func TestGitDetailShowingSomethingElseStartsAtTheTop(t *testing.T) {
	t.Parallel()
	m := newTestDetail(t, 60, 12)
	detailKeys(m, "G")
	if got, _ := m.Selected(); got.Path != "internal/tui/gitrefs.go" {
		t.Fatalf("Selected() = %+v, want the last file", got)
	}
	m.ShowCommit(sampleDetail())
	got, ok := m.Selected()
	if !ok || got.Path != "internal/tui/gitgraph.go" {
		t.Fatalf("Selected() = %+v %v after another commit, want the first file", got, ok)
	}
	m.ShowChanges(sampleChanges())
	if got, ok := m.Selected(); !ok || got.Rev != "" {
		t.Fatalf("Selected() = %+v %v after the working tree, want a file of it", got, ok)
	}
	m.Reading()
	if _, ok := m.Selected(); ok {
		t.Fatal("a pane that is reading has a file selected")
	}
}

func TestGitDetailMouse(t *testing.T) {
	t.Parallel()
	click := func(y int) tea.MouseClickMsg { return tea.MouseClickMsg{Button: tea.MouseLeft, X: 3, Y: y} }
	fileRow := func(m *GitDetailModel, path string) int {
		for i, l := range strings.Split(detailFrame(m), "\n") {
			if strings.Contains(l, path) {
				return i
			}
		}
		return -1
	}
	t.Run("a click puts the cursor on the file", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 40)
		row := fileRow(m, "docs/git.md")
		m.Update(click(row))
		got, ok := m.Selected()
		if !ok || got.Path != "docs/git.md" {
			t.Fatalf("Selected() = %+v %v, want the file that was clicked (row %d)", got, ok, row)
		}
	})
	t.Run("a click on a line that is no file changes nothing", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 40)
		detailKeys(m, "j", "j")
		before, _ := m.Selected()
		// Row 1 is the subject of the commit, which stands for no file.
		m.Update(click(1))
		after, _ := m.Selected()
		if before != after {
			t.Fatalf("Selected() = %+v, want it left at %+v", after, before)
		}
	})
	t.Run("a double click opens the diff", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 40)
		row := fileRow(m, "docs/git.md")
		m.Update(click(row))
		_, cmd := m.Update(click(row))
		msgs := collect(cmd)
		if len(msgs) != 1 {
			t.Fatalf("the double click reported %v", msgs)
		}
		if file, ok := msgs[0].(GitFileChosenMsg); !ok || file.Path != "docs/git.md" {
			t.Fatalf("the double click reported %+v", msgs[0])
		}
	})
	t.Run("a click with another button changes nothing", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 40)
		row := fileRow(m, "docs/git.md")
		before, _ := m.Selected()
		m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 3, Y: row})
		after, _ := m.Selected()
		if before != after {
			t.Fatalf("Selected() = %+v, want it left at %+v", after, before)
		}
	})
	t.Run("a click under the last line changes nothing", func(t *testing.T) {
		t.Parallel()
		// A pane of eight lines draws six of the working tree, and the files
		// under those six are reached by scrolling, never by clicking where
		// they are not.
		m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 8, Now: clock})
		m.ShowChanges(sampleChanges())
		before, _ := m.Selected()
		for _, y := range []int{8, 9, 40} {
			m.Update(click(y))
			after, _ := m.Selected()
			if before != after {
				t.Fatalf("a click on row %d moved the cursor to %+v, want it left at %+v", y, after, before)
			}
		}
	})
	t.Run("the wheel scrolls without moving the cursor", func(t *testing.T) {
		t.Parallel()
		m := newTestDetail(t, 60, 8)
		before, _ := m.Selected()
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		after, _ := m.Selected()
		if before != after {
			t.Fatalf("the wheel moved the cursor to %+v, want it left at %+v", after, before)
		}
		if m.top == 0 {
			t.Fatal("the wheel scrolled nothing")
		}
		for range 10 {
			m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		}
		if m.top != 0 {
			t.Fatalf("the wheel scrolled past the top, to line %d", m.top)
		}
	})
}

// TestGitDetailCapsTheBody keeps a message that is really a file out of the
// pane: the beginning of it is drawn and the rest is not.
func TestGitDetailCapsTheBody(t *testing.T) {
	t.Parallel()
	d := sampleDetail()
	d.Body = strings.Repeat("body ", 400) + "THEEND"
	m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 60, Height: 200, Now: clock})
	m.ShowCommit(d)
	frame := detailFrame(m)
	if strings.Contains(frame, "THEEND") {
		t.Fatalf("the whole message was drawn:\n%s", frame)
	}
	body := strings.Count(frame, "body ")
	if body == 0 || body > maxDetailBody/len("body ") {
		t.Fatalf("the message was drawn %d times over, want at most %d", body, maxDetailBody/len("body "))
	}
	// Whatever the message, the files are still reachable under it.
	if !strings.Contains(frame, "internal/tui/gitgraph.go") {
		t.Fatalf("the message pushed the files out of the pane:\n%s", frame)
	}
}

func TestGitDetailQuitKeys(t *testing.T) {
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
			m := NewGitDetail(GitDetailOptions{
				Styles: goldenStyles(t), Popup: tc.popup, Width: 48, Height: 24, Now: clock,
			})
			m.ShowCommit(sampleDetail())
			var quit bool
			for _, msg := range detailKeys(m, tc.keys...) {
				if _, ok := msg.(tea.QuitMsg); ok {
					quit = true
				}
			}
			if quit != tc.want {
				t.Fatalf("the pane quit = %v, want %v", quit, tc.want)
			}
		})
	}
}

func TestGitDetailResizes(t *testing.T) {
	t.Parallel()
	m := newTestDetail(t, 48, 24)
	detailKeys(m, "G")
	for _, size := range []struct{ w, h int }{{20, 5}, {80, 40}, {1, 1}, {48, 24}} {
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

func TestGitDetailASCII(t *testing.T) {
	t.Parallel()
	m := NewGitDetail(GitDetailOptions{
		Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Width: 60, Height: 30, Now: clock,
	})
	m.ShowCommit(sampleDetail())
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"@ commit", "internal/tui/refs.go -> internal/tui/gitrefs.go"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the ascii frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.ContainsFunc(frame, func(r rune) bool { return r > 0x7e }) {
		t.Errorf("the ascii frame draws glyphs that are not ascii:\n%s", frame)
	}
}

// TestGitDetailDrawsNothingItWasGiven proves a message, an address or a path
// out of a repository is drawn as text and nothing else.
func TestGitDetailDrawsNothingItWasGiven(t *testing.T) {
	t.Parallel()
	d := vcs.CommitDetail{
		Commit: vcs.Commit{
			OID: graphOID("a"), Author: "red\x1b[31mauthor", Committed: fixedNow,
			Subject: "a subject\rwith a carriage return",
			Refs:    []vcs.Ref{{Name: "a\x1b[0mbranch", Full: "refs/heads/a", Kind: vcs.RefBranch}},
		},
		AuthorEmail: "a\x07b@example.invalid",
		Body:        strings.Repeat("a body that goes on. ", 60) + "\x1b[2J",
		Files: []vcs.NumStat{
			{Path: "dir\x1b[31m/file.go", Added: 1},
			{Path: strings.Repeat("deep/", 40) + "file.go", Added: 2},
		},
	}
	m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 48, Height: 20, Now: clock})
	m.ShowCommit(d)
	content := m.View().Content
	for i, l := range strings.Split(content, "\n") {
		if strings.ContainsAny(ansi.Strip(l), "\x1b\a\r\t") {
			t.Fatalf("line %d carries control characters: %q", i+1, l)
		}
		if w := ansi.StringWidth(l); w > 48 {
			t.Fatalf("line %d is %d cells wide, over the pane", i+1, w)
		}
	}
	if strings.Contains(ansi.Strip(content), "[2J") {
		t.Fatalf("an escape sequence reached the screen:\n%s", ansi.Strip(content))
	}
}
