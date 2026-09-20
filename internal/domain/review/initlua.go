package review

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// Icons is the glyph set of the review explorer.
type Icons string

// Icon sets, named like the workspace icon settings.
const (
	IconsUnicode Icons = "unicode"
	IconsNerd    Icons = "nerd"
	IconsASCII   Icons = "ascii"
)

// ParseIcons parses unicode, nerd or ascii.
func ParseIcons(s string) (Icons, error) {
	switch Icons(s) {
	case IconsUnicode, IconsNerd, IconsASCII:
		return Icons(s), nil
	}
	return IconsUnicode, fmt.Errorf("review: unknown icon set %q (want unicode, nerd or ascii)", s)
}

// Colors are the plugin highlight colors as "#rrggbb"; an empty field keeps
// the plugin's own choice.
type Colors struct {
	LineInsert           string
	LineDelete           string
	CharInsert           string
	CharDelete           string
	ConflictSign         string
	ConflictSignResolved string
	ConflictSignAccepted string
	ConflictSignRejected string
}

// ErrBadColor reports a color that is not #RRGGBB.
var ErrBadColor = errors.New("review: color must be #RRGGBB")

// fields pairs each color with its plugin setup key, in output order.
func (c Colors) fields() [][2]string {
	return [][2]string{
		{"line_insert", c.LineInsert},
		{"line_delete", c.LineDelete},
		{"char_insert", c.CharInsert},
		{"char_delete", c.CharDelete},
		{"conflict_sign", c.ConflictSign},
		{"conflict_sign_resolved", c.ConflictSignResolved},
		{"conflict_sign_accepted", c.ConflictSignAccepted},
		{"conflict_sign_rejected", c.ConflictSignRejected},
	}
}

// Validate checks that every set color is #RRGGBB. Anything else would be
// read by the plugin as a highlight group name.
func (c Colors) Validate() error {
	for _, f := range c.fields() {
		if f[1] == "" {
			continue
		}
		if _, err := theme.ParseHex(f[1]); err != nil {
			return fmt.Errorf("%w: %s = %q", ErrBadColor, f[0], f[1])
		}
	}
	return nil
}

// Tint strengths in percent of the accent over the background: line
// highlights stay quiet enough to read code on, changed characters stand out.
const (
	lineTint = 22
	charTint = 45
)

// ColorsFromPalette derives the review colors from a workspace palette:
// additions tint the background with the success color, deletions with the
// danger color, and conflict signs reuse the semantic colors directly.
func ColorsFromPalette(p theme.Palette) Colors {
	return Colors{
		LineInsert:           mix(p.Bg, p.Success, lineTint).Hex(),
		LineDelete:           mix(p.Bg, p.Danger, lineTint).Hex(),
		CharInsert:           mix(p.Bg, p.Success, charTint).Hex(),
		CharDelete:           mix(p.Bg, p.Danger, charTint).Hex(),
		ConflictSign:         p.Warning.Hex(),
		ConflictSignResolved: p.Muted.Hex(),
		ConflictSignAccepted: p.Success.Hex(),
		ConflictSignRejected: p.Danger.Hex(),
	}
}

// mix blends pct percent of b into a, using the RGB approximation of
// palette-indexed colors.
func mix(a, b theme.Color, pct int) theme.Color {
	a, b = rgb(a), rgb(b)
	ch := func(x, y uint8) uint8 {
		return uint8(int(x) + (int(y)-int(x))*pct/100) //nolint:gosec // The blend of two bytes stays within 0..255.
	}
	return theme.RGB(ch(a.R, b.R), ch(a.G, b.G), ch(a.B, b.B))
}

func rgb(c theme.Color) theme.Color {
	if !c.Indexed {
		return c
	}
	parsed, _ := theme.ParseHex(c.Hex()) // Hex always renders #rrggbb
	return parsed
}

// InitOptions shape the generated init file.
type InitOptions struct {
	// PluginDir is the absolute directory of the installed plugin.
	PluginDir string
	Colors    Colors
	// Scheme colors the editor itself; the zero value keeps Neovim's colors.
	Scheme Scheme
	Icons  Icons
	// TrueColor turns on 24-bit colors; otherwise the plugin's 256-color
	// fallbacks are used.
	TrueColor bool
	// Light selects a light background.
	Light bool
	// Layout is the default diff layout; LayoutDefault keeps the plugin's.
	Layout Layout
}

// glyphs are the explorer and filler characters per icon set. The plugin's
// defaults need a patched font, so only the nerd set keeps them.
type glyphs struct {
	filler, folderClosed, folderOpen, ellipsis string
	indentMarkers                              bool
}

func glyphsFor(icons Icons) (glyphs, bool) {
	switch icons {
	case IconsNerd:
		return glyphs{}, false
	case IconsASCII:
		return glyphs{filler: "/", folderClosed: "+", folderOpen: "-", ellipsis: "..."}, true
	}
	return glyphs{filler: "╱", folderClosed: "▸", folderOpen: "▾", ellipsis: "…", indentMarkers: true}, true
}

