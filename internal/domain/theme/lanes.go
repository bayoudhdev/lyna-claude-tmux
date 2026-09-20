package theme

import "math"

// GraphLanes is how many colors a history graph draws its lines of descent
// with before it starts again at the first: past eight lanes the shape of the
// history says more than the color does.
const GraphLanes = 8

// laneContrast is the ratio a lane keeps against the background it is drawn
// on, the WCAG 2 bound for a graphical object.
const laneContrast = 3.0

// indexedLanes are the lanes of a palette made of terminal colors rather than
// of its own: the six hues of the basic set, then two bright ones, so eight
// lanes are eight colors on a terminal that has nothing else.
var indexedLanes = [GraphLanes]uint8{2, 4, 3, 5, 6, 1, 14, 13}

// Lanes are the colors a graph draws its lanes with, in the order it takes
// them. They are derived from the palette rather than written down: the hue
// of the accent turned round the circle in eight steps, kept at the accent's
// saturation and lifted or lowered until every lane stands out against the
// background of the palette. A palette of terminal colors takes the eight
// hues the terminal has.
func (p Palette) Lanes() [GraphLanes]Color {
	var lanes [GraphLanes]Color
	if p.Accent.Indexed {
		for i, idx := range indexedLanes {
			lanes[i] = ANSI(idx)
		}
		return lanes
	}
	h, s, l := toHSL(p.Accent)
	// A palette with no color of its own keeps none: its lanes are steps of
	// lightness rather than hues, so a grey theme stays grey.
	if s < greyAccent {
		return greyLanes(p)
	}
	// A washed-out accent would make eight washed-out lanes, and one at the
	// end of its range would make eight of the same; the lanes of a graph are
	// lines a few cells wide, so they keep a color and a lightness of their
	// own to move from.
	s = math.Max(s, 0.45)
	l = math.Min(math.Max(l, 0.42), 0.72)
	for i := range lanes {
		hue := math.Mod(h+float64(i)*(360.0/GraphLanes), 360)
		lanes[i] = readable(fromHSL(hue, s, l), p.Bg, p.Dark)
	}
	return lanes
}

// greyAccent is the saturation under which a palette is read as having no
// color of its own.
const greyAccent = 0.12

// greyLanes are the lanes of such a palette: eight steps of lightness, away
// from the background, every one of them as far from its neighbors as the
// range allows.
func greyLanes(p Palette) [GraphLanes]Color {
	var lanes [GraphLanes]Color
	from, to := 0.45, 0.98
	if !p.Dark {
		from, to = 0.62, 0.12
	}
	for i := range lanes {
		l := from + (to-from)*float64(i)/float64(GraphLanes-1)
		lanes[i] = readable(fromHSL(0, 0, l), p.Bg, p.Dark)
	}
	return lanes
}

// readable lifts a color away from the background until it stands out on it,
// up the lightness on a dark background and down on a light one. A hue that
// cannot reach the bound (a yellow on white) stops at the end of its range
// rather than turning into another color.
func readable(c, bg Color, dark bool) Color {
	const step = 0.04
	h, s, l := toHSL(c)
	for range 20 {
		if Contrast(fromHSL(h, s, l), bg) >= laneContrast {
			break
		}
		if dark {
			if l >= 0.95 {
				break
			}
			l = math.Min(l+step, 0.95)
			continue
		}
		if l <= 0.15 {
			break
		}
		l = math.Max(l-step, 0.15)
	}
	return fromHSL(h, s, l)
}

// toHSL reads a color as a hue in degrees, a saturation and a lightness, both
// from 0 to 1.
func toHSL(c Color) (h, s, l float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	high := math.Max(r, math.Max(g, b))
	low := math.Min(r, math.Min(g, b))
	l = (high + low) / 2
	if high == low {
		return 0, 0, l
	}
	d := high - low
	if l > 0.5 {
		s = d / (2 - high - low)
	} else {
		s = d / (high + low)
	}
	switch high {
	case r:
		h = math.Mod((g-b)/d+6, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return math.Mod(h*60, 360), s, l
}

// fromHSL builds a color from a hue in degrees and a saturation and a
// lightness from 0 to 1.
func fromHSL(h, s, l float64) Color {
	h = math.Mod(math.Mod(h, 360)+360, 360)
	s = math.Min(math.Max(s, 0), 1)
	l = math.Min(math.Max(l, 0), 1)
	if s == 0 {
		v := round(l)
		return Color{R: v, G: v, B: v}
	}
	q := l + s - l*s
	if l < 0.5 {
		q = l * (1 + s)
	}
	p := 2*l - q
	return Color{
		R: round(hueToChannel(p, q, h+120)),
		G: round(hueToChannel(p, q, h)),
		B: round(hueToChannel(p, q, h-120)),
	}
}

// hueToChannel is one channel of a color built from a hue.
func hueToChannel(p, q, h float64) float64 {
	h = math.Mod(math.Mod(h, 360)+360, 360) / 360
	switch {
	case h < 1.0/6:
		return p + (q-p)*6*h
	case h < 1.0/2:
		return q
	case h < 2.0/3:
		return p + (q-p)*(2.0/3-h)*6
	}
	return p
}

// round turns a channel from 0 to 1 into the byte it is drawn with.
func round(v float64) uint8 {
	return uint8(math.Min(math.Max(math.Round(v*255), 0), 255))
}
