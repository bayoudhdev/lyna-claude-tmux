package review

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode"
)

func TestParseLayout(t *testing.T) {
	cases := []struct {
		in      string
		want    Layout
		wantErr bool
	}{
		{in: "", want: LayoutDefault},
		{in: "default", want: LayoutDefault},
		{in: "inline", want: LayoutInline},
		{in: "side-by-side", want: LayoutSideBySide},
		{in: "Inline", wantErr: true},
		{in: "split", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseLayout(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLayout(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ParseLayout(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModesAndReservedWords(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "modes", got: modeNames(Modes()), want: []string{"changes", "staged", "revision", "pr", "history"}},
		{name: "reserved words", got: ReservedWords(), want: []string{"install", "install!", "pr", "file", "dir", "history", "merge"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func modeNames(ms []Mode) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return out
}

func TestValidateRevision(t *testing.T) {
	cases := []struct {
		name    string
		rev     string
		wantErr string // substring; empty means valid
	}{
		{name: "branch", rev: "main"},
		{name: "remote branch", rev: "origin/feature-x"},
		{name: "ancestor", rev: "HEAD~5"},
		{name: "parent", rev: "HEAD^2"},
		{name: "reflog", rev: "HEAD@{1}"},
		{name: "upstream", rev: "@{u}"},
		{name: "previous branch", rev: "@{-1}"},
		{name: "peel", rev: "v1.0^{commit}"},
		{name: "peel empty", rev: "v1.0^{}"},
		{name: "commit id", rev: "09d9ebef2cc5a5c04db7a349cd6c61bdf84ecc8e"},
		{name: "merge base open", rev: "main..."},
		{name: "merge base target", rev: "main...HEAD"},
		{name: "two dot range", rev: "main..feature"},
		{name: "index stage", rev: ":0:"},
		{name: "plus", rev: "release+hotfix"},
		{name: "max length", rev: strings.Repeat("a", MaxRevisionBytes)},
		{name: "empty", rev: "", wantErr: "revision is empty"},
		{name: "too long", rev: strings.Repeat("a", MaxRevisionBytes+1), wantErr: "longer than 256"},
		{name: "flag", rev: "-p", wantErr: "must not start with '-'"},
		{name: "long flag", rev: "--staged", wantErr: "must not start with '-'"},
		{name: "reserved install", rev: "install", wantErr: "subcommand name"},
		{name: "reserved install bang", rev: "install!", wantErr: "subcommand name"},
		{name: "reserved pr", rev: "pr", wantErr: "subcommand name"},
		{name: "reserved file", rev: "file", wantErr: "subcommand name"},
		{name: "reserved dir", rev: "dir", wantErr: "subcommand name"},
		{name: "reserved history", rev: "history", wantErr: "subcommand name"},
		{name: "reserved merge", rev: "merge", wantErr: "subcommand name"},
		{name: "leading dot", rev: "..main", wantErr: "must not start with '.'"},
		{name: "leading triple dot", rev: "...main", wantErr: "must not start with '.'"},
		{name: "leading slash", rev: "/main", wantErr: "must not start with '/'"},
		{name: "leading tilde", rev: "~main", wantErr: "must not start with '~'"},
		{name: "space", rev: "main HEAD", wantErr: "whitespace, control or non-ASCII"},
		{name: "tab", rev: "main\tx", wantErr: "whitespace, control or non-ASCII"},
		{name: "newline", rev: "main\n", wantErr: "whitespace, control or non-ASCII"},
		{name: "nul", rev: "ma\x00in", wantErr: "whitespace, control or non-ASCII"},
		{name: "delete", rev: "main\x7f", wantErr: "whitespace, control or non-ASCII"},
		{name: "unicode", rev: "brancé", wantErr: "whitespace, control or non-ASCII"},
		{name: "pipe", rev: "main|x", wantErr: `contains '|'`},
		{name: "backtick", rev: "a`id`", wantErr: "contains '`'"},
		{name: "dollar", rev: "$HOME", wantErr: "contains '$'"},
		{name: "percent", rev: "a%b", wantErr: "contains '%'"},
		{name: "glob", rev: "a*", wantErr: "contains '*'"},
		{name: "equals", rev: "a=b", wantErr: "contains '='"},
		{name: "four dots", rev: "a....b", wantErr: "run of 4 dots"},
		{name: "two ranges", rev: "a..b..c", wantErr: "more than one range operator"},
		{name: "range and merge base", rev: "a...b..c", wantErr: "more than one range operator"},
		{name: "nested braces", rev: "a@{b@{c}}", wantErr: "nests braces"},
		{name: "bare brace", rev: "a{b}", wantErr: "'{' must follow '@' or '^'"},
		{name: "unmatched close", rev: "a}", wantErr: "unmatched '}'"},
		{name: "unmatched open", rev: "a@{1", wantErr: "unmatched '{'"},
		{name: "range inside braces", rev: "a@{1..3}", wantErr: "range inside braces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidation(t, ValidateRevision(tc.rev), tc.wantErr)
		})
	}
}

func TestValidateBranch(t *testing.T) {
	cases := []struct {
		name    string
		branch  string
		wantErr string
	}{
		{name: "simple", branch: "main"},
		{name: "nested", branch: "release/2.x"},
		{name: "revision rule applies", branch: "-main", wantErr: "must not start with '-'"},
		{name: "colon", branch: "a:b", wantErr: `contains ":"`},
		{name: "tilde", branch: "a~1", wantErr: `contains "~"`},
		{name: "caret", branch: "a^", wantErr: `contains "^"`},
		{name: "reflog", branch: "a@{1}", wantErr: `contains "{"`},
		{name: "range", branch: "a..b", wantErr: `contains ".."`},
		{name: "double slash", branch: "a//b", wantErr: `contains "//"`},
		{name: "dot component", branch: "a/.b", wantErr: `contains "/."`},
		{name: "at", branch: "@", wantErr: "not a branch name"},
		{name: "trailing slash", branch: "a/", wantErr: "must not end with"},
		{name: "trailing dot", branch: "a.", wantErr: "must not end with"},
		{name: "lock suffix", branch: "a.lock", wantErr: "must not end with"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidation(t, ValidateBranch(tc.branch), tc.wantErr)
		})
	}
}

func TestValidatePath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "relative", path: "src/main.go"},
		{name: "spaces", path: "docs/my file.md"},
		{name: "pipe", path: "a|b"},
		{name: "quotes", path: `it's "quoted"`},
		{name: "unicode", path: "docs/résumé-日本.md"},
		{name: "pathspec magic", path: ":(glob)**/*.go"},
		{name: "dot", path: "."},
		{name: "max length", path: strings.Repeat("p", MaxPathBytes)},
		{name: "empty", path: "", wantErr: "path is empty"},
		{name: "too long", path: strings.Repeat("p", MaxPathBytes+1), wantErr: "longer than 4096"},
		{name: "invalid utf8", path: "a\xffb", wantErr: "not valid UTF-8"},
		{name: "leading dash", path: "-rf", wantErr: "must not start with '-'"},
		{name: "nul", path: "a\x00b", wantErr: "control character"},
		{name: "newline", path: "a\nb", wantErr: "control character"},
		{name: "escape", path: "a\x1b[31mb", wantErr: "control character"},
		{name: "delete", path: "a\x7f", wantErr: "control character"},
		{name: "c1 control", path: "a\u009bb", wantErr: "control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidation(t, ValidatePath(tc.path), tc.wantErr)
		})
	}
}

func TestRequestValidate(t *testing.T) {
	many := make([]string, MaxPaths+1)
	for i := range many {
		many[i] = "f"
	}
	big := strings.Repeat("x", MaxPathBytes)
	cases := []struct {
		name    string
		req     Request
		wantErr string
	}{
		{name: "zero value is changes", req: Request{}},
		{name: "changes with paths and layout", req: Request{Mode: ModeChanges, Paths: []string{"a b", "c|d"}, Layout: LayoutInline}},
		{name: "staged", req: Request{Mode: ModeStaged}},
		{name: "staged against revision", req: Request{Mode: ModeStaged, Revisions: []string{"HEAD~1"}}},
		{name: "revision one", req: Request{Mode: ModeRevision, Revisions: []string{"main"}}},
		{name: "revision two", req: Request{Mode: ModeRevision, Revisions: []string{"main", "HEAD"}}},
		{name: "revision merge base", req: Request{Mode: ModeRevision, Revisions: []string{"main...HEAD"}, Paths: []string{"src"}}},
		{name: "pr number only", req: Request{Mode: ModePR, PR: PR{Number: 42}}},
		{name: "pr full", req: Request{Mode: ModePR, PR: PR{Number: MaxPRNumber, Remote: "up.stream_1", Base: "release/2.x"}, Paths: []string{"a"}}},
		{name: "history", req: Request{Mode: ModeHistory}},
		{name: "history range reverse file", req: Request{Mode: ModeHistory, History: History{Range: "main..HEAD", Reverse: true}, Paths: []string{"src/my file.go"}}},
		{name: "max paths", req: Request{Paths: many[:MaxPaths]}},

		{name: "unknown mode", req: Request{Mode: "blame"}, wantErr: `unknown mode "blame"`},
		{name: "unknown layout", req: Request{Layout: "split"}, wantErr: `unknown layout "split"`},
		{name: "revisions in changes", req: Request{Revisions: []string{"main"}}, wantErr: "revisions are only used"},
		{name: "revisions in pr", req: Request{Mode: ModePR, PR: PR{Number: 1}, Revisions: []string{"main"}}, wantErr: "revisions are only used"},
		{name: "pr fields in changes", req: Request{PR: PR{Number: 1}}, wantErr: "pull request fields are only used"},
		{name: "history fields in revision", req: Request{Mode: ModeRevision, Revisions: []string{"a"}, History: History{Reverse: true}}, wantErr: "history fields are only used"},
		{name: "staged two revisions", req: Request{Mode: ModeStaged, Revisions: []string{"a", "b"}}, wantErr: "at most one revision"},
		{name: "staged bad revision", req: Request{Mode: ModeStaged, Revisions: []string{"-x"}}, wantErr: "must not start with '-'"},
		{name: "staged merge base", req: Request{Mode: ModeStaged, Revisions: []string{"main..."}}, wantErr: "does not take a range"},
		{name: "staged two dot range", req: Request{Mode: ModeStaged, Revisions: []string{"a..b"}}, wantErr: "does not take a range"},
		{name: "revision none", req: Request{Mode: ModeRevision}, wantErr: "one or two revisions, got 0"},
		{name: "revision three", req: Request{Mode: ModeRevision, Revisions: []string{"a", "b", "c"}}, wantErr: "one or two revisions, got 3"},
		{name: "revision bad single", req: Request{Mode: ModeRevision, Revisions: []string{"history"}}, wantErr: "subcommand name"},
		{name: "revision bad second", req: Request{Mode: ModeRevision, Revisions: []string{"a", "b c"}}, wantErr: "whitespace"},
		{name: "revision range with second", req: Request{Mode: ModeRevision, Revisions: []string{"main...", "HEAD"}}, wantErr: "cannot be combined with a second revision"},
		{name: "revision second range", req: Request{Mode: ModeRevision, Revisions: []string{"main", "a..b"}}, wantErr: "cannot be combined with a second revision"},
		{name: "pr zero", req: Request{Mode: ModePR}, wantErr: "outside 1..2147483647"},
		{name: "pr negative", req: Request{Mode: ModePR, PR: PR{Number: -3}}, wantErr: "outside 1..2147483647"},
		{name: "pr too big", req: Request{Mode: ModePR, PR: PR{Number: MaxPRNumber + 1}}, wantErr: "outside 1..2147483647"},
		{name: "pr remote dash", req: Request{Mode: ModePR, PR: PR{Number: 1, Remote: "-x"}}, wantErr: "must not start with '-'"},
		{name: "pr remote slash", req: Request{Mode: ModePR, PR: PR{Number: 1, Remote: "a/b"}}, wantErr: "contains '/'"},
		{name: "pr remote dot", req: Request{Mode: ModePR, PR: PR{Number: 1, Remote: ".."}}, wantErr: "not a remote name"},
		{name: "pr remote too long", req: Request{Mode: ModePR, PR: PR{Number: 1, Remote: strings.Repeat("r", MaxRemoteBytes+1)}}, wantErr: "longer than 256"},
		{name: "pr base refspec", req: Request{Mode: ModePR, PR: PR{Number: 1, Base: "main:evil"}}, wantErr: `contains ":"`},
		{name: "history bad range", req: Request{Mode: ModeHistory, History: History{Range: "install"}}, wantErr: "subcommand name"},
		{name: "history two paths", req: Request{Mode: ModeHistory, Paths: []string{"a", "b"}}, wantErr: "follows one file, got 2"},
		{name: "history backtick path", req: Request{Mode: ModeHistory, Paths: []string{"a`touch x`"}}, wantErr: "which Neovim would expand"},
		{name: "history dollar path", req: Request{Mode: ModeHistory, Paths: []string{"$HOME/x"}}, wantErr: "which Neovim would expand"},
		{name: "history backslash path", req: Request{Mode: ModeHistory, Paths: []string{`a\b`}}, wantErr: "which Neovim would expand"},
		{name: "history brace path", req: Request{Mode: ModeHistory, Paths: []string{"a{b"}}, wantErr: "which Neovim would expand"},
		{name: "history glob path", req: Request{Mode: ModeHistory, Paths: []string{"*.go"}}, wantErr: "which Neovim would expand"},
		{name: "history percent path", req: Request{Mode: ModeHistory, Paths: []string{"%"}}, wantErr: "must not start with '%'"},
		{name: "history hash path", req: Request{Mode: ModeHistory, Paths: []string{"#1"}}, wantErr: "must not start with '#'"},
		{name: "history angle path", req: Request{Mode: ModeHistory, Paths: []string{"<cfile>"}}, wantErr: "must not start with '<'"},
		{name: "history tilde path", req: Request{Mode: ModeHistory, Paths: []string{"~/x"}}, wantErr: "must not start with '~'"},
		{name: "changes allows expand characters", req: Request{Paths: []string{"$HOME", "a`b`", "~x", "*.go"}}},
		{name: "too many paths", req: Request{Paths: many}, wantErr: "at most 256 paths, got 257"},
		{name: "bad path", req: Request{Paths: []string{"ok", ""}}, wantErr: "path is empty"},
		{name: "paths total", req: Request{Paths: []string{big, big, big, big, big, big, big, big, "x"}}, wantErr: "add up to 32769 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidation(t, tc.req.Validate(), tc.wantErr)
		})
	}
}