// unsafeRuntimeDirRune reports characters a runtimepath entry cannot hold.
// The option is a comma-separated list, and Neovim finds plugin files by
// expanding each entry as a glob, partly through the shell: with a quote,
// '$', a backtick, braces, brackets, glob characters or a backslash in the
// directory (Neovim 0.12, verified) the plugin silently does not load, and a
// backtick would run a command. Control characters have no place in it either.
func unsafeRuntimeDirRune(r rune) bool {
	return strings.ContainsRune(",'$`{}[]\\*?", r) || r < 0x20 || r == 0x7f
}

// ValidatePluginDir checks that Neovim can load a plugin from dir.
func ValidatePluginDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("review: plugin directory %q is not absolute", dir)
	}
	if i := strings.IndexFunc(dir, unsafeRuntimeDirRune); i >= 0 {
		return fmt.Errorf("review: plugin directory %q contains %q, which Neovim cannot load plugins from; set XDG_DATA_HOME or LYNA_TMUX_HOME to a plain directory",
			dir, dir[i])
	}
	return nil
}

// RenderInit renders the init file of the isolated review editor.
func RenderInit(opts InitOptions) ([]byte, error) {
	if err := ValidatePluginDir(opts.PluginDir); err != nil {
		return nil, err
	}
	if err := opts.Colors.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Scheme.Validate(); err != nil {
		return nil, err
	}
	icons := opts.Icons
	if icons == "" {
		icons = IconsUnicode
	}
	if _, err := ParseIcons(string(icons)); err != nil {
		return nil, err
	}
	if _, err := ParseLayout(string(opts.Layout)); err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(initHeader)
	fmt.Fprintf(&b, "local plugin = %s\n", LuaString(opts.PluginDir))
	b.WriteString(initRuntime)
	b.WriteString("\nvim.o.modeline = false\nvim.o.exrc = false\nvim.o.swapfile = false\nvim.o.shadafile = \"NONE\"\n")
	fmt.Fprintf(&b, "vim.o.termguicolors = %t\n", opts.TrueColor)
	background := "dark"
	if opts.Light {
		background = "light"
	}
	fmt.Fprintf(&b, "vim.o.background = %s\n", LuaString(background))
	b.WriteString(opts.Scheme.Lua())

	b.WriteString("\nlocal ok, err = pcall(function()\n  require(\"codediff\").setup({\n")
	var hl []string
	for _, f := range opts.Colors.fields() {
		if f[1] != "" {
			hl = append(hl, fmt.Sprintf("      %s = %s,\n", f[0], LuaString(strings.ToLower(f[1]))))
		}
	}
	if len(hl) > 0 {
		b.WriteString("    highlights = {\n")
		b.WriteString(strings.Join(hl, ""))
		b.WriteString("    },\n")
	}
	g, custom := glyphsFor(icons)
	b.WriteString("    diff = {\n")
	if opts.Layout != LayoutDefault {
		fmt.Fprintf(&b, "      layout = %s,\n", LuaString(string(opts.Layout)))
	}
	if custom {
		fmt.Fprintf(&b, "      filler_text = %s,\n", LuaString(g.filler))
	}
	b.WriteString("    },\n    explorer = {\n      auto_refresh = true,\n")
	if custom {
		fmt.Fprintf(&b, "      indent_markers = %t,\n", g.indentMarkers)
		fmt.Fprintf(&b, "      ellipsis = %s,\n", LuaString(g.ellipsis))
		fmt.Fprintf(&b, "      icons = {\n        folder_closed = %s,\n        folder_open = %s,\n      },\n",
			LuaString(g.folderClosed), LuaString(g.folderOpen))
	}
	b.WriteString("    },\n  })\nend)\nif not ok then\n")
	fmt.Fprintf(&b, "  vim.g.%s = tostring(err)\nend\n", SetupErrorVar)
	b.WriteString(initChrome)
	return []byte(b.String()), nil
}

const initHeader = `-- Generated by lyna-tmux for "lmux review". It is rewritten before a
-- review starts, so local edits do not last.

`

// initRuntime keeps Neovim's own runtime and the pinned plugin only. --clean
// already drops the user's configuration and data directories; the system
// wide ones are dropped too, so plugins installed there cannot change the
// review either.
const initRuntime = `local system = {}
for _, kind in ipairs({ "config_dirs", "data_dirs" }) do
  for _, dir in ipairs(vim.fn.stdpath(kind)) do
    for _, suffix in ipairs({ "", "/after", "/site", "/site/after" }) do
      system[dir .. suffix] = true
    end
  end
end
local function without_system(entries)
  local kept = {}
  for _, entry in ipairs(entries) do
    if not system[entry] then
      kept[#kept + 1] = entry
    end
  end
  return kept
end
vim.opt.runtimepath = without_system(vim.opt.runtimepath:get())
vim.opt.packpath = without_system(vim.opt.packpath:get())
vim.opt.runtimepath:prepend(plugin)
`

