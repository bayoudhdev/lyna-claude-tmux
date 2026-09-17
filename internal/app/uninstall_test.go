package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Completion script headers as the CLI generates them, one per shell.
const (
	uninstallBashHeader = "# bash completion V2 for lyna-tmux                            -*- shell-script -*-\n\n__lyna-tmux_debug()\n"
	uninstallZshHeader  = "#compdef lyna-tmux\ncompdef _lyna-tmux lyna-tmux\n"
	uninstallFishHeader = "# fish completion for lyna-tmux                            -*- shell-script -*-\n\nfunction __lyna_tmux_debug\n"
)

// uninstallTmuxConf is a user tmux configuration with the plugin manager
// entry, the source-file line of plugin mode and lines of their own.
const uninstallTmuxConf = "set -g mouse on\n" +
	"set -g @plugin 'bayoudhdev/lyna-claude-tmux'\n" +
	"# unrelated\n" +
	"source-file ~/.local/state/lyna-tmux/plugin/plugin.tmux.conf\n"

// uninstallRegistry is a Claude Code plugin registry that lists the plugins
// named, in the registry's own shape.
func uninstallRegistry(ids ...string) string {
	plugins := map[string][]map[string]string{}
	for _, id := range ids {
		plugins[id] = []map[string]string{{"scope": "user"}}
	}
	data, err := json.Marshal(map[string]any{"version": 2, "plugins": plugins})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// uninstallFixture is what uninstallHome laid out: the paths the plan must
// list, in the order Apply removes them, and the steps it must leave.
type uninstallFixture struct {
	root        string
	exe         string
	themes      []string
	completions []string
	manual      []UninstallManual
}

// uninstallHome fills a temporary home with lyna-tmux directories in the XDG
// layout, Claude theme files, completion scripts, the binary, traces in the
// user's tmux configuration and Claude plugin registry, and bystander files
// that must survive.
func uninstallHome(t *testing.T) uninstallFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	palette, err := theme.Get(config.Default().UI.Theme)
	if err != nil {
		t.Fatal(err)
	}
	data, err := claudetheme.Generate(palette)
	if err != nil {
		t.Fatal(err)
	}
	themes := filepath.Join(root, ".claude", "themes")
	for path, content := range map[string]string{
		".local/state/lyna-tmux/tmux.conf":                    "conf",
		".local/state/lyna-tmux/settings/a.json":              "{}",
		".cache/lyna-tmux/agents.json":                        "[]",
		".local/share/lyna-tmux/review/codediff/VERSION":      "2.1.0",
		".config/lyna-tmux/config.toml":                       "[ui]\n",
		".local/state/other-tool/log":                         "keep",
		".local/share/lyna-tmux-notes/keep.txt":               "keep",
		".config/other/config":                                "keep",
		".tmux.conf":                                          uninstallTmuxConf,
		".claude/settings.json":                               "{}",
		".claude/plugins/installed_plugins.json":              uninstallRegistry("other@other", "lyna-tmux@lyna-tmux"),
		".claude/themes/lyna-" + palette.Name + ".json":       string(data),
		".claude/themes/lyna-edited.json":                     uninstallEdited(t, data),
		".claude/themes/mine.json":                            `{"name": "mine"}`,
		".claude/themes/copy-of-lyna.json":                    string(data),
		"elsewhere/lyna-linked.json":                          string(data),
		".local/share/bash-completion/completions/lyna-tmux":  uninstallBashHeader,
		".local/share/bash-completion/completions/other-tool": "# bash completion V2 for other-tool\n",
		".zfunc/_lyna-tmux":                                   uninstallZshHeader,
		".config/fish/completions/lyna-tmux.fish":             "function __mine\nend\n",
		".local/bin/lyna-tmux":                                "#!/bin/sh\nexit 0\n",
	} {
		initWrite(t, filepath.Join(root, filepath.FromSlash(path)), content, 0o600)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere", "lyna-linked.json"), filepath.Join(themes, "lyna-linked.json")); err != nil {
		t.Fatal(err)
	}
	fish := filepath.Join(root, ".config", "fish", "completions", "lyna-tmux.fish")
	tmuxConf := filepath.Join(root, ".tmux.conf")
	return uninstallFixture{
		root:        root,
		exe:         filepath.Join(root, ".local", "bin", "lyna-tmux"),
		themes:      []string{filepath.Join(themes, "copy-of-lyna.json"), filepath.Join(themes, "lyna-"+palette.Name+".json")},
		completions: []string{filepath.Join(root, ".local", "share", "bash-completion", "completions", "lyna-tmux"), filepath.Join(root, ".zfunc", "_lyna-tmux")},
		manual: []UninstallManual{
			{Step: "remove " + fish + " if it is lyna-tmux's: it is not the script `lyna-tmux completion` writes", How: "rm " + fish},
			{Step: "in Claude Code, remove the companion plugin", How: "/plugin uninstall lyna-tmux@lyna-tmux"},
			{Step: "remove line 2 of " + tmuxConf, How: "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"},
			{Step: "remove line 4 of " + tmuxConf, How: "source-file ~/.local/state/lyna-tmux/plugin/plugin.tmux.conf"},
		},
	}
}

