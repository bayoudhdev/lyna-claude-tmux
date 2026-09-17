// Package tmuxtest starts disposable tmux servers for tests.
//
// Each server listens on its own randomly named socket, loads no user
// configuration and is killed when the test ends, so tests never touch a
// developer's running tmux.
package tmuxtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Server is a running isolated tmux server.
type Server struct {
	Client *tmux.Client
	Name   string
	Bin    string
}

// Require skips the test when tmux is not installed or tests run with -short,
// and returns the tmux path.
func Require(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs tmux; skipped with -short")
	}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	return bin
}

// Start launches an isolated server with one detached session named "base"
// running a long sleep, and registers cleanup.
func Start(t testing.TB) *Server {
	t.Helper()
	bin := Require(t)
	name := Socket(t, bin)
	client := tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}, Config: "/dev/null"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.Run(ctx, "new-session", "-d", "-s", "base", "-x", "200", "-y", "50", "sleep 3600"); err != nil {
		t.Fatalf("start tmux server: %v", err)
	}
	return &Server{Client: tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}}), Name: name, Bin: bin}
}

// Nested is an isolated server with a real attached client: the client runs in
// a pane of a second isolated server, so tests can type keys (including Alt
// keys and mouse sequences) into the client and observe the inner server.
type Nested struct {
	// Inner is the server under test; its session "main" has one attached client.
	Inner *Server
	// Outer hosts the client in its session "term".
	Outer *Server
}

// StartNested starts an inner server from conf (a configuration file path,
// or "/dev/null") with a session "main" in dir, and attaches a client to it
// from a pane of an outer server sized cols x rows. The test fails if the
// client does not attach within the test step deadline.
func StartNested(t testing.TB, conf, dir string, cols, rows int) *Nested {
	t.Helper()
	bin := Require(t)
	inner := newServer(t, bin, conf)
	ctx := Context(t)
	if _, err := inner.Client.Run(ctx, "new-session", "-d", "-s", "main", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows-1), "-c", tmux.FormatEscape(dir)); err != nil {
		t.Fatalf("start inner tmux server: %v", err)
	}
	outer := newServer(t, bin, "/dev/null")
	// The client draws the inner server for the test to read, so it declares
	// UTF-8 the way every client of this CLI does: a machine with no locale
	// would otherwise get an underscore for each icon of the status line.
	attach := tmux.ShellJoin(bin, "-L", inner.Name, "-u", "attach-session", "-t", "=main")
	if _, err := outer.Client.Run(ctx, "new-session", "-d", "-s", "term", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), attach); err != nil {
		t.Fatalf("start outer tmux server: %v", err)
	}
	WaitFor(t, "client attached", func() bool {
		out, err := inner.Client.Run(ctx, "list-clients", "-F", "#{client_name}")
		return err == nil && strings.TrimSpace(out) != ""
	})
	return &Nested{Inner: inner, Outer: outer}
}

// Keys types tmux key names into the attached client.
func (n *Nested) Keys(t testing.TB, keys ...string) {
	t.Helper()
	if _, err := n.Outer.Client.Run(Context(t), "send-keys", append([]string{"-t", "=term:"}, keys...)...); err != nil {
		t.Fatalf("send keys %v: %v", keys, err)
	}
}

// Literal types text into the attached client without key name lookup.
func (n *Nested) Literal(t testing.TB, s string) {
	t.Helper()
	if _, err := n.Outer.Client.Run(Context(t), "send-keys", "-l", "-t", "=term:", s); err != nil {
		t.Fatalf("send literal: %v", err)
	}
}

// Screen returns what the client currently shows.
func (n *Nested) Screen(t testing.TB) string {
	t.Helper()
	out, err := n.Outer.Client.Run(Context(t), "capture-pane", "-p", "-t", "=term:")
	if err != nil {
		t.Fatalf("capture client screen: %v", err)
	}
	return out
}

// WaitScreen waits until the client screen contains want, or with absent set,
// until it no longer does.
func (n *Nested) WaitScreen(t testing.TB, want string, absent bool) {
	t.Helper()
	var screen string
	what := "screen to show " + strconv.Quote(want)
	if absent {
		what = "screen to stop showing " + strconv.Quote(want)
	}
	if !poll(func() bool {
		screen = n.Screen(t)
		return strings.Contains(screen, want) != absent
	}) {
		t.Fatalf("timed out waiting for %s; screen:\n%s", what, screen)
	}
}

// Click sends an SGR mouse press and release of button (0 left, 2 right) at
// the zero-based cell x, y of the client.
func (n *Nested) Click(t testing.TB, button, x, y int) {
	t.Helper()
	pos := fmt.Sprintf("%d;%d;%d", button, x+1, y+1)
	n.Literal(t, "\x1b[<"+pos+"M\x1b[<"+pos+"m")
}

// WaitFor polls cond until it holds, failing the test after the poll
// deadline. It is for conditions with no event to wait on (a client
// attaching, a menu being drawn).
func WaitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	if !poll(cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// pollTimeout bounds every poll; generous so loaded CI runners do not flake.
const pollTimeout = 10 * time.Second

func poll(cond func() bool) bool {
	deadline := time.Now().Add(pollTimeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newServer(t testing.TB, bin, conf string) *Server {
	t.Helper()
	name := Socket(t, bin)
	client := tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}, Config: conf})
	return &Server{Client: client, Name: name, Bin: bin}
}

// SocketPrefix starts every socket name the package hands out.
const SocketPrefix = "lt-test-"

// Socket returns a fresh random socket name and registers cleanup that kills
// any server listening on it and removes the socket file, which tmux leaves
// behind after kill-server.
func Socket(t testing.TB, bin string) string {
	t.Helper()
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	name := SocketPrefix + hex.EncodeToString(buf[:])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Name: name}}).Run(ctx, "kill-server")
		if err := RemoveSocket(name); err != nil {
			t.Errorf("remove tmux socket: %v", err)
		}
	})
	return name
}

// SocketPath is where tmux creates the socket for a -L name.
func SocketPath(name string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), name)
}

// RemoveSocket deletes the socket file of a test server. Only names with
// SocketPrefix are accepted and only a socket is removed, so a mistake can
// never delete another file or a developer's own server socket.
func RemoveSocket(name string) error {
	if !strings.HasPrefix(name, SocketPrefix) || strings.ContainsRune(name, '/') {
		return fmt.Errorf("refusing to remove socket %q", name)
	}
	path := SocketPath(name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove %s: not a socket", path)
	}
	return os.Remove(path)
}

// contextTimeout bounds a tmux call so a server that never answers fails the
// test instead of hanging it. It is several poll timeouts long on purpose: a
// test that holds one context while it polls would otherwise go blind when
// the context expires, and every call after that answers with an error the
// poll cannot tell from a screen that has not changed yet.
const contextTimeout = 6 * pollTimeout

// Context returns a context bounded for one test step.
func Context(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	t.Cleanup(cancel)
	return ctx
}
