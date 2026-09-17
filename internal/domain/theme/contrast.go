package theme

import "math"

// Luminance is the relative luminance of a color as WCAG 2 defines it: each
// sRGB channel is linearized, then weighted by how strongly the eye responds
// to it. Black is 0, white is 1. An indexed color is measured on the standard
// xterm palette entry, the same approximation Hex uses.
func Luminance(c Color) float64 {
	if c.Indexed {
		c = ansiRGB[c.ANSI]
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

// linear undoes the sRGB transfer curve for one channel.
func linear(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// Contrast is the WCAG 2 contrast ratio between two colors, from 1 (equal)
// to 21 (black on white). The order of the arguments does not matter.
// Body text needs 4.5 and large or secondary text 3 to stay legible.
func Contrast(a, b Color) float64 {
	la, lb := Luminance(a), Luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
