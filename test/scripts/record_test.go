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

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// fakeChrome records its arguments and writes the image the renderer asks
// for, so a test exercises the whole pipeline without a browser.
const fakeChrome = `#!/bin/sh
echo "chrome $*" >> "$REC_LOG"
for arg in "$@"; do
  case $arg in
  --screenshot=*) printf '\211PNG\r\n\032\n' > "${arg#--screenshot=}" ;;
  esac
done
`

// fakeFFmpeg records its arguments, copies the frame list it was given so the
// test can read the durations, and writes the output file named last.
const fakeFFmpeg = `#!/bin/sh
echo "ffmpeg $*" >> "$REC_LOG"
out=""
list=""
prev=""
for arg in "$@"; do
  case $prev in
  -i) list=$arg ;;
  esac
  prev=$arg
  out=$arg
done
if [ -n "$list" ]; then cp "$list" "$REC_LIST"; fi
printf 'animation\n' > "$out"
`

type recordEnv struct {
	dir, bin, log, list, font string
}

func newRecordEnv(t *testing.T) recordEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("record.sh needs a POSIX host")
	}
	root := t.TempDir()
	e := recordEnv{
		dir:  root,
		bin:  filepath.Join(root, "bin"),
		log:  filepath.Join(root, "calls.log"),
		list: filepath.Join(root, "frames.txt"),
	}
	if err := os.Mkdir(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(e.bin, "chrome"), fakeChrome)
	writeExecutable(t, filepath.Join(e.bin, "ffmpeg"), fakeFFmpeg)
	// The recorder draws with a patched font it downloads once. A test never
	// reaches the network, so it hands it one instead.
	e.font = filepath.Join(root, "font.ttf")
	if err := os.WriteFile(e.font, []byte("fake font"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

// env is the environment of a record.sh run: the fake tools first, then the
// system directories that hold tmux and python3.
func (e recordEnv) env(t *testing.T) []string {
	t.Helper()
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	return []string{
		"PATH=" + e.bin + ":" + filepath.Dir(tmux) + ":/usr/bin:/bin",
		"CHROME_PATH=" + filepath.Join(e.bin, "chrome"),
		"REC_LOG=" + e.log,
		"REC_LIST=" + e.list,
		"TMPDIR=" + e.dir,
		"HOME=" + e.dir,
		"LYNA_TMUX_RECORD_FONT=" + e.font,
	}
}

func (e recordEnv) scene(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(e.dir, "scene.scene")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runRecord(t *testing.T, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	cmd := exec.Command(bash, append([]string{scriptPath(t, "record.sh")}, args...)...)
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

// TestRecordArguments covers the command line, which is checked before
// anything is started, so these cases need no tmux and no browser.
func TestRecordArguments(t *testing.T) {
	cases := []struct {
		name     string
		scene    string
		args     []string
		wantExit int
		wantOut  string
		wantErr  string
	}{
		{name: "help", args: []string{"--help"}, wantOut: "Usage: scripts/record.sh --scene FILE"},
		{name: "unknown argument", args: []string{"--speed", "2"}, wantExit: 2, wantErr: "unknown argument: --speed"},
		{name: "missing value", args: []string{"--scene"}, wantExit: 2, wantErr: "--scene needs a value"},
		{name: "no scene", args: nil, wantExit: 2, wantErr: "--scene is required"},
		{name: "scene is not a file", args: []string{"--scene", "/nonexistent/x.scene", "--out", "/tmp/x.gif"}, wantExit: 2, wantErr: "is not a file"},
		{
			name: "no output", scene: "run true\nframe\n", args: []string{"--out", ""},
			wantExit: 2, wantErr: "one of --out, --still, --mp4 or --srt is required",
		},
		{
			name: "width is a number", scene: "run true\nframe\n", args: []string{"--out", "/tmp/x.gif", "--width", "wide"},
			wantExit: 2, wantErr: "--width takes a whole number of pixels",
		},
		{
			name: "fps is a positive number", scene: "run true\nframe\n", args: []string{"--out", "/tmp/x.gif", "--fps", "0"},
			wantExit: 2, wantErr: "--fps takes a whole number of frames",
		},
		{
			name: "a redaction is a pair", scene: "run true\nframe\n", args: []string{"--out", "/tmp/x.gif", "--redact", "secret"},
			wantExit: 2, wantErr: "--redact takes FROM=TO",
		},
		{
			name: "a redaction keeps the columns", scene: "run true\nframe\n",
			args:     []string{"--out", "/tmp/x.gif", "--redact", "realname=someone"},
			wantExit: 2, wantErr: "a replacement must be as long as what it replaces",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecordEnv(t)
			args := tc.args
			if tc.scene != "" {
				args = append([]string{"--scene", e.scene(t, tc.scene)}, args...)
			}
			stdout, stderr, exit := runRecord(t, e.env(t), args...)
			if exit != tc.wantExit || !strings.Contains(stdout, tc.wantOut) || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			if _, err := os.Stat(e.log); err == nil {
				t.Fatalf("a rejected command line still ran a tool:\n%s", mustRead(t, e.log))
			}
		})
	}
}

// TestRecordScene covers the scene file: what a valid one parses to, and every
// way an invalid one is refused. A dry run starts no server and no browser.
func TestRecordScene(t *testing.T) {
	cases := []struct {
		name     string
		scene    string
		wantExit int
		wantErr  string
		golden   string
	}{
		{
			name: "every directive",
			scene: `# the scene of the test
title Split and zoom
size 100 30
dir /src/acme-api
env LYNA_TMUX_DEMO=1
env TERM_PROGRAM=demo
run lmux attach api
wait 2
frame
keys M-\\
wait 0.5
frame 1.8
type git status
enter
film 3 0.4
chapter Panes and windows
caption lmux split right | a pane beside the agent
frame 1.2
`,
			golden: "record/scene.txt",
		},
		{name: "unknown directive", scene: "run true\nzoom 2\nframe\n", wantExit: 2, wantErr: "unknown directive: zoom"},
		{name: "no run", scene: "title Only a title\nframe\n", wantExit: 2, wantErr: "no run directive"},
		{name: "empty run", scene: "run\nframe\n", wantExit: 2, wantErr: "run takes a command"},
		{name: "two runs", scene: "run true\nrun false\nframe\n", wantExit: 2, wantErr: "run is allowed once"},
		{name: "no frame", scene: "run true\nwait 1\n", wantExit: 2, wantErr: "no frame or film directive"},
		{name: "size takes two numbers", scene: "size 100\nrun true\nframe\n", wantExit: 2, wantErr: "size takes COLS and ROWS"},
		{name: "size takes whole numbers", scene: "size 100 tall\nrun true\nframe\n", wantExit: 2, wantErr: "size takes whole numbers"},
		{name: "env takes an assignment", scene: "env DEMO\nrun true\nframe\n", wantExit: 2, wantErr: "env takes NAME=VALUE"},
		{name: "film takes a count", scene: "run true\nfilm many 0.3\n", wantExit: 2, wantErr: "film takes a whole COUNT"},
		{name: "film takes a gap", scene: "run true\nfilm 3\n", wantExit: 2, wantErr: "film takes COUNT and GAP"},
		{name: "chapter takes a title", scene: "run true\nchapter\nframe\n", wantExit: 2, wantErr: "chapter takes a title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecordEnv(t)
			scene := e.scene(t, tc.scene)
			stdout, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--out", filepath.Join(e.dir, "out.gif"), "--dry-run")
			if exit != tc.wantExit || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			if tc.golden != "" {
				golden.Assert(t, tc.golden, []byte(strings.ReplaceAll(stdout, scene, "SCENE")))
			}
			if _, err := os.Stat(e.log); err == nil {
				t.Fatalf("a dry run ran a tool:\n%s", mustRead(t, e.log))
			}
		})
	}
}

// TestRecordCapturesARealScreen runs the whole pipeline on a real tmux server:
// the keys of the scene reach the pane, every frame is the screen at that
// moment, and the frames are handed to the assembler with their durations.
func TestRecordCapturesARealScreen(t *testing.T) {
	e := newRecordEnv(t)
	scene := e.scene(t, `title A recorded scene
size 40 8
run cat
frame 0.5
type hello from the scene
enter
wait 0.4
frame 1.5
`)
	keep := filepath.Join(e.dir, "keep")
	gif := filepath.Join(e.dir, "assets", "demo.gif")
	still := filepath.Join(e.dir, "assets", "demo.png")
	stdout, stderr, exit := runRecord(t, e.env(t),
		"--scene", scene, "--out", gif, "--still", still, "--width", "800", "--fps", "15", "--keep", keep)
	if exit != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	for _, want := range []string{"wrote " + still, "wrote " + gif} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout does not report %q:\n%s", want, stdout)
		}
	}
	for _, path := range []string{gif, still} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("%s: %v", path, err)
		}
	}

	// The second frame is the screen after the keys were typed, the first is
	// the screen before: this is what proves the capture is the real screen
	// and not the scene text.
	first := string(mustRead(t, filepath.Join(keep, "frames", "0001.ansi")))
	second := string(mustRead(t, filepath.Join(keep, "frames", "0002.ansi")))
	if strings.Contains(first, "hello from the scene") {
		t.Fatalf("the first frame already shows the typed line:\n%s", first)
	}
	if !strings.Contains(second, "hello from the scene") {
		t.Fatalf("the typed line never reached the pane:\n%s", second)
	}

	// One image per frame, and the last one is the still.
	frames := mustRead(t, e.list)
	wantList := []string{"0001.png", "duration 0.5", "0002.png", "duration 1.5"}
	for _, want := range wantList {
		if !strings.Contains(string(frames), want) {
			t.Fatalf("frame list has no %q:\n%s", want, frames)
		}
	}
	if n := strings.Count(string(frames), "0002.png"); n != 2 {
		t.Fatalf("the last frame is named %d times, want 2 (the concat demuxer drops the last duration):\n%s", n, frames)
	}

	log := string(mustRead(t, e.log))
	if n := strings.Count(log, "chrome "); n != 2 {
		t.Fatalf("chrome ran %d times, want one per frame:\n%s", n, log)
	}
	for _, want := range []string{"--window-size=418,233", "palettegen", "scale=800:-1", "fps=15", "-loop 0"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the tools were not called with %q:\n%s", want, log)
		}
	}
	// The scratch directory is removed even though --keep saved a copy.
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lyna-tmux-rec.") {
			t.Fatalf("scratch directory not removed: %s", entry.Name())
		}
	}
}

