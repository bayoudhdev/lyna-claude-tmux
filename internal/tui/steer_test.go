package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// startMessage builds the message form, runs Init and reports the terminal
// size, as the program does before the first key.
func startMessage(t *testing.T, opts MessageOptions, msgs ...tea.Msg) *MessageModel {
	t.Helper()
	if opts.Styles.Text.String() == "" {
		opts.Styles = goldenStyles(t)
	}
	if opts.Agent == "" {
		opts.Agent, opts.Workspace = "review-api", "api"
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 80, 24
	}
	m := NewMessage(opts)
	drive(t, m, m.Init(), append([]tea.Msg{resize(opts.Width, opts.Height)}, msgs...)...)
	return m
}

// startStop does the same for the stop form.
func startStop(t *testing.T, opts StopOptions, msgs ...tea.Msg) *StopModel {
	t.Helper()
	if opts.Styles.Text.String() == "" {
		opts.Styles = goldenStyles(t)
	}
	if opts.Teammate == "" {
		opts.Teammate, opts.Workspace = "review-api", "api"
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 80, 24
	}
	m := NewStop(opts)
	drive(t, m, m.Init(), append([]tea.Msg{resize(opts.Width, opts.Height)}, msgs...)...)
	return m
}

// then joins key sequences, typed text included, in the order they happen.
func then(parts ...[]tea.Msg) []tea.Msg {
	var msgs []tea.Msg
	for _, p := range parts {
		msgs = append(msgs, p...)
	}
	return msgs
}

// TestMessageForm walks the form to every end it has, and reads back what it
// asked for.
func TestMessageForm(t *testing.T) {
	cases := []struct {
		name string
		msgs []tea.Msg
		// want is the result the form ends on, and frame what its last frame
		// says.
		want  MessageResult
		frame string
	}{
		{
			name:  "a message sent as it was typed",
			msgs:  then(typeText("read the router again"), keys("enter", "enter")),
			want:  MessageResult{Text: "read the router again", Sent: true},
			frame: "sending to review-api",
		},
		{
			name: "a message edited before it is sent",
			msgs: then(
				typeText("read the rout"), keys("enter", "down", "enter"),
				typeText("er"), keys("enter", "enter"),
			),
			want:  MessageResult{Text: "read the router", Sent: true},
			frame: "sending to review-api",
		},
		{
			name:  "text around the message is not sent",
			msgs:  then(typeText("   read the router   "), keys("enter", "enter")),
			want:  MessageResult{Text: "read the router", Sent: true},
			frame: "read the router",
		},
		{
			name:  "canceled on the send step",
			msgs:  then(typeText("read the router"), keys("enter", "down", "down", "enter")),
			frame: "nothing was sent",
		},
		{name: "escape on the message", msgs: then(typeText("read"), keys("esc")), frame: "nothing was sent"},
		{name: "ctrl+c on the send step", msgs: then(typeText("read"), keys("enter", "ctrl+c")), frame: "nothing was sent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startMessage(t, MessageOptions{}, tc.msgs...)
			res, done := m.Result()
			if !done || res != tc.want {
				t.Fatalf("result %+v (done %v), want %+v\n%s", res, done, tc.want, plain(m))
			}
			if !strings.Contains(plain(m), tc.frame) {
				t.Fatalf("frame lacks %q:\n%s", tc.frame, plain(m))
			}
		})
	}
}

// TestMessageShowsWhatIsSent keeps the send step on the exact text the agent
// is sent, before anything reaches it.
func TestMessageShowsWhatIsSent(t *testing.T) {
	m := startMessage(t, MessageOptions{}, then(typeText("read the router"), keys("enter"))...)
	if _, done := m.Result(); done {
		t.Fatal("the form sent without showing the text first")
	}
	got := plain(m)
	for _, want := range []string{"review-api in workspace api is sent, exactly:", "read the router", "send", "edit", "cancel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the send step lacks %q:\n%s", want, got)
		}
	}
}

