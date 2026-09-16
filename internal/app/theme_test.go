package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// themeTestHost is an offline host rooted in a temporary directory.
func themeTestHost(t *testing.T, env map[string]string) (Host, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"LYNA_TMUX_HOME": filepath.Join(root, "lt"), "HOME": root}
	for k, v := range env {
		vars[k] = strings.ReplaceAll(v, "<root>", root)
	}
	return Host{Getenv: func(k string) string { return vars[k] }, Home: root, TmuxBin: filepath.Join(root, "no-tmux")}, root
}

func themeWrite(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func TestThemeNames(t *testing.T) {
	got := ThemeNames()
	if !slices.Equal(got, config.Choices("ui.theme")) {
		t.Fatalf("ThemeNames %q, want the documented ui.theme choices", got)
	}
	sorted := slices.Sorted(slices.Values(got))
	if !slices.Equal(sorted, theme.Names()) {
		t.Fatalf("theme names %q do not cover the palettes %q", sorted, theme.Names())
	}
	palettes, err := themePalettes()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range palettes {
		if p.Name != got[i] {
			t.Fatalf("palette %d is %q, want %q", i, p.Name, got[i])
		}
	}
}

func TestThemeList(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		config      string
		noHome      bool
		wantCurrent string
		wantDepth   theme.Depth
		wantErr     string
	}{
		{name: "defaults detect the depth", env: map[string]string{"COLORTERM": "truecolor"}, wantCurrent: "lyna", wantDepth: theme.DepthTrue},
		{name: "configured theme", env: map[string]string{"TERM": "xterm-256color"}, config: "[ui]\ntheme = \"ansi\"\n", wantCurrent: "ansi", wantDepth: theme.Depth256},
		{name: "configured depth wins", env: map[string]string{"COLORTERM": "truecolor"}, config: "[ui]\ncolor = \"16\"\n", wantCurrent: "lyna", wantDepth: theme.Depth16},
		{name: "invalid configuration", config: "[ui]\ntheme = 1\n", wantErr: "theme"},
		{name: "no home", env: map[string]string{"LYNA_TMUX_HOME": ""}, noHome: true, wantErr: xdg.ErrNoHome.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, root := themeTestHost(t, tc.env)
			if tc.noHome {
				h.Home = ""
			}
			if tc.config != "" {
				themeWrite(t, filepath.Join(root, "lt", "config", "config.toml"), tc.config, 0o600)
			}
			got, err := ThemeList(h)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Current != tc.wantCurrent || got.Depth != tc.wantDepth || got.ConfigPath != filepath.Join(root, "lt", "config", "config.toml") {
				t.Fatalf("listing %+v", got)
			}
			if len(got.Palettes) != len(ThemeNames()) {
				t.Fatalf("%d palettes", len(got.Palettes))
			}
		})
	}
}