// TestRecordDrawsWithAPatchedFont covers the font the frames are drawn with:
// the page loads it from beside itself, Chrome is allowed to read it, and a
// recorder that cannot get one stops rather than drawing boxes where the
// status bar separators and the icons belong.
func TestRecordDrawsWithAPatchedFont(t *testing.T) {
	e := newRecordEnv(t)
	scene := e.scene(t, "title A frame\nsize 20 5\nrun cat\nframe 0.5\n")
	keep := filepath.Join(e.dir, "keep")
	still := filepath.Join(e.dir, "assets", "demo.png")
	_, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--still", still, "--keep", keep)
	if exit != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	page := string(mustRead(t, filepath.Join(keep, "frames", "0001.html")))
	cases := []struct {
		name string
		want string
	}{
		{name: "the face is declared", want: `@font-face { font-family: "LynaRecorder"; src: url("font.ttf") format("truetype"); }`},
		{name: "the terminal draws with it", want: `fontFamily: "LynaRecorder, Menlo, ui-monospace, monospace"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(page, tc.want) {
				t.Fatalf("the page lacks %q:\n%s", tc.want, page)
			}
		})
	}
	if got := mustRead(t, filepath.Join(keep, "frames", "font.ttf")); string(got) != "fake font" {
		t.Fatalf("the font beside the page is %q", got)
	}
	if log := string(mustRead(t, e.log)); !strings.Contains(log, "--allow-file-access-from-files") {
		t.Fatalf("the browser may not read the font:\n%s", log)
	}

	env := append(e.env(t), "LYNA_TMUX_RECORD_FONT="+filepath.Join(e.dir, "missing.ttf"))
	_, stderr, exit = runRecord(t, env, "--scene", scene, "--still", still)
	if exit != 1 || !strings.Contains(stderr, "could not get the patched font") {
		t.Fatalf("a recorder without a font: exit %d\nstderr:\n%s", exit, stderr)
	}
}

// TestRecordRedacts covers what keeps a recording made in a real account
// publishable: the captured screen is rewritten before it is drawn, so the
// image cannot hold what the rule replaced.
// TestRecordRefusesAnErrorScreen holds the recorder to what it captured: a
// frame showing the command line's own error banner ends the recording, and a
// line that merely carries the word does not.
func TestRecordRefusesAnErrorScreen(t *testing.T) {
	cases := []struct {
		name string
		// print is the shell the recorded command runs, and wantExit what the
		// recorder does with the screen it drew.
		print    string
		wantExit int
		wantErr  string
	}{
		{
			name: "the command failed",
			// The banner as the command line draws it: the word alone on its
			// line, in a badge, with the message under it.
			print:    `printf '\n       \033[1;41m ERROR \033[0m\n       the workspace name is taken\n'`,
			wantExit: 1,
			wantErr:  "the command failed on screen in 0001.ansi",
		},
		{
			name:  "a line that carries the word",
			print: `printf 'ERROR rate limited, retrying\n'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecordEnv(t)
			writeExecutable(t, filepath.Join(e.bin, "scene-command"), "#!/bin/sh\n"+tc.print+"\n")
			scene := e.scene(t, `title A recorded scene
size 44 8
run scene-command
wait 0.4
frame 0.5
`)
			still := filepath.Join(e.dir, "assets", "demo.png")
			stdout, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--still", still)
			if exit != tc.wantExit || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			_, err := os.Stat(still)
			if tc.wantExit == 0 && err != nil {
				t.Fatalf("no picture was written: %v", err)
			}
			if tc.wantExit != 0 && err == nil {
				t.Fatal("a picture was written for a screen that shows a failure")
			}
		})
	}
}