// LabelFunc is the global Lua function of the generated init file that turns a
// buffer name into the text the status and tab lines show.
const LabelFunc = "lyna_tmux_review_label"

// TablineFunc is the global Lua function that renders the tab line.
const TablineFunc = "lyna_tmux_review_tabline"

// initChrome renders the lines Neovim draws around the review. Only the
// isolated editor gets them; the user's own Neovim keeps its own.
const initChrome = `
-- Neovim names a window and a tab after the buffer in it, and a diff buffer is
-- a codediff:///<repository>///<revision>/<file> URL: shortened to the width
-- there is, it becomes one character per path component and names neither the
-- file nor the revision it holds. Both lines are built from the URL instead.
-- A status line takes the text as it is, a tab line reads it as a format, so
-- only the tab line escapes it.
local function revision(rev)
  if rev:sub(1, 1) == ":" then
    return "index"
  end
  if #rev > 8 and rev:match("^%x+$") then
    return rev:sub(1, 8)
  end
  return rev
end

-- A review splits the window three ways, so a label often has fewer columns
-- than it needs. Neovim would cut it at the front, which turns README.md into
-- <ADME.md: the shorter forms below are tried in order instead. The room is
-- what the window being drawn has left once the marker and the ruler on the
-- other side of the line have taken theirs.
local ruler = 16

local function fit(forms, measure)
  if not measure then
    return forms[1]
  end
  -- Neovim draws a status line with its own window current, so this is the
  -- width of that window and not of the one the cursor is in.
  local width = vim.fn.winwidth(0)
  if width <= 0 then
    return forms[1]
  end
  local room = width - ruler
  for _, form in ipairs(forms) do
    if vim.fn.strdisplaywidth(form) <= room then
      return form
    end
  end
  return forms[#forms]
end

-- measure is set by the status line, which is drawn in one window and has its
-- width to respect. The tab line spans the whole editor and passes nothing.
function _G.` + LabelFunc + `(name, measure)
  if not name or name == "" then
    return "[no name]"
  end
  local rev, file = name:match("^codediff:///.-///([^/]+)/(.+)$")
  if file then
    return fit({ file .. "  @ " .. revision(rev), file, vim.fn.fnamemodify(file, ":t") }, measure)
  end
  local panel = name:match("^CodeDiff (%a+) %[%d+%]$")
  if panel then
    return panel
  end
  local short = vim.fn.fnamemodify(name, ":~:.")
  return fit({ short, vim.fn.fnamemodify(short, ":t") }, measure)
end

function _G.` + TablineFunc + `()
  local current = vim.api.nvim_get_current_tabpage()
  local out = {}
  for i, tab in ipairs(vim.api.nvim_list_tabpages()) do
    local label = "[no name]"
    local ok, win = pcall(vim.api.nvim_tabpage_get_win, tab)
    if ok then
      -- bufname() is the name as the plugin set it; nvim_buf_get_name() would
      -- turn the name of a panel into a path under the working directory.
      label = _G.` + LabelFunc + `(vim.fn.bufname(vim.api.nvim_win_get_buf(win)))
    end
    out[#out + 1] = (tab == current and "%#TabLineSel#" or "%#TabLine#")
      .. "%" .. i .. "T " .. (label:gsub("%%", "%%%%")) .. " "
  end
  return table.concat(out) .. "%#TabLineFill#%T"
end

vim.o.tabline = "%!v:lua.` + TablineFunc + `()"
-- %m would mark every diff buffer with the [-] of a read-only one, which all of
-- them are; only unsaved work in the file under review is worth a marker. The
-- space before it is its own item because Neovim drops the leading space of an
-- expression that follows another one.
vim.o.statusline = " %{v:lua.` + LabelFunc + `(bufname(), 1)} %{&modified ? '[+]' : ''}%=%l:%c  %P "

-- codediff.nvim opens its review in a new tab, which leaves the empty buffer
-- Neovim starts with in a tab of its own. Closing it gives the review the whole
-- window, and a lone tab draws no tab line at all.
vim.api.nvim_create_autocmd("TabNewEntered", {
  once = true,
  callback = function()
    vim.schedule(function()
      local tabs = vim.api.nvim_list_tabpages()
      if #tabs < 2 or tabs[1] == vim.api.nvim_get_current_tabpage() then
        return
      end
      local wins = vim.api.nvim_tabpage_list_wins(tabs[1])
      if #wins ~= 1 then
        return
      end
      local buf = vim.api.nvim_win_get_buf(wins[1])
      local lines = vim.api.nvim_buf_get_lines(buf, 0, 2, false)
      if vim.api.nvim_buf_get_name(buf) ~= "" or vim.bo[buf].modified then
        return
      end
      if #lines > 1 or (lines[1] or "") ~= "" then
        return
      end
      pcall(vim.api.nvim_command, "tabclose 1")
    end)
  end,
})
`
