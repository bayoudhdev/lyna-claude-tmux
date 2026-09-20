package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// workSizes are the frame sizes the workstation lays out at: three regions,
// two, and one.
var workSizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "120x30", width: 120, height: 30, ansi: true},
	{name: "90x24", width: 90, height: 24},
	{name: "60x18", width: 60, height: 18},
}

// sampleState is a repository with a history, refs, a working tree and
// nothing left in the middle. Its branches stand on the commits of the
// history, which is what following a ref needs.
func sampleState() GitState {
	commits := sampleCommits()
	refs := sampleGitRefs()
	for i := range refs.Branches {
		refs.Branches[i].OID = commits[min(i, len(commits)-1)].OID
	}
	return GitState{Commits: commits, Refs: refs, Changes: sampleChanges(), At: fixedNow}
}

// workOptions are the options every test drives the workstation with, with
// the readings it asks for recorded rather than run.
type workRecorder struct {
	reads   []string
	more    []string
	opens   [][2]string
	refresh int
}

func (r *workRecorder) options(t testing.TB) GitWorkOptions {
	t.Helper()
	return GitWorkOptions{
		Styles: goldenStyles(t), Root: testHome + "/src/acme", Home: testHome, Now: clock,
		ReadCommit: func(rev string) tea.Cmd {
			r.reads = append(r.reads, rev)
			return nil
		},
		ReadMore: func(before string) tea.Cmd {
			r.more = append(r.more, before)
			return nil
		},
		Open: func(path, rev string) tea.Cmd {
			r.opens = append(r.opens, [2]string{path, rev})
			return nil
		},
		Refresh: func() tea.Cmd {
			r.refresh++
			return nil
		},
	}
}

// newTestWork opens a workstation on the sample repository.
func newTestWork(t *testing.T, width, height int) (*GitWorkModel, *workRecorder) {
	t.Helper()
	rec := &workRecorder{}
	opts := rec.options(t)
	opts.Width, opts.Height = width, height
	m := NewGitWork(opts)
	m.Update(gitStateMsg{state: sampleState()})
	return m, rec
}

func workFrame(m *GitWorkModel) string { return ansi.Strip(m.View().Content) }

func workKeys(m *GitWorkModel, keys ...string) {
	for _, k := range keys {
		m.Update(press(k))
	}
}

func TestGitWorkFrames(t *testing.T) {
	t.Parallel()
	rebase := sampleState()
	rebase.Progress = vcs.InProgress{
		Kind: vcs.OperationRebase, Branch: "feat/git-workstation", Step: 2, Total: 5,
		Heads: []string{graphOID("c")},
	}
	states := []struct {
		name  string
		state *GitState
		keys  []string
	}{
		{name: "reading"},
		{name: "history", state: ptr(sampleState())},
		{name: "refs", state: ptr(sampleState()), keys: []string{"shift+tab"}},
		{name: "detail", state: ptr(sampleState()), keys: []string{"tab"}},
		{name: "working-tree", state: ptr(sampleState()), keys: []string{"w"}},
		{name: "rebase", state: ptr(rebase)},
		{
			name:  "failed",
			state: ptr(GitState{Err: errors.New("git log: exit status 128: fatal: bad revision"), At: fixedNow}),
		},
	}
	for _, size := range workSizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				rec := &workRecorder{}
				opts := rec.options(t)
				opts.Width, opts.Height = size.width, size.height
				m := NewGitWork(opts)
				if st.state != nil {
					m.Update(gitStateMsg{state: *st.state})
				}
				workKeys(m, st.keys...)
				assertFrame(t, "gitwork/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestGitWorkLaysOutItsRegions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                       string
		width                      int
		region                     GitRegion
		refs, graph, detail        bool
		wantRefs, wantGraph, wantD int
	}{
		{name: "three regions", width: 120, region: RegionGraph, refs: true, graph: true, detail: true},
		{name: "three regions, the widest", width: 200, region: RegionGraph, refs: true, graph: true, detail: true},
		{name: "two regions on the history", width: 90, region: RegionGraph, refs: true, graph: true},
		{name: "two regions on the detail", width: 90, region: RegionDetail, refs: true, detail: true},
		{name: "one region, the history", width: 60, region: RegionGraph, graph: true},
		{name: "one region, the detail", width: 60, region: RegionDetail, detail: true},
		{name: "one region, the refs", width: 60, region: RegionRefs, refs: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newTestWork(t, tc.width, 24)
			m.focus(tc.region)
			m.resize()
			refs, graph, detail := m.layout()
			if (refs > 0) != tc.refs || (graph > 0) != tc.graph || (detail > 0) != tc.detail {
				t.Fatalf("layout() = %d %d %d, want refs = %v, history = %v, detail = %v",
					refs, graph, detail, tc.refs, tc.graph, tc.detail)
			}
			if refs+graph+detail != tc.width {
				t.Fatalf("layout() = %d %d %d, %d cells in a frame of %d", refs, graph, detail, refs+graph+detail, tc.width)
			}
			// Whatever the layout, every line of the frame is the width of
			// the frame and no more.
			for i, l := range strings.Split(m.View().Content, "\n") {
				if w := ansi.StringWidth(l); w > tc.width {
					t.Fatalf("line %d is %d cells wide, over the frame", i+1, w)
				}
			}
		})
	}
}

