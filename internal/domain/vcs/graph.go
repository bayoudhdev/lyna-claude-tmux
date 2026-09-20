package vcs

// Graph is a history laid out in lanes: one column per line of descent, the
// commits kept in the order they were read (git writes a history child first,
// so the drawing runs from the newest commit down).
type Graph struct {
	Rows []GraphRow
	// Width is the number of columns the drawing needs.
	Width int
}

// GraphRow is one commit of a graph with the lines drawn around it.
type GraphRow struct {
	Commit Commit
	// Lane is the column the commit sits in.
	Lane int
	// Lanes are the columns carrying a line on the commit's own line, Lane
	// among them, ascending.
	Lanes []int
	// Link are the lines of the gap above the commit's line, each from a
	// column of the row before to a column of this one: From equal to To is a
	// line carrying on straight, To equal to Lane a line ending at this
	// commit, and From equal to the lane of the row before a line starting at
	// the commit drawn there.
	Link []Edge
}

// Edge is one line of the gap between two rows.
type Edge struct{ From, To int }

// LayGraph lays commits out in lanes. A commit takes the lane of the first
// child waiting for it, its first parent carries that lane on, and every
// other parent opens a lane of its own; a lane is freed as soon as the commit
// it waited for is drawn, and the next lane opened takes its place, so the
// drawing stays as narrow as the history allows.
func LayGraph(commits []Commit) Graph {
	g := Graph{Rows: make([]GraphRow, 0, len(commits))}
	// expect holds the commit each column waits for, empty when the column is
	// free; src holds the column the line of a column comes from, -1 when the
	// line starts at this row.
	var (
		expect []string
		src    []int
	)
	open := func(oid string, from int) int {
		col := freeLane(expect)
		if col == len(expect) {
			expect, src = append(expect, ""), append(src, -1)
		}
		expect[col], src[col] = oid, from
		return col
	}
	for _, c := range commits {
		node := waitingLane(expect, c.OID)
		if node < 0 {
			node = open(c.OID, -1)
		}
		row := GraphRow{Commit: c, Lane: node}
		for i, oid := range expect {
			if oid == "" || src[i] < 0 {
				continue
			}
			to := i
			if oid == c.OID {
				to = node
			}
			row.Link = append(row.Link, Edge{From: src[i], To: to})
		}
		// Every other lane waiting for this commit ends in the gap above it.
		for i, oid := range expect {
			if oid == c.OID && i != node {
				expect[i], src[i] = "", -1
			}
		}
		for i, oid := range expect {
			if oid != "" {
				row.Lanes = append(row.Lanes, i)
				src[i] = i
			}
		}
		// The parents take the lanes: the first the commit's own, so a line of
		// descent keeps its column, the others whatever is free.
		expect[node], src[node] = "", -1
		for n, p := range c.Parents {
			if n == 0 {
				expect[node], src[node] = p, node
				continue
			}
			open(p, node)
		}
		g.Width = max(g.Width, rowWidth(row))
		g.Rows = append(g.Rows, row)
	}
	return g
}

// waitingLane is the lowest column waiting for a commit, -1 when none is.
func waitingLane(expect []string, oid string) int {
	for i, want := range expect {
		if want == oid {
			return i
		}
	}
	return -1
}

// freeLane is the lowest column holding no line, or one past the last column
// when they all do.
func freeLane(expect []string) int {
	for i, want := range expect {
		if want == "" {
			return i
		}
	}
	return len(expect)
}

// rowWidth is the number of columns a row occupies, its own lines and the
// lines of the gap above it alike.
func rowWidth(row GraphRow) int {
	w := row.Lane + 1
	if n := len(row.Lanes); n > 0 {
		w = max(w, row.Lanes[n-1]+1)
	}
	for _, e := range row.Link {
		w = max(w, e.From+1, e.To+1)
	}
	return w
}
