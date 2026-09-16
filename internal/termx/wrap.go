package termx

import (
	"strings"
	"unicode/utf8"
)

// Wrap breaks s on spaces so that no line it returns is longer than limit,
// keeping the indentation of s on each of them. A word of its own longer than
// limit, a path or a URL, is kept whole rather than cut in two: a terminal
// wrapping it at least loses no characters. A limit of zero or less, which is
// what an unknown terminal width comes to, leaves s alone.
func Wrap(s string, limit int) []string {
	words := strings.Fields(s)
	if limit <= 0 || len(words) == 0 {
		return []string{s}
	}
	indent := s[:len(s)-len(strings.TrimLeft(s, " "))]
	var lines []string
	line, length := indent+words[0], utf8.RuneCountInString(indent+words[0])
	for _, word := range words[1:] {
		n := utf8.RuneCountInString(word)
		if length+1+n > limit {
			lines = append(lines, line)
			line, length = indent+word, utf8.RuneCountInString(indent)+n
			continue
		}
		line, length = line+" "+word, length+1+n
	}
	return append(lines, line)
}

// WrapLines wraps every line of a block of text to limit.
func WrapLines(lines []string, limit int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, Wrap(line, limit)...)
	}
	return out
}