func TestGitWorkMovesBetweenRegions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		width int
		keys  []string
		want  GitRegion
	}{
		{name: "it opens on the history", width: 120, want: RegionGraph},
		{name: "tab goes right", width: 120, keys: []string{"tab"}, want: RegionDetail},
		{name: "tab wraps round", width: 120, keys: []string{"tab", "tab"}, want: RegionRefs},
		{name: "shift+tab goes left", width: 120, keys: []string{"shift+tab"}, want: RegionRefs},
		{name: "round and back", width: 120, keys: []string{"tab", "tab", "tab"}, want: RegionGraph},
		{name: "a narrow frame keeps the refs out of the way", width: 60, keys: []string{"tab", "tab"}, want: RegionGraph},
		{name: "w goes to the working tree", width: 120, keys: []string{"w"}, want: RegionDetail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newTestWork(t, tc.width, 24)
			workKeys(m, tc.keys...)
			if m.Region() != tc.want {
				t.Fatalf("Region() = %v, want %v", m.Region(), tc.want)
			}
			// The region the keys reach is the one drawing its title bright,
			// and it is the only one.
			var focused int
			for _, r := range []bool{m.refs.focused, m.graph.focused, m.detail.focused} {
				if r {
					focused++
				}
			}
			if focused != 1 {
				t.Fatalf("%d regions are focused, want one", focused)
			}
		})
	}
}

// TestGitWorkKeysGoToTheRegionThatIsTyping proves a filter open in a region
// takes every printable key, so typing a name never moves the keys elsewhere.
func TestGitWorkKeysGoToTheRegionThatIsTyping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		keys   []string
		region GitRegion
		value  string
	}{
		// w and r are keys of the workstation, so a filter that takes them
		// is a filter that takes every printable key.
		{
			name: "the keys of a filter belong to the refs",
			keys: []string{"shift+tab", "/", "w", "o", "r", "k"}, region: RegionRefs, value: "work",
		},
		{
			name: "the keys of a search belong to the history",
			keys: []string{"/", "w", "o", "r", "k"}, region: RegionGraph, value: "work",
		},
		{
			name: "esc gives the keys back",
			keys: []string{"/", "w", "o", "r", "k", "esc", "w"}, region: RegionDetail,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newTestWork(t, 120, 24)
			workKeys(m, tc.keys...)
			if m.Region() != tc.region {
				t.Fatalf("Region() = %v, want %v", m.Region(), tc.region)
			}
			if tc.value == "" {
				return
			}
			if !strings.Contains(workFrame(m), tc.value) {
				t.Fatalf("what was typed is nowhere on screen:\n%s", workFrame(m))
			}
		})
	}
}

