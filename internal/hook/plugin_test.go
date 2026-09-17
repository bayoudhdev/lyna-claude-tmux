package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// repoRoot is the module root as seen from this package directory.
var repoRoot = filepath.Join("..", "..")

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPluginHooksFileIsGenerated keeps the committed hooks.json identical to
// PluginHooks. Regenerate with LYNA_TMUX_UPDATE_GOLDEN=1 and review the diff.
func TestPluginHooksFileIsGenerated(t *testing.T) {
	want, err := PluginHooks()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(PluginHooksPath))
	if os.Getenv(golden.EnvUpdate) == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := readRepoFile(t, PluginHooksPath)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale (regenerate with %s=1)\n%s", PluginHooksPath, golden.EnvUpdate, golden.Diff(want, got))
	}
}

type fileHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   *bool  `json:"async"`
}

type fileGroup struct {
	Matcher *string       `json:"matcher"`
	Hooks   []fileHandler `json:"hooks"`
}

// orderedHooks decodes the "hooks" object of hooks.json keeping key order,
// which encoding/json maps would lose.
func orderedHooks(t *testing.T, data []byte) (string, []hookevent.Registration, []fileGroup) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	for k := range top {
		if k != "description" && k != "hooks" {
			t.Fatalf("unexpected top-level key %q", k)
		}
	}
	var description string
	if err := json.Unmarshal(top["description"], &description); err != nil {
		t.Fatalf("description: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(top["hooks"]))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("hooks is not an object: %v %v", tok, err)
	}
	var regs []hookevent.Registration
	var groups []fileGroup
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		event := tok.(string)
		var gs []fileGroup
		if err := dec.Decode(&gs); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
		for _, g := range gs {
			m := ""
			if g.Matcher != nil {
				m = *g.Matcher
				if m == "" {
					t.Fatalf("%s: empty matcher should be omitted", event)
				}
			}
			regs = append(regs, hookevent.Registration{Event: hookevent.Event(event), Matcher: m})
			groups = append(groups, g)
		}
	}
	return description, regs, groups
}

func TestPluginHooksMatchRegistrations(t *testing.T) {
	description, regs, groups := orderedHooks(t, readRepoFile(t, PluginHooksPath))
	if strings.TrimSpace(description) == "" {
		t.Fatal("hooks.json needs a description")
	}
	if !reflect.DeepEqual(regs, hookevent.Registrations()) {
		t.Fatalf("hooks.json registrations\n%v\nwant\n%v", regs, hookevent.Registrations())
	}
	for i, g := range groups {
		t.Run(string(regs[i].Event)+"/"+regs[i].Matcher, func(t *testing.T) {
			if len(g.Hooks) != 1 {
				t.Fatalf("%d handlers; want 1", len(g.Hooks))
			}
			h := g.Hooks[0]
			if h.Type != "command" {
				t.Fatalf("type %q", h.Type)
			}
			if h.Async == nil || !*h.Async {
				t.Fatal("handler must be async so the turn never waits on tmux")
			}
			want := `case ":$PATH:" in *:[!/]*) exit 0;; esac; bin=$(command -v lmux 2>/dev/null) || bin=$(command -v lyna-tmux 2>/dev/null) || exit 0; case "$bin" in /*) ;; *) exit 0;; esac; exec "$bin" hook --plugin ` + string(regs[i].Event)
			if h.Command != want {
				t.Fatalf("command %q; want %q", h.Command, want)
			}
		})
	}
}

// The event name is interpolated into a shell command, so it must stay a
// plain word.
func TestPluginCommandEventsAreShellWords(t *testing.T) {
	word := regexp.MustCompile(`^[A-Za-z]+$`)
	for _, r := range hookevent.Registrations() {
		if !word.MatchString(string(r.Event)) {
			t.Fatalf("event %q is not a plain word", r.Event)
		}
	}
}

