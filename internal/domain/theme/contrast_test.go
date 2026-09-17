package theme

import (
	"math"
	"testing"
)

func TestLuminance(t *testing.T) {
	cases := []struct {
		name string
		in   Color
		want float64
	}{
		{name: "black", in: RGB(0, 0, 0), want: 0},
		{name: "white", in: RGB(255, 255, 255), want: 1},
		{name: "pure red", in: RGB(255, 0, 0), want: 0.2126},
		{name: "pure green", in: RGB(0, 255, 0), want: 0.7152},
		{name: "pure blue", in: RGB(0, 0, 255), want: 0.0722},
		{name: "mid gray", in: RGB(0x80, 0x80, 0x80), want: 0.2159},
		{name: "below the linear knee", in: RGB(10, 10, 10), want: 0.003035},
		{name: "indexed uses the xterm entry", in: ANSI(15), want: 1},
		{name: "indexed black", in: ANSI(0), want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Luminance(tc.in); math.Abs(got-tc.want) > 0.0005 {
				t.Fatalf("Luminance(%+v) = %.4f, want %.4f", tc.in, got, tc.want)
			}
		})
	}
}

func TestContrast(t *testing.T) {
	cases := []struct {
		name string
		a, b Color
		want float64
	}{
		{name: "black on white", a: RGB(0, 0, 0), b: RGB(255, 255, 255), want: 21},
		{name: "white on black is the same ratio", a: RGB(255, 255, 255), b: RGB(0, 0, 0), want: 21},
		{name: "same color", a: RGB(0x39, 0xd3, 0x53), b: RGB(0x39, 0xd3, 0x53), want: 1},
		// The reference greys of the WCAG threshold: #767676 is the darkest
		// grey that passes 4.5:1 on white, #949494 passes 3:1.
		{name: "aa text grey on white", a: RGB(0x76, 0x76, 0x76), b: RGB(255, 255, 255), want: 4.54},
		{name: "aa large grey on white", a: RGB(0x94, 0x94, 0x94), b: RGB(255, 255, 255), want: 3.03},
		{name: "pure red on white", a: RGB(255, 0, 0), b: RGB(255, 255, 255), want: 4.0},
		{name: "pure blue on white", a: RGB(0, 0, 255), b: RGB(255, 255, 255), want: 8.59},
		{name: "pure blue on black", a: RGB(0, 0, 255), b: RGB(0, 0, 0), want: 2.44},
		{name: "lyna text on its background", a: RGB(0xe6, 0xed, 0xf3), b: RGB(0x0d, 0x11, 0x17), want: 16.02},
		{name: "indexed white on indexed black", a: ANSI(15), b: ANSI(0), want: 21},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Contrast(tc.a, tc.b); math.Abs(got-tc.want) > 0.01 {
				t.Fatalf("Contrast(%+v, %+v) = %.2f, want %.2f", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
