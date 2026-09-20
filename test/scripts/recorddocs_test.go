package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeRecord answers a dry run with the frame count encoded in the scene file
// and records every real call, so the driver can be tested without a browser.
const fakeRecord = `#!/bin/sh
scene=""
dry=0
for arg in "$@"; do
  case $arg in
  --dry-run) dry=1 ;;
  esac
done
prev=""
for arg in "$@"; do
  if [ "$prev" = --scene ]; then scene=$arg; fi
  prev=$arg
done
if [ "$dry" = 1 ]; then
  grep '^frames:' "$scene"
  exit 0
fi
echo "record $* cwd=$PWD path=$PATH shell=$SHELL" >> "$DOCS_LOG"
if [ -f "$XDG_CONFIG_HOME/lyna-tmux/config.toml" ]; then
  echo "config $(tr '\n' ' ' < "$XDG_CONFIG_HOME/lyna-tmux/config.toml")" >> "$DOCS_LOG"
fi
`

// fakeLyna answers the two commands the driver needs of the binary: writing
// the recording configuration and saying where it is.
const fakeLyna = `#!/bin/sh
echo "lmux $*" >> "$DOCS_LOG"
config=$XDG_CONFIG_HOME/lyna-tmux/config.toml
if [ "$1" = config ] && [ "$2" = init ]; then
  mkdir -p "$(dirname "$config")"
  icons='icons = "auto"'
  [ -z "$FAKE_ICONS_RENAMED" ] || icons='icon_set = "auto"'
  printf '[ui]\ntheme = "monokai"\n%s\nstatus_style = "auto"\n' "$icons" > "$config"
fi
if [ "$1" = config ] && [ "$2" = path ]; then
  echo "$config"
fi
if [ "$1" = review ] && [ "$2" = status ]; then
  [ -z "$FAKE_REVIEW_MISSING" ] || exit 1
fi
`

type docsEnv struct {
	dir, bin, scenes, assets, project, record, log string
}

