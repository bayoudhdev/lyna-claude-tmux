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

// graphSizes are the frame sizes of the history: the pane it opens at in a
// workspace, and a narrow one where the columns are dropped one by one.
var graphSizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "100x30", width: 100, height: 30, ansi: true},
	{name: "40x16", width: 40, height: 16},
}

// graphOID builds a readable object name from a letter, since a history is
// read here by the shape it draws.
func graphOID(letter string) string {
	return letter + strings.Repeat("0", 39)
}

// sampleCommits is a history with a merge, a branch, tags and a remote, the
// shapes the graph has to draw.
func sampleCommits() []vcs.Commit {
	at := func(d time.Duration) time.Time { return fixedNow.Add(-d) }
	return []vcs.Commit{
		{
			OID: graphOID("a"), Parents: []string{graphOID("b"), graphOID("d")},
			Author: "Ada Lovelace", Authored: at(time.Hour), Committed: at(time.Hour),
			Refs: []vcs.Ref{
				{Name: "main", Full: "refs/heads/main", Kind: vcs.RefBranch, Head: true},
				{Name: "origin/main", Full: "refs/remotes/origin/main", Kind: vcs.RefRemote},
			},
			Subject: "merge the git workstation",
		},
		{
			OID: graphOID("b"), Parents: []string{graphOID("c")},
			Author: "Ada Lovelace", Authored: at(3 * time.Hour), Committed: at(3 * time.Hour),
			Subject: "the refs pane of the git workstation",
		},
		{
			OID: graphOID("d"), Parents: []string{graphOID("c")},
			Author: "Grace Hopper", Authored: at(26 * time.Hour), Committed: at(26 * time.Hour),
			Refs:    []vcs.Ref{{Name: "spike", Full: "refs/heads/spike", Kind: vcs.RefBranch}},
			Subject: "a spike that was kept",
		},
		{
			OID: graphOID("c"), Parents: []string{graphOID("e")},
			Author: "Ada Lovelace", Authored: at(50 * time.Hour), Committed: at(50 * time.Hour),
			Refs:    []vcs.Ref{{Name: "v1.1.0", Full: "refs/tags/v1.1.0", Kind: vcs.RefTag}},
			Subject: "the agents workstation",
		},
		{
			OID: graphOID("e"), Author: "Grace Hopper", Authored: at(400 * time.Hour), Committed: at(400 * time.Hour),
			Subject: "the first commit",
		},
	}
}

// laneCommits is a history of many lines of descent at once: a merge of six
// branches over one root, which is what the colors of the lanes are for.
func laneCommits() []vcs.Commit {
	letters := []string{"a", "b", "c", "d", "e", "f"}
	merge := vcs.Commit{
		OID: graphOID("m"), Author: "Ada Lovelace", Committed: fixedNow.Add(-time.Hour),
		Refs:    []vcs.Ref{{Name: "main", Full: "refs/heads/main", Kind: vcs.RefBranch, Head: true}},
		Subject: "merge six branches at once",
	}
	commits := []vcs.Commit{}
	for i, l := range letters {
		merge.Parents = append(merge.Parents, graphOID(l))
		commits = append(commits, vcs.Commit{
			OID: graphOID(l), Parents: []string{graphOID("r")}, Author: "Grace Hopper",
			Committed: fixedNow.Add(-time.Duration(2+i) * time.Hour),
			Refs:      []vcs.Ref{{Name: "task-" + l, Full: "refs/heads/task-" + l, Kind: vcs.RefBranch}},
			Subject:   "work on task " + l,
		})
	}
	root := vcs.Commit{
		OID: graphOID("r"), Author: "Ada Lovelace", Committed: fixedNow.Add(-40 * time.Hour),
		Refs:    []vcs.Ref{{Name: "v1.0.0", Full: "refs/tags/v1.0.0", Kind: vcs.RefTag}},
		Subject: "the commit they all come from",
	}
	return append(append([]vcs.Commit{merge}, commits...), root)
}

func newTestGraph(t *testing.T, width, height int) *GitGraphModel {
	t.Helper()
	return NewGitGraph(GitGraphOptions{
		Styles: goldenStyles(t), Commits: sampleCommits(), Width: width, Height: height, Now: clock,
	})
}

// graphKeys sends keys and returns every message the commands produced.
func graphKeys(m *GitGraphModel, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(press(k))
		out = append(out, collect(cmd)...)
	}
	return out
}

// collect runs a command and unpacks the batch it may be.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func graphFrame(m *GitGraphModel) string { return ansi.Strip(m.View().Content) }

