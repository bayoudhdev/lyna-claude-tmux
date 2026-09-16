package procx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseStat(t *testing.T) {
	tail := " 0 0 0 0 0 0 0 0 20 0 1 0 123456 1000 10"
	cases := []struct {
		name    string
		in      string
		want    statFields
		wantErr bool
	}{
		{
			name: "plain",
			in:   "4242 (claude) S 4200 4242 4242 34817 4242 4194560" + tail,
			want: statFields{PID: 4242, Comm: "claude", PPID: 4200, TTYNr: 34817, StartTick: 123456},
		},
		{
			name: "command with spaces and parentheses",
			in:   "7 (a) b (c)) R 1 7 7 0 -1 0" + tail,
			want: statFields{PID: 7, Comm: "a) b (c)", PPID: 1, TTYNr: 0, StartTick: 123456},
		},
		{
			name: "negative tty for no terminal",
			in:   "9 (kworker/0:1) I 2 0 0 -1 -1 0" + tail,
			want: statFields{PID: 9, Comm: "kworker/0:1", PPID: 2, TTYNr: -1, StartTick: 123456},
		},
		{
			name: "real systemd record",
			in:   "1 (systemd) S 0 1 1 0 -1 4194560 115307 35085047 99 3009 262 426 139393 23447 20 0 1 0 5 172441600 3035 18446744073709551615\n",
			want: statFields{PID: 1, Comm: "systemd", PPID: 0, TTYNr: 0, StartTick: 5},
		},
		{name: "empty", in: "", wantErr: true},
		{name: "no parentheses", in: "1 init S 0 1 1 0", wantErr: true},
		{name: "closing before opening", in: "1 )init( S 0", wantErr: true},
		{name: "missing pid", in: "(init) S 0 1 1 0 -1 0" + tail, wantErr: true},
		{name: "bad pid", in: "x (init) S 0 1 1 0 -1 0" + tail, wantErr: true},
		{name: "truncated", in: "1 (init) S 0 1 1 0", wantErr: true},
		{name: "bad ppid", in: "1 (init) S -5 1 1 0 -1 0" + tail, wantErr: true},
		{name: "bad tty", in: "1 (init) S 0 1 1 tty -1 0" + tail, wantErr: true},
		{name: "bad start", in: "1 (init) S 0 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 soon 1000 10", wantErr: true},
		{name: "negative start", in: "1 (init) S 0 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 -5 1000 10", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStat([]byte(tc.in))
			if tc.wantErr {
				if !errors.Is(err, errMalformed) {
					t.Fatalf("parseStat(%q) error = %v, want errMalformed", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStat(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseStat(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseBootTime(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "present", in: "cpu  1 2 3\nintr 5\nbtime 1700000000\nprocesses 9\n", want: 1700000000},
		{name: "missing", in: "cpu 1 2 3\n", wantErr: true},
		{name: "bad value", in: "btime soon\n", wantErr: true},
		{name: "negative", in: "btime -4\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBootTime([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseBootTime error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseBootTime = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLinuxTTY(t *testing.T) {
	cases := []struct {
		name         string
		in           int64
		major, minor uint32
		ok           bool
	}{
		{name: "pts 1", in: 34817, major: 136, minor: 1, ok: true},
		{name: "tty1", in: 1025, major: 4, minor: 1, ok: true},
		{name: "large minor", in: 136<<8 | 0x34 | 0x12<<20, major: 136, minor: 0x1234, ok: true},
		{name: "none", in: 0},
		{name: "negative", in: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, ok := linuxTTY(tc.in)
			if major != tc.major || minor != tc.minor || ok != tc.ok {
				t.Errorf("linuxTTY(%d) = %d, %d, %v; want %d, %d, %v", tc.in, major, minor, ok, tc.major, tc.minor, tc.ok)
			}
		})
	}
}

func TestParseCmdline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "argv", in: "node\x00/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js\x00--resume\x00", want: []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "--resume"}},
		{name: "empty argument kept", in: "sh\x00\x00x\x00", want: []string{"sh", "", "x"}},
		{name: "no trailing nul", in: "a\x00b", want: []string{"a", "b"}},
		{name: "kernel thread", in: "", want: nil},
		{name: "only nul", in: "\x00\x00", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCmdline([]byte(tc.in)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseCmdline(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	t.Run("bounded", func(t *testing.T) {
		in := strings.Repeat("x\x00", maxArgs+10)
		if got := parseCmdline([]byte(in)); len(got) != maxArgs {
			t.Errorf("len = %d, want %d", len(got), maxArgs)
		}
	})
}

func procArgs(argc int32, body string) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, argc)
	buf.WriteString(body)
	return buf.Bytes()
}

func TestParseProcArgs2(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		wantExe  string
		wantArgs []string
		wantErr  bool
	}{
		{
			name:     "native claude with environment",
			in:       procArgs(3, "/Users/u/.local/bin/claude\x00\x00\x00\x00claude\x00--resume\x00abc\x00HOME=/Users/u\x00PATH=/bin\x00"),
			wantExe:  "/Users/u/.local/bin/claude",
			wantArgs: []string{"claude", "--resume", "abc"},
		},
		{
			name:     "no padding",
			in:       procArgs(1, "/bin/sleep\x00sleep\x00"),
			wantExe:  "/bin/sleep",
			wantArgs: []string{"sleep"},
		},
		{
			name:     "argc larger than the buffer",
			in:       procArgs(9, "/bin/sh\x00sh\x00-c"),
			wantExe:  "/bin/sh",
			wantArgs: []string{"sh", "-c"},
		},
		{name: "zero argc", in: procArgs(0, "/bin/x\x00"), wantExe: "/bin/x", wantArgs: []string{}},
		{name: "too short", in: []byte{1, 0}, wantErr: true},
		{name: "negative argc", in: procArgs(-1, "/bin/x\x00"), wantErr: true},
		{name: "unterminated path", in: procArgs(1, "/bin/x"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, args, err := parseProcArgs2(tc.in)
			if tc.wantErr {
				if !errors.Is(err, errMalformed) {
					t.Fatalf("error = %v, want errMalformed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if exe != tc.wantExe || !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("parseProcArgs2 = %q, %q; want %q, %q", exe, args, tc.wantExe, tc.wantArgs)
			}
		})
	}
}

func FuzzParseStat(f *testing.F) {
	f.Add([]byte("4242 (claude) S 4200 4242 4242 34817 4242 4194560 0 0 0 0 0 0 0 0 20 0 1 0 123456 1000 10"))
	f.Add([]byte("7 (a) b (c)) R 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 0 20 0 1 0 9 1 1"))
	f.Add([]byte("1 ("))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := parseStat(data)
		if err != nil {
			return
		}
		if got.PID < 0 || got.PPID < 0 {
			t.Fatalf("negative ids accepted: %+v", got)
		}
		if !bytes.Contains(data, []byte("("+got.Comm+")")) {
			t.Fatalf("comm %q is not a parenthesized part of the input", got.Comm)
		}
	})
}

func FuzzParseProcArgs2(f *testing.F) {
	f.Add(procArgs(3, "/Users/u/.local/bin/claude\x00\x00\x00claude\x00--resume\x00abc\x00HOME=/x\x00"))
	f.Add(procArgs(1<<30, "/bin/x\x00a\x00"))
	f.Add([]byte{0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		exe, args, err := parseProcArgs2(data)
		if err != nil {
			return
		}
		if len(args) > maxArgs {
			t.Fatalf("%d args exceed the bound", len(args))
		}
		if strings.Contains(exe, "\x00") {
			t.Fatalf("exe %q contains NUL", exe)
		}
		for _, a := range args {
			if strings.Contains(a, "\x00") {
				t.Fatalf("argument %q contains NUL", a)
			}
		}
	})
}

func FuzzParseCmdline(f *testing.F) {
	f.Add([]byte("node\x00cli.js\x00"))
	f.Add([]byte("\x00a\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		args := parseCmdline(data)
		if len(args) > maxArgs {
			t.Fatalf("%d args exceed the bound", len(args))
		}
		// Re-encoding the arguments reproduces a prefix of the input.
		encoded := strings.Join(args, "\x00")
		if !bytes.HasPrefix(data, []byte(encoded)) {
			t.Fatalf("args %q do not re-encode to a prefix of %q", args, data)
		}
	})
}
