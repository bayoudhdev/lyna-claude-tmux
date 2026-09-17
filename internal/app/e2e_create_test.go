package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// The client every layout is opened under. It is large enough for the widest
// built-in layout to take one more split without tmux refusing for want of
// space, and its height leaves room for the status line.
const (
	e2eCols = 200
	e2eRows = 50
)

// e2eWorkspace is a workspace opened by create with a real client attached to
// it, so the keys the workspace binds are interpreted the way they are for a
// user. Driving it through send-keys on the server instead would type into the
// pane's program and never reach a binding.
type e2eWorkspace struct {
	*tmuxtest.Nested
	env  *createEnv
	srv  *Server
	name string
}

// openE2EWorkspace creates the workspace for one layout and attaches a client.
func openE2EWorkspace(t *testing.T, name string, width, height int) *e2eWorkspace {
	t.Helper()
	e := newCreateEnv(t)
	s := openServer(t, e.testHost)
	res, err := s.Create(tmuxtest.Context(t), e.Host, CreateRequest{Dir: e.project, Layout: name, Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	inner := &tmuxtest.Server{Client: s.Client, Name: e.env[session.EnvSocketName], Bin: e.TmuxBin}
	return &e2eWorkspace{
		Nested: tmuxtest.Attach(t, inner, res.Name, e2eCols, e2eRows),
		env:    e, srv: s, name: res.Name,
	}
}

func (w *e2eWorkspace) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := w.srv.Client.Run(tmuxtest.Context(t), args[0], args[1:]...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimRight(out, "\n")
}

// panes expands format once per pane of the workspace window, in pane order.
func (w *e2eWorkspace) panes(t *testing.T, format string) []string {
	t.Helper()
	out := w.run(t, "list-panes", "-t", tmux.ExactSession(w.name), "-F", format)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// active expands format against the pane the client has focused.
func (w *e2eWorkspace) active(t *testing.T, format string) string {
	t.Helper()
	return w.run(t, "display-message", "-p", "-t", tmux.ExactSession(w.name), format)
}

// waitActive waits until format expands to want against the focused pane. A
// key reaches the client, the client asks the server: what the key did is
// visible a moment after the key was sent, never in the same call.
func (w *e2eWorkspace) waitActive(t *testing.T, format, want string) {
	t.Helper()
	var got string
	tmuxtest.WaitFor(t, format+" = "+strconv.Quote(want), func() bool {
		got = w.active(t, format)
		return got == want
	})
	if got != want {
		t.Fatalf("%s = %q, want %q", format, got, want)
	}
}

// alive fails naming every pane whose program has exited. A dead pane is what
// the user saw: the workspace keeps it on screen with its exit status, so the
// test reads the status instead of finding the pane gone.
func (w *e2eWorkspace) alive(t *testing.T) {
	t.Helper()
	for _, p := range w.panes(t, "#{pane_index}|#{@lt_role}|#{pane_dead}|#{pane_dead_status}") {
		if f := strings.Split(p, "|"); f[2] != "0" {
			t.Fatalf("pane %s (%s) is dead with status %q", f[0], f[1], f[3])
		}
	}
}

// splitAndClose splits the focused pane with split and closes the pane that
// appears with closeKey, answering the confirmation the workspace asks for.
func (w *e2eWorkspace) splitAndClose(t *testing.T, before int, split, closeKey []string) {
	t.Helper()
	w.Keys(t, split...)
	w.waitActive(t, "#{window_panes}|#{@lt_role}|#{pane_dead}", strconv.Itoa(before+1)+"|shell|0")
	index := w.active(t, "#{pane_index}")
	w.Keys(t, closeKey...)
	w.WaitScreen(t, "Close pane "+index+"? (y/n)", false)
	w.Keys(t, "y")
	w.waitActive(t, "#{window_panes}", strconv.Itoa(before))
	w.alive(t)
}

// TestE2ECreateLayouts opens every built-in layout the way a user does and
// checks the workspace that comes up is usable: the panes the layout names,
// with their roles, the agent running and focused, nothing dead, and focus,
// split and close working from the Alt keys and from the prefix.
func TestE2ECreateLayouts(t *testing.T) {
	cases := []struct {
		name      string
		layout    string
		width     int
		height    int
		wantRoles []string
	}{
		{name: "solo", layout: layout.Solo, wantRoles: []string{"claude"}},
		{name: "duo", layout: layout.Duo, wantRoles: []string{"claude", "shell"}},
		{name: "trio", layout: layout.Trio, wantRoles: []string{"claude", "shell", "changes"}},
		{name: "quad", layout: layout.Quad, wantRoles: []string{"claude", "claude", "claude", "claude"}},
		{name: "review", layout: layout.Review, wantRoles: []string{"claude", "review"}},
		{
			name: "auto on a terminal with room for three", layout: layout.Auto,
			width: layout.AutoTrioWidth, height: layout.AutoTrioHeight,
			wantRoles: []string{"claude", "shell", "changes"},
		},
		{name: "auto with no terminal size", layout: layout.Auto, wantRoles: []string{"claude", "shell"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := openE2EWorkspace(t, tc.layout, tc.width, tc.height)
			var agents int
			for _, r := range tc.wantRoles {
				if r == string(layout.RoleClaude) {
					agents++
				}
			}
			ids := w.panes(t, "#{pane_id}")
			// The pane after the first, which is the first again in a layout
			// of one: one pane is a cycle of one, and the keys must leave the
			// user on it rather than fail.
			next := 0
			if len(ids) > 1 {
				next = 1
			}

			steps := []struct {
				name string
				do   func(t *testing.T)
			}{
				{"the layout has its panes", func(t *testing.T) {
					if got := w.panes(t, "#{@lt_role}"); strings.Join(got, ",") != strings.Join(tc.wantRoles, ",") {
						t.Fatalf("roles %q, want %q", got, tc.wantRoles)
					}
					w.alive(t)
				}},
				{"the agent runs in every Claude pane", func(t *testing.T) {
					if got := w.env.invocations(t, agents); len(got) != agents {
						t.Fatalf("%d agent starts, want %d", len(got), agents)
					}
					w.WaitScreen(t, fakeclaude.ReadyLine, false)
				}},
				{"the agent has the focus", func(t *testing.T) {
					w.waitActive(t, "#{pane_id}|#{@lt_role}|#{pane_dead}", ids[0]+"|claude|0")
				}},
				{"the Alt keys move the focus", func(t *testing.T) {
					w.Keys(t, "M-]")
					w.waitActive(t, "#{pane_id}", ids[next])
					w.Keys(t, "M-[")
					w.waitActive(t, "#{pane_id}", ids[0])
				}},
				{"the prefix moves the focus", func(t *testing.T) {
					w.Keys(t, "C-b", "o")
					w.waitActive(t, "#{pane_id}", ids[next])
					w.Keys(t, "C-b", ";")
					w.waitActive(t, "#{pane_id}", ids[0])
				}},
				{"the Alt keys split and close", func(t *testing.T) {
					w.splitAndClose(t, len(ids), []string{`M-\`}, []string{"M-x"})
				}},
				{"the prefix splits and closes", func(t *testing.T) {
					w.splitAndClose(t, len(ids), []string{"C-b", "-"}, []string{"C-b", "x"})
				}},
				{"the layout is the one it started as", func(t *testing.T) {
					if got := w.panes(t, "#{@lt_role}"); strings.Join(got, ",") != strings.Join(tc.wantRoles, ",") {
						t.Fatalf("roles %q, want %q", got, tc.wantRoles)
					}
					w.alive(t)
				}},
			}
			for _, step := range steps {
				if !t.Run(step.name, step.do) {
					t.Fatalf("screen:\n%s", w.Screen(t))
				}
			}
		})
	}
}
