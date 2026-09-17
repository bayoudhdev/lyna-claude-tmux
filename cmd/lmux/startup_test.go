package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Claude runs hook and statusline commands many times per turn, so package
// initialization is paid on every call. A dependency once spent 25 ms in
// init() building width tables; this guard keeps that class of regression
// out of the binary. Each package's init time is the minimum over several
// runs, so scheduler noise on a busy machine does not fail the test.
const (
	initRuns       = 7
	maxPackageInit = 2.0 // milliseconds
	// Allocation budget for one package of this module. Work that belongs in
	// a command (compiling patterns, building tables) must happen on first
	// use, not at init: a hook pays it on every tool call.
	maxPackageInitAllocs = 64
	modulePrefix         = "github.com/bayoudhdev/lyna-claude-tmux/"
)

var initLine = regexp.MustCompile(`^init (\S+) @\S+ ms, ([0-9.]+) ms clock, \d+ bytes, (\d+) allocs`)

func TestStartupInitCost(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary; skipped in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "lmux")
	build := exec.CommandContext(t.Context(), goBin, "build", "-trimpath", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cases := []struct {
		name string
		args []string
	}{
		{name: "version", args: []string{"version"}},
		// Claude starts these two many times per turn.
		{name: "statusline", args: []string{"statusline"}},
		{name: "hook", args: []string{"hook", "Stop"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			best := map[string]float64{}
			allocs := map[string]int{}
			for range initRuns {
				cmd := exec.CommandContext(t.Context(), bin, tc.args...)
				cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
				var stderr bytes.Buffer
				cmd.Stdout = &bytes.Buffer{}
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("%v: %v\n%s", tc.args, err, stderr.String())
				}
				for pkg, cost := range parseInitTrace(t, stderr.Bytes()) {
					if prev, ok := best[pkg]; !ok || cost.ms < prev {
						best[pkg] = cost.ms
					}
					if prev, ok := allocs[pkg]; !ok || cost.allocs < prev {
						allocs[pkg] = cost.allocs
					}
				}
			}
			if len(best) == 0 {
				t.Fatal("no inittrace lines: GODEBUG=inittrace=1 had no effect")
			}
			for pkg, ms := range best {
				if ms > maxPackageInit {
					t.Errorf("init %s takes %.2f ms (best of %d runs), budget %.1f ms", pkg, ms, initRuns, maxPackageInit)
				}
			}
			for pkg, n := range allocs {
				if strings.HasPrefix(pkg, modulePrefix) && n > maxPackageInitAllocs {
					t.Errorf("init %s allocates %d times, budget %d: build it on first use (sync.OnceValue)", pkg, n, maxPackageInitAllocs)
				}
			}
		})
	}
}

// initCost is what one package spent initializing.
type initCost struct {
	ms     float64
	allocs int
}

func parseInitTrace(t *testing.T, stderr []byte) map[string]initCost {
	t.Helper()
	got := map[string]initCost{}
	for line := range bytes.Lines(stderr) {
		m := initLine.FindSubmatch(bytes.TrimSpace(line))
		if m == nil {
			continue
		}
		ms, err := strconv.ParseFloat(string(m[2]), 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		n, err := strconv.Atoi(string(m[3]))
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		cost := got[string(m[1])]
		cost.ms += ms
		cost.allocs += n
		got[string(m[1])] = cost
	}
	return got
}

func TestParseInitTrace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  map[string]initCost
	}{
		{
			name:  "trace lines",
			input: "init runtime @0.048 ms, 0.081 ms clock, 0 bytes, 0 allocs\ninit github.com/mattn/go-runewidth @5.5 ms, 25 ms clock, 57040 bytes, 52 allocs\n",
			want:  map[string]initCost{"runtime": {ms: 0.081}, "github.com/mattn/go-runewidth": {ms: 25, allocs: 52}},
		},
		{
			name:  "program output ignored",
			input: "lmux dev darwin/arm64\ninit os @2.2 ms, 0.17 ms clock, 4304 bytes, 20 allocs\n",
			want:  map[string]initCost{"os": {ms: 0.17, allocs: 20}},
		},
		{
			name:  "repeated package summed",
			input: "init p @1 ms, 0.5 ms clock, 0 bytes, 3 allocs\ninit p @2 ms, 0.25 ms clock, 0 bytes, 4 allocs\n",
			want:  map[string]initCost{"p": {ms: 0.75, allocs: 7}},
		},
		{name: "empty", input: "", want: map[string]initCost{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInitTrace(t, []byte(tc.input))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got[%q] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