func TestThemeSetFailures(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		noHome  bool
		setup   func(t *testing.T, root, file string)
		theme   string
		wantErr string
		wantIs  error
	}{
		{name: "unknown theme", theme: "dark", wantIs: ErrThemeUnknown},
		{name: "no home", env: map[string]string{"LYNA_TMUX_HOME": ""}, noHome: true, theme: "light", wantIs: xdg.ErrNoHome},
		{
			name: "configuration path is a directory", theme: "light", wantErr: "is not a regular file",
			setup: func(t *testing.T, _, file string) {
				t.Helper()
				if err := os.MkdirAll(file, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized file", theme: "light", wantErr: "read ",
			setup: func(t *testing.T, _, file string) {
				t.Helper()
				themeWrite(t, file, "# "+strings.Repeat("x", config.MaxFileSize)+"\n", 0o600)
			},
		},
		{
			name: "configuration directory unreadable", theme: "light", wantErr: "stat ",
			setup: func(t *testing.T, _, file string) {
				t.Helper()
				themeWrite(t, file, "[ui]\n", 0o600)
				themeChmod(t, filepath.Dir(file), 0)
			},
		},
		{
			name: "file cannot be created", theme: "light", wantErr: "permission denied",
			setup: func(t *testing.T, root, _ string) {
				t.Helper()
				themeChmod(t, root, 0o500)
			},
		},
		{
			name: "file cannot be replaced", theme: "light", wantErr: "permission denied",
			setup: func(t *testing.T, _, file string) {
				t.Helper()
				themeWrite(t, file, "[ui]\ntheme = \"lyna\"\n", 0o600)
				themeChmod(t, filepath.Dir(file), 0o500)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, root := themeTestHost(t, tc.env)
			if tc.noHome {
				h.Home = ""
			}
			file := filepath.Join(root, "lt", "config", "config.toml")
			if tc.setup != nil {
				tc.setup(t, filepath.Join(root, "lt"), file)
			}
			_, err := ThemeSet(h, tc.theme)
			if err == nil {
				t.Fatal("ThemeSet succeeded")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("error %v, want %v", err, tc.wantIs)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// themeChmod changes a directory mode for one test and restores it so the
// temporary directory can be removed.
func themeChmod(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestThemeApply(t *testing.T) {
	cases := []struct {
		name        string
		tmux        string // script body; empty means no tmux binary at all
		realServer  bool
		wantRunning bool
		wantErr     bool
	}{
		{name: "tmux not installed"},
		{name: "tmux too old", tmux: "echo 'tmux 2.9'"},
		{name: "tmux version fails", tmux: "echo broken >&2; exit 3", wantErr: true},
		{name: "server not running", realServer: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, root := themeTestHost(t, map[string]string{"TERM": "xterm-256color"})
			switch {
			case tc.tmux != "":
				h.TmuxBin = filepath.Join(root, "tmux")
				themeWrite(t, h.TmuxBin, "#!/bin/sh\n"+tc.tmux+"\n", 0o700)
			case tc.realServer:
				bin := tmuxtest.Require(t)
				socket := tmuxtest.Socket(t, bin)
				getenv := h.Getenv
				h.Getenv = func(k string) string {
					if k == "LYNA_TMUX_SOCKET_NAME" {
						return socket
					}
					return getenv(k)
				}
				h.TmuxBin = bin
			}
			running, err := ThemeApply(tmuxtest.Context(t), h)
			if (err != nil) != tc.wantErr || running != tc.wantRunning {
				t.Fatalf("ThemeApply = %v, %v; want running %v, error %v", running, err, tc.wantRunning, tc.wantErr)
			}
		})
	}
}

func TestThemeClaude(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		setup   func(t *testing.T, root string)
		wantDir string // relative to the root
		wantErr string
	}{
		{name: "missing configuration directory", wantErr: "no Claude Code configuration directory at <root>/.claude: start Claude Code once"},
		{
			name: "relative CLAUDE_CONFIG_DIR is ignored", env: map[string]string{"CLAUDE_CONFIG_DIR": "relative"},
			setup: func(t *testing.T, root string) {
				t.Helper()
				themeWrite(t, filepath.Join(root, ".claude", "settings.json"), "{}", 0o600)
			},
			wantDir: ".claude/themes",
		},
		{
			name: "CLAUDE_CONFIG_DIR", env: map[string]string{"CLAUDE_CONFIG_DIR": "<root>/cfg"},
			setup: func(t *testing.T, root string) {
				t.Helper()
				themeWrite(t, filepath.Join(root, "cfg", "settings.json"), "{}", 0o600)
			},
			wantDir: "cfg/themes",
		},
		{
			name: "themes is a file", env: map[string]string{"CLAUDE_CONFIG_DIR": "<root>/cfg"},
			setup: func(t *testing.T, root string) {
				t.Helper()
				themeWrite(t, filepath.Join(root, "cfg", "themes"), "", 0o600)
			},
			wantDir: "cfg/themes", wantErr: "not a directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, root := themeTestHost(t, tc.env)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			rep, err := ThemeClaude(h)
			wantErr := strings.ReplaceAll(tc.wantErr, "<root>", root)
			if (err != nil) != (wantErr != "") || err != nil && !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("error %v, want %q", err, wantErr)
			}
			if tc.wantDir != "" && rep.Dir != filepath.Join(root, filepath.FromSlash(tc.wantDir)) {
				t.Fatalf("themes dir %q, want %q", rep.Dir, tc.wantDir)
			}
			if err == nil && len(rep.Files) != len(ThemeNames()) {
				t.Fatalf("%d files written", len(rep.Files))
			}
		})
	}
}
