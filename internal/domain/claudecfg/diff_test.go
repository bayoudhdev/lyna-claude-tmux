package claudecfg

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "equal", old: "a\nb\n", new: "a\nb\n", want: ""},
		{name: "both empty", old: "", new: "", want: ""},
		{name: "new file", old: "", new: "a\nb\n", want: "@@ -0,0 +1,2 @@\n+a\n+b\n"},
		{name: "emptied file", old: "a\n", new: "", want: "@@ -1 +0,0 @@\n-a\n"},
		{name: "one line changed", old: "a\nb\nc\n", new: "a\nB\nc\n", want: "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		{
			name: "context is limited to three lines",
			old:  "1\n2\n3\n4\n5\n6\n7\n8\n9\n",
			new:  "1\n2\n3\n4\nfive\n6\n7\n8\n9\n",
			want: "@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n",
		},
		{
			name: "close changes share a hunk",
			old:  "a\n1\n2\n3\n4\n5\n6\nb\n",
			new:  "A\n1\n2\n3\n4\n5\n6\nB\n",
			want: "@@ -1,8 +1,8 @@\n-a\n+A\n 1\n 2\n 3\n 4\n 5\n 6\n-b\n+B\n",
		},
		{
			name: "distant changes get separate hunks",
			old:  "a\n1\n2\n3\n4\n5\n6\n7\nb\n",
			new:  "A\n1\n2\n3\n4\n5\n6\n7\nB\n",
			want: "@@ -1,4 +1,4 @@\n-a\n+A\n 1\n 2\n 3\n@@ -6,4 +6,4 @@\n 5\n 6\n 7\n-b\n+B\n",
		},
		{name: "added final newline", old: "a", new: "a\n", want: "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n"},
		{name: "removed final newline", old: "x\na\n", new: "x\na", want: "@@ -1,2 +1,2 @@\n x\n-a\n+a\n\\ No newline at end of file\n"},
		{name: "insert in the middle", old: "{\n}\n", new: "{\n  \"a\": 1\n}\n", want: "@@ -1,2 +1,3 @@\n {\n+  \"a\": 1\n }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LineDiff([]byte(tc.old), []byte(tc.new))
			if got != tc.want {
				t.Fatalf("LineDiff =\n%s\nwant\n%s", got, tc.want)
			}
			if applied, err := applyUnified(tc.old, got); err != nil || applied != tc.new {
				t.Fatalf("applying the diff = %q, %v; want %q", applied, err, tc.new)
			}
		})
	}
}

func TestLineDiffLargeInputFallsBackAndStaysCorrect(t *testing.T) {
	var oldText, newText strings.Builder
	// Enough differing lines that the LCS table would exceed maxLCSCells.
	for i := range 2100 {
		fmt.Fprintf(&oldText, "old %d\n", i)
		fmt.Fprintf(&newText, "new %d\n", i)
	}
	diff := LineDiff([]byte(oldText.String()), []byte(newText.String()))
	if !strings.HasPrefix(diff, "@@ -1,2100 +1,2100 @@\n-old 0\n") {
		t.Fatalf("unexpected diff start: %q", diff[:min(len(diff), 80)])
	}
	if applied, err := applyUnified(oldText.String(), diff); err != nil || applied != newText.String() {
		t.Fatalf("applying the fallback diff failed: %v", err)
	}
}

func FuzzLineDiff(f *testing.F) {
	f.Add("", "")
	f.Add("a\nb\nc\n", "a\nc\nd")
	f.Add("x", "x\n")
	f.Add("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "0\n2\n3\n4\n5\n6\n7\n8\n9\nten\n")
	f.Fuzz(func(t *testing.T, oldText, newText string) {
		if len(oldText) > 4096 || len(newText) > 4096 {
			return
		}
		diff := LineDiff([]byte(oldText), []byte(newText))
		if (diff == "") != (oldText == newText) {
			t.Fatalf("LineDiff(%q, %q) = %q", oldText, newText, diff)
		}
		got, err := applyUnified(oldText, diff)
		if err != nil {
			t.Fatalf("diff does not apply: %v\n%s", err, diff)
		}
		if got != newText {
			t.Fatalf("applying LineDiff(%q, %q) gives %q", oldText, newText, got)
		}
	})
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@$`)

// applyUnified applies a LineDiff result to old, checking every header count,
// context line and removed line, so a wrong diff cannot apply by accident.
func applyUnified(oldText, diff string) (string, error) {
	var oldLines []string
	if oldText != "" {
		oldLines = splitLines(oldText)
	}
	var out strings.Builder
	next := 0 // index of the next old line not yet copied
	lines := strings.SplitAfter(diff, "\n")
	for i := 0; i < len(lines) && lines[i] != ""; {
		m := hunkHeader.FindStringSubmatch(strings.TrimSuffix(lines[i], "\n"))
		if m == nil {
			return "", fmt.Errorf("line %d is not a hunk header: %q", i, lines[i])
		}
		oldStart, oldCount := headerRange(m[1], m[2])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}
		first := oldStart - 1
		if oldCount == 0 {
			first = oldStart
		}
		if first < next || first > len(oldLines) {
			return "", fmt.Errorf("hunk %q starts at old line %d, already at %d", lines[i], first, next)
		}
		for ; next < first; next++ {
			out.WriteString(oldLines[next])
		}
		i++
		seenOld, seenNew := 0, 0
		for i < len(lines) && lines[i] != "" && !strings.HasPrefix(lines[i], "@@") {
			body := lines[i]
			i++
			if i < len(lines) && lines[i] == "\\ No newline at end of file\n" {
				body = strings.TrimSuffix(body, "\n")
				i++
			}
			if body == "" {
				return "", errors.New("empty diff line")
			}
			text := body[1:]
			switch body[0] {
			case ' ', '-':
				if next >= len(oldLines) || oldLines[next] != text {
					return "", fmt.Errorf("old line %d is %q, diff says %q", next, at(oldLines, next), text)
				}
				next++
				seenOld++
				if body[0] == ' ' {
					out.WriteString(text)
					seenNew++
				}
			case '+':
				out.WriteString(text)
				seenNew++
			default:
				return "", fmt.Errorf("bad diff line %q", body)
			}
		}
		if seenOld != oldCount || seenNew != newCount {
			return "", fmt.Errorf("hunk counts -%d +%d, header says -%d +%d", seenOld, seenNew, oldCount, newCount)
		}
	}
	for ; next < len(oldLines); next++ {
		out.WriteString(oldLines[next])
	}
	return out.String(), nil
}

func headerRange(start, count string) (startN, countN int) {
	startN, _ = strconv.Atoi(start)
	countN = 1
	if count != "" {
		countN, _ = strconv.Atoi(count)
	}
	return startN, countN
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of file>"
}
