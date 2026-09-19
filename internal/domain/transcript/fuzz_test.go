package transcript

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// seeds are the fuzz corpus every target starts from: the session fixture,
// its lines one by one, and shapes that are almost a transcript.
func seeds(f *testing.F) [][]byte {
	f.Helper()
	data, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		f.Fatal(err)
	}
	out := [][]byte{data}
	for line := range strings.SplitSeq(string(data), "\n") {
		out = append(out, []byte(line+"\n"))
	}
	for _, s := range []string{
		"", "\n", "{", "null\n", `{"type":"assistant","message":{"id":"m","usage":{"output_tokens":-1}}}` + "\n",
		`{"type":"assistant","message":{"usage":{"input_tokens":9223372036854775807}}}` + "\n",
		`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"\u001b]0;x\u0007"}]}]}}`,
	} {
		out = append(out, []byte(s))
	}
	return out
}

// pieces cuts data at the lengths cuts spells out, one byte per piece, the
// way reads of a growing file cut it wherever they happen to end.
func pieces(data, cuts []byte) [][]byte {
	var out [][]byte
	for i := 0; len(data) > 0; i++ {
		n := len(data)
		if len(cuts) > 0 {
			n = min(int(cuts[i%len(cuts)])+1, len(data))
		}
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

// FuzzMeter feeds a transcript whole and in pieces: the tally is the same
// however the reads cut it, with the default bound on a line and with one
// small enough to drop lines.
func FuzzMeter(f *testing.F) {
	for _, s := range seeds(f) {
		f.Add(s, []byte{0, 3, 17})
	}
	f.Fuzz(func(t *testing.T, data, cuts []byte) {
		for _, limit := range []int{0, 64} {
			whole := Meter{lines: Lines{limit: limit}}
			_, _ = whole.Write(data)
			split := Meter{lines: Lines{limit: limit}}
			for _, p := range pieces(data, cuts) {
				_, _ = split.Write(p)
			}
			a, b := whole.Tally(), split.Tally()
			if !sameTally(a, b) {
				t.Fatalf("limit %d: whole %+v, in pieces %+v", limit, a, b)
			}
			for _, u := range []Usage{a.Sum, a.Last} {
				if u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite < 0 || u.Total() < 0 {
					t.Fatalf("a negative count: %+v", u)
				}
			}
		}
	})
}

// FuzzLines checks the lines themselves: pieces give the lines the whole
// gives, and no line holds a newline or goes past the bound.
func FuzzLines(f *testing.F) {
	for _, s := range seeds(f) {
		f.Add(s, []byte{1, 0, 250})
	}
	f.Fuzz(func(t *testing.T, data, cuts []byte) {
		const limit = 32
		collect := func(chunks [][]byte) []string {
			l := Lines{limit: limit}
			var got []string
			for _, c := range chunks {
				l.Feed(c, func(b []byte) { got = append(got, string(b)) })
			}
			return got
		}
		whole, split := collect([][]byte{data}), collect(pieces(data, cuts))
		if !slices.Equal(whole, split) {
			t.Fatalf("whole %q, in pieces %q", whole, split)
		}
		for _, l := range whole {
			if strings.Contains(l, "\n") || len(l) > limit {
				t.Fatalf("line %q", l)
			}
		}
	})
}

// FuzzEntries reads anything as a transcript: no panic, and nothing reaches a
// reader that could drive the terminal.
func FuzzEntries(f *testing.F) {
	for _, s := range seeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, e := range Entries(data) {
			switch e.Who {
			case User, Assistant, Tool:
			default:
				t.Fatalf("entry of %q", e.Who)
			}
			if sanitize.Plain(e.Text) != e.Text || strings.Contains(e.Text, "\t") || len(e.Text) > MaxText {
				t.Fatalf("text %q", e.Text)
			}
			if sanitize.Line(e.Tool) != e.Tool || (e.Tool != "" && e.Who != Tool) {
				t.Fatalf("tool %q of %q", e.Tool, e.Who)
			}
		}
	})
}
