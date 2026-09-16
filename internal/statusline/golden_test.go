package statusline

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"

	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// visible spells ESC as \e so golden files review as text.
func visible(s string) string { return strings.ReplaceAll(s, "\x1b", `\e`) }

func TestRenderGolden(t *testing.T) {
	type variant struct {
		name    string
		theme   string
		depth   string
		icons   string
		data    string
		sandbox string
		columns string
	}
	var cases []variant
	for _, depth := range []string{"truecolor", "256", "16"} {
		for _, icons := range []string{"unicode", "nerd", "ascii"} {
			cases = append(cases, variant{
				name:  "lyna-" + depth + "-" + icons,
				theme: "lyna", depth: depth, icons: icons, data: documentedStatus, sandbox: "off",
			})
		}
	}
	cases = append(cases,
		variant{name: "light-truecolor-unicode", theme: "light", depth: "truecolor", icons: "unicode", data: documentedStatus, sandbox: "strict"},
		variant{name: "ansi-truecolor-unicode", theme: "ansi", depth: "truecolor", icons: "unicode", data: documentedStatus, sandbox: "standard"},
		variant{name: "lyna-256-unicode-columns60", theme: "lyna", depth: "256", icons: "unicode", data: documentedStatus, sandbox: "off", columns: "60"},
		variant{name: "lyna-truecolor-unicode-invalid-input", theme: "lyna", depth: "truecolor", icons: "unicode", data: `{"model":`, sandbox: "strict"},
	)
	read := headAt("/home/user/project", "ref: refs/heads/feature-xyz\n")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envMap(map[string]string{
				session.EnvTheme: tc.theme, session.EnvColor: tc.depth, session.EnvIcons: tc.icons,
				"LYNA_TMUX_SANDBOX": tc.sandbox, "COLUMNS": tc.columns,
			})
			got := render([]byte(tc.data), env, fixedNow, read)
			golden.Assert(t, tc.name+".golden", []byte(visible(got)+"\n"+ansi.Strip(got)+"\n"))
		})
	}
}

func FuzzRender(f *testing.F) {
	f.Add([]byte(documentedStatus), 0, uint8(0))
	f.Add([]byte(documentedStatus), 40, uint8(5))
	f.Add([]byte("{\"model\":{\"display_name\":\"\x1b]0;x\x07\"}}"), 3, uint8(7))
	f.Add([]byte(`{"cwd":"/a/`+strings.Repeat("漢", 40)+`","rate_limits":{"five_hour":{"used_percentage":1e308,"resets_at":-1}}}`), 17, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, columns int, pick uint8) {
		depths := []string{"truecolor", "256", "16"}
		icons := []string{"unicode", "nerd", "ascii"}
		profiles := []string{"", "off", "strict", "standard"}
		vars := map[string]string{
			session.EnvColor: depths[pick%3], session.EnvIcons: icons[(pick/3)%3], "LYNA_TMUX_SANDBOX": profiles[(pick/9)%4],
			"COLUMNS": strconv.Itoa(columns % 512),
		}
		if pick&0x80 != 0 {
			vars["NO_COLOR"] = "1"
		}
		got := render(data, envMap(vars), fixedNow, noRepo)
		if again := render(data, envMap(vars), fixedNow, noRepo); again != got {
			t.Fatalf("render is not deterministic: %q then %q", got, again)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8: %q", got)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("line break in %q", got)
		}
		// Only the renderer's own SGR color sequences reach the terminal. The
		// sanitizer also drops private-use glyphs, which the trusted nerd icon
		// set is made of, so those are taken out of the expectation.
		want := got
		set, _ := theme.GetIcons(vars[session.EnvIcons])
		for _, glyph := range []string{set.Claude, set.Branch, set.Shield, set.ShieldOff, set.Sep} {
			if glyph != "" && sanitize.Plain(glyph) != glyph {
				want = strings.ReplaceAll(want, glyph, "")
			}
		}
		if clean := sanitize.Terminal(got); clean != want {
			t.Fatalf("control sequences in output:\n%q\nsanitized\n%q", got, clean)
		}
		if n := columns % 512; n > 0 && ansi.StringWidth(got) > n {
			t.Fatalf("width %d exceeds COLUMNS=%d: %q", ansi.StringWidth(got), n, got)
		}
	})
}

func BenchmarkRender(b *testing.B) {
	env := envMap(map[string]string{
		session.EnvTheme: "lyna", session.EnvColor: "truecolor", session.EnvIcons: "unicode",
		"LYNA_TMUX_SANDBOX": "standard", "COLUMNS": "120",
	})
	data := []byte(documentedStatus)

	b.Run("documented", func(b *testing.B) {
		read := headAt("/home/user/project", "ref: refs/heads/feature-xyz\n")
		b.ReportAllocs()
		for b.Loop() {
			render(data, env, fixedNow, read)
		}
	})
	b.Run("repository on disk", func(b *testing.B) {
		repo := b.TempDir()
		sub := filepath.Join(repo, "internal", "app")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			b.Fatal(err)
		}
		onDisk := []byte(strings.Replace(documentedStatus, `"current_dir": "/home/user/project"`, `"current_dir": "`+sub+`"`, 1))
		b.ReportAllocs()
		for b.Loop() {
			Render(onDisk, env, fixedNow)
		}
	})
}
