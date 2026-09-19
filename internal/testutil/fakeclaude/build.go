package fakeclaude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mainPackage is the import path of the fake's command.
const mainPackage = "github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/fakeclaude/cmd/fakeclaude"

// buildTimeout bounds one `go build`; a cold module cache on CI is the slow case.
const buildTimeout = 5 * time.Minute

// Build compiles the fake into a fresh temporary directory and returns the
// absolute path of the executable, named claude so the directory can go
// first on PATH. The build cache keeps repeated builds fast.
func Build(t testing.TB) string {
	t.Helper()
	return BuildProgram(t, mainPackage, "claude")
}

// BuildProgram compiles the main package pkg into a fresh temporary directory
// as an executable called name, and returns its absolute path. It is how a
// test runs a real binary of this module in a pane beside the fake: the
// lyna-tmux binary Claude Code starts a teammate through, for one.
func BuildProgram(t testing.TB, pkg, name string) string {
	t.Helper()
	goBin, err := goTool()
	if err != nil {
		t.Fatalf("fakeclaude: %v", err)
	}
	out := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("fakeclaude: go build %s: %v\n%s", pkg, err, output.String())
	}
	return out
}

// goTool finds the go command: the toolchain named by GOROOT when set, as
// `go test` does for the binaries it runs, then PATH.
func goTool() (string, error) {
	if root := os.Getenv("GOROOT"); root != "" {
		p := filepath.Join(root, "bin", "go")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
	}
	p, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("the go command is not on PATH and GOROOT is unset; it is needed to build the fake claude executable")
	}
	return p, nil
}

// ReadRecords returns the invocations recorded in path, in order. A missing
// file means the fake never ran and yields none.
func ReadRecords(t testing.TB, path string) []Invocation {
	t.Helper()
	var out []Invocation
	readLines(t, path, "invocation", func(line []byte) error {
		var inv Invocation
		err := json.Unmarshal(line, &inv)
		out = append(out, inv)
		return err
	})
	return out
}

// ReadHookRuns returns the hook commands recorded in path, in order.
func ReadHookRuns(t testing.TB, path string) []HookRun {
	t.Helper()
	var out []HookRun
	readLines(t, path, "hook", func(line []byte) error {
		var run HookRun
		err := json.Unmarshal(line, &run)
		out = append(out, run)
		return err
	})
	return out
}

// ReadTmuxRuns returns the tmux commands a lead recorded in path, in order.
func ReadTmuxRuns(t testing.TB, path string) []TmuxRun {
	t.Helper()
	var out []TmuxRun
	readLines(t, path, "tmux", func(line []byte) error {
		var run TmuxRun
		err := json.Unmarshal(line, &run)
		out = append(out, run)
		return err
	})
	return out
}

// ReadTeammates returns the teammates a lead recorded opening in path, in
// order.
func ReadTeammates(t testing.TB, path string) []TeammateSpawn {
	t.Helper()
	var out []TeammateSpawn
	readLines(t, path, "teammate", func(line []byte) error {
		var s TeammateSpawn
		err := json.Unmarshal(line, &s)
		out = append(out, s)
		return err
	})
	return out
}

func readLines(t testing.TB, path, kind string, decode func([]byte) error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("fakeclaude: read records: %v", err)
	}
	// Only complete lines count: a test polls the file while fakes in other
	// panes append to it, and a line still being written has no newline yet.
	data = data[:bytes.LastIndexByte(data, '\n')+1]
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for n := 1; sc.Scan(); n++ {
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(sc.Bytes(), &head); err != nil {
			t.Fatalf("fakeclaude: %s line %d: %v", path, n, err)
		}
		if head.Kind != kind {
			continue
		}
		if err := decode(sc.Bytes()); err != nil {
			t.Fatalf("fakeclaude: %s line %d: %v", path, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("fakeclaude: %s: %v", path, err)
	}
}
