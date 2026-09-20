package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestThemeListCLI(t *testing.T) {
	interactive := Terminal{Interactive: true, Width: 120, Height: 40}
	cases := []struct {
		name     string
		config   string
		env      map[string]string
		term     Terminal
		args     []string
		wantCode int
		golden   string
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{name: "defaults at 256 colors", env: map[string]string{"TERM": "xterm-256color"}, args: []string{"theme"}, golden: "theme/list-256.txt", outHas: []string{"Preview one with: lmux theme preview <name>\n"}},
		{name: "configured theme is marked", config: "[ui]\ntheme = \"light\"\n", args: []string{"theme"}, outHas: []string{"* light", "\n  lyna ", "\n  ansi "}},
		{
			name: "configured depth wins over detection", config: "[ui]\ncolor = \"16\"\n", env: map[string]string{"COLORTERM": "truecolor"}, args: []string{"theme"},
			outHas: []string{"COLORS (16)", "color2 color12 color3 color9 color7 color8"}, outLacks: []string{"#39d353"},
		},
		{
			name: "truecolor blocks on a terminal", env: map[string]string{"COLORTERM": "truecolor"}, term: interactive, args: []string{"theme"},
			outHas:   []string{"COLORS (truecolor)", "\x1b[38;2;57;211;83m\u2588\u2588\x1b[0m \x1b[38;2;88;166;255m\u2588\u2588\x1b[0m", "\x1b[32m\u2588\u2588\x1b[0m \x1b[34m\u2588\u2588\x1b[0m"},
			outLacks: []string{"#39d353"},
		},
		{
			name: "256 color blocks on a terminal", env: map[string]string{"TERM": "screen-256color"}, term: interactive, args: []string{"theme"},
			outHas: []string{"COLORS (256)", "\x1b[38;5;"},
		},
		{
			name: "NO_COLOR spells the colors", env: map[string]string{"COLORTERM": "truecolor", "NO_COLOR": "1"}, term: interactive, args: []string{"theme"},
			outHas: []string{"#39d353 #58a6ff #f0b72f #ff5f56 #e6edf3 #7d8590"}, outLacks: []string{"\x1b["},
		},
		{
			name: "no terminal spells the colors", env: map[string]string{"COLORTERM": "truecolor"}, args: []string{"theme"},
			outHas: []string{"#1a7f37 #0969da", "color2 color4 color3 color1 color15 color7"}, outLacks: []string{"\x1b["},
		},
		{
			name: "sanitizes the config path", env: map[string]string{"LYNA_TMUX_HOME": "/tmp/lt\x1b]0;x\x07home"}, args: []string{"theme"},
			outHas: []string{"ui.theme in " + sanitize.Line("/tmp/lt\x1b]0;x\x07home/config/config.toml") + ")"}, outLacks: []string{"\x1b", "\x07"},
		},
		{name: "invalid config", config: "[ui]\ntheme = \"nope\"\n", args: []string{"theme"}, wantCode: 1, errHas: []string{"theme"}},
		{name: "too many arguments", args: []string{"theme", "light", "ansi"}, wantCode: 1, errHas: []string{"accepts at most 1 arg"}},
		{name: "completion lists themes and the subcommands", args: []string{"__complete", "theme", ""}, outHas: []string{"\nlyna\n", "\nlight\n", "\nansi\n", "\nnord\n", "claude\t", "preview\t"}},
		{name: "completion filters by prefix", args: []string{"__complete", "theme", "l"}, outHas: []string{"lyna\n", "light\n"}, outLacks: []string{"ansi", "claude", "preview"}},
		{name: "completion stops after a name", args: []string{"__complete", "theme", "light", ""}, outLacks: []string{"lyna", "ansi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGlueEnv(t)
			e.term = tc.term
			for k, v := range tc.env {
				e.env[k] = v
			}
			if tc.config != "" {
				e.writeConfig(t, tc.config)
			}
			code, stdout, stderr := e.run(t, "", tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.golden != "" {
				golden.Assert(t, tc.golden, []byte(strings.ReplaceAll(stdout, e.configFile(), "<config>")))
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%q", s, stdout)
				}
			}
			for _, s := range tc.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%q", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
}

func TestThemePreviewCLI(t *testing.T) {
	terminal := Terminal{Interactive: true, Width: 140, Height: 40}
	// lynaGap is the status line gap of the lyna theme at 24-bit depth: the
	// status style, text on the background. On the 140-column terminal it is
	// what the status row leaves free after the indent and the row label.
	lynaGap := "\x1b[38;2;230;237;243;48;2;13;17;23m"
	statusCells := 0
	lyna, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	unicode, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := theme.GetSeps(theme.StatusPlain)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slices.Concat(theme.Preview(lyna, unicode, plain)[0].Spans, theme.Preview(lyna, unicode, plain)[0].Right) {
		statusCells += ansi.StringWidth(s.Text)
	}
	edgeGap := 140 - len(previewIndent) - previewLabel - statusCells
	if edgeGap <= 2 {
		t.Fatalf("the status row is %d cells wide, too wide for the test terminal", statusCells)
	}
	cases := []struct {
		name     string
		config   string
		env      map[string]string
		term     Terminal
		args     []string
		wantCode int
		golden   string
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{name: "16 colors on a terminal", env: map[string]string{"TERM": "xterm"}, term: terminal, args: []string{"theme", "preview", "lyna"}, golden: "theme/preview-lyna-16.txt"},
		{name: "256 colors on a terminal", env: map[string]string{"TERM": "xterm-256color"}, term: terminal, args: []string{"theme", "preview", "lyna"}, golden: "theme/preview-lyna-256.txt"},
		{name: "truecolor on a terminal", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, term: terminal, args: []string{"theme", "preview", "lyna"}, golden: "theme/preview-lyna-true.txt"},
		{name: "NO_COLOR names the colors", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8", "NO_COLOR": "1"}, term: terminal, args: []string{"theme", "preview", "lyna"}, golden: "theme/preview-lyna-plain.txt", outLacks: []string{"\x1b"}},
		{
			name: "no terminal names the colors", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, args: []string{"theme", "preview", "nord"},
			outHas:   []string{"nord: dark\n", "  status  ", " λ  api ", "◆ 1 waiting", "  colors  bg=#2e3440 surface=#3b4252 ", " text=#eceff4 accent=#88c0d0 ", " warning=#d08770\n", "  swatch  #88c0d0 #81a1c1 #ebcb8b #bf616a #eceff4 #98a3b8\n", "\nSwitch with: lmux theme nord\n"},
			outLacks: []string{"\x1b", "(configured)"},
		},
		{
			name: "status line reaches the right edge", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, term: terminal, args: []string{"theme", "preview", "lyna"},
			outHas: []string{lynaGap + strings.Repeat(" ", edgeGap) + "\x1b[0m"},
		},
		{
			name: "narrow terminal keeps a two-space gap", env: map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"}, term: Terminal{Interactive: true, Width: 40}, args: []string{"theme", "preview", "lyna"},
			outHas: []string{lynaGap + "  \x1b[0m"},
		},
		{
			name: "every theme in documented order", env: map[string]string{"TERM": "xterm-256color"}, args: []string{"theme", "preview"},
			outHas:   []string{"monokai: dark (configured)\n", "\nslate: dark\n", "\nlight: light\n", "\nansi: terminal colors\n", "  colors  bg=color0 ", "\nSwitch with: lmux theme <name>\n"},
			outLacks: []string{"\x1b"},
		},
		{name: "configured theme is marked", config: "[ui]\ntheme = \"rose\"\n", args: []string{"theme", "preview"}, outHas: []string{"\nrose: dark (configured)\n", "lyna: dark\n"}},
		{name: "configured depth wins over detection", config: "[ui]\ncolor = \"16\"\n", env: map[string]string{"COLORTERM": "truecolor"}, args: []string{"theme", "preview", "lyna"}, outHas: []string{"  colors  bg=color0 surface=color0 "}, outLacks: []string{"#0d1117"}},
		{name: "configured icons are drawn", config: "[ui]\nicons = \"ascii\"\n", env: map[string]string{"LANG": "en_US.UTF-8"}, args: []string{"theme", "preview", "lyna"}, outHas: []string{" L  api ", "o 1:main", "-- > claude * working"}, outLacks: []string{"λ", "━"}},
		{name: "unknown theme", args: []string{"theme", "preview", "neon"}, wantCode: 1, errHas: []string{`unknown theme "neon": choose ` + strings.Join(app.ThemeNames(), ", ")}},
		{name: "invalid configuration", config: "[ui]\ntheme = \"nope\"\n", args: []string{"theme", "preview", "lyna"}, wantCode: 1, errHas: []string{"ui.theme"}},
		{name: "too many arguments", args: []string{"theme", "preview", "lyna", "nord"}, wantCode: 1, errHas: []string{"accepts at most 1 arg"}},
		{name: "completion lists the themes", args: []string{"__complete", "theme", "preview", "s"}, outHas: []string{"slate\n", "solar-dark\n", "solar-light\n"}, outLacks: []string{"lyna", "claude"}},
		{name: "completion stops after a name", args: []string{"__complete", "theme", "preview", "nord", ""}, outLacks: []string{"lyna", "slate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGlueEnv(t)
			e.term = tc.term
			for k, v := range tc.env {
				e.env[k] = v
			}
			if tc.config != "" {
				e.writeConfig(t, tc.config)
			}
			code, stdout, stderr := e.run(t, "", tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.golden != "" {
				golden.Assert(t, tc.golden, []byte(stdout))
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%q", s, stdout)
				}
			}
			for _, s := range tc.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%q", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
}

func TestThemeBackground(t *testing.T) {
	c := theme.RGB(0x39, 0xd3, 0x53)
	cases := []struct {
		name  string
		color theme.Color
		depth theme.Depth
		want  string
	}{
		{name: "truecolor", color: c, depth: theme.DepthTrue, want: "48;2;57;211;83"},
		{name: "256", color: c, depth: theme.Depth256, want: "48;5;77"},
		{name: "16 normal", color: c, depth: theme.Depth16, want: "42"},
		{name: "16 bright", color: theme.RGB(255, 255, 255), depth: theme.Depth16, want: "107"},
		{name: "indexed normal ignores depth", color: theme.ANSI(4), depth: theme.DepthTrue, want: "44"},
		{name: "indexed bright", color: theme.ANSI(12), depth: theme.Depth256, want: "104"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := themeBackground(tc.color, tc.depth); got != tc.want {
				t.Fatalf("themeBackground() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestThemePaintLine(t *testing.T) {
	fg, bg := theme.RGB(1, 2, 3), theme.RGB(4, 5, 6)
	paint := func(text string, bold bool) string {
		if bold {
			return "\x1b[1;38;2;1;2;3;48;2;4;5;6m" + text + "\x1b[0m"
		}
		return "\x1b[38;2;1;2;3;48;2;4;5;6m" + text + "\x1b[0m"
	}
	filled := theme.Span{Text: "ab", Fg: fg, Bg: bg, Fill: true}
	// The gap is drawn in the base style, not in the style of either
	// neighbor: the status line's empty cells are its own.
	base := theme.Span{Fg: theme.RGB(7, 8, 9), Bg: bg, Fill: true}
	gap := func(n int) string { return "\x1b[38;2;7;8;9;48;2;4;5;6m" + strings.Repeat(" ", n) + "\x1b[0m" }
	cases := []struct {
		name  string
		line  theme.Line
		width int
		want  string
	}{
		{name: "no background", line: theme.Line{Spans: []theme.Span{{Text: "x", Fg: fg}}}, want: "\x1b[38;2;1;2;3mx\x1b[0m"},
		{name: "bold filled", line: theme.Line{Spans: []theme.Span{{Text: "x", Fg: fg, Bg: bg, Fill: true, Bold: true}}}, want: paint("x", true)},
		{name: "right part fills to the width", line: theme.Line{Spans: []theme.Span{filled}, Right: []theme.Span{filled}, Base: base}, width: 10, want: paint("ab", false) + gap(6) + paint("ab", false)},
		{name: "wide glyphs count their cells", line: theme.Line{Spans: []theme.Span{{Text: "日本", Fg: fg, Bg: bg, Fill: true}}, Right: []theme.Span{filled}, Base: base}, width: 10, want: paint("日本", false) + gap(4) + paint("ab", false)},
		{name: "unknown width keeps two spaces", line: theme.Line{Spans: []theme.Span{filled}, Right: []theme.Span{filled}, Base: base}, want: paint("ab", false) + gap(2) + paint("ab", false)},
		{name: "too narrow keeps two spaces", line: theme.Line{Spans: []theme.Span{filled}, Right: []theme.Span{filled}, Base: base}, width: 5, want: paint("ab", false) + gap(2) + paint("ab", false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := themePaintLine(tc.line, theme.DepthTrue, tc.width); got != tc.want {
				t.Fatalf("themePaintLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestThemeSetCLI(t *testing.T) {
	e := newGlueEnv(t)
	file := e.configFile()
	withTheme := func(name string) []byte {
		return bytes.Replace(config.Template(), []byte(`theme = "monokai"`), []byte(`theme = "`+name+`"`), 1)
	}
	fileIs := func(t *testing.T, want []byte) {
		t.Helper()
		if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("config file %q (%v), want %q", got, err, want)
		}
	}
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	dotfile := filepath.Join(e.root, "dotfiles", "lyna.toml")
	var before []byte
	steps := []struct {
		name     string
		setup    func(t *testing.T)
		args     []string
		wantCode int
		outHas   []string
		outLacks []string
		errHas   []string
		check    func(t *testing.T)
	}{
		{
			name: "unknown theme writes nothing", args: []string{"theme", "nope"}, wantCode: 1,
			errHas: []string{`unknown theme "nope": choose ` + strings.Join(app.ThemeNames(), ", ")},
			check: func(t *testing.T) {
				t.Helper()
				if _, err := os.Lstat(filepath.Dir(file)); !os.IsNotExist(err) {
					t.Fatalf("config directory created: %v", err)
				}
			},
		},
		{
			name: "creates the file from the template", args: []string{"theme", "light"},
			outHas: []string{"Created " + file + " from the template\n", "Theme set to light in " + file + "\n"}, outLacks: []string{"Restyled"},
			check: func(t *testing.T) {
				t.Helper()
				fileIs(t, withTheme("light"))
				if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("mode %v (%v)", info.Mode(), err)
				}
			},
		},
		{
			name: "same theme leaves the file untouched",
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.Chtimes(file, old, old); err != nil {
					t.Fatal(err)
				}
			},
			args: []string{"theme", "light"}, outHas: []string{"Theme is already light in " + file + "\n"}, outLacks: []string{"Created", "set to"},
			check: func(t *testing.T) {
				t.Helper()
				if info, err := os.Stat(file); err != nil || !info.ModTime().Equal(old) {
					t.Fatalf("file rewritten: %v %v", info.ModTime(), err)
				}
			},
		},
		{
			name:  "default theme on a missing file",
			setup: func(t *testing.T) { t.Helper(); mustRemove(t, file) },
			args:  []string{"theme", "monokai"}, outHas: []string{"Created " + file, "Theme set to monokai in " + file},
			check: func(t *testing.T) { t.Helper(); fileIs(t, config.Template()) },
		},
		{
			name: "keeps comments and layout",
			setup: func(t *testing.T) {
				t.Helper()
				e.writeConfig(t, "# mine\n[ui]\n# theme = \"lyna\"\nclock = false # keep\n\n[popup]\nwidth = \"80%\"\n")
			},
			args: []string{"theme", "ansi"}, outHas: []string{"Theme set to ansi in " + file},
			check: func(t *testing.T) {
				t.Helper()
				fileIs(t, []byte("# mine\n[ui]\n# theme = \"lyna\"\ntheme = \"ansi\"\nclock = false # keep\n\n[popup]\nwidth = \"80%\"\n"))
			},
		},
		{
			name:  "fixes an invalid theme value",
			setup: func(t *testing.T) { t.Helper(); e.writeConfig(t, "[ui]\ntheme = \"nope\"\nclock = false\n") },
			args:  []string{"theme", "light"}, outHas: []string{"Theme set to light"},
			check: func(t *testing.T) { t.Helper(); fileIs(t, []byte("[ui]\ntheme = \"light\"\nclock = false\n")) },
		},
		{
			name: "refuses when the result is not a valid configuration",
			setup: func(t *testing.T) {
				t.Helper()
				e.writeConfig(t, "[ui]\ntheme = \"lyna\"\nbogus = 1\n")
				before, _ = os.ReadFile(file)
			},
			args: []string{"theme", "light"}, wantCode: 1, errHas: []string{"bogus", "lmux config edit"},
			check: func(t *testing.T) { t.Helper(); fileIs(t, before) },
		},
		{
			name: "refuses a layout it cannot edit in place",
			setup: func(t *testing.T) {
				t.Helper()
				e.writeConfig(t, "ui = { icons = \"auto\" }\n")
				before, _ = os.ReadFile(file)
			},
			args: []string{"theme", "light"}, wantCode: 1, errHas: []string{"ui.theme", "cannot edit in place", "lmux config edit"},
			check: func(t *testing.T) { t.Helper(); fileIs(t, before) },
		},
		{
			name: "refuses invalid TOML",
			setup: func(t *testing.T) {
				t.Helper()
				e.writeConfig(t, "[ui\n")
				before, _ = os.ReadFile(file)
			},
			args: []string{"theme", "light"}, wantCode: 1, errHas: []string{"line 1"},
			check: func(t *testing.T) { t.Helper(); fileIs(t, before) },
		},
		{
			name: "edits where a symbolic link points and keeps its mode",
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(dotfile), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dotfile, []byte("[ui]\ntheme = \"lyna\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				mustRemove(t, file)
				if err := os.Symlink(dotfile, file); err != nil {
					t.Fatal(err)
				}
			},
			args: []string{"theme", "ansi"}, outHas: []string{"Theme set to ansi in " + file}, outLacks: []string{"Created"},
			check: func(t *testing.T) {
				t.Helper()
				if info, err := os.Lstat(file); err != nil || info.Mode()&fs.ModeSymlink == 0 {
					t.Fatalf("link replaced: %v", err)
				}
				got, err := os.ReadFile(dotfile)
				if err != nil || string(got) != "[ui]\ntheme = \"ansi\"\n" {
					t.Fatalf("link target %q (%v)", got, err)
				}
				if info, _ := os.Stat(dotfile); info.Mode().Perm() != 0o644 {
					t.Fatalf("link target mode %v", info.Mode())
				}
			},
		},
		{
			name:  "refuses a dangling link",
			setup: func(t *testing.T) { t.Helper(); mustRemove(t, dotfile) },
			args:  []string{"theme", "light"}, wantCode: 1, errHas: []string{"resolve " + file},
		},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.setup != nil {
				st.setup(t)
			}
			code, stdout, stderr := e.run(t, "", st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%s", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
			if st.check != nil {
				st.check(t)
			}
		})
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// TestThemeApplyCLI changes the theme while a workspace runs and reads the
// status style back from the server.
func TestThemeApplyCLI(t *testing.T) {
	e := newCLIEnv(t)
	ctx := tmuxtest.Context(t)
	depth := theme.DetectDepth(e.host.Getenv)
	statusBg := func(t *testing.T, name string) string {
		t.Helper()
		p, err := theme.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		return "bg=" + p.Bg.Tmux(depth)
	}
	server := func(t *testing.T) *app.Server {
		t.Helper()
		s, err := app.OpenServer(ctx, e.host)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	statusStyle := func(t *testing.T) string {
		t.Helper()
		v, err := server(t).Client.ShowOption(ctx, "-g", "", "status-style")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	code, stdout, stderr := e.run(t, "theme", "ansi")
	if code != 0 || strings.Contains(stdout, "Restyled") {
		t.Fatalf("without a server: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := server(t).Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); err == nil {
		t.Fatal("setting the theme started a server")
	}

	e.start(t, "api", e.host.Home)
	if got, want := statusStyle(t), statusBg(t, "ansi"); !strings.Contains(got, want) {
		t.Fatalf("status-style %q before the change, want %q", got, want)
	}
	code, stdout, stderr = e.run(t, "theme", "light")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Restyled the running workspaces.") {
		t.Fatalf("stdout %q does not say the workspaces were restyled", stdout)
	}
	if got, want := statusStyle(t), statusBg(t, "light"); !strings.Contains(got, want) {
		t.Fatalf("status-style %q after the change, want %q", got, want)
	}
	s := server(t)
	if loaded, err := s.Client.ShowOption(ctx, "-g", "", tmux.OptConfHash); err != nil || loaded != s.Fingerprint {
		t.Fatalf("server records configuration %q (%v), want %q", loaded, err, s.Fingerprint)
	}
}

// treeSnapshot describes every path under root (type, mode, modification
// time and content digest) for before and after comparison.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			out[rel] = "link " + target
		case d.IsDir():
			out[rel] = fmt.Sprintf("dir %v %d", info.Mode(), info.ModTime().UnixNano())
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out[rel] = fmt.Sprintf("file %v %d %s", info.Mode(), info.ModTime().UnixNano(), hex.EncodeToString(sum[:]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// treeChanges lists the paths added, removed or changed between snapshots,
// ignoring the modification time of directories whose entries changed.
func treeChanges(before, after map[string]string) []string {
	var changed []string
	for p, a := range after {
		b, ok := before[p]
		if ok && strings.HasPrefix(a, "dir ") && strings.HasPrefix(b, "dir ") {
			a, b = strings.Fields(a)[1], strings.Fields(b)[1]
		}
		if !ok || a != b {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changed = append(changed, p)
		}
	}
	slices.Sort(changed)
	return changed
}

// TestThemeClaudeNothing pins the empty-report path: a run without a themes
// directory has printed nothing, so it must never end in success.
func TestThemeClaudeNothing(t *testing.T) {
	failed := fmt.Errorf("no Claude Code configuration directory at %s", "/nowhere")
	cases := []struct {
		name    string
		err     error
		wantErr error
		errHas  string
	}{
		{name: "the reason it stopped is kept", err: failed, wantErr: failed},
		{name: "a wrapped reason is kept", err: fmt.Errorf("themes: %w", fs.ErrPermission), errHas: "permission denied"},
		{name: "no reason still fails", errHas: "no Claude Code themes were written"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := themeClaudeNothing(tc.err)
			if err == nil {
				t.Fatal("themeClaudeNothing returned no error, so the command would exit 0 in silence")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v", err, tc.wantErr)
			}
			if tc.errHas != "" && !strings.Contains(err.Error(), tc.errHas) {
				t.Fatalf("error %v, want %q", err, tc.errHas)
			}
		})
	}
}

func TestThemeClaudeCLI(t *testing.T) {
	e := newGlueEnv(t)
	cfgDir := filepath.Join(e.root, "claude-config")
	defaultDir := filepath.Join(e.root, ".claude")
	oddDir := filepath.Join(e.root, "odd\x1b]0;x\x07dir")
	for path, content := range map[string]string{
		filepath.Join(cfgDir, "settings.json"):     `{"theme":"dark"}`,
		filepath.Join(defaultDir, "settings.json"): `{"model":"opus"}`,
		filepath.Join(e.root, ".tmux.conf"):        "set -g mouse on\n",
		filepath.Join(oddDir, "keep"):              "x",
		e.configFile():                             string(config.Template()),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	themeFiles := func(dir string) []string {
		rel, _ := filepath.Rel(e.root, dir)
		out := []string{filepath.Join(rel, "themes")}
		for _, name := range theme.Names() {
			out = append(out, filepath.Join(rel, "themes", claudetheme.FileName(name)))
		}
		slices.Sort(out)
		return out
	}
	// displayNames is the /theme list in documented order, without the
	// palettes named.
	displayNames := func(without ...string) string {
		var names []string
		for _, name := range app.ThemeNames() {
			if !slices.Contains(without, name) {
				names = append(names, claudetheme.DisplayName(name))
			}
		}
		return strings.Join(names, ", ")
	}
	lightFile := filepath.Join(cfgDir, "themes", "lyna-light.json")
	steps := []struct {
		name        string
		configDir   string // CLAUDE_CONFIG_DIR; empty unsets it
		setup       func(t *testing.T)
		args        []string
		wantCode    int
		outHas      []string
		outLacks    []string
		errHas      []string
		wantChanges []string
	}{
		{
			name: "writes one theme per palette", configDir: cfgDir, args: []string{"theme", "claude"},
			outHas: []string{
				"Claude Code themes in " + filepath.Join(cfgDir, "themes") + "\n",
				"  created    " + filepath.Join(cfgDir, "themes", "lyna-lyna.json") + "\n",
				"  created    " + lightFile + "\n",
				"  created    " + filepath.Join(cfgDir, "themes", "lyna-ansi.json") + "\n",
				"Pick one in Claude Code with /theme: " + displayNames() + ".\n",
				"Restart Claude Code sessions that are already running to see the new themes.\n",
			},
			wantChanges: themeFiles(cfgDir),
		},
		{
			name: "a second run changes nothing", configDir: cfgDir, args: []string{"theme", "claude"},
			outHas: []string{"  unchanged  " + lightFile + "\n", "Pick one in Claude Code with /theme"}, outLacks: []string{"created", "Restart"},
		},
		{
			name: "an edited theme file is kept", configDir: cfgDir,
			setup: func(t *testing.T) {
				t.Helper()
				if err := os.WriteFile(lightFile, []byte(`{"name":"Mine","base":"light","overrides":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			args: []string{"theme", "claude"}, wantCode: 1,
			outHas: []string{"  refused    " + lightFile + ": not written by lyna-tmux or edited since", "/theme: " + displayNames("light") + ".\n"},
			errHas: []string{fmt.Sprintf("1 of %d theme files were not written", len(theme.Names()))},
		},
		{
			name: "defaults to ~/.claude", args: []string{"theme", "claude"},
			outHas:      []string{"Claude Code themes in " + filepath.Join(defaultDir, "themes")},
			wantChanges: themeFiles(defaultDir),
		},
		{
			name: "missing configuration directory", configDir: filepath.Join(e.root, "missing"), args: []string{"theme", "claude"}, wantCode: 1,
			errHas: []string{"no Claude Code configuration directory at " + filepath.Join(e.root, "missing"), "start Claude Code once, then run", "theme claude again"},
		},
		{
			name: "paths are sanitized", configDir: oddDir, args: []string{"theme", "claude"},
			outHas: []string{"Claude Code themes in " + sanitize.Line(filepath.Join(oddDir, "themes")) + "\n"}, outLacks: []string{"\x1b", "\x07"},
			wantChanges: themeFiles(oddDir),
		},
		{name: "rejects arguments", configDir: cfgDir, args: []string{"theme", "claude", "extra"}, wantCode: 1, errHas: []string{`unknown command "extra"`}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			delete(e.env, "CLAUDE_CONFIG_DIR")
			if st.configDir != "" {
				e.env["CLAUDE_CONFIG_DIR"] = st.configDir
			}
			if st.setup != nil {
				st.setup(t)
			}
			before := treeSnapshot(t, e.root)
			code, stdout, stderr := e.run(t, "", st.args...)
			if code != st.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, st.wantCode, stdout, stderr)
			}
			if got := treeChanges(before, treeSnapshot(t, e.root)); !slices.Equal(got, st.wantChanges) {
				t.Fatalf("changed paths %q, want %q", got, st.wantChanges)
			}
			for _, s := range st.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%q", s, stdout)
				}
			}
			for _, s := range st.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%q", s, stdout)
				}
			}
			for _, s := range st.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
	for _, p := range theme.Names() {
		palette, _ := theme.Get(p)
		want, err := claudetheme.Generate(palette)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(defaultDir, "themes", claudetheme.FileName(p))); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s theme file differs from the generator (%v)", p, err)
		}
	}
}
