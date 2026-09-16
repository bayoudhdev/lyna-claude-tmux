package review

import (
	"errors"
	"strings"
	"testing"
)

func TestLuaString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: `""`},
		{name: "plain", in: "/home/dev/.local/share/lyna-tmux/review/codediff.nvim", want: `"/home/dev/.local/share/lyna-tmux/review/codediff.nvim"`},
		{name: "quotes", in: `say "hi" it's`, want: `"say \"hi\" it's"`},
		{name: "backslash", in: `a\b\\`, want: `"a\\b\\\\"`},
		{name: "named escapes", in: "a\nb\rc\td", want: `"a\nb\rc\td"`},
		{name: "control bytes", in: "\x00\x01\x1b\x7f", want: `"\000\001\027\127"`},
		{name: "decimal before digit", in: "\x001", want: `"\0001"`},
		{name: "long bracket closer", in: "]]==]", want: `"]]==]"`},
		{name: "utf8 kept", in: "╱ ▸ é 日本", want: `"╱ ▸ é 日本"`},
		{name: "invalid utf8", in: "a\xffb\xc3", want: `"a\255b\195"`},
		{name: "c1 control", in: "\u0085", want: `"\194\133"`},
		{name: "line separators", in: "  ", want: `"\226\128\168\226\128\169"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LuaString(tc.in)
			if got != tc.want {
				t.Fatalf("LuaString(%q) = %s, want %s", tc.in, got, tc.want)
			}
			back, err := decodeLuaString(got)
			if err != nil || back != tc.in {
				t.Fatalf("decode(%s) = %q, %v; want %q", got, back, err, tc.in)
			}
		})
	}
}

func TestDecodeLuaStringRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{name: "unquoted", in: "abc"},
		{name: "unterminated", in: `"abc`},
		{name: "raw newline", in: "\"a\nb\""},
		{name: "dangling backslash", in: `"a\"`},
		{name: "decimal too large", in: `"\256"`},
		{name: "unknown escape", in: `"\q"`},
		{name: "quote inside", in: `"a"b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := decodeLuaString(tc.in); err == nil {
				t.Fatalf("decode(%q) = %q, want an error", tc.in, got)
			}
		})
	}
}

var errLuaLiteral = errors.New("not a Lua short string literal")

// decodeLuaString evaluates a Lua 5.1 double-quoted short string literal
// independently of LuaString: every standard escape is accepted, not only
// the ones LuaString writes.
func decodeLuaString(lit string) (string, error) {
	if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
		return "", errLuaLiteral
	}
	body := lit[1 : len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"' || c == '\n' || c == '\r':
			return "", errLuaLiteral
		case c != '\\':
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", errLuaLiteral
		}
		switch e := body[i]; e {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '\\', '"', '\'', '\n':
			b.WriteByte(e)
		default:
			if e < '0' || e > '9' {
				return "", errLuaLiteral
			}
			n := 0
			for k := 0; k < 3 && i < len(body) && body[i] >= '0' && body[i] <= '9'; k++ {
				n = n*10 + int(body[i]-'0')
				i++
			}
			i--
			if n > 255 {
				return "", errLuaLiteral
			}
			b.WriteByte(byte(n))
		}
	}
	return b.String(), nil
}

// FuzzLuaString checks the round trip: the literal is a single line, and it
// decodes back to exactly the input. The real Lua interpreter check lives in
// the review adapter's Neovim integration test.
func FuzzLuaString(f *testing.F) {
	for _, s := range []string{"", "plain", `"\`, "\x00123", "\n\r\t", "\xff\xfe", "╱", "\u0085", "]]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lit := LuaString(s)
		if strings.ContainsAny(lit, "\n\r") {
			t.Fatalf("LuaString(%q) spans lines: %q", s, lit)
		}
		back, err := decodeLuaString(lit)
		if err != nil || back != s {
			t.Fatalf("LuaString(%q) = %q decodes to %q, %v", s, lit, back, err)
		}
	})
}
