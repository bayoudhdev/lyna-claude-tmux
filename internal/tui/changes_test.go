package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

func sampleChanges() vcs.Changes {
	files := []vcs.File{
		{Kind: vcs.KindChanged, Path: "api/handler.go", Index: 'M', Worktree: '.', Added: 12, Deleted: 3},
		{Kind: vcs.KindChanged, Path: "api/handler_test.go", Index: '.', Worktree: 'M', Added: 40, Deleted: 0},
		{Kind: vcs.KindRenamed, Path: "docs/guide.md", OrigPath: "docs/old guide.md", Index: 'R', Worktree: 'M', Added: 1, Deleted: 1},
		{Kind: vcs.KindChanged, Path: "go.sum", Index: 'A', Worktree: 'D', Added: 0, Deleted: 0},
		{Kind: vcs.KindUnmerged, Path: "internal/merge.go", Index: 'U', Worktree: 'U', Added: 7, Deleted: 2},
		{Kind: vcs.KindChanged, Path: "logo.png", Index: 'M', Worktree: '.', Binary: true},
		{Kind: vcs.KindUntracked, Path: "notes/", Index: '?', Worktree: '?'},
		{Kind: vcs.KindChanged, Path: "web/src/components/very/deep/directory/structure/Component.test.tsx", Index: '.', Worktree: 'M', Added: 1024, Deleted: 512},
	}
	c := vcs.Changes{
		Head:  vcs.Head{OID: "0123456789abcdef0123456789abcdef01234567", Name: "feature/agents", Upstream: "origin/feature/agents", AheadBehind: true, Ahead: 2, Behind: 1},
		Files: files,
	}
	for _, f := range files {
		c.Added += f.Added
		c.Deleted += f.Deleted
	}
	return c
}

func changesUpdate(c vcs.Changes, err error) changesUpdateMsg {
	return changesUpdateMsg{update: watch.Update{Changes: c, Err: err, At: fixedNow}}
}

// apply delivers messages without running the commands they return (the
// changes view's command waits on its channel).
func apply(m tea.Model, msgs ...tea.Msg) {
	for _, msg := range msgs {
		m.Update(msg)
	}
}

