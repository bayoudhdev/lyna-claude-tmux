package tmux

import (
	"errors"
	"strings"
)

// escapeArg protects one argv element from tmux's command splitting. When tmux
// parses its own argument vector, an element ending in ';' terminates the
// command unless the semicolon is preceded by a backslash, in which case the
// backslash is consumed and the semicolon kept. Inserting a backslash before a
// trailing ';' therefore round-trips every string, including ";" and "x\;".
func escapeArg(arg string) string {
	if strings.HasSuffix(arg, ";") {
		return arg[:len(arg)-1] + `\;`
	}
	return arg
}

// ConfQuote renders s as a single token for a tmux configuration file or a
// command string parsed by tmux (bind-key bodies, menu item commands, if-shell
// branches). Single quotes disable every expansion; an embedded single quote is
// written as '\” outside the quoted run. Control characters, which have no
// place in configuration tokens, are dropped.
func ConfQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		switch {
		case r == '\'':
			b.WriteString(`'\''`)
		case r < 0x20 || r == 0x7f:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// hashLiteral expands to a single '#'. tmux does not rescan the result of a
// conditional, so a '[' after it stays plain text.
const hashLiteral = "#{?,,##}"

// FormatEscape makes s literal in a tmux format whose expansion is used as
// data rather than drawn: run-shell, display-popup and menu item commands,
// -c and -d directories, list -F output. The expansion yields exactly s.
//
// Every '#' is doubled, except the last '#' of a run followed by '[': tmux
// keeps "##[" through expansion as escaped style markup, so that '#' comes from
// hashLiteral instead. The result is balanced but not comma-escaped; place it
// outside #{?...} branches.
func FormatEscape(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for i := range len(s) {
		switch {
		case s[i] != '#':
			b.WriteByte(s[i])
		case i+1 < len(s) && s[i+1] == '[':
			b.WriteString(hashLiteral)
		default:
			b.WriteString("##")
		}
	}
	return b.String()
}

// DrawEscape makes s literal in text tmux draws after expanding it: menu
// labels and titles, pane-border-format and the values of options shown in
// the status line. Every '#' is doubled; the "##[" this makes from "#[" is
// kept by expansion and drawn as a literal "#[".
func DrawEscape(s string) string {
	return strings.ReplaceAll(s, "#", "##")
}

// DrawEscapeTime is DrawEscape for drawn formats tmux also passes through
// strftime (status-left, status-right, status-format, display-message,
// set-titles-string), where a bare '%' starts a time conversion.
func DrawEscapeTime(s string) string {
	return strings.ReplaceAll(DrawEscape(s), "%", "%%")
}

// GlobEscape protects a path given to source-file, which tmux expands as a
// glob(3) pattern: '*', '?', '[' and backslash are preceded by a backslash.
func GlobEscape(path string) string {
	var b strings.Builder
	for i := range len(path) {
		if strings.IndexByte(`*?[\`, path[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

// ShellQuote renders s as one shell word that POSIX shells and fish both read
// back as s. tmux runs pane and popup commands with default-shell, which is
// often fish, and fish differs from POSIX in two ways that matter here: inside
// single quotes it reads \\ and \' as escapes, and a bare word starting with
// %self expands to its process id. So backslashes are written outside the
// single-quoted runs as "\\" (one backslash in both shell families), and a
// word starting with '%' is always quoted.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if isShellSafe(s) && s[0] != '%' {
		return s
	}
	runs := strings.Split(s, `\`)
	for i, r := range runs {
		if r != "" {
			runs[i] = "'" + strings.ReplaceAll(r, "'", `'\''`) + "'"
		}
	}
	return strings.Join(runs, `"\\"`)
}

// ShellJoin quotes each argument and joins them with spaces.
func ShellJoin(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = ShellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// ShellSplit reads s as the words a POSIX shell would hand to a command, but
// without running one: nothing is expanded, so a '$', a backquote or a glob
// stays the text it is. Only the quoting a user relied on when they wrote the
// string for a shell is honored. Blanks separate words; a single-quoted run
// is literal; in a double-quoted run a backslash before '"', '\', '$' or '`'
// yields that character and stays itself before anything else; outside quotes
// a backslash yields the next character. A quote left open or a trailing
// backslash is an error, since the string cannot mean what its author
// intended. The words ShellJoin wrote read back unchanged.
func ShellSplit(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	// A quoted empty run is a word, so "in a word" is tracked apart from the
	// builder's length.
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\n':
			if inWord {
				words = append(words, b.String())
				b.Reset()
				inWord = false
			}
		case '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("an opening ' has no closing '")
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case '"':
			inWord = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				b.WriteByte(s[i])
			}
			if i == len(s) {
				return nil, errors.New(`an opening " has no closing "`)
			}
		case '\\':
			inWord = true
			if i+1 == len(s) {
				return nil, errors.New("a backslash at the end escapes nothing")
			}
			i++
			b.WriteByte(s[i])
		default:
			inWord = true
			b.WriteByte(c)
		}
	}
	if inWord {
		words = append(words, b.String())
	}
	return words, nil
}

func isShellSafe(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == '@' || c == '%' || c == '+' || c == ',':
		default:
			return false
		}
	}
	return true
}
