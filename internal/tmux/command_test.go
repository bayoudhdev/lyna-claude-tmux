package tmux

import (
	"strings"
	"testing"
)

func TestConfToken(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", "''"},
		{"word", "select-pane", "select-pane"},
		{"option", "@lt_role", "@lt_role"},
		{"target", "=main:1.%3", "=main:1.%3"},
		{"key with plus and comma", "a+b,c", "a+b,c"},
		{"space", "a b", "'a b'"},
		{"hash", "#{pane_id}", "'#{pane_id}'"},
		{"semicolon", ";", "';'"},
		{"single quote", "it's", `'it'\''s'`},
		{"double quote", `"x"`, `'"x"'`},
		{"dollar", "$HOME", "'$HOME'"},
		{"tilde", "~/x", "'~/x'"},
		{"brace", "{", "'{'"},
		{"backslash", `M-\`, `'M-\'`},
		{"non ascii", "é", "'é'"},
		{"control dropped", "a\x1bb", "'ab'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfToken(tc.in); got != tc.want {
				t.Fatalf("ConfToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzConfToken(f *testing.F) {
	for _, s := range []string{"", "plain", "a b", "it's", "#{x}", `\;`, "=s:1", "\x00'"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := ConfToken(in)
		if got == in {
			// Bare tokens must be non-empty and made only of inert characters.
			if in == "" {
				t.Fatal("empty token left bare")
			}
			for i := range len(in) {
				c := in[i]
				if !isWordByte(c) {
					t.Fatalf("token %q left bare with %q", in, c)
				}
			}
			return
		}
		if got != ConfQuote(in) {
			t.Fatalf("ConfToken(%q) = %q, neither bare nor ConfQuote", in, got)
		}
	})
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_@%./:=+,-", c) >= 0
}

func TestSeq(t *testing.T) {
	cases := []struct {
		name       string
		seq        Seq
		wantLine   string
		wantString string
	}{
		{
			name:       "one command",
			seq:        Cmd("resize-pane", "-Z"),
			wantLine:   "resize-pane -Z",
			wantString: "resize-pane -Z",
		},
		{
			name:       "chained",
			seq:        Cmd("split-window", "-h", "-c", "#{pane_current_path}").Then(Cmd("set-option", "-p", "@lt_role", "shell")),
			wantLine:   `split-window -h -c '#{pane_current_path}' \; set-option -p @lt_role shell`,
			wantString: "split-window -h -c '#{pane_current_path}' ; set-option -p @lt_role shell",
		},
		{
			name:       "data semicolon stays quoted",
			seq:        Cmd("send-keys", "-l", "a;b"),
			wantLine:   "send-keys -l 'a;b'",
			wantString: "send-keys -l 'a;b'",
		},
		{
			name:       "empty commands skipped",
			seq:        Seq{nil, Command{"detach-client"}, Command{}},
			wantLine:   "detach-client",
			wantString: "detach-client",
		},
		{name: "empty", seq: nil, wantLine: "", wantString: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.seq.Line(); got != tc.wantLine {
				t.Fatalf("Line() = %q, want %q", got, tc.wantLine)
			}
			if got := tc.seq.String(); got != tc.wantString {
				t.Fatalf("String() = %q, want %q", got, tc.wantString)
			}
		})
	}
}

func TestSeqThenDoesNotAlias(t *testing.T) {
	base := make(Seq, 1, 4)
	base[0] = Command{"a"}
	x := base.Then(Cmd("x"))
	y := base.Then(Cmd("y"))
	if x.String() != "a ; x" || y.String() != "a ; y" {
		t.Fatalf("Then shares backing storage: %q %q", x.String(), y.String())
	}
}