// uninstallEdited returns a generated theme file whose user changed its name.
func uninstallEdited(t *testing.T, data []byte) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["name"] = "My edit"
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// uninstallWalk lists every path below root.
func uninstallWalk(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for rel := range initSnapshot(t, root) {
		out[rel] = true
	}
	return out
}

func TestUninstall(t *testing.T) {
	bin := tmuxtest.Require(t)
	cases := []struct {
		name        string
		purge       bool
		wantRemoved []string
		wantKept    string
	}{
		{name: "keeps the configuration", wantRemoved: []string{".local/state/lyna-tmux", ".cache/lyna-tmux", ".local/share/lyna-tmux"}, wantKept: ".config/lyna-tmux"},
		{name: "purge removes the configuration", purge: true, wantRemoved: []string{".local/state/lyna-tmux", ".cache/lyna-tmux", ".local/share/lyna-tmux", ".config/lyna-tmux"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := uninstallHome(t)
			root := fx.root
			target := tmuxtest.Start(t)
			bystander := tmuxtest.Start(t)
			env := map[string]string{"HOME": root, session.EnvSocketName: target.Name, "TMUX": "/tmp/outer,1,0"}
			h := Host{Getenv: func(k string) string { return env[k] }, Home: root, Exe: fx.exe, TmuxBin: bin}

			p, err := UninstallPlanFor(h, tc.purge)
			if err != nil {
				t.Fatal(err)
			}
			var wantDirs []string
			for _, rel := range tc.wantRemoved {
				wantDirs = append(wantDirs, filepath.Join(root, filepath.FromSlash(rel)))
			}
			wantKept := ""
			if tc.wantKept != "" {
				wantKept = filepath.Join(root, filepath.FromSlash(tc.wantKept))
			}
			if p.SocketName != target.Name || p.ServerSocket != SocketPath(h.Getenv, target.Name) || p.Empty() {
				t.Fatalf("plan %+v does not stop the running server %s", p, target.Name)
			}
			if !slices.Equal(p.Dirs, wantDirs) || !slices.Equal(p.Themes, fx.themes) || !slices.Equal(p.Completions, fx.completions) || p.KeptConfig != wantKept || p.Binary != fx.exe {
				t.Fatalf("plan %+v\nwant dirs %q themes %q completions %q kept %q binary %q", p, wantDirs, fx.themes, fx.completions, wantKept, fx.exe)
			}
			if !slices.Equal(p.Manual, fx.manual) {
				t.Fatalf("manual %+v\nwant %+v", p.Manual, fx.manual)
			}

			before := uninstallWalk(t, root)
			ctx := tmuxtest.Context(t)
			res, err := p.Apply(ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			// The binary is last: everything else is gone before it is touched.
			wantRemoved := slices.Concat(wantDirs, fx.themes, fx.completions, []string{fx.exe})
			if !res.ServerStopped || !slices.Equal(res.Removed, wantRemoved) {
				t.Fatalf("result %+v\nwant removed %q", res, wantRemoved)
			}
			if _, err := target.Client.Run(ctx, "list-sessions"); !errors.Is(err, tmux.ErrNoServer) {
				t.Fatalf("lyna-tmux server still answers: %v", err)
			}
			if _, err := bystander.Client.Run(ctx, "has-session", "-t", "base"); err != nil {
				t.Fatalf("another tmux server was stopped: %v", err)
			}

			after := uninstallWalk(t, root)
			removed := map[string]bool{}
			for rel := range before {
				if !after[rel] {
					removed[rel] = true
				}
			}
			var gone []string
			for _, path := range slices.Concat(fx.themes, fx.completions, []string{fx.exe}) {
				gone = append(gone, strings.TrimPrefix(path, root+"/"))
			}
			want := map[string]bool{}
			for rel := range before {
				for _, g := range slices.Concat(tc.wantRemoved, gone) {
					if rel == filepath.FromSlash(g) || strings.HasPrefix(rel, filepath.FromSlash(g)+string(filepath.Separator)) {
						want[rel] = true
					}
				}
			}
			if !maps.Equal(removed, want) {
				t.Fatalf("removed %v\nwant %v", slices.Sorted(maps.Keys(removed)), slices.Sorted(maps.Keys(want)))
			}
			for rel := range after {
				if !before[rel] {
					t.Fatalf("uninstall created %s", rel)
				}
			}

			// The second plan has nothing left to stop or remove, only the
			// steps that were the user's from the start.
			again, err := UninstallPlanFor(h, tc.purge)
			if err != nil || !again.Empty() || !slices.Equal(again.Manual, fx.manual) {
				t.Fatalf("second plan %+v, %v", again, err)
			}
			if res, err := again.Apply(ctx, h); err != nil || res.ServerStopped || len(res.Removed) != 0 {
				t.Fatalf("second apply %+v, %v", res, err)
			}
		})
	}
}

func TestUninstallBinary(t *testing.T) {
	uid := os.Getuid()
	cases := []struct {
		name string
		// setup lays out the file and returns the path to classify.
		setup      func(t *testing.T, root string) string
		uid        int
		wantRemove bool
		wantStep   string
		wantHow    string
	}{
		{
			name:  "absent is nothing to do",
			setup: func(_ *testing.T, root string) string { return filepath.Join(root, "bin", "lyna-tmux") },
			uid:   uid,
		},
		{
			name:  "empty path is nothing to do",
			setup: func(*testing.T, string) string { return "" },
			uid:   uid,
		},
		{
			name: "a regular file the user owns in a writable directory",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, ".local", "bin", "lyna-tmux")
				initWrite(t, path, "#!/bin/sh\n", 0o700)
				return path
			},
			uid: uid, wantRemove: true,
		},
		{
			name: "in the Homebrew cellar",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "opt", "homebrew", "Cellar", "lyna-tmux", "1.0.0", "bin", "lyna-tmux")
				initWrite(t, path, "#!/bin/sh\n", 0o700)
				return path
			},
			uid:      uid,
			wantStep: "Homebrew installed it", wantHow: "brew uninstall lyna-tmux",
		},
		{
			name: "a link into the Homebrew cellar",
			setup: func(t *testing.T, root string) string {
				target := filepath.Join(root, "opt", "homebrew", "Cellar", "lyna-tmux", "1.0.0", "bin", "lyna-tmux")
				initWrite(t, target, "#!/bin/sh\n", 0o700)
				link := filepath.Join(root, "opt", "homebrew", "bin", "lyna-tmux")
				mkdir(t, filepath.Dir(link))
				if err := os.Symlink("../Cellar/lyna-tmux/1.0.0/bin/lyna-tmux", link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			uid:      uid,
			wantStep: "it is a link to ", wantHow: "brew uninstall lyna-tmux",
		},
		{
			name: "a link to a file of the user's",
			setup: func(t *testing.T, root string) string {
				target := filepath.Join(root, "src", "lyna-tmux")
				initWrite(t, target, "#!/bin/sh\n", 0o700)
				link := filepath.Join(root, "bin", "lyna-tmux")
				mkdir(t, filepath.Dir(link))
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			uid:      uid,
			wantStep: "it is a link to ", wantHow: "rm ",
		},
		{
			name: "owned by another user",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "bin", "lyna-tmux")
				initWrite(t, path, "#!/bin/sh\n", 0o700)
				return path
			},
			uid:      uid + 1,
			wantStep: "another user owns it", wantHow: "sudo rm ",
		},
		{
			name: "in a directory the user cannot write",
			setup: func(t *testing.T, root string) string {
				if uid == 0 {
					t.Skip("root writes everywhere")
				}
				path := filepath.Join(root, "bin", "lyna-tmux")
				initWrite(t, path, "#!/bin/sh\n", 0o700)
				if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
				return path
			},
			uid:      uid,
			wantStep: "you cannot write to ", wantHow: "sudo rm ",
		},
		{
			name: "a directory in place of the file",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "bin", "lyna-tmux")
				mkdir(t, path)
				return path
			},
			uid:      uid,
			wantStep: "it is not a regular file", wantHow: "rm ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			exe := tc.setup(t, root)
			before := initSnapshot(t, root)
			remove, manual, err := uninstallBinary(exe, tc.uid)
			if err != nil {
				t.Fatal(err)
			}
			if (remove != "") != tc.wantRemove || (remove != "" && remove != exe) {
				t.Fatalf("remove %q, want removal %v of %q", remove, tc.wantRemove, exe)
			}
			if (manual != nil) != (tc.wantStep != "") {
				t.Fatalf("manual %+v, want a step %v", manual, tc.wantStep != "")
			}
			if manual != nil {
				if !strings.HasPrefix(manual.Step, "remove the binary "+exe+": "+tc.wantStep) || !strings.HasPrefix(manual.How, tc.wantHow) {
					t.Fatalf("manual %+v\nwant step %q and how %q", manual, tc.wantStep, tc.wantHow)
				}
			}
			if !maps.Equal(before, initSnapshot(t, root)) {
				t.Fatal("classifying the binary changed the directory")
			}
		})
	}
}

