package tmux

import (
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

type confCase struct {
	name    string
	version Version
	palette string
	depth   theme.Depth
	icons   string
	altKeys bool
	mouse   bool
	local   string
	shell   string
	prefix  string
	top     bool
}

// confCases cover each tmux feature tier, every palette, color depth and icon
// set, and the optional settings.
var confCases = []confCase{
	{name: "tmux3.3-lyna-true-unicode", version: Version{Major: 3, Minor: 3, Suffix: "a"}, palette: "lyna", depth: theme.DepthTrue, icons: "unicode", altKeys: true, mouse: true, local: "/home/dev/.config/lyna-tmux/tmux.local.conf"},
	{name: "tmux3.4-lyna-256-unicode", version: Version{Major: 3, Minor: 4}, palette: "lyna", depth: theme.Depth256, icons: "unicode", altKeys: true, mouse: true, local: "/home/dev/.config/lyna-tmux/tmux.local.conf"},
	{name: "tmux3.6-light-true-nerd", version: Version{Major: 3, Minor: 6}, palette: "light", depth: theme.DepthTrue, icons: "nerd", altKeys: true, mouse: true, top: true},
	{name: "tmux3.7-ansi-16-ascii-noalt", version: Version{Major: 3, Minor: 7, Suffix: "c"}, palette: "ansi", depth: theme.Depth16, icons: "ascii", prefix: "C-a", shell: "/bin/zsh"},
}

func (c confCase) options(t *testing.T) ConfOptions {
	t.Helper()
	p, err := theme.Get(c.palette)
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons(c.icons)
	if err != nil {
		t.Fatal(err)
	}
	pos := "bottom"
	if c.top {
		pos = "top"
	}
	return ConfOptions{
		Version: c.version,
		Look:    Look{Palette: p, Depth: c.depth, Icons: icons, Clock: true},
		Env: Env{
			Bin:         "/home/dev/.local/bin/lyna-tmux",
			ConfPath:    "/home/dev/.local/state/lyna-tmux/tmux.conf",
			PopupWidth:  "90%",
			PopupHeight: "85%",
			Bindings:    keys.Defaults(keys.Options{AltKeys: c.altKeys, Prefix: c.prefix}),
		},
		Prefix:         c.prefix,
		Mouse:          c.mouse,
		Bell:           true,
		StatusPosition: pos,
		HistoryLimit:   100000,
		Shell:          c.shell,
		LocalConf:      c.local,
	}
}

func TestGenerateConfGolden(t *testing.T) {
	for _, tc := range confCases {
		t.Run(tc.name, func(t *testing.T) {
			golden.Assert(t, "conf/"+tc.name+".conf", []byte(GenerateConf(tc.options(t))))
		})
	}
}

func TestGenerateConfFeatureGates(t *testing.T) {
	gated := []struct {
		line    string
		feature Feature
	}{
		{"set-option -s extended-keys-format csi-u", FeatureExtendedKeysFormat},
		{"set-option -wg allow-set-title off", FeatureAllowSetTitle},
		{"set-option -g menu-border-lines rounded", FeatureMenuStyles},
		{"set-option -wg pane-scrollbars modal", FeaturePaneScrollbars},
		{"#[range=user|", FeatureUserRanges},
	}
	for _, tc := range confCases {
		conf := GenerateConf(tc.options(t))
		for _, g := range gated {
			t.Run(tc.name+"/"+g.line, func(t *testing.T) {
				if got, want := strings.Contains(conf, g.line), tc.version.Has(g.feature); got != want {
					t.Fatalf("contains %q = %v, want %v", g.line, got, want)
				}
			})
		}
	}
}

func TestGenerateConfSettings(t *testing.T) {
	cases := []struct {
		name          string
		mod           func(*ConfOptions)
		want, notWant []string
	}{
		{
			name:    "defaults",
			mod:     func(*ConfOptions) {},
			want:    []string{"set-option -g prefix C-b", "set-option -g mouse off", "set-option -g status-position bottom", "set-option -wg allow-passthrough off"},
			notWant: []string{"default-shell", "source-file -q", "terminal-features[91]"},
		},
		{
			name:    "truecolor adds RGB feature",
			mod:     func(o *ConfOptions) { o.Look.Depth = theme.DepthTrue },
			want:    []string{"set-option -s 'terminal-features[91]' '*:RGB'"},
			notWant: nil,
		},
		{
			name: "user values",
			mod: func(o *ConfOptions) {
				o.Prefix, o.Mouse, o.AllowPassthrough, o.StatusPosition = "C-a", true, true, "top"
				o.Shell, o.LocalConf, o.HistoryLimit = "/usr/bin/fish shell", "/cfg/it's.conf", 5000
			},
			want: []string{
				"set-option -g prefix C-a", "set-option -g mouse on", "set-option -g status-position top",
				"set-option -wg allow-passthrough on", "set-option -g default-shell '/usr/bin/fish shell'",
				`source-file -q '/cfg/it'\''s.conf'`, "set-option -g history-limit 5000",
			},
		},
		{
			name: "invalid position falls back",
			mod:  func(o *ConfOptions) { o.StatusPosition = "left" },
			want: []string{"set-option -g status-position bottom"},
		},
		{
			name:    "newline in a path cannot inject a line",
			mod:     func(o *ConfOptions) { o.LocalConf = "/cfg/x\nrun-shell 'touch /tmp/pwned'\n.conf" },
			want:    []string{"# Put your own settings in /cfg/xrun-shell 'touch /tmp/pwned'.conf (sourced at the end)."},
			notWant: []string{"\nrun-shell"},
		},
		{
			name:    "glob characters escaped",
			mod:     func(o *ConfOptions) { o.LocalConf = "/cfg/[a]*.conf" },
			want:    []string{`source-file -q '/cfg/\[a]\*.conf'`},
			notWant: nil,
		},
		{
			name:    "local conf sourced last",
			mod:     func(o *ConfOptions) { o.LocalConf = "/cfg/local.conf" },
			want:    []string{"# Put your own settings in /cfg/local.conf"},
			notWant: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := ConfOptions{
				Version: Version{Major: 3, Minor: 3},
				Look:    Look{Palette: mustPalette(t, "lyna"), Depth: theme.Depth256, Icons: mustIcons(t, "ascii")},
				Env:     testEnv(),
			}
			tc.mod(&o)
			conf := GenerateConf(o)
			for _, w := range tc.want {
				if !strings.Contains(conf, w) {
					t.Errorf("conf lacks %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(conf, w) {
					t.Errorf("conf has %q", w)
				}
			}
			if o.LocalConf != "" {
				lines := strings.Split(strings.TrimSpace(conf), "\n")
				if last := lines[len(lines)-1]; !strings.HasPrefix(last, "source-file -q ") {
					t.Errorf("last line = %q, want the local source-file", last)
				}
			}
		})
	}
}

func TestGenerateConfBindsEveryKey(t *testing.T) {
	o := confCases[0].options(t)
	conf := GenerateConf(o)
	for _, b := range o.Env.Bindings {
		t.Run(string(b.Table)+"/"+b.Key, func(t *testing.T) {
			prefix := "bind-key -T " + string(b.Table) + " " + ConfToken(b.Key) + " "
			if !strings.Contains(conf, "\n"+prefix) {
				t.Fatalf("no line starting %q", prefix)
			}
		})
	}
}

func TestGenerateConfIdempotentMarkers(t *testing.T) {
	conf := GenerateConf(confCases[0].options(t))
	for _, line := range strings.Split(conf, "\n") {
		// Appending array options would grow on every reload.
		if strings.Contains(line, "-ag ") || strings.Contains(line, "-as ") {
			t.Fatalf("appending option on reload: %q", line)
		}
	}
}

func TestConfFingerprint(t *testing.T) {
	a := ConfFingerprint("set-option -g mouse on\n")
	cases := []struct {
		name  string
		in    string
		equal bool
	}{
		{"same content", "set-option -g mouse on\n", true},
		{"one byte differs", "set-option -g mouse of\n", false},
		{"empty", "", false},
	}
	if len(a) != 16 {
		t.Fatalf("fingerprint %q is not 16 hex digits", a)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConfFingerprint(tc.in) == a; got != tc.equal {
				t.Fatalf("equal = %v, want %v", got, tc.equal)
			}
		})
	}
}
