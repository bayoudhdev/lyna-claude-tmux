package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// themeSection and themeKey locate ui.theme, the setting the theme commands
// read and write.
const (
	themeSection = "ui"
	themeKey     = "theme"
)

// ErrThemeUnknown reports a theme name that is not a built-in palette.
var ErrThemeUnknown = errors.New("unknown theme")

// ThemeListing is what `lmux theme` shows.
type ThemeListing struct {
	// Palettes are the built-in palettes in the order the configuration
	// documents them.
	Palettes []theme.Palette
	// Current is the configured palette (ui.theme).
	Current string
	// Depth is the color depth swatches are drawn at: ui.color, or the depth
	// detected from the terminal when it is auto.
	Depth      theme.Depth
	ConfigPath string
}

// ThemeList loads the configuration and returns the palettes with the
// configured one.
func ThemeList(h Host) (ThemeListing, error) {
	paths, cfg, depth, err := themeSettings(h)
	if err != nil {
		return ThemeListing{}, err
	}
	palettes, err := themePalettes(ThemeNames())
	if err != nil {
		return ThemeListing{}, err
	}
	return ThemeListing{Palettes: palettes, Current: cfg.UI.Theme, Depth: depth, ConfigPath: paths.ConfigFile()}, nil
}

// ThemeMockup is what `lmux theme preview` shows: the workspace drawn
// in one or every theme.
type ThemeMockup struct {
	// Themes are the previewed palettes in documented order, each with the
	// rows of its mock.
	Themes []ThemeMock
	// Current is the configured palette (ui.theme).
	Current string
	// Depth is the color depth to draw at: ui.color, or the depth detected
	// from the terminal when it is auto.
	Depth theme.Depth
	// Icons is the resolved icon set (ui.icons), so the mock uses the glyphs
	// the workspace would.
	Icons theme.Icons
}

// ThemeMock is the workspace drawn in one palette.
type ThemeMock struct {
	Palette theme.Palette
	Lines   []theme.Line
}

// ThemePreview loads the configuration and mocks the workspace in the named
// theme, or in every theme when name is empty. The icon set and color depth
// come from the configuration, as they would for a workspace, so the preview
// shows what switching would give.
func ThemePreview(h Host, name string) (ThemeMockup, error) {
	if name != "" {
		if err := themeKnown(name); err != nil {
			return ThemeMockup{}, err
		}
	}
	_, cfg, depth, err := themeSettings(h)
	if err != nil {
		return ThemeMockup{}, err
	}
	icons, err := theme.GetIcons(theme.ResolveIcons(cfg.UI.Icons, h.Getenv))
	if err != nil {
		return ThemeMockup{}, err
	}
	names := ThemeNames()
	if name != "" {
		names = []string{name}
	}
	palettes, err := themePalettes(names)
	if err != nil {
		return ThemeMockup{}, err
	}
	mockup := ThemeMockup{Current: cfg.UI.Theme, Depth: depth, Icons: icons, Themes: make([]ThemeMock, 0, len(palettes))}
	for _, p := range palettes {
		mockup.Themes = append(mockup.Themes, ThemeMock{Palette: p, Lines: theme.Preview(p, icons)})
	}
	return mockup, nil
}

// ThemeNames returns the theme names ui.theme accepts, in documented order.
func ThemeNames() []string { return config.Choices(themeSection + "." + themeKey) }

// themeKnown rejects a name that is not a configurable theme, naming the
// ones that are.
func themeKnown(name string) error {
	names := ThemeNames()
	if !slices.Contains(names, name) {
		return fmt.Errorf("%w %q: choose %s", ErrThemeUnknown, name, strings.Join(names, ", "))
	}
	return nil
}

// themeSettings loads the configuration and resolves the color depth the
// theme commands draw at.
func themeSettings(h Host) (xdg.Paths, config.Config, theme.Depth, error) {
	paths, cfg, err := LoadConfig(h)
	if err != nil {
		return xdg.Paths{}, config.Config{}, 0, err
	}
	depth := theme.DetectDepth(h.Getenv)
	if cfg.UI.Color != "auto" {
		if depth, err = theme.ParseDepth(cfg.UI.Color); err != nil {
			return xdg.Paths{}, config.Config{}, 0, err
		}
	}
	return paths, cfg, depth, nil
}