func TestChangesFrames(t *testing.T) {
	t.Parallel()
	notRepo := fmt.Errorf("git rev-parse: %w", git.ErrNotRepository)
	manyFiles := sampleChanges()
	for i := range 40 {
		manyFiles.Files = append(manyFiles.Files, vcs.File{Kind: vcs.KindChanged, Path: "gen/file" + strconv.Itoa(i) + ".go", Index: '.', Worktree: 'M', Added: i})
	}
	states := []struct {
		name  string
		popup bool
		// open gives the view somewhere to open a review into, which the
		// footer offers.
		open bool
		msgs []tea.Msg
	}{
		{name: "loading"},
		{name: "clean", msgs: []tea.Msg{changesUpdate(vcs.Changes{Head: vcs.Head{Name: "main"}}, nil)}},
		{name: "populated", msgs: []tea.Msg{changesUpdate(sampleChanges(), nil)}},
		{name: "popup", popup: true, msgs: []tea.Msg{changesUpdate(sampleChanges(), nil)}},
		{name: "scrolled", msgs: []tea.Msg{changesUpdate(manyFiles, nil), press("pgdown"), press("j")}},
		{name: "initial", msgs: []tea.Msg{changesUpdate(vcs.Changes{
			Head:  vcs.Head{Name: "main", Initial: true},
			Files: []vcs.File{{Kind: vcs.KindChanged, Path: "README.md", Index: 'A', Worktree: '.', Added: 3}},
			Added: 3,
		}, nil)}},
		{name: "detached", msgs: []tea.Msg{changesUpdate(vcs.Changes{Head: vcs.Head{OID: "89abcdef0123", Name: "(detached)", Detached: true}}, nil)}},
		{name: "error", msgs: []tea.Msg{changesUpdate(vcs.Changes{}, errors.New("git status: exit status 128: fatal: index file corrupt"))}},
		{name: "not-repository", msgs: []tea.Msg{changesUpdate(vcs.Changes{}, notRepo)}},
		{name: "stopped", msgs: []tea.Msg{changesUpdate(sampleChanges(), nil), changesClosedMsg{}}},
		{name: "selected", open: true, msgs: []tea.Msg{changesUpdate(sampleChanges(), nil), press("j"), press("j")}},
		{name: "note", open: true, msgs: []tea.Msg{changesUpdate(sampleChanges(), nil), ChangesNote("review: no server running")}},
	}
	for _, size := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				opts := ChangesOptions{
					Styles: goldenStyles(t), Popup: st.popup, Dir: testHome + "/src/api", Home: testHome,
					Width: size.width, Height: size.height, Now: clock,
				}
				if st.open {
					opts.Open = func(string) tea.Cmd { return nil }
					opts.OpenReview = func() tea.Cmd { return nil }
				}
				m := NewChanges(opts)
				apply(m, st.msgs...)
				assertFrame(t, "changes/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestChangesASCII(t *testing.T) {
	t.Parallel()
	m := NewChanges(ChangesOptions{Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Width: 60, Height: 20})
	apply(m, changesUpdate(sampleChanges(), nil))
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"~ changes", "@ feature/agents", "^2 v1", "docs/old guide.md -> docs/guide.md"} {
		if !strings.Contains(frame, want) {
			t.Errorf("ascii frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.ContainsFunc(frame, func(r rune) bool { return r > 0x7e }) {
		t.Errorf("ascii frame draws non-ascii glyphs:\n%s", frame)
	}
}

func TestChangesKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		popup bool
		msgs  []tea.Msg
		quit  bool
	}{
		{name: "q in a pane does nothing", msgs: keys("q")},
		{name: "esc in a pane does nothing", msgs: keys("esc")},
		{name: "q closes the popup", popup: true, msgs: keys("q"), quit: true},
		{name: "esc closes the popup", popup: true, msgs: keys("esc"), quit: true},
		{name: "ctrl+c quits a pane", msgs: keys("ctrl+c"), quit: true},
		{name: "navigation never quits", popup: true, msgs: keys("j", "k", "G", "g", "pgdown", "pgup")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Popup: tc.popup})
			r := drive(t, m, nil, tc.msgs...)
			if r.quit != tc.quit {
				t.Errorf("quit = %v, want %v", r.quit, tc.quit)
			}
		})
	}
}

func TestChangesScroll(t *testing.T) {
	t.Parallel()
	many := vcs.Changes{Head: vcs.Head{Name: "main"}}
	for i := range 30 {
		many.Files = append(many.Files, vcs.File{Kind: vcs.KindChanged, Path: fmt.Sprintf("f%02d", i), Index: '.', Worktree: 'M'})
	}
	// Height 12 leaves 10 file rows, so the last offset is 20.
	cases := []struct {
		name         string
		msgs         []tea.Msg
		cursor, want int
	}{
		{name: "down", msgs: keys("j", "down"), cursor: 2},
		{name: "up stops at zero", msgs: keys("k")},
		{name: "page scrolls once the cursor leaves the view", msgs: keys("pgdown"), cursor: 10, want: 1},
		{name: "end stops at the last file", msgs: keys("G"), cursor: 29, want: 20},
		{name: "top", msgs: keys("G", "g")},
		{name: "wheel", msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}}, cursor: 2},
		{name: "wheel up", msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}}},
		{name: "click selects the row under the pointer", msgs: []tea.Msg{clickAt(3)}, cursor: 2},
		// Row 10 of a 12 row frame is the footer: the body ends above it,
		// although the list has files left to show there.
		{name: "click on the footer is ignored", msgs: []tea.Msg{clickAt(3), clickAt(11)}, cursor: 2},
		{name: "click past the frame is ignored", msgs: []tea.Msg{clickAt(3), clickAt(60)}, cursor: 2},
		{name: "click on the header is ignored", msgs: []tea.Msg{clickAt(3), clickAt(0)}, cursor: 2},
		{name: "right click is ignored", msgs: []tea.Msg{clickAt(3), tea.MouseClickMsg{Button: tea.MouseRight, Y: 5}}, cursor: 2},
		{name: "click follows the offset", msgs: []tea.Msg{press("G"), clickAt(1)}, cursor: 20, want: 20},
		{name: "taller window keeps the cursor", msgs: []tea.Msg{press("G"), resize(80, 30)}, cursor: 29, want: 2},
		{name: "fewer files clamp", msgs: []tea.Msg{press("G"), changesUpdate(vcs.Changes{Files: many.Files[:12]}, nil)}, cursor: 11, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Width: 80, Height: 12, Now: clock})
			apply(m, changesUpdate(many, nil))
			apply(m, tc.msgs...)
			if m.list.offset != tc.want || m.list.cursor != tc.cursor {
				t.Errorf("cursor %d offset %d, want cursor %d offset %d", m.list.cursor, m.list.offset, tc.cursor, tc.want)
			}
			first := strings.Split(ansi.Strip(m.View().Content), "\n")[1]
			if want := fmt.Sprintf("f%02d", tc.want); !strings.Contains(first, want) && len(m.update.Changes.Files) == 30 {
				t.Errorf("first row %q, want %s", first, want)
			}
		})
	}
}

