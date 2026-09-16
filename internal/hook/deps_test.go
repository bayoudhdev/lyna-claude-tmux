package hook

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook/githead"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

func TestRunBranch(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/feat/#1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "cmd", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		event    string
		stdin    string
		read     githead.ReadFunc
		getwd    func() (string, error)
		wantTail []string // commands after the state update, before the bell query
		wantLog  string
	}{
		{
			name: "payload cwd inside repository", event: "SessionStart",
			stdin:    `{"cwd":"` + sub + `","source":"startup"}`,
			read:     githead.ReadFile,
			wantTail: []string{"set-option", "-t", "%3", "@lt_branch", "feat/##1"},
		},
		{
			name: "missing cwd falls back to working directory", event: "SessionStart",
			stdin:    `{"source":"startup"}`,
			read:     githead.ReadFile,
			getwd:    func() (string, error) { return repo, nil },
			wantTail: []string{"set-option", "-t", "%3", "@lt_branch", "feat/##1"},
		},
		{
			name: "relative cwd falls back to working directory", event: "Stop",
			stdin:    `{"cwd":"relative/dir"}`,
			read:     githead.ReadFile,
			getwd:    func() (string, error) { return sub, nil },
			wantTail: []string{"set-option", "-t", "%3", "@lt_branch", "feat/##1"},
		},
		{
			name: "outside any repository clears branch", event: "SessionStart",
			stdin:    `{"cwd":"/nowhere"}`,
			read:     func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist },
			wantTail: []string{"set-option", "-u", "-t", "%3", "@lt_branch"},
		},
		{
			name: "unreadable metadata keeps branch", event: "SessionStart",
			stdin:   `{"cwd":"/locked"}`,
			read:    func(string, int64) ([]byte, error) { return nil, fs.ErrPermission },
			wantLog: "hook SessionStart: branch: permission denied",
		},
		{
			name: "working directory unknown keeps branch", event: "SessionStart",
			stdin:   `{}`,
			read:    githead.ReadFile,
			getwd:   func() (string, error) { return "", errors.New("getwd: gone") },
			wantLog: "hook SessionStart: working directory: getwd: gone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.deps.ReadFile = tc.read
			if tc.getwd != nil {
				h.deps.Getwd = tc.getwd
			}
			h.tmux.result = tmux.Result{Stdout: []byte("/dev/ttys001\n")}
			Run(context.Background(), Input{Event: tc.event, Stdin: strings.NewReader(tc.stdin), Getenv: env(nil)}, h.deps)
			if len(h.tmux.calls) != 1 {
				t.Fatalf("tmux calls %q", h.tmux.calls)
			}
			call := strings.Join(h.tmux.calls[0], "\x00")
			idle := strings.Join([]string{"-S", testSocket, "set-option", "-p", "-t", "%3", "@lt_state", "idle"}, "\x00")
			want := idle
			if tc.wantTail != nil {
				want += "\x00;\x00" + strings.Join(tc.wantTail, "\x00")
			}
			if tc.event == "Stop" {
				want += "\x00;\x00display-message\x00-p\x00-t\x00%3\x00#{pane_tty}"
			}
			if call != want {
				t.Fatalf("tmux call\n%q\nwant\n%q", strings.Split(call, "\x00"), strings.Split(want, "\x00"))
			}
			logged := h.log(t)
			if tc.wantLog == "" && logged != "" || !strings.Contains(logged, tc.wantLog) {
				t.Fatalf("log %q; want %q", logged, tc.wantLog)
			}
		})
	}
}

func TestRunRecoversFromPanics(t *testing.T) {
	h := newHarness(t)
	h.deps.Tmux = func(tmux.Socket) *tmux.Client { panic("factory exploded") }
	status := Run(context.Background(), Input{Event: "UserPromptSubmit", Stdin: strings.NewReader("{}"), Getenv: env(nil)}, h.deps)
	if status != 0 {
		t.Fatalf("status %d", status)
	}
	if logged := h.log(t); !strings.Contains(logged, "hook: internal error: factory exploded") {
		t.Fatalf("log %q", logged)
	}
}

func TestRunNilGetenvIsOutsideTmux(t *testing.T) {
	h := newHarness(t)
	if status := Run(context.Background(), Input{Event: "Stop"}, h.deps); status != 0 {
		t.Fatalf("status %d", status)
	}
	if len(h.tmux.calls) != 0 || h.log(t) != "" {
		t.Fatalf("calls %q log %q", h.tmux.calls, h.log(t))
	}
}

func TestDefaultsFillEveryDependency(t *testing.T) {
	d := Deps{}.withDefaults()
	if d.Tmux == nil || d.Now == nil || d.ReadFile == nil || d.Getwd == nil || d.OpenTTY == nil {
		t.Fatalf("missing defaults: %+v", d)
	}
	c := d.Tmux(tmux.Socket{Path: "/tmp/x"})
	if c.Bin() != "tmux" || c.Socket() != (tmux.Socket{Path: "/tmp/x"}) {
		t.Fatalf("default client %q %+v", c.Bin(), c.Socket())
	}
}

