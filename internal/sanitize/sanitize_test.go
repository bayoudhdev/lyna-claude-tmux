package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTerminal(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain ascii", input: "hello world", want: "hello world"},
		{name: "keeps newline and tab", input: "a\n\tb", want: "a\n\tb"},
		{name: "keeps sgr color", input: "\x1b[31mred\x1b[0m", want: "\x1b[31mred\x1b[0m"},
		{name: "keeps truecolor sgr", input: "\x1b[38;2;255;0;0mx\x1b[m", want: "\x1b[38;2;255;0;0mx\x1b[m"},
		{name: "keeps colon sgr", input: "\x1b[4:3mx", want: "\x1b[4:3mx"},
		{name: "drops private sgr", input: "\x1b[?31mx", want: "x"},
		{name: "drops oversized sgr", input: "\x1b[" + strings.Repeat("1;", 40) + "mx", want: "x"},
		{name: "drops cursor movement", input: "a\x1b[2Jb\x1b[10;20Hc", want: "abc"},
		{name: "drops osc title bel", input: "\x1b]0;pwned\x07ok", want: "ok"},
		{name: "drops osc 52 clipboard st", input: "\x1b]52;c;ZXZpbA==\x1b\\ok", want: "ok"},
		{name: "drops osc 8 hyperlink", input: "\x1b]8;;https://evil\x1b\\link\x1b]8;;\x1b\\", want: "link"},
		{name: "drops dcs", input: "\x1bPq#0;2;0;0;0\x1b\\ok", want: "ok"},
		{name: "drops apc", input: "\x1b_Gf=100;AAAA\x1b\\ok", want: "ok"},
		{name: "unterminated osc swallows rest", input: "ok\x1b]0;never ends", want: "ok"},
		{name: "drops carriage return", input: "safe\rEVIL", want: "safeEVIL"},
		{name: "drops c0 and del", input: "a\x00b\x07c\x08d\x7fe", want: "abcde"},
		{name: "drops two byte escapes", input: "a\x1b7b\x1bcc\x1b(Bd", want: "abcd"},
		{name: "drops trailing esc", input: "abc\x1b", want: "abc"},
		{name: "drops c1 csi", input: "a\u009b2Jb", want: "ab"},
		{name: "drops c1 osc", input: "a\u009d0;t\u009cb", want: "ab"},
		{name: "drops lone c1", input: "a\u0085b", want: "ab"},
		{name: "drops bidi override", input: "admin\u202etxt.exe", want: "admintxt.exe"},
		{name: "keeps unicode", input: "café │ ● 東京", want: "café │ ● 東京"},
		{name: "keeps emoji zwj", input: "\U0001F468\u200d\U0001F4BB", want: "\U0001F468\u200d\U0001F4BB"},
		{name: "drops invalid utf8", input: "a\xffb\xc3", want: "ab"},
		{name: "malformed csi resumes", input: "\x1b[31\x01mx", want: "mx"},
		{name: "esc esc", input: "\x1b\x1b[31mx", want: "[31mx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Terminal(tc.input); got != tc.want {
				t.Fatalf("Terminal(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPlainAndLine(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantPlain string
		wantLine  string
	}{
		{name: "strips sgr", input: "\x1b[1mbold\x1b[0m", wantPlain: "bold", wantLine: "bold"},
		{name: "folds lines", input: " one\ntwo\tthree ", wantPlain: " one\ntwo\tthree ", wantLine: "one two three"},
		{name: "strips title", input: "\x1b]2;x\x07name", wantPlain: "name", wantLine: "name"},
		{name: "empty", input: "", wantPlain: "", wantLine: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plain(tc.input); got != tc.wantPlain {
				t.Fatalf("Plain = %q, want %q", got, tc.wantPlain)
			}
			if got := Line(tc.input); got != tc.wantLine {
				t.Fatalf("Line = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

func assertSafe(t *testing.T, input, out string, keepSGR bool) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Fatalf("invalid utf8 output for %q: %q", input, out)
	}
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c == 0x1b {
			if !keepSGR {
				t.Fatalf("escape survived Plain for %q: %q", input, out)
			}
			j := i + 2
			if j > len(out) || out[i+1] != '[' {
				t.Fatalf("non-CSI escape survived for %q: %q", input, out)
			}
			for j < len(out) && (out[j] >= '0' && out[j] <= '9' || out[j] == ';' || out[j] == ':') {
				j++
			}
			if j >= len(out) || out[j] != 'm' {
				t.Fatalf("non-SGR escape survived for %q: %q", input, out)
			}
			i = j
			continue
		}
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			t.Fatalf("control byte %#x survived for %q: %q", c, input, out)
		}
	}
	for _, r := range out {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("C1 control %U survived for %q", r, input)
		}
	}
}

func FuzzTerminal(f *testing.F) {
	seeds := []string{"", "plain", "\x1b[31mred", "\x1b]52;c;AA\x07", "\x1bP\x1b\\", "\u009b1m", "\xff\xfe", "\x1b[", "a\rb"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out := Terminal(input)
		assertSafe(t, input, out, true)
		if again := Terminal(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", input, out, again)
		}
		plain := Plain(input)
		assertSafe(t, input, plain, false)
		if again := Plain(plain); again != plain {
			t.Fatalf("Plain not idempotent: %q -> %q -> %q", input, plain, again)
		}
	})
}
