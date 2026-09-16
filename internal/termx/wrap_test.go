package termx

import (
	"strings"
	"testing"
)

func TestWrap(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  []string
	}{
		{"no limit", "one two three", 0, []string{"one two three"}},
		{"negative limit", "one two three", -8, []string{"one two three"}},
		{"shorter than the limit", "one two", 20, []string{"one two"}},
		{"exactly the limit", "one two", 7, []string{"one two"}},
		{"one cell too long", "one two", 6, []string{"one", "two"}},
		{"several lines", "a b c d e f", 3, []string{"a b", "c d", "e f"}},
		{"a word of its own is never cut", "see /very/long/path/to/a/file now", 10, []string{"see", "/very/long/path/to/a/file", "now"}},
		{"indentation is kept on every line", "  userns include if exists", 12, []string{"  userns", "  include if", "  exists"}},
		{"runes count as one cell", "éé éé éé", 5, []string{"éé éé", "éé"}},
		{"only spaces", "   ", 4, []string{"   "}},
		{"empty", "", 4, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Wrap(tc.in, tc.limit)
			if len(got) != len(tc.want) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("Wrap(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
		})
	}
}

func TestWrapLines(t *testing.T) {
	got := WrapLines([]string{"a b c", "d"}, 3)
	want := []string{"a b", "c", "d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("WrapLines() = %q, want %q", got, want)
	}
}
