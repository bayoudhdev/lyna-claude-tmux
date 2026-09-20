package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// Scheme colors the review editor from a workspace palette: the chrome
// Neovim draws, the diff groups, and the capture groups nvim-treesitter sets
// on a parsed buffer. The zero value renders nothing, so an editor without a
// scheme keeps Neovim's own colors.
type Scheme struct {
	// Groups are highlight groups in the order they are set, each with its
	// attributes. The order is fixed so the generated file is stable.
	Groups []Group
}

// Group is one highlight group.
type Group struct {
	Name string
	// Fg and Bg are "#rrggbb"; empty leaves the attribute unset, which is how
	// a group takes the background it is drawn on.
	Fg, Bg string
	Bold   bool
	Italic bool
	// Reverse swaps the two colors, which is how Neovim draws a cursor.
	Reverse bool
}

// ErrBadScheme reports a highlight color that is not #RRGGBB.
var ErrBadScheme = fmt.Errorf("review: highlight color must be #RRGGBB")

// Validate checks every color of the scheme.
func (s Scheme) Validate() error {
	seen := map[string]bool{}
	for _, g := range s.Groups {
		if g.Name == "" || strings.IndexFunc(g.Name, badGroupRune) >= 0 {
			return fmt.Errorf("review: highlight group %q is not a name Neovim accepts", g.Name)
		}
		if seen[g.Name] {
			return fmt.Errorf("review: highlight group %q is set twice", g.Name)
		}
		seen[g.Name] = true
		for _, c := range []string{g.Fg, g.Bg} {
			if c == "" {
				continue
			}
			if _, err := theme.ParseHex(c); err != nil {
				return fmt.Errorf("%w: %s = %q", ErrBadScheme, g.Name, c)
			}
		}
	}
	return nil
}

// badGroupRune reports characters a highlight group name cannot hold. Neovim
// accepts letters, digits, underscores, dots and the at sign of a treesitter
// capture; anything else would be a quoting problem waiting to happen.
func badGroupRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '_' || r == '.' || r == '@':
		return false
	}
	return true
}

// Syntax tints: a comment sits between the muted color and the background,
// a selection and the current line are raised from it.
const (
	commentTint = 20
	lineTintBg  = 40
)

