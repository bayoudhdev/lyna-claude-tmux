package claudetheme

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

func mustPalette(t testing.TB, name string) theme.Palette {
	t.Helper()
	p, err := theme.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func decodeStrict(t *testing.T, data []byte) File {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return f
}

func TestTokensAndPresets(t *testing.T) {
	toks := Tokens()
	if len(toks) != 72 || !slices.IsSorted(toks) || len(slices.Compact(slices.Clone(toks))) != len(toks) {
		t.Fatalf("tokens must be 72 sorted unique names, got %d", len(toks))
	}
	toks[0] = "mutated"
	if Tokens()[0] == "mutated" {
		t.Fatal("Tokens returned the package slice")
	}
	if got := Presets(); !slices.Equal(got, []string{"dark", "light", "dark-daltonized", "light-daltonized", "dark-ansi", "light-ansi"}) {
		t.Fatalf("presets %q", got)
	}
}

func TestGenerateEveryPalette(t *testing.T) {
	cases := []struct {
		palette  string
		wantName string
		wantBase string
	}{
		{palette: "lyna", wantName: "Lyna", wantBase: "dark"},
		{palette: "light", wantName: "Lyna Light", wantBase: "light"},
		{palette: "ansi", wantName: "Lyna ANSI", wantBase: "dark-ansi"},
		{palette: "slate", wantName: "Lyna Slate", wantBase: "dark"},
		{palette: "dusk", wantName: "Lyna Dusk", wantBase: "dark"},
		{palette: "contrast", wantName: "Lyna Contrast", wantBase: "dark"},
		{palette: "nord", wantName: "Lyna Nord", wantBase: "dark"},
		{palette: "rose", wantName: "Lyna Rose", wantBase: "dark"},
		{palette: "mono", wantName: "Lyna Mono", wantBase: "dark"},
		{palette: "solar-dark", wantName: "Lyna Solar Dark", wantBase: "dark"},
		{palette: "solar-light", wantName: "Lyna Solar Light", wantBase: "light"},
		{palette: "earth-dark", wantName: "Lyna Earth Dark", wantBase: "dark"},
		{palette: "earth-light", wantName: "Lyna Earth Light", wantBase: "light"},
	}
	if len(cases) != len(theme.Names()) {
		t.Fatalf("palettes %q are not all covered", theme.Names())
	}
	for _, tc := range cases {
		t.Run(tc.palette, func(t *testing.T) {
			data, err := Generate(mustPalette(t, tc.palette))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) > MaxFileBytes || !bytes.HasSuffix(data, []byte("}\n")) {
				t.Fatalf("unexpected framing or size %d", len(data))
			}
			f := decodeStrict(t, data)
			if f.Name != tc.wantName || f.Base != tc.wantBase || f.Generator != Generator {
				t.Fatalf("name %q base %q generator %q", f.Name, f.Base, f.Generator)
			}
			if !slices.Contains(Presets(), f.Base) {
				t.Fatalf("base %q is not a preset", f.Base)
			}
			if len(f.Overrides) < 30 {
				t.Fatalf("only %d overrides", len(f.Overrides))
			}
			for tok, val := range f.Overrides {
				if _, ok := slices.BinarySearch(tokens, tok); !ok {
					t.Errorf("override %q is not a Claude Code token", tok)
				}
				if !ValidColor(val) {
					t.Errorf("override %s = %q is not a valid color", tok, val)
				}
			}
			if !Recognize(data) {
				t.Fatal("generated file is not recognized")
			}
			again, err := Generate(mustPalette(t, tc.palette))
			if err != nil || !bytes.Equal(again, data) {
				t.Fatalf("Generate is not deterministic: %v", err)
			}
		})
	}
}

