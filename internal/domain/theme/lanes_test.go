package theme

import (
	"math"
	"testing"
)

func TestLanes(t *testing.T) {
	cases := []struct {
		name string
		// indexed says the lanes are terminal colors rather than colors of
		// the palette's own, and grey that they are steps of lightness.
		indexed bool
		grey    bool
	}{
		{name: "lyna"},
		{name: "light"},
		{name: "slate"},
		{name: "dusk"},
		{name: "contrast"},
		{name: "solar-dark"},
		{name: "solar-light"},
		{name: "earth-dark"},
		{name: "earth-light"},
		{name: "nord"},
		{name: "rose"},
		{name: "mono", grey: true},
		{name: "ansi", indexed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Get(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			lanes := p.Lanes()
			seen := map[Color]int{}
			for i, c := range lanes {
				seen[c]++
				if c.Indexed != tc.indexed {
					t.Fatalf("lane %d of %s is %+v, indexed = %v, want %v", i, tc.name, c, c.Indexed, tc.indexed)
				}
				if tc.indexed {
					continue
				}
				if got := Contrast(c, p.Bg); got < laneContrast {
					t.Fatalf("lane %d of %s is %+v, %.2f against the background, want %.1f or more", i, tc.name, c, got, laneContrast)
				}
				_, s, _ := toHSL(c)
				if grey := s < greyAccent; grey != tc.grey {
					t.Fatalf("lane %d of %s has saturation %.2f, grey = %v, want %v", i, tc.name, s, grey, tc.grey)
				}
			}
			for c, n := range seen {
				if n > 1 {
					t.Fatalf("%s draws %d of its lanes with %+v, want eight colors of its own", tc.name, n, c)
				}
			}
		})
	}
}

// TestLanesAreStable holds the colors of the default palette: the lanes of a
// graph are read together, so a change to how they are derived is a change to
// every frame that draws one.
func TestLanesAreStable(t *testing.T) {
	p, err := Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	want := []Color{
		RGB(0x39, 0xd3, 0x53), RGB(0x39, 0xd3, 0xc7), RGB(0x39, 0x6c, 0xd3), RGB(0x7a, 0x39, 0xd3),
		RGB(0xd3, 0x39, 0xb9), RGB(0xd3, 0x39, 0x46), RGB(0xd3, 0xa0, 0x39), RGB(0x92, 0xd3, 0x39),
	}
	got := p.Lanes()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lane %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLanesTurnRoundTheCircle proves the lanes are eight hues rather than
// eight shades of one: neighbors sit a step of the circle apart.
func TestLanesTurnRoundTheCircle(t *testing.T) {
	p, err := Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	lanes := p.Lanes()
	const step = 360.0 / GraphLanes
	for i := 1; i < len(lanes); i++ {
		before, _, _ := toHSL(lanes[i-1])
		now, _, _ := toHSL(lanes[i])
		d := math.Mod(now-before+360, 360)
		if math.Abs(d-step) > 2 {
			t.Fatalf("lane %d is %.1f degrees from the one before it, want %.1f", i, d, step)
		}
	}
}

func TestHSLRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		color Color
	}{
		{name: "black", color: RGB(0, 0, 0)},
		{name: "white", color: RGB(0xff, 0xff, 0xff)},
		{name: "a grey", color: RGB(0x7d, 0x85, 0x90)},
		{name: "the green of the default palette", color: RGB(0x39, 0xd3, 0x53)},
		{name: "a blue", color: RGB(0x58, 0xa6, 0xff)},
		{name: "a red", color: RGB(0xff, 0x5f, 0x56)},
		{name: "a saturated yellow", color: RGB(0xff, 0xff, 0x00)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s, l := toHSL(tc.color)
			got := fromHSL(h, s, l)
			if diff(got.R, tc.color.R) > 1 || diff(got.G, tc.color.G) > 1 || diff(got.B, tc.color.B) > 1 {
				t.Fatalf("fromHSL(toHSL(%+v)) = %+v", tc.color, got)
			}
		})
	}
}

func TestFromHSLHoldsItsRange(t *testing.T) {
	cases := []struct {
		name    string
		h, s, l float64
		want    Color
	}{
		{name: "a hue past the circle", h: 480, s: 1, l: 0.5, want: fromHSL(120, 1, 0.5)},
		{name: "a hue before it", h: -120, s: 1, l: 0.5, want: fromHSL(240, 1, 0.5)},
		{name: "a saturation past one", h: 0, s: 4, l: 0.5, want: RGB(0xff, 0, 0)},
		{name: "a lightness under zero", h: 0, s: 1, l: -1, want: RGB(0, 0, 0)},
		{name: "a lightness past one", h: 0, s: 1, l: 2, want: RGB(0xff, 0xff, 0xff)},
		{name: "no saturation at all", h: 200, s: 0, l: 0.5, want: RGB(0x80, 0x80, 0x80)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fromHSL(tc.h, tc.s, tc.l); got != tc.want {
				t.Fatalf("fromHSL(%v, %v, %v) = %+v, want %+v", tc.h, tc.s, tc.l, got, tc.want)
			}
		})
	}
}

func diff(a, b uint8) int {
	if a > b {
		return int(a) - int(b)
	}
	return int(b) - int(a)
}

func FuzzHSL(f *testing.F) {
	f.Add(uint8(0x39), uint8(0xd3), uint8(0x53))
	f.Add(uint8(0), uint8(0), uint8(0))
	f.Add(uint8(0xff), uint8(0xff), uint8(0xff))
	f.Fuzz(func(t *testing.T, r, g, b uint8) {
		c := RGB(r, g, b)
		h, s, l := toHSL(c)
		if h < 0 || h >= 360 || s < 0 || s > 1 || l < 0 || l > 1 {
			t.Fatalf("toHSL(%+v) = %v %v %v, outside its own range", c, h, s, l)
		}
		back := fromHSL(h, s, l)
		if diff(back.R, r) > 1 || diff(back.G, g) > 1 || diff(back.B, b) > 1 {
			t.Fatalf("fromHSL(toHSL(%+v)) = %+v", c, back)
		}
		// Whatever the color, a lane built from it is a color, never a
		// channel out of range or a hue out of the circle.
		lane := readable(c, RGB(0, 0, 0), true)
		if _, _, ll := toHSL(lane); ll < 0 || ll > 1 {
			t.Fatalf("readable(%+v) = %+v, outside the range of a color", c, lane)
		}
	})
}