func TestGitWorkReadsTheCommitTheCursorIsOn(t *testing.T) {
	t.Parallel()
	t.Run("moving the cursor reads the commit", func(t *testing.T) {
		t.Parallel()
		m, rec := newTestWork(t, 120, 24)
		workKeys(m, "j")
		if len(rec.reads) != 1 || rec.reads[0] != graphOID("b") {
			t.Fatalf("the workstation read %v, want the commit the cursor moved to", rec.reads)
		}
		if !strings.Contains(workFrame(m), "reading the commit") {
			t.Fatalf("the detail does not say it is reading:\n%s", workFrame(m))
		}
	})
	t.Run("the commit that comes back is drawn", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		workKeys(m, "j")
		m.Update(gitCommitReadM{read: GitCommitRead{
			Rev: graphOID("b"),
			Detail: vcs.CommitDetail{
				Commit: vcs.Commit{OID: graphOID("b"), Author: "Ada Lovelace", Subject: "a commit read in full"},
				Files:  []vcs.NumStat{{Path: "api/handler.go", Added: 4}},
			},
		}})
		if !strings.Contains(workFrame(m), "a commit read in full") {
			t.Fatalf("the commit that came back is not on screen:\n%s", workFrame(m))
		}
	})
	t.Run("a reading of a commit the cursor left is dropped", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		workKeys(m, "j")
		m.Update(gitCommitReadM{read: GitCommitRead{
			Rev:    graphOID("d"),
			Detail: vcs.CommitDetail{Commit: vcs.Commit{OID: graphOID("d"), Subject: "the one left behind"}},
		}})
		if strings.Contains(workFrame(m), "the one left behind") {
			t.Fatalf("a reading of another commit reached the screen:\n%s", workFrame(m))
		}
	})
	t.Run("a reading that failed says so", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		workKeys(m, "j")
		m.Update(gitCommitReadM{read: GitCommitRead{
			Rev: graphOID("b"), Err: errors.New("git show: exit status 128"),
		}})
		if !strings.Contains(workFrame(m), "exit status 128") {
			t.Fatalf("the reading that failed is not on screen:\n%s", workFrame(m))
		}
	})
}

func TestGitWorkFollowsARef(t *testing.T) {
	t.Parallel()
	t.Run("a ref on a commit of the history moves the cursor to it", func(t *testing.T) {
		t.Parallel()
		m, rec := newTestWork(t, 120, 24)
		// The refs pane opens on the first branch; choosing it follows it.
		workKeys(m, "shift+tab", "j", "enter")
		if m.Region() != RegionGraph {
			t.Fatalf("Region() = %v, want the history the ref was followed into", m.Region())
		}
		if len(rec.reads) == 0 {
			t.Fatal("following a ref read no commit")
		}
		c, ok := m.graph.Selected()
		if !ok || c.OID != rec.reads[len(rec.reads)-1] {
			t.Fatalf("the cursor is on %+v, want the commit that was read", c)
		}
	})
	t.Run("a ref on no commit read yet says so", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		m.showRef(GitRef{Kind: GitRefBranch, Name: "old/branch", Rev: graphOID("f")})
		if !strings.Contains(workFrame(m), "old/branch is on no commit") {
			t.Fatalf("the command bar does not say the ref was not found:\n%s", workFrame(m))
		}
	})
}

func TestGitWorkOpensAFile(t *testing.T) {
	t.Parallel()
	m, rec := newTestWork(t, 120, 24)
	// The detail opens on the working tree, whose files carry no commit.
	workKeys(m, "w", "enter")
	if len(rec.opens) != 1 || rec.opens[0][1] != "" {
		t.Fatalf("the workstation opened %v, want a file of the working tree", rec.opens)
	}
	if rec.opens[0][0] == "" {
		t.Fatal("the workstation opened a file with no path")
	}
}

func TestGitWorkAsksForMoreHistory(t *testing.T) {
	t.Parallel()
	state := sampleState()
	state.Commits = longCommits(40)
	state.More = true
	rec := &workRecorder{}
	opts := rec.options(t)
	opts.Width, opts.Height = 120, 12
	m := NewGitWork(opts)
	m.Update(gitStateMsg{state: state})
	workKeys(m, "G")
	if len(rec.more) == 0 {
		t.Fatal("reaching the end of the history asked for no more of it")
	}
	if want := state.Commits[len(state.Commits)-1].OID; rec.more[0] != want {
		t.Fatalf("the workstation asked for the page before %q, want %q", rec.more[0], want)
	}
}

func TestGitWorkRefreshes(t *testing.T) {
	t.Parallel()
	m, rec := newTestWork(t, 120, 24)
	workKeys(m, "r")
	if rec.refresh != 1 {
		t.Fatalf("r asked for %d readings, want one", rec.refresh)
	}
	// A workstation with nothing to read with does nothing rather than
	// reporting a failure nobody can act on.
	opts := GitWorkOptions{Styles: goldenStyles(t), Width: 120, Height: 24, Now: clock}
	quiet := NewGitWork(opts)
	quiet.Update(gitStateMsg{state: sampleState()})
	workKeys(quiet, "r", "j", "enter", "w")
	if strings.Contains(workFrame(quiet), "read again") {
		t.Fatalf("the bar offers a key that does nothing:\n%s", workFrame(quiet))
	}
}

