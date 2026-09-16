package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recorder struct {
	bin  string
	args [][]string
	res  Result
	err  error
}

func (r *recorder) Exec(_ context.Context, bin string, args []string) (Result, error) {
	r.bin = bin
	r.args = append(r.args, append([]string(nil), args...))
	return r.res, r.err
}

func TestClientArgv(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		cmds []Command
		want []string
	}{
		{
			name: "default socket single command",
			cmds: []Command{{"list-sessions"}},
			want: []string{"tmux", "list-sessions"},
		},
		{
			name: "named socket with config",
			opts: Options{Bin: "/opt/tmux", Socket: Socket{Name: "lyna-tmux"}, Config: "/state/tmux.conf"},
			cmds: []Command{{"new-session", "-d", "-s", "api"}},
			want: []string{"/opt/tmux", "-L", "lyna-tmux", "-f", "/state/tmux.conf", "new-session", "-d", "-s", "api"},
		},
		{
			name: "path socket wins over name",
			opts: Options{Socket: Socket{Name: "x", Path: "/tmp/s"}},
			cmds: []Command{{"ls"}},
			want: []string{"tmux", "-S", "/tmp/s", "ls"},
		},
		{
			name: "batch separators and escaped data",
			cmds: []Command{
				{"set-option", "-p", "-t", "%1", "@lt_state", "busy;"},
				{},
				{"display-message", "-p", ";"},
			},
			want: []string{"tmux", "set-option", "-p", "-t", "%1", "@lt_state", `busy\;`, ";", "display-message", "-p", `\;`},
		},
		{
			name: "leading empty command adds no separator",
			cmds: []Command{{}, {"ls"}},
			want: []string{"tmux", "ls"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := New(tc.opts).Argv(tc.cmds...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Argv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientBatchErrors(t *testing.T) {
	cases := []struct {
		name         string
		res          Result
		execErr      error
		wantOut      string
		wantNoServer bool
		wantNotFound bool
		wantMissing  bool
		wantExists   bool
		wantErr      bool
	}{
		{name: "success", res: Result{Stdout: []byte("ok\n")}, wantOut: "ok\n"},
		{name: "no server", res: Result{Stderr: []byte("no server running on /tmp/tmux-501/lyna-tmux\n"), ExitCode: 1}, wantNoServer: true, wantErr: true},
		{name: "socket missing", res: Result{Stderr: []byte("error connecting to /tmp/tmux-501/x (No such file or directory)"), ExitCode: 1}, wantNoServer: true, wantErr: true},
		{name: "session not found", res: Result{Stderr: []byte("can't find session: api"), ExitCode: 1}, wantNotFound: true, wantErr: true},
		{name: "pane not found", res: Result{Stderr: []byte("can't find pane: %99"), ExitCode: 1}, wantNotFound: true, wantErr: true},
		{name: "duplicate session", res: Result{Stderr: []byte("duplicate session: api"), ExitCode: 1}, wantExists: true, wantErr: true},
		{name: "other failure", res: Result{Stderr: []byte("unknown command: nope"), ExitCode: 1}, wantErr: true},
		{name: "option on a gone pane", res: Result{Stderr: []byte("no such pane: %99"), ExitCode: 1}, wantErr: true, wantNotFound: true},
		{name: "option on a gone session", res: Result{Stderr: []byte("no such session: $99"), ExitCode: 1}, wantErr: true, wantNotFound: true},
		{name: "not installed", execErr: ErrNotInstalled, wantMissing: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{res: tc.res, err: tc.execErr}
			out, err := New(Options{Executor: rec}).Run(context.Background(), "list-sessions")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if out != tc.wantOut {
				t.Fatalf("out = %q, want %q", out, tc.wantOut)
			}
			if got := errors.Is(err, ErrNoServer); got != tc.wantNoServer {
				t.Fatalf("Is(ErrNoServer) = %v", got)
			}
			if got := errors.Is(err, ErrNotFound); got != tc.wantNotFound {
				t.Fatalf("Is(ErrNotFound) = %v", got)
			}
			if got := errors.Is(err, ErrNotInstalled); got != tc.wantMissing {
				t.Fatalf("Is(ErrNotInstalled) = %v", got)
			}
			if got := errors.Is(err, ErrExists); got != tc.wantExists {
				t.Fatalf("Is(ErrExists) = %v", got)
			}
			var tmuxErr *Error
			if err != nil && !errors.As(err, &tmuxErr) {
				t.Fatalf("error is not *Error: %T", err)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
		want string
	}{
		{name: "stderr", err: &Error{Args: []string{"-L", "x", "kill-session", "-t", "=a"}, Stderr: "can't find session: a", Code: 1}, want: "tmux kill-session: can't find session: a"},
		{name: "code only", err: &Error{Args: []string{"ls"}, Code: 2}, want: "tmux ls: exit status 2"},
		{name: "wrapped", err: &Error{Args: []string{"ls"}, Err: ErrNotInstalled}, want: "tmux ls: tmux: not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSocketFromEnv(t *testing.T) {
	cases := []struct {
		in     string
		want   Socket
		wantOK bool
	}{
		{in: "/private/tmp/tmux-501/default,1234,0", want: Socket{Path: "/private/tmp/tmux-501/default"}, wantOK: true},
		{in: "/tmp/s", want: Socket{Path: "/tmp/s"}, wantOK: true},
		{in: ""},
		{in: "relative,1,0"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := SocketFromEnv(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("SocketFromEnv(%q) = %+v, %v", tc.in, got, ok)
			}
		})
	}
}

func TestClientVersion(t *testing.T) {
	rec := &recorder{res: Result{Stdout: []byte("tmux 3.7c\n")}}
	c := New(Options{Executor: rec, Socket: Socket{Name: "ignored"}})
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 3 || v.Minor != 7 || v.Suffix != "c" {
		t.Fatalf("Version = %+v", v)
	}
	if !reflect.DeepEqual(rec.args[0], []string{"-V"}) {
		t.Fatalf("args = %q", rec.args[0])
	}
}

func TestWithSocket(t *testing.T) {
	base := New(Options{Socket: Socket{Name: "a"}})
	other := base.WithSocket(Socket{Path: "/tmp/b"})
	if base.Socket().Name != "a" || other.Socket().Path != "/tmp/b" {
		t.Fatalf("WithSocket mutated original or failed: %+v %+v", base.Socket(), other.Socket())
	}
	if !(Socket{}).IsZero() || (Socket{Name: "x"}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}

func TestChangesChannel(t *testing.T) {
	cases := []struct {
		session string
		want    string
	}{
		{session: "api", want: "lt-changes-api"},
		{session: "my_repo-2", want: "lt-changes-my_repo-2"},
	}
	for _, tc := range cases {
		t.Run(tc.session, func(t *testing.T) {
			if got := ChangesChannel(tc.session); got != tc.want {
				t.Fatalf("ChangesChannel(%q) = %q, want %q", tc.session, got, tc.want)
			}
		})
	}
}
