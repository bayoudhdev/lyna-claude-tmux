// Package golden compares generated artifacts (tmux configuration, settings
// JSON, devcontainer files, rendered frames) with reviewed files under the
// calling package's testdata directory.
//
// Files are rewritten only when EnvUpdate is "1", so a plain `go test ./...`
// never changes them and every regeneration shows up in the diff:
//
//	LYNA_TMUX_UPDATE_GOLDEN=1 go test ./internal/tmux/
package golden

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnvUpdate set to "1" makes Assert write the golden file instead of
// comparing against it. An environment variable works across every package
// in one `go test ./...` run, unlike a per-package flag.
const EnvUpdate = "LYNA_TMUX_UPDATE_GOLDEN"

// contextLines is how many lines around the first difference a failure shows.
const contextLines = 3

// Assert compares got with testdata/<name>, where name is a slash-separated
// path relative to testdata. On mismatch it fails the test with the first
// differing line and its surroundings.
func Assert(t testing.TB, name string, got []byte) {
	t.Helper()
	path, err := Path(name)
	if err != nil {
		t.Fatalf("golden: %v", err)
		return
	}
	if os.Getenv(EnvUpdate) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden: %v", err)
			return
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("golden: %s does not exist; create it with %s=1 and review it", path, EnvUpdate)
		return
	}
	if err != nil {
		t.Fatalf("golden: %v", err)
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden: %s differs (regenerate with %s=1 and review the diff)\n%s", path, EnvUpdate, Diff(want, got))
	}
}

// Path maps a golden name to its file under testdata, rejecting names that
// would escape it.
func Path(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return "", fmt.Errorf("invalid golden name %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("invalid golden name %q", name)
	}
	return filepath.Join("testdata", clean), nil
}

// Diff describes the first line where want and got differ, with a few lines
// of context from both sides. It returns "" when they are equal.
func Diff(want, got []byte) string {
	if bytes.Equal(want, got) {
		return ""
	}
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	i := 0
	for i < len(wl) && i < len(gl) && wl[i] == gl[i] {
		i++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "first difference at line %d (want %d lines, got %d lines)\n", i+1, len(wl), len(gl))
	writeSide(&b, "want", wl, i)
	writeSide(&b, "got ", gl, i)
	return b.String()
}

func writeSide(b *strings.Builder, label string, lines []string, at int) {
	from := max(at-contextLines, 0)
	to := min(at+contextLines+1, len(lines))
	for n := from; n < to; n++ {
		marker := " "
		if n == at {
			marker = ">"
		}
		fmt.Fprintf(b, "%s %s %4d | %q\n", label, marker, n+1, lines[n])
	}
	if at >= len(lines) {
		fmt.Fprintf(b, "%s > %4d | <end of file>\n", label, at+1)
	}
}
