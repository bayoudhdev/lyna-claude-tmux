package review

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// LuaString renders s as a double-quoted Lua string literal that evaluates
// to exactly s. Printable ASCII and valid UTF-8 text other than control
// characters are written as is so generated files stay readable; quotes and
// backslashes are escaped, and every other byte becomes a three-digit decimal
// escape, which Lua reads unambiguously even before a digit.
func LuaString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			writeDecimal(&b, c)
		case c < utf8.RuneSelf:
			b.WriteByte(c)
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if (r == utf8.RuneError && size == 1) || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
				for k := range size {
					writeDecimal(&b, s[i+k])
				}
			} else {
				b.WriteString(s[i : i+size])
			}
			i += size
			continue
		}
		i++
	}
	b.WriteByte('"')
	return b.String()
}

func writeDecimal(b *strings.Builder, c byte) {
	b.WriteByte('\\')
	d := strconv.Itoa(int(c))
	b.WriteString(strings.Repeat("0", 3-len(d)))
	b.WriteString(d)
}