func TestRecordRedacts(t *testing.T) {
	cases := []struct {
		name   string
		scene  string
		rules  []string
		want   string
		unwant []string
	}{
		{
			name:  "a home directory is replaced everywhere it appears",
			rules: []string{"--redact", "/Users/realname=/Users/demouser", "--redact", "realname=demouser"},
			want:  "/Users/demouser/src", unwant: []string{"realname"},
		},
		{
			// The path reaches past the right edge, so the terminal writes it
			// over two rows. Read row by row it holds neither rule.
			name:  "a value the terminal wrapped over two rows is replaced too",
			scene: "size 24 6\nrun cat\ntype cd ~/work/x/Users/realname/src\nenter\nwait 0.4\nframe\n",
			rules: []string{"--redact", "/Users/realname=/Users/demouser", "--redact", "realname=demouser"},
			want:  "demouser", unwant: []string{"realname"},
		},
		{
			name:  "a rule that matches nothing changes nothing",
			rules: []string{"--redact", "/home/other=/home/spare"},
			want:  "/Users/realname/src",
		},
		{
			name:  "the replacement of the first rule is not rewritten by a later one",
			rules: []string{"--redact", "/Users/realname=/Users/demouser", "--redact", "demouser=someuser"},
			want:  "/Users/demouser/src",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecordEnv(t)
			text := tc.scene
			if text == "" {
				text = "size 60 6\nrun cat\ntype cd /Users/realname/src\nenter\nwait 0.4\nframe\n"
			}
			scene := e.scene(t, text)
			keep := filepath.Join(e.dir, "keep")
			_, stderr, exit := runRecord(t, e.env(t),
				append([]string{"--scene", scene, "--still", filepath.Join(e.dir, "x.png"), "--keep", keep}, tc.rules...)...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			frame := string(mustRead(t, filepath.Join(keep, "frames", "0001.ansi")))
			// The screen is a grid: a value long enough is written over two
			// rows, so what it says is read without the breaks between them.
			rows := strings.NewReplacer("\n", "", "\r", "").Replace(frame)
			if !strings.Contains(rows, tc.want) {
				t.Fatalf("the frame does not hold %q:\n%s", tc.want, frame)
			}
			for _, unwant := range tc.unwant {
				if strings.Contains(rows, unwant) {
					t.Fatalf("the frame still holds %q:\n%s", unwant, frame)
				}
			}
		})
	}
}