func TestGenerateMapping(t *testing.T) {
	gen := func(name string) map[string]string {
		data, err := Generate(mustPalette(t, name))
		if err != nil {
			t.Fatal(err)
		}
		return decodeStrict(t, data).Overrides
	}
	lyna, light, ansi := gen("lyna"), gen("light"), gen("ansi")
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"lyna claude is the accent", lyna["claude"], "#39d353"},
		{"lyna shimmer is lighter", lyna["claudeShimmer"], "#6bde7e"},
		{"lyna text", lyna["text"], "#e6edf3"},
		{"lyna inverse text is the background", lyna["inverseText"], "#0d1117"},
		{"lyna error is danger", lyna["error"], "#ff5f56"},
		{"lyna warning", lyna["warning"], "#f0b72f"},
		{"lyna prompt border is muted", lyna["promptBorder"], "#7d8590"},
		{"lyna diff added blends success into the background", lyna["diffAdded"], "#1a4b29"},
		{"lyna diff removed word", lyna["diffRemovedWord"], "#9e403d"},
		{"lyna user message background is the surface", lyna["userMessageBackground"], "#161b22"},
		{"lyna selection is the overlay", lyna["selectionBg"], "#262d38"},
		{"light claude", light["claude"], "#1a7f37"},
		{"light diff added stays light", light["diffAdded"], "#b4d4c0"},
		{"ansi claude uses the terminal palette", ansi["claude"], "ansi:green"},
		{"ansi shimmer is the bright variant", ansi["claudeShimmer"], "ansi:greenBright"},
		{"ansi bright stays bright", ansi["inactiveShimmer"], "ansi:whiteBright"},
		{"ansi text", ansi["text"], "ansi:whiteBright"},
		{"ansi diff added", ansi["diffAdded"], "ansi:green"},
		{"ansi diff removed dimmed", ansi["diffRemovedDimmed"], "ansi:red"},
		{"ansi diff added word", ansi["diffAddedWord"], "ansi:greenBright"},
		{"ansi background surfaces", ansi["userMessageBackground"], "ansi:blackBright"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestTintMixedPalette(t *testing.T) {
	// A palette mixing terminal and RGB colors never blends across the two.
	p := theme.Palette{Bg: theme.ANSI(0)}
	if got := tint(p, theme.RGB(1, 2, 3), 0.3); got != theme.RGB(1, 2, 3) {
		t.Fatalf("tint = %+v", got)
	}
	if got := tint(theme.Palette{Bg: theme.RGB(0, 0, 0)}, theme.ANSI(9), 0.3); got != theme.ANSI(1) {
		t.Fatalf("tint = %+v", got)
	}
	if got := shimmer(theme.ANSI(12)); got != theme.ANSI(12) {
		t.Fatalf("shimmer = %+v", got)
	}
	if got := base(theme.Palette{Bg: theme.ANSI(15)}); got != "light-ansi" {
		t.Fatalf("base = %q", got)
	}
}

func TestGenerateRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "Lyna", "../etc", "a/b", "-dark", "dark-", "two words", "caf\xc3\xa9"} {
		p := mustPalette(t, "lyna")
		p.Name = name
		if _, err := Generate(p); !errors.Is(err, ErrPaletteName) {
			t.Errorf("Generate(%q) = %v; want ErrPaletteName", name, err)
		}
	}
	p := mustPalette(t, "lyna")
	p.Name = "high-contrast2"
	if _, err := Generate(p); err != nil {
		t.Fatalf("Generate(high-contrast2) = %v", err)
	}
}

func TestNames(t *testing.T) {
	cases := []struct{ in, file, display string }{
		{"lyna", "lyna-lyna.json", "Lyna"},
		{"light", "lyna-light.json", "Lyna Light"},
		{"ansi", "lyna-ansi.json", "Lyna ANSI"},
		{"solar-dark", "lyna-solar-dark.json", "Lyna Solar Dark"},
		{"earth-light", "lyna-earth-light.json", "Lyna Earth Light"},
		{"a--b", "lyna-a--b.json", "Lyna A  B"},
		{"", "lyna-.json", "Lyna"},
	}
	for _, tc := range cases {
		if got := FileName(tc.in); got != tc.file {
			t.Errorf("FileName(%q) = %q; want %q", tc.in, got, tc.file)
		}
		if got := DisplayName(tc.in); got != tc.display {
			t.Errorf("DisplayName(%q) = %q; want %q", tc.in, got, tc.display)
		}
	}
}

