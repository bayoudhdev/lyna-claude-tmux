package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// fakeHyperfine records each invocation (arguments, the TMUX variables and
// the stdin file) and writes a CSV export with fixed timings, slower for the
// lyna-tmux commands than for the hello world.
const fakeHyperfine = `#!/bin/sh
if [ "$1" = --version ]; then echo "hyperfine 1.20.0"; exit 0; fi
csv="" name="" input="" last=""
while [ $# -gt 0 ]; do
  case $1 in
  --export-csv) csv=$2; shift 2 ;;
  --command-name) name=$2; shift 2 ;;
  --input) input=$2; shift 2 ;;
  --warmup|--runs|--min-runs) shift 2 ;;
  *) last=$1; shift ;;
  esac
done
{
  echo "run name=$name command=$last TMUX=${TMUX-unset} TMUX_PANE=${TMUX_PANE-unset}"
  if [ -n "$input" ]; then echo "stdin $(cat "$input")"; fi
} >> "$BENCH_LOG"
mean=0.0021
case $name in "go hello world") mean=0.0007 ;; esac
printf 'command,mean,stddev,median,user,system,min,max\n%s,%s,0.0001,%s,0.0004,0.0003,0.0006,0.0030\n' "$name" "$mean" "$mean" > "$csv"
`

// fakeGo answers go version and go env, builds by writing a script at the -o
// path, and answers a benchmark run with one line in the toolchain's format.
// BenchmarkTeammateOpens fails instead, standing for the benchmark a machine
// cannot run: the report keeps the numbers it does have.
const fakeGo = `#!/bin/sh
echo "go $*" >> "$BENCH_LOG"
if [ "$1" = version ]; then echo "go version go1.27.1 testos/testarch"; exit 0; fi
if [ "$1" = env ]; then echo "testarch"; exit 0; fi
bench=""
while [ $# -gt 0 ]; do
  case $1 in
  -bench) bench=$2; shift 2 ;;
  -o) printf '#!/bin/sh\necho hello\n' > "$2"; chmod 0755 "$2"; shift 2 ;;
  *) shift ;;
  esac
done
if [ -n "$bench" ]; then
  name=$(printf '%s' "$bench" | tr -d '^$')
  if [ "$name" = BenchmarkTeammateOpens ]; then exit 1; fi
  printf 'goos: testos\ngoarch: testarch\n%s-8\t100\t2500000 ns/op\t1024 B/op\t8 allocs/op\nPASS\n' "$name"
fi
`

const fakeLmux = `#!/bin/sh
case $1 in
version) echo "lmux v9.9.9 (abc1234, 2026-09-15)" ;;
*) cat > /dev/null ;;
esac
`

type benchEnv struct {
	bin, tmp, log string
}

func newBenchEnv(t *testing.T, withHyperfine bool) benchEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bench.sh needs a POSIX host")
	}
	root := t.TempDir()
	e := benchEnv{bin: filepath.Join(root, "bin"), tmp: filepath.Join(root, "tmp"), log: filepath.Join(root, "bench.log")}
	for _, dir := range []string{e.bin, e.tmp} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(e.bin, "go"), fakeGo)
	if withHyperfine {
		writeExecutable(t, filepath.Join(e.bin, "hyperfine"), fakeHyperfine)
	}
	writeExecutable(t, filepath.Join(root, "lmux"), fakeLmux)
	return e
}

