package scripts_test

// scripts/check-text.sh walks the files of the repository it runs in, so the
// character cases below run in a throwaway repository holding a single file.
// The guard has to catch every pictograph an author could paste into a README
// or a release note, and leave the box drawing, arrow, geometric and Nerd Font
// characters the status line and the TUI draw with.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// chars renders code points, so a case carries the code point it tests instead
// of hiding it in an escape (and this file stays readable under the guard it
// tests).
func chars(cp ...rune) string { return string(cp) }

// checkText runs scripts/check-text.sh in dir and returns its output and exit
// code.
func checkText(t *testing.T, dir string) (string, int) {
	t.Helper()
	cmd := exec.Command(scriptPath(t, "check-text.sh"))
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		// A contributor's own git settings must not change the walk.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"LC_ALL=C",
	}
	// The guard walks the repository, so it must never read its standard
	// input: an em-dash here is a hit only if it does.
	cmd.Stdin = strings.NewReader("stdin " + chars(0x2014) + "\n")
	out, err := cmd.CombinedOutput()
	exit := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out), exit
}

// textRepo creates a git repository holding the given files.
func textRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git := exec.Command("git", "init", "--quiet", dir)
	git.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheckTextCharacters(t *testing.T) {
	cases := []struct {
		name string
		// text is written to notes.md unless file says otherwise.
		text    string
		file    string
		wantHit bool
	}{
		{name: "plain ASCII", text: "the workspace starts\n"},
		{name: "em-dash U+2014", text: "the workspace " + chars(0x2014) + " starts\n", wantHit: true},
		{name: "rocket U+1F680", text: "ship it " + chars(0x1F680) + "\n", wantHit: true},
		{name: "white heavy check mark U+2705", text: "done " + chars(0x2705) + "\n", wantHit: true},
		{name: "warning sign U+26A0", text: "careful " + chars(0x26A0) + "\n", wantHit: true},
		{name: "warning sign with emoji presentation U+26A0 U+FE0F", text: "careful " + chars(0x26A0, 0xFE0F) + "\n", wantHit: true},
		{name: "white medium star U+2B50", text: "star " + chars(0x2B50) + " it\n", wantHit: true},
		{name: "heavy check mark U+2714", text: "passing " + chars(0x2714) + "\n", wantHit: true},
		{name: "sparkles U+2728", text: "new " + chars(0x2728) + "\n", wantHit: true},
		{name: "cross mark U+274C", text: "failed " + chars(0x274C) + "\n", wantHit: true},
		{name: "party popper U+1F389", text: "released " + chars(0x1F389) + "\n", wantHit: true},
		{name: "regional indicator pair U+1F1EB U+1F1F7", text: "french " + chars(0x1F1EB, 0x1F1F7) + "\n", wantHit: true},
		{name: "skin tone modifier U+1F44D U+1F3FD", text: "nice " + chars(0x1F44D, 0x1F3FD) + "\n", wantHit: true},
		{name: "bare variation selector U+FE0F", text: "hidden" + chars(0xFE0F) + "\n", wantHit: true},

		// Characters the project's own terminal output draws with.
		{name: "box drawing U+2500 U+2502 U+2503", text: chars(0x2500, 0x2502, 0x2503) + "\n"},
		{name: "block elements U+258C U+2588", text: chars(0x258C, 0x2588) + "\n"},
		{name: "box drawing diagonal U+2571", text: chars(0x2571) + "\n"},
		{name: "arrows U+2190 U+2191 U+2192 U+2193", text: chars(0x2190, 0x2191, 0x2192, 0x2193) + "\n"},
		{name: "geometric shapes U+25CF U+25CB U+25C6", text: chars(0x25CF, 0x25CB, 0x25C6) + "\n"},
		{name: "geometric shapes U+25A3 U+25A2 U+25CE U+25B8 U+25BE U+25F7", text: chars(0x25A3, 0x25A2, 0x25CE, 0x25B8, 0x25BE, 0x25F7) + "\n"},
		{name: "alternative key symbol U+2387", text: chars(0x2387) + " picker\n"},
		{name: "teardrop-spoked asterisk U+273B", text: chars(0x273B) + " agent\n"},
		{name: "mathematical operators U+229E U+2261", text: chars(0x229E, 0x2261) + "\n"},
		{name: "ellipsis U+2026 and single guillemet U+203A", text: "waiting" + chars(0x2026, 0x20, 0x203A) + "\n"},
		{name: "plus-minus U+00B1 and middle dot U+00B7", text: "12 " + chars(0x00B1) + " 3 " + chars(0x00B7) + " 4\n"},
		{name: "lambda U+03BB", text: chars(0x03BB) + "\n"},
		{name: "accented latin U+00E9", text: "r" + chars(0x00E9) + "sum" + chars(0x00E9) + "\n"},
		{name: "CJK text U+65E5 U+672C U+8A9E", text: chars(0x65E5, 0x672C, 0x8A9E) + "\n"},
		{name: "Nerd Font private use U+E621 U+F0C9", text: chars(0xE621, 0x20, 0xF0C9) + "\n"},
		{name: "line and paragraph separators U+2028 U+2029", text: chars(0x2028, 0x2029) + "\n"},
		// Typographic marks, not pictographs: a copyright or trademark sign in
		// a notice is legitimate.
		{name: "copyright and trademark U+00A9 U+2122", text: chars(0x00A9) + " 2026 LYNA-IT" + chars(0x2122) + "\n"},

		// Files the walk leaves out.
		{name: "fuzz corpus is skipped", text: chars(0x1F680) + "\n", file: "internal/sanitize/testdata/fuzz/FuzzClean/seed"},
		{name: "binary file is skipped", text: chars(0x00, 0x1F680) + "\n", file: "docs/logo.bin"},
		{name: "empty file is skipped", text: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file := tc.file
			if file == "" {
				file = "notes.md"
			}
			out, exit := checkText(t, textRepo(t, map[string]string{file: tc.text}))
			if tc.wantHit {
				if exit != 1 {
					t.Fatalf("exit %d, want 1: the guard let %q through\n%s", exit, tc.text, out)
				}
				if !strings.Contains(out, "remove em-dashes") {
					t.Fatalf("no summary line:\n%s", out)
				}
				return
			}
			if exit != 0 {
				t.Fatalf("exit %d, want 0: the guard rejected %q\n%s", exit, tc.text, out)
			}
		})
	}
}