func TestLog(t *testing.T) {
	cases := []struct {
		name    string
		path    func(dir string) string
		prepare func(t *testing.T, dir string)
		lines   []string
		want    string // expected content; "" with nil path means nothing written
	}{
		{
			name:  "creates private state directory",
			path:  func(dir string) string { return filepath.Join(dir, "a", "b", "lyna-tmux.log") },
			lines: []string{"first", "second"},
			want:  "2026-09-15T12:30:00Z hook Stop: first\n2026-09-15T12:30:00Z hook Stop: second\n",
		},
		{
			name:  "control characters folded",
			path:  func(dir string) string { return filepath.Join(dir, "lyna-tmux.log") },
			lines: []string{"bad\x1b]52;c;cGF5bG9hZA==\x07 value\nnext"},
			want:  "2026-09-15T12:30:00Z hook Stop: bad value next\n",
		},
		{
			name: "symbolic link refused",
			path: func(dir string) string { return filepath.Join(dir, "lyna-tmux.log") },
			prepare: func(t *testing.T, dir string) {
				if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "lyna-tmux.log")); err != nil {
					t.Fatal(err)
				}
			},
			lines: []string{"x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			d := Deps{LogPath: tc.path(dir), Now: func() time.Time { return fixedNow }}
			for _, l := range tc.lines {
				d.logf("hook Stop", "%s", l)
			}
			data, err := os.ReadFile(d.LogPath)
			if tc.want == "" {
				if _, statErr := os.Stat(filepath.Join(dir, "target")); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("log followed the symbolic link: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("log %q; want %q", data, tc.want)
			}
			info, err := os.Stat(d.LogPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("log mode %v", info.Mode().Perm())
			}
			dirInfo, err := os.Stat(filepath.Dir(d.LogPath))
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(d.LogPath) != dir && dirInfo.Mode().Perm() != 0o700 {
				t.Fatalf("log directory mode %v", dirInfo.Mode().Perm())
			}
		})
	}
	// Without a path nothing is written anywhere.
	Deps{Now: time.Now}.logf("hook Stop", "dropped")
}

func TestOpenTTY(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		wantErr bool
		notTTY  bool
	}{
		{name: "character device", path: "/dev/null"},
		{name: "outside dev", path: regular, wantErr: true, notTTY: true},
		{name: "empty", path: "", wantErr: true, notTTY: true},
		{name: "relative", path: "dev/null", wantErr: true, notTTY: true},
		{name: "traversal", path: "/dev/../etc/passwd", wantErr: true, notTTY: true},
		{name: "missing device", path: "/dev/lyna-tmux-does-not-exist", wantErr: true},
		{name: "directory", path: "/dev/fd/", wantErr: true, notTTY: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := OpenTTY(tc.path)
			if !tc.wantErr {
				if err != nil {
					t.Fatal(err)
				}
				if err := ringTTY(func(string) (io.WriteCloser, error) { return w, nil }, tc.path); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				_ = w.Close()
				t.Fatalf("OpenTTY(%q) succeeded", tc.path)
			}
			if tc.notTTY && !errors.Is(err, errNotTTY) {
				t.Fatalf("OpenTTY(%q) = %v; want errNotTTY", tc.path, err)
			}
		})
	}
}

type failingWriter struct{ writeErr, closeErr error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.writeErr }
func (f failingWriter) Close() error              { return f.closeErr }

func TestRingTTYErrors(t *testing.T) {
	writeErr, closeErr, openErr := errors.New("write"), errors.New("close"), errors.New("open")
	cases := []struct {
		name string
		open func(string) (io.WriteCloser, error)
		want []error
	}{
		{name: "open", open: func(string) (io.WriteCloser, error) { return nil, openErr }, want: []error{openErr}},
		{name: "write", open: func(string) (io.WriteCloser, error) { return failingWriter{writeErr: writeErr}, nil }, want: []error{writeErr}},
		{name: "close", open: func(string) (io.WriteCloser, error) { return failingWriter{closeErr: closeErr}, nil }, want: []error{closeErr}},
		{name: "both", open: func(string) (io.WriteCloser, error) {
			return failingWriter{writeErr: writeErr, closeErr: closeErr}, nil
		}, want: []error{writeErr, closeErr}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ringTTY(tc.open, "/dev/pts/1")
			for _, w := range tc.want {
				if !errors.Is(err, w) {
					t.Fatalf("ringTTY = %v; want %v", err, w)
				}
			}
		})
	}
}

func TestChangesFiles(t *testing.T) {
	var got []string
	for _, tool := range []string{"Edit", "MultiEdit", "Write", "NotebookEdit", "Read", "Bash", "", "edit", "Edit|Write"} {
		if changesFiles(tool) {
			got = append(got, tool)
		}
	}
	if want := []string{"Edit", "MultiEdit", "Write", "NotebookEdit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file-changing tools %q; want %q", got, want)
	}
}
