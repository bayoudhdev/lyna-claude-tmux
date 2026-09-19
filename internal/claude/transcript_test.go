package claude_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
)

// transcriptTree lays out a Claude configuration directory with transcripts
// in it, and a directory beside it that no transcript path may reach. Every
// file holds its own name, so a read shows which file was opened.
type transcriptTree struct {
	home, projects, outside string
}

func newTranscriptTree(t *testing.T) transcriptTree {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := transcriptTree{home: filepath.Join(root, "claude"), outside: filepath.Join(root, "outside")}
	tr.projects = filepath.Join(tr.home, "projects")
	for _, f := range []string{
		"projects/-work-api/s.jsonl", "projects/-work-api/s/subagents/agent-a1.jsonl",
		"projects/-work-api/notes.txt", "settings.jsonl",
	} {
		tr.write(t, filepath.Join(tr.home, f), f+"\n")
	}
	tr.write(t, filepath.Join(tr.outside, "secret.jsonl"), "secret\n")
	tr.link(t, "s.jsonl", "projects/-work-api/inside.jsonl")
	tr.link(t, filepath.Join(tr.outside, "secret.jsonl"), "projects/-work-api/escape.jsonl")
	tr.link(t, "../../settings.jsonl", "projects/-work-api/up.jsonl")
	tr.link(t, "notes.txt", "projects/-work-api/text.jsonl")
	tr.link(t, tr.outside, "projects/elsewhere")
	if err := os.MkdirAll(filepath.Join(tr.projects, "-work-api", "dir.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(tr.projects, "-work-api", "pipe.jsonl"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tr
}

func (tr transcriptTree) write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (tr transcriptTree) link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(tr.home, name)); err != nil {
		t.Fatal(err)
	}
}

func (tr transcriptTree) at(name string) string { return filepath.Join(tr.home, name) }