// SchemeFromPalette derives the editor colors from a workspace palette. The
// palette carries no syntax colors, so each role is taken from the slot that
// already means the same thing elsewhere: what is being run in the accent,
// what is being read in the second accent, what needs attention in the
// warning color, what cannot be undone in the danger one. Every group is set
// on its own rather than linked, so a buffer nvim-treesitter parses and one
// only the syntax rules reach are colored alike.
func SchemeFromPalette(p theme.Palette) Scheme {
	bg, fg := p.Bg.Hex(), p.Text.Hex()
	muted, border := p.Muted.Hex(), p.Border.Hex()
	surface, overlay := p.Surface.Hex(), p.Overlay.Hex()
	accent, accent2 := p.Accent.Hex(), p.Accent2.Hex()
	keyword, warning := p.Waiting.Hex(), p.Warning.Hex()
	parameter, danger := p.Busy.Hex(), p.Danger.Hex()
	// The palette has no sixth hue, so constants take the blend of the two it
	// has that a colorscheme of this family spends on them.
	constant := mix(p.Accent2, p.Waiting, 50).Hex()
	comment := mix(p.Muted, p.Bg, commentTint).Hex()
	selection := mix(p.Surface, p.Accent2, lineTintBg).Hex()
	cursorLine := mix(p.Bg, p.Surface, lineTintBg).Hex()

	chrome := []Group{
		{Name: "Normal", Fg: fg, Bg: bg},
		{Name: "NormalNC", Fg: fg, Bg: bg},
		{Name: "NormalFloat", Fg: fg, Bg: surface},
		{Name: "FloatBorder", Fg: accent, Bg: surface},
		{Name: "WinSeparator", Fg: border, Bg: bg},
		{Name: "CursorLine", Bg: cursorLine},
		{Name: "CursorLineNr", Fg: accent, Bold: true},
		{Name: "LineNr", Fg: border},
		{Name: "SignColumn", Bg: bg},
		{Name: "Visual", Bg: selection},
		{Name: "Search", Fg: bg, Bg: warning},
		{Name: "IncSearch", Fg: bg, Bg: parameter},
		{Name: "MatchParen", Fg: parameter, Bold: true},
		{Name: "Cursor", Reverse: true},
		{Name: "Pmenu", Fg: fg, Bg: surface},
		{Name: "PmenuSel", Fg: bg, Bg: accent},
		{Name: "StatusLine", Fg: fg, Bg: surface},
		{Name: "StatusLineNC", Fg: muted, Bg: overlay},
		{Name: "TabLine", Fg: muted, Bg: overlay},
		{Name: "TabLineSel", Fg: bg, Bg: accent, Bold: true},
		{Name: "TabLineFill", Bg: bg},
		{Name: "Title", Fg: accent, Bold: true},
		{Name: "Directory", Fg: accent2},
		{Name: "ErrorMsg", Fg: danger, Bold: true},
		{Name: "WarningMsg", Fg: warning},
		{Name: "NonText", Fg: border},
		{Name: "Whitespace", Fg: border},
		{Name: "Folded", Fg: muted, Bg: surface},
	}

	diff := []Group{
		{Name: "DiffAdd", Bg: mix(p.Bg, p.Success, lineTint).Hex()},
		{Name: "DiffDelete", Fg: danger, Bg: mix(p.Bg, p.Danger, lineTint).Hex()},
		{Name: "DiffChange", Bg: mix(p.Bg, p.Accent2, lineTint).Hex()},
		{Name: "DiffText", Bg: mix(p.Bg, p.Accent2, charTint).Hex()},
	}

	// Each syntax role, with the base group the syntax rules use and the
	// treesitter captures that mean the same thing.
	roles := []struct {
		fg     string
		bold   bool
		italic bool
		groups []string
	}{
		{fg: comment, italic: true, groups: []string{"Comment", "@comment"}},
		{fg: keyword, groups: []string{"Statement", "Keyword", "Conditional", "Repeat", "Exception", "Operator", "@keyword", "@keyword.function", "@keyword.operator", "@keyword.return", "@conditional", "@repeat", "@operator", "@exception"}},
		{fg: accent, groups: []string{"Function", "Identifier", "@function", "@function.call", "@function.method", "@method", "@constructor"}},
		{fg: warning, groups: []string{"String", "Character", "@string", "@string.escape", "@character"}},
		{fg: accent2, italic: true, groups: []string{"Type", "StorageClass", "Structure", "@type", "@type.builtin", "@type.definition", "@namespace", "@module"}},
		{fg: constant, groups: []string{"Constant", "Number", "Boolean", "Float", "PreProc", "Include", "Define", "Macro", "@constant", "@constant.builtin", "@number", "@boolean", "@float", "@include", "@preproc"}},
		{fg: parameter, italic: true, groups: []string{"@parameter", "@variable.parameter", "@field", "@property", "@attribute", "@tag.attribute"}},
		{fg: fg, groups: []string{"@variable", "@variable.builtin", "@text", "@none"}},
		{fg: muted, groups: []string{"Delimiter", "@punctuation.bracket", "@punctuation.delimiter", "@punctuation.special"}},
		{fg: danger, bold: true, groups: []string{"Error", "@error", "@text.danger"}},
		{fg: warning, bold: true, groups: []string{"Todo", "@comment.todo", "@text.todo"}},
	}
	syntax := make([]Group, 0, 64)
	for _, r := range roles {
		for _, name := range r.groups {
			syntax = append(syntax, Group{Name: name, Fg: r.fg, Bold: r.bold, Italic: r.italic})
		}
	}
	sort.SliceStable(syntax, func(i, j int) bool { return syntax[i].Name < syntax[j].Name })

	groups := make([]Group, 0, len(chrome)+len(diff)+len(syntax))
	groups = append(groups, chrome...)
	groups = append(groups, diff...)
	return Scheme{Groups: append(groups, syntax...)}
}

// Lua renders the scheme as the highlight table of the generated init file,
// empty when there is nothing to set.
func (s Scheme) Lua() string {
	if len(s.Groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n-- The workspace palette, on the groups the syntax rules use and on the\n")
	b.WriteString("-- captures nvim-treesitter sets, so a parsed buffer and an unparsed one\n")
	b.WriteString("-- are colored alike.\nfor _, group in ipairs({\n")
	for _, g := range s.Groups {
		var attrs []string
		if g.Fg != "" {
			attrs = append(attrs, "fg = "+LuaString(strings.ToLower(g.Fg)))
		}
		if g.Bg != "" {
			attrs = append(attrs, "bg = "+LuaString(strings.ToLower(g.Bg)))
		}
		if g.Bold {
			attrs = append(attrs, "bold = true")
		}
		if g.Italic {
			attrs = append(attrs, "italic = true")
		}
		if g.Reverse {
			attrs = append(attrs, "reverse = true")
		}
		fmt.Fprintf(&b, "  { %s, { %s } },\n", LuaString(g.Name), strings.Join(attrs, ", "))
	}
	b.WriteString("}) do\n  pcall(vim.api.nvim_set_hl, 0, group[1], group[2])\nend\n")
	b.WriteString(initTreesitter)
	return b.String()
}

// initTreesitter turns the parser on where Neovim has one for the language of
// the buffer, and says nothing where it has not: the review must open on a
// machine with no parser installed, and the syntax rules color it there.
const initTreesitter = `vim.api.nvim_create_autocmd("FileType", {
  callback = function(args)
    pcall(vim.treesitter.start, args.buf)
  end,
})
`