// TestPluginCommandRunsThroughShell executes every hook command the way Claude
// Code runs shell-form hooks (sh -c), with and without lyna-tmux on PATH and
// with the PATH forms a hostile repository can arrange.
func TestPluginCommandRunsThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell available")
	}
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat available")
	}
	withBin := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	fake := "#!" + sh + "\nprintf '%s\\n' \"$@\" > " + tmux.ShellQuote(record+".args") + "\n" +
		tmux.ShellQuote(catPath) + " > " + tmux.ShellQuote(record+".stdin") + "\n"
	writeFake := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, PluginBinary), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFake(t, withBin)
	withoutBin := t.TempDir()
	// A checkout that planted the binary in its own directory, reached
	// through the relative PATH elements a shell profile or a task runner
	// leaves behind.
	planted := t.TempDir()
	writeFake(t, planted)

	cases := []struct {
		name      string
		path      string
		cwd       string
		installed bool
	}{
		{name: "installed", path: withBin, installed: true},
		{name: "not installed", path: withoutBin},
		{name: "dot on path", path: ".", cwd: planted},
		{name: "empty path element", path: ":" + withoutBin, cwd: planted},
		{name: "relative path element", path: "bin", cwd: filepath.Dir(planted)},
		{name: "relative element before an absolute one", path: "." + string(os.PathListSeparator) + withBin, cwd: planted},
	}
	// The relative element case looks the planted binary up as "bin/lmux".
	if err := os.Symlink(planted, filepath.Join(filepath.Dir(planted), "bin")); err != nil {
		t.Fatal(err)
	}
	for _, r := range hookevent.Registrations() {
		for _, tc := range cases {
			t.Run(string(r.Event)+"/"+tc.name, func(t *testing.T) {
				_ = os.Remove(record + ".args")
				_ = os.Remove(record + ".stdin")
				payload := `{"hook_event_name":"` + string(r.Event) + `"}`
				cmd := exec.Command(sh, "-c", PluginCommand(r.Event))
				cmd.Env = []string{"PATH=" + tc.path}
				cmd.Dir = tc.cwd
				cmd.Stdin = strings.NewReader(payload)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("exit: %v (stderr %q)", err, stderr.String())
				}
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Fatalf("hook printed stdout %q stderr %q", stdout.String(), stderr.String())
				}
				args, err := os.ReadFile(record + ".args")
				if !tc.installed {
					if !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("binary ran although not on PATH: %q %v", args, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if want := "hook\n--plugin\n" + string(r.Event) + "\n"; string(args) != want {
					t.Fatalf("argv %q; want %q", args, want)
				}
				stdin, err := os.ReadFile(record + ".stdin")
				if err != nil || string(stdin) != payload {
					t.Fatalf("stdin %q, %v; want the payload", stdin, err)
				}
			})
		}
	}
}

type manifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Author      struct {
		Name string `json:"name"`
	} `json:"author"`
	Repository string   `json:"repository"`
	License    string   `json:"license"`
	Keywords   []string `json:"keywords"`
}

func TestPluginManifest(t *testing.T) {
	data := readRepoFile(t, "plugins/lyna-tmux/.claude-plugin/plugin.json")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// Documented manifest fields only; hooks/hooks.json and themes/ load from
	// their default locations, and naming them again would load them twice.
	allowed := []string{"name", "description", "version", "author", "repository", "license", "keywords"}
	for k := range raw {
		if !slices.Contains(allowed, k) {
			t.Fatalf("unexpected manifest field %q", k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		field string
		got   string
		want  string
	}{
		{"name", m.Name, PluginName},
		{"version", m.Version, "1.0.0"},
		{"author.name", m.Author.Name, "LYNA-IT"},
		{"license", m.License, "MIT"},
		{"repository", m.Repository, "https://github.com/bayoudhdev/lyna-claude-tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q; want %q", tc.field, tc.got, tc.want)
			}
		})
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(m.Name) {
		t.Fatalf("name %q is not kebab-case", m.Name)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(m.Version) {
		t.Fatalf("version %q is not semantic", m.Version)
	}
	if strings.TrimSpace(m.Description) == "" || len(m.Keywords) == 0 {
		t.Fatal("description and keywords are required for discovery")
	}
}

func TestMarketplace(t *testing.T) {
	data := readRepoFile(t, ".claude-plugin/marketplace.json")
	var mp struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Description string `json:"description"`
		Plugins     []struct {
			Name        string `json:"name"`
			Source      string `json:"source"`
			Description string `json:"description"`
			Author      struct {
				Name string `json:"name"`
			} `json:"author"`
			Repository string `json:"repository"`
			License    string `json:"license"`
			Category   string `json:"category"`
		} `json:"plugins"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&mp); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(readRepoFile(t, "plugins/lyna-tmux/.claude-plugin/plugin.json"), &m); err != nil {
		t.Fatal(err)
	}
	if mp.Name != PluginName || mp.Owner.Name != "LYNA-IT" || strings.TrimSpace(mp.Description) == "" {
		t.Fatalf("marketplace header %q %q %q", mp.Name, mp.Owner.Name, mp.Description)
	}
	if len(mp.Plugins) != 1 {
		t.Fatalf("%d plugins; want 1", len(mp.Plugins))
	}
	p := mp.Plugins[0]
	cases := []struct {
		field string
		got   string
		want  string
	}{
		{"name", p.Name, m.Name},
		{"source", p.Source, "./plugins/lyna-tmux"},
		{"description", p.Description, m.Description},
		{"author.name", p.Author.Name, m.Author.Name},
		{"repository", p.Repository, m.Repository},
		{"license", p.License, m.License},
		{"category", p.Category, "productivity"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q; want %q", tc.field, tc.got, tc.want)
			}
		})
	}
	// Relative sources resolve against the marketplace root (the directory
	// holding .claude-plugin/) and must stay inside it.
	source := filepath.Clean(filepath.FromSlash(p.Source))
	if !filepath.IsLocal(source) {
		t.Fatalf("source %q escapes the marketplace root", p.Source)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, source, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("source has no plugin manifest: %v", err)
	}
}
