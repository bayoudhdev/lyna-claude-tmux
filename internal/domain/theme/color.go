// Package theme defines the workspace palettes, reduces them to the color
// depth a terminal supports, and selects the icon set for status lines,
// borders and pickers.
package theme

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Color is either a 24-bit RGB value or an index into the terminal's own
// 16-color palette. Palette-indexed colors follow the user's terminal scheme,
// which is what the ansi theme is for.
type Color struct {
	R, G, B uint8
	// ANSI is the palette index (0-15) when Indexed is true.
	ANSI    uint8
	Indexed bool
}

// RGB builds a 24-bit color.
func RGB(r, g, b uint8) Color { return Color{R: r, G: g, B: b} }

// ANSI builds a terminal palette color. Indices above 15 are clamped to 15.
func ANSI(index uint8) Color { return Color{ANSI: min(index, 15), Indexed: true} }

// ErrBadHex reports a string that is not #RRGGBB.
var ErrBadHex = errors.New("theme: color must be #RRGGBB")

// ParseHex parses "#RRGGBB" (case-insensitive).
func ParseHex(s string) (Color, error) {
	if len(s) != 7 || s[0] != '#' {
		return Color{}, fmt.Errorf("%w (got %q)", ErrBadHex, s)
	}
	b, err := hex.DecodeString(s[1:])
	if err != nil {
		return Color{}, fmt.Errorf("%w (got %q)", ErrBadHex, s)
	}
	return RGB(b[0], b[1], b[2]), nil
}

// Hex renders "#rrggbb". An indexed color renders the RGB of the standard
// xterm palette entry, which is the best static approximation.
func (c Color) Hex() string {
	if c.Indexed {
		c = ansiRGB[c.ANSI]
	}
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// Depth is the number of colors a terminal can show.
type Depth int

const (
	// Depth16 is the basic 16-color palette.
	Depth16 Depth = iota
	// Depth256 is the xterm 256-color palette.
	Depth256
	// DepthTrue is 24-bit color.
	DepthTrue
)

// String renders the configuration spelling: 16, 256 or truecolor.
func (d Depth) String() string {
	switch d {
	case DepthTrue:
		return "truecolor"
	case Depth256:
		return "256"
	}
	return "16"
}

// ParseDepth parses 16, 256 or truecolor.
func ParseDepth(s string) (Depth, error) {
	switch s {
	case "truecolor":
		return DepthTrue, nil
	case "256":
		return Depth256, nil
	case "16":
		return Depth16, nil
	}
	return Depth16, fmt.Errorf("theme: unknown color depth %q (want 16, 256 or truecolor)", s)
}

// DetectDepth resolves the color depth from the terminal environment:
// COLORTERM=truecolor or 24bit means 24-bit, a TERM containing 256color means
// 256 colors, anything else the basic palette.
func DetectDepth(getenv func(string) string) Depth {
	switch getenv("COLORTERM") {
	case "truecolor", "24bit":
		return DepthTrue
	}
	term := getenv("TERM")
	for i := 0; i+8 <= len(term); i++ {
		if term[i:i+8] == "256color" {
			return Depth256
		}
	}
	if term == "xterm-direct" || term == "tmux-direct" {
		return DepthTrue
	}
	return Depth16
}

// Tmux renders the color for tmux styles at the given depth: "#rrggbb",
// "colorN" for the 256-color palette, or "colorN" (N < 16) for palette colors.
// tmux accepts this spelling as well as its British one.
func (c Color) Tmux(d Depth) string {
	if c.Indexed {
		return "color" + strconv.Itoa(int(c.ANSI))
	}
	switch d {
	case DepthTrue:
		return c.Hex()
	case Depth256:
		return "color" + strconv.Itoa(To256(c))
	}
	return "color" + strconv.Itoa(To16(c))
}

// cubeLevels are the channel values of the xterm 6x6x6 color cube.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// To256 returns the nearest xterm-256 index for an RGB color, choosing
// between the 6x6x6 cube (16-231) and the grayscale ramp (232-255) by
// squared distance. Indexed colors map to themselves.
func To256(c Color) int {
	if c.Indexed {
		return int(c.ANSI)
	}
	r, g, b := int(c.R), int(c.G), int(c.B)
	ri, gi, bi := nearestLevel(r), nearestLevel(g), nearestLevel(b)
	cube := 16 + 36*ri + 6*gi + bi
	cubeDist := dist(r, g, b, cubeLevels[ri], cubeLevels[gi], cubeLevels[bi])

	// Grayscale ramp: 232 + i has level 8 + 10*i.
	avg := (r + g + b) / 3
	gi2 := 0
	if avg > 238 {
		gi2 = 23
	} else if avg > 8 {
		gi2 = min((avg-8+5)/10, 23)
	}
	level := 8 + 10*gi2
	if grayDist := dist(r, g, b, level, level, level); grayDist < cubeDist {
		return 232 + gi2
	}
	return cube
}

func nearestLevel(v int) int {
	best, bestDist := 0, 1<<30
	for i, l := range cubeLevels {
		if d := (v - l) * (v - l); d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

func dist(r1, g1, b1, r2, g2, b2 int) int {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return dr*dr + dg*dg + db*db
}

// ansiRGB is the xterm default palette for indices 0-15.
var ansiRGB = [16]Color{
	RGB(0, 0, 0), RGB(205, 0, 0), RGB(0, 205, 0), RGB(205, 205, 0),
	RGB(0, 0, 238), RGB(205, 0, 205), RGB(0, 205, 205), RGB(229, 229, 229),
	RGB(127, 127, 127), RGB(255, 0, 0), RGB(0, 255, 0), RGB(255, 255, 0),
	RGB(92, 92, 255), RGB(255, 0, 255), RGB(0, 255, 255), RGB(255, 255, 255),
}

// To16 returns the nearest index of the xterm default 16-color palette.
func To16(c Color) int {
	if c.Indexed {
		return int(c.ANSI)
	}
	best, bestDist := 0, 1<<30
	for i, p := range ansiRGB {
		if d := dist(int(c.R), int(c.G), int(c.B), int(p.R), int(p.G), int(p.B)); d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}
