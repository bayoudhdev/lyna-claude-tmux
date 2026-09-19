package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
)

// transcriptAt is a moment of the golden transcript, minutes before the fixed
// clock so the age in the footer never moves.
func transcriptAt(minutes int) time.Time {
	return fixedNow.Add(-time.Duration(minutes) * time.Minute)
}

// transcriptSample is one exchange as a transcript holds it: a prompt, an
// answer, two tool calls with their results, and a longer answer that wraps.
func transcriptSample() []transcript.Entry {
	return []transcript.Entry{
		{At: transcriptAt(9), Who: transcript.User, Text: "the rail shows no tokens for the review agent, find out why"},
		{At: transcriptAt(8), Who: transcript.Assistant, Text: "Reading the pane options first."},
		{At: transcriptAt(8), Who: transcript.Tool, Tool: "Read", Text: "/work/api/internal/tmux/options.go"},
		{At: transcriptAt(7), Who: transcript.Tool, Text: "24 lines"},
		{At: transcriptAt(6), Who: transcript.Tool, Tool: "Bash", Text: "go test ./internal/tmux/"},
		{At: transcriptAt(5), Who: transcript.Tool, Text: "exit status 1: list_test.go:41: pane 3 carries no transcript", Failed: true},
		{At: transcriptAt(4), Who: transcript.Assistant, Text: "The option is only written when a session starts, so a pane whose\nsession started before the hooks were installed carries none.\nStarting a session in it fills the rail."},
	}
}

// newTranscriptTest builds a reader of a size, with no update channel: the
// tests deliver readings as messages.
func newTranscriptTest(t testing.TB, width, height int, styles Styles) *TranscriptModel {
	t.Helper()
	return NewTranscript(TranscriptOptions{
		Styles: styles,
		Title:  "review-api",
		Width:  width,
		Height: height,
		Now:    clock,
	})
}

// reading is one update message carrying entries.
func reading(entries ...transcript.Entry) transcriptUpdateMsg {
	return transcriptUpdateMsg{update: TranscriptUpdate{Entries: entries, At: fixedNow}}
}

// TestTranscriptFrames draws one transcript at both golden sizes.
func TestTranscriptFrames(t *testing.T) {
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			m := newTranscriptTest(t, size.width, size.height, goldenStyles(t))
			r := drive(t, m, m.Init(), reading(transcriptSample()...))
			assertFrame(t, "transcript-"+size.name, r.model.(viewer), size.width, size.height, size.ansi)
		})
	}
}

// TestTranscriptStates draws what the reader shows besides a transcript in
// full: before the first reading, with nothing written, after a failure, when
// the reader was scrolled up, once the readings stopped, and in ascii.
func TestTranscriptStates(t *testing.T) {
	cases := []struct {
		name  string
		ascii bool
		// height draws the case at a size of its own; zero draws it at the
		// golden height, which the sample fits into with room to spare.
		height int
		msgs   []tea.Msg
	}{
		{name: "transcript-reading"},
		{name: "transcript-empty", msgs: []tea.Msg{reading()}},
		{
			name: "transcript-error",
			msgs: []tea.Msg{transcriptUpdateMsg{update: TranscriptUpdate{
				Err: errors.New("claude: not a transcript under the projects directory"), At: fixedNow,
			}}},
		},
		{
			// A view the sample overflows, scrolled up three lines: the header
			// counts what is below and the view stops following the end.
			name: "transcript-scrolled", height: 8,
			msgs: []tea.Msg{reading(transcriptSample()...), press("k"), press("k"), press("k")},
		},
		{name: "transcript-stopped", msgs: []tea.Msg{reading(transcriptSample()...), transcriptClosedMsg{}}},
		{name: "transcript-ascii", ascii: true, msgs: []tea.Msg{reading(transcriptSample()...)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			styles := goldenStyles(t)
			if tc.ascii {
				styles = testStyles(t, "lyna", theme.Depth16, "ascii")
			}
			height := 30
			if tc.height > 0 {
				height = tc.height
			}
			m := newTranscriptTest(t, 100, height, styles)
			r := drive(t, m, m.Init(), tc.msgs...)
			assertFrame(t, tc.name, r.model.(viewer), 100, height, false)
		})
	}
}

