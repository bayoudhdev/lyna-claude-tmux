package review

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// TestSchemeFromPalette holds the generated colorscheme to the palette it
// comes from, in every theme: the chrome takes the palette's own colors, each
// syntax role is told apart from the others, and a capture group carries the
// same color as the syntax group that means the same thing.
func TestSchemeFromPalette(t *testing.T) {
	for _, name := range theme.Names() {
		t.Run(name, func(t *testing.T) {
			p := palette(t, name)
			s := SchemeFromPalette(p)
			if err := s.Validate(); err != nil {
				t.Fatalf("generated scheme is invalid: %v", err)
			}
			by := map[string]Group{}
			for _, g := range s.Groups {
				by[g.Name] = g
			}
			cases := []struct {
				name  string
				group string
				fg    string
				bg    string
			}{
				{name: "the editor background", group: "Normal", fg: p.Text.Hex(), bg: p.Bg.Hex()},
				{name: "the line numbers", group: "LineNr", fg: p.Border.Hex()},
				{name: "the selected tab", group: "TabLineSel", fg: p.Bg.Hex(), bg: p.Accent.Hex()},
				{name: "a keyword", group: "Keyword", fg: p.Waiting.Hex()},
				{name: "the keyword capture", group: "@keyword", fg: p.Waiting.Hex()},
				{name: "a function", group: "Function", fg: p.Accent.Hex()},
				{name: "the function capture", group: "@function", fg: p.Accent.Hex()},
				{name: "a string", group: "String", fg: p.Warning.Hex()},
				{name: "a type", group: "Type", fg: p.Accent2.Hex()},
				{name: "an error", group: "Error", fg: p.Danger.Hex()},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					g, ok := by[tc.group]
					if !ok {
						t.Fatalf("no %s group", tc.group)
					}
					if !strings.EqualFold(g.Fg, tc.fg) || !strings.EqualFold(g.Bg, tc.bg) {
						t.Fatalf("%s = fg %q bg %q, want fg %q bg %q", tc.group, g.Fg, g.Bg, tc.fg, tc.bg)
					}
				})
			}
			// A comment must be readable and still quieter than the code
			// around it, and the roles must not collapse onto one color.
			roles := map[string]string{
				"@comment": by["@comment"].Fg, "@keyword": by["@keyword"].Fg, "@function": by["@function"].Fg,
				"@string": by["@string"].Fg, "@type": by["@type"].Fg, "@constant": by["@constant"].Fg,
			}
			seen := map[string]string{}
			for role, color := range roles {
				if color == "" {
					t.Errorf("%s has no color", role)
					continue
				}
				if other, dup := seen[strings.ToLower(color)]; dup && name != "mono" && name != "ansi" {
					t.Errorf("%s and %s are both %s", role, other, color)
				}
				seen[strings.ToLower(color)] = role
			}
			if by["@comment"].Fg == by["Normal"].Fg {
				t.Error("a comment is drawn in the text color")
			}
			if !by["@comment"].Italic || !by["@type"].Italic {
				t.Error("comments and types are not set apart by their shape")
			}
		})
	}
}

func TestSchemeValidate(t *testing.T) {
	cases := []struct {
		name    string
		scheme  Scheme
		wantErr string
	}{
		{name: "empty"},
		{name: "colors and attributes", scheme: Scheme{Groups: []Group{{Name: "Normal", Fg: "#ffffff", Bg: "#000000"}, {Name: "@keyword.return", Bold: true}}}},
		{name: "no name", scheme: Scheme{Groups: []Group{{Fg: "#ffffff"}}}, wantErr: "is not a name Neovim accepts"},
		{name: "a space in the name", scheme: Scheme{Groups: []Group{{Name: "My Group"}}}, wantErr: "is not a name Neovim accepts"},
		{name: "a quote in the name", scheme: Scheme{Groups: []Group{{Name: `N"`}}}, wantErr: "is not a name Neovim accepts"},
		{name: "set twice", scheme: Scheme{Groups: []Group{{Name: "Normal"}, {Name: "Normal"}}}, wantErr: "is set twice"},
		{name: "a color that is a group name", scheme: Scheme{Groups: []Group{{Name: "Normal", Fg: "Comment"}}}, wantErr: `Normal = "Comment"`},
		{name: "a short color", scheme: Scheme{Groups: []Group{{Name: "Normal", Bg: "#fff"}}}, wantErr: `Normal = "#fff"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scheme.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestSchemeLua(t *testing.T) {
	cases := []struct {
		name     string
		scheme   Scheme
		want     []string
		wantNone []string
	}{
		{
			name: "nothing to set", scheme: Scheme{},
			wantNone: []string{"nvim_set_hl", "treesitter"},
		},
		{
			name: "colors, attributes and the parser",
			scheme: Scheme{Groups: []Group{
				{Name: "Normal", Fg: "#E6E6E6", Bg: "#1E1E1E"},
				{Name: "@type", Fg: "#62d8f1", Italic: true},
				{Name: "Cursor", Reverse: true},
			}},
			want: []string{
				`{ "Normal", { fg = "#e6e6e6", bg = "#1e1e1e" } },`,
				`{ "@type", { fg = "#62d8f1", italic = true } },`,
				`{ "Cursor", { reverse = true } },`,
				"pcall(vim.api.nvim_set_hl, 0, group[1], group[2])",
				"pcall(vim.treesitter.start, args.buf)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.scheme.Lua()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the rendered scheme lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tc.wantNone {
				if strings.Contains(got, w) {
					t.Errorf("the rendered scheme has %q:\n%s", w, got)
				}
			}
		})
	}
}

// FuzzSchemeValidate checks that no group name or color gets past Validate
// and into the generated file in a shape that could end the Lua string it is
// written in.
func FuzzSchemeValidate(f *testing.F) {
	f.Add("Normal", "#1e1e1e")
	f.Add("@keyword.return", "")
	f.Add(`N"ame`, "#zzzzzz")
	f.Fuzz(func(t *testing.T, name, color string) {
		s := Scheme{Groups: []Group{{Name: name, Fg: color}}}
		if err := s.Validate(); err != nil {
			return
		}
		lua := s.Lua()
		for _, bad := range []string{"\n", `"`, "\\", "\x00"} {
			if strings.Contains(name, bad) {
				t.Fatalf("group name %q passed validation", name)
			}
		}
		if color != "" && !strings.Contains(lua, strings.ToLower(color)) {
			t.Fatalf("color %q did not reach the file:\n%s", color, lua)
		}
	})
}
