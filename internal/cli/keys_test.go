package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/app"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// offlineEnv runs commands that need no tmux server against a temporary
// lyna-tmux home. Interactive programs are recorded, and onRun stands in for
// what they would do.
type offlineEnv struct {
	root  string
	env   map[string]string
	runs  [][]string
	onRun func(argv []string) error
}

func newOfflineEnv(t *testing.T) *offlineEnv {
	t.Helper()
	root := t.TempDir()
	return &offlineEnv{root: root, env: map[string]string{"LYNA_TMUX_HOME": root}}
}

func (e *offlineEnv) configFile() string { return filepath.Join(e.root, "config", "config.toml") }

func (e *offlineEnv) writeConfig(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.configFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.configFile(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *offlineEnv) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	host := app.Host{Getenv: func(k string) string { return e.env[k] }, Home: e.root, TmuxBin: filepath.Join(e.root, "no-tmux")}
	var out, errOut bytes.Buffer
	cmd := NewRootWith(Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, Deps{
		Host: func() (app.Host, error) { return host, nil },
		Run: func(_ context.Context, argv []string, _ Streams) error {
			e.runs = append(e.runs, argv)
			if e.onRun != nil {
				return e.onRun(argv)
			}
			return nil
		},
	})
	code = run(t.Context(), cmd, args)
	return code, out.String(), errOut.String()
}

// runOffline runs one command against a fresh home holding the given config
// file (none when config is empty).
func runOffline(t *testing.T, config string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	e := newOfflineEnv(t)
	if config != "" {
		e.writeConfig(t, config)
	}
	return e.run(t, args...)
}

func TestKeysCLI(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		args     []string
		wantCode int
		golden   string
		outHas   []string
		outLacks []string
		errHas   []string
	}{
		{name: "defaults", args: []string{"keys"}, golden: "keys/default.txt"},
		{name: "alt keys off", config: "[ui]\nalt_keys = false\n", args: []string{"keys"}, golden: "keys/no-alt.txt"},
		{
			name: "custom prefix", config: "[workspace]\nprefix = \"C-a\"\n", args: []string{"keys"},
			outHas:   []string{"After the prefix Ctrl+a", "Ctrl+a   ", "Send the prefix key to the pane"},
			outLacks: []string{"twice"},
		},
		{name: "prefix shared with Claude", args: []string{"keys"}, outHas: []string{"background task, page up in transcript (press Ctrl+b twice, the prefix takes it first)"}},
		{name: "windows collapsed", args: []string{"keys"}, outHas: []string{"Alt+1..9", "Window 1 to 9"}, outLacks: []string{"Alt+2 ", "Window 2\n"}},
		{name: "invalid config", config: "[ui]\ntheme = \"nope\"\n", args: []string{"keys"}, wantCode: 1, errHas: []string{"theme"}},
		{name: "rejects arguments", args: []string{"keys", "extra"}, wantCode: 1, errHas: []string{"unknown command"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runOffline(t, tc.config, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout, stderr)
			}
			if tc.golden != "" {
				golden.Assert(t, tc.golden, []byte(stdout))
			}
			for _, s := range tc.outHas {
				if !strings.Contains(stdout, s) {
					t.Fatalf("stdout missing %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.outLacks {
				if strings.Contains(stdout, s) {
					t.Fatalf("stdout has %q:\n%s", s, stdout)
				}
			}
			for _, s := range tc.errHas {
				if !containsFolded(stderr, s) {
					t.Fatalf("stderr missing %q:\n%s", s, stderr)
				}
			}
		})
	}
}

func TestKeysJSON(t *testing.T) {
	cases := []struct {
		name          string
		config        string
		wantPrefix    string
		wantWorkspace int
	}{
		{name: "defaults", wantPrefix: "C-b", wantWorkspace: len(keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"}))},
		{name: "alt keys off", config: "[ui]\nalt_keys = false\n", wantPrefix: "C-b", wantWorkspace: len(keys.Defaults(keys.Options{Prefix: "C-b"}))},
		{name: "custom prefix", config: "[workspace]\nprefix = \"C-a\"\n", wantPrefix: "C-a", wantWorkspace: len(keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-a"}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runOffline(t, tc.config, "keys", "--json")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			var r keysReport
			if err := json.Unmarshal([]byte(stdout), &r); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, stdout)
			}
			if r.Prefix != tc.wantPrefix || len(r.Workspace) != tc.wantWorkspace || len(r.Claude) != len(keys.ClaudeReserved) {
				t.Fatalf("prefix %q workspace %d claude %d", r.Prefix, len(r.Workspace), len(r.Claude))
			}
			if r.Conflicts == nil || len(r.Conflicts) != 0 {
				t.Fatalf("conflicts %q, want empty array", r.Conflicts)
			}
			for _, e := range r.Workspace {
				if e.Key == "" || e.Human == "" || e.Help == "" || e.Table == "" || e.Action == "" || e.Group == "" {
					t.Fatalf("incomplete entry %+v", e)
				}
				if e.Human != keys.Human(e.Key) {
					t.Fatalf("human %q for %q", e.Human, e.Key)
				}
			}
		})
	}
}

func TestWriteKeysConflicts(t *testing.T) {
	var out bytes.Buffer
	r := keysReport{Prefix: "C-b", Conflicts: []string{"M-b is reserved"}}
	if err := writeKeys(&out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Conflicts\n    M-b is reserved") {
		t.Fatalf("output:\n%s", out.String())
	}
	out.Reset()
	r.Conflicts = nil
	if err := writeKeys(&out, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Conflicts") {
		t.Fatalf("empty conflicts printed:\n%s", out.String())
	}
}

func TestCollapseWindows(t *testing.T) {
	win := func(n string) keyEntry {
		return keyEntry{Key: "M-" + n, Human: "Alt+" + n, Table: "root", Action: string(keys.ActionWindow), Arg: n, Help: "Window " + n}
	}
	other := keyEntry{Key: "M-n", Human: "Alt+n", Table: "root", Action: string(keys.ActionNewWindow), Help: "New window"}
	tree := keyEntry{Key: "M-e", Human: "Alt+e", Table: "root", Action: string(keys.ActionTree), Help: "Tree"}
	prefix := keyEntry{Key: "d", Human: "d", Table: "prefix", Action: string(keys.ActionDetach), Help: "Detach"}
	cases := []struct {
		name  string
		in    []keyEntry
		table keys.Table
		want  []string
	}{
		{name: "range kept in place", in: []keyEntry{other, win("1"), win("2"), win("3"), tree, prefix}, table: keys.TableRoot, want: []string{"Alt+n", "Alt+1..3 Window 1 to 3", "Alt+e"}},
		{name: "single window unchanged", in: []keyEntry{win("1"), other}, table: keys.TableRoot, want: []string{"Alt+1 Window 1", "Alt+n"}},
		{name: "other table filtered", in: []keyEntry{win("1"), win("2"), prefix}, table: keys.TablePrefix, want: []string{"d"}},
		{name: "empty", table: keys.TableRoot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, e := range collapseWindows(tc.in, tc.table) {
				s := e.Human
				if e.Action == string(keys.ActionWindow) {
					s += " " + e.Help
				}
				got = append(got, s)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