// TestCheckTextReportsFileAndLine pins the hit format, which is what a
// contributor uses to find the character, and the line counter restarting on
// every file.
func TestCheckTextReportsFileAndLine(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name:  "line inside one file",
			files: map[string]string{"docs/notes.md": "one\ntwo\nthree " + chars(0x2705) + "\n"},
			want:  []string{"docs/notes.md:3:"},
		},
		{
			name: "every file counts its own lines",
			files: map[string]string{
				"a.md": "one\ntwo\nthree\nfour " + chars(0x2714) + "\n",
				"b.md": "one\ntwo " + chars(0x26A0) + "\n",
			},
			want: []string{"a.md:4:", "b.md:2:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, exit := checkText(t, textRepo(t, tc.files))
			if exit != 1 {
				t.Fatalf("exit %d, want 1:\n%s", exit, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("hit %q is missing:\n%s", want, out)
				}
			}
		})
	}
}

// TestCheckTextEmptyRepository covers the walk finding nothing to scan.
func TestCheckTextEmptyRepository(t *testing.T) {
	out, exit := checkText(t, textRepo(t, nil))
	if exit != 0 {
		t.Fatalf("exit %d, want 0 on a repository with no files:\n%s", exit, out)
	}
}

// TestCheckTextPassesOnTheRepository keeps the widened guard honest: the tree
// the project ships stays clean under it.
func TestCheckTextPassesOnTheRepository(t *testing.T) {
	out, exit := checkText(t, repoRoot(t))
	if exit != 0 {
		t.Fatalf("scripts/check-text.sh exit %d on the repository:\n%s", exit, out)
	}
}