func TestFargs(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want []string
	}{
		{name: "changes", req: Request{}, want: []string{"--exit-on-close"}},
		{
			name: "changes inline paths", req: Request{Mode: ModeChanges, Layout: LayoutInline, Paths: []string{"a b", "x|echo 1"}},
			want: []string{"--exit-on-close", "--inline", "--", "a b", "x|echo 1"},
		},
		{
			name: "staged", req: Request{Mode: ModeStaged, Layout: LayoutSideBySide},
			want: []string{"--exit-on-close", "--side-by-side", "--staged"},
		},
		{
			name: "staged revision paths", req: Request{Mode: ModeStaged, Revisions: []string{"HEAD~1"}, Paths: []string{"src"}},
			want: []string{"--exit-on-close", "--staged", "HEAD~1", "--", "src"},
		},
		{
			name: "one revision", req: Request{Mode: ModeRevision, Revisions: []string{"main"}},
			want: []string{"--exit-on-close", "main"},
		},
		{
			name: "two revisions", req: Request{Mode: ModeRevision, Revisions: []string{"v1", "v2"}, Paths: []string{"go.mod"}},
			want: []string{"--exit-on-close", "v1", "v2", "--", "go.mod"},
		},
		{
			name: "merge base", req: Request{Mode: ModeRevision, Revisions: []string{"main...HEAD"}},
			want: []string{"--exit-on-close", "main...HEAD"},
		},
		{
			name: "pr", req: Request{Mode: ModePR, PR: PR{Number: 7}},
			want: []string{"pr", "7", "--exit-on-close"},
		},
		{
			name: "pr full", req: Request{Mode: ModePR, PR: PR{Number: 1234, Remote: "upstream", Base: "main"}, Layout: LayoutInline, Paths: []string{"a", "b"}},
			want: []string{"pr", "1234", "--remote=upstream", "--base=main", "--exit-on-close", "--inline", "--", "a", "b"},
		},
		{
			name: "history", req: Request{Mode: ModeHistory},
			want: []string{"history", "--exit-on-close"},
		},
		{
			name: "history file", req: Request{Mode: ModeHistory, Paths: []string{"my file.go"}},
			want: []string{"history", "my file.go", "--exit-on-close"},
		},
		{
			name: "history full", req: Request{Mode: ModeHistory, History: History{Range: "main..HEAD", Reverse: true}, Paths: []string{"a.go"}, Layout: LayoutSideBySide},
			want: []string{"history", "main..HEAD", "a.go", "--reverse", "--exit-on-close", "--side-by-side"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.req.Fargs()
			if err != nil {
				t.Fatalf("Fargs() error = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Fargs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFargsRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		req  Request
	}{
		{name: "bad revision", req: Request{Mode: ModeRevision, Revisions: []string{"--help"}}},
		{name: "bad path", req: Request{Paths: []string{"-x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.req.Fargs()
			if !errors.Is(err, ErrInvalidRequest) || got != nil {
				t.Fatalf("Fargs() = %q, %v; want nil, ErrInvalidRequest", got, err)
			}
		})
	}
}

func assertValidation(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("no error, want one containing %q", wantErr)
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error %v does not wrap ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("error %q does not contain %q", err, wantErr)
	}
}

// FuzzValidateRevision checks the safety properties every accepted revision
// has: it cannot be read as a flag or a subcommand, stays one argument and
// carries nothing Neovim's expand() would substitute or run.
func FuzzValidateRevision(f *testing.F) {
	for _, s := range []string{"main", "HEAD~5", "main...", "a..b", "@{u}", "v1^{}", "-x", "install", "a b", "a`b`", "$X", "....", "a@{1..2}", "~x", "é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rev string) {
		if ValidateRevision(rev) != nil {
			return
		}
		if rev == "" || rev[0] == '-' || rev[0] == '~' || rev[0] == '.' {
			t.Fatalf("accepted %q with a forbidden first byte", rev)
		}
		if slices.Contains(ReservedWords(), rev) {
			t.Fatalf("accepted reserved word %q", rev)
		}
		for _, r := range rev {
			if unicode.IsSpace(r) || unicode.IsControl(r) || r >= 0x7f {
				t.Fatalf("accepted %q containing %q", rev, r)
			}
		}
		if strings.ContainsAny(rev, "`$%#<>*?[]\\'\"|;&=,!") {
			t.Fatalf("accepted %q containing a shell or expand character", rev)
		}
		if strings.Count(rev, "{") != strings.Count(rev, "}") {
			t.Fatalf("accepted %q with unbalanced braces", rev)
		}
		fargs, err := Request{Mode: ModeRevision, Revisions: []string{rev}}.Fargs()
		if err != nil || len(fargs) != 2 || fargs[1] != rev {
			t.Fatalf("accepted revision %q builds fargs %q, %v", rev, fargs, err)
		}
	})
}
