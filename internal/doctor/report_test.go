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
	var buf bytes.Buffer
	if err := WriteText(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control characters reached the text report: %q", out)
	}
	golden.Assert(t, "report.txt", buf.Bytes())
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWritersPropagateErrors(t *testing.T) {
	r := NewReport([]Result{{ID: "x", Title: "x", Status: StatusOK}})
	if err := WriteText(failingWriter{}, r); err == nil {
		t.Fatal("WriteText ignored a write error")
	}
	if err := WriteJSON(failingWriter{}, r); err == nil {
		t.Fatal("WriteJSON ignored a write error")
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