// TestMessageRefusesNothing keeps a message with nothing to type from being
// sent: the form stays on the message and says why.
func TestMessageRefusesNothing(t *testing.T) {
	for _, typed := range []string{"", "   "} {
		t.Run(strings.ReplaceAll(typed, " ", "_"), func(t *testing.T) {
			m := startMessage(t, MessageOptions{}, then(typeText(typed), keys("enter"))...)
			if _, done := m.Result(); done {
				t.Fatal("the form finished on an empty message")
			}
			if got := plain(m); !strings.Contains(got, ErrMessageEmpty.Error()) {
				t.Fatalf("frame lacks %q:\n%s", ErrMessageEmpty.Error(), got)
			}
		})
	}
}

// TestMessageText pins what a message reaches the agent as: one line of
// printable text, which is what a pane is typed.
func TestMessageText(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{name: "a plain message", input: "read the router", want: "read the router"},
		{name: "space around it", input: "  read the router \t", want: "read the router"},
		{name: "lines folded onto one", input: "read\nthe router", want: "read the router"},
		{name: "a sequence that would be read as keys", input: "read\x1b[201~\x03 the router", want: "read the router"},
		{name: "nothing but controls", input: "\x1b[2J\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageText(tc.input); got != tc.want {
				t.Fatalf("MessageText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestStopForm walks the stop form to every end it has.
func TestStopForm(t *testing.T) {
	cases := []struct {
		name  string
		lead  bool
		msgs  []tea.Msg
		want  StopResult
		frame string
	}{
		{
			name:  "the lead is asked",
			lead:  true,
			msgs:  keys("enter", "enter"),
			want:  StopResult{Way: StopAskLead, Message: StopMessage("review-api"), Sent: true},
			frame: "asking the lead",
		},
		{
			name:  "stopped now on the name typed out",
			lead:  true,
			msgs:  then(keys("down", "enter"), typeText("review-api"), keys("enter")),
			want:  StopResult{Way: StopNow, Typed: "review-api", Sent: true},
			frame: "stopping review-api",
		},
		{
			name:  "a workspace with no lead goes straight to the name",
			msgs:  then(typeText("review-api"), keys("enter")),
			want:  StopResult{Way: StopNow, Typed: "review-api", Sent: true},
			frame: "stopping review-api",
		},
		{
			name:  "asking the lead answered no",
			lead:  true,
			msgs:  keys("enter", "left", "enter"),
			want:  StopResult{Way: StopAskLead},
			frame: "nothing was stopped",
		},
		{
			name:  "escape on the name",
			lead:  true,
			msgs:  then(keys("down", "enter"), typeText("review"), keys("esc")),
			want:  StopResult{Way: StopNow},
			frame: "nothing was stopped",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startStop(t, StopOptions{Lead: tc.lead}, tc.msgs...)
			res, done := m.Result()
			if !done || res != tc.want {
				t.Fatalf("result %+v (done %v), want %+v\n%s", res, done, tc.want, plain(m))
			}
			if !strings.Contains(plain(m), tc.frame) {
				t.Fatalf("frame lacks %q:\n%s", tc.frame, plain(m))
			}
		})
	}
}

// TestStopShowsWhatTheLeadIsSent keeps the ask step on the exact text the lead
// is sent.
func TestStopShowsWhatTheLeadIsSent(t *testing.T) {
	m := startStop(t, StopOptions{Lead: true}, keys("enter")...)
	got := plain(m)
	for _, want := range []string{"the lead of workspace api is sent, exactly:", StopMessage("review-api")} {
		if !strings.Contains(got, want) {
			t.Fatalf("the ask step lacks %q:\n%s", want, got)
		}
	}
}

// TestStopNeedsTheNameTypedOut covers the typed confirmation: anything but the
// name exactly as the form shows it keeps the pane open.
func TestStopNeedsTheNameTypedOut(t *testing.T) {
	cases := []struct {
		name, teammate, typed string
	}{
		{name: "nothing typed", teammate: "review-api"},
		{name: "part of the name", teammate: "review-api", typed: "review"},
		{name: "the name with a space after it", teammate: "review-api", typed: "review-api "},
		{name: "the name in another case", teammate: "review-api", typed: "Review-api"},
		{name: "a teammate the form cannot name", teammate: "\x1b[2J", typed: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startStop(t, StopOptions{Teammate: tc.teammate, Workspace: "api"}, then(typeText(tc.typed), keys("enter"))...)
			if res, done := m.Result(); done {
				t.Fatalf("the form stopped the teammate on %q: %+v", tc.typed, res)
			}
			if got := plain(m); !strings.Contains(got, "exactly to stop it") {
				t.Fatalf("frame does not say why:\n%s", got)
			}
		})
	}
}

// TestStopMessage pins the text the lead is sent.
func TestStopMessage(t *testing.T) {
	cases := []struct {
		name, teammate, want string
	}{
		{name: "a teammate", teammate: "review-api", want: "Shut down the teammate review-api: its work is done."},
		{name: "a name carrying a sequence", teammate: "review\x1b[31m-api\n", want: "Shut down the teammate review-api: its work is done."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StopMessage(tc.teammate); got != tc.want {
				t.Fatalf("StopMessage(%q) = %q, want %q", tc.teammate, got, tc.want)
			}
		})
	}
}

