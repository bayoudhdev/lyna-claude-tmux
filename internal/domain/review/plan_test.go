package review

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseEditorMode(t *testing.T) {
	cases := []struct {
		in      string
		want    EditorMode
		wantErr bool
	}{
		{in: "", want: EditorIsolated},
		{in: "isolated", want: EditorIsolated},
		{in: "user", want: EditorUser},
		{in: "system", want: EditorIsolated, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseEditorMode(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("ParseEditorMode(%q) = %q, %v; want %q, err %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestNewPlan(t *testing.T) {
	opts := PlanOptions{Nvim: "/usr/bin/nvim", InitFile: "/d/review/init.lua", LogFile: "/d/review/nvim.log", Dir: "/src/my repo"}
	cases := []struct {
		name     string
		req      Request
		mode     EditorMode
		wantArgv []string
		wantEnv  []string
	}{
		{
			name:     "isolated changes",
			req:      Request{},
			mode:     EditorIsolated,
			wantArgv: []string{"/usr/bin/nvim", "--clean", "-u", "/d/review/init.lua", "-i", "NONE", "-n", "-c", Launcher},
			wantEnv: []string{
				"VSCODE_DIFF_NO_AUTO_INSTALL=1",
				"CODEDIFF_WATCHER_NO_AUTO_INSTALL=1",
				"CODEDIFF_WATCHER_PATH=",
				"NVIM_LOG_FILE=/d/review/nvim.log",
				"LYNA_TMUX_REVIEW_DIR=/src/my repo",
				`LYNA_TMUX_REVIEW_ARGS=["--exit-on-close"]`,
			},
		},
		{
			name:     "isolated paths keep html characters and quotes",
			req:      Request{Paths: []string{`a <b> & "c"`, `d\e`}},
			mode:     EditorIsolated,
			wantArgv: []string{"/usr/bin/nvim", "--clean", "-u", "/d/review/init.lua", "-i", "NONE", "-n", "-c", Launcher},
			wantEnv: []string{
				"VSCODE_DIFF_NO_AUTO_INSTALL=1",
				"CODEDIFF_WATCHER_NO_AUTO_INSTALL=1",
				"CODEDIFF_WATCHER_PATH=",
				"NVIM_LOG_FILE=/d/review/nvim.log",
				"LYNA_TMUX_REVIEW_DIR=/src/my repo",
				`LYNA_TMUX_REVIEW_ARGS=["--exit-on-close","--","a <b> & \"c\"","d\\e"]`,
			},
		},
		{
			name:     "user pr",
			req:      Request{Mode: ModePR, PR: PR{Number: 9}},
			mode:     EditorUser,
			wantArgv: []string{"/usr/bin/nvim", "-c", Launcher},
			wantEnv:  []string{"LYNA_TMUX_REVIEW_DIR=/src/my repo", `LYNA_TMUX_REVIEW_ARGS=["pr","9","--exit-on-close"]`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := NewPlan(tc.req, tc.mode, opts)
			if err != nil {
				t.Fatalf("NewPlan() error = %v", err)
			}
			if !slices.Equal(plan.Argv, tc.wantArgv) {
				t.Errorf("Argv = %q, want %q", plan.Argv, tc.wantArgv)
			}
			if !slices.Equal(plan.Env, tc.wantEnv) {
				t.Errorf("Env = %q, want %q", plan.Env, tc.wantEnv)
			}
			// The encoded arguments decode back to Fargs exactly.
			want, _ := tc.req.Fargs()
			encoded := strings.TrimPrefix(plan.Env[len(plan.Env)-1], EnvArgs+"=")
			var got []string
			if err := json.Unmarshal([]byte(encoded), &got); err != nil || !slices.Equal(got, want) {
				t.Errorf("decoded args = %q (%v), want %q", got, err, want)
			}
		})
	}
}

func TestNewPlanErrors(t *testing.T) {
	good := PlanOptions{Nvim: "/bin/nvim", InitFile: "/r/init.lua", LogFile: "/r/nvim.log", Dir: "/src"}
	cases := []struct {
		name    string
		req     Request
		mode    EditorMode
		opts    PlanOptions
		wantErr error
	}{
		{name: "invalid request", req: Request{Paths: []string{"-x"}}, mode: EditorIsolated, opts: good, wantErr: ErrInvalidRequest},
		{name: "relative nvim", mode: EditorUser, opts: PlanOptions{Nvim: "nvim", Dir: "/src"}, wantErr: ErrInvalidPlan},
		{name: "missing directory", mode: EditorUser, opts: PlanOptions{Nvim: "/bin/nvim"}, wantErr: ErrInvalidPlan},
		{name: "relative directory", mode: EditorIsolated, opts: PlanOptions{Nvim: "/bin/nvim", InitFile: "/r/init.lua", LogFile: "/r/nvim.log", Dir: "src"}, wantErr: ErrInvalidPlan},
		{name: "isolated without init", mode: EditorIsolated, opts: PlanOptions{Nvim: "/bin/nvim", LogFile: "/r/nvim.log", Dir: "/src"}, wantErr: ErrInvalidPlan},
		{name: "isolated relative log", mode: EditorIsolated, opts: PlanOptions{Nvim: "/bin/nvim", InitFile: "/r/init.lua", LogFile: "nvim.log", Dir: "/src"}, wantErr: ErrInvalidPlan},
		{name: "unknown mode", mode: "system", opts: good, wantErr: ErrInvalidPlan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPlan(tc.req, tc.mode, tc.opts); !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewPlan() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestLauncherShape pins the properties the launcher relies on: it is a
// single Ex line (a newline would end the -c command), it reads its data from
// the environment only and it reports each failure with its exit code.
func TestLauncherShape(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "starts with the lua command", ok: strings.HasPrefix(Launcher, "lua ")},
		{name: "single line", ok: !strings.ContainsAny(Launcher, "\n\r")},
		{name: "reads the argument variable", ok: strings.Contains(Launcher, "vim.env.LYNA_TMUX_REVIEW_ARGS")},
		{name: "reads the directory variable", ok: strings.Contains(Launcher, "local dir = vim.env.LYNA_TMUX_REVIEW_DIR")},
		{name: "enters the directory before the command", ok: strings.Contains(Launcher, "pcall(vim.api.nvim_set_current_dir, dir)") &&
			strings.Index(Launcher, "pcall(vim.api.nvim_set_current_dir, dir)") < strings.Index(Launcher, `vim.fn.exists(":CodeDiff")`)},
		{name: "calls nvim_cmd with a list", ok: strings.Contains(Launcher, `pcall(vim.api.nvim_cmd, { cmd = "CodeDiff", args = args }, {})`)},
		{name: "unavailable exit code", ok: strings.Contains(Launcher, "fail(3, ")},
		{name: "failed exit code", ok: strings.Contains(Launcher, "fail(4, ")},
		{name: "checks the setup error", ok: strings.Contains(Launcher, "vim.g."+SetupErrorVar)},
		{name: "quits with the code", ok: strings.Contains(Launcher, `vim.cmd("cquit " .. code)`)},
		{name: "names the install command", ok: strings.Contains(Launcher, "lmux review install")},
		{name: "exit codes match constants", ok: ExitUnavailable == 3 && ExitFailed == 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.ok {
				t.Fatalf("launcher property %q does not hold:\n%s", tc.name, Launcher)
			}
		})
	}
}
