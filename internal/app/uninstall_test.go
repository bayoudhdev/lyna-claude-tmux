package app

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// uninstallHome fills a temporary home with lyna-tmux directories in the XDG
// layout, Claude theme files and bystander files that must survive.
func uninstallHome(t *testing.T) (root string, generated []string) {
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
		".local/state/lyna-tmux/tmux.conf":               "conf",
		".local/state/lyna-tmux/settings/a.json":         "{}",
		".cache/lyna-tmux/agents.json":                   "[]",
		".local/share/lyna-tmux/review/codediff/VERSION": "2.1.0",
		".config/lyna-tmux/config.toml":                  "[ui]\n",
		".local/state/other-tool/log":                    "keep",
		".local/share/lyna-tmux-notes/keep.txt":          "keep",
		".config/other/config":                           "keep",
		".tmux.conf":                                     "keep",
		".claude/settings.json":                          "{}",
		".claude/themes/lyna-" + palette.Name + ".json":  string(data),
		".claude/themes/lyna-edited.json":                uninstallEdited(t, data),
		".claude/themes/mine.json":                       `{"name": "mine"}`,
		".claude/themes/copy-of-lyna.json":               string(data),
		"elsewhere/lyna-linked.json":                     string(data),
	} {
		initWrite(t, filepath.Join(root, filepath.FromSlash(path)), content, 0o600)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere", "lyna-linked.json"), filepath.Join(themes, "lyna-linked.json")); err != nil {
		t.Fatal(err)
	}
	generated = []string{filepath.Join(themes, "copy-of-lyna.json"), filepath.Join(themes, "lyna-"+palette.Name+".json")}
	return root, generated
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
			root, generated := uninstallHome(t)
			target := tmuxtest.Start(t)
			bystander := tmuxtest.Start(t)
			env := map[string]string{"HOME": root, session.EnvSocketName: target.Name, "TMUX": "/tmp/outer,1,0"}
			h := Host{Getenv: func(k string) string { return env[k] }, Home: root, Exe: "/opt/bin/lyna-tmux", TmuxBin: bin}

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
			if p.SocketName != target.Name || !slices.Equal(p.Dirs, wantDirs) || !slices.Equal(p.Themes, generated) || p.KeptConfig != wantKept || p.Binary != h.Exe {
				t.Fatalf("plan %+v\nwant dirs %q themes %q kept %q", p, wantDirs, generated, wantKept)
			}

			before := uninstallWalk(t, root)
			ctx := tmuxtest.Context(t)
			res, err := p.Apply(ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			if !res.ServerStopped || !slices.Equal(res.Removed, slices.Concat(wantDirs, generated)) {
				t.Fatalf("result %+v", res)
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
			want := map[string]bool{}
			for rel := range before {
				for _, gone := range slices.Concat(tc.wantRemoved, []string{".claude/themes/copy-of-lyna.json", strings.TrimPrefix(generated[1], root+"/")}) {
					if rel == filepath.FromSlash(gone) || strings.HasPrefix(rel, filepath.FromSlash(gone)+string(filepath.Separator)) {
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

			again, err := UninstallPlanFor(h, tc.purge)
			if err != nil || len(again.Dirs) != 0 || len(again.Themes) != 0 {
				t.Fatalf("second plan %+v, %v", again, err)
			}
			if res, err := again.Apply(ctx, h); err != nil || res.ServerStopped || len(res.Removed) != 0 {
				t.Fatalf("second apply %+v, %v", res, err)
			}
		})
	}
}

func TestUninstallRefusals(t *testing.T) {
	bin := tmuxtest.Require(t)
	t.Run("from a pane of the server it stops", func(t *testing.T) {
		root, _ := uninstallHome(t)
		target := tmuxtest.Start(t)
		env := map[string]string{"HOME": root, session.EnvSocketName: target.Name}
		env["TMUX"] = SocketPath(func(k string) string { return env[k] }, target.Name) + ",1,0"
		h := Host{Getenv: func(k string) string { return env[k] }, Home: root, TmuxBin: bin}
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