// longCommits is a straight history of n commits, oldest last, the shape a
// page of a real project has.
func longCommits(n int) []vcs.Commit {
	commits := make([]vcs.Commit, 0, n)
	for i := range n {
		// The object names differ in their first characters: a history is
		// read by the short name the graph draws.
		oid := "c" + strconv.Itoa(i) + strings.Repeat("0", 39-len(strconv.Itoa(i)))
		commits = append(commits, vcs.Commit{
			OID: oid, Author: "Ada Lovelace",
			Committed: fixedNow.Add(-time.Duration(i) * time.Hour),
			Subject:   "commit number " + strconv.Itoa(i),
		})
		if i > 0 {
			commits[i-1].Parents = []string{oid}
		}
	}
	return commits
}

func TestGitGraphFrames(t *testing.T) {
	t.Parallel()
	long := longCommits(60)
	states := []struct {
		name    string
		commits []vcs.Commit
		more    bool
		title   string
		msgs    []tea.Msg
	}{
		{name: "empty", commits: []vcs.Commit{}},
		{name: "populated"},
		{name: "filtered", title: "spike"},
		{name: "selected", msgs: []tea.Msg{press("j"), press("j")}},
		{name: "searching", msgs: []tea.Msg{press("/"), press("s"), press("p")}},
		{name: "searched", msgs: []tea.Msg{press("/"), press("s"), press("p"), press("enter")}},
		{name: "no-hit", msgs: []tea.Msg{press("/"), press("z"), press("z")}},
		{name: "paged", commits: long, more: true, msgs: []tea.Msg{press("pgdown")}},
		{name: "lanes", commits: laneCommits()},
	}
	for _, size := range graphSizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				commits := sampleCommits()
				if st.commits != nil {
					commits = st.commits
				}
				m := NewGitGraph(GitGraphOptions{
					Styles: goldenStyles(t), Commits: commits, More: st.more, Title: st.title,
					Width: size.width, Height: size.height, Now: clock,
				})
				apply(m, st.msgs...)
				assertFrame(t, "gitgraph/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

// TestGitGraphDrawsTheShapeOfTheHistory holds the drawing itself: the merge
// opens a lane, the branch runs beside the trunk and the lane closes again.
func TestGitGraphDrawsTheShapeOfTheHistory(t *testing.T) {
	t.Parallel()
	// A pane narrower than the chip column draws the lanes first, which is
	// where the shape is read from.
	m := newTestGraph(t, 60, 30)
	lines := strings.Split(graphFrame(m), "\n")
	var shape []string
	// The first two lines are the title bar and the column header and the
	// last is the footer; the first column of every other one is the cursor,
	// which is not part of the drawing.
	for _, l := range lines[2 : len(lines)-1] {
		trimmed := strings.TrimRight(strings.TrimPrefix(l, "▌"), " ")
		if trimmed = strings.TrimPrefix(trimmed, " "); trimmed == "" {
			continue
		}
		shape = append(shape, strings.SplitN(trimmed, " ", 2)[0])
	}
	want := []string{"◆", "├─╮", "●", "│", "│", "├─╯", "●", "│", "●"}
	if len(shape) < len(want) {
		t.Fatalf("the graph drew %v, want the shape %v", shape, want)
	}
	for i, w := range want {
		if shape[i] != w {
			t.Fatalf("line %d of the graph starts with %q, want %q (whole shape %v)", i+1, shape[i], w, shape)
		}
	}
}

func TestGitGraphMoves(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{name: "the history opens on its newest commit", want: "merge the git workstation"},
		{name: "one down", keys: []string{"j"}, want: "the refs pane of the git workstation"},
		{name: "down and back", keys: []string{"down", "down", "up"}, want: "the refs pane of the git workstation"},
		{name: "to the bottom", keys: []string{"G"}, want: "the first commit"},
		{name: "to the bottom and to the top", keys: []string{"G", "g"}, want: "merge the git workstation"},
		{name: "past the bottom stops there", keys: []string{"G", "j", "j"}, want: "the first commit"},
		{name: "past the top stops there", keys: []string{"k", "k"}, want: "merge the git workstation"},
		{name: "a page at a time", keys: []string{"pgdown"}, want: "the first commit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestGraph(t, 100, 30)
			graphKeys(m, tc.keys...)
			got, ok := m.Selected()
			if !ok {
				t.Fatal("the graph has no commit selected")
			}
			if got.Subject != tc.want {
				t.Fatalf("the cursor is on %q, want %q", got.Subject, tc.want)
			}
		})
	}
}

