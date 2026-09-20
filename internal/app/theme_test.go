package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
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
	palettes, err := themePalettes(got)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range palettes {
		if p.Name != got[i] {
			t.Fatalf("palette %d is %q, want %q", i, p.Name, got[i])
		}
	}
	if _, err := themePalettes([]string{"lyna", "nope"}); err == nil {
		t.Fatal("themePalettes accepted an unknown name")
	}
}

func TestThemePreview(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		config      string
		theme       string
		wantThemes  []string // nil means every configurable theme in order
		wantCurrent string
		wantDepth   theme.Depth
		wantIcons   string
		wantErr     string
		wantIs      error
	}{
		{name: "every theme at the detected depth", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, wantCurrent: "monokai", wantDepth: theme.DepthTrue, wantIcons: "unicode"},
		{name: "one theme", env: map[string]string{"TERM": "xterm-256color"}, theme: "nord", wantThemes: []string{"nord"}, wantCurrent: "monokai", wantDepth: theme.Depth256, wantIcons: "ascii"},
		{name: "configured depth and icons win", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, config: "[ui]\ntheme = \"rose\"\ncolor = \"16\"\nicons = \"nerd\"\n", theme: "light", wantThemes: []string{"light"}, wantCurrent: "rose", wantDepth: theme.Depth16, wantIcons: "nerd"},
		{name: "unknown theme", theme: "neon", wantIs: ErrThemeUnknown, wantErr: "choose monokai, lyna"},
		{name: "invalid configuration", config: "[ui]\ntheme = 1\n", wantErr: "theme"},
		{name: "unknown theme before the configuration", config: "[ui]\ntheme = 1\n", theme: "neon", wantIs: ErrThemeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, root := themeTestHost(t, tc.env)
			if tc.config != "" {
				themeWrite(t, filepath.Join(root, "lt", "config", "config.toml"), tc.config, 0o600)
			}
			got, err := ThemePreview(h, tc.theme)
			if tc.wantErr != "" || tc.wantIs != nil {
				if err == nil {
					t.Fatalf("ThemePreview succeeded: %+v", got)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Fatalf("error %v, want %v", err, tc.wantIs)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Current != tc.wantCurrent || got.Depth != tc.wantDepth || got.Icons.Name != tc.wantIcons {
				t.Fatalf("current %q depth %v icons %q", got.Current, got.Depth, got.Icons.Name)
			}
			wantThemes := tc.wantThemes
			if wantThemes == nil {
				wantThemes = ThemeNames()
			}
			names := make([]string, len(got.Themes))
			for i, m := range got.Themes {
				names[i] = m.Palette.Name
				if want := theme.Preview(m.Palette, got.Icons); !reflect.DeepEqual(m.Lines, want) {
					t.Errorf("%s: rows differ from theme.Preview", m.Palette.Name)
				}
			}
			if !slices.Equal(names, wantThemes) {
				t.Fatalf("themes %q, want %q", names, wantThemes)
			}
		})
	}
}

// TestThemePalettesLoadInTmux sources the workspace configuration of every
// configurable theme, at every color depth, on a real isolated server and
// reads the styled options back: a palette tmux rejected would leave the
// workspace unstyled at start-up, and only tmux can judge its spellings.
func TestThemePalettesLoadInTmux(t *testing.T) {
	tmuxtest.Require(t)
	h, _ := themeTestHost(t, nil)
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		t.Fatal(err)
	}
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	v, err := srv.Client.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.Exe = filepath.Join(h.Home, "bin", "lyna-tmux")
	for _, name := range ThemeNames() {
		for _, depth := range []theme.Depth{theme.DepthTrue, theme.Depth256, theme.Depth16} {
			t.Run(name+"/"+depth.String(), func(t *testing.T) {
				cfg := config.Default()
				cfg.UI.Theme, cfg.UI.Color = name, depth.String()
				o, err := confOptions(cfg, v, h, paths)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "tmux.conf")
				if err := os.WriteFile(path, []byte(tmux.GenerateConf(o)), 0o600); err != nil {
					t.Fatal(err)
				}
				if out, err := srv.Client.Run(ctx, "source-file", path); err != nil {
					t.Fatalf("source-file: %v %s", err, out)
				}
				p := o.Look.Palette
				type styled struct{ option, value string }
				want := []styled{
					{"status-style", "bg=" + p.Bg.Tmux(depth) + ",fg=" + p.Text.Tmux(depth)},
					{"pane-active-border-style", "fg=" + p.Accent.Tmux(depth)},
					{"pane-border-style", "fg=" + p.Border.Tmux(depth)},
				}
				// The menu options arrived in tmux 3.4, so the configuration
				// only sets them on a server that has them; an older server
				// would reject the option name itself, not the palette.
				if v.Has(tmux.FeatureMenuStyles) {
					want = append(want,
						styled{"menu-style", "bg=" + p.Surface.Tmux(depth) + ",fg=" + p.Text.Tmux(depth)},
						styled{"menu-selected-style", "bg=" + p.Accent.Tmux(depth) + ",fg=" + p.Bg.Tmux(depth)},
						styled{"menu-border-style", "fg=" + p.Accent.Tmux(depth)},
					)
				}
				for _, want := range want {
					flags := "-g"
					if strings.HasPrefix(want.option, "pane-") {
						flags = "-wg"
					}
					got, err := srv.Client.ShowOption(ctx, flags, "", want.option)
					if err != nil || got != want.value {
						t.Errorf("%s = %q (%v), want %q", want.option, got, err, want.value)
					}
				}
			})
		}
	}
}

