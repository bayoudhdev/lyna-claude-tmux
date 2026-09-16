package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

func sampleChanges() watch.Changes {
	files := []watch.File{
		{Kind: watch.KindChanged, Path: "api/handler.go", Index: 'M', Worktree: '.', Added: 12, Deleted: 3},
		{Kind: watch.KindChanged, Path: "api/handler_test.go", Index: '.', Worktree: 'M', Added: 40, Deleted: 0},
		{Kind: watch.KindRenamed, Path: "docs/guide.md", OrigPath: "docs/old guide.md", Index: 'R', Worktree: 'M', Added: 1, Deleted: 1},
		{Kind: watch.KindChanged, Path: "go.sum", Index: 'A', Worktree: 'D', Added: 0, Deleted: 0},
		{Kind: watch.KindUnmerged, Path: "internal/merge.go", Index: 'U', Worktree: 'U', Added: 7, Deleted: 2},
		{Kind: watch.KindChanged, Path: "logo.png", Index: 'M', Worktree: '.', Binary: true},
		{Kind: watch.KindUntracked, Path: "notes/", Index: '?', Worktree: '?'},
		{Kind: watch.KindChanged, Path: "web/src/components/very/deep/directory/structure/Component.test.tsx", Index: '.', Worktree: 'M', Added: 1024, Deleted: 512},
	}
	c := watch.Changes{
		Branch: watch.Branch{OID: "0123456789abcdef0123456789abcdef01234567", Head: "feature/agents", Upstream: "origin/feature/agents", AheadBehind: true, Ahead: 2, Behind: 1},
		Files:  files,
	}
	for _, f := range files {
		c.Added += f.Added
		c.Deleted += f.Deleted
	}
	return c
}

func changesUpdate(c watch.Changes, err error) changesUpdateMsg {
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
	notRepo := fmt.Errorf("git rev-parse: %w", watch.ErrNotRepository)
	manyFiles := sampleChanges()
	for i := range 40 {
		manyFiles.Files = append(manyFiles.Files, watch.File{Kind: watch.KindChanged, Path: "gen/file" + strconv.Itoa(i) + ".go", Index: '.', Worktree: 'M', Added: i})
	}
	states := []struct {
		name  string
		popup bool
		msgs  []tea.Msg
	}{
		{name: "loading"},
		{name: "clean", msgs: []tea.Msg{changesUpdate(watch.Changes{Branch: watch.Branch{Head: "main"}}, nil)}},
		{name: "populated", msgs: []tea.Msg{changesUpdate(sampleChanges(), nil)}},
		{name: "popup", popup: true, msgs: []tea.Msg{changesUpdate(sampleChanges(), nil)}},
		{name: "scrolled", msgs: []tea.Msg{changesUpdate(manyFiles, nil), press("pgdown"), press("j")}},
		{name: "initial", msgs: []tea.Msg{changesUpdate(watch.Changes{
			Branch: watch.Branch{Head: "main", Initial: true},
			Files:  []watch.File{{Kind: watch.KindChanged, Path: "README.md", Index: 'A', Worktree: '.', Added: 3}},
			Added:  3,
		}, nil)}},
		{name: "detached", msgs: []tea.Msg{changesUpdate(watch.Changes{Branch: watch.Branch{OID: "89abcdef0123", Head: "(detached)", Detached: true}}, nil)}},
		{name: "error", msgs: []tea.Msg{changesUpdate(watch.Changes{}, errors.New("git status: exit status 128: fatal: index file corrupt"))}},
		{name: "not-repository", msgs: []tea.Msg{changesUpdate(watch.Changes{}, notRepo)}},
		{name: "stopped", msgs: []tea.Msg{changesUpdate(sampleChanges(), nil), changesClosedMsg{}}},
	}
	for _, size := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				m := NewChanges(ChangesOptions{
					Styles: goldenStyles(t), Popup: st.popup, Dir: testHome + "/src/api", Home: testHome,
					Width: size.width, Height: size.height,
				})
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
	many := watch.Changes{Branch: watch.Branch{Head: "main"}}
	for i := range 30 {
		many.Files = append(many.Files, watch.File{Kind: watch.KindChanged, Path: fmt.Sprintf("f%02d", i), Index: '.', Worktree: 'M'})
	}
	// Height 12 leaves 10 file rows, so the last offset is 20.
	cases := []struct {
		name string
		msgs []tea.Msg
		want int
	}{
		{name: "down", msgs: keys("j", "down"), want: 2},
		{name: "up stops at zero", msgs: keys("k"), want: 0},
		{name: "page", msgs: keys("pgdown"), want: 10},
		{name: "end stops at the last page", msgs: keys("G"), want: 20},
		{name: "top", msgs: keys("G", "g"), want: 0},
		{name: "wheel", msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}}, want: 6},
		{name: "wheel up", msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelDown}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}}, want: 0},
		{name: "taller window clamps", msgs: []tea.Msg{press("G"), resize(80, 30)}, want: 2},
		{name: "fewer files clamp", msgs: []tea.Msg{press("G"), changesUpdate(watch.Changes{Files: many.Files[:12]}, nil)}, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Width: 80, Height: 12})
			apply(m, changesUpdate(many, nil))
			apply(m, tc.msgs...)
			if m.offset != tc.want {
				t.Errorf("offset = %d, want %d", m.offset, tc.want)
			}
			first := strings.Split(ansi.Strip(m.View().Content), "\n")[1]
			if want := fmt.Sprintf("f%02d", tc.want); !strings.Contains(first, want) && len(m.update.Changes.Files) == 30 {
				t.Errorf("first row %q, want %s", first, want)
			}
		})
	}
}

func TestChangesChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan watch.Update, 2)
	first := sampleChanges()
	second := watch.Changes{Branch: watch.Branch{Head: "main"}}
	ch <- watch.Update{Changes: first, At: fixedNow}
	ch <- watch.Update{Changes: second, At: fixedNow}
	close(ch)
	m := NewChanges(ChangesOptions{Styles: goldenStyles(t), Updates: ch})
	r := drive(t, m, m.Init())
	if r.quit {
		t.Fatal("closed channel quit the view")
	}
	if !m.closed || !m.have || m.update.Changes.Branch.Head != "main" || !m.update.Changes.Clean() {
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
	c := watch.Changes{
		Branch: watch.Branch{Head: "evil\x1b]0;owned\x07branch"},
		Files: []watch.File{
			{Kind: watch.KindRenamed, Path: "new\x1b[2J.go", OrigPath: "old\x1b]8;;http://x\x1b\\.go", Index: 'R', Worktree: '.'},
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
	apply(m, changesUpdate(watch.Changes{}, fmt.Errorf("git: %w: \x1b]52;c;eA==\x07", watch.ErrNotRepository)))
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
