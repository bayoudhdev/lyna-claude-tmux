package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// testHost is a host with its own LYNA_TMUX_HOME and tmux socket; the server
// on that socket is killed when the test ends.
type testHost struct {
	Host
	env  map[string]string
	root string
}

func newTestHost(t testing.TB) *testHost {
	t.Helper()
	bin := tmuxtest.Require(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &testHost{root: root, env: map[string]string{
		"LYNA_TMUX_HOME":              root,
		session.EnvSocketName:         tmuxtest.Socket(t, bin),
		"PATH":                        os.Getenv("PATH"),
		"HOME":                        root,
		"TERM":                        "xterm-256color",
		"COLORTERM":                   "truecolor",
		"LANG":                        "en_US.UTF-8",
		"CLAUDE_CODE_MESSAGING_TOKEN": "parent-secret",
		"TMUX":                        "/tmp/outer,1,0",
	}}
	h.Host = Host{Getenv: func(k string) string { return h.env[k] }, Home: root, Exe: filepath.Join(root, "bin", "lyna-tmux"), TmuxBin: bin}
	h.refreshEnviron()
	return h
}

func (h *testHost) refreshEnviron() {
	h.Environ = h.Environ[:0]
	for k, v := range h.env {
		h.Environ = append(h.Environ, k+"="+v)
	}
}

func (h *testHost) writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(h.root, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func openServer(t testing.TB, h *testHost) *Server {
	t.Helper()
	s, err := OpenServer(tmuxtest.Context(t), h.Host)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startWorkspace starts the server with a solo workspace named name.
func startWorkspace(t *testing.T, s *Server, name string) {
	t.Helper()
	plan, err := layout.Builtin(layout.Solo, layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := tmuxtest.Context(t)
	_, err = s.Client.CreateWorkspace(ctx, tmux.WorkspaceSpec{
		Session: name,
		Window:  tmux.WindowSpec{Dir: s.Paths.Home, Plan: plan, Procs: []tmux.PaneProcess{{Argv: []string{"/bin/sh", "-c", "exec sleep 3600"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenServerWritesConfiguration(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	conf := s.Paths.TmuxConf()
	info, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("conf mode %v", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(conf))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode %v, %v", dirInfo.Mode().Perm(), err)
	}
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if tmux.ConfFingerprint(string(data)) != s.Fingerprint || !strings.Contains(string(data), h.Exe) {
		t.Fatal("written configuration does not match the fingerprint or the binary path")
	}

	t.Run("unchanged configuration is not rewritten", func(t *testing.T) {
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		if err := os.Chtimes(conf, old, old); err != nil {
			t.Fatal(err)
		}
		again := openServer(t, h)
		info, err := os.Stat(conf)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) || again.Fingerprint != s.Fingerprint {
			t.Fatalf("rewritten: mtime %v, fingerprint %s vs %s", info.ModTime(), again.Fingerprint, s.Fingerprint)
		}
	})

	t.Run("changed configuration is rewritten", func(t *testing.T) {
		h.writeConfig(t, "[ui]\ntheme = \"light\"\n")
		changed := openServer(t, h)
		if changed.Fingerprint == s.Fingerprint || changed.Config.UI.Theme != "light" {
			t.Fatalf("fingerprint %s, theme %q", changed.Fingerprint, changed.Config.UI.Theme)
		}
		data, err := os.ReadFile(conf)
		if err != nil || tmux.ConfFingerprint(string(data)) != changed.Fingerprint {
			t.Fatalf("file not updated: %v", err)
		}
	})
}

func TestOpenServerErrors(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, h *testHost)
		wantIs  error
		wantMsg string
	}{
		{
			name:    "invalid configuration",
			prepare: func(t *testing.T, h *testHost) { t.Helper(); h.writeConfig(t, "[ui]\ntheme = \"neon\"\n") },
			wantMsg: "config.toml",
		},
		{
			name:    "invalid socket name",
			prepare: func(_ *testing.T, h *testHost) { h.env[session.EnvSocketName] = "../x" },
			wantMsg: session.EnvSocketName,
		},
		{
			name:    "tmux missing",
			prepare: func(_ *testing.T, h *testHost) { h.TmuxBin = filepath.Join(h.root, "no-tmux") },
			wantIs:  tmux.ErrNotInstalled,
		},
		{
			name: "tmux too old",
			prepare: func(t *testing.T, h *testHost) {
				t.Helper()
				old := filepath.Join(h.root, "old-tmux")
				if err := os.WriteFile(old, []byte("#!/bin/sh\necho 'tmux 3.2a'\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				h.TmuxBin = old
			},
			wantIs:  ErrTmuxTooOld,
			wantMsg: "found 3.2a",
		},
		{
			name: "symlinked generated configuration refused",
			prepare: func(t *testing.T, h *testHost) {
				t.Helper()
				state := filepath.Join(h.root, "state")
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(h.root, "elsewhere.conf"), filepath.Join(state, "tmux.conf")); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "tmux.conf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHost(t)
			tc.prepare(t, h)
			_, err := OpenServer(tmuxtest.Context(t), h.Host)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want %v", err, tc.wantIs)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantMsg)
			}
		})
	}
}

func TestServerSync(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	ctx := tmuxtest.Context(t)
	running, err := s.Sync(ctx)
	if err != nil || running {
		t.Fatalf("stopped server: running %v, %v", running, err)
	}
	startWorkspace(t, s, "sync")
	if err := s.MarkLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	// A manual change survives a sync with an unchanged configuration and is
	// reset when the configuration changes and the file is sourced again.
	if _, err := s.Client.Run(ctx, "set-option", "-g", "status-left", "manual"); err != nil {
		t.Fatal(err)
	}
	running, err = s.Sync(ctx)
	if err != nil || !running {
		t.Fatalf("running server: running %v, %v", running, err)
	}
	if got, _ := s.Client.ShowOption(ctx, "-g", "", "status-left"); got != "manual" {
		t.Fatalf("unchanged configuration was sourced again: status-left %q", got)
	}

	h.writeConfig(t, "[ui]\ntheme = \"ansi\"\n")
	changed := openServer(t, h)
	if _, err := changed.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := changed.Client.ShowOption(ctx, "-g", "", "status-left"); got == "manual" {
		t.Fatal("changed configuration was not sourced")
	}
	if got, _ := changed.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); got != changed.Fingerprint {
		t.Fatalf("recorded fingerprint %q, want %q", got, changed.Fingerprint)
	}
}

func TestServerEnvironmentScrubbed(t *testing.T) {
	h := newTestHost(t)
	s := openServer(t, h)
	startWorkspace(t, s, "env")
	out, err := s.Client.Run(tmuxtest.Context(t), "show-environment", "-g")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	has := func(prefix string) bool {
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return true
			}
		}
		return false
	}
	if has("CLAUDE_CODE_MESSAGING_TOKEN=") || has("TMUX=") {
		t.Fatalf("server environment has caller-only variables:\n%s", out)
	}
	if !has("PATH=") || !has("LANG=en_US.UTF-8") {
		t.Fatalf("server environment lost user variables:\n%s", out)
	}
}