// clickAt is a left click on a row of the frame.
func clickAt(y int) tea.MouseClickMsg { return tea.MouseClickMsg{Button: tea.MouseLeft, Y: y} }

func TestChangesOpen(t *testing.T) {
	t.Parallel()
	files := sampleChanges()
	cases := []struct {
		name string
		// noOpeners leaves Open and OpenReview nil, the way a view with
		// nothing to open into runs.
		noOpeners bool
		msgs      []tea.Msg
		wantOpen  []string
		wantAll   int
	}{
		{name: "enter opens the file under the cursor", msgs: keys("enter"), wantOpen: []string{"api/handler.go"}},
		{name: "enter after moving", msgs: keys("j", "j", "enter"), wantOpen: []string{"docs/guide.md"}},
		{name: "a rename opens under the path it has now", msgs: keys("G", "enter"), wantOpen: []string{"web/src/components/very/deep/directory/structure/Component.test.tsx"}},
		{name: "o opens the whole review", msgs: keys("o"), wantAll: 1},
		{name: "double click opens the row", msgs: []tea.Msg{clickAt(2), clickAt(2)}, wantOpen: []string{"api/handler_test.go"}},
		{name: "one click only selects", msgs: []tea.Msg{clickAt(2)}},
		{name: "two clicks on different rows only select", msgs: []tea.Msg{clickAt(2), clickAt(3)}},
		{name: "without openers the keys do nothing", noOpeners: true, msgs: keys("enter", "o")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opened []string
			all := 0
			opts := ChangesOptions{Styles: goldenStyles(t), Width: 80, Height: 20, Now: clock}
			if !tc.noOpeners {
				opts.Open = func(path string) tea.Cmd {
					opened = append(opened, path)
					return nil
				}
				opts.OpenReview = func() tea.Cmd {
					all++
					return nil
				}
			}
			m := NewChanges(opts)
			apply(m, changesUpdate(files, nil))
			r := drive(t, m, nil, tc.msgs...)
			if r.quit {
				t.Error("opening a review quit the view")
			}
			if !slices.Equal(opened, tc.wantOpen) {
				t.Errorf("opened %q, want %q", opened, tc.wantOpen)
			}
			if all != tc.wantAll {
				t.Errorf("opened the whole review %d times, want %d", all, tc.wantAll)
			}
		})
	}
}

