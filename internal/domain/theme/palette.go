package theme

import (
	"fmt"
	"slices"
)

// Palette is the set of semantic colors every surface draws with. Generators
// never use raw colors, so a theme change restyles the status line, borders,
// menus, pickers and the review editor together.
type Palette struct {
	Name string
	// Dark reports whether the palette is designed for a dark background.
	Dark bool

	Bg      Color // status line and popup background
	Surface Color // raised areas: active tab, menu background
	Overlay Color // selection and highlighted rows
	Border  Color // inactive pane borders
	Muted   Color // secondary text, inactive tabs
	Text    Color // primary text
	Accent  Color // brand, active border, focused elements
	Accent2 Color // secondary accent: branch, links

	Busy    Color // agent working
	Waiting Color // agent needs the user
	Idle    Color // agent finished
	Danger  Color // destructive actions, sandbox off
	Success Color // additions, sandbox strict
	Warning Color // attention without error
}

var palettes = map[string]Palette{
	"lyna": {
		Name:    "lyna",
		Dark:    true,
		Bg:      RGB(0x0d, 0x11, 0x17),
		Surface: RGB(0x16, 0x1b, 0x22),
		Overlay: RGB(0x26, 0x2d, 0x38),
		Border:  RGB(0x30, 0x36, 0x3d),
		Muted:   RGB(0x7d, 0x85, 0x90),
		Text:    RGB(0xe6, 0xed, 0xf3),
		Accent:  RGB(0x39, 0xd3, 0x53),
		Accent2: RGB(0x58, 0xa6, 0xff),
		Busy:    RGB(0xf0, 0xb7, 0x2f),
		Waiting: RGB(0xff, 0x5f, 0x56),
		Idle:    RGB(0x39, 0xd3, 0x53),
		Danger:  RGB(0xff, 0x5f, 0x56),
		Success: RGB(0x39, 0xd3, 0x53),
		Warning: RGB(0xf0, 0xb7, 0x2f),
	},
	"light": {
		Name:    "light",
		Dark:    false,
		Bg:      RGB(0xf6, 0xf8, 0xfa),
		Surface: RGB(0xea, 0xee, 0xf2),
		Overlay: RGB(0xd0, 0xd7, 0xde),
		Border:  RGB(0xd0, 0xd7, 0xde),
		Muted:   RGB(0x57, 0x60, 0x6a),
		Text:    RGB(0x1f, 0x23, 0x28),
		Accent:  RGB(0x1a, 0x7f, 0x37),
		Accent2: RGB(0x09, 0x69, 0xda),
		Busy:    RGB(0x9a, 0x67, 0x00),
		Waiting: RGB(0xcf, 0x22, 0x2e),
		Idle:    RGB(0x1a, 0x7f, 0x37),
		Danger:  RGB(0xcf, 0x22, 0x2e),
		Success: RGB(0x1a, 0x7f, 0x37),
		Warning: RGB(0x9a, 0x67, 0x00),
	},
	// ansi uses the terminal's own palette so the workspace follows whatever
	// color scheme the terminal is configured with.
	"ansi": {
		Name:    "ansi",
		Dark:    true,
		Bg:      ANSI(0),
		Surface: ANSI(8),
		Overlay: ANSI(8),
		Border:  ANSI(8),
		Muted:   ANSI(7),
		Text:    ANSI(15),
		Accent:  ANSI(2),
		Accent2: ANSI(4),
		Busy:    ANSI(3),
		Waiting: ANSI(1),
		Idle:    ANSI(2),
		Danger:  ANSI(1),
		Success: ANSI(2),
		Warning: ANSI(3),
	},
}

// Names lists the built-in palettes in a stable order.
func Names() []string {
	names := make([]string, 0, len(palettes))
	for n := range palettes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Get returns a built-in palette by name.
func Get(name string) (Palette, error) {
	p, ok := palettes[name]
	if !ok {
		return Palette{}, fmt.Errorf("theme: unknown theme %q (available: %v)", name, Names())
	}
	return p, nil
}
