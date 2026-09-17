package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// uninstallTmuxConfLine is the plugin manager entry a user of plugin mode
// added to their tmux configuration.
const uninstallTmuxConfLine = "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"

// uninstallFiles fills the test home with lyna-tmux directories, the binary,
// the zsh completion the CLI itself generates, a tmux configuration with the
// plugin manager entry, and a file that must survive.
func uninstallFiles(t *testing.T, e *infraEnv) (bystander string) {
	t.Helper()
	_, completion, stderr := e.run(t, "completion", "zsh")
	if !strings.HasPrefix(completion, "#compdef lmux\n") {
		t.Fatalf("completion zsh:\n%s\n%s", completion, stderr)
	}
	files := map[string]string{
		"state/tmux.conf": "# generated", "cache/agents.json": "[]", "data/review/VERSION": "2.1.0",
		"config/config.toml": "[ui]\n", "notes/keep.txt": "keep",
		"lmux": "#!/bin/sh\nexit 0\n", ".zfunc/_lmux": completion,
		".tmux.conf": "set -g mouse on\n" + uninstallTmuxConfLine + "\n",
	}
	for rel, content := range files {
		path := filepath.Join(e.host.Home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(e.host.Home, "notes", "keep.txt")
}

func TestUninstallCLI(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		term       bool
		stdin      string
		wantCode   int
		wantRemove bool
		wantConfig bool
		outHas     []string
		outLacks   []string
		errHas     []string
	}{
		{
			name: "confirmed on a terminal", args: []string{"uninstall"}, term: true, stdin: "y\n", wantRemove: true, wantConfig: true,
			outHas: []string{
				"This stops the lyna-tmux server on socket lt-test-", "[y/N]", "Removed ", "Stopped the lyna-tmux server",
				"(the lmux binary)", "(a shell completion lyna-tmux generated)",
				"Left to do by hand:", "remove line 2 of ", uninstallTmuxConfLine,
			},
			// No Claude plugin registry means no plugin to uninstall, and the
			// removed binary and completion need no manual step.
			outLacks: []string{"/plugin uninstall", "remove the binary", "lmux completion`"},
		},
		{
			name: "--yes without a terminal", args: []string{"uninstall", "--yes"}, wantRemove: true, wantConfig: true,
			outHas: []string{"Removed ", "pass --purge to remove it too"},
		},
		{
			name: "--purge removes the configuration", args: []string{"uninstall", "--yes", "--purge"}, wantRemove: true,
			outHas: []string{"config"}, outLacks: []string{"pass --purge"},
		},
		{
			name: "declined", args: []string{"uninstall"}, term: true, stdin: "n\n", wantCode: 1, wantConfig: true,
			errHas: []string{"canceled"},
		},
		{
			name: "without a terminal and without --yes", args: []string{"uninstall"}, wantCode: 1, wantConfig: true,
			errHas: []string{"--yes"},
		},
		{
			name: "rejects arguments", args: []string{"uninstall", "extra"}, wantCode: 1, wantConfig: true,
			errHas: []string{"unknown command"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			bystander := uninstallFiles(t, e)
			e.term = Terminal{Interactive: tc.term}
			e.stdin = strings.NewReader(tc.stdin)
			project := infraProject(t, e, "api")
			e.start(t, "api", project)
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.outLacks {
				if strings.Contains(stdout, unwanted) {
					t.Fatalf("stdout has %q:\n%s", unwanted, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			for _, rel := range []string{"notes/keep.txt", ".tmux.conf"} {
				if _, err := os.Stat(filepath.Join(e.host.Home, filepath.FromSlash(rel))); err != nil {
					t.Fatalf("a file that is not lyna-tmux's was removed: %v", err)
				}
			}
			if _, err := os.Stat(bystander); err != nil {
				t.Fatalf("a file that is not lyna-tmux's was removed: %v", err)
			}
			if _, err := os.Stat(project); err != nil {
				t.Fatalf("the project was removed: %v", err)
			}
			for _, rel := range []string{"state", "cache", "data", "lmux", ".zfunc/_lmux"} {
				_, err := os.Stat(filepath.Join(e.host.Home, filepath.FromSlash(rel)))
				if (err != nil) != tc.wantRemove {
					t.Fatalf("%s removed %v, want %v", rel, err != nil, tc.wantRemove)
				}
			}
			if _, err := os.Stat(filepath.Join(e.host.Home, "config")); (err == nil) != tc.wantConfig {
				t.Fatalf("configuration kept %v, want %v", err == nil, tc.wantConfig)
			}
			client := tmux.New(tmux.Options{Bin: e.bins["tmux"], Socket: tmux.Socket{Name: e.env["LYNA_TMUX_SOCKET_NAME"]}})
			_, err := client.Run(tmuxtest.Context(t), "has-session", "-t", "api")
			if (err != nil) != tc.wantRemove {
				t.Fatalf("workspace server stopped %v, want %v (%v)", err != nil, tc.wantRemove, err)
			}
			if !tc.wantRemove {
				return
			}
			// A second run finds nothing to stop or remove and asks nothing,
			// while the tmux configuration line is still the user's to remove.
			e.term = Terminal{Interactive: true}
			e.stdin = strings.NewReader("")
			code, stdout, stderr = e.run(t, "uninstall")
			if code != 0 || strings.Contains(stdout, "[y/N]") || strings.Contains(stdout, "Removed ") {
				t.Fatalf("second run: exit %d\n%s\n%s", code, stdout, stderr)
			}
			for _, want := range []string{"Nothing to remove", "Left to do by hand:", uninstallTmuxConfLine} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("second run lacks %q:\n%s", want, stdout)
				}
			}
		})
	}
}

// unwrapped drops every space and line break so an assertion does not depend
// on where the output was wrapped. The width follows the terminal, and a
// temporary path inside a message moves the break with it: the same run wraps
// "kill-server" across two lines on one machine and not on another.
func unwrapped(s string) string { return strings.Join(strings.Fields(s), "") }

// TestUninstallServerStopsCLI holds the run to what it reports: tmux
// acknowledges kill-server from its event loop and closes its socket on a
// later turn, so "Stopped" is printed only once the socket the plan probed
// stopped answering, and a socket that never closes fails the run before
// anything is removed. A listener the test owns stands in for the server,
// and a tmux stand-in acknowledges kill-server by writing a marker.
func TestUninstallServerStopsCLI(t *testing.T) {
	cases := []struct {
		name string
		// closes makes the listener go away once kill-server was acknowledged.
		closes     bool
		wantCode   int
		wantRemove bool
		outHas     []string
		outLacks   []string
		errHas     string
	}{
		{
			name: "the socket closes after the acknowledgement", closes: true, wantRemove: true,
			outHas: []string{"Stopped the lyna-tmux server", "Removed "},
		},
		{
			name: "the socket never closes", wantCode: 1,
			outLacks: []string{"Stopped the lyna-tmux server", "Removed "}, errHas: "still answering after kill-server",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			uninstallFiles(t, e)
			// A directory of its own keeps the socket path under the unix
			// socket length limit, which the test temporary directory would
			// not on every platform.
			tmp, err := os.MkdirTemp("", "lt")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tmp) })
			e.env["TMUX_TMPDIR"] = tmp
			socket := app.SocketPath(e.host.Getenv, e.env["LYNA_TMUX_SOCKET_NAME"])
			if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
				t.Fatal(err)
			}
			killed := filepath.Join(e.host.Home, "killed")
			e.host.TmuxBin = e.bin(t, "tmux", "case \"$*\" in *kill-server*) : > '"+killed+"' ;; esac\nexit 0\n")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_ = conn.Close()
					if _, err := os.Stat(killed); err == nil && tc.closes {
						_ = ln.Close()
						return
					}
				}
			}()
			code, stdout, stderr := e.run(t, "uninstall", "--yes")
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			if _, err := os.Stat(killed); err != nil {
				t.Fatalf("kill-server was not run: %v", err)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(unwrapped(stdout), unwrapped(want)) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.outLacks {
				if strings.Contains(unwrapped(stdout), unwrapped(unwanted)) {
					t.Fatalf("stdout has %q:\n%s", unwanted, stdout)
				}
			}
			if !strings.Contains(unwrapped(stderr), unwrapped(tc.errHas)) {
				t.Fatalf("stderr lacks %q:\n%s", tc.errHas, stderr)
			}
			if _, err := os.Stat(filepath.Join(e.host.Home, "state")); (err != nil) != tc.wantRemove {
				t.Fatalf("state removed %v, want %v", err != nil, tc.wantRemove)
			}
		})
	}
}

