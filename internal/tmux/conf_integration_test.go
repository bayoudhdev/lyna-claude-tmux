package tmux_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// installedVersion returns the version of the tmux under test.
func installedVersion(t *testing.T) tmux.Version {
	t.Helper()
	bin := tmuxtest.Require(t)
	v, err := tmux.New(tmux.Options{Bin: bin}).Version(tmuxtest.Context(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// unescapeKey undoes the escaping list-keys applies to the key column. tmux
// prints a key that would not survive being read back as a token with a
// backslash in front of it: ';' comes out as '\;', '#' as '\#' and 'M-\' as
// 'M-\\'. Comparing the printed column to the key we asked for needs the
// backslashes removed, otherwise a binding that is installed reads as missing.
func unescapeKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type lookSpec struct {
	palette, icons string
	depth          theme.Depth
}

func confOptions(t *testing.T, v tmux.Version, look lookSpec, env tmux.Env) tmux.ConfOptions {
	t.Helper()
	p, err := theme.Get(look.palette)
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons(look.icons)
	if err != nil {
		t.Fatal(err)
	}
	return tmux.ConfOptions{
		Version:        v,
		Look:           tmux.Look{Palette: p, Depth: look.depth, Icons: icons, Clock: true},
		Env:            env,
		Prefix:         "C-a",
		Mouse:          true,
		Bell:           true,
		StatusPosition: "bottom",
		HistoryLimit:   50000,
		Shell:          "/bin/sh",
	}
}

func writeConf(t *testing.T, conf string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestIntegrationConfLoads sources the generated configuration for every
// supported feature tier up to the installed tmux, in every look, and checks
// the server took it: no parse or option errors, bindings hold their whole
// body, registered commands read back exactly and nothing ran at load time.
func TestIntegrationConfLoads(t *testing.T) {
	installed := installedVersion(t)
	tiers := []tmux.Version{{Major: 3, Minor: 3}, {Major: 3, Minor: 4}, {Major: 3, Minor: 5}, {Major: 3, Minor: 6}, {Major: 3, Minor: 7}}
	looks := []lookSpec{
		{"lyna", "unicode", theme.DepthTrue},
		{"light", "nerd", theme.Depth256},
		{"ansi", "ascii", theme.Depth16},
	}
	env := tmux.Env{
		Bin:         "/opt/lyna tools/it's/lyna-tmux",
		ConfPath:    "/state/#1/tmux.conf",
		PopupWidth:  "90%",
		PopupHeight: "85%",
		Bindings:    keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-a"}),
	}
	for _, v := range tiers {
		if !installed.AtLeast(v.Major, v.Minor) {
			continue
		}
		for _, look := range looks {
			t.Run(v.String()+"/"+look.palette, func(t *testing.T) {
				srv := tmuxtest.Start(t)
				ctx := tmuxtest.Context(t)
				o := confOptions(t, v, look, env)
				path := writeConf(t, tmux.GenerateConf(o))
				if out, err := srv.Client.Run(ctx, "source-file", path); err != nil {
					t.Fatalf("source-file: %v %s", err, out)
				}
				// Sourcing twice must not fail or change anything either.
				if _, err := srv.Client.Run(ctx, "source-file", path); err != nil {
					t.Fatalf("second source-file: %v", err)
				}

				panes, err := srv.Client.ListPanes(ctx, "")
				if err != nil || len(panes) != 1 {
					t.Fatalf("configuration ran commands at load: %d panes, %v", len(panes), err)
				}

				for _, r := range env.Registry() {
					got, err := srv.Client.ShowOption(ctx, "-g", "", tmux.DoOption(r.Name))
					if err != nil || got != r.Seq.String() {
						t.Fatalf("registered %s = %q (%v), want %q", r.Name, got, err, r.Seq.String())
					}
				}

				// list-keys with a key argument misses keys such as M-[, so the
				// whole table is listed and matched by key column.
				listed := map[string]string{}
				for _, table := range []string{"root", "prefix"} {
					out, err := srv.Client.Run(ctx, "list-keys", "-T", table)
					if err != nil {
						t.Fatal(err)
					}
					for _, line := range strings.Split(out, "\n") {
						f := strings.Fields(line)
						if len(f) >= 5 && f[0] == "bind-key" {
							listed[f[2]+" "+unescapeKey(f[3])] = strings.Join(f[4:], " ")
						}
					}
				}
				for _, b := range env.Bindings {
					got, ok := listed[string(b.Table)+" "+b.Key]
					if !ok {
						t.Fatalf("binding %s %s not installed", b.Table, b.Key)
					}
					body := env.ActionSeq(b)
					for _, c := range body {
						if !strings.Contains(got, c[0]) {
							t.Fatalf("binding %s %s lost command %q: %s", b.Table, b.Key, c[0], got)
						}
					}
				}

				for option, want := range map[string]string{
					"prefix": "C-a", "mouse": "on", "base-index": "1", "history-limit": "50000", "default-shell": "/bin/sh",
				} {
					if got, _ := srv.Client.ShowOption(ctx, "-g", "", option); got != want {
						t.Errorf("option %s = %q, want %q", option, got, want)
					}
				}
			})
		}
	}
}

// TestIntegrationGlobEscape sources a configuration whose path is also a glob
// pattern matching a different file.
func TestIntegrationGlobEscape(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := filepath.Join(t.TempDir(), "a*b?[c]")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, file, decoy string }{
		{"class", "conf[ab].conf", "confa.conf"},
		{"star", "c*.conf", "cX.conf"},
		{"question", "c?.conf", "cY.conf"},
		{"backslash", `c\d.conf`, "cd.conf"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			option := "@lt_glob" + string(rune('a'+i))
			for file, value := range map[string]string{tc.file: "real", tc.decoy: "decoy"} {
				line := "set-option -g " + option + " " + value + "\n"
				if err := os.WriteFile(filepath.Join(dir, file), []byte(line), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := srv.Client.Run(ctx, "source-file", tmux.GlobEscape(filepath.Join(dir, tc.file))); err != nil {
				t.Fatal(err)
			}
			if got, _ := srv.Client.ShowOption(ctx, "-g", "", option); got != "real" {
				t.Fatalf("sourced %q, want the literal file", got)
			}
		})
	}
}

// TestIntegrationRegisteredCommandsParse feeds every registered command text,
// the text run-shell -C parses, to tmux's parser.
func TestIntegrationRegisteredCommandsParse(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	env := tmux.Env{
		Bin: "/w/a b/#{pane_id}/lyna-tmux", ConfPath: "/c/it's.conf", PopupWidth: "80%", PopupHeight: "80%",
		Bindings: keys.Defaults(keys.Options{AltKeys: true}),
	}
	for _, r := range env.Registry() {
		t.Run(r.Name, func(t *testing.T) {
			path := writeConf(t, r.Seq.String()+"\n")
			if _, err := srv.Client.Run(ctx, "source-file", "-n", path); err != nil {
				t.Fatalf("tmux cannot parse %s: %v", r.Name, err)
			}
		})
	}
}

var styleRE = regexp.MustCompile(`#\[[^\]]*\]`)

// drawn approximates what the status line draws from an expanded format:
// style blocks removed and escaped hashes collapsed.
func drawn(expanded string) string {
	return strings.ReplaceAll(styleRE.ReplaceAllString(expanded, ""), "##", "#")
}

// TestIntegrationStatusFormats expands the status and border formats on a
// real server with agent state set through the same options hooks write.
func TestIntegrationStatusFormats(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	look := tmux.Look{Palette: p, Depth: theme.DepthTrue, Icons: icons, Buttons: true}

	dir := t.TempDir()
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "ws", "-c", dir, "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "split-window", "-d", "-t", tmux.ExactSession("ws"), "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
	if err != nil || len(panes) != 2 {
		t.Fatalf("panes %v %v", panes, err)
	}
	claude, shell := panes[0].ID, panes[1].ID

	set := func(cmds ...tmux.Command) {
		t.Helper()
		if _, err := srv.Client.Batch(ctx, cmds...); err != nil {
			t.Fatal(err)
		}
	}
	set(
		tmux.Command{"set-option", "-p", "-t", claude, tmux.OptRole, tmux.RoleClaude},
		tmux.Command{"set-option", "-p", "-t", shell, tmux.OptRole, tmux.RoleShell},
		tmux.Command{"set-option", "-t", tmux.ExactSession("ws"), tmux.OptManaged, "1"},
		tmux.Command{"set-option", "-t", tmux.ExactSession("ws"), tmux.OptBranch, tmux.BranchOption("feat/#42-50%")},
	)

	cases := []struct {
		name          string
		setup         []tmux.Command
		target        string
		format        string
		want, notWant []string
	}{
		{
			name:   "idle agent, standard sandbox",
			setup:  []tmux.Command{{"set-option", "-p", "-t", claude, tmux.OptState, "idle"}, {"set-option", "-t", tmux.ExactSession("ws"), tmux.OptSandbox, "standard"}},
			target: claude, format: look.StatusRight(),
			want:    []string{"◎ 1", "▣ std", "⎇ feat/#42-50%", "⊞ split", "± review"},
			notWant: []string{"waiting", "sandbox off"},
		},
		{
			name:   "waiting agent, sandbox off",
			setup:  []tmux.Command{{"set-option", "-p", "-t", claude, tmux.OptState, "waiting"}, {"set-option", "-t", tmux.ExactSession("ws"), tmux.OptSandbox, "off"}},
			target: claude, format: look.StatusRight(),
			want:    []string{"◆ 1 waiting", "▢ sandbox off"},
			notWant: []string{"◎ 1"},
		},
		{
			name:   "strict sandbox",
			setup:  []tmux.Command{{"set-option", "-t", tmux.ExactSession("ws"), tmux.OptSandbox, "strict"}},
			target: claude, format: look.StatusRight(),
			want: []string{"▣ strict"},
		},
		{
			name:   "unmanaged session has no shield",
			setup:  []tmux.Command{{"set-option", "-u", "-t", tmux.ExactSession("ws"), tmux.OptManaged}},
			target: claude, format: look.StatusRight(),
			notWant: []string{"strict", "std", "sandbox off"},
		},
		{
			name:   "claude border busy with subagents",
			setup:  []tmux.Command{{"set-option", "-p", "-t", claude, tmux.OptState, "busy"}, {"set-option", "-p", "-t", claude, tmux.OptSubagents, "2"}},
			target: claude, format: look.BorderFormat(),
			want: []string{"✻ claude", "● working", "+2 subagents"},
		},
		{
			name:   "shell border without state",
			target: shell, format: look.BorderFormat(),
			want:    []string{"› shell"},
			notWant: []string{"working", "idle", "subagents"},
		},
		{
			name:   "zero subagents hidden",
			setup:  []tmux.Command{{"set-option", "-p", "-t", claude, tmux.OptSubagents, "0"}, {"set-option", "-p", "-t", claude, tmux.OptState, "waiting"}},
			target: claude, format: look.BorderFormat(),
			want:    []string{"✻ claude", "◆ needs you"},
			notWant: []string{"subagents"},
		},
		{
			name:   "window tab shows most urgent state",
			target: claude, format: look.WindowFormat(),
			want: []string{"◆ 0:"},
		},
		{
			name:   "status left",
			target: claude, format: look.StatusLeft(),
			want: []string{" λ  ws "},
		},
		{
			name: "teammate border carries its own name",
			setup: []tmux.Command{
				{"set-option", "-p", "-t", shell, tmux.OptRole, tmux.RoleTeammate},
				{"set-option", "-p", "-t", shell, tmux.OptAgent, tmux.AgentOption("review-api")},
				{"set-option", "-p", "-t", shell, tmux.OptState, "busy"},
			},
			target: shell, format: look.BorderFormat(),
			want:    []string{"◎ review-api", "● working"},
			notWant: []string{"claude", "shell"},
		},
		{
			name: "a hash in a teammate name is drawn as a hash",
			setup: []tmux.Command{
				{"set-option", "-p", "-t", shell, tmux.OptAgent, tmux.AgentOption("fix#12")},
			},
			target: shell, format: look.BorderFormat(),
			want: []string{"◎ fix#12"},
		},
		{
			name: "a teammate pane that lost its name still says what it is",
			setup: []tmux.Command{
				{"set-option", "-p", "-u", "-t", shell, tmux.OptAgent},
			},
			target: shell, format: look.BorderFormat(),
			want: []string{"◎ teammate"},
		},
		{
			name: "the rail says what it is and carries no state",
			setup: []tmux.Command{
				{"set-option", "-p", "-t", shell, tmux.OptRole, tmux.RoleAgents},
				{"set-option", "-p", "-t", shell, tmux.OptState, "busy"},
			},
			target: shell, format: look.BorderFormat(),
			want:    []string{"◎ agents"},
			notWant: []string{"teammate", "claude", "shell"},
		},
		{
			name: "the git workstation is labeled with the branch icon",
			setup: []tmux.Command{
				{"set-option", "-p", "-t", shell, tmux.OptRole, tmux.RoleGit},
			},
			target: shell, format: look.BorderFormat(),
			want:    []string{"⎇ git"},
			notWant: []string{"agents", "changes", "shell"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.setup) > 0 {
				set(tc.setup...)
			}
			out, err := srv.Client.Display(ctx, tc.target, tc.format)
			if err != nil {
				t.Fatal(err)
			}
			got := drawn(out)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("drawn %q lacks %q", got, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("drawn %q has %q", got, w)
				}
			}
			if strings.Contains(got, "#{") {
				t.Errorf("unexpanded format in %q", got)
			}
		})
	}
}

// TestIntegrationBorderShowsAFailedPane asserts on a real server that the
// border of a pane whose program exited says so, and says it instead of the
// agent state the pane carried while it was alive. A pane kept on screen after
// a failure is only useful if the label explains why it is still there.
func TestIntegrationBorderShowsAFailedPane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	look := tmux.Look{Palette: p, Depth: theme.DepthTrue, Icons: icons}

	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "ws", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	out, err := srv.Client.Run(ctx, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.ExactSession("ws"), "sleep 3600")
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(out)
	if _, err := srv.Client.Batch(ctx,
		tmux.Command{"set-option", "-p", "-t", pane, tmux.OptRole, tmux.RoleChanges},
		tmux.Command{"set-option", "-p", "-t", pane, tmux.OptState, "idle"},
		tmux.Command{"set-option", "-p", "-t", pane, "remain-on-exit", "failed"},
		tmux.Command{"respawn-pane", "-k", "-t", pane, "exit 7"},
	); err != nil {
		t.Fatal(err)
	}
	tmuxtest.WaitFor(t, "the pane to die", func() bool {
		dead, err := srv.Client.Display(ctx, pane, "#{pane_dead}")
		return err == nil && strings.TrimSpace(dead) == "1"
	})

	got, err := srv.Client.Display(ctx, pane, look.BorderFormat())
	if err != nil {
		t.Fatal(err)
	}
	drawnLabel := drawn(got)
	for _, want := range []string{"± changes", "exited"} {
		if !strings.Contains(drawnLabel, want) {
			t.Errorf("border %q lacks %q", drawnLabel, want)
		}
	}
	if strings.Contains(drawnLabel, "idle") {
		t.Errorf("border %q still shows the agent state of a dead pane", drawnLabel)
	}
	// tmux leaves pane_dead_status empty for some programs, so the number is
	// asserted only where this tmux reports one.
	status, err := srv.Client.Display(ctx, pane, "#{pane_dead_status}")
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(status); s != "" && !strings.Contains(drawnLabel, "exited "+s) {
		t.Errorf("border %q lacks the exit status %q", drawnLabel, s)
	}
}

// TestIntegrationStatusLeftFitsTheLongestName asserts on a real server that a
// workspace named up to session.MaxNameLen is drawn whole: tmux truncates
// status-left at status-left-length, counting the columns the brand block
// spends as well as the name.
func TestIntegrationStatusLeftFitsTheLongestName(t *testing.T) {
	installed := installedVersion(t)
	looks := []lookSpec{
		{"lyna", "unicode", theme.DepthTrue},
		{"light", "nerd", theme.Depth256},
		{"ansi", "ascii", theme.Depth16},
	}
	name := strings.Repeat("a", session.MaxNameLen)
	for _, look := range looks {
		t.Run(look.icons, func(t *testing.T) {
			srv := tmuxtest.Start(t)
			ctx := tmuxtest.Context(t)
			o := confOptions(t, installed, look, tmux.Env{Bin: "/opt/lmux", ConfPath: "/state/tmux.conf"})
			path := writeConf(t, tmux.GenerateConf(o))
			if out, err := srv.Client.Run(ctx, "source-file", path); err != nil {
				t.Fatalf("source-file: %v %s", err, out)
			}
			if _, err := srv.Client.Run(ctx, "rename-session", "-t", "=base", name); err != nil {
				t.Fatal(err)
			}
			// The format is expanded the way the status bar expands it, then
			// clipped to the option, which is what a client would draw.
			// A session target takes the name itself: the exact-match "="
			// form resolves nothing here, and display-message then prints an
			// empty format rather than failing.
			got, err := srv.Client.Run(ctx, "display-message", "-p", "-t", name, "#{T:status-left}")
			if err != nil {
				t.Fatal(err)
			}
			limit, err := srv.Client.ShowOption(ctx, "-g", "", "status-left-length")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, name) {
				t.Fatalf("the status bar lost the session name: %q", got)
			}
			if limit != strconv.Itoa(session.MaxNameLen+tmux.StatusLeftFixed) {
				t.Fatalf("status-left-length is %s, want the longest name plus its block", limit)
			}
		})
	}
}

