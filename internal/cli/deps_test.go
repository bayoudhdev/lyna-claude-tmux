package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRunProgram(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		in      string
		wantOut string
		wantErr string
		fails   bool
	}{
		{name: "streams connected", argv: []string{"/bin/sh", "-c", `read x; echo "got $x"; echo oops >&2`}, in: "line\n", wantOut: "got line\n", wantErr: "oops\n"},
		{name: "exit status is an error", argv: []string{"/bin/sh", "-c", "exit 3"}, fails: true},
		{name: "missing program", argv: []string{"/nonexistent/editor"}, fails: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := runProgram(t.Context(), tc.argv, Streams{In: strings.NewReader(tc.in), Out: &out, Err: &errOut})
			if (err != nil) != tc.fails {
				t.Fatalf("err %v, fails %v", err, tc.fails)
			}
			if out.String() != tc.wantOut || errOut.String() != tc.wantErr {
				t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
			}
		})
	}
}

func TestDepsNow(t *testing.T) {
	fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		clock func() time.Time
		check func(time.Time) bool
	}{
		{name: "injected clock", clock: func() time.Time { return fixed }, check: func(got time.Time) bool { return got.Equal(fixed) }},
		{name: "process clock when unset", check: func(got time.Time) bool { return time.Since(got).Abs() < time.Minute }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Deps{Now: tc.clock}).now(); !tc.check(got) {
				t.Fatalf("now() = %v", got)
			}
		})
	}
}
