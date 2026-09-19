package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// transcriptHome lays a projects directory out under the host's own Claude
// configuration directory, which is where a transcript may be read from.
func transcriptHome(t *testing.T, h *testHost) string {
	t.Helper()
	home := filepath.Join(h.root, ".claude")
	if err := os.MkdirAll(filepath.Join(home, "projects", "-work-api"), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

// promptLine is a prompt as Claude Code writes it, a plain string of content.
func promptLine(text string) string {
	return `{"type":"user","message":{"role":"user","content":"` + text +
		`"},"uuid":"u-` + text + `","timestamp":"2026-09-15T12:00:00.000Z"}` + "\n"
}

// setTranscript stores a transcript path on a pane, the way the SessionStart
// hook stores it.
func setTranscript(t *testing.T, s *Server, pane, path string) {
	t.Helper()
	if _, err := s.Client.Batch(tmuxtest.Context(t), tmux.Command{"set-option", "-p", "-t", pane, tmux.OptTranscript, path}); err != nil {
		t.Fatal(err)
	}
}

// paneOf is the first pane of a workspace.
func paneOf(t *testing.T, s *Server, name string) string {
	t.Helper()
	panes, err := s.Client.ListPanes(tmuxtest.Context(t), tmux.ExactSession(name))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) == 0 {
		t.Fatalf("workspace %s has no pane", name)
	}
	return panes[0].ID
}

// TestOpenTranscriptRefusals covers every request the reader refuses rather
// than opening on: a pane that is not one, a pane of another workspace, a pane
// no session has started in, a transcript that may not be read, and a
// workspace the request cannot name.
func TestOpenTranscriptRefusals(t *testing.T) {
	cases := []struct {
		name string
		// outside runs the request from outside tmux.
		outside bool
		session string
		// pane names the pane of the request; "scene" is the pane of the
		// workspace the scene opened, and "other" a pane of another workspace.
		pane string
		// transcript is stored on the scene's pane before the request; "none"
		// leaves the pane without one.
		transcript string
		agent      string
		want       error
		says       string
	}{
		{name: "no pane at all", want: ErrTranscriptPane, says: "pass --to"},
		{name: "a pane id that is not one", pane: "nope", want: ErrTranscriptPane},
		{name: "a pane of no server", pane: "%4040", want: ErrTranscriptPane, says: "is not a pane of"},
		{name: "a pane of another workspace", pane: "other", want: ErrTranscriptPane, says: "is not a pane of"},
		{name: "a pane no session has started in", pane: "scene", transcript: "none", want: ErrNoTranscript},
		{name: "a transcript outside the projects directory", pane: "scene", transcript: "outside", want: claude.ErrNotTranscript},
		{name: "a subagent identifier that is not one", pane: "scene", agent: "../../etc/passwd", want: ErrNoTranscript},
		{name: "outside tmux with no workspace named", outside: true, pane: "scene", want: ErrTranscriptPane, says: "pass --session"},
		{name: "outside tmux naming a workspace that is not running", outside: true, session: "gone", pane: "scene", want: ErrNoWorkspace},
		{name: "a name no workspace can carry", outside: true, session: "../etc", pane: "scene", says: "invalid session name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tmuxtest.Context(t)
			h := newTestHost(t)
			s := openServer(t, h)
			home := transcriptHome(t, h)
			scene := openTeammateScene(t, h, s, 240, 60)
			startWorkspace(t, s, "docs")
			path := writeTranscript(t, home, "lead.jsonl", promptLine("hello"))
			switch tc.transcript {
			case "none":
			case "outside":
				outside := filepath.Join(h.root, "elsewhere.jsonl")
				if err := os.WriteFile(outside, []byte(promptLine("hello")), 0o600); err != nil {
					t.Fatal(err)
				}
				setTranscript(t, s, scene.pane, outside)
			default:
				setTranscript(t, s, scene.pane, path)
			}
			pane := tc.pane
			switch tc.pane {
			case "scene":
				pane = scene.pane
			case "other":
				pane = paneOf(t, s, "docs")
			}
			if tc.outside {
				h.env["TMUX"], h.env["TMUX_PANE"] = "", ""
				h.refreshEnviron()
			}
			_, err := OpenTranscript(ctx, h.Host, TranscriptRequest{Session: tc.session, Pane: pane, Agent: tc.agent})
			switch {
			case err == nil:
				t.Fatal("the reader opened on a request it should refuse")
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("error %v, want %v", err, tc.want)
			case tc.says != "" && !strings.Contains(err.Error(), tc.says):
				t.Fatalf("error %v, want one saying %q", err, tc.says)
			}
		})
	}
}