// TestRecordScenesAreValid parses every scene the repository ships, so a scene
// that no longer matches the recorder is caught by the test suite rather than
// by a documentation rebuild.
func TestRecordScenesAreValid(t *testing.T) {
	scenes, err := filepath.Glob(filepath.Join(repoRoot(t), "docs", "scenes", "*.scene"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) == 0 {
		t.Fatal("no scene files under docs/scenes")
	}
	for _, scene := range scenes {
		t.Run(filepath.Base(scene), func(t *testing.T) {
			e := newRecordEnv(t)
			stdout, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--still", filepath.Join(e.dir, "x.png"), "--dry-run")
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			if !strings.Contains(stdout, "run: ") {
				t.Fatalf("no run directive reported:\n%s", stdout)
			}
		})
	}
}

// TestRecordCaptionsAndChapters covers what turns the recorder into a film
// recorder: the subtitle a frame carries, the band it is drawn in, the title
// card a chapter opens with, and the sidecar track that says the same thing
// at the same moment.
func TestRecordCaptionsAndChapters(t *testing.T) {
	e := newRecordEnv(t)
	scene := e.scene(t, `title A chapter
size 40 8
run cat
chapter Panes and windows
caption lmux split right | a pane beside the agent
frame 0.5
frame 1.5
caption
frame 1
`)
	keep := filepath.Join(e.dir, "keep")
	mp4 := filepath.Join(e.dir, "assets", "demo.mp4")
	srt := filepath.Join(e.dir, "assets", "demo.srt")
	stdout, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--mp4", mp4, "--srt", srt, "--keep", keep)
	if exit != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	// The chapter is announced with the second it starts at, which is what a
	// chapter list is built from.
	if !strings.Contains(stdout, "chapter: 0\tPanes and windows") {
		t.Fatalf("stdout does not report the chapter:\n%s", stdout)
	}
	// The card is a screen the recorder writes, not one it captured.
	card := string(mustRead(t, filepath.Join(keep, "frames", "0001.ansi")))
	if !strings.Contains(card, "Panes and windows") {
		t.Fatalf("the title card does not name the chapter:\n%s", card)
	}
	// One cue for the run of frames that share a caption: it opens when the
	// card ends and lasts as long as those frames do, and its two halves are
	// the command and what it does.
	want := "1\n00:00:02,000 --> 00:00:04,000\nlmux split right\na pane beside the agent\n"
	if got := string(mustRead(t, srt)); !strings.Contains(got, want) {
		t.Fatalf("the subtitle track is\n%s\nwant a cue\n%s", got, want)
	}
	cases := []struct {
		name   string
		frame  string
		want   []string
		unwant string
	}{
		{name: "a captioned frame carries its two lines", frame: "0002.html", want: []string{`id="caption"`, "lmux split right", "a pane beside the agent"}},
		{name: "a frame after an empty caption keeps the band", frame: "0004.html", want: []string{`id="caption"`}, unwant: "lmux split right"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := string(mustRead(t, filepath.Join(keep, "frames", tc.frame)))
			for _, w := range tc.want {
				if !strings.Contains(html, w) {
					t.Fatalf("%s lacks %q", tc.frame, w)
				}
			}
			if tc.unwant != "" && strings.Contains(html, tc.unwant) {
				t.Fatalf("%s still holds %q", tc.frame, tc.unwant)
			}
		})
	}
	// The band is reserved in the window the frames are drawn in, so a film
	// keeps one size and no caption is ever drawn over the status bar.
	log := string(mustRead(t, e.log))
	if !strings.Contains(log, "--window-size=418,337") {
		t.Fatalf("the window does not make room for the band:\n%s", log)
	}
}

