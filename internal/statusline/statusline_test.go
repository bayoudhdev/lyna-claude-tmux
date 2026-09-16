package statusline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"

	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook/githead"
)

// fixedNow is 2026-09-15T17:30:00Z; the documented example's five-hour window
// resets 2h05m later.
var fixedNow = time.Unix(1789493400, 0)

// envMap builds a getenv from pairs.
func envMap(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// plainEnv renders without color in the ascii icon set, so assertions read
// as plain text.
func plainEnv(extra map[string]string) func(string) string {
	vars := map[string]string{"NO_COLOR": "1", session.EnvIcons: "ascii"}
	for k, v := range extra {
		vars[k] = v
	}
	return envMap(vars)
}

// headAt returns a reader serving one repository HEAD file.
func headAt(repo, content string) githead.ReadFunc {
	return func(path string, _ int64) ([]byte, error) {
		if path == filepath.Join(repo, ".git", "HEAD") {
			return []byte(content), nil
		}
		return nil, fs.ErrNotExist
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var noRepo githead.ReadFunc = func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist }

func TestRenderSegments(t *testing.T) {
	unreadable := func(string, int64) ([]byte, error) { return nil, fs.ErrPermission }
	cases := []struct {
		name string
		data string
		env  map[string]string
		read githead.ReadFunc
		want string
	}{
		{name: "empty input", data: ``, want: "> Claude"},
		{name: "malformed input", data: `{"model":`, want: "> Claude"},
		{name: "not an object", data: `"Opus"`, want: "> Claude"},
		{name: "model only", data: `{"model":{"display_name":"Opus"}}`, want: "> Opus"},
		{name: "effort joins the model", data: `{"model":{"display_name":"Opus"},"effort":{"level":"xhigh"}}`, want: "> Opus xhigh"},
		{name: "effort without model", data: `{"effort":{"level":"low"}}`, want: "> Claude low"},
		{
			name: "documented example", data: documentedStatus, read: headAt("/home/user/project", "ref: refs/heads/feature-xyz\n"),
			env:  map[string]string{"LYNA_TMUX_SANDBOX": "standard"},
			want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h | style Explanatory",
		},
		{name: "sandbox strict", data: `{}`, env: map[string]string{"LYNA_TMUX_SANDBOX": "strict"}, want: "> Claude | # strict"},
		{name: "sandbox off", data: `{}`, env: map[string]string{"LYNA_TMUX_SANDBOX": "off"}, want: "> Claude | - sandbox off"},
		{name: "unknown sandbox value ignored", data: `{}`, env: map[string]string{"LYNA_TMUX_SANDBOX": "\x1b[31mnone"}, want: "> Claude"},
		{name: "context rounds", data: `{"context_window":{"used_percentage":49.5}}`, want: "> Claude | ctx 50%"},
		{name: "context clamps", data: `{"context_window":{"used_percentage":-3}}`, want: "> Claude | ctx 0%"},
		{name: "context null", data: `{"context_window":{"used_percentage":null}}`, want: "> Claude"},
		{name: "cost cents", data: `{"cost":{"total_cost_usd":12.5}}`, want: "> Claude | $12.50"},
		{name: "cost zero", data: `{"cost":{"total_cost_usd":0}}`, want: "> Claude | $0.00"},
		{name: "cost negative", data: `{"cost":{"total_cost_usd":-2}}`, want: "> Claude | $0.00"},
		{name: "cost whole dollars", data: `{"cost":{"total_cost_usd":1234.99}}`, want: "> Claude | $1234"},
		{name: "cost capped", data: `{"cost":{"total_cost_usd":1e300}}`, want: "> Claude | $999999+"},
		{name: "directory from cwd", data: `{"cwd":"/srv/api"}`, want: "> Claude | api"},
		{name: "workspace directory preferred", data: `{"cwd":"/srv/api","workspace":{"current_dir":"/srv/web"}}`, want: "> Claude | web"},
		{name: "root directory", data: `{"cwd":"/"}`, want: "> Claude | /"},
		{name: "branch from HEAD", data: `{"cwd":"/r/sub"}`, read: headAt("/r", "ref: refs/heads/main\n"), want: "> Claude | sub @ main"},
		{name: "detached HEAD", data: `{"cwd":"/r"}`, read: headAt("/r", strings.Repeat("ab", 20)+"\n"), want: "> Claude | r @ abababa"},
		{name: "outside repository", data: `{"cwd":"/tmp/x","worktree":{"branch":"wt"}}`, read: noRepo, want: "> Claude | x"},
		{name: "unreadable metadata falls back to worktree branch", data: `{"cwd":"/r","worktree":{"branch":"worktree-a"}}`, read: unreadable, want: "> Claude | r @ worktree-a"},
		{name: "relative directory uses worktree branch", data: `{"cwd":"rel","worktree":{"branch":"worktree-b"}}`, read: noRepo, want: "> Claude | rel @ worktree-b"},
		{name: "branch without directory segment", data: `{"worktree":{"branch":"worktree-c"}}`, read: noRepo, want: "> Claude | @ worktree-c"},
		{name: "limit without reset time", data: `{"rate_limits":{"five_hour":{"used_percentage":80}}}`, want: "> Claude | 5h 80%"},
		{name: "expired limit dropped", data: `{"rate_limits":{"five_hour":{"used_percentage":99,"resets_at":1789493400}}}`, want: "> Claude"},
		{name: "limit without percentage dropped", data: `{"rate_limits":{"seven_day":{"resets_at":1789999999}}}`, want: "> Claude"},
		{name: "spend above limit", data: `{"rate_limits":{"spend_limit":{"used_percentage":163.4,"resets_at":1789493460}}}`, want: "> Claude | spend 163% 1m"},
		{name: "default output style hidden", data: `{"output_style":{"name":"default"}}`, want: "> Claude"},
		{name: "output style shown", data: `{"output_style":{"name":"Learning"}}`, want: "> Claude | style Learning"},
		{
			name: "control sequences removed from every field",
			data: mustJSON(t, map[string]any{"model": map[string]string{"display_name": "Op\x1b]52;c;aGk=\x07us\n5"}, "effort": map[string]string{"level": "hi\x1b[2Jgh"}, "cwd": "/p/\u009b31mproj", "output_style": map[string]string{"name": "\u202eevil"}}),
			want: "> Opus 5 high | proj | style evil",
		},
		{
			name: "long field truncated",
			data: `{"model":{"display_name":"` + strings.Repeat("m", 50) + `"}}`,
			want: "> " + strings.Repeat("m", 29) + "...",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read := tc.read
			if read == nil {
				read = noRepo
			}
			got := render([]byte(tc.data), plainEnv(tc.env), fixedNow, read)
			if got != tc.want {
				t.Fatalf("render =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestRenderFitsColumns(t *testing.T) {
	full := documentedStatus
	read := headAt("/home/user/project", "ref: refs/heads/feature-xyz\n")
	cases := []struct {
		columns string
		want    string
	}{
		{columns: "", want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h | style Explanatory"},
		{columns: "abc", want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h | style Explanatory"},
		{columns: "-5", want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h | style Explanatory"},
		// Exactly as wide as the full line keeps everything.
		{columns: "132", want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h | style Explanatory"},
		{columns: "131", want: "> Opus high | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | 7d 41% 3d10h | spend 63% 5d20h"},
		{columns: "100", want: "> Opus | # sandbox | ctx 8% | $0.01 | project @ feature-xyz | 5h 24% 2h05m | spend 63% 5d20h"},
		// Without its directory the branch is separated rather than glued to
		// the segment before it.
		{columns: "80", want: "> Opus | # sandbox | ctx 8% | @ feature-xyz | 5h 24% 2h05m | spend 63% 5d20h"},
		{columns: "40", want: "> Opus | # sandbox | ctx 8%"},
		{columns: "20", want: "> Opus | # sandbox"},
		{columns: "8", want: "> Opus"},
		{columns: "4", want: ">..."},
		{columns: "1", want: ""},
	}
	for _, tc := range cases {
		t.Run("columns="+tc.columns, func(t *testing.T) {
			got := render([]byte(full), plainEnv(map[string]string{"COLUMNS": tc.columns, "LYNA_TMUX_SANDBOX": "standard"}), fixedNow, read)
			if got != tc.want {
				t.Fatalf("render =\n%q\nwant\n%q", got, tc.want)
			}
			if n, err := strconv.Atoi(tc.columns); err == nil && n > 0 && ansi.StringWidth(got) > n {
				t.Fatalf("width %d exceeds %d", ansi.StringWidth(got), n)
			}
		})
	}
}

func TestRenderColoredTruncationResets(t *testing.T) {
	env := envMap(map[string]string{"COLUMNS": "4", session.EnvIcons: "ascii", session.EnvColor: "16"})
	got := render([]byte(`{"model":{"display_name":"Opus"}}`), env, fixedNow, noRepo)
	if want := "\x1b[1;32m>...\x1b[0m"; got != want {
		t.Fatalf("render = %q; want %q", got, want)
	}
}

func TestForeground(t *testing.T) {
	rgb := theme.RGB(0x39, 0xd3, 0x53)
	cases := []struct {
		name  string
		color theme.Color
		depth theme.Depth
		want  string
	}{
		{name: "truecolor", color: rgb, depth: theme.DepthTrue, want: "38;2;57;211;83"},
		{name: "256", color: rgb, depth: theme.Depth256, want: "38;5;" + strconv.Itoa(theme.To256(rgb))},
		{name: "16 bright", color: theme.RGB(0, 250, 10), depth: theme.Depth16, want: "92"},
		{name: "16 normal green", color: rgb, depth: theme.Depth16, want: "32"},
		{name: "16 normal", color: theme.RGB(205, 0, 0), depth: theme.Depth16, want: "31"},
		{name: "indexed ignores truecolor", color: theme.ANSI(4), depth: theme.DepthTrue, want: "34"},
		{name: "indexed bright", color: theme.ANSI(15), depth: theme.Depth256, want: "97"},
		{name: "indexed black", color: theme.ANSI(0), depth: theme.Depth16, want: "30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Foreground(tc.color, tc.depth); got != tc.want {
				t.Fatalf("Foreground = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestStyleFromEnvironment(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantTheme string
		wantIcons string
		wantDepth theme.Depth
		wantColor bool
	}{
		{name: "defaults", env: nil, wantTheme: "lyna", wantIcons: "ascii", wantDepth: theme.Depth16, wantColor: true},
		{name: "configured", env: map[string]string{session.EnvTheme: "light", session.EnvIcons: "nerd", session.EnvColor: "256"}, wantTheme: "light", wantIcons: "nerd", wantDepth: theme.Depth256, wantColor: true},
		{name: "auto detects", env: map[string]string{session.EnvIcons: "auto", session.EnvColor: "auto", "LANG": "en_US.UTF-8", "COLORTERM": "truecolor"}, wantTheme: "lyna", wantIcons: "unicode", wantDepth: theme.DepthTrue, wantColor: true},
		{name: "forced depth wins over detection", env: map[string]string{session.EnvColor: "16", "COLORTERM": "truecolor"}, wantTheme: "lyna", wantIcons: "ascii", wantDepth: theme.Depth16, wantColor: true},
		{name: "unknown values fall back", env: map[string]string{session.EnvTheme: "neon", session.EnvIcons: "emoji", session.EnvColor: "millions", "TERM": "tmux-256color", "LC_ALL": "C.UTF-8"}, wantTheme: "lyna", wantIcons: "unicode", wantDepth: theme.Depth256, wantColor: true},
		{name: "no color", env: map[string]string{"NO_COLOR": "1", session.EnvTheme: "ansi"}, wantTheme: "ansi", wantIcons: "ascii", wantDepth: theme.Depth16, wantColor: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStyle(envMap(tc.env))
			if st.pal.Name != tc.wantTheme || st.icons.Name != tc.wantIcons || st.depth != tc.wantDepth || st.color != tc.wantColor {
				t.Fatalf("style = %s/%s/%v/%v; want %s/%s/%v/%v", st.pal.Name, st.icons.Name, st.depth, st.color, tc.wantTheme, tc.wantIcons, tc.wantDepth, tc.wantColor)
			}
		})
	}
}

func TestUsageLevels(t *testing.T) {
	st := newStyle(envMap(nil))
	cases := []struct {
		pct  float64
		want theme.Color
	}{
		{0, st.pal.Success}, {49.9, st.pal.Success}, {50, st.pal.Warning}, {79.9, st.pal.Warning}, {80, st.pal.Danger}, {163, st.pal.Danger},
	}
	for _, tc := range cases {
		if got := st.level(tc.pct); got != tc.want {
			t.Fatalf("level(%v) = %+v; want %+v", tc.pct, got, tc.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{59*time.Minute + time.Second, "1h00m"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d00h"},
		{50*time.Hour + 10*time.Minute, "2d02h"},
		{365 * 24 * time.Hour, "365d00h"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Fatalf("formatDuration(%v) = %q; want %q", tc.d, got, tc.want)
		}
	}
}

func TestUntilReset(t *testing.T) {
	cases := []struct {
		name     string
		resetsAt float64
		want     time.Duration
		wantOK   bool
	}{
		{name: "future", resetsAt: 1789493400 + 90, want: 90 * time.Second, wantOK: true},
		{name: "now", resetsAt: 1789493400},
		{name: "past", resetsAt: 1},
		{name: "far future capped at a year", resetsAt: 1e300, want: 365 * 24 * time.Hour, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := untilReset(tc.resetsAt, fixedNow)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("untilReset = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestRun(t *testing.T) {
	cases := []struct {
		name  string
		stdin io.Reader
		want  string
	}{
		{name: "renders stdin", stdin: strings.NewReader(`{"model":{"display_name":"Opus"}}`), want: "> Opus\n"},
		{name: "no stdin", stdin: nil, want: "> Claude\n"},
		{name: "unreadable stdin", stdin: errReader{}, want: "> Claude\n"},
		{name: "oversized stdin", stdin: io.MultiReader(strings.NewReader(`{"model":{"display_name":"Opus"},"x":"`), strings.NewReader(strings.Repeat("x", MaxStdinBytes))), want: "> Claude\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if status := Run(tc.stdin, &out, plainEnv(nil), fixedNow); status != 0 {
				t.Fatalf("status %d", status)
			}
			if out.String() != tc.want {
				t.Fatalf("output %q; want %q", out.String(), tc.want)
			}
		})
	}
}

func TestRenderReadsRepositoryOnDisk(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/release/2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"workspace":{"current_dir":"` + repo + `"}}`)
	got := Render(data, plainEnv(nil), fixedNow)
	if want := "> Claude | " + filepath.Base(repo) + " @ release/2.0"; got != want {
		t.Fatalf("Render = %q; want %q", got, want)
	}
	// A nil environment renders the defaults.
	if got := Render([]byte(`{}`), nil, fixedNow); !strings.Contains(got, "Claude") {
		t.Fatalf("Render with nil getenv = %q", got)
	}
}