// TestIntegrationAgentsRailToggles runs the registered rail command twice on a
// real server, the way the key binding and the menu run it. The first call
// opens the rail as the leftmost pane of the window, at the width the rail is
// drawn in, tagged with its role and with the focus left where it was; the
// second call closes it again. The binary is a script in a directory whose
// name has a space, so the quoting of the path survives being read back out of
// a format and parsed as a command.
func TestIntegrationAgentsRailToggles(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := filepath.Join(t.TempDir(), "lyna tools")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 3600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := tmux.Env{
		Bin: bin, ConfPath: filepath.Join(dir, "tmux.conf"), PopupWidth: "90%", PopupHeight: "85%",
		Bindings: keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-a"}),
	}
	o := confOptions(t, installedVersion(t), lookSpec{"lyna", "unicode", theme.DepthTrue}, env)
	if out, err := srv.Client.Run(ctx, "source-file", writeConf(t, tmux.GenerateConf(o))); err != nil {
		t.Fatalf("source-file: %v %s", err, out)
	}
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "ws", "-x", "120", "-y", "30", "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
	if err != nil || len(panes) != 1 {
		t.Fatalf("panes %v %v", panes, err)
	}
	lead := panes[0].ID
	if _, err := srv.Client.Run(ctx, "set-option", "-p", "-t", lead, tmux.OptRole, tmux.RoleClaude); err != nil {
		t.Fatal(err)
	}
	toggle := func() {
		t.Helper()
		if out, err := srv.Client.Run(ctx, "run-shell", "-C", "-t", lead, "#{"+tmux.DoOption(tmux.DoAgentsRail)+"}"); err != nil {
			t.Fatalf("toggle: %v %s", err, out)
		}
	}

	toggle()
	tmuxtest.WaitFor(t, "the rail to open", func() bool {
		panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
		return err == nil && len(panes) == 2
	})
	panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
	if err != nil {
		t.Fatal(err)
	}
	rail := panes[0]
	if rail.Role != string(layout.RoleAgents) {
		t.Fatalf("the first pane of the window is %q, want the rail", rail.Role)
	}
	if rail.Width != layout.RailWidth {
		t.Fatalf("the rail is %d cells wide, want %d", rail.Width, layout.RailWidth)
	}
	if rail.Active || !panes[1].Active {
		t.Fatalf("the rail took the focus: %+v", panes)
	}
	if panes[1].ID != lead {
		t.Fatalf("the lead moved: %q, want %q", panes[1].ID, lead)
	}

	toggle()
	tmuxtest.WaitFor(t, "the rail to close", func() bool {
		panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
		return err == nil && len(panes) == 1
	})
	panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession("ws"))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 || panes[0].ID != lead {
		t.Fatalf("closing the rail left %+v, want the lead alone", panes)
	}
}
