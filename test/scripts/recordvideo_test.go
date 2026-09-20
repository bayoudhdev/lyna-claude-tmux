package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFilmDriver answers for record-docs.sh --film: it writes the film and
// the subtitles of every chapter it was asked for, with the duration and the
// height the fake ffprobe reads back, and prints what the real driver prints.
const fakeFilmDriver = `#!/bin/sh
echo "driver $*" >> "$FILM_LOG"
assets=""
names=""
prev=""
for arg in "$@"; do
  case $prev in
  --assets) assets=$arg ;;
  --only) names="$names $arg" ;;
  esac
  prev=$arg
done
mkdir -p "$assets"
for name in $names; do
  echo "$name" > "$assets/$name.mp4"
  eval "meta=\"\$FILM_META_$(printf '%s' "$name" | tr -c '[:alnum:]' '_')\""
  [ -n "$meta" ] || meta="10 700"
  echo "$meta" > "$assets/$name.mp4.meta"
  eval "cue=\"\$FILM_CUE_$(printf '%s' "$name" | tr -c '[:alnum:]' '_')\""
  if [ -n "$cue" ]; then printf '%s\n' "$cue" > "$assets/$name.srt"; fi
  echo "$name: 3 frames"
  printf 'chapter: 0\tThe %s chapter\n' "$name"
done
`

type filmEnv struct {
	dir, bin, scenes, assets, driver, log string
}