func TestGitGraphReportsTheCommitItIsOn(t *testing.T) {
	t.Parallel()
	m := newTestGraph(t, 100, 30)
	msgs := graphKeys(m, "j")
	var selected vcs.Commit
	for _, msg := range msgs {
		if sel, ok := msg.(GitCommitSelectedMsg); ok {
			selected = sel.Commit
		}
	}
	if selected.Subject != "the refs pane of the git workstation" {
		t.Fatalf("the graph reported %+v, want the commit the cursor moved to", selected)
	}
	// A move that changes nothing reports nothing: the detail pane is not
	// asked to redraw for a key that did not move.
	if msgs := graphKeys(m, "k", "k"); len(msgs) != 1 {
		t.Fatalf("moving to the top and past it reported %d messages, want the one move", len(msgs))
	}
	// enter opens the commit the cursor is on.
	opened := graphKeys(m, "enter")
	if len(opened) != 1 {
		t.Fatalf("enter reported %v, want the commit", opened)
	}
	chosen, ok := opened[0].(GitCommitChosenMsg)
	if !ok || chosen.Commit.Subject != "merge the git workstation" {
		t.Fatalf("enter reported %T %+v, want the commit the cursor is on", opened[0], opened[0])
	}
}

func TestGitGraphAsksForTheNextPage(t *testing.T) {
	t.Parallel()
	commits := longCommits(20)
	m := NewGitGraph(GitGraphOptions{
		Styles: goldenStyles(t), Commits: commits, More: true, Width: 100, Height: 8, Now: clock,
	})
	asked := func(msgs []tea.Msg) bool {
		for _, msg := range msgs {
			if _, ok := msg.(GitGraphMoreMsg); ok {
				return true
			}
		}
		return false
	}
	if asked(graphKeys(m, "j")) {
		t.Fatal("the graph asked for another page on the first move")
	}
	if !asked(graphKeys(m, "G")) {
		t.Fatal("the graph reached the end of the page without asking for the next one")
	}
	// It asks once: the host is reading, and a second ask would read twice.
	if asked(graphKeys(m, "k", "G")) {
		t.Fatal("the graph asked for the same page twice")
	}
	// The longer page puts the cursor back where it was and lets it ask
	// again at the end of that one.
	m.SetCommits(longCommits(40), true)
	if got, ok := m.Selected(); !ok || got.Subject != "commit number 19" {
		t.Fatalf("Selected() = %+v %v after the page grew, want the commit the cursor was on", got, ok)
	}
	if !asked(graphKeys(m, "G")) {
		t.Fatal("the graph did not ask again at the end of the longer page")
	}
	// A history that is whole never asks.
	whole := NewGitGraph(GitGraphOptions{Styles: goldenStyles(t), Commits: commits, Width: 100, Height: 8, Now: clock})
	if asked(graphKeys(whole, "G")) {
		t.Fatal("a whole history asked for another page")
	}
}

func TestGitGraphSearch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{name: "a subject", keys: []string{"/", "s", "p", "i", "k", "e"}, want: "a spike that was kept"},
		{name: "the same letters in any case", keys: []string{"/", "S", "P", "I"}, want: "a spike that was kept"},
		{name: "an author", keys: []string{"/", "g", "r", "a", "c", "e"}, want: "a spike that was kept"},
		{name: "an object name", keys: []string{"/", "e", "0", "0"}, want: "the first commit"},
		{name: "nothing at all leaves the cursor", keys: []string{"/", "z", "z"}, want: "merge the git workstation"},
		// Every letter searches from the top again, so taking letters back
		// walks the cursor back to what the shorter word matches.
		{name: "a hit taken back", keys: []string{"/", "s", "p", "i", "backspace", "backspace"}, want: "merge the git workstation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestGraph(t, 100, 30)
			graphKeys(m, tc.keys...)
			got, _ := m.Selected()
			if got.Subject != tc.want {
				t.Fatalf("the search put the cursor on %q, want %q", got.Subject, tc.want)
			}
		})
	}
}

func TestGitGraphWalksTheHits(t *testing.T) {
	t.Parallel()
	m := newTestGraph(t, 100, 30)
	// "the" is in three subjects; n walks them and wraps round.
	graphKeys(m, "/", "t", "h", "e", "enter")
	want := []string{
		"merge the git workstation",
		"the refs pane of the git workstation",
		"the agents workstation",
		"the first commit",
		"merge the git workstation",
	}
	got, _ := m.Selected()
	if got.Subject != want[0] {
		t.Fatalf("the search landed on %q, want %q", got.Subject, want[0])
	}
	for _, w := range want[1:] {
		graphKeys(m, "n")
		got, _ := m.Selected()
		if got.Subject != w {
			t.Fatalf("n moved to %q, want %q", got.Subject, w)
		}
	}
	graphKeys(m, "N")
	if got, _ := m.Selected(); got.Subject != want[len(want)-2] {
		t.Fatalf("N moved to %q, want %q", got.Subject, want[len(want)-2])
	}
	// esc outside the search line gives the search up, and n then does
	// nothing rather than walking the hits of a search that is over.
	graphKeys(m, "esc")
	before, _ := m.Selected()
	graphKeys(m, "n")
	if after, _ := m.Selected(); after.OID != before.OID {
		t.Fatalf("n moved to %q after the search was given up", after.Subject)
	}
}

