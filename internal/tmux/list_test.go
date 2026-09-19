package tmux

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func row(fields ...string) string { return strings.Join(fields, fieldSep) }

// escapedRow is a row as tmux 3.4 prints it: the separator comes back as the
// four characters of its octal escape.
func escapedRow(fields ...string) string { return strings.Join(fields, escapedFieldSep) }

func TestSplitFields(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{name: "the separator itself", line: row("$1", "api"), want: []string{"$1", "api"}},
		{name: "the separator escaped", line: escapedRow("$1", "api"), want: []string{"$1", "api"}},
		{name: "both in one line", line: "$1" + fieldSep + "api" + escapedFieldSep + "duo", want: []string{"$1", "api", "duo"}},
		{name: "no separator", line: "$1", want: []string{"$1"}},
		{name: "empty fields", line: row("", ""), want: []string{"", ""}},
		{name: "nothing", line: "", want: []string{""}},
		// The escape is put back wherever it is; a backslash of a path that
		// starts something else is left alone.
		{name: "a backslash that is not the escape", line: row(`/work/a\b`, `\03`), want: []string{`/work/a\b`, `\03`}},
		{name: "a path holding the escape", line: escapedRow(`/work/a`, `b`), want: []string{"/work/a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitFields(tc.line); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SplitFields(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestFieldSep(t *testing.T) {
	if got := FieldSep("#{pane_id}", "#{session_id}"); got != "#{pane_id}"+fieldSep+"#{session_id}" {
		t.Fatalf("FieldSep() = %q", got)
	}
	if got := FieldSep("#{pane_id}"); got != "#{pane_id}" {
		t.Fatalf("FieldSep() of one field = %q", got)
	}
}

// TestParsePanesEscapedReply reads a reply from the tmux version that prints
// the separator as an octal escape: every row must still parse.
func TestParsePanesEscapedReply(t *testing.T) {
	fields := []string{
		"%3", "$1", "api", "@2", "1", "claude", "0", "/dev/ttys004", "4242", "/work/api", "claude", "Claude",
		"1", "1", "0", "120", "40", "claude", "waiting", "", "", "", "", "", "",
	}
	got := parsePanes(escapedRow(fields...) + "\n")
	if len(got) != 1 || got[0].ID != "%3" || got[0].SessionName != "api" || got[0].State != "waiting" {
		t.Fatalf("parsePanes() = %+v", got)
	}
}

func TestParseSessions(t *testing.T) {
	out := strings.Join([]string{
		row("$1", "api", "3", "1", "1757930000", "/work/api", "1", "/work/api", "strict", "duo"),
		row("$2", "notes", "1", "0", "1757930100", "/home/me", "", "", "", ""),
		"garbage row without separators",
		row("$3", "short"),
	}, "\n") + "\n"
	got := parseSessions(out)
	want := []Session{
		{ID: "$1", Name: "api", Windows: 3, Attached: 1, Created: time.Unix(1757930000, 0), Path: "/work/api", Managed: true, Project: "/work/api", Sandbox: "strict", Layout: "duo"},
		{ID: "$2", Name: "notes", Windows: 1, Attached: 0, Created: time.Unix(1757930100, 0), Path: "/home/me"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSessions() =\n%+v\nwant\n%+v", got, want)
	}
	if parseSessions("") != nil {
		t.Fatal("empty output should yield nil")
	}
}

func TestParsePanes(t *testing.T) {
	out := row("%3", "$1", "api", "@2", "1", "claude", "0", "/dev/ttys004", "4242", "/work/api", "claude", "Claude",
		"1", "1", "0", "120", "40", "claude", "waiting", "", "", "", "2", "a1=explore,b2=review",
		"/home/u/.claude/projects/-work-api/5f0c2a1e.jsonl") + "\n" +
		row("%4", "$1", "api", "@2", "1", "claude", "1", "/dev/ttys005", "4243", "/work/api", "zsh", "",
			"0", "1", "1", "80", "40", "", "", "", "", "", "", "", "") + "\n" +
		row("%5", "$1", "api", "@3", "2", "review-api", "0", "/dev/ttys006", "4244", "/work/api", "claude", "review-api",
			"1", "0", "0", "144", "40", "teammate", "busy", "review-api", "api-developer", "session-8f3c1d2a", "0", "",
			"/home/u/My Projects #2/.claude/projects/-work-api/9b1e.jsonl") + "\n" +
		// A row one field short is from a format this release did not ask
		// for, one without the transcript, and is skipped rather than read
		// into the wrong fields.
		row("%6", "$1", "api", "@3", "2", "review-api", "1", "/dev/ttys007", "4245", "/work/api", "claude", "",
			"0", "0", "0", "144", "40", "teammate", "", "", "", "", "", "") + "\n"
	got := parsePanes(out)
	want := []Pane{
		{ID: "%3", SessionID: "$1", SessionName: "api", WindowID: "@2", WindowIndex: 1, WindowName: "claude", PaneIndex: 0, TTY: "/dev/ttys004", PID: 4242, CurrentPath: "/work/api", CurrentCommand: "claude", Title: "Claude", Active: true, WindowActive: true, Width: 120, Height: 40, Role: "claude", State: "waiting", Subagents: "2", Running: "a1=explore,b2=review", Transcript: "/home/u/.claude/projects/-work-api/5f0c2a1e.jsonl"},
		{ID: "%4", SessionID: "$1", SessionName: "api", WindowID: "@2", WindowIndex: 1, WindowName: "claude", PaneIndex: 1, TTY: "/dev/ttys005", PID: 4243, CurrentPath: "/work/api", CurrentCommand: "zsh", WindowActive: true, Dead: true, Width: 80, Height: 40},
		{
			ID: "%5", SessionID: "$1", SessionName: "api", WindowID: "@3", WindowIndex: 2, WindowName: "review-api",
			PaneIndex: 0, TTY: "/dev/ttys006", PID: 4244, CurrentPath: "/work/api", CurrentCommand: "claude",
			Title: "review-api", Active: true, Width: 144, Height: 40, Role: "teammate", State: "busy",
			Agent: "review-api", AgentType: "api-developer", Team: "session-8f3c1d2a", Subagents: "0",
			Transcript: "/home/u/My Projects #2/.claude/projects/-work-api/9b1e.jsonl",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePanes() =\n%+v\nwant\n%+v", got, want)
	}
	if loc := got[0].Location(); loc != "api:1.0" {
		t.Fatalf("Location() = %q", loc)
	}
}

func TestQueryArgs(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) error
		want []string
	}{
		{
			name: "list panes all",
			call: func(c *Client) error { _, err := c.ListPanes(context.Background(), ""); return err },
			want: []string{"list-panes", "-a", "-F", strings.Join(paneFields, fieldSep)},
		},
		{
			name: "list panes session",
			call: func(c *Client) error { _, err := c.ListPanes(context.Background(), "=api"); return err },
			want: []string{"list-panes", "-s", "-t", "=api", "-F", strings.Join(paneFields, fieldSep)},
		},
		{
			name: "has session exact",
			call: func(c *Client) error { _, err := c.HasSession(context.Background(), "api"); return err },
			want: []string{"has-session", "-t", "=api:"},
		},
		{
			name: "capture with history",
			call: func(c *Client) error { _, err := c.CapturePane(context.Background(), "%1", 200); return err },
			want: []string{"capture-pane", "-p", "-e", "-t", "%1", "-S", "-200"},
		},
		{
			name: "capture visible only",
			call: func(c *Client) error { _, err := c.CapturePane(context.Background(), "%1", 0); return err },
			want: []string{"capture-pane", "-p", "-e", "-t", "%1"},
		},
		{
			name: "show pane option",
			call: func(c *Client) error { _, err := c.ShowOption(context.Background(), "-p", "%1", OptState); return err },
			want: []string{"show-options", "-pqv", "-t", "%1", "@lt_state"},
		},
		{
			name: "show global option",
			call: func(c *Client) error { _, err := c.ShowOption(context.Background(), "", "", OptParent); return err },
			want: []string{"show-options", "-qv", "@lt_parent"},
		},
		{
			name: "display format",
			call: func(c *Client) error { _, err := c.Display(context.Background(), "%1", "#{pane_tty}"); return err },
			want: []string{"display-message", "-p", "-t", "%1", "#{pane_tty}"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			if err := tc.call(New(Options{Executor: rec})); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rec.args[0], tc.want) {
				t.Fatalf("args = %q, want %q", rec.args[0], tc.want)
			}
		})
	}
}

func TestQueriesTolerateNoServer(t *testing.T) {
	rec := &recorder{res: Result{Stderr: []byte("no server running on /tmp/tmux-501/x"), ExitCode: 1}}
	c := New(Options{Executor: rec})
	ctx := context.Background()
	if s, err := c.ListSessions(ctx); err != nil || s != nil {
		t.Fatalf("ListSessions = %v, %v", s, err)
	}
	if p, err := c.ListPanes(ctx, ""); err != nil || p != nil {
		t.Fatalf("ListPanes = %v, %v", p, err)
	}
	if ok, err := c.HasSession(ctx, "x"); err != nil || ok {
		t.Fatalf("HasSession = %v, %v", ok, err)
	}
}
