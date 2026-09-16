package claudecfg

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines surround each change.
const diffContext = 3

// maxLCSCells bounds the quadratic part of LineDiff. Settings files are small;
// past the bound the differing middle is shown as removed then added, which
// is still a correct, if longer, diff.
const maxLCSCells = 4_000_000

type opKind byte

const (
	opEqual  opKind = ' '
	opDelete opKind = '-'
	opInsert opKind = '+'
)

type diffOp struct {
	kind opKind
	line string // includes its trailing newline, when the line had one
}

// LineDiff returns a unified diff of two texts without file headers, meant for
// the confirmation preview before a project file is rewritten. It returns ""
// when the texts are equal. A last line without a newline is marked the way
// diff(1) marks it.
func LineDiff(oldText, newText []byte) string {
	if string(oldText) == string(newText) {
		return ""
	}
	ops := diffLines(splitLines(string(oldText)), splitLines(string(newText)))
	var b strings.Builder
	for i := 0; i < len(ops); {
		if ops[i].kind == opEqual {
			i++
			continue
		}
		from := max(i-diffContext, 0)
		// Grow the hunk over later changes separated by at most two contexts
		// of unchanged lines, so hunks never overlap.
		end := i
		for {
			for end < len(ops) && ops[end].kind != opEqual {
				end++
			}
			next := end
			for next < len(ops) && ops[next].kind == opEqual {
				next++
			}
			if next == len(ops) || next-end > 2*diffContext {
				break
			}
			end = next
		}
		to := min(end+diffContext, len(ops))
		writeHunk(&b, ops, from, to)
		i = to
	}
	return b.String()
}

func writeHunk(b *strings.Builder, ops []diffOp, from, to int) {
	oldStart, newStart := 1, 1
	for _, op := range ops[:from] {
		if op.kind != opInsert {
			oldStart++
		}
		if op.kind != opDelete {
			newStart++
		}
	}
	oldCount, newCount := 0, 0
	for _, op := range ops[from:to] {
		if op.kind != opInsert {
			oldCount++
		}
		if op.kind != opDelete {
			newCount++
		}
	}
	fmt.Fprintf(b, "@@ -%s +%s @@\n", hunkRange(oldStart, oldCount), hunkRange(newStart, newCount))
	for _, op := range ops[from:to] {
		b.WriteByte(byte(op.kind))
		if line, ok := strings.CutSuffix(op.line, "\n"); ok {
			b.WriteString(line)
			b.WriteByte('\n')
		} else {
			b.WriteString(op.line)
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

func hunkRange(start, count int) string {
	if count == 0 {
		// An empty range names the line before it, as unified diffs do.
		return fmt.Sprintf("%d,0", start-1)
	}
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// splitLines splits after each newline; a final line without one is kept.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines computes an edit script: common prefix and suffix first, then a
// longest common subsequence over the middle.
func diffLines(a, b []string) []diffOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for _, l := range a[:prefix] {
		ops = append(ops, diffOp{opEqual, l})
	}
	ops = append(ops, middleOps(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	for _, l := range a[len(a)-suffix:] {
		ops = append(ops, diffOp{opEqual, l})
	}
	return ops
}

func middleOps(a, b []string) []diffOp {
	var ops []diffOp
	if len(a)*len(b) > maxLCSCells {
		for _, l := range a {
			ops = append(ops, diffOp{opDelete, l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{opInsert, l})
		}
		return ops
	}
	// lcs[i][j] is the LCS length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opInsert, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{opDelete, a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{opInsert, b[j]})
	}
	return ops
}