// TestThemePreviewMatchesWorkspace holds the mock to the workspace it stands
// for: every label it draws is one the tmux status formats, border format or
// session menu draw, so a renamed button or state cannot leave the preview
// showing the old one.
func TestThemePreviewMatchesWorkspace(t *testing.T) {
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	look := tmux.Look{Palette: p, Depth: theme.DepthTrue, Icons: icons, Clock: true, Buttons: true}
	formats := look.StatusLeft() + look.WindowFormat() + look.WindowCurrentFormat() + look.StatusRight() + look.BorderFormat()
	env := tmux.Env{Bindings: keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"})}
	menu := map[string]string{}
	for _, item := range env.SessionMenu().Items {
		menu[item.Label] = item.Hint
	}

	lines := theme.Preview(p, icons)
	var status, border, menus []theme.Line
	for _, l := range lines {
		switch l.Kind {
		case theme.LineStatus:
			status = append(status, l)
		case theme.LineBorder:
			border = append(border, l)
		case theme.LineMenu:
			menus = append(menus, l)
		}
	}
	cases := []struct {
		name string
		text string
	}{
		{name: "brand block", text: " " + icons.Brand + " "},
		{name: "zoomed marker", text: " [z]"},
		{name: "split button", text: icons.Split + " split"},
		{name: "review button", text: icons.Review + " review"},
		{name: "menu button", text: " " + icons.Menu + " "},
		{name: "waiting agents", text: " waiting"},
		{name: "strict shield", text: " " + icons.Shield + " strict "},
		{name: "branch", text: " " + icons.Branch + " "},
		{name: "clock", text: icons.Clock + " "},
		{name: "claude label", text: icons.Claude + " claude"},
		{name: "shell label", text: icons.Shell + " shell"},
		{name: "working state", text: " " + icons.Busy + " working"},
		{name: "subagents", text: " subagents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(formats, tmux.DrawEscapeTime(tc.text)) {
				t.Fatalf("the tmux formats never draw %q", tc.text)
			}
			shown := false
			for _, l := range append(status, border...) {
				shown = shown || strings.Contains(l.Text(), tc.text)
			}
			if !shown {
				t.Fatalf("the preview never draws %q", tc.text)
			}
		})
	}
	for _, l := range menus {
		t.Run("menu "+strings.TrimSpace(l.Text()), func(t *testing.T) {
			// An entry reads "<label>   <hint>" between the two border cells.
			entry := strings.TrimSpace(l.Spans[1].Text)
			i := strings.LastIndex(entry, "  ")
			if i < 0 {
				t.Fatalf("entry %q has no hint column", entry)
			}
			label, hint := strings.TrimSpace(entry[:i]), strings.TrimSpace(entry[i:])
			if got, ok := menu[label]; !ok || got != hint {
				t.Fatalf("session menu has %q with hint %q (listed %v), preview shows hint %q", label, got, ok, hint)
			}
		})
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
		{name: "defaults detect the depth", env: map[string]string{"COLORTERM": "truecolor"}, wantCurrent: "monokai", wantDepth: theme.DepthTrue},
		{name: "configured theme", env: map[string]string{"TERM": "xterm-256color"}, config: "[ui]\ntheme = \"ansi\"\n", wantCurrent: "ansi", wantDepth: theme.Depth256},
		{name: "configured depth wins", env: map[string]string{"COLORTERM": "truecolor"}, config: "[ui]\ncolor = \"16\"\n", wantCurrent: "monokai", wantDepth: theme.Depth16},
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
