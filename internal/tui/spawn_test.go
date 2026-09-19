package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agentdef"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
)

// spawnDefs are the definitions a workspace offers in these tests.
func spawnDefs() []agentdef.Def {
	return []agentdef.Def{
		{Name: "api-developer", Description: "changes the API"},
		{Name: "security-auditor", Description: "reads a diff for its risks"},
	}
}

// startSpawn builds the form, runs Init and reports the terminal size, as the
// program does before the first key.
func startSpawn(t *testing.T, opts SpawnOptions, msgs ...tea.Msg) *SpawnModel {
	t.Helper()
	if opts.Styles.Text.String() == "" {
		opts.Styles = goldenStyles(t)
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 80, 24
	}
	m := NewSpawn(opts)
	drive(t, m, m.Init(), append([]tea.Msg{resize(opts.Width, opts.Height)}, msgs...)...)
	return m
}

// TestSpawnAsksTheLead walks the form to the end with the lead as the target
// and reads back the request and the text the lead is sent.
func TestSpawnAsksTheLead(t *testing.T) {
	m := startSpawn(t, SpawnOptions{Agents: spawnDefs(), Lead: true, Worktree: true},
		append(
			keys("enter", "down", "enter", "enter", "down", "enter", "y"),
			append(typeText("review-api"), append(keys("enter"), append(typeText("read the router for injection"), keys("enter", "y")...)...)...)...,
		)...)
	res, done := m.Result()
	if !done || !res.Sent {
		t.Fatalf("the form did not finish: %+v", res)
	}
	want := SpawnRequest{
		Target: SpawnLead, Agent: "api-developer", Effort: "low", Worktree: true,
		Name: "review-api", Prompt: "read the router for injection",
	}
	if res.Request != want {
		t.Fatalf("request %+v, want %+v", res.Request, want)
	}
	if res.Message != SpawnMessage(want) || !strings.Contains(res.Message, "read the router for injection") {
		t.Fatalf("message %q", res.Message)
	}
	if !strings.Contains(plain(m), "asking the lead") {
		t.Fatalf("frame\n%s", plain(m))
	}
}

// TestSpawnAsksTheLeadInItsOwnWords covers the prompts only a launch refuses:
// what the lead is asked is text in a conversation that is already running, so
// a single word or a leading dash is sent as it was typed.
func TestSpawnAsksTheLeadInItsOwnWords(t *testing.T) {
	for _, prompt := range []string{"refactor", "-v is the flag to add"} {
		t.Run(prompt, func(t *testing.T) {
			m := startSpawn(t, SpawnOptions{Agents: spawnDefs(), Lead: true},
				append(keys("enter", "enter", "enter", "enter", "n", "enter"), append(typeText(prompt), keys("enter", "y")...)...)...)
			res, done := m.Result()
			if !done || !res.Sent || res.Request.Prompt != prompt {
				t.Fatalf("the form did not send %q: %+v (done %v)\n%s", prompt, res, done, plain(m))
			}
		})
	}
}

// TestSpawnStartsItsOwn covers the other target: a workspace with no lead to
// ask never offers the choice, and the agent opens in a window of its own.
func TestSpawnStartsItsOwn(t *testing.T) {
	m := startSpawn(t, SpawnOptions{Agents: spawnDefs(), Model: "opus", Effort: "high"},
		append(
			keys("enter", "enter", "enter", "n"),
			append(typeText("spike"), append(keys("enter"), append(typeText("try the other parser"), keys("enter", "y")...)...)...)...,
		)...)
	res, done := m.Result()
	if !done || !res.Sent {
		t.Fatalf("the form did not finish: %+v", res)
	}
	want := SpawnRequest{
		Target: SpawnOwn, Model: "opus", Effort: "high",
		Name: "spike", Prompt: "try the other parser",
	}
	if res.Request != want {
		t.Fatalf("request %+v, want %+v", res.Request, want)
	}
	// Nothing is typed at an agent here, so there is no message to show first.
	if res.Message != "" {
		t.Fatalf("message %q, want none", res.Message)
	}
}

// TestSpawnCancels covers every way the form ends with nothing started.
func TestSpawnCancels(t *testing.T) {
	cases := []struct {
		name string
		msgs []tea.Msg
	}{
		{name: "escape on the first question", msgs: keys("esc")},
		{name: "ctrl+c", msgs: keys("enter", "ctrl+c")},
		{
			name: "the last question answered no",
			msgs: append(
				keys("enter", "enter", "enter", "enter", "y"),
				append(typeText("spike"), append(keys("enter"), append(typeText("try the other parser"), keys("enter", "n")...)...)...)...,
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startSpawn(t, SpawnOptions{Agents: spawnDefs(), Lead: true}, tc.msgs...)
			res, done := m.Result()
			if !done || res.Sent {
				t.Fatalf("the form started something: %+v (done %v)", res, done)
			}
			if !strings.Contains(plain(m), "nothing was started") {
				t.Fatalf("frame\n%s", plain(m))
			}
		})
	}
}