// TestTranscriptEntryLines pins how each kind of entry is drawn: who wrote it,
// the tool a call names, and a result that failed.
func TestTranscriptEntryLines(t *testing.T) {
	cases := []struct {
		name  string
		entry transcript.Entry
		want  []string
	}{
		{name: "a prompt", entry: transcript.Entry{Who: transcript.User, Text: "ship it"}, want: []string{"› ship it"}},
		{name: "an answer", entry: transcript.Entry{Who: transcript.Assistant, Text: "on it"}, want: []string{"● on it"}},
		{
			name:  "an answer over two paragraphs",
			entry: transcript.Entry{Who: transcript.Assistant, Text: "first\nsecond"},
			want:  []string{"● first", "  second"},
		},
		{
			name:  "a tool call",
			entry: transcript.Entry{Who: transcript.Tool, Tool: "Bash", Text: "go test ./..."},
			want:  []string{"  ▸ Bash go test ./..."},
		},
		{
			name:  "a tool result",
			entry: transcript.Entry{Who: transcript.Tool, Text: "ok"},
			want:  []string{"  └ ok"},
		},
		{
			name:  "a tool result that failed",
			entry: transcript.Entry{Who: transcript.Tool, Text: "exit status 1", Failed: true},
			want:  []string{"  └ exit status 1"},
		},
		{
			name:  "an empty answer",
			entry: transcript.Entry{Who: transcript.Assistant},
			want:  []string{"●"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTranscriptTest(t, 40, 10, goldenStyles(t))
			m.add(TranscriptUpdate{Entries: []transcript.Entry{tc.entry}})
			var got []string
			for _, l := range m.lines {
				var b strings.Builder
				for _, s := range l.segs {
					b.WriteString(s.text)
				}
				got = append(got, strings.TrimRight(b.String(), " "))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("drawn as %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d is %q, want %q", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestTranscriptWraps wraps a long answer in the room the width leaves, and
// rewraps it when the window is resized.
func TestTranscriptWraps(t *testing.T) {
	long := strings.Repeat("token ", 30)
	m := newTranscriptTest(t, 40, 12, goldenStyles(t))
	m.add(TranscriptUpdate{Entries: []transcript.Entry{{Who: transcript.Assistant, Text: long}}})
	wide := len(m.lines)
	if wide < 4 {
		t.Fatalf("a %d cell answer takes %d lines at 40 cells", len(long), wide)
	}
	for _, l := range m.lines {
		var b strings.Builder
		for _, s := range l.segs {
			b.WriteString(s.text)
		}
		if w := ansi.StringWidth(b.String()); w > 40 {
			t.Fatalf("a line is %d cells wide: %q", w, b.String())
		}
	}
	next, _ := m.Update(resize(24, 12))
	m = next.(*TranscriptModel)
	if len(m.lines) <= wide {
		t.Fatalf("narrowed to 24 cells the answer still takes %d lines, was %d", len(m.lines), wide)
	}
	next, _ = m.Update(resize(40, 12))
	m = next.(*TranscriptModel)
	if len(m.lines) != wide {
		t.Fatalf("back at 40 cells the answer takes %d lines, was %d", len(m.lines), wide)
	}
}

// TestTranscriptFollows keeps the view at the end while it is at the end, and
// leaves a reader who scrolled up where they were reading.
func TestTranscriptFollows(t *testing.T) {
	m := newTranscriptTest(t, 60, 10, goldenStyles(t))
	r := drive(t, m, m.Init(), reading(transcriptSample()...))
	m = r.model.(*TranscriptModel)
	if m.offset != m.maxOffset() {
		t.Fatalf("the first reading is drawn at line %d of %d, want the end", m.offset, m.maxOffset())
	}
	more := []transcript.Entry{{Who: transcript.Assistant, Text: "and one more thing"}}
	next, _ := m.Update(reading(more...))
	m = next.(*TranscriptModel)
	if m.offset != m.maxOffset() {
		t.Fatalf("after a reading the view is at line %d of %d, want the end", m.offset, m.maxOffset())
	}
	next, _ = m.Update(press("k"))
	m = next.(*TranscriptModel)
	if m.follow {
		t.Fatal("scrolling up still follows the transcript")
	}
	at := m.offset
	next, _ = m.Update(reading(more...))
	m = next.(*TranscriptModel)
	if m.offset != at {
		t.Fatalf("a reading moved the scrolled view from line %d to %d", at, m.offset)
	}
	next, _ = m.Update(press("G"))
	m = next.(*TranscriptModel)
	if !m.follow || m.offset != m.maxOffset() {
		t.Fatalf("the end key leaves the view at line %d of %d, following %v", m.offset, m.maxOffset(), m.follow)
	}
}

// TestTranscriptScrollKeys covers every movement key and the wheel, which all
// stay inside the transcript.
func TestTranscriptScrollKeys(t *testing.T) {
	entries := make([]transcript.Entry, 40)
	for i := range entries {
		entries[i] = transcript.Entry{Who: transcript.User, Text: "line " + strconv.Itoa(i)}
	}
	cases := []struct {
		name string
		msgs []tea.Msg
		want func(m *TranscriptModel) int
	}{
		{name: "up", msgs: []tea.Msg{press("up")}, want: func(m *TranscriptModel) int { return m.maxOffset() - 1 }},
		{name: "down at the end stays", msgs: []tea.Msg{press("j")}, want: func(m *TranscriptModel) int { return m.maxOffset() }},
		{name: "page up", msgs: []tea.Msg{press("pgup")}, want: func(m *TranscriptModel) int { return m.maxOffset() - m.bodyHeight() }},
		{name: "top", msgs: []tea.Msg{press("g")}, want: func(*TranscriptModel) int { return 0 }},
		{name: "top then page down", msgs: []tea.Msg{press("g"), press("pgdown")}, want: func(m *TranscriptModel) int { return m.bodyHeight() }},
		{name: "top then bottom", msgs: []tea.Msg{press("g"), press("end")}, want: func(m *TranscriptModel) int { return m.maxOffset() }},
		{
			name: "the wheel",
			msgs: []tea.Msg{tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.MouseWheelMsg{Button: tea.MouseWheelDown}},
			want: func(m *TranscriptModel) int { return m.maxOffset() - 1 },
		},
		{
			name: "a key that moves nothing",
			msgs: []tea.Msg{press("x")},
			want: func(m *TranscriptModel) int { return m.maxOffset() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTranscriptTest(t, 60, 12, goldenStyles(t))
			r := drive(t, m, m.Init(), append([]tea.Msg{reading(entries...)}, tc.msgs...)...)
			m = r.model.(*TranscriptModel)
			if want := tc.want(m); m.offset != want {
				t.Fatalf("the view is at line %d, want %d", m.offset, want)
			}
			if m.offset < 0 || m.offset > m.maxOffset() {
				t.Fatalf("the view left the transcript at line %d of %d", m.offset, m.maxOffset())
			}
		})
	}
}

// TestTranscriptRestart replaces what is on screen when the file was rewritten
// or replaced, so no entry of the file that is gone is left behind.
func TestTranscriptRestart(t *testing.T) {
	m := newTranscriptTest(t, 60, 12, goldenStyles(t))
	r := drive(t, m, m.Init(), reading(transcriptSample()...))
	m = r.model.(*TranscriptModel)
	before := len(m.lines)
	next, _ := m.Update(transcriptUpdateMsg{update: TranscriptUpdate{
		Restart: true,
		Entries: []transcript.Entry{{Who: transcript.User, Text: "start again"}},
		At:      fixedNow,
	}})
	m = next.(*TranscriptModel)
	if len(m.entries) != 1 || m.entries[0].Text != "start again" {
		t.Fatalf("after a restart the reader holds %d entries", len(m.entries))
	}
	if len(m.lines) != 1 {
		t.Fatalf("after a restart the reader draws %d lines, was %d", len(m.lines), before)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "start again") {
		t.Fatal("the frame does not show what the transcript says now")
	}
}

// TestTranscriptKeepsTheEnd bounds what a long session leaves in memory, and
// keeps the end of it, which is what a reader follows.
func TestTranscriptKeepsTheEnd(t *testing.T) {
	m := newTranscriptTest(t, 60, 12, goldenStyles(t))
	for i := range TranscriptKeep + 50 {
		m.add(TranscriptUpdate{Entries: []transcript.Entry{{Who: transcript.User, Text: "line " + strconv.Itoa(i)}}})
	}
	if len(m.entries) != TranscriptKeep {
		t.Fatalf("the reader holds %d entries, want %d", len(m.entries), TranscriptKeep)
	}
	if got := m.entries[len(m.entries)-1].Text; got != "line "+strconv.Itoa(TranscriptKeep+49) {
		t.Fatalf("the last entry is %q", got)
	}
	if len(m.lines) != TranscriptKeep {
		t.Fatalf("the reader draws %d lines for %d entries", len(m.lines), len(m.entries))
	}
	if !m.follow || m.offset != m.maxOffset() {
		t.Fatalf("the view is at line %d of %d", m.offset, m.maxOffset())
	}
}

// TestTranscriptCloses leaves on the keys that close a reader and on nothing
// else.
func TestTranscriptCloses(t *testing.T) {
	cases := []struct {
		key  string
		quit bool
	}{
		{key: "q", quit: true},
		{key: "esc", quit: true},
		{key: "ctrl+c", quit: true},
		{key: "j"},
		{key: "Q"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			m := newTranscriptTest(t, 60, 12, goldenStyles(t))
			r := drive(t, m, m.Init(), reading(transcriptSample()...), press(tc.key))
			if r.quit != tc.quit {
				t.Fatalf("%q left the reader: %v, want %v", tc.key, r.quit, tc.quit)
			}
		})
	}
}

// TestTranscriptUpdatesChannel takes its readings from the channel it was
// given, and stays on what it has once the readings stop.
func TestTranscriptUpdatesChannel(t *testing.T) {
	updates := make(chan TranscriptUpdate, 2)
	updates <- TranscriptUpdate{Entries: []transcript.Entry{{Who: transcript.User, Text: "first"}}, At: fixedNow}
	updates <- TranscriptUpdate{Entries: []transcript.Entry{{Who: transcript.Assistant, Text: "second"}}, At: fixedNow}
	close(updates)
	m := NewTranscript(TranscriptOptions{Styles: goldenStyles(t), Updates: updates, Title: "api", Width: 60, Height: 12, Now: clock})
	r := drive(t, m, m.Init())
	m = r.model.(*TranscriptModel)
	if len(m.entries) != 2 {
		t.Fatalf("the reader took %d entries from the channel", len(m.entries))
	}
	if !m.closed {
		t.Fatal("the reader does not know the readings stopped")
	}
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "first") || !strings.Contains(frame, "second") {
		t.Fatalf("the frame shows neither reading:\n%s", frame)
	}
	if !strings.Contains(frame, "reader stopped") {
		t.Fatalf("the frame does not say the readings stopped:\n%s", frame)
	}
}

// TestTranscriptSanitizes keeps control characters of a transcript, of its
// title and of an error out of the frame.
func TestTranscriptSanitizes(t *testing.T) {
	m := NewTranscript(TranscriptOptions{
		Styles: goldenStyles(t),
		Title:  "api\x1b]0;stolen\x07",
		Width:  80,
		Height: 12,
		Now:    clock,
	})
	m.add(TranscriptUpdate{
		Entries: []transcript.Entry{{Who: transcript.User, Text: "run \x1b[31mred\x1b[0m"}},
		Err:     errors.New("read \x07bell"),
	})
	content := m.View().Content
	if strings.ContainsAny(ansi.Strip(content), "\x1b\x07\r") {
		t.Fatalf("the frame carries control characters: %q", content)
	}
	if strings.Contains(ansi.Strip(content), "stolen") {
		t.Fatalf("the frame draws a title escape: %q", ansi.Strip(content))
	}
}

// TestTranscriptSmall draws in a window too small for a header, a body and a
// footer.
func TestTranscriptSmall(t *testing.T) {
	for _, size := range []struct{ w, h int }{{w: 20, h: 3}, {w: 12, h: 2}, {w: 8, h: 1}} {
		t.Run(strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h), func(t *testing.T) {
			m := newTranscriptTest(t, size.w, size.h, goldenStyles(t))
			r := drive(t, m, m.Init(), reading(transcriptSample()...))
			assertFrameSize(t, r.model.(viewer), size.w, size.h)
		})
	}
}

// assertFrameSize checks a frame's geometry without a golden.
func assertFrameSize(t *testing.T, m viewer, width, height int) {
	t.Helper()
	lines := strings.Split(m.View().Content, "\n")
	if len(lines) != height {
		t.Fatalf("%d lines, want %d", len(lines), height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Fatalf("line %d is %d cells wide, over %d: %q", i+1, w, width, ansi.Strip(l))
		}
	}
}