func TestValidColor(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"#39d353", true},
		{"#39D353", true},
		{"#abc", true},
		{"#abcd", false},
		{"#39d35", false},
		{"#gggggg", false},
		{"rgb(1,2,3)", true},
		{"rgb(255, 255, 255)", true},
		{"rgb( 1,2,3 )", true},
		{"rgb(1,  2,3)", false},
		{"rgb(1,2,3,4)", false},
		{"rgb(1000,2,3)", false},
		{"ansi256(255)", true},
		{"ansi256(1)", true},
		{"ansi256( 1)", false},
		{"ansi256(1234)", false},
		{"ansi:red", true},
		{"ansi:cyanBright", true},
		{"ansi:Cyan", false},
		{"ansi:", false},
		{"red", false},
		{"", false},
		{"#39d353\n", false},
		{"rgb(1,2,3)#abc", false},
		{"#abcansi256(1)", false},
		{"#abc|ansi256(1)", false},
	}
	for _, tc := range cases {
		if got := ValidColor(tc.in); got != tc.want {
			t.Errorf("ValidColor(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestRecognize(t *testing.T) {
	data, err := Generate(mustPalette(t, "lyna"))
	if err != nil {
		t.Fatal(err)
	}
	edit := func(mutate func(f *File)) []byte {
		var f File
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		mutate(&f)
		out, err := encode(f)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "generated", data: data, want: true},
		{name: "reformatted but unchanged", data: func() []byte {
			var buf bytes.Buffer
			if err := json.Compact(&buf, data); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}(), want: true},
		{name: "color edited by hand", data: edit(func(f *File) { f.Overrides["claude"] = "#ff00ff" }), want: false},
		{name: "renamed by hand", data: edit(func(f *File) { f.Name = "Mine" }), want: false},
		{name: "base changed", data: edit(func(f *File) { f.Base = "light" }), want: false},
		{name: "saved by the theme editor", data: edit(func(f *File) { f.Generator, f.Checksum = "", "" }), want: false},
		{name: "other generator", data: edit(func(f *File) { f.Generator = "someone-else" }), want: false},
		{name: "user theme", data: []byte(`{"name":"Dracula","base":"dark","overrides":{"claude":"#bd93f9"}}`), want: false},
		{name: "invalid JSON", data: []byte(`{"name":`), want: false},
		{name: "wrong types", data: []byte(`{"generator":"lyna-tmux","overrides":[1]}`), want: false},
		{name: "empty", data: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Recognize(tc.data); got != tc.want {
				t.Fatalf("Recognize = %v; want %v", got, tc.want)
			}
		})
	}
}

