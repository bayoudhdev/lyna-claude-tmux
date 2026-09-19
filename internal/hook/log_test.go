package hook

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// teammateNames are the names the doctor reads the log for.
var teammateNames = []string{LogTeammate, LogTeammateUnplaced, LogTeammateFallback}

func TestLastLogEntry(t *testing.T) {
	later := fixedNow.Add(time.Minute)
	cases := []struct {
		name  string
		log   string
		want  LogEntry
		found bool
	}{
		{name: "an empty log"},
		{name: "no line of the names asked for", log: "2026-09-15T12:30:00Z hook Stop: pane is gone\n"},
		{
			name: "the last of several",
			log: "2026-09-15T12:30:00Z teammate: review-api opened in workspace api\n" +
				"2026-09-15T12:30:00Z hook Stop: pane is gone\n" +
				"2026-09-15T12:31:00Z teammate-fallback: write-docs opens the way the agent opens it: no server\n",
			want:  LogEntry{At: later, Name: LogTeammateFallback, Message: "write-docs opens the way the agent opens it: no server"},
			found: true,
		},
		{
			name:  "a message that holds the separator itself",
			log:   "2026-09-15T12:30:00Z teammate-unplaced: api opened: the window: gone\n",
			want:  LogEntry{At: fixedNow, Name: LogTeammateUnplaced, Message: "api opened: the window: gone"},
			found: true,
		},
		{
			name:  "a last line cut by a rotation or a crash",
			log:   "2026-09-15T12:30:00Z teammate: api opened in workspace api\n2026-09-15T12:3",
			want:  LogEntry{At: fixedNow, Name: LogTeammate, Message: "api opened in workspace api"},
			found: true,
		},
		{
			name:  "a line with no newline at the end",
			log:   "2026-09-15T12:30:00Z teammate: api opened in workspace api",
			want:  LogEntry{At: fixedNow, Name: LogTeammate, Message: "api opened in workspace api"},
			found: true,
		},
		{
			name: "lines that only look like entries",
			log: "not-a-time teammate: api\n" +
				"2026-09-15T12:30:00Z teammate api with no separator\n" +
				"2026-09-15T12:30:00Z : no name\n" +
				"2026-09-15T12:30:00Z teammate\n" +
				"2026-09-15T12:30:00Z\n",
		},
		{name: "a name that only starts like one asked for", log: "2026-09-15T12:30:00Z teammates: api\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := LastLogEntry([]byte(tc.log), teammateNames...)
			if found != tc.found || !got.At.Equal(tc.want.At) || got.Name != tc.want.Name || got.Message != tc.want.Message {
				t.Fatalf("LastLogEntry = %+v, %v; want %+v, %v", got, found, tc.want, tc.found)
			}
		})
	}
}

// TestLogReadsBack writes lines the way every part of lyna-tmux does and reads
// the last one back, which is the contract between the launcher and doctor.
func TestLogReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "lyna-tmux.log")
	Log(path, fixedNow, LogTeammate, "%s opened in workspace %s", "review-api", "api")
	Log(path, fixedNow, "hook Stop", "pane is gone")
	Log(path, fixedNow.Add(time.Second), LogTeammateFallback, "no server:\n%s", "\x1b[31mred")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := LastLogEntry(data, teammateNames...)
	want := LogEntry{At: fixedNow.Add(time.Second), Name: LogTeammateFallback, Message: "no server: red"}
	if !ok || !got.At.Equal(want.At) || got.Name != want.Name || got.Message != want.Message {
		t.Fatalf("read back %+v, %v; want %+v", got, ok, want)
	}
}

// FuzzLastLogEntry holds the reader to two promises whatever the log holds: an
// entry it returns is one of the names asked for and one line long, and a line
// written after the rest is the one it returns.
func FuzzLastLogEntry(f *testing.F) {
	f.Add([]byte("2026-09-15T12:30:00Z teammate: api opened\n"), "api opened")
	f.Add([]byte("2026-09-15T12:30:00Z teammate"), "")
	f.Add([]byte("\n\n: :\n2026-13-45T99:99:99Z teammate: x"), "a: b")
	f.Add([]byte{0xff, 0xfe, '\n'}, "\x00\x1b]52;c;x\x07")
	f.Fuzz(func(t *testing.T, data []byte, message string) {
		if got, ok := LastLogEntry(data, teammateNames...); ok {
			if !slices.Contains(teammateNames, got.Name) || strings.Contains(got.Message, "\n") {
				t.Fatalf("returned %+v", got)
			}
		}
		appended := append(append(append([]byte(nil), data...), '\n'), logLine(fixedNow, LogTeammate, message)...)
		got, ok := LastLogEntry(appended, teammateNames...)
		if !ok || got.Name != LogTeammate || got.Message != sanitize.Line(message) || !got.At.Equal(fixedNow) {
			t.Fatalf("the line written last reads back as %+v, %v", got, ok)
		}
	})
}
