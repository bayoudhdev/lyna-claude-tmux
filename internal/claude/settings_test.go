package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

func TestWriteSettings(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state", "settings")
	doc := []byte("{\n  \"sandbox\": {\n    \"enabled\": true\n  }\n}\n")

	path, err := WriteSettings(dir, doc)
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	if want := filepath.Join(dir, claudecfg.SettingsFileName(doc)); path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	assertMode(t, dir, fsx.PrivateDir)
	assertMode(t, path, fsx.PrivateFile)
	if got, _ := os.ReadFile(path); string(got) != string(doc) {
		t.Fatalf("content = %q", got)
	}

	// Same content: same path, refreshed modification time, one file.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	again, err := WriteSettings(dir, doc)
	if err != nil || again != path {
		t.Fatalf("second WriteSettings = %s, %v", again, err)
	}
	if info, _ := os.Stat(path); !info.ModTime().After(old) {
		t.Fatal("rewriting did not refresh the modification time")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries", len(entries))
	}

	other, err := WriteSettings(dir, []byte(`{}`))
	if err != nil || other == path {
		t.Fatalf("different content = %s, %v", other, err)
	}
}

func TestWriteSettingsRefuses(t *testing.T) {
	root := t.TempDir()
	doc := []byte(`{"a":1}`)
	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(t.TempDir(), linkedDir); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(root, "file")
	if err := os.WriteFile(fileDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(root, "victim")
	if err := os.WriteFile(linkTarget, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	plantedDir := filepath.Join(root, "planted")
	if err := os.Mkdir(plantedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, filepath.Join(plantedDir, claudecfg.SettingsFileName(doc))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		dir   string
		data  []byte
		errIs error
		text  string
	}{
		{name: "relative dir", dir: "settings", data: doc, text: "must be absolute"},
		{name: "invalid json", dir: filepath.Join(root, "ok"), data: []byte("{"), text: "not valid JSON"},
		{name: "dir is a link", dir: linkedDir, data: doc, errIs: fsx.ErrSymlink},
		{name: "dir is a file", dir: fileDir, data: doc, errIs: fsx.ErrNotDir},
		{name: "settings file is a planted link", dir: plantedDir, data: doc, errIs: fsx.ErrSymlink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteSettings(tc.dir, tc.data)
			if err == nil || (tc.errIs != nil && !errors.Is(err, tc.errIs)) || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("WriteSettings error = %v, want %v containing %q", err, tc.errIs, tc.text)
			}
		})
	}
	if got, _ := os.ReadFile(linkTarget); string(got) != "keep" {
		t.Fatalf("link target was written: %q", got)
	}
}

func TestPruneSettings(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	const (
		fresh    = "aaaaaaaaaaaaaaaa.json"
		stale    = "bbbbbbbbbbbbbbbb.json"
		kept     = "cccccccccccccccc.json"
		staleTmp = ".dddddddddddddddd.json.tmp-12345"
		freshTmp = ".eeeeeeeeeeeeeeee.json.tmp-999"
	)
	cases := []struct {
		name string
		opts PruneOptions
		want []string
	}{
		{name: "age only", opts: PruneOptions{MaxAge: 24 * time.Hour, Now: now}, want: []string{staleTmp, stale, kept}},
		{name: "age with keep", opts: PruneOptions{MaxAge: 24 * time.Hour, Now: now, Keep: []string{"/somewhere/" + kept}}, want: []string{staleTmp, stale}},
		{name: "keep only removes every other generated file", opts: PruneOptions{Keep: []string{kept}}, want: []string{fresh, stale}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			touch := func(name string, age time.Duration) string {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				at := now.Add(-age)
				if err := os.Chtimes(p, at, at); err != nil {
					t.Fatal(err)
				}
				return p
			}
			touch(fresh, time.Hour)
			touch(stale, 72*time.Hour)
			touch(kept, 72*time.Hour)
			touch(staleTmp, 72*time.Hour)
			touch(freshTmp, time.Minute)
			untouched := []string{
				touch("config.toml", 72*time.Hour),
				touch("ffffffffffffffff.json.bak", 72*time.Hour),
				touch("FFFFFFFFFFFFFFFF.json", 72*time.Hour),
			}
			// A stale-looking link and a directory with a generated name are never touched.
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "1111111111111111.json")
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
			subdir := filepath.Join(dir, "2222222222222222.json")
			if err := os.Mkdir(subdir, 0o700); err != nil {
				t.Fatal(err)
			}
			untouched = append(untouched, link, subdir)

			removed, err := PruneSettings(dir, tc.opts)
			if err != nil {
				t.Fatalf("PruneSettings: %v", err)
			}
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(removed, want) {
				t.Fatalf("removed %v, want %v", removed, want)
			}
			for _, name := range tc.want {
				if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("%s still exists", name)
				}
			}
			for _, p := range untouched {
				if _, err := os.Lstat(p); err != nil {
					t.Fatalf("%s was removed: %v", p, err)
				}
			}
			if got, _ := os.ReadFile(victim); string(got) != "keep" {
				t.Fatal("link target changed")
			}
		})
	}
}

func TestPruneSettingsEdges(t *testing.T) {
	root := t.TempDir()
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(t.TempDir(), linked); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	age := PruneOptions{MaxAge: time.Hour}
	cases := []struct {
		name  string
		dir   string
		opts  PruneOptions
		errIs error
		text  string
	}{
		{name: "relative dir", dir: "settings", opts: age, text: "must be absolute"},
		{name: "no criteria", dir: root, opts: PruneOptions{}, text: "keep list or a maximum age"},
		{name: "missing dir is fine", dir: filepath.Join(root, "missing"), opts: age},
		{name: "dir is a link", dir: linked, opts: age, errIs: fsx.ErrSymlink},
		{name: "dir is a file", dir: file, opts: age, errIs: fsx.ErrNotDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := PruneSettings(tc.dir, tc.opts)
			if tc.errIs == nil && tc.text == "" {
				if err != nil || removed != nil {
					t.Fatalf("PruneSettings = %v, %v", removed, err)
				}
				return
			}
			if err == nil || (tc.errIs != nil && !errors.Is(err, tc.errIs)) || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("PruneSettings error = %v, want %v containing %q", err, tc.errIs, tc.text)
			}
		})
	}

	// Now defaults to the current time: a file written just now is young.
	dir := t.TempDir()
	path, err := WriteSettings(dir, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	removed, err := PruneSettings(dir, PruneOptions{MaxAge: time.Hour})
	if err != nil || len(removed) != 0 {
		t.Fatalf("PruneSettings removed a fresh file: %v, %v", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
