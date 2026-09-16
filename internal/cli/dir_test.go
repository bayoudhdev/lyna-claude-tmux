package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandDir(t *testing.T) {
	getwd := func() (string, error) { return "/work/api", nil }
	cases := []struct{ name, dir, want string }{
		{name: "empty is the working directory", dir: "", want: "/work/api"},
		{name: "home alone", dir: "~", want: "/home/dev"},
		{name: "under home", dir: "~/src/api", want: "/home/dev/src/api"},
		{name: "home is not a prefix match", dir: "~user/src", want: "/work/api/~user/src"},
		{name: "relative", dir: "../web", want: "/work/web"},
		{name: "absolute", dir: "/opt/tools/", want: "/opt/tools"},
		{name: "absolute with a dot segment", dir: "/opt/tools/../lib", want: "/opt/lib"},
		{name: "dot", dir: ".", want: "/work/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandDir(tc.dir, "/home/dev", getwd)
			if err != nil || got != tc.want {
				t.Fatalf("expandDir(%q) = %q, %v; want %q", tc.dir, got, err, tc.want)
			}
		})
	}

	t.Run("an absolute path never asks for the working directory", func(t *testing.T) {
		got, err := expandDir("/opt/tools", "/home/dev", func() (string, error) {
			t.Fatal("the working directory was read for an absolute path")
			return "", nil
		})
		if err != nil || got != "/opt/tools" {
			t.Fatalf("expandDir = %q, %v", got, err)
		}
	})
	t.Run("a working directory that cannot be read is reported", func(t *testing.T) {
		boom := errors.New("boom")
		if _, err := expandDir("src", "/home/dev", func() (string, error) { return "", boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}

func TestExistingDir(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	getwd := func() (string, error) { return root, nil }
	cases := []struct {
		name, dir, want, wantErr string
	}{
		{name: "an existing directory", dir: root, want: root},
		{name: "a relative directory", dir: ".", want: root},
		{name: "a directory with spaces around it", dir: "  " + root + "  ", want: root},
		{name: "empty", wantErr: "a directory is required"},
		{name: "only spaces", dir: "   ", wantErr: "a directory is required"},
		{name: "missing", dir: filepath.Join(root, "nope"), wantErr: "no such directory"},
		{name: "a file", dir: file, wantErr: "is not a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := existingDir(tc.dir, "/home/dev", getwd)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("existingDir(%q) = %q, %v; want %q", tc.dir, got, err, tc.want)
			}
		})
	}
}