func newDocsEnv(t *testing.T, scenes map[string]string) docsEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("record-docs.sh needs a POSIX host")
	}
	root := t.TempDir()
	e := docsEnv{
		dir:     root,
		bin:     filepath.Join(root, "bin"),
		scenes:  filepath.Join(root, "scenes"),
		assets:  filepath.Join(root, "assets"),
		project: filepath.Join(root, "project"),
		record:  filepath.Join(root, "bin", "record.sh"),
		log:     filepath.Join(root, "calls.log"),
	}
	for _, dir := range []string{e.bin, e.scenes, e.assets, e.project, filepath.Join(e.scenes, "fixtures", "bin"), filepath.Join(e.scenes, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, e.record, fakeRecord)
	writeExecutable(t, filepath.Join(e.bin, "lmux"), fakeLyna)
	for name, body := range scenes {
		if err := os.WriteFile(filepath.Join(e.scenes, name+".scene"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e docsEnv) env() []string {
	return []string{"PATH=" + e.bin + ":/usr/bin:/bin", "DOCS_LOG=" + e.log, "HOME=" + e.dir, "SHELL=" + accountShell}
}

func (e docsEnv) args(extra ...string) []string {
	return append([]string{
		"--project", e.project, "--scenes", e.scenes, "--assets", e.assets, "--record", e.record,
	}, extra...)
}

func runRecordDocs(t *testing.T, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	cmd := exec.Command(bash, append([]string{scriptPath(t, "record-docs.sh")}, args...)...)
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

// TestRecordDocsConfiguration checks the configuration the scenes run against:
// written by the binary itself rather than taken from the account, and set to
// the icon set the patched font the frames are drawn with is for, which is
// what gives the status bar its pointed separators.
func TestRecordDocsConfiguration(t *testing.T) {
	e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
	_, stderr, exit := runRecordDocs(t, e.env(), e.args()...)
	if exit != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	log := string(mustRead(t, e.log))
	if !strings.Contains(log, "lmux config init") {
		t.Fatalf("no configuration was written:\n%s", log)
	}
	// The recorder takes its configuration away with it, so what the scenes
	// ran against is read from what the recorder was handed.
	got := log
	cases := []struct {
		name string
		want string
	}{
		{name: "the icon set of the recording font", want: `icons = "nerd"`},
		{name: "the theme a workspace opens with", want: `theme = "monokai"`},
		{name: "the status bar follows the icon set", want: `status_style = "auto"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Fatalf("the recording configuration lacks %q:\n%s", tc.want, got)
			}
		})
	}
	for _, dir := range []string{".config", ".reccfg"} {
		if _, err := os.Stat(filepath.Join(e.dir, dir, "lyna-tmux")); err == nil {
			t.Errorf("the recorder left %s behind in the account", dir)
		}
	}
}

// TestRecordDocsNeedsTheIconSet holds the driver to the configuration it
// edits: the scenes are drawn with a patched font and must run with the icon
// set that font is for, so a template that no longer names the key it reads
// records nothing rather than a picture drawn with the wrong glyphs.
func TestRecordDocsNeedsTheIconSet(t *testing.T) {
	e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
	_, stderr, exit := runRecordDocs(t, append(e.env(), "FAKE_ICONS_RENAMED=1"), e.args()...)
	if exit != 1 || !strings.Contains(stderr, "no longer sets the icon set") {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	if _, err := os.Stat(e.log); err == nil {
		if log := string(mustRead(t, e.log)); strings.Contains(log, "record ") {
			t.Fatalf("a scene was recorded anyway:\n%s", log)
		}
	}
}

// TestRecordDocsNeedsTheReviewPlugin holds the driver to what the scenes
// need: a machine whose review cannot start would be recorded showing the
// warning rather than the editor, so nothing is recorded at all.
func TestRecordDocsNeedsTheReviewPlugin(t *testing.T) {
	e := newDocsEnv(t, map[string]string{"review": "frames: 3\n"})
	_, stderr, exit := runRecordDocs(t, append(e.env(), "FAKE_REVIEW_MISSING=1"), e.args()...)
	if exit != 1 || !strings.Contains(stderr, "lmux review install") {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	if _, err := os.Stat(e.log); err == nil {
		if log := string(mustRead(t, e.log)); strings.Contains(log, "record ") {
			t.Fatalf("a scene was recorded anyway:\n%s", log)
		}
	}
}

// TestRecordDocsOutputs pins the rule that decides what a scene produces: one
// frame is a picture, several frames are an animation and a picture of the
// last one.
func TestRecordDocsOutputs(t *testing.T) {
	e := newDocsEnv(t, map[string]string{
		"doctor": "frames: 1\n",
		"split":  "frames: 6\n",
	})
	stdout, stderr, exit := runRecordDocs(t, e.env(), e.args()...)
	if exit != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
	}
	if !strings.Contains(stdout, "recorded 2 scenes") {
		t.Fatalf("stdout:\n%s", stdout)
	}
	log := string(mustRead(t, e.log))
	cases := []struct {
		name    string
		want    []string
		unwant  []string
		wantCwd string
	}{
		{
			name:    "a single frame scene writes only a picture",
			want:    []string{"--scene " + e.scenes + "/doctor.scene", "--still " + e.assets + "/doctor.png"},
			unwant:  []string{"doctor.gif"},
			wantCwd: "cwd=" + e.project,
		},
		{
			name:    "a scene with several frames writes an animation too",
			want:    []string{"--scene " + e.scenes + "/split.scene", "--still " + e.assets + "/split.png", "--out " + e.assets + "/split.gif"},
			wantCwd: "cwd=" + e.project,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Fatalf("the recorder was not called with %q:\n%s", want, log)
				}
			}
			for _, unwant := range tc.unwant {
				if strings.Contains(log, unwant) {
					t.Fatalf("the recorder was called with %q:\n%s", unwant, log)
				}
			}
			if !strings.Contains(log, tc.wantCwd) {
				t.Fatalf("the recorder did not run in the project:\n%s", log)
			}
		})
	}
}

// TestRecordDocsRedactsTheAccount pins the promise of the driver: nothing it
// records can name the account it was recorded in.
// defaultHome is the neutral account name record-docs.sh derives when no
// --home is given: the word padded to the length of the real one.
func defaultHome(account string) string {
	name := "developer"
	for len(name) < len(account) {
		name += "x"
	}
	return name[:len(account)]
}

func TestRecordDocsRedactsTheAccount(t *testing.T) {
	user, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Skip("id is not available")
	}
	account := strings.TrimSpace(string(user))
	// A name as long as the account keeps every column where it was, which is
	// the property the driver warns about when it does not hold.
	sameLength := strings.Repeat("d", len(account))
	cases := []struct {
		name     string
		args     []string
		want     []string
		wantErr  string
		wantExit int
	}{
		{
			name: "the home directory and the account name are rewritten",
			args: []string{"--home", sameLength},
			want: []string{"--redact HOME=HOMEDIR/" + sameLength, "--redact " + account + "=" + sameLength},
		},
		{
			// Nobody has to count the letters of their own account name.
			name: "the default name is as long as the account",
			want: []string{"--redact " + account + "=" + defaultHome(account)},
		},
		{
			// A shorter or longer name would pull every status bar and box
			// border out of line, and could not be written over a value the
			// terminal wrapped, so nothing is recorded with one.
			name:     "a replacement of another length is refused",
			args:     []string{"--home", sameLength + "x"},
			wantErr:  account + " is " + strconv.Itoa(len(account)) + " characters",
			wantExit: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
			_, stderr, exit := runRecordDocs(t, e.env(), e.args(tc.args...)...)
			if exit != tc.wantExit {
				t.Fatalf("exit %d, want %d\nstderr:\n%s", exit, tc.wantExit, stderr)
			}
			if tc.wantExit != 0 {
				if !strings.Contains(stderr, tc.wantErr) {
					t.Fatalf("stderr does not report %q:\n%s", tc.wantErr, stderr)
				}
				if _, err := os.Stat(e.log); err == nil {
					t.Fatalf("a refused redaction still recorded a scene:\n%s", mustRead(t, e.log))
				}
				return
			}
			log := string(mustRead(t, e.log))
			log = strings.ReplaceAll(log, e.dir, "HOME")
			log = strings.ReplaceAll(log, filepath.Dir(e.dir), "HOMEDIR")
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Fatalf("the recorder was not called with %q:\n%s", want, log)
				}
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr does not report %q:\n%s", tc.wantErr, stderr)
			}
			if tc.wantErr == "" && strings.Contains(stderr, account+" is ") {
				t.Fatalf("a same length account name was reported as a shift:\n%s", stderr)
			}
		})
	}
}

// TestRecordDocsRedactsTheAddress pins the other half of that promise: the
// address git commits under is on screen wherever a recording shows a commit,
// and it is the recorder's own.
func TestRecordDocsRedactsTheAddress(t *testing.T) {
	const address = "someone@example.invalid"
	cases := []struct {
		name     string
		env      []string
		args     []string
		want     []string
		wantErr  string
		wantExit int
	}{
		{
			name: "the address git would commit under is rewritten",
			env:  []string{"GIT_AUTHOR_EMAIL=" + address},
			want: []string{"--redact " + address + "=dev@example.comxxxxxxxx"},
		},
		{
			name: "every address git reads is rewritten once",
			env:  []string{"GIT_AUTHOR_EMAIL=" + address, "GIT_COMMITTER_EMAIL=" + address, "EMAIL=other@example.invalid"},
			want: []string{"--redact " + address + "=", "--redact other@example.invalid="},
		},
		{
			name: "anything else the recorder names goes too",
			args: []string{"--redact", "acme-internal=demo-project"},
			want: []string{"--redact acme-internal=demo-project"},
		},
		{
			name:     "a redaction that is not a pair is refused",
			args:     []string{"--redact", "acme-internal"},
			wantErr:  "--redact takes FROM=TO",
			wantExit: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
			_, stderr, exit := runRecordDocs(t, append(e.env(), tc.env...), e.args(tc.args...)...)
			if exit != tc.wantExit {
				t.Fatalf("exit %d, want %d\nstderr:\n%s", exit, tc.wantExit, stderr)
			}
			if tc.wantExit != 0 {
				if !strings.Contains(stderr, tc.wantErr) {
					t.Fatalf("stderr does not report %q:\n%s", tc.wantErr, stderr)
				}
				return
			}
			log := string(mustRead(t, e.log))
			for _, want := range tc.want {
				if !strings.Contains(log, want) {
					t.Fatalf("the recorder was not called with %q:\n%s", want, log)
				}
			}
			if strings.Count(log, "--redact "+address+"=") > 1 {
				t.Fatalf("%s is rewritten more than once:\n%s", address, log)
			}
		})
	}
}

// TestRecordDocsFixtures pins which scenes see the fixture binaries: they
// answer for every scene by default, and a scene that prints the path of a
// real installation opts out of them.
func TestRecordDocsFixtures(t *testing.T) {
	cases := []struct {
		name  string
		scene string
		want  bool
	}{
		{name: "a scene sees the fixtures", scene: "frames: 1\n", want: true},
		{name: "a scene can opt out of them", scene: "# fixtures: off\nframes: 1\n"},
		{name: "the opt out is a whole line", scene: "# not about fixtures: off here\nframes: 1\n", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": tc.scene})
			_, stderr, exit := runRecordDocs(t, e.env(), e.args()...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			fixtures := filepath.Join(e.scenes, "fixtures", "bin")
			got := strings.Contains(string(mustRead(t, e.log)), "path="+fixtures+":")
			if got != tc.want {
				t.Fatalf("the fixtures on PATH: %v, want %v", got, tc.want)
			}
		})
	}
}

// accountShell stands for the shell of the account a recording is made in.
const accountShell = "/the/account/shell"

// TestRecordDocsShell pins the shell the scenes open panes with: the account's
// own would draw its own prompt and read its own start-up files into every
// picture.
func TestRecordDocsShell(t *testing.T) {
	cases := []struct {
		name    string
		fixture bool
	}{
		{name: "the scenes run the recording shell", fixture: true},
		{name: "without it the account's own shell is left alone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
			shell := filepath.Join(e.scenes, "bin", "demo-shell")
			want := accountShell
			if tc.fixture {
				writeExecutable(t, shell, "#!/bin/sh\nexit 0\n")
				want = shell
			}
			_, stderr, exit := runRecordDocs(t, e.env(), e.args()...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			if got := string(mustRead(t, e.log)); !strings.Contains(got, "shell="+want) {
				t.Fatalf("log does not hold shell=%s:\n%s", want, got)
			}
		})
	}
}

func TestRecordDocsArguments(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		noLyna   bool
		wantExit int
		wantOut  string
		wantErr  string
		wantLog  []string
		wantNoun string
	}{
		{name: "help", args: []string{"--help"}, wantOut: "Usage: scripts/record-docs.sh --project DIR"},
		{name: "unknown argument", args: []string{"--fast"}, wantExit: 2, wantErr: "unknown argument: --fast"},
		{name: "missing value", args: []string{"--project"}, wantExit: 2, wantErr: "--project needs a value"},
		{name: "no project", args: nil, wantExit: 2, wantErr: "--project is required"},
		{
			name: "one scene", args: []string{"--only", "split"},
			wantOut: "recorded 1 scenes", wantLog: []string{"split.scene"}, wantNoun: "doctor.scene",
		},
		{
			name: "a scene name with its extension", args: []string{"--only", "doctor.scene"},
			wantOut: "recorded 1 scenes", wantLog: []string{"doctor.scene"}, wantNoun: "split.scene",
		},
		{name: "no scene matches", args: []string{"--only", "nothing"}, wantExit: 1, wantErr: "no scene matched"},
		{name: "a dry run records nothing", args: []string{"--dry-run"}, wantOut: "split: 6 frames"},
		{name: "the binary must be on PATH", noLyna: true, wantExit: 1, wantErr: "lmux is not on PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n", "split": "frames: 6\n"})
			if tc.noLyna {
				if err := os.Remove(filepath.Join(e.bin, "lmux")); err != nil {
					t.Fatal(err)
				}
			}
			args := tc.args
			if tc.name != "help" && tc.name != "unknown argument" && tc.name != "missing value" && tc.name != "no project" {
				args = e.args(tc.args...)
			}
			stdout, stderr, exit := runRecordDocs(t, e.env(), args...)
			if exit != tc.wantExit || !strings.Contains(stdout, tc.wantOut) || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			log := ""
			if data, err := os.ReadFile(e.log); err == nil {
				log = string(data)
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(log, want) {
					t.Fatalf("the recorder was not called for %q:\n%s", want, log)
				}
			}
			if tc.wantNoun != "" && strings.Contains(log, tc.wantNoun) {
				t.Fatalf("the recorder was called for %q, which was not asked for:\n%s", tc.wantNoun, log)
			}
			if strings.Contains(tc.name, "dry run") && log != "" {
				t.Fatalf("a dry run ran the recorder:\n%s", log)
			}
		})
	}
}

// TestRecordDocsEndsItsWorkspaces pins what a pass leaves behind: the scenes
// open workspaces on the recorder's own server, and the last thing the driver
// does is end them, while the binary and the configuration they were opened
// with are still in place.
func TestRecordDocsEndsItsWorkspaces(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "a pass ends what it opened", want: true},
		{name: "a dry run opened nothing to end", args: []string{"--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDocsEnv(t, map[string]string{"doctor": "frames: 1\n"})
			_, stderr, exit := runRecordDocs(t, e.env(), e.args(tc.args...)...)
			if exit != 0 {
				t.Fatalf("exit %d\nstderr:\n%s", exit, stderr)
			}
			log := ""
			if data, err := os.ReadFile(e.log); err == nil {
				log = string(data)
			}
			if got := strings.Contains(log, "lmux kill --all"); got != tc.want {
				t.Fatalf("every workspace ended: %v, want %v\n%s", got, tc.want, log)
			}
			if !tc.want {
				return
			}
			if last := strings.LastIndex(log, "lmux kill --all"); last < strings.LastIndex(log, "record ") {
				t.Fatalf("a scene was recorded after the workspaces were ended:\n%s", log)
			}
		})
	}
}
