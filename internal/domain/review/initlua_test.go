package review

import (
	"errors"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

const goldenPluginDir = "/home/dev/.local/share/lyna-tmux/review/codediff.nvim"

func palette(t *testing.T, name string) theme.Palette {
	t.Helper()
	p, err := theme.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRenderInitGolden(t *testing.T) {
	cases := []struct {
		name   string
		golden string
		opts   InitOptions
	}{
		{
			name:   "lyna colors unicode truecolor",
			golden: "init/lyna-unicode-truecolor.lua",
			opts: InitOptions{
				PluginDir: goldenPluginDir, Colors: ColorsFromPalette(palette(t, "lyna")),
				Scheme: SchemeFromPalette(palette(t, "lyna")), Icons: IconsUnicode, TrueColor: true,
			},
		},
		{
			name:   "default colors ascii 256",
			golden: "init/default-ascii-256.lua",
			opts:   InitOptions{PluginDir: goldenPluginDir, Icons: IconsASCII},
		},
		{
			name:   "light nerd inline",
			golden: "init/light-nerd-inline.lua",
			opts: InitOptions{
				PluginDir: goldenPluginDir, Colors: ColorsFromPalette(palette(t, "light")),
				Scheme: SchemeFromPalette(palette(t, "light")), Icons: IconsNerd, TrueColor: true, Light: true, Layout: LayoutInline,
			},
		},
		{
			name:   "ansi side by side with quoted directory",
			golden: "init/ansi-side-by-side-quoted-dir.lua",
			opts:   InitOptions{PluginDir: `/Users/dev/My "Data" (é)/lyna-tmux/review/codediff.nvim`, Colors: ColorsFromPalette(palette(t, "ansi")), Layout: LayoutSideBySide},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderInit(tc.opts)
			if err != nil {
				t.Fatalf("RenderInit() error = %v", err)
			}
			golden.Assert(t, tc.golden, got)
		})
	}
}

func TestRenderInitErrors(t *testing.T) {
	cases := []struct {
		name    string
		opts    InitOptions
		wantErr string
	}{
		{name: "relative plugin dir", opts: InitOptions{PluginDir: "review/codediff.nvim"}, wantErr: "not absolute"},
		{name: "comma in plugin dir", opts: InitOptions{PluginDir: "/data,old/review/codediff.nvim"}, wantErr: `contains ','`},
		{name: "single quote in plugin dir", opts: InitOptions{PluginDir: "/Users/o'neil/review/codediff.nvim"}, wantErr: `contains '\''`},
		{name: "dollar in plugin dir", opts: InitOptions{PluginDir: "/d/$x/codediff.nvim"}, wantErr: `contains '$'`},
		{name: "backtick in plugin dir", opts: InitOptions{PluginDir: "/d/`id`/codediff.nvim"}, wantErr: "contains '`'"},
		{name: "brace in plugin dir", opts: InitOptions{PluginDir: "/d/{a}/codediff.nvim"}, wantErr: `contains '{'`},
		{name: "bracket in plugin dir", opts: InitOptions{PluginDir: "/d/[a]/codediff.nvim"}, wantErr: `contains '['`},
		{name: "backslash in plugin dir", opts: InitOptions{PluginDir: `/d/a\b/codediff.nvim`}, wantErr: `contains '\\'`},
		{name: "glob in plugin dir", opts: InitOptions{PluginDir: "/d/a*/codediff.nvim"}, wantErr: `contains '*'`},
		{name: "newline in plugin dir", opts: InitOptions{PluginDir: "/d/a\n/codediff.nvim"}, wantErr: `contains '\n'`},
		{name: "bad color", opts: InitOptions{PluginDir: "/p", Colors: Colors{CharDelete: "red"}}, wantErr: `char_delete = "red"`},
		{name: "bad highlight color", opts: InitOptions{PluginDir: "/p", Scheme: Scheme{Groups: []Group{{Name: "Normal", Fg: "green"}}}}, wantErr: `Normal = "green"`},
		{name: "bad highlight group", opts: InitOptions{PluginDir: "/p", Scheme: Scheme{Groups: []Group{{Name: "Normal Float", Fg: "#ffffff"}}}}, wantErr: "is not a name Neovim accepts"},
		{name: "bad icons", opts: InitOptions{PluginDir: "/p", Icons: "emoji"}, wantErr: "unknown icon set"},
		{name: "bad layout", opts: InitOptions{PluginDir: "/p", Layout: "stacked"}, wantErr: "unknown layout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderInit(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RenderInit() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRenderInitDefaultsToUnicode(t *testing.T) {
	implicit, err := RenderInit(InitOptions{PluginDir: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := RenderInit(InitOptions{PluginDir: "/p", Icons: IconsUnicode})
	if err != nil {
		t.Fatal(err)
	}
	if string(implicit) != string(explicit) {
		t.Fatalf("empty icon set renders differently from unicode:\n%s", golden.Diff(explicit, implicit))
	}
}

func TestParseIcons(t *testing.T) {
	cases := []struct {
		in      string
		want    Icons
		wantErr bool
	}{
		{in: "unicode", want: IconsUnicode},
		{in: "nerd", want: IconsNerd},
		{in: "ascii", want: IconsASCII},
		{in: "auto", want: IconsUnicode, wantErr: true},
		{in: "", want: IconsUnicode, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseIcons(tc.in)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("ParseIcons(%q) = %q, %v", tc.in, got, err)
			}
		})
	}
}

func TestColorsValidate(t *testing.T) {
	cases := []struct {
		name    string
		colors  Colors
		wantErr bool
	}{
		{name: "empty", colors: Colors{}},
		{name: "lower and upper hex", colors: Colors{LineInsert: "#1d3042", LineDelete: "#ABCDEF"}},
		{name: "short hex", colors: Colors{CharInsert: "#abc"}, wantErr: true},
		{name: "group name", colors: Colors{LineInsert: "DiffAdd"}, wantErr: true},
		{name: "lua injection", colors: Colors{ConflictSign: `#000000", evil = "`}, wantErr: true},
		{name: "last field checked", colors: Colors{ConflictSignRejected: "#12345g"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.colors.Validate()
			if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, ErrBadColor)) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestColorsFromPalette(t *testing.T) {
	cases := []struct {
		name  string
		theme string
		want  Colors
	}{
		{
			name:  "lyna",
			theme: "lyna",
			want: Colors{
				// 22% and 45% of #39d353 / #ff5f56 over #0d1117, truncated per channel.
				LineInsert: "#163b24", LineDelete: "#422224", CharInsert: "#206832", CharDelete: "#793433",
				ConflictSign: "#f0b72f", ConflictSignResolved: "#7d8590", ConflictSignAccepted: "#39d353", ConflictSignRejected: "#ff5f56",
			},
		},
		{
			name:  "ansi uses the xterm approximation",
			theme: "ansi",
			want: Colors{
				LineInsert: "#002d00", LineDelete: "#2d0000", CharInsert: "#005c00", CharDelete: "#5c0000",
				ConflictSign: "#cdcd00", ConflictSignResolved: "#e5e5e5", ConflictSignAccepted: "#00cd00", ConflictSignRejected: "#cd0000",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ColorsFromPalette(palette(t, tc.theme))
			if got != tc.want {
				t.Fatalf("ColorsFromPalette(%s) = %+v, want %+v", tc.theme, got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, name := range theme.Names() {
		t.Run("valid "+name, func(t *testing.T) {
			if err := ColorsFromPalette(palette(t, name)).Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