// TestRecordBandIsOnlyForFilms pins that a scene which captions nothing is
// drawn exactly as before: the pictures of the documentation keep their size.
func TestRecordBandIsOnlyForFilms(t *testing.T) {
	cases := []struct {
		name  string
		scene string
		want  string
	}{
		{name: "a scene without captions reserves nothing", scene: "title A frame\nsize 20 5\nrun cat\nframe 0.5\n", want: "band: 0"},
		{name: "a caption reserves the band", scene: "title A frame\nsize 20 5\nrun cat\ncaption lmux doctor\nframe 0.5\n", want: "band: 1"},
		{name: "a chapter reserves the band", scene: "title A frame\nsize 20 5\nrun cat\nchapter Install\nframe 0.5\n", want: "band: 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecordEnv(t)
			scene := e.scene(t, tc.scene)
			stdout, stderr, exit := runRecord(t, e.env(t), "--scene", scene, "--still", filepath.Join(e.dir, "x.png"), "--dry-run")
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Fatalf("stdout does not report %q:\n%s", tc.want, stdout)
			}
		})
	}
}

// TestScriptUsageIsOnlyTheHeader keeps every script's --help to the comment it
// is written from. The help is printed by reading a range of lines out of the
// script itself, so a line added to the header spills the shell below it into
// the help, which is how each of these scripts has already been caught once.
func TestScriptUsageIsOnlyTheHeader(t *testing.T) {
	for _, name := range []string{"record.sh", "record-docs.sh", "record-video.sh", "bench.sh"} {
		t.Run(name, func(t *testing.T) {
			bash, err := exec.LookPath("bash")
			if err != nil {
				t.Skip("bash is not installed")
			}
			out, err := exec.Command(bash, scriptPath(t, name), "--help").CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			help := string(out)
			if !strings.Contains(help, "Usage: scripts/"+name) {
				t.Fatalf("the help does not say how to run it:\n%s", help)
			}
			for _, code := range []string{"set -euo pipefail", "#!/usr/bin/env bash", "fail()"} {
				if strings.Contains(help, code) {
					t.Errorf("the help carries the line %q of the script:\n%s", code, help)
				}
			}
		})
	}
}
