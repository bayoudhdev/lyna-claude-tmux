package tmux_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// TestIntegrationAgentsRailOpensAtItsWidth runs the registered rail toggle on
// a real server loaded from a generated configuration, once per width the
// workspace can ask for: the width travels through the configuration file and
// the stored command to the split that opens the rail, and nowhere on the way
// is it replaced by the default.
func TestIntegrationAgentsRailOpensAtItsWidth(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "lmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 3600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "default", want: layout.RailWidth},
		{name: "narrowest", width: layout.MinRailWidth, want: layout.MinRailWidth},
		{name: "wider", width: 44, want: 44},
		{name: "widest", width: layout.MaxRailWidth, want: layout.MaxRailWidth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tmux.Env{
				Bin: bin, ConfPath: filepath.Join(dir, "tmux.conf"), PopupWidth: "90%", PopupHeight: "85%",
				Bindings: keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"}), RailWidth: tc.width,
			}
			o := confOptions(t, installedVersion(t), lookSpec{"lyna", "unicode", theme.DepthTrue}, env)
			if out, err := srv.Client.Run(ctx, "source-file", writeConf(t, tmux.GenerateConf(o))); err != nil {
				t.Fatalf("source-file: %v %s", err, out)
			}
			name := "rail-" + tc.name
			if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", name, "-x", "200", "-y", "30", "sleep 3600"); err != nil {
				t.Fatal(err)
			}
			lead, err := srv.Client.Display(ctx, tmux.ExactSession(name), "#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			if out, err := srv.Client.Run(ctx, "run-shell", "-C", "-t", lead, "#{"+tmux.DoOption(tmux.DoAgentsRail)+"}"); err != nil {
				t.Fatalf("toggle: %v %s", err, out)
			}
			var panes []tmux.Pane
			tmuxtest.WaitFor(t, "the rail to open", func() bool {
				panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession(name))
				return err == nil && len(panes) == 2
			})
			rail := panes[0]
			if rail.Role != string(layout.RoleAgents) {
				t.Fatalf("the first pane of the window is %q, want the rail", rail.Role)
			}
			if rail.Width != tc.want {
				t.Fatalf("the rail is %d cells wide, want %d", rail.Width, tc.want)
			}
			if got := panes[1].Width; got != 200-tc.want-1 {
				t.Fatalf("the lead is %d cells wide, want %d", got, 200-tc.want-1)
			}
		})
	}
}

// TestIntegrationGitWorkstationToggle runs the registered git workstation
// toggle on a real server loaded from a generated configuration. The share the
// workspace leaves the Claude pane travels through the configuration file and
// the stored command to the split that opens the workstation, the workstation
// lands in a column of its own on the right of a window that already holds two
// panes, and running the toggle again closes it.
func TestIntegrationGitWorkstationToggle(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "lmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 3600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const width = 200
	cases := []struct {
		name  string
		ratio int
		want  int
	}{
		{name: "default", want: width * 38 / 100},
		{name: "a wider claude pane", ratio: 70, want: width * 30 / 100},
		{name: "a ratio no window can be split at", ratio: 95, want: width * 38 / 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tmux.Env{
				Bin: bin, ConfPath: filepath.Join(dir, "tmux.conf"), PopupWidth: "90%", PopupHeight: "85%",
				Bindings: keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"}), SplitRatio: tc.ratio,
			}
			o := confOptions(t, installedVersion(t), lookSpec{"lyna", "unicode", theme.DepthTrue}, env)
			if out, err := srv.Client.Run(ctx, "source-file", writeConf(t, tmux.GenerateConf(o))); err != nil {
				t.Fatalf("source-file: %v %s", err, out)
			}
			name := "git-" + tc.name
			if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", name,
				"-x", strconv.Itoa(width), "-y", "50", "sleep 3600"); err != nil {
				t.Fatal(err)
			}
			// A second pane below the first: the workstation must take a column
			// of the window rather than half of the pane the toggle ran in.
			if _, err := srv.Client.Run(ctx, "split-window", "-v", "-t", tmux.ExactSession(name), "sleep 3600"); err != nil {
				t.Fatal(err)
			}
			panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession(name))
			if err != nil || len(panes) != 2 {
				t.Fatalf("panes %v %v", panes, err)
			}
			if out, err := srv.Client.Run(ctx, "run-shell", "-C", "-t", panes[1].ID,
				"#{"+tmux.DoOption(tmux.DoGitWork)+"}"); err != nil {
				t.Fatalf("toggle: %v %s", err, out)
			}
			tmuxtest.WaitFor(t, "the git workstation to open", func() bool {
				panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession(name))
				return err == nil && len(panes) == 3
			})
			work := panes[len(panes)-1]
			if work.Role != string(layout.RoleGit) {
				t.Fatalf("the last pane of the window is %q, want the git workstation", work.Role)
			}
			if !work.Active {
				t.Error("the workstation is not the active pane: it is typed in, not glanced at")
			}
			if work.Width != tc.want {
				t.Errorf("the workstation is %d cells wide, want %d", work.Width, tc.want)
			}
			// The column runs the full height of the window: the panes it opened
			// beside keep the width the border leaves them, stacked as they were.
			for i, p := range panes[:2] {
				if p.Height >= work.Height {
					t.Errorf("pane %d is %d cells high and the workstation %d: it is not a column of its own", i+1, p.Height, work.Height)
				}
				if got := p.Width; got != width-tc.want-1 {
					t.Errorf("pane %d is %d cells wide, want %d", i+1, got, width-tc.want-1)
				}
			}
			if out, err := srv.Client.Run(ctx, "run-shell", "-C", "-t", work.ID,
				"#{"+tmux.DoOption(tmux.DoGitWork)+"}"); err != nil {
				t.Fatalf("toggle again: %v %s", err, out)
			}
			tmuxtest.WaitFor(t, "the git workstation to close", func() bool {
				panes, err = srv.Client.ListPanes(ctx, tmux.ExactSession(name))
				return err == nil && len(panes) == 2
			})
			for _, p := range panes {
				if p.Role == string(layout.RoleGit) {
					t.Fatalf("the workstation is still there: %+v", p)
				}
			}
		})
	}
}