func themePalettes(names []string) ([]theme.Palette, error) {
	out := make([]theme.Palette, 0, len(names))
	for _, name := range names {
		p, err := theme.Get(name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ThemeChange is the outcome of ThemeSet.
type ThemeChange struct {
	// Path is the configuration file.
	Path string
	// Created reports that the file did not exist and was first written from
	// the documented template.
	Created bool
	// Changed is false when ui.theme already named the theme; the file is then
	// left untouched.
	Changed bool
}

// ThemeSet sets ui.theme in the configuration file. Only the value changes
// (or one line is added), so the user's comments and layout survive. A missing
// file is created from the template first. A file that is a symbolic link, as
// in a dotfiles repository, is edited where it points and stays a link, with
// its permissions kept. Nothing is written when the name is unknown or when
// the edited file would not be a valid configuration.
func ThemeSet(h Host, name string) (ThemeChange, error) {
	if err := themeKnown(name); err != nil {
		return ThemeChange{}, err
	}
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return ThemeChange{}, err
	}
	change := ThemeChange{Path: paths.ConfigFile()}
	if _, err := os.Lstat(change.Path); errors.Is(err, fs.ErrNotExist) {
		_, err := InitConfig(h, false)
		switch {
		case err == nil:
			change.Created = true
		case !errors.Is(err, ErrConfigExists):
			return change, err
		}
	} else if err != nil {
		return change, fmt.Errorf("stat %s: %w", change.Path, err)
	}

	target, err := filepath.EvalSymlinks(change.Path)
	if err != nil {
		return change, fmt.Errorf("resolve %s: %w", change.Path, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return change, err
	}
	if !info.Mode().IsRegular() {
		return change, fmt.Errorf("%s is not a regular file", change.Path)
	}
	data, err := fsx.ReadFileLimited(target, config.MaxFileSize)
	if err != nil {
		return change, fmt.Errorf("read %s: %w", change.Path, err)
	}
	out, err := config.SetString(data, themeSection, themeKey, name)
	if err != nil {
		return change, fmt.Errorf("%s: %w; set %s.%s with: lmux config edit", change.Path, err, themeSection, themeKey)
	}
	if _, err := config.Decode(out); err != nil {
		return change, fmt.Errorf("%s: %w; fix the file with: lmux config edit", change.Path, err)
	}
	if bytes.Equal(out, data) {
		return change, nil
	}
	if err := fsx.WriteFileAtomic(target, out, info.Mode().Perm()); err != nil {
		return change, err
	}
	change.Changed = true
	return change, nil
}

// ThemeApply makes a running lyna-tmux server use the current configuration,
// so a theme change restyles open workspaces at once. It reports whether a
// server is running. Without a usable tmux no lyna-tmux server can be running,
// which is not an error here.
func ThemeApply(ctx context.Context, h Host) (bool, error) {
	s, err := OpenServer(ctx, h)
	if errors.Is(err, tmux.ErrNotInstalled) || errors.Is(err, ErrTmuxTooOld) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.Sync(ctx)
}

// ThemeClaude writes a Claude Code theme for every palette into the Claude
// Code configuration directory (CLAUDE_CONFIG_DIR or ~/.claude). Only
// <dir>/themes/lyna-*.json files are created or replaced, and a theme file the
// user wrote or edited is never overwritten (see claudetheme.Write).
func ThemeClaude(h Host) (claudetheme.Report, error) {
	palettes, err := themePalettes(ThemeNames())
	if err != nil {
		return claudetheme.Report{}, err
	}
	dir := xdg.ClaudeHome(h.Getenv, h.Home)
	rep, err := claudetheme.Write(dir, palettes...)
	if rep.Dir == "" && errors.Is(err, fs.ErrNotExist) {
		return rep, fmt.Errorf("no Claude Code configuration directory at %s: start Claude Code once, then run lmux theme claude again", dir)
	}
	return rep, err
}
