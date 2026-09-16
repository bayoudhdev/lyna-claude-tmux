package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

type popupRun struct {
	code           int
	stdout, stderr string
}

// TestPopupCLI runs the popup commands the way plugin mode key bindings do:
// $TMUX names the user's own server, and the pane and client are real ones
// of an attached client, so the popups open on a real terminal.
func TestPopupCLI(t *testing.T) {
	e := newCLIEnv(t)
	record := e.withFakeClaude(t)
	exeLog := filepath.Join(e.host.Home, "exe.log")
	script := "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$PWD\" \"$*\" \"$TMUX\" >> " + tmux.ShellQuote(exeLog) + "\n"
	if err := os.WriteFile(e.host.Exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(e.host.Home, "src", "web")
	dir := filepath.Join(project, "pkg #{x}")
	for _, d := range []string{filepath.Join(project, ".git"), dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := tmuxtest.StartNested(t, "/dev/null", dir, 120, 30)
	user := n.Inner
	ctx := tmuxtest.Context(t)
	socket := tmuxtest.SocketPath(user.Name)
	e.setenv("TMUX", socket+",1,0")
	// Panes of the user's server inherit that server's environment, not the
	// environment of the command that creates them.
	for _, key := range []string{fakeclaude.EnvRecord, "CLAUDE_CONFIG_DIR"} {
		if _, err := user.Client.Run(ctx, "set-environment", "-g", key, e.env[key]); err != nil {
			t.Fatal(err)
		}
	}
	info, err := user.Client.Run(ctx, "list-clients", "-F", "#{client_name}|#{pane_id}|#{window_id}")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Split(strings.TrimSpace(info), "|")
	client, pane, window := f[0], f[1], f[2]
	popupName := session.PopupName(session.DefaultPopupPrefix, dir)

	launch := func(t *testing.T, args ...string) <-chan popupRun {
		t.Helper()
		done := make(chan popupRun, 1)
		go func() {
			code, stdout, stderr := e.run(t, append([]string{"popup", "launch"}, args...)...)
			done <- popupRun{code, stdout, stderr}
		}()
		return done
	}
	// attached waits for a client showing the named session and returns it
	// with its active pane.
	attached := func(t *testing.T, name string) (popupClient, popupPane string) {
		t.Helper()
		tmuxtest.WaitFor(t, "a client attached to "+name, func() bool {
			out, err := user.Client.Run(ctx, "list-clients", "-F", "#{client_name}|#{session_name}|#{pane_id}")
			if err != nil {
				return false
			}
			for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
				if f := strings.Split(line, "|"); len(f) == 3 && f[1] == name {
					popupClient, popupPane = f[0], f[2]
					return true
				}
			}
			return false
		})
		return popupClient, popupPane
	}
	finished := func(t *testing.T, done <-chan popupRun) {
		t.Helper()
		select {
		case r := <-done:
			if r.code != 0 || r.stdout+r.stderr != "" {
				t.Fatalf("popup launch exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
			}
		case <-ctx.Done():
			t.Fatal("popup launch did not return after the popup closed")
		}
	}
	invocations := func(t *testing.T) []fakeclaude.Invocation {
		t.Helper()
		var got []fakeclaude.Invocation
		if _, err := os.Stat(record); err != nil {
			return nil
		}
		for _, r := range fakeclaude.ReadRecords(t, record) {
			if r.Kind == "invocation" {
				got = append(got, r)
			}
		}
		return got
	}
	option := func(t *testing.T, target, name string) string {
		t.Helper()
		v, err := user.Client.ShowOption(ctx, "", target, name)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	steps := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"flags are required", func(t *testing.T) {
			for _, sub := range []string{"launch", "agents"} {
				code, _, stderr := e.run(t, "popup", sub)
				if code != 1 || !containsFolded(stderr, `required flag(s) "client", "pane" not set`) {
					t.Fatalf("%s: exit %d, stderr %q", sub, code, stderr)
				}
			}
		}},
		{"hidden from help", func(t *testing.T) {
			if _, stdout, _ := e.run(t, "--help"); strings.Contains(stdout, "popup") {
				t.Fatalf("help lists popup:\n%s", stdout)
			}
		}},
		{"invalid requests", func(t *testing.T) {
			cases := []struct {
				tmuxEnv string
				args    []string
				errHas  string
			}{
				{"", []string{"--pane", pane, "--client", client}, "$TMUX is not set"},
				{socket + ",1,0", []string{"--pane", "main", "--client", client}, `pane "main" is not a pane id`},
				{socket + ",1,0", []string{"--pane", pane, "--client", "bad\x01"}, "not a tmux client name"},
				{socket + ",1,0", []string{"--pane", "%99999", "--client", client}, "pane %99999: tmux: target not found"},
			}
			for _, sub := range []string{"launch", "agents"} {
				for _, tc := range cases {
					e.setenv("TMUX", tc.tmuxEnv)
					code, _, stderr := e.run(t, append([]string{"popup", sub}, tc.args...)...)
					if code != 1 || !containsFolded(stderr, tc.errHas) {
						t.Fatalf("%s %q: exit %d, stderr %q, want %q", sub, tc.args, code, stderr, tc.errHas)
					}
				}
			}
			e.setenv("TMUX", socket+",1,0")
		}},
		{"launch starts Claude and shows it in a popup", func(t *testing.T) {
			done := launch(t, "--pane", pane, "--client", client)
			_, popupPane := attached(t, popupName)
			n.WaitScreen(t, fakeclaude.ReadyLine, false)
			target := tmux.ExactSession(popupName)
			if o, c := option(t, target, tmux.OptOrigin), option(t, target, tmux.OptClaudeOrigin); o != window || c != window {
				t.Fatalf("origin options %q and %q, want %s", o, c, window)
			}
			if m, p := option(t, target, tmux.OptManaged), option(t, target, tmux.OptProject); m != "1" || p != project {
				t.Fatalf("session options managed %q project %q", m, p)
			}
			settings, err := user.Client.ShowOption(ctx, "-p", popupPane, tmux.OptSettings)
			if err != nil {
				t.Fatal(err)
			}
			invs := invocations(t)
			if len(invs) != 1 {
				t.Fatalf("%d claude starts, want 1", len(invs))
			}
			inv := invs[0]
			if inv.Cwd != dir || !slices.Contains(inv.Args, "--name="+popupName) || inv.SettingsPath != settings || len(inv.Settings) == 0 {
				t.Fatalf("claude cwd %q args %q settings %q (pane has %q)", inv.Cwd, inv.Args, inv.SettingsPath, settings)
			}
			if filepath.Dir(settings) != filepath.Join(e.host.Home, "state", "plugin", "settings") {
				t.Fatalf("settings file %s outside the plugin mode directory", settings)
			}
			if inv.Env["LYNA_TMUX_SOCKET"] != socket || inv.Env["LYNA_TMUX_SESSION"] != popupName || inv.Env["LYNA_TMUX_MANAGED"] != "1" {
				t.Fatalf("claude environment %v", inv.Env)
			}
			select {
			case r := <-done:
				t.Fatalf("popup launch returned while the popup is open: %+v", r)
			default:
			}
			// Pressing the key inside the popup runs launch for the popup's
			// own pane and client: that closes the popup.
			popupClient, _ := attached(t, popupName)
			code, stdout, stderr := e.run(t, "popup", "launch", "--pane", popupPane, "--client", popupClient)
			if code != 0 || stdout+stderr != "" {
				t.Fatalf("launch inside the popup: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			finished(t, done)
			n.WaitScreen(t, fakeclaude.ReadyLine, true)
			if ok, err := user.Client.HasSession(ctx, popupName); err != nil || !ok {
				t.Fatalf("closing the popup ended the session: %v", err)
			}
		}},
		{"launch again shows the running session", func(t *testing.T) {
			done := launch(t, "--pane", pane, "--client", client)
			attached(t, popupName)
			n.WaitScreen(t, fakeclaude.ReadyLine, false)
			if got := len(invocations(t)); got != 1 {
				t.Fatalf("%d claude starts, want the running one only", got)
			}
			if _, err := user.Client.Run(ctx, "detach-client", "-s", tmux.ExactSession(popupName)); err != nil {
				t.Fatal(err)
			}
			finished(t, done)
		}},
		{"plugin options choose the session prefix", func(t *testing.T) {
			if _, err := user.Client.Batch(ctx,
				tmux.Command{"set-option", "-g", tmux.OptClaudeSessionPrefix, "agent-"},
				tmux.Command{"set-option", "-g", tmux.OptClaudePopupWidth, "50"},
				tmux.Command{"set-option", "-g", tmux.OptClaudePopupHeight, "12"},
			); err != nil {
				t.Fatal(err)
			}
			name := session.PopupName("agent-", dir)
			done := launch(t, "--pane", pane, "--client", client)
			popupClient, popupPane := attached(t, name)
			tmuxtest.WaitFor(t, "second claude start", func() bool { return len(invocations(t)) == 2 })
			if got := invocations(t)[1]; !slices.Contains(got.Args, "--name="+name) {
				t.Fatalf("claude args %q, want the session %s", got.Args, name)
			}
			// The key inside a session with the configured prefix closes it.
			code, _, stderr := e.run(t, "popup", "launch", "--pane", popupPane, "--client", popupClient)
			if code != 0 {
				t.Fatalf("launch inside the popup: %s", stderr)
			}
			finished(t, done)
		}},
		{"agents opens the picker in the pane's directory", func(t *testing.T) {
			code, stdout, stderr := e.run(t, "popup", "agents", "--pane", pane, "--client", client)
			if code != 0 || stdout+stderr != "" {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			data, err := os.ReadFile(exeLog)
			if err != nil {
				t.Fatal(err)
			}
			line := strings.TrimSpace(string(data))
			// tmux exports its socket path with symlinks resolved.
			if want := dir + "|agents --popup|" + app.SocketPath(os.Getenv, user.Name) + ","; !strings.HasPrefix(line, want) {
				t.Fatalf("picker call %q, want prefix %q", line, want)
			}
		}},
	}
	for _, st := range steps {
		if !t.Run(st.name, st.do) {
			t.Fatalf("step %q failed; later steps depend on it", st.name)
		}
	}
}
