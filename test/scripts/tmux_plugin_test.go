package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

const (
	missingNotice = "lyna-tmux is not installed: see https://github.com/bayoudhdev/lyna-claude-tmux for install steps, then reload tmux"
	failedNotice  = "lyna-tmux plugin tmux --apply failed: run it in a shell inside tmux to see the error"
	hookIndex     = "[4217]"
)

// pluginEnv is a scratch home and PATH for one plugin run inside an isolated
// tmux server. The PATH holds tmux, the system tools and, depending on the
// case, a fake lyna-tmux; fake curl and wget record any download attempt.
type pluginEnv struct {
	home, bin, log string
	path           string
}

func newPluginEnv(t *testing.T) pluginEnv {
	t.Helper()
	tmuxBin := tmuxtest.Require(t)
	root := t.TempDir()
	e := pluginEnv{home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), log: filepath.Join(root, "calls.log")}
	for _, dir := range []string{e.home, e.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"curl", "wget"} {
		writeExecutable(t, filepath.Join(e.bin, name), "#!/bin/sh\necho \"download $0 $*\" >> \"$LT_TEST_LOG\"\nexit 1\n")
	}
	e.path = strings.Join([]string{e.bin, filepath.Dir(tmuxBin), "/usr/bin", "/bin"}, ":")
	return e
}

// fakeLyna writes a lyna-tmux that records its arguments and server and exits
// with status. Tests wait on tmux state rather than a wait-for signal from the
// fake: a signal sent from a run-shell job while tmux 3.7c loads its startup
// configuration does not latch, so a later wait-for blocks.
func (e pluginEnv) fakeLyna(t *testing.T, path, status string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, path, "#!/bin/sh\n"+
		"echo \"lyna-tmux $*\" >> \"$LT_TEST_LOG\"\n"+
		"echo \"server ${TMUX%%,*}\" >> \"$LT_TEST_LOG\"\n"+
		"exit "+status+"\n")
}

// conf sets the environment tmux gives run-shell jobs, then optionally runs
// the plugin the way tpm does: as an executable, while the configuration loads.
func (e pluginEnv) conf(t *testing.T, runPlugin bool) string {
	t.Helper()
	lines := []string{
		"set-environment -g PATH " + tmux.ConfQuote(e.path),
		"set-environment -g HOME " + tmux.ConfQuote(e.home),
		"set-environment -g LT_TEST_LOG " + tmux.ConfQuote(e.log),
		"set -g status-position bottom",
		// Panes run no interactive shell: one exiting after kill-server could
		// still write history into the scratch home while it is removed.
		"set -g default-command " + tmux.ConfQuote("exec sleep 86400"),
	}
	if runPlugin {
		// run-shell holds the configuration until the plugin exits, so the
		// option after it marks the end of the whole plugin run.
		lines = append(lines,
			"run-shell "+tmux.ConfQuote(tmux.FormatEscape(tmux.ShellQuote(pluginPath(t)))),
			"set -g @lt_test_plugin_done 1")
	}
	path := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e pluginEnv) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// applyCall is the log a fake binary writes when the plugin applies itself on
// the inner server.
func (e pluginEnv) applyCall(t *testing.T, n *tmuxtest.Nested) string {
	t.Helper()
	socket, err := filepath.EvalSymlinks(tmuxtest.SocketPath(n.Inner.Name))
	if err != nil {
		t.Fatal(err)
	}
	return "lyna-tmux plugin tmux --apply\nserver " + socket + "\n"
}

func pluginPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "lyna-tmux.tmux"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTmuxPluginIsExecutable(t *testing.T) {
	info, err := os.Stat(pluginPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// tpm skips *.tmux files without the execute bit.
	if info.Mode().Perm()&0o111 != 0o111 {
		t.Fatalf("lyna-tmux.tmux mode %v, want executable (git update-index --chmod=+x lyna-tmux.tmux)", info.Mode().Perm())
	}
}

func TestTmuxPluginAppliesInstalledBinary(t *testing.T) {
	cases := []struct {
		name string
		// where places the fake binary: on PATH or in the installer's default.
		where func(e pluginEnv) string
	}{
		{name: "on PATH", where: func(e pluginEnv) string { return filepath.Join(e.bin, "lyna-tmux") }},
		{name: "in ~/.local/bin only", where: func(e pluginEnv) string { return filepath.Join(e.home, ".local", "bin", "lyna-tmux") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newPluginEnv(t)
			e.fakeLyna(t, tc.where(e), "0")
			n := tmuxtest.StartNested(t, e.conf(t, true), e.home, 160, 20)
			ctx := tmuxtest.Context(t)
			tmuxtest.WaitFor(t, "plugin run to finish", func() bool {
				out, err := n.Inner.Client.Run(ctx, "show-options", "-gqv", "@lt_test_plugin_done")
				return err == nil && strings.TrimSpace(out) == "1"
			})
			if got, want := e.calls(t), e.applyCall(t, n); got != want {
				t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
			}
			hooks, err := n.Inner.Client.Run(ctx, "show-hooks", "-g")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(hooks, hookIndex) {
				t.Fatalf("a successful apply left notice hooks:\n%s", hooks)
			}
			if screen := n.Screen(t); strings.Contains(screen, "lyna-tmux") {
				t.Fatalf("a successful apply showed a message:\n%s", screen)
			}
		})
	}
}

func TestTmuxPluginNotices(t *testing.T) {
	cases := []struct {
		name string
		// status is the fake binary's exit status; empty means not installed.
		status string
		// atStart runs the plugin while tmux loads its configuration, before
		// any client is attached; otherwise it runs with a client attached.
		atStart bool
		notice  string
	}{
		{name: "missing at server start", atStart: true, notice: missingNotice},
		{name: "missing with a client attached", notice: missingNotice},
		{name: "apply failure at server start", status: "3", atStart: true, notice: failedNotice},
		{name: "apply failure with a client attached", status: "3", notice: failedNotice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newPluginEnv(t)
			if tc.status != "" {
				e.fakeLyna(t, filepath.Join(e.bin, "lyna-tmux"), tc.status)
			}
			n := tmuxtest.StartNested(t, e.conf(t, tc.atStart), e.home, 160, 20)
			ctx := tmuxtest.Context(t)
			if !tc.atStart {
				if _, err := n.Inner.Client.Run(ctx, "run-shell", tmux.FormatEscape(tmux.ShellQuote(pluginPath(t)))); err != nil {
					t.Fatalf("run plugin: %v", err)
				}
			}
			n.WaitScreen(t, tc.notice, false)

			hooks, err := n.Inner.Client.Run(ctx, "show-hooks", "-g")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(hooks, hookIndex) {
				t.Fatalf("notice hooks were not removed after the first client:\n%s", hooks)
			}
			calls := e.calls(t)
			if strings.Contains(calls, "download") {
				t.Fatalf("plugin tried to download:\n%s", calls)
			}
			wantCalls := ""
			if tc.status != "" {
				wantCalls = e.applyCall(t, n)
			}
			if calls != wantCalls {
				t.Fatalf("calls:\n%s\nwant:\n%s", calls, wantCalls)
			}
		})
	}
}
