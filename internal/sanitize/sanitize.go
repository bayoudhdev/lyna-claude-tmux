// Package sanitize neutralizes untrusted text before lyna-tmux renders it.
//
// Pane captures, agent names, working directories and branch names can carry
// terminal control sequences. Replayed verbatim they could retitle the window,
// write the clipboard (OSC 52), open hyperlinks, move the cursor over trusted UI
// or query the terminal. Terminal keeps printable text and SGR color sequences;
// Plain and Line keep printable text only.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSGRParams bounds the parameter bytes of a kept SGR sequence.
const maxSGRParams = 64

// Terminal returns s with every control sequence removed except SGR (ESC [ ... m)
// with plain numeric parameters. Newlines and tabs survive; carriage returns,
// other C0/C1 controls, invalid UTF-8 and bidirectional overrides do not.
func Terminal(s string) string { return clean(s, true) }

// Plain returns s with every control sequence removed, SGR included.
func Plain(s string) string { return clean(s, false) }

// Line returns Plain(s) folded onto one trimmed line: newlines and tabs become spaces.
func Line(s string) string {
	p := Plain(s)
	p = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, p)
	return strings.TrimSpace(p)
}

func clean(s string, keepSGR bool) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == 0x1b:
			i = escape(&b, s, i, keepSGR)
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && size <= 1:
				i++
			case r >= 0x80 && r <= 0x9f:
				i = c1(s, i+size, r)
			case isBidiControl(r) || !unicode.IsPrint(r) && !unicode.IsSpace(r) && !isAllowedFormat(r):
				i += size
			default:
				b.WriteString(s[i : i+size])
				i += size
			}
		}
	}
	return b.String()
}

// escape consumes the sequence starting at s[i] == ESC and returns the next index.
func escape(b *strings.Builder, s string, i int, keepSGR bool) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[':
		return csi(b, s, i, i+2, keepSGR)
	case ']', 'P', '_', '^', 'X':
		return stringTerminated(s, i+2)
	default:
		// Two-byte escape (ESC 7, ESC c, ESC =, ...) or charset designation (ESC ( B).
		next := i + 2
		if s[i+1] >= 0x20 && s[i+1] <= 0x2f && next < len(s) {
			next++
		}
		return next
	}
}

// csi consumes a control sequence whose parameters start at j.
func csi(b *strings.Builder, s string, start, j int, keepSGR bool) int {
	k := j
	for k < len(s) && s[k] >= 0x30 && s[k] <= 0x3f {
		k++
	}
	params := s[j:k]
	for k < len(s) && s[k] >= 0x20 && s[k] <= 0x2f {
		k++
	}
	if k >= len(s) {
		return len(s)
	}
	final := s[k]
	if final < 0x40 || final > 0x7e {
		// Malformed: drop the introducer and resume at the offending byte.
		return k
	}
	if keepSGR && final == 'm' && k == j+len(params) && validSGR(params) {
		b.WriteString(s[start : k+1])
	}
	return k + 1
}

func validSGR(params string) bool {
	if len(params) > maxSGRParams {
		return false
	}
	for i := range len(params) {
		c := params[i]
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return false
		}
	}
	return true
}

// stringTerminated skips an OSC/DCS/APC/PM/SOS body up to BEL or ST (ESC \).
// An unterminated body swallows the rest of the input.
func stringTerminated(s string, j int) int {
	for k := j; k < len(s); k++ {
		switch s[k] {
		case 0x07:
			return k + 1
		case 0x1b:
			if k+1 < len(s) && s[k+1] == '\\' {
				return k + 2
			}
		}
	}
	return len(s)
}

// c1 handles an 8-bit C1 control encoded as UTF-8. CSI (U+009B) and the string
// introducers consume their bodies; the rest are dropped alone.
func c1(s string, j int, r rune) int {
	switch r {
	case 0x9b:
		var discard strings.Builder
		return csi(&discard, s, j, j, false)
	case 0x9d, 0x90, 0x9f, 0x9e, 0x98:
		for k := j; k < len(s); k++ {
			if s[k] == 0x07 {
				return k + 1
			}
			if s[k] == 0x1b && k+1 < len(s) && s[k+1] == '\\' {
				return k + 2
			}
			if st, size := utf8.DecodeRuneInString(s[k:]); st == 0x9c {
				return k + size
			}
		}
		return len(s)
	default:
		return j
	}
}

func isBidiControl(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c
}

// isAllowedFormat keeps zero-width joiners and variation selectors, which compose
// visible glyphs, while dropping other invisible format characters.
func isAllowedFormat(r rune) bool {
	return r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f)
}