func TestGitGraphSetCommitsKeepsTheCursor(t *testing.T) {
	t.Parallel()
	m := newTestGraph(t, 100, 30)
	graphKeys(m, "j", "j")
	before, _ := m.Selected()
	// A commit made on top of the history moves every row down by one; the
	// cursor stays on the commit it was on.
	next := append([]vcs.Commit{{
		OID: graphOID("9"), Parents: []string{graphOID("a")}, Author: "Ada Lovelace",
		Committed: fixedNow, Subject: "the newest commit",
	}}, sampleCommits()...)
	m.SetCommits(next, false)
	after, ok := m.Selected()
	if !ok || after.OID != before.OID {
		t.Fatalf("Selected() = %+v %v, want the commit it was on (%+v)", after, ok, before)
	}
	// A history that no longer holds the commit leaves the cursor on a
	// commit rather than on nothing.
	m.SetCommits(sampleCommits()[:1], false)
	if _, ok := m.Selected(); !ok {
		t.Fatal("the cursor landed on no commit at all")
	}
	// An empty history has nothing selected and draws its own line.
	m.SetCommits(nil, false)
	if _, ok := m.Selected(); ok {
		t.Fatal("an empty history has a commit selected")
	}
	if !strings.Contains(graphFrame(m), "no commit yet") {
		t.Fatalf("an empty history draws:\n%s", graphFrame(m))
	}
}

func TestGitGraphMouse(t *testing.T) {
	t.Parallel()
	click := func(y int) tea.MouseClickMsg { return tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: y} }
	t.Run("a click puts the cursor on the commit", func(t *testing.T) {
		t.Parallel()
		m := newTestGraph(t, 100, 30)
		m.Update(click(3))
		got, _ := m.Selected()
		if got.Subject != "the refs pane of the git workstation" {
			t.Fatalf("the click put the cursor on %q", got.Subject)
		}
	})
	t.Run("a click on the gap between commits changes nothing", func(t *testing.T) {
		t.Parallel()
		m := newTestGraph(t, 100, 30)
		before, _ := m.Selected()
		// The second body line is the gap the merge opens its lane in.
		m.Update(click(2))
		after, _ := m.Selected()
		if after.OID != before.OID {
			t.Fatalf("the cursor moved to %q on a click on the gap", after.Subject)
		}
	})
	t.Run("a double click opens the commit", func(t *testing.T) {
		t.Parallel()
		m := newTestGraph(t, 100, 30)
		m.Update(click(3))
		_, cmd := m.Update(click(3))
		var chosen vcs.Commit
		for _, msg := range collect(cmd) {
			if c, ok := msg.(GitCommitChosenMsg); ok {
				chosen = c.Commit
			}
		}
		if chosen.Subject != "the refs pane of the git workstation" {
			t.Fatalf("the double click opened %+v", chosen)
		}
	})
	t.Run("the wheel moves the cursor", func(t *testing.T) {
		t.Parallel()
		m := newTestGraph(t, 100, 10)
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		if got, _ := m.Selected(); got.Subject != "the refs pane of the git workstation" {
			t.Fatalf("the wheel put the cursor on %q", got.Subject)
		}
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		if got, _ := m.Selected(); got.Subject != "merge the git workstation" {
			t.Fatalf("the wheel back put the cursor on %q", got.Subject)
		}
	})
}

func TestGitGraphQuitKeys(t *testing.T) {
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
			m := NewGitGraph(GitGraphOptions{
				Styles: goldenStyles(t), Commits: sampleCommits(), Popup: tc.popup,
				Width: 80, Height: 20, Now: clock,
			})
			var quit bool
			for _, msg := range graphKeys(m, tc.keys...) {
				if _, ok := msg.(tea.QuitMsg); ok {
					quit = true
				}
			}
			if quit != tc.want {
				t.Fatalf("the graph quit = %v, want %v", quit, tc.want)
			}
		})
	}
}

