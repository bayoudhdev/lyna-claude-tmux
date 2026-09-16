package tmuxtest

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// shortTmpdir points TMUX_TMPDIR at a fresh short directory: unix socket paths
// are limited to about 100 bytes and test temp directories are long on macOS.
func shortTmpdir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ltt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	sockets := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(sockets, 0o700); err != nil {
		t.Fatal(err)
	}
	return sockets
}

func TestRemoveSocket(t *testing.T) {
	cases := []struct {
		name string
		// make creates what sits at the socket path.
		make    func(t *testing.T, path string)
		sock    string
		wantErr string
		gone    bool
	}{
		{
			name: "removes a socket", sock: "lt-test-aa", gone: true,
			make: func(t *testing.T, path string) {
				t.Helper()
				l, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				l.(*net.UnixListener).SetUnlinkOnClose(false)
				_ = l.Close()
			},
		},
		{name: "missing is fine", sock: "lt-test-bb", gone: true},
		{
			name: "refuses a regular file", sock: "lt-test-cc", wantErr: "not a socket",
			make: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{name: "refuses another prefix", sock: "default", wantErr: "refusing"},
		{name: "refuses a path", sock: "lt-test-/../default", wantErr: "refusing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTmpdir(t)
			path := filepath.Join(dir, tc.sock)
			if SocketPath(tc.sock) != path {
				t.Fatalf("SocketPath %q, want %q", SocketPath(tc.sock), path)
			}
			if tc.make != nil {
				tc.make(t, path)
			}
			err := RemoveSocket(tc.sock)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err %v, want %q", err, tc.wantErr)
			}
			if _, statErr := os.Lstat(path); tc.gone != errors.Is(statErr, fs.ErrNotExist) && tc.make != nil {
				t.Fatalf("gone %v, stat %v", tc.gone, statErr)
			}
		})
	}
}

// TestSocketCleanup starts a real server on a Socket name and checks the
// cleanup kills it and leaves no socket file.
func TestSocketCleanup(t *testing.T) {
	bin := Require(t)
	dir := shortTmpdir(t)
	var name string
	t.Run("server", func(t *testing.T) {
		name = Socket(t, bin)
		if !strings.HasPrefix(name, SocketPrefix) {
			t.Fatalf("name %q", name)
		}
		c := tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}, Config: "/dev/null"})
		if _, err := c.Run(Context(t), "new-session", "-d", "sleep 3600"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("socket not created under TMUX_TMPDIR: %v", err)
		}
	})
	if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("socket file left behind: %v", err)
	}
	c := tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}})
	if _, err := c.Run(context.Background(), "list-sessions"); err == nil {
		t.Fatal("server still running")
	}
}