func TestUninstallManaged(t *testing.T) {
	cases := []struct {
		name string
		path string
		how  string
	}{
		{name: "apple silicon homebrew", path: "/opt/homebrew/Cellar/lyna-tmux/1.0.0/bin/lyna-tmux", how: "brew uninstall lyna-tmux"},
		{name: "intel homebrew", path: "/usr/local/Cellar/lyna-tmux/1.0.0/bin/lyna-tmux", how: "brew uninstall lyna-tmux"},
		{name: "linuxbrew", path: "/home/linuxbrew/.linuxbrew/Cellar/lyna-tmux/1.0.0/bin/lyna-tmux", how: "brew uninstall lyna-tmux"},
		{name: "nix store", path: "/nix/store/abc-lyna-tmux-1.0.0/bin/lyna-tmux", how: "nix profile remove lyna-tmux"},
		{name: "system prefix", path: "/usr/bin/lyna-tmux", how: "sudo rm /usr/bin/lyna-tmux"},
		{name: "usr local", path: "/usr/local/bin/lyna-tmux", how: "sudo rm /usr/local/bin/lyna-tmux"},
		{name: "a home directory named like the nix store", path: "/home/nix/store/lyna-tmux"},
		{name: "a user bin directory", path: "/home/u/.local/bin/lyna-tmux"},
		{name: "a go bin directory", path: "/home/u/go/bin/lyna-tmux"},
		{name: "the homebrew bin directory itself", path: "/opt/homebrew/bin/lyna-tmux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := uninstallManaged(tc.path)
			if ok != (tc.how != "") || m.how != tc.how || (ok && m.reason == "") {
				t.Fatalf("uninstallManaged(%q) = %+v, %v; want how %q", tc.path, m, ok, tc.how)
			}
		})
	}
}

