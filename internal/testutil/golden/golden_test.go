package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder captures failures so the helper's own failure paths can be tested
// without failing the enclosing test.
type recorder struct {
	testing.TB
	failed bool
	msg    string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
}

func TestAssert(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		golden   string
		got      string
		update   bool
		wantFail string
		wantFile string
	}{
		{name: "match", files: map[string]string{"conf/a.txt": "one\ntwo\n"}, golden: "conf/a.txt", got: "one\ntwo\n"},
		{name: "mismatch names first differing line", files: map[string]string{"a.txt": "one\ntwo\nthree\n"}, golden: "a.txt", got: "one\nTWO\nthree\n", wantFail: "first difference at line 2"},
		{name: "missing file explains update", golden: "absent.txt", got: "x", wantFail: EnvUpdate + "=1"},
		{name: "update creates nested file", golden: "new/dir/b.json", got: "{}\n", update: true, wantFile: "{}\n"},
		{name: "update overwrites", files: map[string]string{"c.txt": "old"}, golden: "c.txt", got: "new", update: true, wantFile: "new"},
		{name: "rejects parent escape", golden: "../outside.txt", got: "x", wantFail: "invalid golden name"},
		{name: "rejects absolute", golden: "/etc/passwd", got: "x", wantFail: "invalid golden name"},
		{name: "rejects empty", golden: "", got: "x", wantFail: "invalid golden name"},
		{name: "update refuses escape", golden: "a/../../x", got: "x", update: true, wantFail: "invalid golden name"},
		{name: "read error is reported", files: map[string]string{"dir/inner.txt": "x"}, golden: "dir", got: "x", wantFail: "golden:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for name, body := range tc.files {
				path := filepath.Join("testdata", filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.update {
				t.Setenv(EnvUpdate, "1")
			} else {
				t.Setenv(EnvUpdate, "")
			}

			rec := &recorder{TB: t}
			Assert(rec, tc.golden, []byte(tc.got))

			if tc.wantFail == "" && rec.failed {
				t.Fatalf("unexpected failure: %s", rec.msg)
			}
			if tc.wantFail != "" && (!rec.failed || !strings.Contains(rec.msg, tc.wantFail)) {
				t.Fatalf("failure = %v %q, want message containing %q", rec.failed, rec.msg, tc.wantFail)
			}
			if tc.wantFile != "" {
				data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(tc.golden)))
				if err != nil || string(data) != tc.wantFile {
					t.Fatalf("file = %q, %v; want %q", data, err, tc.wantFile)
				}
			}
		})
	}
}

func TestAssertUpdateWriteError(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvUpdate, "1")
	// A regular file where the parent directory should be makes MkdirAll fail.
	if err := os.WriteFile("testdata", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{TB: t}
	Assert(rec, "a/b.txt", []byte("x"))
	if !rec.failed {
		t.Fatal("expected failure when testdata cannot be created")
	}

	if err := os.Remove("testdata"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join("testdata", "dir.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec = &recorder{TB: t}
	Assert(rec, "dir.txt", []byte("x"))
	if !rec.failed {
		t.Fatal("expected failure when the golden path is a directory")
	}
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name  string
		want  string
		got   string
		empty bool
		parts []string
	}{
		{name: "equal", want: "a\nb", got: "a\nb", empty: true},
		{name: "changed line", want: "a\nb\nc", got: "a\nX\nc", parts: []string{"line 2", `want >    2 | "b"`, `got  >    2 | "X"`}},
		{name: "got longer", want: "a", got: "a\nb", parts: []string{"line 2", "want >    2 | <end of file>", `got  >    2 | "b"`}},
		{name: "want longer", want: "a\nb", got: "a", parts: []string{"got  >    2 | <end of file>"}},
		{name: "context is bounded", want: "1\n2\n3\n4\n5\n6\n7\n8\n9", got: "1\n2\n3\n4\n5\n6\nX\n8\n9", parts: []string{`    4 | "4"`, `    9 | "9"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Diff([]byte(tc.want), []byte(tc.got))
			if tc.empty != (d == "") {
				t.Fatalf("Diff empty = %v, want %v:\n%s", d == "", tc.empty, d)
			}
			for _, p := range tc.parts {
				if !strings.Contains(d, p) {
					t.Errorf("Diff missing %q:\n%s", p, d)
				}
			}
			if tc.name == "context is bounded" && strings.Contains(d, `    3 | "3"`) {
				t.Errorf("Diff shows more than %d lines of leading context:\n%s", contextLines, d)
			}
		})
	}
}

func TestPath(t *testing.T) {
	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{name: "a.txt", want: filepath.Join("testdata", "a.txt")},
		{name: "x/y/z.golden", want: filepath.Join("testdata", "x", "y", "z.golden")},
		{name: "x/./y", want: filepath.Join("testdata", "x", "y")},
		{name: "x/../y", want: filepath.Join("testdata", "y")},
		{name: "..", wantErr: true},
		{name: "../a", wantErr: true},
		{name: "/abs", wantErr: true},
		{name: `a\b`, wantErr: true},
		{name: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Path(tc.name)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("Path(%q) = %q, %v; want %q, err %v", tc.name, got, err, tc.want, tc.wantErr)
			}
		})
	}
}
