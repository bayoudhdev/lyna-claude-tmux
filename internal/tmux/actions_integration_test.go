package tmux_test

import (
	"os"
	"path/filepath"
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
