package claudecfg

import "strings"

// shellQuote renders s as one POSIX shell word. Claude Code runs a hook or
// status line command through `sh -c`, so the lyna-tmux path must survive
// word splitting and expansion whatever directory it was installed in.
// Words made only of characters no POSIX shell treats specially stay bare,
// which keeps the common case readable. Everything else is single-quoted;
// an embedded single quote closes the quoted run, adds a backslash-escaped
// quote and reopens the run.
//
// The domain layer may not import internal/tmux, so this mirrors
// tmux.ShellQuote. Its tests decode the result with /bin/sh, and its fuzz
// test with a POSIX word parser.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellSafe(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-_./:@%+,", c) >= 0:
		default:
			return false
		}
	}
	return true
}
