package theme

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseHex(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Color
		wantErr bool
	}{
		{name: "lower", in: "#39d353", want: RGB(0x39, 0xd3, 0x53)},
		{name: "upper", in: "#FFAA00", want: RGB(0xff, 0xaa, 0x00)},
		{name: "black", in: "#000000", want: RGB(0, 0, 0)},
		{name: "missing hash", in: "39d353", wantErr: true},
		{name: "short", in: "#fff", wantErr: true},
		{name: "long", in: "#39d3531", wantErr: true},
		{name: "not hex", in: "#39d35g", wantErr: true},
		{name: "sign", in: "#+39d35", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHex(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrBadHex) {
					t.Fatalf("ParseHex(%q) err = %v, want ErrBadHex", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ParseHex(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			}
			if got.Hex() != strings.ToLower(tc.in) {
				t.Fatalf("Hex() = %q, want %q", got.Hex(), strings.ToLower(tc.in))
			}
		})
	}
}

func FuzzParseHex(f *testing.F) {
	for _, s := range []string{"#000000", "#ffffff", "#39D353", "#fff", "", "#zzzzzz", "#-00000"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseHex(s)
		if err != nil {
			return
		}
		if c.Indexed {
			t.Fatalf("ParseHex(%q) returned an indexed color", s)
		}
		if got := c.Hex(); got != strings.ToLower(s) {
			t.Fatalf("round trip %q -> %q", s, got)
		}
	})
}

