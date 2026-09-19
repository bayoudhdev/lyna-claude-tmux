package app

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// lmuxPackage is the lyna-tmux command. The team path runs the real binary:
// Claude Code starts a teammate through the launcher the workspace wrote, and
// the launcher runs `lmux teammate` in the teammate's own pane.
const lmuxPackage = "github.com/bayoudhdev/lyna-claude-tmux/cmd/lmux"

// e2eTeamGate is the channel the lead waits on before it opens each teammate,
// so the test opens them one at a time, the way a lead that spawns one
// teammate per turn does.
const e2eTeamGate = "lt-e2e-team"

// e2eTeammate is one teammate the lead of every workspace here opens.
type e2eTeammate struct{ name, agentType string }

// e2eTeammates are those teammates, in the order they open.
var e2eTeammates = []e2eTeammate{{"reviewer", "Explore"}, {"tester", ""}}

// teamWorkspace is a workspace whose lead opens a team, with a real client
// attached to it.
type teamWorkspace struct {
	*tmuxtest.Nested
	env  *createEnv
	srv  *Server
	name string
	// calls holds one file per tmux command a program in a pane ran, which is
	// every command the fake, the launcher and the hooks sent.
	calls string
}

// openTeamWorkspace opens a workspace with agent teams on, under config, whose
// lead opens e2eTeammates. lmux is the real lyna-tmux binary.
//
// The workspace binary is a script that hands the two commands a team runs
// through it, `teammate` and `hook`, to lmux, and holds every other pane
// (the rail) on a sleep, as the other end-to-end tests do. tmux is found
// through a script first on PATH that records each command before running
// it, so the test sees every server anything in a pane addressed.
func openTeamWorkspace(t *testing.T, lmux, config string) *teamWorkspace {
	t.Helper()
	e := newCreateEnv(t)
	if config != "" {
		e.writeConfig(t, config)
	}
	exe := "#!/bin/sh\ncase \"$1\" in\nteammate|hook) exec " + tmux.ShellQuote(lmux) + " \"$@\" ;;\nesac\n" +
		"{ printf '%s|' \"$@\"; echo; } >> " + tmux.ShellQuote(e.exeLog) + "\nexec sleep 3600\n"
	if err := os.WriteFile(e.Exe, []byte(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	wrap, calls := filepath.Join(e.root, "wrap"), filepath.Join(e.root, "tmux-calls")
	mkdir(t, wrap)
	mkdir(t, calls)
	// The first field is $TMUX, which is the server a command that names no
	// socket reaches.
	recorder := "#!/bin/sh\nf=$(mktemp " + tmux.ShellQuote(filepath.Join(calls, "call.XXXXXXXX")) + ") || exit 70\n" +
		"{ printf '%s\\037' \"$TMUX\"; for a in \"$@\"; do printf '%s\\037' \"$a\"; done; } > \"$f\"\n" +
		"exec " + tmux.ShellQuote(e.TmuxBin) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrap, "tmux"), []byte(recorder), 0o700); err != nil {
		t.Fatal(err)
	}
	e.env["PATH"] = wrap + string(os.PathListSeparator) + e.env["PATH"]
	var spec []string
	for _, m := range e2eTeammates {
		spec = append(spec, m.name+":"+m.agentType)
	}
	e.env[fakeclaude.EnvTeammates] = strings.Join(spec, ",")
	e.env[fakeclaude.EnvTeamGate] = e2eTeamGate
	e.env[fakeclaude.EnvHooks] = "SessionStart"
	e.refreshEnviron()

	s := openServer(t, e.testHost)
	res, err := s.Create(tmuxtest.Context(t), e.Host, CreateRequest{
		Dir: e.project, Width: e2eCols, Height: e2eRows, Launch: LaunchOptions{Teams: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := &tmuxtest.Server{Client: s.Client, Name: e.env[session.EnvSocketName], Bin: e.TmuxBin}
	return &teamWorkspace{
		Nested: tmuxtest.Attach(t, inner, res.Name, e2eCols, e2eRows),
		env:    e, srv: s, name: res.Name, calls: calls,
	}
}

func (w *teamWorkspace) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := w.srv.Client.Run(tmuxtest.Context(t), args[0], args[1:]...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimRight(out, "\n")
}

// lines expands format once per pane of target.
func (w *teamWorkspace) lines(t *testing.T, target, format string) []string {
	t.Helper()
	out := w.run(t, "list-panes", "-t", target, "-F", format)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// lead is the lead's recorded start.
func (w *teamWorkspace) lead(t *testing.T) fakeclaude.Invocation {
	t.Helper()
	var lead fakeclaude.Invocation
	tmuxtest.WaitFor(t, "the lead started", func() bool {
		for _, inv := range fakeclaude.ReadRecords(t, w.env.record) {
			if !slices.Contains(inv.Args, "--agent-id") {
				lead = inv
				return true
			}
		}
		return false
	})
	return lead
}

// spawns waits for n teammates the lead recorded opening.
func (w *teamWorkspace) spawns(t *testing.T, n int) []fakeclaude.TeammateSpawn {
	t.Helper()
	var got []fakeclaude.TeammateSpawn
	tmuxtest.WaitFor(t, strconv.Itoa(n)+" teammates opened", func() bool {
		got = fakeclaude.ReadTeammates(t, w.env.record)
		return len(got) >= n
	})
	return got
}

// started waits for the teammate running in pane to record its start.
func (w *teamWorkspace) started(t *testing.T, pane string) fakeclaude.Invocation {
	t.Helper()
	var got fakeclaude.Invocation
	tmuxtest.WaitFor(t, "the teammate in "+pane+" started", func() bool {
		for _, inv := range fakeclaude.ReadRecords(t, w.env.record) {
			if slices.Contains(inv.Args, "--agent-id") && inv.Env["TMUX_PANE"] == pane {
				got = inv
				return true
			}
		}
		return false
	})
	return got
}

// teammateLog returns what the diagnostic log says about teammates, one
// "name: message" entry per line.
func (w *teamWorkspace) teammateLog(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(w.srv.Paths.LogFile())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		_, entry, _ := strings.Cut(line, " ")
		name, _, _ := strings.Cut(entry, ": ")
		if name == hook.LogTeammate || name == hook.LogTeammateUnplaced || name == hook.LogTeammateFallback {
			out = append(out, entry)
		}
	}
	return out
}

// open lets the lead open its next teammate, and waits for the launcher to
// log how that teammate opened, which is the last thing it does before the
// agent starts.
func (w *teamWorkspace) open(t *testing.T, name string) {
	t.Helper()
	w.run(t, "wait-for", "-S", e2eTeamGate)
	waitRead(t, "the launcher to log "+name, func() (bool, string) {
		entries := w.teammateLog(t)
		for _, e := range entries {
			if _, message, _ := strings.Cut(e, ": "); strings.HasPrefix(message, name+" ") {
				return true, ""
			}
		}
		return false, strings.Join(entries, "\n")
	})
}

// waitRead polls cond like tmuxtest.WaitFor, and on a timeout also reports
// the last thing cond read.
func waitRead(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	var last string
	defer func() {
		if t.Failed() {
			t.Logf("waiting for %s, last read:\n%s", what, last)
		}
	}()
	tmuxtest.WaitFor(t, what, func() bool {
		ok, read := cond()
		last = read
		return ok
	})
}

// tmuxCall is one tmux command a program in a pane ran: its arguments, and
// the $TMUX it ran with.
type tmuxCall struct {
	env  string
	args []string
}

// tmuxCalls returns every tmux command a program in a pane ran.
func (w *teamWorkspace) tmuxCalls(t *testing.T) []tmuxCall {
	t.Helper()
	entries, err := os.ReadDir(w.calls)
	if err != nil {
		t.Fatal(err)
	}
	var out []tmuxCall
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(w.calls, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(string(data), "\x1f")
		if len(fields) < 2 {
			// The recorder is still writing it.
			continue
		}
		out = append(out, tmuxCall{env: fields[0], args: fields[1 : len(fields)-1]})
	}
	return out
}

// TestE2ETeam runs the whole team path on a real server with a real client:
// a workspace with agent teams on, a lead that opens two teammates the way
// Claude Code's tmux backend opens them, the launcher the workspace wrote
// running the real lmux in each teammate's pane, and the agent it starts
// there. What it checks is what the user sees: where each teammate is, how
// its pane is drawn and labeled, that the agent runs in it, what the log says
// and what the rail reads, and that nothing ever reached another server.
func TestE2ETeam(t *testing.T) {
	lmux := fakeclaude.BuildProgram(t, lmuxPackage, "lmux")
	teamName := team.Name(fakeclaude.DefaultSessionID)
	cases := []struct {
		name   string
		config string
		// fallback takes the workspace mark off the session before the team
		// opens, so the launcher runs in a pane that is not a workspace of
		// ours and has to start the teammate the way the agent would.
		fallback bool
		// inProcess expects the teammates kept inside the lead, which is given
		// no launcher.
		inProcess bool
		// wantWindows names the teammates the pane policy moves to a window of
		// their own; every other one stays beside the lead.
		wantWindows []string
	}{
		{name: "a team of two in the team layout"},
		{
			name: "one teammate beside the lead and the next in a window of its own", config: "[workspace]\nagent_panes = 1\n",
			wantWindows: []string{"tester"},
		},
		{name: "teammates kept in process", config: "[claude]\nteammate_mode = \"in-process\"\n", inProcess: true},
		{name: "a launcher that cannot take the pane over", fallback: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := openTeamWorkspace(t, lmux, tc.config)
			labeled := !tc.inProcess && !tc.fallback
			var lead fakeclaude.Invocation
			var leadPane, leadWindow string
			var spawns []fakeclaude.TeammateSpawn

			steps := []struct {
				name string
				do   func(t *testing.T)
			}{
				{"the workspace opens in the team layout with the launcher set", func(t *testing.T) {
					if got := w.lines(t, tmux.ExactSession(w.name), "#{@lt_role}"); !slices.Equal(got, []string{"agents", "claude"}) {
						t.Fatalf("roles %q, want the rail and the lead", got)
					}
					lead = w.lead(t)
					launcher, set := lead.Env["CLAUDE_CODE_TEAMMATE_COMMAND"]
					if set == tc.inProcess || lead.Env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] != "1" {
						t.Fatalf("lead env %v, want a launcher: %v", lead.Env, !tc.inProcess)
					}
					if set && !filepath.IsAbs(launcher) {
						t.Fatalf("launcher %q is not a path", launcher)
					}
					pane := w.lines(t, tmux.ExactSession(w.name), "#{@lt_role}|#{pane_id}|#{window_id}")[1]
					_, ids, _ := strings.Cut(pane, "|")
					leadPane, leadWindow, _ = strings.Cut(ids, "|")
				}},
				{"the team opens", func(t *testing.T) {
					if tc.fallback {
						w.run(t, "set-option", "-u", "-t", tmux.ExactSession(w.name), tmux.OptManaged)
					}
					if !tc.inProcess {
						for _, m := range e2eTeammates {
							w.open(t, m.name)
						}
					}
					spawns = w.spawns(t, len(e2eTeammates))
					for i, sp := range spawns {
						m := e2eTeammates[i]
						backend := "tmux"
						if tc.inProcess {
							backend = "in-process"
						}
						if sp.Name != m.name || sp.AgentType != m.agentType || sp.Team != teamName || sp.Backend != backend {
							t.Fatalf("teammate %d opened as %+v, want %+v on %s", i, sp, m, backend)
						}
					}
				}},
				{"the teammates are where the pane policy puts them", func(t *testing.T) {
					if tc.inProcess {
						if got := w.lines(t, leadWindow, "#{@lt_role}"); !slices.Equal(got, []string{"agents", "claude"}) {
							t.Fatalf("the lead's window holds %q, want no teammate", got)
						}
						return
					}
					for _, sp := range spawns {
						where := w.run(t, "display-message", "-p", "-t", sp.Pane, "#{window_id}|#{window_name}|#{window_panes}")
						window, rest, _ := strings.Cut(where, "|")
						if slices.Contains(tc.wantWindows, sp.Name) {
							if window == leadWindow || rest != sp.Name+"|1" {
								t.Fatalf("%s is in %s, want a window of its own named after it", sp.Name, where)
							}
						} else if window != leadWindow {
							t.Fatalf("%s is in %s, want the lead's window %s", sp.Name, where, leadWindow)
						}
					}
				}},
				{"the lead's window is arranged for its agents", func(t *testing.T) {
					if tc.inProcess {
						return
					}
					format := "#{pane_id}|#{@lt_role}|#{pane_left}|#{pane_width}|#{window_width}"
					waitRead(t, "the lead's window arranged", func() (bool, string) {
						panes := w.lines(t, leadWindow, format)
						return arrangedForAgents(panes, labeled), strings.Join(panes, "\n")
					})
				}},
				{"every teammate's pane is labeled and drawn by the workspace", func(t *testing.T) {
					if tc.inProcess {
						all := w.lines(t, tmux.ExactSession(w.name), "#{@lt_role}")
						if slices.Contains(all, tmux.RoleTeammate) {
							t.Fatalf("a teammate kept in process was labeled: %q", all)
						}
						return
					}
					ours := w.run(t, "show-options", "-A", "-p", "-v", "-t", leadPane, "pane-border-format")
					for _, sp := range spawns {
						color := team.ParseSpawn(sp.Args).Color
						got := map[string]string{}
						for _, o := range []string{tmux.OptRole, tmux.OptAgent, tmux.OptAgentType, tmux.OptTeam} {
							got[o] = w.run(t, "display-message", "-p", "-t", sp.Pane, "#{"+o+"}")
						}
						border := w.run(t, "show-options", "-p", "-v", "-t", sp.Pane, "pane-border-format")
						drawn := w.run(t, "show-options", "-A", "-p", "-v", "-t", sp.Pane, "pane-border-format")
						if !labeled {
							// A pane the launcher could not take over is left as
							// the agent prepared it.
							if got[tmux.OptRole] != "" || border != fakeclaude.BorderFormat(color) {
								t.Fatalf("%s: labels %v, border %q; want the agent's own pane", sp.Name, got, border)
							}
							continue
						}
						want := map[string]string{
							tmux.OptRole: tmux.RoleTeammate, tmux.OptAgent: sp.Name,
							tmux.OptAgentType: sp.AgentType, tmux.OptTeam: teamName,
						}
						for o, v := range want {
							if got[o] != v {
								t.Fatalf("%s: %s = %q, want %q", sp.Name, o, got[o], v)
							}
						}
						if border != "" || drawn != ours || drawn == fakeclaude.BorderFormat(color) {
							t.Fatalf("%s: border %q drawn as %q, want the workspace's %q", sp.Name, border, drawn, ours)
						}
					}
				}},
				{"every teammate runs as the agent in its own pane", func(t *testing.T) {
					for _, sp := range spawns {
						if tc.inProcess {
							continue
						}
						inv := w.started(t, sp.Pane)
						if inv.Program != lead.Program || !slices.Equal(inv.Args, sp.Args) || inv.Cwd != lead.Cwd {
							t.Fatalf("%s started as %s %q in %s, want %s %q in %s", sp.Name, inv.Program, inv.Args, inv.Cwd, lead.Program, sp.Args, lead.Cwd)
						}
						if got := team.ParseSpawn(inv.Args); got.Name != sp.Name || got.AgentType != sp.AgentType || got.Team != teamName {
							t.Fatalf("the launcher reads %+v from %q", got, inv.Args)
						}
						ws := ""
						if labeled {
							ws = w.name
						}
						if inv.Env[session.EnvSession] != ws || (inv.Env[session.EnvManaged] == "1") != labeled {
							t.Fatalf("%s runs with %v, want the workspace %q", sp.Name, inv.Env, ws)
						}
						waitRead(t, sp.Name+" ready", func() (bool, string) {
							screen := w.run(t, "capture-pane", "-p", "-t", sp.Pane)
							return strings.Contains(screen, fakeclaude.ReadyLine), screen
						})
					}
				}},
				{"the log says how every teammate opened", func(t *testing.T) {
					var want []string
					for _, m := range e2eTeammates {
						switch {
						case tc.inProcess:
						case tc.fallback:
							want = append(want, hook.LogTeammateFallback+": "+m.name+" opens the way the agent opens it: the pane is not in a workspace of ours")
						default:
							want = append(want, hook.LogTeammate+": "+m.name+" opened in workspace "+w.name)
						}
					}
					if got := w.teammateLog(t); !slices.Equal(got, want) {
						t.Fatalf("log\n got %q\nwant %q", got, want)
					}
				}},
				{"the rail reads every teammate in its pane", func(t *testing.T) {
					if !labeled {
						return
					}
					bar := &agentBar{client: w.srv.Client, session: w.name, claudeHome: w.env.env["CLAUDE_CONFIG_DIR"]}
					update := bar.read(tmuxtest.Context(t))
					if update.Err != nil || update.View.Team != teamName {
						t.Fatalf("rail read %+v", update)
					}
					var got []string
					for _, r := range update.View.Rows {
						if r.Group == team.GroupTeammates {
							got = append(got, r.Name+"@"+r.Pane+"|"+strconv.FormatBool(r.State == team.StateGone))
						}
					}
					var want []string
					for _, sp := range spawns {
						want = append(want, sp.Name+"@"+sp.Pane+"|false")
					}
					slices.Sort(want)
					if !slices.Equal(got, want) {
						t.Fatalf("teammates on the rail %q, want %q", got, want)
					}
				}},
				{"nothing reaches another server", func(t *testing.T) {
					calls := w.tmuxCalls(t)
					if len(calls) == 0 {
						t.Fatal("no tmux command was recorded from a pane, so nothing was checked")
					}
					for _, c := range calls {
						if socket := reached(c.args, c.env); filepath.Base(socket) != w.srv.SocketName || strings.Contains(socket, team.SwarmSocket) {
							t.Fatalf("a pane ran tmux %q with TMUX=%q, which reaches %q, not the test server %s", c.args, c.env, socket, w.srv.SocketName)
						}
					}
					for _, r := range fakeclaude.ReadTmuxRuns(t, w.env.record) {
						if len(r.Args) < 2 || r.Args[0] != "-S" || filepath.Base(r.Args[1]) != w.srv.SocketName {
							t.Fatalf("the agent ran tmux %q, which is not addressed to the test server %s", r.Args, w.srv.SocketName)
						}
					}
					if socket, _, _ := strings.Cut(lead.Env["TMUX"], ","); filepath.Base(socket) != w.srv.SocketName {
						t.Fatalf("the lead runs on %q", lead.Env["TMUX"])
					}
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

// reached is the socket a tmux command line reaches: the one it names with -S
// or -L, else the server $TMUX (env) names, which is the one tmux falls back
// to. It is empty for a command that names no socket outside tmux, which would
// reach the user's own default server.
func reached(args []string, env string) string {
	for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "-S", "-L":
			if i+1 < len(args) {
				return args[i+1]
			}
		case "-f", "-c", "-T":
			i++
		}
	}
	socket, _, _ := strings.Cut(env, ",")
	return socket
}

// arrangedForAgents reports whether panes, one "id|role|left|width|window
// width" line each, is the lead's window as the workspace arranges it for its
// agents: the rail first at its own width, and every agent stacked in the
// column it leaves. Without labeled teammates the window is the agent's own,
// which puts its first pane, the rail, in a column of 30%.
func arrangedForAgents(panes []string, labeled bool) bool {
	if len(panes) < 3 {
		return false
	}
	rail := strings.Split(panes[0], "|")
	if rail[1] != string(layout.RoleAgents) || rail[2] != "0" {
		return false
	}
	railWidth, _ := strconv.Atoi(rail[3])
	if !labeled {
		return railWidth != layout.RailWidth
	}
	if railWidth != layout.RailWidth {
		return false
	}
	for _, line := range panes[1:] {
		f := strings.Split(line, "|")
		left, _ := strconv.Atoi(f[2])
		width, _ := strconv.Atoi(f[3])
		window, _ := strconv.Atoi(f[4])
		if left != railWidth+1 || width != window-railWidth-1 {
			return false
		}
	}
	return true
}

// TestReached covers how the guard reads the server a tmux command line
// reaches, the one it names first and the one $TMUX names when it names none.
func TestReached(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "a socket path", args: []string{"-S", "/tmp/tmux-1/lt-test-1", "display-message"}, want: "/tmp/tmux-1/lt-test-1"},
		{name: "a socket name", args: []string{"-L", "lt-test-1", "list-panes"}, want: "lt-test-1"},
		{name: "a socket after other flags", args: []string{"-u", "-f", "/x.conf", "-L", "claude-swarm-42", "info"}, want: "claude-swarm-42"},
		{name: "no socket inside tmux", args: []string{"info"}, env: "/tmp/tmux-1/lt-test-1,42,0", want: "/tmp/tmux-1/lt-test-1"},
		{name: "no socket outside tmux", args: []string{"info"}},
		{name: "a socket flag with nothing after it", args: []string{"-S"}, env: "/tmp/tmux-1/x,1,0", want: "/tmp/tmux-1/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reached(tc.args, tc.env); got != tc.want {
				t.Fatalf("reached(%q, %q) = %q, want %q", tc.args, tc.env, got, tc.want)
			}
		})
	}
}