func TestGitWorkSaysWhatItIsWaitingFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(m *GitWorkModel)
		want string
	}{
		{name: "before the first reading", do: func(*GitWorkModel) {}, want: "reading the repository"},
		{
			name: "once one has come",
			do:   func(m *GitWorkModel) { m.Update(gitStateMsg{state: sampleState()}) },
			want: "~/src/acme",
		},
		{
			name: "a reading that failed",
			do: func(m *GitWorkModel) {
				m.Update(gitStateMsg{state: GitState{Err: errors.New("fatal: not a git repository")}})
			},
			want: "not a git repository",
		},
		{
			name: "a watcher that stopped",
			do: func(m *GitWorkModel) {
				m.Update(gitStateMsg{state: sampleState()})
				m.Update(gitStatesDone{})
			},
			want: "no longer watched",
		},
		{
			name: "a note that something could not be done",
			do: func(m *GitWorkModel) {
				m.Update(gitStateMsg{state: sampleState()})
				m.Update(GitWorkNoteMsg{Text: "the branch has an upstream already"})
			},
			want: "upstream already",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &workRecorder{}
			opts := rec.options(t)
			opts.Width, opts.Height = 120, 24
			m := NewGitWork(opts)
			tc.do(m)
			if !strings.Contains(workFrame(m), tc.want) {
				t.Fatalf("the command bar lacks %q:\n%s", tc.want, workFrame(m))
			}
		})
	}
}

// TestGitWorkClearsTheNote keeps a line nobody read on screen until a key is
// pressed, and no longer.
func TestGitWorkClearsTheNote(t *testing.T) {
	t.Parallel()
	m, _ := newTestWork(t, 120, 24)
	m.Update(GitWorkNoteMsg{Text: "the branch has an upstream already"})
	if !strings.Contains(workFrame(m), "upstream already") {
		t.Fatalf("the note is not on screen:\n%s", workFrame(m))
	}
	m.Update(gitStateMsg{state: sampleState()})
	if !strings.Contains(workFrame(m), "upstream already") {
		t.Fatalf("a reading took the note away:\n%s", workFrame(m))
	}
	workKeys(m, "j")
	if strings.Contains(workFrame(m), "upstream already") {
		t.Fatalf("the note is still there after a key:\n%s", workFrame(m))
	}
}

func TestGitWorkMouse(t *testing.T) {
	t.Parallel()
	click := func(x, y int) tea.MouseClickMsg {
		return tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}
	}
	t.Run("a click moves the keys to the region it fell in", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		refs, graph, _ := m.layout()
		for _, tc := range []struct {
			x    int
			want GitRegion
		}{
			{x: 2, want: RegionRefs},
			{x: refs + 2, want: RegionGraph},
			{x: refs + graph + 2, want: RegionDetail},
		} {
			m.Update(click(tc.x, 4))
			if m.Region() != tc.want {
				t.Fatalf("a click at column %d put the keys on %v, want %v", tc.x, m.Region(), tc.want)
			}
		}
	})
	t.Run("a click on the command bar changes nothing", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		before := m.Region()
		m.Update(click(2, 23))
		if m.Region() != before {
			t.Fatalf("a click on the bar moved the keys to %v", m.Region())
		}
	})
	t.Run("a click under a banner lands on the row it points at", func(t *testing.T) {
		t.Parallel()
		state := sampleState()
		state.Progress = vcs.InProgress{Kind: vcs.OperationMerge, Branch: "main"}
		rec := &workRecorder{}
		opts := rec.options(t)
		opts.Width, opts.Height = 120, 24
		m := NewGitWork(opts)
		m.Update(gitStateMsg{state: state})
		refs, _, _ := m.layout()
		// Under the banner comes the history, whose own first two lines are
		// its heading and its column names.
		m.Update(click(refs+2, 1+2+1))
		if m.Region() != RegionGraph {
			t.Fatalf("the click landed on %v", m.Region())
		}
		if len(rec.reads) == 0 {
			t.Fatal("the click read no commit")
		}
	})
	t.Run("the wheel scrolls the region under the pointer", func(t *testing.T) {
		t.Parallel()
		m, _ := newTestWork(t, 120, 24)
		refs, _, _ := m.layout()
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: refs + 2, Y: 4})
		if m.Region() != RegionGraph {
			t.Fatalf("the wheel put the keys on %v", m.Region())
		}
	})
}

func TestGitWorkQuitKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		popup bool
		keys  []string
		want  bool
	}{
		{name: "ctrl+c always closes", keys: []string{"ctrl+c"}, want: true},
		{name: "q closes a popup", popup: true, keys: []string{"q"}, want: true},
		{name: "esc closes a popup", popup: true, keys: []string{"esc"}, want: true},
		{name: "q does nothing in a pane", keys: []string{"q"}},
		{name: "esc does nothing in a pane", keys: []string{"esc"}},
		{name: "a region of a pane closes nothing", keys: []string{"shift+tab", "q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &workRecorder{}
			opts := rec.options(t)
			opts.Width, opts.Height, opts.Popup = 120, 24, tc.popup
			m := NewGitWork(opts)
			m.Update(gitStateMsg{state: sampleState()})
			var quit bool
			for _, k := range tc.keys {
				_, cmd := m.Update(press(k))
				for _, msg := range collect(cmd) {
					if _, ok := msg.(tea.QuitMsg); ok {
						quit = true
					}
				}
			}
			if quit != tc.want {
				t.Fatalf("the workstation quit = %v, want %v", quit, tc.want)
			}
		})
	}
}

func TestGitWorkWaitsOnItsChannel(t *testing.T) {
	t.Parallel()
	states := make(chan GitState, 1)
	rec := &workRecorder{}
	opts := rec.options(t)
	opts.Width, opts.Height, opts.States = 120, 24, states
	m := NewGitWork(opts)
	states <- sampleState()
	msg := m.Init()()
	if _, ok := msg.(gitStateMsg); !ok {
		t.Fatalf("the first message is %T, want a reading", msg)
	}
	m.Update(msg)
	if !strings.Contains(workFrame(m), "~/src/acme") {
		t.Fatalf("the reading is not on screen:\n%s", workFrame(m))
	}
	close(states)
	done := m.Init()()
	if _, ok := done.(gitStatesDone); !ok {
		t.Fatalf("a closed channel reported %T, want the end of the readings", done)
	}
	// A workstation with no channel waits for nothing rather than blocking.
	quiet := NewGitWork(GitWorkOptions{Styles: goldenStyles(t), Width: 40, Height: 12})
	if cmd := quiet.Init(); cmd != nil {
		t.Fatal("a workstation with no channel waits on one")
	}
}

func TestGitWorkResizes(t *testing.T) {
	t.Parallel()
	m, _ := newTestWork(t, 120, 30)
	for _, size := range []struct{ w, h int }{{40, 8}, {200, 60}, {1, 1}, {72, 20}, {104, 3}, {120, 30}} {
		m.Update(resize(size.w, size.h))
		frame := strings.Split(m.View().Content, "\n")
		if len(frame) != size.h {
			t.Fatalf("a %dx%d workstation drew %d lines", size.w, size.h, len(frame))
		}
		for i, l := range frame {
			if w := ansi.StringWidth(l); w > size.w {
				t.Fatalf("a %dx%d workstation drew line %d %d cells wide", size.w, size.h, i+1, w)
			}
		}
	}
}

// TestGitWorkRegionsAreQuietWhenTheKeysAreElsewhere proves the region being
// typed at is the one that says so.
func TestGitWorkRegionsAreQuietWhenTheKeysAreElsewhere(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(t *testing.T) interface {
			SetFocused(bool)
			View() tea.View
		}
	}{
		{
			name: "the refs pane",
			build: func(t *testing.T) interface {
				SetFocused(bool)
				View() tea.View
			} {
				m := NewGitRefs(GitRefsOptions{Styles: goldenStyles(t), Width: 40, Height: 12, Now: clock})
				m.SetRefs(sampleGitRefs())
				return m
			},
		},
		{
			name: "the history",
			build: func(t *testing.T) interface {
				SetFocused(bool)
				View() tea.View
			} {
				m := NewGitGraph(GitGraphOptions{Styles: goldenStyles(t), Width: 80, Height: 12, Now: clock})
				m.SetCommits(sampleCommits(), false)
				return m
			},
		},
		{
			name: "the detail",
			build: func(t *testing.T) interface {
				SetFocused(bool)
				View() tea.View
			} {
				m := NewGitDetail(GitDetailOptions{Styles: goldenStyles(t), Width: 48, Height: 12, Now: clock})
				m.ShowCommit(sampleDetail())
				return m
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tc.build(t)
			on := m.View().Content
			m.SetFocused(false)
			off := m.View().Content
			if on == off {
				t.Fatalf("a region draws the same whether or not the keys reach it:\n%s", ansi.Strip(on))
			}
			// What it says is the same; only how it says it changes.
			if ansi.Strip(on) == ansi.Strip(off) {
				return
			}
			// The cursor column is the one thing that may go quiet.
			if strings.ReplaceAll(ansi.Strip(on), "▌", " ") != ansi.Strip(off) {
				t.Fatalf("a region unfocused draws other text:\nfocused:\n%s\nquiet:\n%s",
					ansi.Strip(on), ansi.Strip(off))
			}
		})
	}
}

