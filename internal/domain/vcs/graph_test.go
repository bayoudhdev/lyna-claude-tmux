package vcs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// commitOf makes a commit of a shape a graph cares about: an object name and
// the parents it follows.
func commitOf(oid string, parents ...string) Commit {
	return Commit{OID: oid, Parents: parents, Subject: "commit " + oid}
}

func TestLayGraph(t *testing.T) {
	cases := []struct {
		name    string
		commits []Commit
		want    []GraphRow
		width   int
	}{
		{name: "an empty history"},
		{
			name:    "a history of one commit",
			commits: []Commit{commitOf("a")},
			want:    []GraphRow{{Lane: 0, Lanes: []int{0}}},
			width:   1,
		},
		{
			name:    "a straight line keeps its lane",
			commits: []Commit{commitOf("c", "b"), commitOf("b", "a"), commitOf("a")},
			want: []GraphRow{
				{Lane: 0, Lanes: []int{0}},
				{Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}}},
				{Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}}},
			},
			width: 1,
		},
		{
			name:    "a merge opens a lane and the parents close it",
			commits: []Commit{commitOf("m", "a", "b"), commitOf("a", "r"), commitOf("b", "r"), commitOf("r")},
			want: []GraphRow{
				{Lane: 0, Lanes: []int{0}},
				{Lane: 0, Lanes: []int{0, 1}, Link: []Edge{{From: 0, To: 0}, {From: 0, To: 1}}},
				{Lane: 1, Lanes: []int{0, 1}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 1}}},
				{Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 0}}},
			},
			width: 2,
		},
		{
			name:    "a merge of three parents",
			commits: []Commit{commitOf("m", "a", "b", "c")},
			want:    []GraphRow{{Lane: 0, Lanes: []int{0}}},
			width:   1,
		},
		{
			name:    "a branch tip starts a lane of its own",
			commits: []Commit{commitOf("c", "b"), commitOf("t", "b"), commitOf("b")},
			want: []GraphRow{
				{Lane: 0, Lanes: []int{0}},
				{Lane: 1, Lanes: []int{0, 1}, Link: []Edge{{From: 0, To: 0}}},
				{Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 0}}},
			},
			width: 2,
		},
		{
			name:    "a lane freed is taken by the next one opened",
			commits: []Commit{commitOf("m", "a", "b"), commitOf("a"), commitOf("b", "x", "y")},
			want: []GraphRow{
				{Lane: 0, Lanes: []int{0}},
				{Lane: 0, Lanes: []int{0, 1}, Link: []Edge{{From: 0, To: 0}, {From: 0, To: 1}}},
				{Lane: 1, Lanes: []int{1}, Link: []Edge{{From: 1, To: 1}}},
			},
			width: 2,
		},
		{
			name:    "a page that stops before the parents it names",
			commits: []Commit{commitOf("c", "b")},
			want:    []GraphRow{{Lane: 0, Lanes: []int{0}}},
			width:   1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LayGraph(tc.commits)
			if got.Width != tc.width {
				t.Fatalf("Width = %d, want %d", got.Width, tc.width)
			}
			if len(got.Rows) != len(tc.want) {
				t.Fatalf("LayGraph() drew %d rows, want %d", len(got.Rows), len(tc.want))
			}
			for i, row := range got.Rows {
				want := tc.want[i]
				if row.Commit.OID != tc.commits[i].OID {
					t.Fatalf("row %d holds %q, want %q", i, row.Commit.OID, tc.commits[i].OID)
				}
				if row.Lane != want.Lane {
					t.Fatalf("row %d lane = %d, want %d", i, row.Lane, want.Lane)
				}
				if !equalInts(row.Lanes, want.Lanes) {
					t.Fatalf("row %d lanes = %v, want %v", i, row.Lanes, want.Lanes)
				}
				if !equalEdges(row.Link, want.Link) {
					t.Fatalf("row %d link = %+v, want %+v", i, row.Link, want.Link)
				}
			}
		})
	}
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalEdges(got, want []Edge) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestLayGraphFrames draws the histories a reader has to recognize at a
// glance, so the goldens carry the shape and not only the numbers.
func TestLayGraphFrames(t *testing.T) {
	cases := []struct {
		name    string
		commits []Commit
	}{
		{
			name:    "straight",
			commits: []Commit{commitOf("c", "b"), commitOf("b", "a"), commitOf("a")},
		},
		{
			name:    "merge",
			commits: []Commit{commitOf("m", "a", "b"), commitOf("a", "r"), commitOf("b", "r"), commitOf("r")},
		},
		{
			name: "branches",
			commits: []Commit{
				commitOf("f", "e"), commitOf("t", "d"), commitOf("e", "d"),
				commitOf("d", "c"), commitOf("s", "c"), commitOf("c"),
			},
		},
		{
			name: "octopus",
			commits: []Commit{
				commitOf("m", "a", "b", "c"), commitOf("a", "r"),
				commitOf("b", "r"), commitOf("c", "r"), commitOf("r"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			golden.Assert(t, filepath.Join("graph", tc.name+".golden"), []byte(draw(LayGraph(tc.commits))))
		})
	}
}

// TestLayGraphDrawsGitItself lays out the history captured from a real
// repository: a merge, two branches that diverged and a first commit.
func TestLayGraphDrawsGitItself(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "log.z"))
	if err != nil {
		t.Fatal(err)
	}
	commits, err := ParseLog(data)
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	g := LayGraph(commits)
	if len(g.Rows) != len(commits) {
		t.Fatalf("LayGraph() drew %d rows, want %d", len(g.Rows), len(commits))
	}
	if g.Width < 2 {
		t.Fatalf("Width = %d, want a history of more than one lane", g.Width)
	}
	golden.Assert(t, filepath.Join("graph", "repository.golden"), []byte(draw(g)))
}