func TestUninstallNothingCLI(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		term     bool
		files    map[string]string
		outHas   []string
		outLacks []string
	}{
		{
			name: "on a terminal without --yes", args: []string{"uninstall"}, term: true,
			outHas: []string{"Nothing to remove", "Nothing is left to do by hand."}, outLacks: []string{"[y/N]", "Left to do by hand:", "Kept:"},
		},
		{
			name: "with --yes", args: []string{"uninstall", "--yes", "--purge"},
			outHas: []string{"Nothing to remove", "Nothing is left to do by hand."}, outLacks: []string{"[y/N]", "Removed "},
		},
		{
			name: "without a terminal and without --yes", args: []string{"uninstall"},
			outHas: []string{"Nothing to remove"}, outLacks: []string{"[y/N]"},
		},
		{
			name: "a kept configuration is still named", args: []string{"uninstall"}, term: true,
			files:  map[string]string{"config/config.toml": "[ui]\n"},
			outHas: []string{"Nothing to remove", "Kept: ", "pass --purge to remove it too"}, outLacks: []string{"[y/N]"},
		},
		{
			name: "only manual steps remain", args: []string{"uninstall"}, term: true,
			files: map[string]string{
				".tmux.conf":                             uninstallTmuxConfLine + "\n",
				".claude/plugins/installed_plugins.json": `{"version": 2, "plugins": {"lyna-tmux@lyna-tmux": [{"scope": "user"}]}}`,
				".zfunc/_lyna-tmux":                      "# mine\n",
			},
			outHas: []string{
				"Nothing to remove", "Left to do by hand:",
				"remove line 1 of ", uninstallTmuxConfLine,
				"in Claude Code, remove the companion plugin", "/plugin uninstall lyna-tmux@lyna-tmux",
				"it is not the script `lmux completion` writes",
			},
			outLacks: []string{"[y/N]", "Nothing is left to do by hand.", "remove the binary"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.term = Terminal{Interactive: tc.term}
			for rel, content := range tc.files {
				path := filepath.Join(e.host.Home, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := e.run(t, tc.args...)
			if code != 0 {
				t.Fatalf("exit %d:\n%s\n%s", code, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, unwanted := range tc.outLacks {
				if strings.Contains(stdout, unwanted) {
					t.Fatalf("stdout has %q:\n%s", unwanted, stdout)
				}
			}
			for rel := range tc.files {
				if _, err := os.Stat(filepath.Join(e.host.Home, filepath.FromSlash(rel))); err != nil {
					t.Fatalf("a run with nothing to remove removed %s: %v", rel, err)
				}
			}
		})
	}
}

func TestUninstallBinaryKeptCLI(t *testing.T) {
	cases := []struct {
		name string
		// place lays out the binary and returns its path.
		place    func(t *testing.T, home string) string
		wantStep string
		wantHow  string
	}{
		{
			name: "in a directory the user cannot write",
			place: func(t *testing.T, home string) string {
				if os.Getuid() == 0 {
					t.Skip("root writes everywhere")
				}
				dir := filepath.Join(home, "sbin")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				exe := filepath.Join(dir, "lyna-tmux")
				if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				return exe
			},
			wantStep: "you cannot write to ", wantHow: "sudo rm ",
		},
		{
			name: "in a package manager cellar",
			place: func(t *testing.T, home string) string {
				exe := filepath.Join(home, "Cellar", "lyna-tmux", "1.0.0", "bin", "lyna-tmux")
				if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				return exe
			},
			wantStep: "Homebrew installed it", wantHow: "brew uninstall lyna-tmux",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			uninstallFiles(t, e)
			e.host.Exe = tc.place(t, e.host.Home)
			code, stdout, stderr := e.run(t, "uninstall", "--yes")
			if code != 0 {
				t.Fatalf("exit %d:\n%s\n%s", code, stdout, stderr)
			}
			for _, want := range []string{"Left to do by hand:", "remove the binary " + e.host.Exe + ": " + tc.wantStep, "\n    " + tc.wantHow} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if strings.Contains(stdout, "(the lyna-tmux binary)") || strings.Contains(stdout, "Removed "+e.host.Exe) {
				t.Fatalf("the binary was planned for removal:\n%s", stdout)
			}
			if _, err := os.Stat(e.host.Exe); err != nil {
				t.Fatalf("the binary was removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(e.host.Home, "state")); err == nil {
				t.Fatal("the state directory survived a run that kept only the binary")
			}
		})
	}
}