// TestOpenTranscriptReads opens on the pane of an agent and reads its
// transcript: what it holds, then only what it gained.
func TestOpenTranscriptReads(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	home := transcriptHome(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	path := writeTranscript(t, home, "lead.jsonl", promptLine("read the options")+usageLine("m1", 5))
	setTranscript(t, s, scene.pane, path)

	view, err := OpenTranscript(ctx, h.Host, TranscriptRequest{Pane: scene.pane})
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	if view.Options.Title == "" || view.Read == nil || view.Signal == nil || view.Watch == nil {
		t.Fatalf("the view is %+v", view.Options)
	}
	got := view.Read(ctx)
	if got.Err != nil || got.Restart {
		t.Fatalf("the first reading is %v, restart %v", got.Err, got.Restart)
	}
	if len(got.Entries) != 2 || got.Entries[0].Who != transcript.User || got.Entries[0].Text != "read the options" {
		t.Fatalf("the first reading holds %+v", got.Entries)
	}
	if got.Entries[1].Who != transcript.Assistant || got.Entries[1].Text != "ok" {
		t.Fatalf("the answer of the first reading is %+v", got.Entries[1])
	}
	// A reading of a transcript that gained nothing holds nothing, and the one
	// after it holds what was appended and nothing before it.
	if again := view.Read(ctx); len(again.Entries) != 0 || again.Err != nil {
		t.Fatalf("a reading of a transcript that gained nothing holds %+v, %v", again.Entries, again.Err)
	}
	appendTranscript(t, path, promptLine("and the list"))
	next := view.Read(ctx)
	if len(next.Entries) != 1 || next.Entries[0].Text != "and the list" {
		t.Fatalf("the reading after the write holds %+v", next.Entries)
	}
}

// TestOpenTranscriptKeepsAPartialLine holds a line that is not whole yet until
// its newline arrives, so no half written entry is ever shown.
func TestOpenTranscriptKeepsAPartialLine(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	home := transcriptHome(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	whole := promptLine("the whole line")
	path := writeTranscript(t, home, "lead.jsonl", whole[:len(whole)-10])
	setTranscript(t, s, scene.pane, path)

	view, err := OpenTranscript(ctx, h.Host, TranscriptRequest{Pane: scene.pane})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.Read(ctx); len(got.Entries) != 0 || got.Err != nil {
		t.Fatalf("a line that is not whole yet was read as %+v, %v", got.Entries, got.Err)
	}
	appendTranscript(t, path, whole[len(whole)-10:])
	got := view.Read(ctx)
	if len(got.Entries) != 1 || got.Entries[0].Text != "the whole line" {
		t.Fatalf("the finished line was read as %+v", got.Entries)
	}
}

// TestOpenTranscriptStartsOver reports a transcript rewritten under the agent,
// so a reader is never shown the end of one file under the start of another.
func TestOpenTranscriptStartsOver(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	home := transcriptHome(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	path := writeTranscript(t, home, "lead.jsonl", promptLine("first")+promptLine("second"))
	setTranscript(t, s, scene.pane, path)

	view, err := OpenTranscript(ctx, h.Host, TranscriptRequest{Pane: scene.pane})
	if err != nil {
		t.Fatal(err)
	}
	if got := view.Read(ctx); len(got.Entries) != 2 {
		t.Fatalf("the first reading holds %+v", got.Entries)
	}
	writeTranscript(t, home, "lead.jsonl", promptLine("again"))
	got := view.Read(ctx)
	if !got.Restart {
		t.Fatal("a transcript that was rewritten was read as one that grew")
	}
	if len(got.Entries) != 1 || got.Entries[0].Text != "again" {
		t.Fatalf("the reading after the rewrite holds %+v", got.Entries)
	}
}

// TestOpenTranscriptSubagent follows a subagent of a pane, whose transcript is
// derived from the pane's own and is often not written yet.
func TestOpenTranscriptSubagent(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	home := transcriptHome(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	parent := writeTranscript(t, home, "session-8f3c.jsonl", promptLine("start a subagent"))
	setTranscript(t, s, scene.pane, parent)

	view, err := OpenTranscript(ctx, h.Host, TranscriptRequest{Pane: scene.pane, Agent: "a3f2e1d0"})
	if err != nil {
		t.Fatalf("OpenTranscript: %v", err)
	}
	if view.Options.Title != "a3f2e1d0" {
		t.Fatalf("the reader is titled %q", view.Options.Title)
	}
	// Nothing written yet is nothing to show, and no failure to report.
	if got := view.Read(ctx); len(got.Entries) != 0 || got.Err != nil {
		t.Fatalf("the reading before the subagent wrote is %+v, %v", got.Entries, got.Err)
	}
	dir := filepath.Join(home, "projects", "-work-api", "session-8f3c", "subagents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "agent-a3f2e1d0.jsonl")
	if err := os.WriteFile(file, []byte(promptLine("find the leak")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := view.Read(ctx)
	if len(got.Entries) != 1 || got.Entries[0].Text != "find the leak" {
		t.Fatalf("the subagent reading holds %+v", got.Entries)
	}
}

// TestTranscriptReaderWatches pokes the refresh loop when the transcript is
// written, which is how the reader follows an agent without polling it.
func TestTranscriptReaderWatches(t *testing.T) {
	home := usageHome(t)
	path := writeTranscript(t, home, "lead.jsonl", promptLine("first"))
	r := &transcriptReader{claudeHome: home, path: path}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pokes := make(chan struct{}, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.watch(ctx, func() {
			select {
			case pokes <- struct{}{}:
			default:
			}
		})
	}()
	waitFor := func(what string) {
		t.Helper()
		select {
		case <-pokes:
		case <-time.After(10 * time.Second):
			t.Fatalf("no poke after %s", what)
		}
	}
	// The watch is armed from the first reading, which is also what a reader
	// that opened before the file existed does.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.read(ctx)
		r.mu.Lock()
		armed := r.armed
		r.mu.Unlock()
		if armed || time.Now().After(deadline) {
			if !armed {
				t.Fatal("the reader never watched the transcript")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	appendTranscript(t, path, promptLine("second"))
	waitFor("a write to the transcript")
	got := r.read(ctx)
	if len(got.Entries) != 1 || got.Entries[0].Text != "second" {
		t.Fatalf("the reading after the poke holds %+v", got.Entries)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch outlived its context")
	}
}

// TestTranscriptOf covers what the reader follows for a request: the pane's
// own transcript, the derived one of a subagent, and the agent each is named
// after.
func TestTranscriptOf(t *testing.T) {
	parent := "/home/dev/.claude/projects/-work-api/session-8f3c.jsonl"
	cases := []struct {
		name      string
		pane      tmux.Pane
		agent     string
		wantPath  string
		wantTitle string
	}{
		{
			name:      "the agent of the pane",
			pane:      tmux.Pane{Agent: "review-api", Transcript: parent},
			wantPath:  parent,
			wantTitle: "review-api",
		},
		{
			name:      "a pane with no agent name",
			pane:      tmux.Pane{Transcript: parent},
			wantPath:  parent,
			wantTitle: "api",
		},
		{
			name:      "a subagent of the pane",
			pane:      tmux.Pane{Agent: "review-api", Transcript: parent},
			agent:     "a3f2e1d0",
			wantPath:  "/home/dev/.claude/projects/-work-api/session-8f3c/subagents/agent-a3f2e1d0.jsonl",
			wantTitle: "a3f2e1d0",
		},
		{
			name:      "a pane no session has started in",
			pane:      tmux.Pane{Agent: "review-api"},
			wantTitle: "review-api",
		},
		{
			name:      "a subagent of a pane no session has started in",
			pane:      tmux.Pane{Agent: "review-api"},
			agent:     "a3f2e1d0",
			wantTitle: "a3f2e1d0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, title := transcriptOf(tc.pane, tc.agent, "api")
			if path != tc.wantPath || title != tc.wantTitle {
				t.Fatalf("transcriptOf = %q, %q; want %q, %q", path, title, tc.wantPath, tc.wantTitle)
			}
		})
	}
}

// TestPaneByID finds the pane of a request among the panes of the server.
func TestPaneByID(t *testing.T) {
	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2", Agent: "review-api"}}
	cases := []struct {
		name  string
		id    string
		want  string
		found bool
	}{
		{name: "a pane that is there", id: "%2", want: "review-api", found: true},
		{name: "a pane that is not", id: "%3"},
		{name: "no pane at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := paneByID(panes, tc.id)
			if ok != tc.found || got.Agent != tc.want {
				t.Fatalf("paneByID(%q) = %+v, %v", tc.id, got, ok)
			}
		})
	}
}