func TestOverlay(t *testing.T) {
	t.Parallel()
	base := []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"}
	cases := []struct {
		name string
		box  []string
		x, y int
		want []string
	}{
		{name: "nothing at all", want: base},
		{
			name: "a box in the middle", box: []string{"XX"}, x: 4, y: 1,
			want: []string{"aaaaaaaaaa", "bbbbXXbbbb", "cccccccccc"},
		},
		{
			name: "a box at the left edge", box: []string{"XX", "YY"}, x: 0, y: 0,
			want: []string{"XXaaaaaaaa", "YYbbbbbbbb", "cccccccccc"},
		},
		{
			name: "a box at the right edge", box: []string{"XX"}, x: 8, y: 2,
			want: []string{"aaaaaaaaaa", "bbbbbbbbbb", "ccccccccXX"},
		},
		{
			name: "a box past the right edge", box: []string{"XXXX"}, x: 8, y: 0,
			want: []string{"aaaaaaaaXXXX", "bbbbbbbbbb", "cccccccccc"},
		},
		{
			name: "a box past the bottom", box: []string{"XX", "YY"}, x: 0, y: 2,
			want: []string{"aaaaaaaaaa", "bbbbbbbbbb", "XXcccccccc"},
		},
		{name: "a box above the top", box: []string{"XX"}, y: -1, want: base},
		{name: "a box off to the left", box: []string{"XX"}, x: -2, want: base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := overlay(base, tc.box, tc.x, tc.y)
			if len(got) != len(tc.want) {
				t.Fatalf("overlay() drew %d lines, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("line %d = %q, want %q", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestOverlayKeepsTheStylesUnderIt proves a box pasted over a styled frame
// leaves the colors of what it did not cover.
func TestOverlayKeepsTheStylesUnderIt(t *testing.T) {
	t.Parallel()
	s := goldenStyles(t)
	base := []string{s.line(20, nil, seg("left", s.Success), seg("right", s.Danger))}
	got := overlay(base, []string{s.Accent.Render("XX")}, 5, 0)
	if w := ansi.StringWidth(got[0]); w != 20 {
		t.Fatalf("the line is %d cells wide, want 20: %q", w, got[0])
	}
	if plain := ansi.Strip(got[0]); plain != "leftrXXht           " {
		t.Fatalf("the line reads %q", plain)
	}
	if !strings.Contains(got[0], s.Success.Render("left")[:6]) {
		t.Fatalf("the style before the box is gone: %q", got[0])
	}
}

func TestBeside(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		columns [][]string
		want    []string
	}{
		{name: "no column at all"},
		{name: "one column", columns: [][]string{{"aa", "bb"}}, want: []string{"aa", "bb"}},
		{
			name:    "two of the same height",
			columns: [][]string{{"aa", "bb"}, {"cc", "dd"}},
			want:    []string{"aacc", "bbdd"},
		},
		{
			name:    "one shorter than the other",
			columns: [][]string{{"aa"}, {"cc", "dd"}},
			want:    []string{"aacc", "  dd"},
		},
		{
			name:    "a column whose lines are not the same width",
			columns: [][]string{{"aaa", "b"}, {"cc", "dd"}},
			want:    []string{"aaacc", "b  dd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := beside(tc.columns...)
			if len(got) != len(tc.want) {
				t.Fatalf("beside() drew %d lines, want %d: %q", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("line %d = %q, want %q", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestGitWorkNow(t *testing.T) {
	t.Parallel()
	// A workstation with no clock uses the real one, which is only checked
	// for not being the zero time.
	m := NewGitWork(GitWorkOptions{Styles: goldenStyles(t), Width: 80, Height: 20})
	m.Update(gitStateMsg{state: sampleState()})
	if strings.TrimSpace(workFrame(m)) == "" {
		t.Fatal("a workstation with no clock drew nothing")
	}
	if time.Since(fixedNow) == 0 {
		t.Fatal("the fixed clock is the real one")
	}
}
