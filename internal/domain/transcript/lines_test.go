package transcript

import (
	"slices"
	"strings"
	"testing"
)

// feedLines runs pieces through a Lines with the given limit and returns the
// lines it saw, copied, since the slice a line arrives in is reused.
func feedLines(limit int, pieces ...string) []string {
	l := Lines{limit: limit}
	var got []string
	for _, p := range pieces {
		l.Feed([]byte(p), func(b []byte) { got = append(got, string(b)) })
	}
	return got
}

func TestLinesFeed(t *testing.T) {
	cases := []struct {
		name   string
		limit  int
		pieces []string
		want   []string
	}{
		{name: "whole lines in one piece", pieces: []string{"a\nbb\n"}, want: []string{"a", "bb"}},
		{name: "a line cut in two", pieces: []string{"ab", "c\nd\n"}, want: []string{"abc", "d"}},
		{name: "a line cut in three", pieces: []string{"a", "b", "c\n"}, want: []string{"abc"}},
		{name: "a newline alone", pieces: []string{"abc", "\n"}, want: []string{"abc"}},
		{name: "a line not ended yet is kept", pieces: []string{"a\nb"}, want: []string{"a"}},
		{name: "empty lines are lines", pieces: []string{"\n\n"}, want: []string{"", ""}},
		{name: "nothing", pieces: []string{""}},
		{name: "a line at the limit", limit: 3, pieces: []string{"abc\n"}, want: []string{"abc"}},
		{name: "a line over the limit in one piece", limit: 3, pieces: []string{"abcd\nef\n"}, want: []string{"ef"}},
		{name: "a line over the limit across pieces", limit: 3, pieces: []string{"ab", "cd", "ef\ngh\n"}, want: []string{"gh"}},
		{name: "a line that goes over the limit on its last piece", limit: 3, pieces: []string{"ab", "cd\nx\n"}, want: []string{"x"}},
		{name: "a line over the limit that has not ended", limit: 3, pieces: []string{"ok\nabcdef"}, want: []string{"ok"}},
		{name: "after a dropped line the next one is whole", limit: 3, pieces: []string{"abcdef", "gh", "\nij\n"}, want: []string{"ij"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedLines(tc.limit, tc.pieces...); !slices.Equal(got, tc.want) {
				t.Fatalf("lines %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLinesReleaseLongBuffers covers the memory a long line leaves behind: the
// buffer that held it is given back once the line is out.
func TestLinesReleaseLongBuffers(t *testing.T) {
	long := strings.Repeat("x", keepCap+1)
	cases := []struct {
		name   string
		pieces []string
		kept   bool
	}{
		{name: "a short line keeps its buffer", pieces: []string{"ab", "c\n"}, kept: true},
		{name: "a long line gives its buffer back", pieces: []string{long, "\n"}},
		{name: "a long line over the limit gives its buffer back", pieces: []string{long, long + "\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := Lines{limit: 2*keepCap + 1}
			for _, p := range tc.pieces {
				l.Feed([]byte(p), func([]byte) {})
			}
			if kept := l.partial != nil; kept != tc.kept {
				t.Fatalf("buffer kept %v (cap %d), want %v", kept, cap(l.partial), tc.kept)
			}
		})
	}
}