func TestUninstallGeneratedCompletion(t *testing.T) {
	cases := []struct {
		name    string
		content string
		link    bool
		want    bool
	}{
		{name: "bash", content: uninstallBashHeader, want: true},
		{name: "zsh", content: uninstallZshHeader, want: true},
		{name: "fish", content: uninstallFishHeader, want: true},
		{name: "bash without the editor marker", content: "# bash completion V2 for lyna-tmux\n", want: true},
		{name: "a script for another program", content: "# bash completion V2 for lyna-tmux-other\n"},
		{name: "a script of the user's", content: "function __mine\nend\n"},
		{name: "the older bash generator", content: "# bash completion for lyna-tmux\n"},
		{name: "empty", content: ""},
		{name: "a link to a generated script", content: uninstallZshHeader, link: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "_lyna-tmux")
			if tc.link {
				initWrite(t, filepath.Join(root, "elsewhere"), tc.content, 0o600)
				if err := os.Symlink(filepath.Join(root, "elsewhere"), path); err != nil {
					t.Fatal(err)
				}
			} else {
				initWrite(t, path, tc.content, 0o600)
			}
			if got := uninstallGeneratedCompletion(path); got != tc.want {
				t.Fatalf("uninstallGeneratedCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUninstallCompletionPaths(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "the paths setup names",
			want: []string{"/home/u/.local/share/bash-completion/completions/lyna-tmux", "/home/u/.zfunc/_lyna-tmux", "/home/u/.config/fish/completions/lyna-tmux.fish"},
		},
		{
			name: "moved by the xdg variables",
			env:  map[string]string{"XDG_DATA_HOME": "/srv/data", "XDG_CONFIG_HOME": "/srv/cfg"},
			want: []string{
				"/home/u/.local/share/bash-completion/completions/lyna-tmux", "/srv/data/bash-completion/completions/lyna-tmux",
				"/home/u/.zfunc/_lyna-tmux",
				"/home/u/.config/fish/completions/lyna-tmux.fish", "/srv/cfg/fish/completions/lyna-tmux.fish",
			},
		},
		{
			name: "relative xdg variables are ignored",
			env:  map[string]string{"XDG_DATA_HOME": "data", "XDG_CONFIG_HOME": "cfg"},
			want: []string{"/home/u/.local/share/bash-completion/completions/lyna-tmux", "/home/u/.zfunc/_lyna-tmux", "/home/u/.config/fish/completions/lyna-tmux.fish"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uninstallCompletionPaths(func(k string) string { return tc.env[k] }, "/home/u")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("paths %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestUninstallClaudePluginInstalled(t *testing.T) {
	cases := []struct {
		name string
		// content is the registry; nil leaves it absent.
		content *string
		want    bool
	}{
		{name: "no registry", want: false},
		{name: "listed", content: ptr(uninstallRegistry("lyna-tmux@lyna-tmux")), want: true},
		{name: "listed among others", content: ptr(uninstallRegistry("a@b", "lyna-tmux@lyna-tmux", "c@d")), want: true},
		{name: "not listed", content: ptr(uninstallRegistry("a@b")), want: false},
		{name: "no plugin at all", content: ptr(uninstallRegistry()), want: false},
		{name: "another shape", content: ptr(`{"version": 3, "installed": ["lyna-tmux@lyna-tmux"]}`), want: true},
		{name: "not json", content: ptr("{"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.content != nil {
				initWrite(t, filepath.Join(home, "plugins", "installed_plugins.json"), *tc.content, 0o600)
			}
			if got := uninstallClaudePluginInstalled(home); got != tc.want {
				t.Fatalf("installed = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestUninstallTmuxConfLines(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		env   map[string]string
		want  []UninstallManual
	}{
		{name: "no configuration"},
		{name: "a configuration without lyna-tmux", files: map[string]string{".tmux.conf": "set -g mouse on\n"}},
		{
			name:  "the plugin manager entry",
			files: map[string]string{".tmux.conf": "set -g mouse on\nset -g @plugin 'bayoudhdev/lyna-claude-tmux'\n"},
			want:  []UninstallManual{{Step: "remove line 2 of $HOME/.tmux.conf", How: "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"}},
		},
		{
			name:  "the plugin manager entry with double quotes and indentation",
			files: map[string]string{".tmux.conf": "  set -g @plugin \"bayoudhdev/lyna-claude-tmux\"  \n"},
			want:  []UninstallManual{{Step: "remove line 1 of $HOME/.tmux.conf", How: "set -g @plugin \"bayoudhdev/lyna-claude-tmux\""}},
		},
		{
			name:  "the source-file line of plugin mode in the xdg configuration",
			files: map[string]string{".config/tmux/tmux.conf": "source-file /home/u/.local/state/lyna-tmux/plugin/plugin.tmux.conf\n"},
			want:  []UninstallManual{{Step: "remove line 1 of $HOME/.config/tmux/tmux.conf", How: "source-file /home/u/.local/state/lyna-tmux/plugin/plugin.tmux.conf"}},
		},
		{
			name:  "both files",
			files: map[string]string{".tmux.conf": "set -g @plugin 'bayoudhdev/lyna-claude-tmux'\n", ".config/tmux/tmux.conf": "# lyna-tmux keys\n"},
			want: []UninstallManual{
				{Step: "remove line 1 of $HOME/.tmux.conf", How: "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"},
				{Step: "remove line 1 of $HOME/.config/tmux/tmux.conf", How: "# lyna-tmux keys"},
			},
		},
		{
			name:  "the xdg variable moves the configuration",
			files: map[string]string{"cfg/tmux/tmux.conf": "set -g @plugin 'bayoudhdev/lyna-claude-tmux'\n", ".config/tmux/tmux.conf": "set -g @plugin 'bayoudhdev/lyna-claude-tmux'\n"},
			env:   map[string]string{"XDG_CONFIG_HOME": "$HOME/cfg"},
			want:  []UninstallManual{{Step: "remove line 1 of $HOME/cfg/tmux/tmux.conf", How: "set -g @plugin 'bayoudhdev/lyna-claude-tmux'"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			expand := func(s string) string { return strings.ReplaceAll(s, "$HOME", home) }
			for rel, content := range tc.files {
				initWrite(t, filepath.Join(home, filepath.FromSlash(rel)), content, 0o600)
			}
			before := initSnapshot(t, home)
			got, err := uninstallTmuxConfLines(func(k string) string { return expand(tc.env[k]) }, home)
			if err != nil {
				t.Fatal(err)
			}
			var want []UninstallManual
			for _, m := range tc.want {
				want = append(want, UninstallManual{Step: expand(m.Step), How: m.How})
			}
			if !slices.Equal(got, want) {
				t.Fatalf("lines %+v\nwant %+v", got, want)
			}
			if !maps.Equal(before, initSnapshot(t, home)) {
				t.Fatal("reading the tmux configuration changed it")
			}
		})
	}
}

func TestUninstallRefusals(t *testing.T) {
	bin := tmuxtest.Require(t)
	t.Run("from a pane of the server it stops", func(t *testing.T) {
		fx := uninstallHome(t)
		root := fx.root
		target := tmuxtest.Start(t)
		env := map[string]string{"HOME": root, session.EnvSocketName: target.Name}
		env["TMUX"] = SocketPath(func(k string) string { return env[k] }, target.Name) + ",1,0"
		h := Host{Getenv: func(k string) string { return env[k] }, Home: root, Exe: fx.exe, TmuxBin: bin}
		p, err := UninstallPlanFor(h, true)
		if err != nil {
			t.Fatal(err)
		}
		before := initSnapshot(t, root)
		ctx := tmuxtest.Context(t)
		if _, err := p.Apply(ctx, h); !errors.Is(err, ErrUninstallInside) || !strings.Contains(err.Error(), "outside lyna-tmux") {
			t.Fatalf("err = %v", err)
		}
		if _, err := target.Client.Run(ctx, "has-session", "-t", "base"); err != nil {
			t.Fatalf("server stopped: %v", err)
		}
		if !maps.Equal(before, initSnapshot(t, root)) {
			t.Fatal("refused uninstall removed files")
		}
	})
	t.Run("invalid socket name", func(t *testing.T) {
		env := map[string]string{"HOME": t.TempDir(), session.EnvSocketName: "bad/name"}
		h := Host{Getenv: func(k string) string { return env[k] }, Home: env["HOME"]}
		if _, err := UninstallPlanFor(h, false); err == nil || !strings.Contains(err.Error(), session.EnvSocketName) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown home", func(t *testing.T) {
		h := Host{Getenv: func(string) string { return "" }}
		if _, err := UninstallPlanFor(h, false); err == nil {
			t.Fatal("no error without a home")
		}
	})
	t.Run("default socket name", func(t *testing.T) {
		env := map[string]string{"HOME": t.TempDir()}
		h := Host{Getenv: func(k string) string { return env[k] }, Home: env["HOME"]}
		p, err := UninstallPlanFor(h, false)
		if err != nil || p.SocketName != tmux.DefaultSocketName || len(p.Dirs) != 0 || p.KeptConfig != "" {
			t.Fatalf("plan %+v, %v", p, err)
		}
	})
	// A home lyna-tmux never wrote to, with a binary that is already gone,
	// gives a plan with nothing to stop, remove or do by hand.
	t.Run("nothing to do", func(t *testing.T) {
		env := map[string]string{"HOME": t.TempDir(), session.EnvSocketName: "lt-" + filepath.Base(t.TempDir())}
		h := Host{Getenv: func(k string) string { return env[k] }, Home: env["HOME"], Exe: filepath.Join(env["HOME"], "bin", "lyna-tmux")}
		p, err := UninstallPlanFor(h, true)
		if err != nil || !p.Empty() || len(p.Manual) != 0 || p.ServerSocket != "" {
			t.Fatalf("plan %+v, %v", p, err)
		}
	})
	// The binary is removed after everything else and classified again just
	// before, so a binary that stopped being the user's to remove between
	// the plan and the removal fails the run with everything else done.
	t.Run("the binary becomes a link between the plan and the removal", func(t *testing.T) {
		fx := uninstallHome(t)
		env := map[string]string{"HOME": fx.root, session.EnvSocketName: "lt-" + filepath.Base(fx.root), "TMUX": "/tmp/outer,1,0"}
		h := Host{Getenv: func(k string) string { return env[k] }, Home: fx.root, Exe: fx.exe, TmuxBin: bin}
		p, err := UninstallPlanFor(h, true)
		if err != nil || p.Binary != fx.exe {
			t.Fatalf("plan %+v, %v", p, err)
		}
		target := filepath.Join(fx.root, "elsewhere", "lyna-tmux")
		initWrite(t, target, "#!/bin/sh\n", 0o700)
		if err := os.Remove(fx.exe); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fx.exe); err != nil {
			t.Fatal(err)
		}
		res, err := p.Apply(tmuxtest.Context(t), h)
		if err == nil || !strings.Contains(err.Error(), "it is a link to "+target) {
			t.Fatalf("err = %v, want the link refused", err)
		}
		if _, err := os.Lstat(fx.exe); err != nil {
			t.Fatalf("the link was removed: %v", err)
		}
		if _, err := os.Lstat(target); err != nil {
			t.Fatalf("the link target was removed: %v", err)
		}
		wantRemoved := slices.Concat([]string{
			filepath.Join(fx.root, ".local", "state", "lyna-tmux"), filepath.Join(fx.root, ".cache", "lyna-tmux"),
			filepath.Join(fx.root, ".local", "share", "lyna-tmux"), filepath.Join(fx.root, ".config", "lyna-tmux"),
		}, fx.themes, fx.completions)
		if !slices.Equal(res.Removed, wantRemoved) {
			t.Fatalf("removed %q\nwant everything but the binary %q", res.Removed, wantRemoved)
		}
	})
	// Under LYNA_TMUX_HOME the four roots are plain names the user may have
	// been using for years, so each one is removed only when everything in it
	// is something lyna-tmux writes.
	ownedFiles := []string{
		"state/tmux.conf", "state/settings/a.json", "state/lyna-tmux.log", "state/plugin/lyna-tmux.conf",
		"cache/agents.json", "data/review/codediff/VERSION", "config/config.toml", "config/tmux.local.conf",
	}
	lynaHome := func(t *testing.T, files []string) Host {
		t.Helper()
		root := t.TempDir()
		for _, rel := range files {
			initWrite(t, filepath.Join(root, filepath.FromSlash(rel)), "x", 0o600)
		}
		env := map[string]string{"HOME": root, "LYNA_TMUX_HOME": root}
		return Host{Getenv: func(k string) string { return env[k] }, Home: root}
	}
	t.Run("LYNA_TMUX_HOME layout", func(t *testing.T) {
		h := lynaHome(t, append(slices.Clone(ownedFiles), "notes/keep"))
		root := h.Home
		p, err := UninstallPlanFor(h, true)
		want := []string{filepath.Join(root, "state"), filepath.Join(root, "cache"), filepath.Join(root, "data"), filepath.Join(root, "config")}
		if err != nil || !slices.Equal(p.Dirs, want) {
			t.Fatalf("plan %+v, %v", p, err)
		}
	})
	t.Run("LYNA_TMUX_HOME over a directory lyna-tmux did not fill", func(t *testing.T) {
		cases := []struct {
			name    string
			files   []string
			purge   bool
			wantErr string
		}{
			{name: "a document in the state root", files: []string{"state/notes.md"}, wantErr: "state/notes.md is not something lyna-tmux created"},
			{name: "a mail directory in the cache root", files: []string{"cache/mail/inbox"}, wantErr: "cache/mail is not something lyna-tmux created"},
			{name: "a project in the data root", files: []string{"data/photos/a.jpg"}, wantErr: "data/photos is not something lyna-tmux created"},
			{name: "a dotfile in the configuration root", files: []string{"config/config.toml", "config/nvim/init.lua"}, purge: true, wantErr: "config/nvim is not something lyna-tmux created"},
			{name: "the configuration root is not read without purge", files: []string{"config/nvim/init.lua"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := lynaHome(t, tc.files)
				before := initSnapshot(t, h.Home)
				p, err := UninstallPlanFor(h, tc.purge)
				if tc.wantErr == "" {
					if err != nil {
						t.Fatalf("plan %+v, %v", p, err)
					}
					return
				}
				if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), tc.wantErr) {
					t.Fatalf("err = %v, want it to name %q", err, tc.wantErr)
				}
				if !maps.Equal(before, initSnapshot(t, h.Home)) {
					t.Fatal("a refused plan changed the directory")
				}
			})
		}
	})
	t.Run("a directory that fills up between the plan and the removal", func(t *testing.T) {
		h := lynaHome(t, ownedFiles)
		p, err := UninstallPlanFor(h, true)
		if err != nil {
			t.Fatal(err)
		}
		initWrite(t, filepath.Join(h.Home, "config", "nvim", "init.lua"), "x", 0o600)
		res, err := p.Apply(tmuxtest.Context(t), Host{Getenv: h.Getenv, Home: h.Home, TmuxBin: bin})
		if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), "config/nvim is not something lyna-tmux created") {
			t.Fatalf("err = %v, want the second check to refuse the configuration root", err)
		}
		if _, err := os.Lstat(filepath.Join(h.Home, "config", "nvim", "init.lua")); err != nil {
			t.Fatalf("the refused directory was removed anyway: %v", err)
		}
		if !slices.Contains(res.Removed, filepath.Join(h.Home, "state")) {
			t.Fatalf("removed %q, want the state directory to have been removed first", res.Removed)
		}
	})
}

func TestUninstallCheckDir(t *testing.T) {
	// The directories are absent, so only the ownership rule decides here:
	// what the layout resolves to, never a name that merely looks right.
	xdgHost := Host{Getenv: func(string) string { return "" }, Home: "/home/u"}
	lynaEnv := map[string]string{"LYNA_TMUX_HOME": "/srv/lt"}
	lynaHost := Host{Getenv: func(k string) string { return lynaEnv[k] }, Home: "/home/u"}
	cases := []struct {
		name string
		host Host
		dir  string
		ok   bool
	}{
		{name: "xdg state", host: xdgHost, dir: "/home/u/.local/state/lyna-tmux", ok: true},
		{name: "xdg config", host: xdgHost, dir: "/home/u/.config/lyna-tmux", ok: true},
		{name: "xdg state with a trailing slash", host: xdgHost, dir: "/home/u/.local/state/lyna-tmux/", ok: true},
		{name: "another application's directory of the same name", host: xdgHost, dir: "/opt/lyna-tmux", ok: false},
		{name: "the xdg parent", host: xdgHost, dir: "/home/u/.local/state", ok: false},
		{name: "lyna home state", host: lynaHost, dir: "/srv/lt/state", ok: true},
		{name: "lyna home root", host: lynaHost, dir: "/srv/lt", ok: false},
		{name: "a sibling of the lyna home roots", host: lynaHost, dir: "/srv/lt/notes", ok: false},
		{name: "the xdg layout while lyna home is set", host: lynaHost, dir: "/home/u/.local/state/lyna-tmux", ok: false},
		{name: "another root's child", host: lynaHost, dir: "/srv/other/state", ok: false},
		{name: "the home directory", host: xdgHost, dir: "/home/u", ok: false},
		{name: "the filesystem root", host: xdgHost, dir: "/", ok: false},
		{name: "a relative path", host: xdgHost, dir: "relative/lyna-tmux", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := uninstallCheckDir(tc.host, tc.dir); (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

// TestUninstallAwaitStopped pins the wait between kill-server and the report
// that the server stopped: the socket may answer for a few event loop turns
// after the acknowledgement, and a server still answering at the deadline is
// an error, not a stop.
func TestUninstallAwaitStopped(t *testing.T) {
	cases := []struct {
		name string
		// answers is what the probe reports on each call; the last value
		// repeats once the sequence is used up.
		answers    []bool
		ctxTimeout time.Duration
		wantProbe  int
		wantErr    error
	}{
		{name: "already gone", answers: []bool{false}, wantProbe: 1},
		{name: "gone after a few turns", answers: []bool{true, true, true, false}, wantProbe: 4},
		{name: "still answering at the deadline", answers: []bool{true}, ctxTimeout: 5 * uninstallStopPoll, wantErr: context.DeadlineExceeded},
		{name: "context canceled first", answers: []bool{true}, ctxTimeout: -1, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			switch {
			case tc.ctxTimeout < 0:
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case tc.ctxTimeout > 0:
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.ctxTimeout)
				defer cancel()
			}
			probes := 0
			answers := func(socket string) bool {
				if socket != "/tmp/tmux-0/lt" {
					t.Fatalf("probed %q", socket)
				}
				probes++
				return tc.answers[min(probes, len(tc.answers))-1]
			}
			err := uninstallAwaitStopped(ctx, "/tmp/tmux-0/lt", answers)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if !strings.Contains(err.Error(), "still answering after kill-server") || probes == 0 {
					t.Fatalf("err = %v after %d probes", err, probes)
				}
				return
			}
			if probes != tc.wantProbe {
				t.Fatalf("%d probes, want %d", probes, tc.wantProbe)
			}
		})
	}
}