func TestTranscriptFile(t *testing.T) {
	tr := newTranscriptTree(t)
	cases := []struct {
		name string
		path string
		want string // the file it resolves to; empty for a refusal
		err  error
	}{
		{name: "a transcript", path: tr.at("projects/-work-api/s.jsonl"), want: tr.at("projects/-work-api/s.jsonl")},
		{name: "a subagent transcript", path: tr.at("projects/-work-api/s/subagents/agent-a1.jsonl"), want: tr.at("projects/-work-api/s/subagents/agent-a1.jsonl")},
		{name: "a link to a transcript under projects", path: tr.at("projects/-work-api/inside.jsonl"), want: tr.at("projects/-work-api/s.jsonl")},
		{name: "a link out of the tree", path: tr.at("projects/-work-api/escape.jsonl"), err: claude.ErrNotTranscript},
		{name: "a link up to the configuration", path: tr.at("projects/-work-api/up.jsonl"), err: claude.ErrNotTranscript},
		{name: "a link to a file that is not a transcript", path: tr.at("projects/-work-api/text.jsonl"), err: claude.ErrNotTranscript},
		{name: "a linked directory out of the tree", path: tr.at("projects/elsewhere/secret.jsonl"), err: claude.ErrNotTranscript},
		{name: "a transcript outside projects", path: tr.at("settings.jsonl"), err: claude.ErrNotTranscript},
		{name: "a file outside the configuration", path: filepath.Join(tr.outside, "secret.jsonl"), err: claude.ErrNotTranscript},
		{name: "a file that is not a transcript", path: tr.at("projects/-work-api/notes.txt"), err: claude.ErrNotTranscript},
		// The paths that are not clean are spelled out rather than joined,
		// since joining would clean them.
		{name: "a path that climbs back in", path: tr.home + "/projects/-work-api/../-work-api/s.jsonl", err: claude.ErrNotTranscript},
		{name: "a path that climbs out", path: tr.home + "/projects/../settings.jsonl", err: claude.ErrNotTranscript},
		{name: "a doubled slash", path: tr.projects + "//-work-api/s.jsonl", err: claude.ErrNotTranscript},
		{name: "a relative path", path: "projects/-work-api/s.jsonl", err: claude.ErrNotTranscript},
		{name: "an extension alone", path: tr.at("projects/-work-api/.jsonl"), err: claude.ErrNotTranscript},
		{name: "nothing", path: "", err: claude.ErrNotTranscript},
		{name: "a transcript not written yet", path: tr.at("projects/-work-api/s/subagents/agent-b2.jsonl"), err: fs.ErrNotExist},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claude.TranscriptFile(tr.home, tc.path)
			if tc.err != nil {
				if !errors.Is(err, tc.err) || got != "" {
					t.Fatalf("TranscriptFile(%q) = %q, %v; want %v", tc.path, got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("TranscriptFile(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

// TestTranscriptFileLinkedConfiguration covers a configuration directory, and
// a projects directory, that are links themselves: the transcripts behind them
// are read, whichever of the two paths names them.
func TestTranscriptFileLinkedConfiguration(t *testing.T) {
	tr := newTranscriptTree(t)
	root := filepath.Dir(tr.home)
	linkedHome := filepath.Join(root, "linked-home")
	if err := os.Symlink(tr.home, linkedHome); err != nil {
		t.Fatal(err)
	}
	movedProjects := filepath.Join(root, "moved-projects")
	if err := os.MkdirAll(filepath.Join(movedProjects, "-work-web"), 0o700); err != nil {
		t.Fatal(err)
	}
	tr.write(t, filepath.Join(movedProjects, "-work-web", "w.jsonl"), "w\n")
	otherHome := filepath.Join(root, "other-home")
	if err := os.MkdirAll(otherHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(movedProjects, filepath.Join(otherHome, "projects")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, home, path, want string
	}{
		{name: "the home named by its link", home: linkedHome, path: tr.at("projects/-work-api/s.jsonl"), want: tr.at("projects/-work-api/s.jsonl")},
		{name: "the transcript named through the link", home: tr.home, path: filepath.Join(linkedHome, "projects/-work-api/s.jsonl"), want: tr.at("projects/-work-api/s.jsonl")},
		{name: "a projects directory that is a link", home: otherHome, path: filepath.Join(otherHome, "projects/-work-web/w.jsonl"), want: filepath.Join(movedProjects, "-work-web", "w.jsonl")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claude.TranscriptFile(tc.home, tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("TranscriptFile = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := claude.TranscriptFile(filepath.Join(root, "no-home"), tr.at("projects/-work-api/s.jsonl")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a home with no projects directory: %v", err)
	}
}

// TestReadTranscriptOpensNothingOutsideProjects reads every path a transcript
// may not be: each one is refused before anything is read. The link that
// escapes leads to a file that does exist, so a guard that let it through
// would read it; the pipe would be opened, and is refused as what it is.
func TestReadTranscriptOpensNothingOutsideProjects(t *testing.T) {
	tr := newTranscriptTree(t)
	cases := []struct {
		name string
		path string
	}{
		{name: "a link out of the tree", path: tr.at("projects/-work-api/escape.jsonl")},
		{name: "a link up to the configuration", path: tr.at("projects/-work-api/up.jsonl")},
		{name: "a linked directory out of the tree", path: tr.at("projects/elsewhere/secret.jsonl")},
		{name: "a transcript outside projects", path: tr.at("settings.jsonl")},
		{name: "a file outside the configuration", path: filepath.Join(tr.outside, "secret.jsonl")},
		{name: "a path that climbs out", path: tr.home + "/projects/../settings.jsonl"},
		{name: "a relative path", path: "../outside/secret.jsonl"},
		{name: "a file that is not a transcript", path: tr.at("projects/-work-api/notes.txt")},
		{name: "a directory named like a transcript", path: tr.at("projects/-work-api/dir.jsonl")},
		{name: "a pipe named like a transcript", path: tr.at("projects/-work-api/pipe.jsonl")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claude.ReadTranscript(tr.home, tc.path, claude.TranscriptCursor{})
			if !errors.Is(err, claude.ErrNotTranscript) || len(got.Data) != 0 {
				t.Fatalf("ReadTranscript(%q) = %q, %v; want %v", tc.path, got.Data, err, claude.ErrNotTranscript)
			}
		})
	}
}

// TestReadTranscriptFollows reads a transcript the way a reader follows one
// while the agent writes it: only what was appended, and from the start again
// when the file shrank or was replaced.
func TestReadTranscriptFollows(t *testing.T) {
	tr := newTranscriptTree(t)
	path := tr.at("projects/-work-api/live.jsonl")
	type step struct {
		name string
		// change is done to the file before the read.
		change      func(t *testing.T)
		wantData    string
		wantRestart bool
	}
	appendLine := func(s string) func(t *testing.T) {
		return func(t *testing.T) {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	steps := []step{
		{name: "the first read", change: appendLine("one\ntw"), wantData: "one\ntw"},
		{name: "what was appended", change: appendLine("o\nthree\n"), wantData: "o\nthree\n"},
		{name: "nothing new", change: func(*testing.T) {}},
		{name: "a file that shrank", change: func(t *testing.T) { tr.write(t, path, "new\n") }, wantData: "new\n", wantRestart: true},
		{name: "appended after the restart", change: appendLine("more\n"), wantData: "more\n"},
		{
			name: "a file replaced by a longer one", wantData: "replaced and longer than before\n", wantRestart: true,
			change: func(t *testing.T) {
				next := path + ".tmp"
				tr.write(t, next, "replaced and longer than before\n")
				if err := os.Rename(next, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	var cur claude.TranscriptCursor
	for _, st := range steps {
		st.change(t)
		got, err := claude.ReadTranscript(tr.home, path, cur)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if string(got.Data) != st.wantData || got.Restart != st.wantRestart || got.More {
			t.Fatalf("%s: read %q restart %v more %v; want %q restart %v", st.name, got.Data, got.Restart, got.More, st.wantData, st.wantRestart)
		}
		cur = got.Next
	}
}

// TestReadTranscriptBounded reads a transcript far longer than one read: it
// comes in reads of MaxTranscriptRead, each saying whether more is waiting,
// and adds up to the whole file.
func TestReadTranscriptBounded(t *testing.T) {
	tr := newTranscriptTree(t)
	path := tr.at("projects/-work-api/long.jsonl")
	line := strings.Repeat("x", 1023) + "\n"
	whole := strings.Repeat(line, 2*claude.MaxTranscriptRead/len(line)+3)
	tr.write(t, path, whole)
	var got bytes.Buffer
	var cur claude.TranscriptCursor
	for reads := 1; ; reads++ {
		chunk, err := claude.ReadTranscript(tr.home, path, cur)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk.Data) > claude.MaxTranscriptRead {
			t.Fatalf("read %d returned %d bytes", reads, len(chunk.Data))
		}
		got.Write(chunk.Data)
		cur = chunk.Next
		if !chunk.More {
			if reads != 3 {
				t.Fatalf("%d reads, want 3", reads)
			}
			break
		}
	}
	if got.String() != whole {
		t.Fatalf("read %d bytes, want %d", got.Len(), len(whole))
	}
}