func FuzzRecognize(f *testing.F) {
	for _, name := range theme.Names() {
		data, err := Generate(mustPalette(f, name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"name":"x","base":"dark","overrides":{},"generator":"lyna-tmux","checksum":"sha256:00"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ok := Recognize(data)
		var file File
		err := json.Unmarshal(data, &file)
		if ok && (err != nil || file.Generator != Generator) {
			t.Fatalf("recognized a file without the generator marker: %q", data)
		}
		if err != nil {
			return
		}
		// Stamping any decodable file makes it recognized, and changing a
		// color afterwards makes it foreign again.
		file.Generator = Generator
		file.Checksum = file.checksum()
		stamped, err := encode(file)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !Recognize(stamped) {
			t.Fatalf("stamped file not recognized: %s", stamped)
		}
		if file.Overrides == nil {
			file.Overrides = map[string]string{}
		}
		file.Overrides["claude"] += "x"
		edited, err := encode(file)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if Recognize(edited) {
			t.Fatalf("edited file still recognized: %s", edited)
		}
	})
}

// TestPluginThemesAreGenerated keeps plugins/lyna-tmux/themes identical to
// Generate. Regenerate with LYNA_TMUX_UPDATE_GOLDEN=1.
func TestPluginThemesAreGenerated(t *testing.T) {
	dir := filepath.Join("..", "..", "plugins", "lyna-tmux", DirName)
	want := map[string][]byte{}
	for _, name := range theme.Names() {
		data, err := Generate(mustPalette(t, name))
		if err != nil {
			t.Fatal(err)
		}
		want[FileName(name)] = data
	}
	if os.Getenv(golden.EnvUpdate) == "1" {
		for file, data := range want {
			if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	var names []string
	for file := range want {
		names = append(names, file)
	}
	slices.Sort(names)
	if !slices.Equal(got, names) {
		t.Fatalf("%s holds %q; want exactly %q", dir, got, names)
	}
	for file, data := range want {
		onDisk, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(onDisk, data) {
			t.Fatalf("%s differs from Generate (regenerate with %s=1)\n%s", file, golden.EnvUpdate, golden.Diff(data, onDisk))
		}
	}
}

func palettes(t *testing.T, names ...string) []theme.Palette {
	t.Helper()
	out := make([]theme.Palette, 0, len(names))
	for _, n := range names {
		out = append(out, mustPalette(t, n))
	}
	return out
}

func generated(t *testing.T, p theme.Palette) []byte {
	t.Helper()
	data, err := Generate(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot reads every entry of dir, following links, so a later comparison
// shows whether anything a refused file pointed at changed.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			out[e.Name()] = "<unreadable>"
			continue
		}
		out[e.Name()] = string(data)
	}
	return out
}

func TestWrite(t *testing.T) {
	names := []string{"lyna", "light", "ansi"}
	lyna := mustPalette(t, "lyna")
	older := lyna
	older.Accent = theme.RGB(1, 2, 3)
	var edited File
	if err := json.Unmarshal(generated(t, lyna), &edited); err != nil {
		t.Fatal(err)
	}
	edited.Overrides["claude"] = "#bd93f9"
	editedData, err := encode(edited)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		prepare func(t *testing.T, themes string)
		want    map[string]Outcome
		wantDir bool
	}{
		{
			name:    "creates the directory and every file",
			want:    map[string]Outcome{"lyna": Created, "light": Created, "ansi": Created},
			wantDir: true,
		},
		{
			name: "identical files are unchanged",
			prepare: func(t *testing.T, themes string) {
				for _, p := range palettes(t, names...) {
					writeFile(t, filepath.Join(themes, FileName(p.Name)), generated(t, p))
				}
			},
			want: map[string]Outcome{"lyna": Unchanged, "light": Unchanged, "ansi": Unchanged},
		},
		{
			name: "file from an earlier palette is updated",
			prepare: func(t *testing.T, themes string) {
				writeFile(t, filepath.Join(themes, FileName("lyna")), generated(t, older))
			},
			want: map[string]Outcome{"lyna": Updated, "light": Created, "ansi": Created},
		},
		{
			name: "hand edited file is refused",
			prepare: func(t *testing.T, themes string) {
				writeFile(t, filepath.Join(themes, FileName("lyna")), editedData)
			},
			want: map[string]Outcome{"lyna": Refused, "light": Created, "ansi": Created},
		},
		{
			name: "user theme under the same name is refused",
			prepare: func(t *testing.T, themes string) {
				writeFile(t, filepath.Join(themes, FileName("light")), []byte(`{"name":"Mine","base":"light","overrides":{"claude":"#bd93f9"}}`+"\n"))
			},
			want: map[string]Outcome{"lyna": Created, "light": Refused, "ansi": Created},
		},
		{
			name: "invalid JSON is refused",
			prepare: func(t *testing.T, themes string) {
				writeFile(t, filepath.Join(themes, FileName("ansi")), []byte("{"))
			},
			want: map[string]Outcome{"lyna": Created, "light": Created, "ansi": Refused},
		},
		{
			name: "symbolic link is refused and its target kept",
			prepare: func(t *testing.T, themes string) {
				target := filepath.Join(filepath.Dir(themes), "target.json")
				writeFile(t, target, generated(t, older))
				if err := os.Symlink(target, filepath.Join(themes, FileName("lyna"))); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]Outcome{"lyna": Refused, "light": Created, "ansi": Created},
		},
		{
			name: "oversized file is refused",
			prepare: func(t *testing.T, themes string) {
				writeFile(t, filepath.Join(themes, FileName("lyna")), bytes.Repeat([]byte(" "), MaxFileBytes+1))
			},
			want: map[string]Outcome{"lyna": Refused, "light": Created, "ansi": Created},
		},
		{
			name: "directory in place of a file is refused",
			prepare: func(t *testing.T, themes string) {
				if err := os.Mkdir(filepath.Join(themes, FileName("light")), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]Outcome{"lyna": Created, "light": Refused, "ansi": Created},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			themes := filepath.Join(home, DirName)
			if tc.prepare != nil {
				if err := os.Mkdir(themes, 0o755); err != nil {
					t.Fatal(err)
				}
				tc.prepare(t, themes)
			}
			before := snapshot(t, themes)
			targetBefore, _ := os.ReadFile(filepath.Join(home, "target.json"))

			rep, err := Write(home, palettes(t, names...)...)
			if rep.Dir != themes || rep.CreatedDir != tc.wantDir || len(rep.Files) != len(names) {
				t.Fatalf("report %+v", rep)
			}
			refused := false
			for i, res := range rep.Files {
				p := mustPalette(t, names[i])
				if res.Palette != p.Name || res.Path != filepath.Join(themes, FileName(p.Name)) {
					t.Fatalf("result %d = %+v", i, res)
				}
				if res.Outcome != tc.want[p.Name] {
					t.Fatalf("%s: outcome %s (%v); want %s", p.Name, res.Outcome, res.Err, tc.want[p.Name])
				}
				switch res.Outcome {
				case Refused:
					refused = true
					if !errors.Is(res.Err, ErrNotGenerated) {
						t.Fatalf("%s: error %v; want ErrNotGenerated", p.Name, res.Err)
					}
					if after := snapshot(t, themes); after[FileName(p.Name)] != before[FileName(p.Name)] {
						t.Fatalf("%s: refused file changed", p.Name)
					}
				default:
					if res.Err != nil {
						t.Fatalf("%s: unexpected error %v", p.Name, res.Err)
					}
					info, err := os.Lstat(res.Path)
					if err != nil {
						t.Fatal(err)
					}
					if !info.Mode().IsRegular() {
						t.Fatalf("%s: mode %v", p.Name, info.Mode())
					}
					if res.Outcome != Unchanged && info.Mode().Perm() != fsx.PrivateFile {
						t.Fatalf("%s: permissions %v", p.Name, info.Mode().Perm())
					}
					onDisk, err := os.ReadFile(res.Path)
					if err != nil || !bytes.Equal(onDisk, generated(t, p)) {
						t.Fatalf("%s: content differs from Generate (%v)", p.Name, err)
					}
				}
			}
			if refused != (err != nil) || refused && !errors.Is(err, ErrNotGenerated) {
				t.Fatalf("Write error %v with refused=%v", err, refused)
			}
			if tc.wantDir {
				info, err := os.Stat(themes)
				if err != nil || info.Mode().Perm() != fsx.PrivateDir {
					t.Fatalf("themes directory %v, %v", info, err)
				}
			}
			if targetAfter, _ := os.ReadFile(filepath.Join(home, "target.json")); !bytes.Equal(targetAfter, targetBefore) {
				t.Fatal("link target changed")
			}
			// Nothing else appears next to the theme files.
			after := snapshot(t, themes)
			for file := range maps.Keys(after) {
				if !strings.HasPrefix(file, "lyna-") || !strings.HasSuffix(file, ".json") {
					t.Fatalf("stray file %q", file)
				}
			}
		})
	}
}

func TestWriteErrors(t *testing.T) {
	rootUser := os.Geteuid() == 0
	cases := []struct {
		name      string
		home      func(t *testing.T) string
		palettes  func(t *testing.T) []theme.Palette
		skipRoot  bool
		wantErrIs error
		wantText  string
		wantFiles map[string]Outcome
	}{
		{
			name:     "relative config directory",
			home:     func(*testing.T) string { return "relative/.claude" },
			wantText: "must be absolute",
		},
		{
			name:      "missing config directory",
			home:      func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") },
			wantErrIs: fs.ErrNotExist,
		},
		{
			name: "config directory is a file",
			home: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "file")
				writeFile(t, path, nil)
				return path
			},
			wantErrIs: fsx.ErrNotDir,
		},
		{
			name: "themes directory is a link",
			home: func(t *testing.T) string {
				home := t.TempDir()
				elsewhere := filepath.Join(home, "elsewhere")
				if err := os.Mkdir(elsewhere, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, filepath.Join(home, DirName)); err != nil {
					t.Fatal(err)
				}
				return home
			},
			wantErrIs: fsx.ErrSymlink,
		},
		{
			name: "themes path is a file",
			home: func(t *testing.T) string {
				home := t.TempDir()
				writeFile(t, filepath.Join(home, DirName), nil)
				return home
			},
			wantErrIs: fsx.ErrNotDir,
		},
		{
			name: "config directory not writable",
			home: func(t *testing.T) string {
				home := t.TempDir()
				if err := os.Chmod(home, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
				return home
			},
			skipRoot:  true,
			wantErrIs: fs.ErrPermission,
		},
		{
			name: "themes directory not writable",
			home: func(t *testing.T) string {
				home := t.TempDir()
				if err := os.Mkdir(filepath.Join(home, DirName), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, DirName), 0o700) })
				return home
			},
			skipRoot:  true,
			wantErrIs: fs.ErrPermission,
			wantFiles: map[string]Outcome{"lyna": Failed},
		},
		{
			name: "invalid palette name does not stop the others",
			home: func(t *testing.T) string { return t.TempDir() },
			palettes: func(t *testing.T) []theme.Palette {
				bad := mustPalette(t, "lyna")
				bad.Name = "../escape"
				return []theme.Palette{bad, mustPalette(t, "light")}
			},
			wantErrIs: ErrPaletteName,
			wantFiles: map[string]Outcome{"../escape": Failed, "light": Created},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipRoot && rootUser {
				t.Skip("permission bits do not apply to root")
			}
			ps := []theme.Palette{mustPalette(t, "lyna")}
			if tc.palettes != nil {
				ps = tc.palettes(t)
			}
			rep, err := Write(tc.home(t), ps...)
			if err == nil {
				t.Fatalf("Write succeeded: %+v", rep)
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("Write error %v; want %v", err, tc.wantErrIs)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("Write error %v; want %q", err, tc.wantText)
			}
			got := map[string]Outcome{}
			for _, res := range rep.Files {
				got[res.Palette] = res.Outcome
			}
			if len(tc.wantFiles) == 0 && len(rep.Files) != 0 || len(tc.wantFiles) != 0 && !maps.Equal(got, tc.wantFiles) {
				t.Fatalf("files %v; want %v", got, tc.wantFiles)
			}
		})
	}
}
