package tmux

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestEscapeArg(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{";", `\;`},
		{"a;", `a\;`},
		{`a\;`, `a\\;`},
		{";;", `;\;`},
		{"a;b", "a;b"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := escapeArg(tc.in); got != tc.want {
				t.Fatalf("escapeArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConfQuote(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "abc", "'abc'"},
		{"empty", "", "''"},
		{"single quote", "it's", `'it'\''s'`},
		{"dollar and hash stay literal", "$HOME #{pane_id} ~", "'$HOME #{pane_id} ~'"},
		{"control chars dropped", "a\nb\tc\x1b", "'abc'"},
		{"backslash literal", `a\b`, `'a\b'`},
		{"unicode", "● lyna", "'● lyna'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfQuote(tc.in); got != tc.want {
				t.Fatalf("ConfQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatEscape(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "abc", "abc"},
		{"hash", "#", "##"},
		{"variable", "#{pane_id}", "##{pane_id}"},
		{"double hash", "a##b", "a####b"},
		{"style", "#[fg=red]", "#{?,,##}[fg=red]"},
		{"run before bracket", "##[", "###{?,,##}["},
		{"hash at end", "a#", "a##"},
		{"bracket alone", "[x]", "[x]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatEscape(tc.in); got != tc.want {
				t.Fatalf("FormatEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// expandEscaped models how tmux expands the text FormatEscape produces: "##"
// is one '#', a run of '#' followed by '[' is kept as it is, and hashLiteral
// is one '#' that is not rescanned. Anything else starting with '#' is outside
// the grammar and reported.
func expandEscaped(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '#' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if strings.HasPrefix(s[i:], hashLiteral) {
			b.WriteByte('#')
			i += len(hashLiteral)
			continue
		}
		if i+1 >= len(s) || s[i+1] != '#' {
			return "", false
		}
		j := i + 2
		for j < len(s) && s[j] == '#' {
			j++
		}
		if j < len(s) && s[j] == '[' {
			b.WriteString(s[i : j+1])
			i = j + 1
			continue
		}
		b.WriteByte('#')
		i += 2
	}
	return b.String(), true
}

func FuzzFormatEscape(f *testing.F) {
	for _, s := range []string{"", "#", "#[", "##[", "###[x", "#{a}", "a#b#[c", "[#]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, ok := expandEscaped(FormatEscape(in))
		if !ok || got != in {
			t.Fatalf("FormatEscape(%q) = %q expands to %q (ok=%v)", in, FormatEscape(in), got, ok)
		}
	})
}

func TestDrawEscape(t *testing.T) {
	cases := []struct{ name, in, want, wantTime string }{
		{"plain", "abc", "abc", "abc"},
		{"variable", "#{pane_id}", "##{pane_id}", "##{pane_id}"},
		{"style kept for drawing", "#[fg=red]", "##[fg=red]", "##[fg=red]"},
		{"percent", "100%", "100%", "100%%"},
		{"time and hash", "%Y #S", "%Y ##S", "%%Y ##S"},
		{"double percent", "%%", "%%", "%%%%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DrawEscape(tc.in); got != tc.want {
				t.Fatalf("DrawEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got := DrawEscapeTime(tc.in); got != tc.wantTime {
				t.Fatalf("DrawEscapeTime(%q) = %q, want %q", tc.in, got, tc.wantTime)
			}
		})
	}
}

func TestGlobEscape(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "/home/dev/.config/lyna-tmux/tmux.local.conf", "/home/dev/.config/lyna-tmux/tmux.local.conf"},
		{"class", "/Users/a[work]/x.conf", `/Users/a\[work]/x.conf`},
		{"star and question", "/w/*?.conf", `/w/\*\?.conf`},
		{"backslash", `/w/a\b`, `/w/a\\b`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GlobEscape(tc.in); got != tc.want {
				t.Fatalf("GlobEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", "''"},
		{"safe path", "/usr/local/bin/lyna-tmux", "/usr/local/bin/lyna-tmux"},
		{"space", "/Users/me/My Apps/lyna-tmux", "'/Users/me/My Apps/lyna-tmux'"},
		{"quote", "it's", `'it'\''s'`},
		{"assignment is quoted", "A=b", "'A=b'"},
		{"metachars", "$(rm -rf ~); `x` | &", "'$(rm -rf ~); `x` | &'"},
		{"glob", "*", "'*'"},
		{"backslash outside quotes", `a\b`, `'a'"\\"'b'`},
		{"leading and trailing backslash", `\x\`, `"\\"'x'"\\"`},
		{"only backslashes", `\\`, `"\\""\\"`},
		{"backslash before quote", `\'`, `"\\"''\'''`},
		{"leading percent quoted", "%self", "'%self'"},
		{"pane id quoted", "%12", "'%12'"},
		{"inner percent stays bare", "50%", "50%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShellQuote(tc.in); got != tc.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := ShellJoin("printf", "%s", "a b"); got != "printf '%s' 'a b'" {
		t.Fatalf("ShellJoin = %q", got)
	}
}

// TestShellQuoteRoundTrip runs quoted words through the real shells tmux may
// use as default-shell. A shell that is not installed is skipped by name.
func TestShellQuoteRoundTrip(t *testing.T) {
	inputs := []string{
		"", "plain", "with space", "it's", `back\slash`, `\`, `\\'`, `a\'b`, `'\'`, "$HOME", "`id`", "a\nb", "*", "-n",
		"é●", "A=b", "%self", "%12", "{a,b}", "~", "(x)", "$(id)", "a;b", "#c",
	}
	// Startup files are skipped: a user's shell configuration may print.
	shells := []struct {
		name string
		args []string
	}{
		{"sh", nil}, {"bash", []string{"--norc", "--noprofile"}}, {"zsh", []string{"-f"}}, {"fish", []string{"--no-config"}},
	}
	for _, shell := range shells {
		path, err := exec.LookPath(shell.name)
		if err != nil {
			t.Logf("%s not installed, skipped", shell.name)
			continue
		}
		t.Run(shell.name, func(t *testing.T) {
			for _, in := range inputs {
				args := append(slices.Clone(shell.args), "-c", "printf '%s' "+ShellQuote(in))
				out, err := exec.CommandContext(t.Context(), path, args...).Output()
				if err != nil {
					t.Fatalf("%q: %v", in, err)
				}
				if string(out) != in {
					t.Fatalf("round trip %q -> %q via %s", in, out, ShellQuote(in))
				}
			}
		})
	}
}

func FuzzShellQuote(f *testing.F) {
	for _, s := range []string{"", "a b", "'", `\`, "$(x)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		q := ShellQuote(in)
		if in == "" {
			if q != "''" {
				t.Fatalf("empty quoted as %q", q)
			}
			return
		}
		if isShellSafe(in) && in[0] != '%' {
			if q != in {
				t.Fatalf("safe word changed: %q -> %q", in, q)
			}
			return
		}
		// Undo the quoting and compare. Every run is '...' holding no
		// backslash (fish reads one there as an escape), \', or "\\".
		var b strings.Builder
		for i := 0; i < len(q); {
			switch {
			case q[i] == '\'':
				j := strings.IndexByte(q[i+1:], '\'')
				if j < 0 {
					t.Fatalf("unterminated quote in %q", q)
				}
				run := q[i+1 : i+1+j]
				if strings.ContainsRune(run, '\\') {
					t.Fatalf("backslash inside single quotes in %q", q)
				}
				b.WriteString(run)
				i += j + 2
			case strings.HasPrefix(q[i:], `\'`):
				b.WriteByte('\'')
				i += 2
			case strings.HasPrefix(q[i:], `"\\"`):
				b.WriteByte('\\')
				i += 4
			default:
				t.Fatalf("unexpected byte %q at %d in %q", q[i], i, q)
			}
		}
		if b.String() != in {
			t.Fatalf("decode(%q) = %q, want %q", q, b.String(), in)
		}
	})
}

func TestShellSplit(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr string
	}{
		{name: "empty", in: ""},
		{name: "blanks only", in: " \t\n "},
		{name: "one word", in: "--verbose", want: []string{"--verbose"}},
		{name: "blank runs", in: "  --model\t opus \n", want: []string{"--model", "opus"}},
		{name: "double quotes keep a space", in: `--append-system-prompt "be brief"`, want: []string{"--append-system-prompt", "be brief"}},
		{name: "single quotes keep a space", in: `--append-system-prompt 'be brief'`, want: []string{"--append-system-prompt", "be brief"}},
		{name: "quotes join to their neighbors", in: `a"b c"d'e f'g`, want: []string{"ab cde fg"}},
		{name: "quoted empty word", in: `'' "" x`, want: []string{"", "", "x"}},
		{name: "backslash escapes a blank", in: `be\ brief`, want: []string{"be brief"}},
		{name: "backslash escapes a quote", in: `it\'s`, want: []string{"it's"}},
		{name: "backslash before a multibyte character", in: `\é`, want: []string{"é"}},
		{name: "single quotes are literal", in: `'$HOME \n ` + "`id`'", want: []string{`$HOME \n ` + "`id`"}},
		{name: "double quotes unescape the shell specials", in: `"\"\\\$\` + "`\"", want: []string{`"\$` + "`"}},
		{name: "double quotes keep other backslashes", in: `"a\nb \x"`, want: []string{`a\nb \x`}},
		{name: "nothing is expanded", in: "$HOME *.go ~ `id` $(id)", want: []string{"$HOME", "*.go", "~", "`id`", "$(id)"}},
		{name: "prompt after a double dash", in: `-- "fix the tests"`, want: []string{"--", "fix the tests"}},
		{name: "unterminated single quote", in: `--model 'opus`, wantErr: "an opening ' has no closing '"},
		{name: "unterminated double quote", in: `--model "opus`, wantErr: `an opening " has no closing "`},
		{name: "unterminated double quote after an escape", in: `"a\"`, wantErr: `an opening " has no closing "`},
		{name: "trailing backslash", in: `--model opus\`, wantErr: "a backslash at the end escapes nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ShellSplit(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ShellSplit(%q) = %q, %v; want error %q", tc.in, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("ShellSplit(%q) = %#v, %v; want %#v", tc.in, got, err, tc.want)
			}
		})
	}
}

// FuzzShellSplit pins ShellSplit as the inverse of ShellJoin: any word list
// it reads out of a string, and any string quoted as one word, comes back
// unchanged, and no input panics.
func FuzzShellSplit(f *testing.F) {
	for _, s := range []string{"", "a b", "'", `"`, `\`, `'a b' "c\"d" e\ f`, "$(x)", `""`, "é●"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, err := ShellSplit(ShellQuote(in)); err != nil || !slices.Equal(got, []string{in}) {
			t.Fatalf("ShellSplit(ShellQuote(%q)) = %q, %v", in, got, err)
		}
		words, err := ShellSplit(in)
		if err != nil {
			if words != nil {
				t.Fatalf("ShellSplit(%q) returned words %q with error %v", in, words, err)
			}
			return
		}
		again, err := ShellSplit(ShellJoin(words...))
		if err != nil || !slices.Equal(again, words) || (again == nil) != (words == nil) {
			t.Fatalf("ShellSplit(ShellJoin(%q)) = %#v, %v", words, again, err)
		}
	})
}

func FuzzEscapeArg(f *testing.F) {
	for _, s := range []string{"", ";", "a;", `\;`, `\\;`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := unescapeLikeTmux(escapeArg(in))
		if got != in {
			t.Fatalf("round trip %q -> %q -> %q", in, escapeArg(in), got)
		}
	})
}

// unescapeLikeTmux mirrors cmd_parse_from_arguments for a single element and
// fails the round trip when the element would terminate the command.
func unescapeLikeTmux(arg string) string {
	if !strings.HasSuffix(arg, ";") {
		return arg
	}
	body := arg[:len(arg)-1]
	if strings.HasSuffix(body, `\`) {
		return body[:len(body)-1] + ";"
	}
	return "\x00terminated"
}