func FuzzLayGraph(f *testing.F) {
	f.Add(nul(commitRecord(commitA, commitB+" "+commitC, "Ada", "1", "1", "", "a merge")...))
	f.Add(nul(append(
		commitRecord(commitA, commitB, "Ada", "1", "1", "", "one"),
		commitRecord(commitB, "", "Ada", "1", "1", "", "two")...,
	)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		commits, err := ParseLog(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseLog() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		g := LayGraph(commits)
		if len(g.Rows) != len(commits) {
			t.Fatalf("LayGraph() drew %d rows, want %d", len(g.Rows), len(commits))
		}
		if len(g.Rows) == 0 && g.Width != 0 {
			t.Fatalf("Width = %d for no commit at all", g.Width)
		}
		for i, row := range g.Rows {
			if row.Lane < 0 || row.Lane >= g.Width {
				t.Fatalf("row %d sits in lane %d, outside the %d columns drawn", i, row.Lane, g.Width)
			}
			if !contains(row.Lanes, row.Lane) {
				t.Fatalf("row %d draws lanes %v, without its own %d", i, row.Lanes, row.Lane)
			}
			for j, lane := range row.Lanes {
				if lane < 0 || lane >= g.Width {
					t.Fatalf("row %d draws lane %d, outside the %d columns", i, lane, g.Width)
				}
				if j > 0 && lane <= row.Lanes[j-1] {
					t.Fatalf("row %d draws lanes %v, which are not ascending", i, row.Lanes)
				}
			}
			for _, e := range row.Link {
				if e.From < 0 || e.From >= g.Width || e.To < 0 || e.To >= g.Width {
					t.Fatalf("row %d links %+v, outside the %d columns", i, e, g.Width)
				}
				if e.To != row.Lane && !contains(row.Lanes, e.To) {
					t.Fatalf("row %d links %+v, to a column it does not draw (%v)", i, e, row.Lanes)
				}
			}
			if i == 0 && len(row.Link) != 0 {
				t.Fatalf("the first row links %+v, with no row above it", row.Link)
			}
			// A drawing of the row is as wide as the layout says, whatever
			// the history was.
			if len(NodeCells(g.Width, row)) != CellCount(g.Width) || len(LinkCells(g.Width, row.Link)) != CellCount(g.Width) {
				t.Fatalf("row %d of a %d lane graph draws the wrong number of cells", i, g.Width)
			}
		}
	})
}

func contains(lanes []int, lane int) bool {
	for _, l := range lanes {
		if l == lane {
			return true
		}
	}
	return false
}

// draw renders a graph the way the view does: the gap above a commit, then
// the commit's own line with its object name and subject.
func draw(g Graph) string {
	var b strings.Builder
	for _, row := range g.Rows {
		if len(row.Link) > 0 {
			b.WriteString(strings.TrimRight(string(LinkCells(g.Width, row.Link)), " ") + "\n")
		}
		b.WriteString(string(NodeCells(g.Width, row)) + "  " + row.Commit.Short() + " " + row.Commit.Subject + "\n")
	}
	return b.String()
}

func TestGraphCells(t *testing.T) {
	cases := []struct {
		name  string
		width int
		row   GraphRow
		node  string
		link  string
	}{
		{
			name: "one commit alone", width: 1,
			row:  GraphRow{Commit: commitOf("a"), Lanes: []int{0}},
			node: "●", link: "",
		},
		{
			name: "a line carrying on beside a commit", width: 2,
			row:  GraphRow{Commit: commitOf("a"), Lane: 0, Lanes: []int{0, 1}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 1}}},
			node: "● │", link: "│ │",
		},
		{
			name: "a merge opening a lane", width: 2,
			row:  GraphRow{Commit: commitOf("m", "a", "b"), Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 1}}},
			node: "◆  ", link: "╰─╮",
		},
		{
			name: "a lane closing into the one on its left", width: 2,
			row:  GraphRow{Commit: commitOf("r"), Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 0}}},
			node: "●  ", link: "├─╯",
		},
		{
			name: "an octopus closing three lanes", width: 3,
			row:  GraphRow{Commit: commitOf("r"), Lane: 0, Lanes: []int{0}, Link: []Edge{{From: 0, To: 0}, {From: 1, To: 0}, {From: 2, To: 0}}},
			node: "●    ", link: "├─┴─╯",
		},
		{
			name: "a width that is no width at all", width: 0,
			row:  GraphRow{Commit: commitOf("a"), Lanes: []int{0}},
			node: "●", link: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(NodeCells(tc.width, tc.row)); got != tc.node {
				t.Fatalf("NodeCells() = %q, want %q", got, tc.node)
			}
			if len(tc.row.Link) == 0 {
				return
			}
			if got := string(LinkCells(tc.width, tc.row.Link)); got != tc.link {
				t.Fatalf("LinkCells() = %q, want %q", got, tc.link)
			}
		})
	}
}

func TestCellLane(t *testing.T) {
	cases := []struct{ cell, lane int }{{0, 0}, {1, 1}, {2, 1}, {3, 2}, {4, 2}}
	for _, tc := range cases {
		t.Run("cell "+itoa(tc.cell), func(t *testing.T) {
			if got := CellLane(tc.cell); got != tc.lane {
				t.Fatalf("CellLane(%d) = %d, want %d", tc.cell, got, tc.lane)
			}
		})
	}
}

// TestGraphCellsHoldTheirWidth proves a drawing is exactly as wide as the
// graph says, whatever the rows carry: a view lays its columns out on that
// number before it draws anything.
func TestGraphCellsHoldTheirWidth(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "log.z"))
	if err != nil {
		t.Fatal(err)
	}
	commits, err := ParseLog(data)
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	g := LayGraph(commits)
	for _, row := range g.Rows {
		if n := len(NodeCells(g.Width, row)); n != CellCount(g.Width) {
			t.Fatalf("NodeCells() drew %d cells, want %d", n, CellCount(g.Width))
		}
		if n := len(LinkCells(g.Width, row.Link)); n != CellCount(g.Width) {
			t.Fatalf("LinkCells() drew %d cells, want %d", n, CellCount(g.Width))
		}
	}
}