func TestTo256(t *testing.T) {
	cases := []struct {
		name string
		in   Color
		want int
	}{
		{name: "black is cube origin", in: RGB(0, 0, 0), want: 16},
		{name: "white is cube corner", in: RGB(255, 255, 255), want: 231},
		{name: "pure red", in: RGB(255, 0, 0), want: 196},
		{name: "cube exact", in: RGB(95, 135, 175), want: 16 + 36*1 + 6*2 + 3},
		{name: "mid gray uses ramp", in: RGB(128, 128, 128), want: 244},
		{name: "near black gray ramp", in: RGB(8, 8, 8), want: 232},
		{name: "light gray ramp top", in: RGB(238, 238, 238), want: 255},
		{name: "indexed passes through", in: ANSI(3), want: 3},
		{name: "lyna accent", in: RGB(0x39, 0xd3, 0x53), want: 77},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := To256(tc.in); got != tc.want {
				t.Fatalf("To256(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestTo16(t *testing.T) {
	cases := []struct {
		name string
		in   Color
		want int
	}{
		{name: "black", in: RGB(0, 0, 0), want: 0},
		{name: "bright white", in: RGB(255, 255, 255), want: 15},
		{name: "dark red", in: RGB(200, 10, 10), want: 1},
		{name: "bright green", in: RGB(20, 250, 20), want: 10},
		{name: "gray", in: RGB(125, 125, 125), want: 8},
		{name: "indexed passes through", in: ANSI(12), want: 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := To16(tc.in); got != tc.want {
				t.Fatalf("To16(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestColorTmux(t *testing.T) {
	c := RGB(0x39, 0xd3, 0x53)
	cases := []struct {
		name  string
		color Color
		depth Depth
		want  string
	}{
		{name: "truecolor hex", color: c, depth: DepthTrue, want: "#39d353"},
		{name: "256 cube", color: c, depth: Depth256, want: "color77"},
		{name: "16 palette", color: c, depth: Depth16, want: "color2"},
		{name: "indexed ignores depth", color: ANSI(4), depth: DepthTrue, want: "color4"},
		{name: "indexed clamps", color: ANSI(200), depth: Depth16, want: "color15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.color.Tmux(tc.depth); got != tc.want {
				t.Fatalf("Tmux() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIndexedHex(t *testing.T) {
	if got := ANSI(9).Hex(); got != "#ff0000" {
		t.Fatalf("ANSI(9).Hex() = %q, want #ff0000", got)
	}
}

func TestDepth(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Depth
		wantErr bool
	}{
		{name: "truecolor", in: "truecolor", want: DepthTrue},
		{name: "256", in: "256", want: Depth256},
		{name: "16", in: "16", want: Depth16},
		{name: "auto is not a depth", in: "auto", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDepth(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseDepth(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if err == nil && (got != tc.want || got.String() != tc.in) {
				t.Fatalf("ParseDepth(%q) = %v (%s)", tc.in, got, got)
			}
		})
	}
}

func TestDetectDepth(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Depth
	}{
		{name: "colorterm truecolor", env: map[string]string{"COLORTERM": "truecolor", "TERM": "xterm"}, want: DepthTrue},
		{name: "colorterm 24bit", env: map[string]string{"COLORTERM": "24bit"}, want: DepthTrue},
		{name: "term 256color", env: map[string]string{"TERM": "xterm-256color"}, want: Depth256},
		{name: "tmux 256color", env: map[string]string{"TERM": "tmux-256color"}, want: Depth256},
		{name: "direct color term", env: map[string]string{"TERM": "xterm-direct"}, want: DepthTrue},
		{name: "plain xterm", env: map[string]string{"TERM": "xterm"}, want: Depth16},
		{name: "dumb", env: map[string]string{"TERM": "dumb"}, want: Depth16},
		{name: "nothing", env: nil, want: Depth16},
		{name: "unknown colorterm", env: map[string]string{"COLORTERM": "yes", "TERM": "screen"}, want: Depth16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectDepth(envOf(tc.env)); got != tc.want {
				t.Fatalf("DetectDepth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// paletteNames is the documented set of built-in palettes, in the sorted
// order Names returns. Adding a palette means adding it here too.
var paletteNames = []string{
	"ansi", "contrast", "dusk", "earth-dark", "earth-light", "light", "lyna",
	"mono", "nord", "rose", "slate", "solar-dark", "solar-light",
}

func TestPalettes(t *testing.T) {
	if got := Names(); !reflect.DeepEqual(got, paletteNames) {
		t.Fatalf("Names() = %v, want %v", got, paletteNames)
	}
	if !slices.IsSorted(Names()) {
		t.Fatalf("Names() is not sorted: %v", Names())
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			p, err := Get(name)
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != name {
				t.Fatalf("palette %q reports name %q", name, p.Name)
			}
			// Every semantic slot must be set and distinguishable from the
			// background, otherwise that element disappears on screen. A
			// zero Color is RGB black, which only the high-contrast set uses
			// on purpose, and there as the background alone.
			v := reflect.ValueOf(p)
			for i := range v.NumField() {
				f := v.Type().Field(i)
				c, ok := v.Field(i).Interface().(Color)
				if !ok {
					continue
				}
				if c == (Color{}) && (f.Name != "Bg" || name != "contrast") {
					t.Errorf("%s.%s is not set", name, f.Name)
				}
				if f.Name != "Bg" && c == p.Bg {
					t.Errorf("%s.%s equals the background", name, f.Name)
				}
			}
			if p.Waiting == p.Busy || p.Waiting == p.Idle || p.Busy == p.Idle {
				t.Errorf("%s: agent states must use distinct colors", name)
			}
			if p.Bg.Indexed != p.Text.Indexed || p.Dark != (Luminance(p.Bg) < Luminance(p.Text)) {
				t.Errorf("%s: Dark=%v does not match its background", name, p.Dark)
			}
			// Legibility bounds. The ansi palette is exempt: it borrows the
			// terminal's own colors, whose real values are unknown here, so
			// the xterm defaults it would be measured on prove nothing.
			if name != "ansi" {
				if got := Contrast(p.Text, p.Bg); got < 4.5 {
					t.Errorf("%s: text contrast %.2f, want at least 4.5", name, got)
				}
				if got := Contrast(p.Muted, p.Bg); got < 3 {
					t.Errorf("%s: muted contrast %.2f, want at least 3", name, got)
				}
			}
			// Reduced depths must keep the text readable and the agent
			// states apart: most terminals still run at 256 colors.
			if To256(p.Text) == To256(p.Bg) || To16(p.Text) == To16(p.Bg) {
				t.Errorf("%s: text and background merge at 256 (%d, %d) or 16 (%d, %d) colors", name, To256(p.Text), To256(p.Bg), To16(p.Text), To16(p.Bg))
			}
			states := []int{To256(p.Busy), To256(p.Waiting), To256(p.Idle)}
			if len(slices.Compact(slices.Sorted(slices.Values(states)))) != 3 {
				t.Errorf("%s: agent states merge at 256 colors: %v", name, states)
			}
		})
	}
	if _, err := Get("neon"); err == nil || !strings.Contains(err.Error(), "available: [ansi contrast") {
		t.Fatalf("Get(neon) = %v, want an error listing the names", err)
	}
}

func TestIcons(t *testing.T) {
	for _, name := range []string{"unicode", "nerd", "ascii"} {
		t.Run(name, func(t *testing.T) {
			set, err := GetIcons(name)
			if err != nil {
				t.Fatal(err)
			}
			if set.Name != name {
				t.Fatalf("set %q reports name %q", name, set.Name)
			}
			v := reflect.ValueOf(set)
			for i := range v.NumField() {
				f := v.Type().Field(i)
				if f.Name == "Name" {
					continue
				}
				glyph := v.Field(i).String()
				if w := ansi.StringWidth(glyph); w > 1 {
					t.Errorf("%s.%s = %q is %d cells wide, want at most 1", name, f.Name, glyph, w)
				}
				if name != "ascii" && glyph == "" {
					t.Errorf("%s.%s is empty", name, f.Name)
				}
				if name == "ascii" {
					for _, r := range glyph {
						if r > 0x7e {
							t.Errorf("ascii.%s = %q is not ASCII", f.Name, glyph)
						}
					}
				}
			}
		})
	}
	if _, err := GetIcons("emoji"); err == nil {
		t.Fatal("GetIcons(emoji) succeeded")
	}
}

func TestResolveIcons(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		env     map[string]string
		want    string
	}{
		{name: "explicit nerd", setting: "nerd", want: "nerd"},
		{name: "explicit ascii on utf8", setting: "ascii", env: map[string]string{"LANG": "en_US.UTF-8"}, want: "ascii"},
		{name: "auto utf8 lang", setting: "auto", env: map[string]string{"LANG": "en_US.UTF-8"}, want: "unicode"},
		{name: "empty behaves as auto", setting: "", env: map[string]string{"LC_CTYPE": "C.utf8"}, want: "unicode"},
		{name: "lc_all wins over lang", setting: "auto", env: map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, want: "ascii"},
		{name: "lc_ctype wins over lang", setting: "auto", env: map[string]string{"LC_CTYPE": "fr_FR.UTF-8", "LANG": "C"}, want: "unicode"},
		{name: "no locale", setting: "auto", want: "ascii"},
		{name: "posix", setting: "auto", env: map[string]string{"LANG": "POSIX"}, want: "ascii"},
		// East Asian locales draw the ambiguous width glyphs two cells wide,
		// which would pull every status segment and border out of line.
		{name: "auto japanese", setting: "auto", env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: "ascii"},
		{name: "auto simplified chinese", setting: "auto", env: map[string]string{"LC_ALL": "zh_CN.UTF-8"}, want: "ascii"},
		{name: "auto korean", setting: "auto", env: map[string]string{"LC_CTYPE": "ko_KR.utf8"}, want: "ascii"},
		{name: "unicode can still be asked for", setting: "unicode", env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: "unicode"},
		{name: "a language that only starts like one", setting: "auto", env: map[string]string{"LANG": "jam_JM.UTF-8"}, want: "unicode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveIcons(tc.setting, envOf(tc.env)); got != tc.want {
				t.Fatalf("ResolveIcons(%q) = %q, want %q", tc.setting, got, tc.want)
			}
		})
	}
}