func TestChangesOpenWithoutFiles(t *testing.T) {
	t.Parallel()
	opened := 0
	opts := ChangesOptions{
		Styles: goldenStyles(t), Width: 80, Height: 20, Now: clock,
		Open: func(string) tea.Cmd {
			opened++
			return nil
		},
	}
	cases := []struct {
		name string
		msgs []tea.Msg
	}{
		{name: "before the first reading", msgs: keys("enter")},
		{name: "a clean working tree", msgs: []tea.Msg{changesUpdate(vcs.Changes{Head: vcs.Head{Name: "main"}}, nil), press("enter")}},
		{name: "a failed reading", msgs: []tea.Msg{changesUpdate(sampleChanges(), nil), changesUpdate(vcs.Changes{}, errors.New("git status: exit status 128")), press("enter")}},
		{name: "a click on an empty row", msgs: []tea.Msg{clickAt(1), clickAt(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewChanges(opts)
			apply(m, tc.msgs...)
			if opened != 0 {
				t.Fatalf("opened a review %d times with nothing to open", opened)
			}
		})
	}
}

func TestChangesNote(t *testing.T) {
	t.Parallel()
	m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Width: 80, Height: 20})
	apply(m, changesUpdate(sampleChanges(), nil), ChangesNote("review: no server running\x1b]0;x\x07"))
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "review: no server running") {
		t.Errorf("frame lacks the note:\n%s", frame)
	}
	if strings.Contains(m.View().Content, "\x1b]") || strings.Contains(m.View().Content, "\x07") {
		t.Error("the note carries control sequences")
	}
	apply(m, press("j"))
	if strings.Contains(ansi.Strip(m.View().Content), "no server running") {
		t.Error("a key press left the note on screen")
	}
}

func TestChangesChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan watch.Update, 2)
	first := sampleChanges()
	second := vcs.Changes{Head: vcs.Head{Name: "main"}}
	ch <- watch.Update{Changes: first, At: fixedNow}
	ch <- watch.Update{Changes: second, At: fixedNow}
	close(ch)
	m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Updates: ch})
	r := drive(t, m, m.Init())
	if r.quit {
		t.Fatal("closed channel quit the view")
	}
	if !m.closed || !m.have || m.update.Changes.Head.Name != "main" || !m.update.Changes.Clean() {
		t.Errorf("closed %v have %v update %+v", m.closed, m.have, m.update.Changes)
	}
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"working tree clean", "watcher stopped"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q", want)
		}
	}
	if NewChanges(ChangesOptions{}).Init() != nil {
		t.Error("Init without a channel returned a command")
	}
}

func TestChangesSanitizesUntrustedText(t *testing.T) {
	t.Parallel()
	c := vcs.Changes{
		Head: vcs.Head{Name: "evil\x1b]0;owned\x07branch"},
		Files: []vcs.File{
			{Kind: vcs.KindRenamed, Path: "new\x1b[2J.go", OrigPath: "old\x1b]8;;http://x\x1b\\.go", Index: 'R', Worktree: '.'},
		},
	}
	m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Dir: "/tmp/\x07dir"})
	apply(m, changesUpdate(c, nil))
	frame := m.View().Content
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x1b\\"} {
		if strings.Contains(frame, bad) {
			t.Errorf("frame carries %q", bad)
		}
	}
	if !strings.Contains(ansi.Strip(frame), "evilbranch") {
		t.Errorf("branch name not kept as text:\n%s", ansi.Strip(frame))
	}
	apply(m, changesUpdate(vcs.Changes{}, fmt.Errorf("git: %w: \x1b]52;c;eA==\x07", git.ErrNotRepository)))
	if frame := m.View().Content; strings.Contains(frame, "\x1b]") || strings.Contains(frame, "\x07") {
		t.Error("error frame carries control sequences")
	}
}

func TestChangesResize(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ w, h int }{{100, 30}, {60, 20}, {30, 5}, {10, 2}, {1, 1}, {0, 0}} {
		t.Run(strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h), func(t *testing.T) {
			t.Parallel()
			m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Popup: true})
			apply(m, changesUpdate(sampleChanges(), nil), resize(size.w, size.h))
			w, h := max(size.w, 1), max(size.h, 1)
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != h {
				t.Fatalf("%d lines, want %d", len(lines), h)
			}
			for i, l := range lines {
				if ansi.StringWidth(l) > w {
					t.Errorf("line %d is %d cells wide, over %d", i, ansi.StringWidth(l), w)
				}
			}
		})
	}
}

func TestChangesView(t *testing.T) {
	t.Parallel()
	v := NewChanges(ChangesOptions{Styles: goldenStyles(t)}).View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("view alt %v mouse %v", v.AltScreen, v.MouseMode)
	}
	if plural(1, "file", "files") != "1 file" || plural(0, "file", "files") != "0 files" {
		t.Error("plural")
	}
}