// TestSpawnMessage pins the text the lead is sent, which the user reads in
// full before anything reaches an agent.
func TestSpawnMessage(t *testing.T) {
	cases := []struct {
		name string
		req  SpawnRequest
		want string
	}{
		{
			name: "the plainest request",
			req:  SpawnRequest{Prompt: "read the router"},
			want: "Start a general-purpose agent, and tell it: read the router",
		},
		{
			name: "an agent of a type the project carries",
			req:  SpawnRequest{Agent: "api-developer", Prompt: "read the router"},
			want: "Start a api-developer agent, and tell it: read the router",
		},
		{
			name: "everything the form asks for",
			req: SpawnRequest{
				Agent: "api-developer", Model: "opus", Effort: "high",
				Worktree: true, Name: "review-api", Prompt: "read the router",
			},
			want: "Start a api-developer agent in a git worktree of its own named review-api," +
				" on opus, with high effort, and tell it: read the router",
		},
		{
			name: "a worktree the lead names itself",
			req:  SpawnRequest{Worktree: true, Prompt: "read the router"},
			want: "Start a general-purpose agent in a git worktree of its own, and tell it: read the router",
		},
		{
			name: "text that would carry an escape sequence",
			req:  SpawnRequest{Agent: "api\x1b[31m", Prompt: "read\nthe router"},
			want: "Start a api agent, and tell it: read the router",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SpawnMessage(tc.req); got != tc.want {
				t.Fatalf("SpawnMessage =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestSpawnAgentName covers the name a request is shown under.
func TestSpawnAgentName(t *testing.T) {
	cases := []struct {
		agent, want string
	}{
		{agent: "", want: agentdef.Default},
		{agent: "api-developer", want: "api-developer"},
	}
	for _, tc := range cases {
		if got := (SpawnRequest{Agent: tc.agent}).AgentName(); got != tc.want {
			t.Fatalf("AgentName(%q) = %q, want %q", tc.agent, got, tc.want)
		}
	}
}

// TestEffortOptions keeps the forms in step with the launch: every effort the
// launch accepts is offered, after the choice that leaves it unset, and
// nothing the launch would refuse is.
func TestEffortOptions(t *testing.T) {
	opts := effortOptions("unset")
	values := make([]string, 0, len(opts))
	for _, o := range opts {
		values = append(values, o.Value)
	}
	if want := append([]string{""}, claudecfg.Efforts()...); !slices.Equal(values, want) {
		t.Fatalf("effort options %q, want %q", values, want)
	}
	if opts[0].Key != "unset" {
		t.Fatalf("the first option reads %q", opts[0].Key)
	}
}

// TestSpawnRefusesWhatCannotBeStarted drives the validation of the form: a
// worktree with no name, a name no worktree could take, and an agent with
// nothing to do.
func TestSpawnRefusesWhatCannotBeStarted(t *testing.T) {
	cases := []struct {
		name  string
		lead  bool
		msgs  []tea.Msg
		wants string
	}{
		{
			name:  "a worktree with no name",
			msgs:  append(keys("enter", "enter", "enter", "y"), press("enter")),
			wants: "a worktree needs a name",
		},
		{
			name:  "a name no worktree could take",
			msgs:  append(append(keys("enter", "enter", "enter", "y"), typeText("../escape")...), press("enter")),
			wants: "worktree name",
		},
		{
			name:  "an agent of its own with no name",
			msgs:  append(keys("enter", "enter", "enter", "n"), press("enter")),
			wants: "a window of its own needs a name",
		},
		{
			name:  "a prompt claude would read as an option",
			msgs:  append(append(keys("enter", "enter", "enter", "n"), typeText("spike")...), append(keys("enter"), append(typeText("-v now"), press("enter"))...)...),
			wants: "must not start with '-'",
		},
		{
			name:  "a prompt of one word the agent would run as a subcommand",
			msgs:  append(append(keys("enter", "enter", "enter", "n"), typeText("spike")...), append(keys("enter"), append(typeText("update"), press("enter"))...)...),
			wants: "one word",
		},
		{
			name:  "an agent with nothing to do",
			msgs:  append(append(keys("enter", "enter", "enter", "n"), typeText("spike")...), keys("enter", "enter")...),
			wants: ErrSpawnPrompt.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startSpawn(t, SpawnOptions{Agents: spawnDefs(), Lead: tc.lead}, tc.msgs...)
			if _, done := m.Result(); done {
				t.Fatal("the form finished on a request it cannot start")
			}
			if got := plain(m); !strings.Contains(got, tc.wants) {
				t.Fatalf("frame lacks %q:\n%s", tc.wants, got)
			}
		})
	}
}

// TestSpawnFrames pins the form at the sizes it is drawn at.
func TestSpawnFrames(t *testing.T) {
	cases := []struct {
		name          string
		opts          SpawnOptions
		msgs          []tea.Msg
		width, height int
		ansi          bool
	}{
		{name: "spawn-target", opts: SpawnOptions{Agents: spawnDefs(), Lead: true}, width: 80, height: 24, ansi: true},
		{name: "spawn-agent", opts: SpawnOptions{Agents: spawnDefs(), Lead: true}, msgs: keys("enter"), width: 80, height: 24},
		{
			name: "spawn-review", opts: SpawnOptions{Agents: spawnDefs(), Lead: true, Worktree: true},
			msgs: append(
				keys("enter", "down", "enter", "enter", "enter", "y"),
				append(typeText("review-api"), append(keys("enter"), append(typeText("read the router"), press("enter"))...)...)...,
			),
			width: 80, height: 24,
		},
		{name: "spawn-small", opts: SpawnOptions{Agents: spawnDefs()}, width: 40, height: 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Width, opts.Height = tc.width, tc.height
			m := startSpawn(t, opts, tc.msgs...)
			assertFrame(t, tc.name, m, tc.width, tc.height, tc.ansi)
		})
	}
}
