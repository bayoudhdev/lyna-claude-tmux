package theme

import (
	"strings"
	"testing"
	"unicode"
)

// paletteColors lists every color slot of a palette by name.
func paletteColors(p Palette) map[string]Color {
	return map[string]Color{
		"Bg": p.Bg, "Surface": p.Surface, "Overlay": p.Overlay, "Border": p.Border, "Muted": p.Muted, "Text": p.Text,
		"Accent": p.Accent, "Accent2": p.Accent2, "Busy": p.Busy, "Waiting": p.Waiting, "Idle": p.Idle,
		"Danger": p.Danger, "Success": p.Success, "Warning": p.Warning,
	}
}

func TestPreview(t *testing.T) {
	cases := []struct {
		name  string
		icons string
		// wantText are fragments every preview row must show, by kind.
		wantText map[string][]string
	}{
		{
			name: "unicode icons", icons: "unicode",
			wantText: map[string][]string{
				LineStatus: {" λ ", " api ", "○ 1:main", "● 2:auth [z]", "◆ 3:docs", "⊞ split", "± review", " ≡ ", "◆ 1 waiting", "▣ strict", "⎇ main", "◷ 12:00"},
				LineBorder: {"━━ ✻ claude", "● working +2 subagents", "━━ › shell "},
				LineMenu:   {"│ Agents            M-a │", "│ Review changes    M-g │"},
			},
		},
		{
			name: "ascii icons", icons: "ascii",
			wantText: map[string][]string{
				LineStatus: {" L ", "o 1:main", "* 2:auth [z]", "! 3:docs", "+ split", "~ review", " = ", "! 1 waiting", "# strict", "@ main", " 12:00 "},
				LineBorder: {"-- > claude", "* working +2 subagents", "-- $ shell "},
				LineMenu:   {"| Agents            M-a |", "| Review changes    M-g |"},
			},
		},
	}
	for _, tc := range cases {
		for _, style := range []string{StatusPlain, StatusPowerline} {
			t.Run(tc.name+"/"+style, func(t *testing.T) { runPreviewCase(t, tc.icons, style, tc.wantText) })
		}
	}
}

// runPreviewCase draws every palette in one icon set and one status bar style
// and holds the rows to what they must show.
func runPreviewCase(t *testing.T, iconSet, style string, wantText map[string][]string) {
	t.Helper()
	icons, err := GetIcons(iconSet)
	if err != nil {
		t.Fatal(err)
	}
	seps, err := GetSeps(style)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names() {
		p, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := Preview(p, icons, seps)
		kinds := make([]string, len(lines))
		text := map[string]string{}
		for i, l := range lines {
			kinds[i] = l.Kind
			text[l.Kind] += l.Text() + "\n"
		}
		if got := strings.Join(kinds, " "); got != "status border border menu menu" {
			t.Fatalf("%s: rows %q", name, got)
		}
		for kind, fragments := range wantText {
			for _, f := range fragments {
				if !strings.Contains(text[kind], f) {
					t.Errorf("%s: %s rows lack %q:\n%s", name, kind, f, text[kind])
				}
			}
		}
		if iconSet == "ascii" && !seps.Powerline() {
			for kind, s := range text {
				if strings.ContainsFunc(s, func(r rune) bool { return r > unicode.MaxASCII }) {
					t.Errorf("%s: %s rows are not ASCII with ascii icons:\n%s", name, kind, s)
				}
			}
		}
		if seps.Powerline() {
			for _, g := range []string{seps.Right, seps.Left, seps.Thin} {
				if !strings.Contains(text[LineStatus], g) {
					t.Errorf("%s: the bar draws no %q separator:\n%s", name, g, text[LineStatus])
				}
			}
			if !strings.Contains(text[LineBorder], seps.Right) {
				t.Errorf("%s: the active border wears no label block:\n%s", name, text[LineBorder])
			}
		}
		checkPreviewSpans(t, p, seps, lines)
	}
}

// checkPreviewSpans holds every span to the palette: the preview shows the
// theme, so a color from outside it or an empty run would be a lie.
func checkPreviewSpans(t *testing.T, p Palette, seps Seps, lines []Line) {
	t.Helper()
	colors := paletteColors(p)
	inPalette := func(c Color) bool {
		for _, pc := range colors {
			if pc == c {
				return true
			}
		}
		return false
	}
	seen := map[Color]bool{}
	for _, l := range lines {
		// A row with a right part fills the gap between the two in its base
		// style, which must be a palette background as well.
		if (len(l.Right) > 0) != (l.Base.Fill && l.Base.Text == "") {
			t.Errorf("%s: %s row has a right part %v but base %+v", p.Name, l.Kind, len(l.Right) > 0, l.Base)
		}
		if l.Base.Fill && (!inPalette(l.Base.Fg) || l.Base.Bg != p.Bg) {
			t.Errorf("%s: %s row base %+v is not the palette background", p.Name, l.Kind, l.Base)
		}
		for _, s := range append(append([]Span{}, l.Spans...), l.Right...) {
			if s.Text == "" {
				t.Errorf("%s: empty span in a %s row", p.Name, l.Kind)
			}
			if strings.ContainsFunc(s.Text, unicode.IsControl) {
				t.Errorf("%s: control character in %q", p.Name, s.Text)
			}
			if !inPalette(s.Fg) {
				t.Errorf("%s: foreground %+v of %q is not in the palette", p.Name, s.Fg, s.Text)
			}
			if s.Fill && !inPalette(s.Bg) {
				t.Errorf("%s: background %+v of %q is not in the palette", p.Name, s.Bg, s.Text)
			}
			if l.Kind == LineStatus && !s.Fill {
				t.Errorf("%s: status span %q has no background", p.Name, s.Text)
			}
			if l.Kind == LineBorder && s.Fill && !seps.Powerline() {
				t.Errorf("%s: border span %q paints a background", p.Name, s.Text)
			}
			seen[s.Fg] = true
			if s.Fill {
				seen[s.Bg] = true
			}
		}
	}
	// The preview exists to show the theme, so the colors that set one apart
	// (the accents, all three agent states, and each surface) must appear.
	for _, slot := range []string{"Bg", "Surface", "Border", "Muted", "Text", "Accent", "Accent2", "Busy", "Waiting", "Idle", "Success", "Warning"} {
		if !seen[colors[slot]] {
			t.Errorf("%s: preview never draws %s", p.Name, slot)
		}
	}
}

func TestLineText(t *testing.T) {
	cases := []struct {
		name string
		line Line
		want string
	}{
		{name: "empty", line: Line{}, want: ""},
		{name: "left only", line: Line{Spans: []Span{{Text: "a"}, {Text: "b "}}}, want: "ab "},
		{name: "right joins after two spaces", line: Line{Spans: []Span{{Text: "left"}}, Right: []Span{{Text: "r1"}, {Text: "r2"}}}, want: "left  r1r2"},
		{name: "right only", line: Line{Right: []Span{{Text: "r"}}}, want: "  r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.line.Text(); got != tc.want {
				t.Fatalf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}
