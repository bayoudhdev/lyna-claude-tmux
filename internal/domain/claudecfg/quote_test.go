package claudecfg

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "''"},
		{name: "safe path stays bare", in: "/usr/local/bin/lyna-tmux", want: "/usr/local/bin/lyna-tmux"},
		{name: "safe punctuation", in: "a-b_c.d/e:f@g%h+i,j", want: "a-b_c.d/e:f@g%h+i,j"},
		{name: "space", in: "/My Tools/x", want: "'/My Tools/x'"},
		{name: "single quote", in: "it's", want: `'it'\''s'`},
		{name: "dollar and backtick", in: "$(id)`id`", want: "'$(id)`id`'"},
		{name: "glob and tilde", in: "~/*", want: "'~/*'"},
		{name: "semicolon and ampersand", in: "a;b&c", want: "'a;b&c'"},
		{name: "newline", in: "a\nb", want: "'a\nb'"},
		{name: "backslash", in: `a\b`, want: `'a\b'`},
	}
	sh, shErr := exec.LookPath("sh")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellQuote(tc.in)
			if got != tc.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if shErr != nil {
				return
			}
			// The contract that matters: a real shell reads the word back unchanged.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, sh, "-c", "printf '%s' "+got).Output()
			if err != nil {
				t.Fatalf("sh rejected %q: %v", got, err)
			}
			if string(out) != tc.in {
				t.Fatalf("sh read %q back as %q", tc.in, out)
			}
		})
	}
}

func FuzzShellQuote(f *testing.F) {
	for _, s := range []string{"", "a", "a b", "'", "''", `\'`, "$HOME", "\x00", "é", "a'b'c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		quoted := shellQuote(s)
		words, err := posixWords(quoted)
		if err != nil {
			t.Fatalf("shellQuote(%q) = %q does not parse: %v", s, quoted, err)
		}
		if len(words) != 1 || words[0] != s {
			t.Fatalf("shellQuote(%q) = %q parses as %q", s, quoted, words)
		}
	})
}

// posixWords splits a command line the way a POSIX shell tokenizes words
// built from bare characters, single quotes and backslash escapes. Anything
// that would expand (unquoted $, `, globs, ~) or separate commands is an error,
// so a quoting bug cannot hide behind an expansion that happens to be empty.
func posixWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
			}
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.IndexByte("\"$`*?[~#;&|<>(){}!\n=", c) >= 0:
			return nil, errors.New("unquoted special character " + string(c))
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