func runBench(t *testing.T, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	cmd := exec.Command(bash, append([]string{scriptPath(t, "bench.sh")}, args...)...)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), exit
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBenchScriptArguments(t *testing.T) {
	// The missing-tool messages, verbatim: a run that dies in mktemp or in
	// the build instead of naming the tool is what these cases guard.
	const (
		goRequired        = "bench.sh: go is required (set GO to the toolchain to use): /nonexistent/go not found\n"
		hyperfineRequired = "bench.sh: hyperfine is required (brew install hyperfine, apt-get install hyperfine or cargo install hyperfine): "
	)
	cases := []struct {
		name      string
		args      []string
		hyperfine bool
		// env is added to the environment; GO points the script at a go
		// that does not exist when set.
		env      []string
		wantExit int
		wantOut  string
		wantErr  string
		golden   string
	}{
		{name: "dry run with a binary", args: []string{"--dry-run", "--runs", "3", "--bin", "/opt/lyna tools/lmux"}, golden: "bench/dry-run-bin.txt"},
		{name: "dry run builds the binary", args: []string{"--dry-run"}, golden: "bench/dry-run-build.txt"},
		{name: "dry run needs no hyperfine", args: []string{"--dry-run"}, hyperfine: false, wantOut: "hyperfine --shell=none"},
		{name: "dry run needs no go", args: []string{"--dry-run"}, env: []string{"GO=/nonexistent/go"}, wantOut: "/nonexistent/go build"},
		{name: "dry run shows the go benchmarks", args: []string{"--dry-run"}, wantOut: "-bench '^BenchmarkAgentBarRender$' -benchtime 2s"},
		{name: "the benchmark time comes from the environment", args: []string{"--dry-run"}, env: []string{"BENCHTIME=1x"}, wantOut: "-benchtime 1x"},
		{name: "help", args: []string{"--help"}, wantOut: "Usage: scripts/bench.sh [--dry-run] [--bin PATH] [--runs N] [--output FILE]"},
		{name: "help names the environment", args: []string{"--help"}, wantOut: "HYPERFINE      the hyperfine command to use (default: hyperfine)"},
		{name: "help names the benchmark time", args: []string{"--help"}, wantOut: "BENCHTIME      how long each Go benchmark runs (default: 2s)"},
		{name: "unknown argument", args: []string{"--fast"}, wantExit: 2, wantErr: "unknown argument: --fast"},
		{name: "missing value", args: []string{"--dry-run", "--bin"}, wantExit: 2, wantErr: "--bin needs a value"},
		{name: "one run is refused", args: []string{"--runs", "1"}, wantExit: 2, wantErr: "--runs must be a whole number of at least 2"},
		{name: "non-numeric runs are refused", args: []string{"--runs", "5x"}, wantExit: 2, wantErr: "--runs must be"},
		{name: "hyperfine missing", args: nil, hyperfine: false, wantExit: 1, wantErr: hyperfineRequired},
		{name: "hyperfine missing with a binary", args: []string{"--bin", "/bin/sh"}, hyperfine: false, wantExit: 1, wantErr: hyperfineRequired},
		{name: "go missing", args: nil, hyperfine: true, env: []string{"GO=/nonexistent/go"}, wantExit: 1, wantErr: goRequired},
		{name: "go missing with a binary", args: []string{"--bin", "/bin/sh"}, hyperfine: true, env: []string{"GO=/nonexistent/go"}, wantExit: 1, wantErr: goRequired},
		{name: "binary not executable", args: []string{"--bin", "/nonexistent/lmux"}, hyperfine: true, wantExit: 1, wantErr: "/nonexistent/lmux is not an executable file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newBenchEnv(t, tc.hyperfine || tc.golden != "")
			// TMPDIR does not exist: a dry run must not create its scratch
			// directory, the printed paths stay stable for the goldens, and a
			// run missing a tool must fail on the tool, which it can only do
			// by looking before it creates anything.
			env := append([]string{"PATH=" + e.bin + ":/usr/bin:/bin", "TMPDIR=/nonexistent/tmp/", "BENCH_LOG=" + e.log}, tc.env...)
			// A real run looks for hyperfine, so HYPERFINE names the fake, or
			// nothing: a hyperfine installed on the machine must not stand
			// in. The dry runs print the bare name the goldens hold.
			if !slices.Contains(tc.args, "--dry-run") {
				env = append(env, "HYPERFINE="+filepath.Join(e.bin, "hyperfine"))
			}
			stdout, stderr, exit := runBench(t, env, tc.args...)
			if exit != tc.wantExit || !strings.Contains(stdout, tc.wantOut) || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			if tc.golden != "" {
				golden.Assert(t, tc.golden, []byte(strings.ReplaceAll(stdout, repoRoot(t), "ROOT")))
			}
			if strings.Contains(tc.name, "dry run") {
				if data, err := os.ReadFile(e.log); err == nil {
					t.Fatalf("dry run executed commands:\n%s", data)
				}
			}
		})
	}
}