func TestGitGraphResizes(t *testing.T) {
	t.Parallel()
	m := newTestGraph(t, 100, 30)
	graphKeys(m, "G")
	for _, size := range []struct{ w, h int }{{30, 6}, {120, 40}, {1, 1}, {100, 30}} {
		m.Update(resize(size.w, size.h))
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size.h {
			t.Fatalf("a %dx%d graph drew %d lines", size.w, size.h, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size.w {
				t.Fatalf("a %dx%d graph drew line %d %d cells wide", size.w, size.h, i+1, w)
			}
		}
	}
}

// TestGitGraphDrawsWideHistories proves a history with more lines of descent
// than the pane draws is cut rather than pushed off the screen.
func TestGitGraphDrawsWideHistories(t *testing.T) {
	t.Parallel()
	// One commit per lane: every commit is a root of its own, so the layout
	// opens a lane for each.
	var commits []vcs.Commit
	for i := range maxGraphLanes + 6 {
		commits = append(commits, vcs.Commit{
			OID:       strings.Repeat("0", 38) + strconv.Itoa(10+i),
			Author:    "Ada Lovelace",
			Committed: fixedNow.Add(-time.Duration(i) * time.Hour),
			Subject:   "root " + strconv.Itoa(i),
		})
	}
	for i := range commits[:len(commits)-1] {
		commits[i].Parents = nil
	}
	m := NewGitGraph(GitGraphOptions{Styles: goldenStyles(t), Commits: commits, Width: 60, Height: 20, Now: clock})
	for i, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Fatalf("line %d is %d cells wide", i+1, w)
		}
	}
	if !strings.Contains(graphFrame(m), "root 0") {
		t.Fatalf("a wide history drew no subject:\n%s", graphFrame(m))
	}
}

func TestGitGraphASCII(t *testing.T) {
	t.Parallel()
	wide := NewGitGraph(GitGraphOptions{
		Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Commits: sampleCommits(),
		Width: 100, Height: 30, Now: clock,
	})
	frame := ansi.Strip(wide.View().Content)
	for _, want := range []string{"* history", "BRANCH / TAG", "* main", "@ spike", "#v1.1.0", "Ada Lovelace"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the ascii frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.ContainsFunc(frame, func(r rune) bool { return r > 0x7e }) {
		t.Errorf("the ascii frame draws glyphs that are not ascii:\n%s", frame)
	}
	// A pane too narrow for a column of chips draws the refs before the
	// subject instead, in the brackets of their kind.
	narrow := NewGitGraph(GitGraphOptions{
		Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Commits: sampleCommits(),
		Width: 60, Height: 20, Now: clock,
	})
	got := ansi.Strip(narrow.View().Content)
	for _, want := range []string{"[main]", "<v1.1.0>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the narrow ascii frame lacks %q:\n%s", want, got)
		}
	}
}

// TestGitGraphDrawsNothingItWasGiven proves a subject or an author out of a
// repository is drawn as text: no escape sequence, no wrapping, no line of
// its own.
func TestGitGraphDrawsNothingItWasGiven(t *testing.T) {
	t.Parallel()
	commits := []vcs.Commit{
		{OID: graphOID("a"), Author: "red\x1b[31mauthor", Committed: fixedNow, Subject: "a subject\rwith a carriage return"},
		{OID: graphOID("b"), Author: "Ada", Committed: fixedNow, Subject: strings.Repeat("long ", 60)},
		{
			OID: graphOID("c"), Author: "Ada", Committed: fixedNow, Subject: "many refs",
			Refs: []vcs.Ref{
				{Name: "a\x1b[0mbranch", Full: "refs/heads/a", Kind: vcs.RefBranch},
				{Name: strings.Repeat("wide", 30), Full: "refs/heads/wide", Kind: vcs.RefBranch},
				{Name: "v1", Full: "refs/tags/v1", Kind: vcs.RefTag},
				{Name: "v2", Full: "refs/tags/v2", Kind: vcs.RefTag},
				{Name: "v3", Full: "refs/tags/v3", Kind: vcs.RefTag},
			},
		},
	}
	m := NewGitGraph(GitGraphOptions{Styles: goldenStyles(t), Commits: commits, Width: 70, Height: 12, Now: clock})
	content := m.View().Content
	for i, l := range strings.Split(content, "\n") {
		if strings.ContainsAny(ansi.Strip(l), "\x1b\a\r\t") {
			t.Fatalf("line %d carries control characters: %q", i+1, l)
		}
		if w := ansi.StringWidth(l); w > 70 {
			t.Fatalf("line %d is %d cells wide, over the pane", i+1, w)
		}
	}
	if strings.Contains(ansi.Strip(content), "[31m") {
		t.Fatalf("an escape sequence reached the screen:\n%s", ansi.Strip(content))
	}
}
