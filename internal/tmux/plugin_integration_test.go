package tmux_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func hookLines(t *testing.T, srv *tmuxtest.Server) []string {
	t.Helper()
	out, err := srv.Client.Run(tmuxtest.Context(t), "show-hooks", "-g")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "alert-bell") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestIntegrationPluginApply(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	if _, err := srv.Client.Run(ctx, "set-hook", "-g", "alert-bell", "display-message user-hook"); err != nil {
		t.Fatal(err)
	}
	opts := tmux.PluginOptions{Bin: "/opt/it's #{x}/lyna-tmux", LaunchKey: "y", ListKey: "u", ForwardBell: true}
	// Applying twice must not duplicate anything.
	for range 2 {
		if _, err := srv.Client.Batch(ctx, tmux.PluginSeq(opts)...); err != nil {
			t.Fatal(err)
		}
	}
	hooks := hookLines(t, srv)
	if len(hooks) != 2 || !strings.HasPrefix(hooks[0], "alert-bell[0] display-message user-hook") ||
		!strings.HasPrefix(hooks[1], "alert-bell[91] run-shell -b ") {
		t.Fatalf("alert-bell hooks = %q", hooks)
	}
	keys, err := srv.Client.Run(ctx, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"popup launch --pane #{pane_id} --client #{q:client_name}", "popup agents --pane #{pane_id} --client #{q:client_name}"} {
		if strings.Count(keys, want) != 1 {
			t.Errorf("prefix table has %d bindings with %q", strings.Count(keys, want), want)
		}
	}

	opts.ForwardBell = false
	if _, err := srv.Client.Batch(ctx, tmux.PluginSeq(opts)...); err != nil {
		t.Fatal(err)
	}
	if hooks := hookLines(t, srv); len(hooks) != 1 || !strings.HasPrefix(hooks[0], "alert-bell[0] ") {
		t.Fatalf("after disabling bell forwarding hooks = %q", hooks)
	}
}

func TestIntegrationReadPluginOptions(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	set := map[string]string{
		tmux.OptClaudeLaunchKey:     "Y",
		tmux.OptClaudeForwardBell:   "off",
		tmux.OptClaudeArgs:          "--model 'opus' ;#{pane_id} %H",
		tmux.OptClaudePopupHeight:   "80%",
		tmux.OptClaudeSessionPrefix: "agent-",
	}
	for name, v := range set {
		if _, err := srv.Client.Run(ctx, "set-option", "-g", name, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := srv.Client.ReadPluginOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := tmux.PluginUserOptions{LaunchKey: "Y", ForwardBell: "off", SessionPrefix: "agent-", Args: "--model 'opus' ;#{pane_id} %H", PopupHeight: "80%"}
	if got != want {
		t.Fatalf("ReadPluginOptions = %+v, want %+v", got, want)
	}
}

// TestE2EPlugin sources the file `plugin tmux --write` produces into a server
// with an attached client, presses the prefix keys and rings a bell in a
// popup session, and checks what the lyna-tmux binary receives.
func TestE2EPlugin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin #{dir} it's")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "calls")
	bin := filepath.Join(binDir, "lyna-tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + tmux.ShellQuote(marker) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "plugin.conf")
	body := "set-option -g default-shell /bin/sh\n" +
		tmux.PluginConf(tmux.PluginOptions{Bin: bin, LaunchKey: "y", ListKey: "u", ForwardBell: true})
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	n := tmuxtest.StartNested(t, conf, root, 120, 30)
	ctx := tmuxtest.Context(t)
	info, err := n.Inner.Client.Run(ctx, "list-clients", "-F", "#{client_name} #{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	client, pane, _ := strings.Cut(strings.TrimSpace(info), " ")

	lines := func() []string {
		data, _ := os.ReadFile(marker)
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	waitCall := func(t *testing.T, want string) {
		t.Helper()
		tmuxtest.WaitFor(t, "call "+want, func() bool {
			for _, l := range lines() {
				if l == want {
					return true
				}
			}
			return false
		})
	}

	steps := []struct {
		name string
		keys []string
		want string
	}{
		{name: "launch key", keys: []string{"C-b", "y"}, want: "popup launch --pane " + pane + " --client " + client},
		{name: "list key", keys: []string{"C-b", "u"}, want: "popup agents --pane " + pane + " --client " + client},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			n.Keys(t, s.keys...)
			waitCall(t, s.want)
		})
	}

	t.Run("bell in a popup session", func(t *testing.T) {
		id, err := n.Inner.Client.Run(ctx, "new-session", "-d", "-s", "claude-0badcafe", "-P", "-F", "#{session_id}",
			"--", "/bin/sh", "-c", `sleep 0.2; printf '\a'; exec sleep 3600`)
		if err != nil {
			t.Fatal(err)
		}
		waitCall(t, "bell-forward "+strings.TrimSpace(id))
	})
}
