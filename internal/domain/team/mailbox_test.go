package team_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

func inbox(t *testing.T, name string) team.Inbox {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "inbox", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := team.ParseInbox(data)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestParseInboxReadsAMailbox(t *testing.T) {
	in := inbox(t, "team-lead")
	if len(in.Messages) != 3 || in.Dropped != 0 {
		t.Fatalf("%d messages, %d dropped", len(in.Messages), in.Dropped)
	}
	cases := []struct {
		name    string
		message team.Message
		kind    string
		from    string
		summary string
		color   string
		read    bool
	}{
		{name: "a message that names its kind", message: in.Messages[0], kind: "message", from: "explore-git", color: "blue", read: true},
		{name: "a message that names none is a message", message: in.Messages[1], kind: "message", from: "review-api", summary: "Two handlers build queries unsafely", color: "green"},
		{name: "a message of another kind", message: in.Messages[2], kind: "task_handback", from: "tests"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.message
			if m.Type != tc.kind || m.From != tc.from || m.Summary != tc.summary || m.Color != tc.color || m.Read != tc.read {
				t.Fatalf("message %+v, want kind %q from %q summary %q color %q read %v", m, tc.kind, tc.from, tc.summary, tc.color, tc.read)
			}
			if m.Text == "" {
				t.Fatal("a message with no text is not one this file holds")
			}
			if _, ok := m.At(); !ok {
				t.Fatalf("the time %q cannot be read", m.Timestamp)
			}
		})
	}
	t.Run("two of the three are unread", func(t *testing.T) {
		if got := in.Unread(); got != 2 {
			t.Fatalf("Unread = %d, want 2", got)
		}
	})
	t.Run("the last entry is the last message", func(t *testing.T) {
		m, ok := in.Last()
		if !ok || m.From != "tests" {
			t.Fatalf("Last = %+v, %v", m, ok)
		}
	})
}

func TestParseInboxDropsWhatIsNotAMessage(t *testing.T) {
	in := inbox(t, "broken")
	if len(in.Messages) != 1 || in.Dropped != 3 {
		t.Fatalf("%d messages, %d dropped; want 1 and 3", len(in.Messages), in.Dropped)
	}
	m, _ := in.Last()
	if m.From != "tests" {
		t.Fatalf("the message kept is %+v", m)
	}
	if _, ok := m.At(); ok {
		t.Fatalf("a time nobody can read is reported as one: %q", m.Timestamp)
	}
}

func TestParseInboxOfAnEmptyMailbox(t *testing.T) {
	in := inbox(t, "empty")
	if len(in.Messages) != 0 || in.Dropped != 0 || in.Unread() != 0 {
		t.Fatalf("inbox %+v, want nothing at all", in)
	}
	if m, ok := in.Last(); ok {
		t.Fatalf("Last of an empty mailbox = %+v", m)
	}
}

func TestParseInboxRefusals(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantErr error
	}{
		{name: "not json at all", data: []byte("[")},
		{name: "an object where the file is a list", data: []byte(`{"messages":[]}`)},
		{name: "a file past the cap", data: append([]byte("[]"), make([]byte, team.MaxInboxSize)...), wantErr: team.ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := team.ParseInbox(tc.data)
			if err == nil {
				t.Fatalf("no error, inbox %+v", in)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestMessageAt(t *testing.T) {
	cases := []struct {
		name  string
		stamp string
		want  bool
	}{
		{name: "the time the product writes", stamp: "2026-09-10T09:42:13.004Z", want: true},
		{name: "a time with an offset", stamp: "2026-09-10T09:42:13+01:00", want: true},
		{name: "a date alone", stamp: "2026-09-10"},
		{name: "a number of milliseconds", stamp: "1789024519411"},
		{name: "no time at all", stamp: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := team.Message{Timestamp: tc.stamp}.At()
			if ok != tc.want {
				t.Fatalf("At(%q) = %v, %v", tc.stamp, got, ok)
			}
			if !ok && !got.IsZero() {
				t.Fatalf("At(%q) reported %v with no time to report", tc.stamp, got)
			}
		})
	}
}

func FuzzParseInbox(f *testing.F) {
	for _, name := range []string{"team-lead", "broken", "empty"} {
		data, err := os.ReadFile(filepath.Join("testdata", "inbox", name+".json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		in, err := team.ParseInbox(data)
		if err != nil {
			return
		}
		if in.Dropped < 0 {
			t.Fatalf("%d dropped entries", in.Dropped)
		}
		if in.Unread() > len(in.Messages) {
			t.Fatalf("%d unread of %d messages", in.Unread(), len(in.Messages))
		}
		last, ok := in.Last()
		if ok != (len(in.Messages) > 0) {
			t.Fatalf("Last = %+v, %v with %d messages", last, ok, len(in.Messages))
		}
	})
}
