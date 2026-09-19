package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
)

// usageHome lays out a Claude configuration directory with a projects
// directory in it, which is the only place a transcript is read from.
func usageHome(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects", "-work-api"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// usageLine is one assistant message as Claude Code writes it, with the usage
// it reports. Every line is the same length for a given output count, so a
// test can rewrite one in place.
func usageLine(id string, out int64) string {
	return `{"parentUuid":null,"isSidechain":false,"type":"assistant","message":{"id":"` + id +
		`","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],` +
		`"usage":{"input_tokens":1,"cache_creation_input_tokens":10,"cache_read_input_tokens":100,"output_tokens":` +
		strconv.FormatInt(out, 10) + `}},"uuid":"u-` + id + `","timestamp":"2026-09-15T12:00:00.000Z"}` + "\n"
}

// writeTranscript writes a transcript under the projects directory and returns
// its path.
func writeTranscript(t *testing.T, home, name, body string) string {
	t.Helper()
	path := filepath.Join(home, "projects", "-work-api", name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendTranscript(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

// TestTranscriptUsageFill fills the rows of one reading: an agent with a
// transcript, one whose transcript is not written yet, one with none at all,
// and two rows reading the same file.
func TestTranscriptUsageFill(t *testing.T) {
	home := usageHome(t)
	lead := writeTranscript(t, home, "lead.jsonl", usageLine("m1", 5)+usageLine("m2", 7))
	mate := writeTranscript(t, home, "mate.jsonl", usageLine("m3", 40))
	rows := []team.Row{
		{Name: "api", Transcript: lead},
		{Name: "review-api", Transcript: mate},
		{Name: "build-api", Transcript: filepath.Join(home, "projects", "-work-api", "later.jsonl")},
		{Name: "write-docs"},
		{Name: "watcher", Transcript: lead},
	}
	u := NewTranscriptUsage(home)
	if err := u.Fill(context.Background(), rows); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	want := map[string]transcript.Usage{
		"api":        {Input: 2, CacheWrite: 20, CacheRead: 200, Output: 12},
		"review-api": {Input: 1, CacheWrite: 10, CacheRead: 100, Output: 40},
		"build-api":  {},
		"write-docs": {},
		"watcher":    {Input: 2, CacheWrite: 20, CacheRead: 200, Output: 12},
	}
	for _, r := range rows {
		if r.Usage.Sum != want[r.Name] {
			t.Fatalf("%s spent %+v, want %+v", r.Name, r.Usage.Sum, want[r.Name])
		}
	}
	if rows[0].Usage.Last.Output != 7 || rows[0].Usage.LastAt.IsZero() {
		t.Fatalf("the latest message of the lead is %+v at %v", rows[0].Usage.Last, rows[0].Usage.LastAt)
	}
}

// TestTranscriptUsageReadsWhatWasAppended proves the second reading reads only
// the new bytes: the line already read is rewritten in place with another
// count, and the tally keeps the count it read the first time.
func TestTranscriptUsageReadsWhatWasAppended(t *testing.T) {
	home := usageHome(t)
	path := writeTranscript(t, home, "lead.jsonl", usageLine("m1", 5))
	rows := []team.Row{{Name: "api", Transcript: path}}
	u := NewTranscriptUsage(home)
	if err := u.Fill(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].Usage.Sum.Output != 5 {
		t.Fatalf("first reading %+v", rows[0].Usage.Sum)
	}
	rewritten := usageLine("m1", 9)
	if len(rewritten) != len(usageLine("m1", 5)) {
		t.Fatal("the rewritten line is not the same length")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(rewritten), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path, usageLine("m2", 3))
	if err := u.Fill(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Usage.Sum.Output; got != 8 {
		t.Fatalf("second reading %d tokens out, want 8: the file was read again from its start", got)
	}
}

// TestTranscriptUsageStartsOver covers the transcripts a reading cannot carry
// on from: one that shrank and one replaced under the same name.
func TestTranscriptUsageStartsOver(t *testing.T) {
	cases := []struct {
		name    string
		again   func(t *testing.T, home, path string)
		wantOut int64
	}{
		{
			name:    "a transcript that shrank",
			again:   func(t *testing.T, home, _ string) { writeTranscript(t, home, "lead.jsonl", usageLine("m9", 2)) },
			wantOut: 2,
		},
		{
			name: "a transcript replaced under the same name",
			again: func(t *testing.T, _, path string) {
				next := path + ".tmp"
				if err := os.WriteFile(next, []byte(usageLine("m9", 2)+usageLine("m8", 40)+usageLine("m7", 40)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(next, path); err != nil {
					t.Fatal(err)
				}
			},
			wantOut: 82,
		},
		{
			name:    "a transcript that only grew",
			again:   func(t *testing.T, _, path string) { appendTranscript(t, path, usageLine("m9", 2)) },
			wantOut: 14,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := usageHome(t)
			path := writeTranscript(t, home, "lead.jsonl", usageLine("m1", 5)+usageLine("m2", 7))
			rows := []team.Row{{Name: "api", Transcript: path}}
			u := NewTranscriptUsage(home)
			if err := u.Fill(context.Background(), rows); err != nil {
				t.Fatal(err)
			}
			tc.again(t, home, path)
			if err := u.Fill(context.Background(), rows); err != nil {
				t.Fatal(err)
			}
			if got := rows[0].Usage.Sum.Output; got != tc.wantOut {
				t.Fatalf("%d tokens out, want %d", got, tc.wantOut)
			}
		})
	}
}

// TestTranscriptUsageForgetsWhatIsGone drops the state of a transcript no row
// names any more, so a rail that runs for a day holds the agents it has.
func TestTranscriptUsageForgetsWhatIsGone(t *testing.T) {
	home := usageHome(t)
	lead := writeTranscript(t, home, "lead.jsonl", usageLine("m1", 5))
	mate := writeTranscript(t, home, "mate.jsonl", usageLine("m2", 5))
	u := NewTranscriptUsage(home)
	both := []team.Row{{Name: "api", Transcript: lead}, {Name: "review-api", Transcript: mate}}
	if err := u.Fill(context.Background(), both); err != nil {
		t.Fatal(err)
	}
	if len(u.files) != 2 {
		t.Fatalf("following %d transcripts", len(u.files))
	}
	alone := []team.Row{{Name: "api", Transcript: lead}}
	if err := u.Fill(context.Background(), alone); err != nil {
		t.Fatal(err)
	}
	if len(u.files) != 1 || u.files[lead] == nil {
		t.Fatalf("following %d transcripts: %v", len(u.files), u.files)
	}
	// The teammate that came back is read from its start again, which is the
	// whole of what it spent and not the part it gained.
	appendTranscript(t, mate, usageLine("m3", 6))
	if err := u.Fill(context.Background(), both); err != nil {
		t.Fatal(err)
	}
	if got := both[1].Usage.Sum.Output; got != 11 {
		t.Fatalf("the teammate spent %d tokens out, want 11", got)
	}
}

// TestTranscriptUsageFailures covers what a reading does with a transcript it
// may not read, one that is not there, and a reading that was cut short.
func TestTranscriptUsageFailures(t *testing.T) {
	home := usageHome(t)
	outside := filepath.Join(filepath.Dir(home), "elsewhere.jsonl")
	if err := os.WriteFile(outside, []byte(usageLine("m1", 5)), 0o600); err != nil {
		t.Fatal(err)
	}
	good := writeTranscript(t, home, "lead.jsonl", usageLine("m1", 5))
	cases := []struct {
		name    string
		rows    []team.Row
		cancel  bool
		wantErr string
	}{
		{name: "a transcript of a session that has not written yet", rows: []team.Row{{Name: "build-api", Transcript: filepath.Join(home, "projects", "-work-api", "later.jsonl")}}},
		{name: "a file outside the projects directory", rows: []team.Row{{Name: "planted", Transcript: outside}}, wantErr: "usage of planted: " + claude.ErrNotTranscript.Error()},
		{name: "a path that is not a transcript", rows: []team.Row{{Name: "planted", Transcript: "/etc/passwd"}}, wantErr: "usage of planted"},
		{name: "a reading that was cut short", rows: []team.Row{{Name: "api", Transcript: good}}, cancel: true, wantErr: "context canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			err := NewTranscriptUsage(home).Fill(ctx, tc.rows)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Fill: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Fill = %v, want an error saying %q", err, tc.wantErr)
			}
			if tc.rows[0].Usage.Sum.Total() != 0 {
				t.Fatalf("a row that could not be read has a usage: %+v", tc.rows[0].Usage)
			}
		})
	}
}
