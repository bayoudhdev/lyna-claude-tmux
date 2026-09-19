package tmux

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// allLooks returns every palette, color depth and icon set combination, with
// buttons and clock toggled.
func allLooks(t *testing.T) map[string]Look {
	t.Helper()
	out := map[string]Look{}
	for _, pn := range theme.Names() {
		p, err := theme.Get(pn)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []theme.Depth{theme.Depth16, theme.Depth256, theme.DepthTrue} {
			for _, in := range []string{"unicode", "nerd", "ascii"} {
				icons, err := theme.GetIcons(in)
				if err != nil {
					t.Fatal(err)
				}
				for _, flags := range []struct {
					name           string
					buttons, clock bool
				}{{"plain", false, false}, {"buttons+clock", true, true}} {
					out[pn+"/"+d.String()+"/"+in+"/"+flags.name] = Look{Palette: p, Depth: d, Icons: icons, Clock: flags.clock, Buttons: flags.buttons}
				}
			}
		}
	}
	return out
}

func TestLookFormatsParse(t *testing.T) {
	for name, l := range allLooks(t) {
		formats := map[string]string{
			"status-left":                  l.StatusLeft(),
			"status-right":                 l.StatusRight(),
			"window-status-format":         l.WindowFormat(),
			"window-status-current-format": l.WindowCurrentFormat(),
			"pane-border-format":           l.BorderFormat(),
		}
		for fname, f := range formats {
			t.Run(name+"/"+fname, func(t *testing.T) {
				if err := checkFormat(f); err != nil {
					t.Fatal(err)
				}
				if strings.ContainsAny(f, "\n\x1b") {
					t.Fatalf("format has control characters: %q", f)
				}
			})
		}
	}
}

func TestStatusRightParts(t *testing.T) {
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		buttons, clock bool
		icons          theme.Icons
		want, notWant  []string
	}{
		{
			name: "buttons and clock", buttons: true, clock: true, icons: icons,
			want: []string{
				"#[range=user|split]", "#[range=user|review]", "#[range=user|menu]",
				"#[range=user|agents]", "#[range=user|sandbox]", "#[norange]",
				"%H:%M", "◷ ", "sandbox off", "#{@lt_branch}",
			},
		},
		{
			name: "no buttons keeps agents and shield", icons: icons,
			want:    []string{"◎ ", "sandbox off", "#{@lt_managed}"},
			notWant: []string{"range=user", "%H:%M", "split"},
		},
		{
			name: "ascii clock has no icon", clock: true, icons: mustIcons(t, "ascii"),
			want: []string{"%H:%M "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Look{Palette: p, Depth: theme.DepthTrue, Icons: tc.icons, Clock: tc.clock, Buttons: tc.buttons}.StatusRight()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("status-right lacks %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("status-right has %q", w)
				}
			}
		})
	}
}

func mustIcons(t *testing.T, name string) theme.Icons {
	t.Helper()
	i, err := theme.GetIcons(name)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestLookColorsFollowDepth(t *testing.T) {
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		depth   theme.Depth
		want    string
		notWant string
	}{
		{theme.DepthTrue, "bg=#39d353", "bg=color"},
		{theme.Depth256, "bg=color", "bg=#"},
		{theme.Depth16, "bg=color", "bg=#"},
	}
	for _, tc := range cases {
		t.Run(tc.depth.String(), func(t *testing.T) {
			got := Look{Palette: p, Depth: tc.depth, Icons: mustIcons(t, "ascii")}.StatusLeft()
			if !strings.Contains(got, tc.want) || strings.Contains(got, tc.notWant) {
				t.Fatalf("status-left %q: want %q, not %q", got, tc.want, tc.notWant)
			}
		})
	}
}

func TestTextEscapesIcons(t *testing.T) {
	l := Look{Palette: mustPalette(t, "lyna"), Depth: theme.DepthTrue, Icons: theme.Icons{Brand: "#%", Claude: "a,b"}}
	if got := l.StatusLeft(); !strings.Contains(got, " ##%% ") {
		t.Fatalf("brand not escaped: %q", got)
	}
}

func mustPalette(t *testing.T, name string) theme.Palette {
	t.Helper()
	p, err := theme.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBranchOption(t *testing.T) {
	long := strings.Repeat("b", MaxBranchRunes)
	cases := []struct {
		name, in, want string
	}{
		{"plain", "main", "main"},
		{"slash and dash", "feat/api-v2", "feat/api-v2"},
		{"hash escaped", "fix/#12", "fix/##12"},
		{"percent kept", "50%", "50%"},
		{"style markup escaped", "#[fg=red]x", "##[fg=red]x"},
		{"controls removed", "a\x1b[31mb\x07c\x7fd\u009be", "a[31mbcde"},
		{"exactly max", long, long},
		{"truncated", long + "xyz", long + "..."},
		{"truncation counts runes", strings.Repeat("é", MaxBranchRunes+1), strings.Repeat("é", MaxBranchRunes) + "..."},
		{"escape after truncation keeps pairs", strings.Repeat("#", MaxBranchRunes+1), strings.Repeat("##", MaxBranchRunes) + "..."},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BranchOption(tc.in); got != tc.want {
				t.Fatalf("BranchOption(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAgentOption(t *testing.T) {
	long := strings.Repeat("a", MaxAgentRunes)
	cases := []struct {
		name, in, want string
	}{
		{"a teammate name", "review-api", "review-api"},
		{"a name with a hash", "fix/#12", "fix/##12"},
		{"style markup escaped", "#[fg=red]x", "##[fg=red]x"},
		{"controls removed", "a\x1b[31mb\x07c", "a[31mbc"},
		{"exactly max", long, long},
		{"a name longer than the border allows", long + "xyz", long + "..."},
		{"no name", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentOption(tc.in); got != tc.want {
				t.Fatalf("AgentOption(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzAgentOption(f *testing.F) {
	for _, s := range []string{"review-api", "#[x]", "\x1b]52;c;x\x07", strings.Repeat("#", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := AgentOption(in)
		for _, r := range got {
			if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
				t.Fatalf("control %U kept in %q", r, got)
			}
		}
		if strings.Count(strings.ReplaceAll(got, "##", ""), "#") != 0 {
			t.Fatalf("unpaired # in %q", got)
		}
		if n := len([]rune(strings.ReplaceAll(strings.TrimSuffix(got, "..."), "##", "#"))); n > MaxAgentRunes {
			t.Fatalf("%d runes drawn, max %d", n, MaxAgentRunes)
		}
	})
}

func FuzzBranchOption(f *testing.F) {
	for _, s := range []string{"main", "#[x]", "\x1b]52;c;x\x07", strings.Repeat("#", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := BranchOption(in)
		for _, r := range got {
			if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
				t.Fatalf("control %U kept in %q", r, got)
			}
		}
		// Every '#' is part of an escape pair, so the drawn text is literal.
		if strings.Count(strings.ReplaceAll(got, "##", ""), "#") != 0 {
			t.Fatalf("unpaired # in %q", got)
		}
		if n := len([]rune(strings.ReplaceAll(strings.TrimSuffix(got, "..."), "##", "#"))); n > MaxBranchRunes {
			t.Fatalf("%d runes drawn, max %d", n, MaxBranchRunes)
		}
	})
}