// TestSteerFrames pins both forms at the sizes they are drawn at.
func TestSteerFrames(t *testing.T) {
	cases := []struct {
		name          string
		model         func(t *testing.T, w, h int) viewer
		width, height int
		ansi          bool
	}{
		{
			name: "message-write", width: 80, height: 24, ansi: true,
			model: func(t *testing.T, w, h int) viewer {
				return startMessage(t, MessageOptions{Width: w, Height: h}, typeText("read the router")...)
			},
		},
		{
			name: "message-send", width: 80, height: 24,
			model: func(t *testing.T, w, h int) viewer {
				return startMessage(t, MessageOptions{Width: w, Height: h}, then(typeText("read the router for injection"), keys("enter"))...)
			},
		},
		{
			name: "message-small", width: 40, height: 14,
			model: func(t *testing.T, w, h int) viewer {
				return startMessage(t, MessageOptions{Width: w, Height: h}, then(typeText("read the router"), keys("enter"))...)
			},
		},
		{
			name: "stop-way", width: 80, height: 24, ansi: true,
			model: func(t *testing.T, w, h int) viewer {
				return startStop(t, StopOptions{Lead: true, Width: w, Height: h})
			},
		},
		{
			name: "stop-ask", width: 80, height: 24,
			model: func(t *testing.T, w, h int) viewer {
				return startStop(t, StopOptions{Lead: true, Width: w, Height: h}, keys("enter")...)
			},
		},
		{
			name: "stop-now", width: 80, height: 24,
			model: func(t *testing.T, w, h int) viewer {
				return startStop(t, StopOptions{Lead: true, Width: w, Height: h}, then(keys("down", "enter"), typeText("review"), keys("enter"))...)
			},
		},
		{
			name: "stop-small", width: 40, height: 14,
			model: func(t *testing.T, w, h int) viewer {
				return startStop(t, StopOptions{Width: w, Height: h})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFrame(t, tc.name, tc.model(t, tc.width, tc.height), tc.width, tc.height, tc.ansi)
		})
	}
}

// TestSteerFormsResize redraws both forms at a new size, and keeps a finished
// form from taking another message.
func TestSteerFormsResize(t *testing.T) {
	msg := startMessage(t, MessageOptions{})
	stop := startStop(t, StopOptions{Lead: true})
	for _, m := range []tea.Model{msg, stop} {
		drive(t, m, nil, resize(50, 16))
		if lines := strings.Count(plain(m.(viewer)), "\n") + 1; lines != 16 {
			t.Fatalf("%T draws %d lines after a resize to 16", m, lines)
		}
	}
	drive(t, msg, nil, then(typeText("read"), keys("enter", "enter"))...)
	res, _ := msg.Result()
	drive(t, msg, nil, then(keys("up"), typeText("more"))...)
	if again, _ := msg.Result(); again != res {
		t.Fatalf("a finished form changed its result to %+v", again)
	}
}
