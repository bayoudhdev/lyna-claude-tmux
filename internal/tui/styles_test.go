package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestThemeColorDepth(t *testing.T) {
	t.Parallel()
	rgb := theme.RGB(0x7a, 0xe5, 0x82)
	cases := []struct {
		name  string
		depth theme.Depth
		color theme.Color
		want  string
	}{
		{name: "truecolor keeps the exact color", depth: theme.DepthTrue, color: rgb, want: "38;2;122;229;130"},
		{name: "256 quantizes to the cube", depth: theme.Depth256, color: rgb, want: "38;5;" + strconv.Itoa(theme.To256(rgb))},
		{name: "16 quantizes to the basic palette", depth: theme.Depth16, color: rgb, want: basicSGR(theme.To16(rgb))},
		{name: "palette index ignores truecolor", depth: theme.DepthTrue, color: theme.ANSI(2), want: "32"},
		{name: "bright palette index", depth: theme.Depth256, color: theme.ANSI(9), want: "91"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			th := Theme{Depth: tc.depth}
			out := lipgloss.NewStyle().Foreground(th.Color(tc.color)).Render("x")
			if !strings.Contains(out, "\x1b["+tc.want+"m") {
				t.Errorf("render = %q, want SGR %q", out, tc.want)
			}
		})
	}
}

func basicSGR(i int) string {
	if i < 8 {
		return strconv.Itoa(30 + i)
	}
	return strconv.Itoa(90 + i - 8)
}

func TestThemeFromConfig(t *testing.T) {
	t.Parallel()
	utf8Truecolor := env(map[string]string{"COLORTERM": "truecolor", "LANG": "en_US.UTF-8"})
	cases := []struct {
		name    string
		mutate  func(*config.UI)
		getenv  func(string) string
		depth   theme.Depth
		icons   string
		palette string
		wantErr string
	}{
		{name: "auto resolves from the environment", getenv: utf8Truecolor, depth: theme.DepthTrue, icons: "unicode", palette: "lyna"},
		{
			name: "auto on a plain terminal", getenv: env(map[string]string{"TERM": "xterm", "LANG": "C"}),
			depth: theme.Depth16, icons: "ascii", palette: "lyna",
		},
		{
			name: "explicit values win", getenv: utf8Truecolor, depth: theme.Depth256, icons: "nerd", palette: "light",
			mutate: func(ui *config.UI) { ui.Theme, ui.Color, ui.Icons = "light", "256", "nerd" },
		},
		{name: "empty color means auto", getenv: utf8Truecolor, depth: theme.DepthTrue, icons: "unicode", palette: "lyna", mutate: func(ui *config.UI) { ui.Color = "" }},
		{name: "unknown theme", getenv: utf8Truecolor, mutate: func(ui *config.UI) { ui.Theme = "neon" }, wantErr: "unknown theme"},
		{name: "unknown depth", getenv: utf8Truecolor, mutate: func(ui *config.UI) { ui.Color = "65k" }, wantErr: "unknown color depth"},
		{name: "unknown icons", getenv: utf8Truecolor, mutate: func(ui *config.UI) { ui.Icons = "emoji" }, wantErr: "unknown icon set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ui := config.Default().UI
			if tc.mutate != nil {
				tc.mutate(&ui)
			}
			th, err := ThemeFromConfig(ui, tc.getenv)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if th.Depth != tc.depth || th.Icons.Name != tc.icons || th.Palette.Name != tc.palette {
				t.Errorf("theme = depth %v icons %s palette %s, want %v %s %s", th.Depth, th.Icons.Name, th.Palette.Name, tc.depth, tc.icons, tc.palette)
			}
		})
	}
}

func TestStylesIconsAndEllipsis(t *testing.T) {
	t.Parallel()
	cases := []struct {
		icons    string
		ellipsis string
		status   map[agent.Status]string
	}{
		{icons: "unicode", ellipsis: "…", status: map[agent.Status]string{
			agent.StatusWaiting: "◆", agent.StatusIdle: "○", agent.StatusBusy: "●", agent.StatusUnknown: "·", "bogus": "·",
		}},
		{icons: "ascii", ellipsis: "~", status: map[agent.Status]string{
			agent.StatusWaiting: "!", agent.StatusIdle: "o", agent.StatusBusy: "*", agent.StatusUnknown: "?",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.icons, func(t *testing.T) {
			t.Parallel()
			s := testStyles(t, "lyna", theme.DepthTrue, tc.icons)
			if s.Ellipsis != tc.ellipsis || ansi.StringWidth(s.Ellipsis) != 1 {
				t.Errorf("ellipsis = %q, want one-cell %q", s.Ellipsis, tc.ellipsis)
			}
			for st, icon := range tc.status {
				if _, got := s.Status(st); got != icon {
					t.Errorf("Status(%q) icon = %q, want %q", st, got, icon)
				}
			}
		})
	}
}

func TestSwatch(t *testing.T) {
	t.Parallel()
	for _, depth := range []theme.Depth{theme.DepthTrue, theme.Depth256, theme.Depth16} {
		t.Run(depth.String(), func(t *testing.T) {
			t.Parallel()
			s := testStyles(t, "lyna", depth, "unicode")
			for _, name := range theme.Names() {
				p, err := theme.Get(name)
				if err != nil {
					t.Fatal(err)
				}
				sw := s.Swatch(p)
				if w := ansi.StringWidth(sw); w != 20 {
					t.Errorf("%s swatch is %d cells, want 20", name, w)
				}
				if strings.TrimSpace(ansi.Strip(sw)) != "" {
					t.Errorf("%s swatch draws glyphs: %q", name, ansi.Strip(sw))
				}
				if !strings.Contains(sw, "\x1b[") {
					t.Errorf("%s swatch has no color", name)
				}
			}
		})
	}
}
