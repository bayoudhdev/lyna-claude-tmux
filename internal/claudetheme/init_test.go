package claudetheme

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// maxInitAllocs bounds the allocations of this package's initialization. The
// package is linked into the hook and statusline commands, which Claude Code
// starts on every tool call and assistant message; compiling the color
// patterns at init once cost 311 allocations there.
const maxInitAllocs = 8

func TestPackageInitIsCheap(t *testing.T) {
	if testing.Short() {
		t.Skip("re-executes the test binary; skipped in -short mode")
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("re-run test binary: %v\n%s", err, stderr.String())
	}
	line := regexp.MustCompile(`(?m)^init github\.com/bayoudhdev/lyna-claude-tmux/internal/claudetheme @\S+ ms, \S+ ms clock, \d+ bytes, (\d+) allocs$`)
	m := line.FindSubmatch(stderr.Bytes())
	if m == nil {
		if !bytes.Contains(stderr.Bytes(), []byte("init runtime @")) {
			t.Fatalf("GODEBUG=inittrace=1 had no effect:\n%s", stderr.String())
		}
		return // nothing to initialize at all
	}
	allocs, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if allocs > maxInitAllocs {
		t.Fatalf("package init allocates %d times, budget %d: compile patterns and build tables on first use", allocs, maxInitAllocs)
	}
}