func newFilmEnv(t *testing.T, chapters []string) filmEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("record-video.sh needs a POSIX host")
	}
	root := t.TempDir()
	e := filmEnv{
		dir:    root,
		bin:    filepath.Join(root, "bin"),
		scenes: filepath.Join(root, "video"),
		assets: filepath.Join(root, "assets"),
		driver: filepath.Join(root, "bin", "driver.sh"),
		log:    filepath.Join(root, "film.log"),
	}
	for _, dir := range []string{e.bin, e.scenes, e.assets} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, e.driver, fakeFilmDriver)
	writeExecutable(t, filepath.Join(e.bin, "ffmpeg"), fakeFilmFFmpeg)
	writeExecutable(t, filepath.Join(e.bin, "ffprobe"), fakeFilmFFprobe)
	for _, name := range chapters {
		if err := os.WriteFile(filepath.Join(e.scenes, name+".scene"), []byte("run true\nframe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// fakeFilmFFmpeg records the join and writes the film it was asked for, with
// the duration of everything it joined and the height they share.
const fakeFilmFFmpeg = `#!/bin/sh
echo "ffmpeg $*" >> "$FILM_LOG"
out=""
list=""
prev=""
for arg in "$@"; do
  if [ "$prev" = -i ]; then list=$arg; fi
  prev=$arg
  out=$arg
done
echo joined > "$out"
[ -n "$list" ] || exit 0
total=0
height=0
while read -r line; do
  path=$(printf '%s' "$line" | sed -n "s/^file '\(.*\)'$/\1/p")
  [ -n "$path" ] || continue
  set -- $(cat "$path.meta")
  total=$(awk -v a="$total" -v b="$1" 'BEGIN { printf "%.3f", a + b }')
  height=$2
done < "$list"
echo "$total $height" > "$out.meta"
`

// fakeFilmFFprobe answers the two questions the assembler asks: how long a
// chapter is, and how tall it is.
const fakeFilmFFprobe = `#!/bin/sh
file=""
want=duration
for arg in "$@"; do
  case $arg in
  *height*) want=height ;;
  esac
  file=$arg
done
set -- $(cat "$file.meta" 2>/dev/null || echo "10 700")
if [ "$want" = height ]; then echo "$2"; else echo "$1"; fi
`

func runRecordVideo(t *testing.T, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	cmd := exec.Command(bash, append([]string{scriptPath(t, "record-video.sh")}, args...)...)
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

func (e filmEnv) env(extra ...string) []string {
	return append([]string{
		"PATH=" + e.bin + ":/usr/bin:/bin",
		"FILM_LOG=" + e.log,
		"HOME=" + e.dir,
		"TMPDIR=" + e.dir,
	}, extra...)
}

func (e filmEnv) args(extra ...string) []string {
	return append([]string{
		"--project", e.dir, "--scenes", e.scenes, "--assets", e.assets, "--driver", e.driver,
	}, extra...)
}

// TestRecordVideoJoinsTheChapters is the whole of the assembler: the chapters
// are recorded in the order they are numbered, joined without being encoded
// again, their subtitles moved by the second each chapter starts at, and the
// chapter list printed with the time a reader would seek to.
func TestRecordVideoJoinsTheChapters(t *testing.T) {
	e := newFilmEnv(t, []string{"01-open", "02-panes"})
	env := e.env(
		"FILM_META_01_open=8 700",
		"FILM_META_02_panes=5 700",
		"FILM_CUE_01_open=1\n00:00:01,000 --> 00:00:03,000\nlmux create\na workspace opens\n",
		"FILM_CUE_02_panes=1\n00:00:00,500 --> 00:00:02,000\nAlt+backslash\na pane beside it\n",
	)
	stdout, stderr, exit := runRecordVideo(t, env, e.args()...)
	if exit != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	log := string(mustRead(t, e.log))
	if !strings.Contains(log, "--film") || !strings.Contains(log, "--only 01-open") || !strings.Contains(log, "--only 02-panes") {
		t.Fatalf("the chapters were not recorded as films:\n%s", log)
	}
	if !strings.Contains(log, "-c copy") {
		t.Fatalf("the chapters were encoded again instead of joined:\n%s", log)
	}
	cases := []struct {
		name string
		want string
	}{
		{name: "the first chapter keeps its own times", want: "00:00:01,000 --> 00:00:03,000\nlmux create\na workspace opens"},
		{name: "the second is moved by the first", want: "00:00:08,500 --> 00:00:10,000\nAlt+backslash\na pane beside it"},
		{name: "the cues are numbered in order", want: "2\n00:00:08,500"},
	}
	srt := string(mustRead(t, filepath.Join(e.assets, "demo.srt")))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(srt, tc.want) {
				t.Fatalf("the subtitle track lacks %q:\n%s", tc.want, srt)
			}
		})
	}
	for _, want := range []string{"00:00:00,000\tThe 01-open chapter", "00:00:08,000\tThe 02-panes chapter"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the chapter list lacks %q:\n%s", want, stdout)
		}
	}
	// A chapter stays on its own, for a page that embeds a single feature.
	for _, name := range []string{"demo.mp4", "demo-01-open.mp4", "demo-02-panes.mp4"} {
		if _, err := os.Stat(filepath.Join(e.assets, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// TestRecordVideoArguments covers what the assembler refuses, including the
// one thing a film cannot recover from: two chapters of different sizes.
func TestRecordVideoArguments(t *testing.T) {
	cases := []struct {
		name     string
		chapters []string
		args     []string
		env      []string
		wantExit int
		wantOut  string
		wantErr  string
	}{
		{name: "help", chapters: []string{"01-open"}, args: []string{"--help"}, wantOut: "Usage: scripts/record-video.sh --project DIR"},
		{name: "no project", chapters: []string{"01-open"}, wantExit: 2, wantErr: "--project is required"},
		{name: "unknown argument", chapters: []string{"01-open"}, args: []string{"--quick"}, wantExit: 2, wantErr: "unknown argument: --quick"},
		{name: "missing value", chapters: []string{"01-open"}, args: []string{"--out"}, wantExit: 2, wantErr: "--out needs a value"},
		{
			name: "no chapter matches", chapters: []string{"01-open"}, args: []string{"--only", "nothing"},
			wantExit: 1, wantErr: "no chapter matched",
		},
		{
			name: "a dry run records nothing", chapters: []string{"01-open"}, args: []string{"--dry-run"},
			wantOut: "chapter: 01-open",
		},
		{
			name:     "chapters of two sizes are refused",
			chapters: []string{"01-open", "02-panes"},
			env:      []string{"FILM_META_01_open=10 700", "FILM_META_02_panes=5 640"},
			wantExit: 1, wantErr: "every scene of the film needs the same size",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFilmEnv(t, tc.chapters)
			args := tc.args
			if tc.name != "help" && tc.name != "no project" && tc.name != "unknown argument" && tc.name != "missing value" {
				args = e.args(tc.args...)
			}
			stdout, stderr, exit := runRecordVideo(t, e.env(tc.env...), args...)
			if exit != tc.wantExit {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			if tc.wantOut != "" && !strings.Contains(stdout, tc.wantOut) {
				t.Fatalf("stdout lacks %q:\n%s", tc.wantOut, stdout)
			}
			if tc.wantErr != "" && !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr lacks %q:\n%s", tc.wantErr, stderr)
			}
			if strings.Contains(tc.name, "dry run") {
				if _, err := os.Stat(filepath.Join(e.assets, "demo.mp4")); err == nil {
					t.Fatal("a dry run wrote the film")
				}
			}
		})
	}
}