func TestBenchScriptReport(t *testing.T) {
	for _, output := range []bool{false, true} {
		name := "stdout"
		if output {
			name = "output file"
		}
		t.Run(name, func(t *testing.T) {
			e := newBenchEnv(t, true)
			lyna := filepath.Join(filepath.Dir(e.bin), "lmux")
			env := []string{
				"PATH=" + e.bin + ":/usr/bin:/bin", "TMPDIR=" + e.tmp, "BENCH_LOG=" + e.log,
				"TMUX=/tmp/tmux-1000/default,1,0", "TMUX_PANE=%1",
			}
			args := []string{"--runs", "2", "--bin", lyna}
			outFile := filepath.Join(t.TempDir(), "benchmarks.md")
			if output {
				args = append(args, "--output", outFile)
			}
			stdout, stderr, exit := runBench(t, env, args...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			report := stdout
			if output {
				if stdout != "" {
					t.Fatalf("--output also printed to stdout:\n%s", stdout)
				}
				report = string(mustRead(t, outFile))
			}
			// The architecture comes from the fake go, so only the operating
			// system name is this machine's.
			uname, err := exec.Command("uname", "-s").Output()
			if err != nil {
				t.Fatal(err)
			}
			report = strings.ReplaceAll(report, strings.TrimSpace(string(uname)), "UNAME")
			golden.Assert(t, "bench/report.md", []byte(report))

			log := strings.ReplaceAll(string(mustRead(t, e.log)), lyna, "LYNA")
			// The Go benchmarks are run from the repository, whose path is this
			// machine's: the golden holds the calls, not where they were made.
			log = strings.ReplaceAll(rewriteWork(log, e.tmp), repoRoot(t), "ROOT")
			golden.Assert(t, "bench/calls.log", []byte(log))
			if entries, _ := os.ReadDir(e.tmp); len(entries) != 0 {
				t.Fatalf("scratch directory not removed: %v", entries)
			}
		})
	}
}

// rewriteWork replaces the random scratch directory under tmp with WORK.
func rewriteWork(s, tmp string) string {
	prefix := strings.TrimSuffix(tmp, "/") + "/lyna-tmux-bench."
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			return s
		}
		end := i + len(prefix)
		for end < len(s) && s[end] != '/' && s[end] != '"' && s[end] != ' ' && s[end] != '\n' && s[end] != '\'' {
			end++
		}
		s = s[:i] + "WORK" + s[end:]
	}
}

// TestBenchScriptWithHyperfine runs the real hyperfine and go toolchain on a
// scripted binary, proving the hyperfine flags and the table parsing work
// with the installed versions.
func TestBenchScriptWithHyperfine(t *testing.T) {
	if testing.Short() {
		t.Skip("runs hyperfine; skipped with -short")
	}
	hyperfine, err := exec.LookPath("hyperfine")
	if err != nil {
		t.Skip("hyperfine is not installed")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	e := newBenchEnv(t, false)
	env := []string{
		"PATH=" + filepath.Dir(hyperfine) + ":" + filepath.Dir(goBin) + ":/usr/bin:/bin",
		"TMPDIR=" + e.tmp,
		"HOME=" + os.Getenv("HOME"),
		"GOCACHE=" + goEnv(t, "GOCACHE"),
		"GOPATH=" + goEnv(t, "GOPATH"),
		// The Go benchmarks run for real here, so each one runs once: the
		// flags and the parsing are what this case proves, not the timings.
		"BENCHTIME=1x",
	}
	stdout, stderr, exit := runBench(t, env, "--runs", "2", "--bin", filepath.Join(filepath.Dir(e.bin), "lmux"))
	if exit != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	rows := 0
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.HasPrefix(line, "| `") {
			rows++
		}
	}
	// Four commands timed by hyperfine, five benchmarks timed by the Go
	// toolchain. A benchmark this machine cannot run still has its row, so the
	// count holds whether or not tmux is installed here.
	if rows != 9 || !strings.Contains(stdout, "| Command | Mean [ms] |") ||
		!strings.Contains(stdout, "| Inside a workspace | Mean [ms] |") || !strings.Contains(stdout, "lmux v9.9.9") {
		t.Fatalf("report:\n%s", stdout)
	}
}

func goEnv(t *testing.T, key string) string {
	t.Helper()
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
