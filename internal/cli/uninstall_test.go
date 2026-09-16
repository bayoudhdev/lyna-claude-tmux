package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// uninstallFiles fills the test home with lyna-tmux directories and a file
// that must survive.
func uninstallFiles(t *testing.T, e *infraEnv) (bystander string) {
	t.Helper()
	files := map[string]string{
		"state/tmux.conf": "# generated", "cache/agents.json": "[]", "data/review/VERSION": "2.1.0",
		"config/config.toml": "[ui]\n", "notes/keep.txt": "keep",
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
		errHas     []string
	}{
		{
			name: "confirmed on a terminal", args: []string{"uninstall"}, term: true, stdin: "y\n", wantRemove: true, wantConfig: true,
			outHas: []string{"This stops the lyna-tmux server on socket lt-test-", "[y/N]", "Removed ", "Stopped the lyna-tmux server", "Left to do by hand:", "/plugin uninstall lyna-tmux@lyna-tmux", "set -g @plugin 'bayoudhdev/lyna-claude-tmux'", "lyna-tmux completion"},
		},
		{
			name: "--yes without a terminal", args: []string{"uninstall", "--yes"}, wantRemove: true, wantConfig: true,
			outHas: []string{"Removed ", "pass --purge to remove it too"},
		},
		{
			name: "--purge removes the configuration", args: []string{"uninstall", "--yes", "--purge"}, wantRemove: true,
			outHas: []string{"config"},
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
			e.term = Terminal{Interactive: tc.term}
			e.stdin = strings.NewReader(tc.stdin)
			bystander := uninstallFiles(t, e)
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
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if _, err := os.Stat(bystander); err != nil {
				t.Fatalf("a file that is not lyna-tmux's was removed: %v", err)
			}
			if _, err := os.Stat(project); err != nil {
				t.Fatalf("the project was removed: %v", err)
			}
			for _, rel := range []string{"state", "cache", "data"} {
				_, err := os.Stat(filepath.Join(e.host.Home, rel))
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
		})
	}
}

func TestUninstallEmptyHomeCLI(t *testing.T) {
	e := newInfraEnv(t)
	code, stdout, stderr := e.run(t, "uninstall", "--yes")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing: lyna-tmux has written no files here") || strings.Contains(stdout, "Removed ") {
		t.Fatalf("stdout:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Left to do by hand:") {
		t.Fatalf("manual steps missing:\n%s", stdout)
	}
}
