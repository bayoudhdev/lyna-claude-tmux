package doctor

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

func TestNewReport(t *testing.T) {
	cases := []struct {
		name    string
		results []Result
		want    Summary
		failed  bool
	}{
		{name: "nil results", want: Summary{}},
		{name: "all statuses", results: []Result{
			{Status: StatusOK}, {Status: StatusOK}, {Status: StatusWarn}, {Status: StatusFail}, {Status: StatusSkip}, {Status: "bogus"},
		}, want: Summary{OK: 2, Warn: 1, Fail: 1, Skip: 1}, failed: true},
		{name: "warnings do not fail", results: []Result{{Status: StatusWarn}}, want: Summary{Warn: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReport(tc.results)
			if r.Summary != tc.want || r.Failed() != tc.failed || r.Results == nil {
				t.Fatalf("NewReport() = %+v, failed %v; want %+v, failed %v", r, r.Failed(), tc.want, tc.failed)
			}
		})
	}
}

func TestWriteTextGolden(t *testing.T) {
	report := NewReport([]Result{
		{ID: "a", Title: "tmux", Status: StatusOK, Detail: "tmux 3.7c at /usr/bin/tmux", Fix: "never shown for ok"},
		{ID: "b", Title: "Option as Meta", Status: StatusWarn, Detail: "needs a setting", Fix: `Terminal > Settings > Profiles > Keyboard: enable "Use Option as Meta Key"`},
		{ID: "c", Title: "User namespaces", Status: StatusFail, Detail: "restricted", Fix: fixAppArmorBwrap},
		{ID: "d", Title: "Docker", Status: StatusSkip, Detail: "not needed", Fix: "never shown for skip"},
		{ID: "e", Title: "evil\x1b]0;pwned\x07", Status: StatusWarn, Detail: "line one\x1b[2J\nline two\r\x1b[31mred\x1b[0m", Fix: "run\x1b]52;c;ZXZpbA==\x07 this"},
		{ID: "f", Title: "Empty", Status: StatusOK},
	})
	// The widths are the two the report has to read well in: a terminal whose
	// width is not known, and one narrow enough to wrap almost every detail.
	for _, tc := range []struct {
		name   string
		width  int
		golden string
	}{
		{name: "width unknown", golden: "report.txt"},
		{name: "narrow terminal", width: 64, golden: "report-64.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteText(&buf, report, tc.width); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			if strings.ContainsAny(out, "\x1b\x07\r") {
				t.Fatalf("control characters reached the text report: %q", out)
			}
			// A fix written over several lines is a command to copy and is
			// never rewrapped, so its own lines are allowed to run long.
			verbatim := map[string]bool{}
			for _, line := range strings.Split(fixAppArmorBwrap, "\n") {
				verbatim[strings.TrimSpace(line)] = true
			}
			for _, line := range strings.Split(out, "\n") {
				trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "fix:"))
				if tc.width > 0 && len([]rune(line)) > tc.width && !verbatim[trimmed] {
					t.Errorf("line longer than %d cells: %q", tc.width, line)
				}
			}
			golden.Assert(t, tc.golden, buf.Bytes())
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWritersPropagateErrors(t *testing.T) {
	r := NewReport([]Result{{ID: "x", Title: "x", Status: StatusOK}})
	if err := WriteText(failingWriter{}, r, 0); err == nil {
		t.Fatal("WriteText ignored a write error")
	}
	if err := WriteJSON(failingWriter{}, r); err == nil {
		t.Fatal("WriteJSON ignored a write error")
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
		want  []string
	}{
		{"no limit", "one two three", 0, []string{"one two three"}},
		{"negative limit", "one two three", -8, []string{"one two three"}},
		{"shorter than the limit", "one two", 20, []string{"one two"}},
		{"exactly the limit", "one two", 7, []string{"one two"}},
		{"one cell too long", "one two", 6, []string{"one", "two"}},
		{"several lines", "a b c d e f", 3, []string{"a b", "c d", "e f"}},
		{"a word of its own is never cut", "see /very/long/path/to/a/file now", 10, []string{"see", "/very/long/path/to/a/file", "now"}},
		{"indentation is kept on every line", "  userns include if exists", 12, []string{"  userns", "  include if", "  exists"}},
		{"runes count as one cell", "éé éé éé", 5, []string{"éé éé", "éé"}},
		{"only spaces", "   ", 4, []string{"   "}},
		{"empty", "", 4, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrap(tc.in, tc.limit)
			if len(got) != len(tc.want) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("wrap(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
			}
		})
	}
}

func TestWrapLines(t *testing.T) {
	got := wrapLines([]string{"a b c", "d"}, 3)
	want := []string{"a b", "c", "d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrapLines() = %q, want %q", got, want)
	}
}

func TestWriteTextNarrowTerminalIsLeftAlone(t *testing.T) {
	// Under minRoom there is no column to wrap into, so the report is written
	// as if the width were unknown and the terminal wraps it itself.
	report := NewReport([]Result{{ID: "a", Title: "a title that is long", Status: StatusWarn, Detail: "one two three four five six"}})
	var narrow, unknown bytes.Buffer
	if err := WriteText(&narrow, report, 40); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(&unknown, report, 0); err != nil {
		t.Fatal(err)
	}
	if narrow.String() != unknown.String() {
		t.Fatalf("a terminal too narrow to wrap into changed the report:\n%s\nwant:\n%s", narrow.String(), unknown.String())
	}
}

func TestCleanLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"indentation kept", "profile {\n  userns,\n}", []string{"profile {", "  userns,", "}"}},
		{"escapes removed", "a\x1b[1mb\x1b[0m", []string{"ab"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanLines(tc.in)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
				t.Fatalf("cleanLines(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
