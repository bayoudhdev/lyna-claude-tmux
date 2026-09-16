package tmux

import "strings"

// Seq is a sequence of tmux commands written into configuration: key binding
// bodies, menu item commands and conditional branches. Each command is a
// token list; tokens are quoted on output so data never changes the parse.
type Seq []Command

// Cmd builds a one-command sequence.
func Cmd(name string, args ...string) Seq {
	return Seq{append(Command{name}, args...)}
}

// Then appends the commands of next.
func (s Seq) Then(next Seq) Seq {
	return append(append(Seq{}, s...), next...)
}

// Line renders the sequence for the tail of a configuration line, such as a
// bind-key body, where commands are separated by an escaped semicolon.
func (s Seq) Line() string { return s.join(` \; `) }

// String renders the sequence as a command string that tmux parses again when
// it runs it (menu items, if-shell branches, confirm-before). Pass the result
// through ConfQuote when it is written as a single token.
func (s Seq) String() string { return s.join(" ; ") }

func (s Seq) join(sep string) string {
	parts := make([]string, 0, len(s))
	for _, c := range s {
		if len(c) == 0 {
			continue
		}
		tokens := make([]string, len(c))
		for i, t := range c {
			tokens[i] = ConfToken(t)
		}
		parts = append(parts, strings.Join(tokens, " "))
	}
	return strings.Join(parts, sep)
}

// ConfToken renders one configuration token, leaving plain words unquoted
// for readability and single-quoting everything else. Unquoted words are
// limited to characters with no meaning to the configuration parser: '#'
// would start a comment or a format, '$' and '~' expand, braces open blocks
// and quotes, backslashes, semicolons and spaces change tokenization.
func ConfToken(s string) string {
	if s == "" {
		return "''"
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_@%./:=+,-", c) >= 0
		if !ok {
			return ConfQuote(s)
		}
	}
	return s
}
