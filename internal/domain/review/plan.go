package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// EditorMode selects which Neovim setup runs the review.
type EditorMode string

// Editor modes.
const (
	// EditorIsolated runs Neovim without the user's configuration, with the
	// pinned plugin lyna-tmux installed into its own data directory.
	EditorIsolated EditorMode = "isolated"
	// EditorUser runs the user's own Neovim with their own codediff.nvim.
	EditorUser EditorMode = "user"
)

// ParseEditorMode parses isolated or user; "" means isolated.
func ParseEditorMode(s string) (EditorMode, error) {
	switch s {
	case "", string(EditorIsolated):
		return EditorIsolated, nil
	case string(EditorUser):
		return EditorUser, nil
	}
	return EditorIsolated, fmt.Errorf("review: unknown editor mode %q (want isolated or user)", s)
}

// Environment variables of the review editor.
const (
	// EnvArgs carries the :CodeDiff arguments as a JSON array of strings. An
	// environment variable keeps data out of every command line Neovim parses.
	EnvArgs = "LYNA_TMUX_REVIEW_ARGS"
	// EnvDir is the directory the review opens in. The launcher enters it
	// before :CodeDiff runs, so the review does not depend on the working
	// directory Neovim inherited: a command that replaces its own process
	// with Neovim keeps its caller's.
	EnvDir = "LYNA_TMUX_REVIEW_DIR"
	// EnvNoAutoInstall stops the plugin from downloading its native library
	// without verification; the installer provides a checksum-verified copy.
	EnvNoAutoInstall = "VSCODE_DIFF_NO_AUTO_INSTALL"
	// EnvNoWatcherInstall stops the plugin from downloading and running an
	// unverified file watcher executable. The explorer then refreshes by polling.
	EnvNoWatcherInstall = "CODEDIFF_WATCHER_NO_AUTO_INSTALL"
	// EnvWatcherPath would name a watcher executable to run; it is cleared.
	EnvWatcherPath = "CODEDIFF_WATCHER_PATH"
	// EnvNvimLog is Neovim's log file, which otherwise lands in the user's
	// Neovim state directory.
	EnvNvimLog = "NVIM_LOG_FILE"
)

// Exit codes of the launcher.
const (
	// ExitUnavailable means :CodeDiff does not exist in the editor.
	ExitUnavailable = 3
	// ExitFailed means the arguments were unreadable, the plugin setup failed
	// or :CodeDiff raised an error.
	ExitFailed = 4
)

// SetupErrorVar is the global variable the generated init file sets when
// the plugin's setup() fails, so the launcher can report the cause.
const SetupErrorVar = "lyna_tmux_review_setup_error"

// Launcher is the Ex command that opens the review. It is one constant, never
// built from data: the arguments come from EnvArgs and reach :CodeDiff through
// nvim_cmd, which hands each list element to the command as one argument and
// interprets no command separators, and the directory comes from EnvDir and is
// entered through nvim_set_current_dir. Failures exit with ExitUnavailable or
// ExitFailed; with a UI attached the message stays on screen until a key is
// pressed, because a closing popup would take it away.
const Launcher = `lua ` +
	`local function fail(code, msg) ` +
	`msg = "lyna-tmux review: " .. msg ` +
	`if #vim.api.nvim_list_uis() > 0 then ` +
	`vim.cmd("redraw") ` +
	`vim.api.nvim_echo({ { msg, "ErrorMsg" }, { "\nPress any key to close", "MoreMsg" } }, false, {}) ` +
	`pcall(vim.fn.getchar) ` +
	`else io.stderr:write(msg, "\n") end ` +
	`vim.cmd("cquit " .. code) ` +
	`end ` +
	`local ok, args = pcall(vim.json.decode, vim.env.` + EnvArgs + ` or "") ` +
	`if not ok or type(args) ~= "table" or not (vim.islist or vim.tbl_islist)(args) then ` +
	`return fail(4, "` + EnvArgs + ` is not a JSON array") end ` +
	`for _, a in ipairs(args) do if type(a) ~= "string" then ` +
	`return fail(4, "` + EnvArgs + ` must hold strings only") end end ` +
	`local dir = vim.env.` + EnvDir + ` ` +
	`if dir and dir ~= "" then ` +
	`local entered, cderr = pcall(vim.api.nvim_set_current_dir, dir) ` +
	`if not entered then return fail(4, "cannot enter " .. dir .. ": " .. tostring(cderr)) end end ` +
	`if vim.fn.exists(":CodeDiff") ~= 2 then ` +
	`return fail(3, "the :CodeDiff command is not available. Install it with: lyna-tmux review install") end ` +
	`if vim.g.` + SetupErrorVar + ` then ` +
	`return fail(4, "codediff.nvim setup failed: " .. tostring(vim.g.` + SetupErrorVar + `)) end ` +
	`local ran, err = pcall(vim.api.nvim_cmd, { cmd = "CodeDiff", args = args }, {}) ` +
	`if not ran then return fail(4, tostring(err)) end`

// PlanOptions are the installation facts a plan needs.
type PlanOptions struct {
	// Nvim is the absolute path of the Neovim binary.
	Nvim string
	// InitFile is the generated init file (isolated mode).
	InitFile string
	// LogFile receives Neovim's log (isolated mode).
	LogFile string
	// Dir is the absolute directory the review opens in.
	Dir string
}

// Plan is how to start the review editor.
type Plan struct {
	// Argv is the command line; Argv[0] is the Neovim binary.
	Argv []string
	// Env holds the KEY=VALUE pairs to set on top of the inherited environment.
	Env []string
}

// ErrInvalidPlan reports unusable plan options.
var ErrInvalidPlan = errors.New("invalid review launch")

// NewPlan builds the editor command for a validated request.
func NewPlan(req Request, mode EditorMode, opts PlanOptions) (Plan, error) {
	fargs, err := req.Fargs()
	if err != nil {
		return Plan{}, err
	}
	if !filepath.IsAbs(opts.Nvim) {
		return Plan{}, fmt.Errorf("%w: Neovim path %q is not absolute", ErrInvalidPlan, opts.Nvim)
	}
	if !filepath.IsAbs(opts.Dir) {
		return Plan{}, fmt.Errorf("%w: review directory %q is not absolute", ErrInvalidPlan, opts.Dir)
	}
	// The arguments stay last so a reader of the environment finds the
	// review data together at the end.
	reviewEnv := []string{EnvDir + "=" + opts.Dir, EnvArgs + "=" + encodeArgs(fargs)}
	switch mode {
	case EditorIsolated:
		if !filepath.IsAbs(opts.InitFile) || !filepath.IsAbs(opts.LogFile) {
			return Plan{}, fmt.Errorf("%w: isolated mode needs absolute init and log files", ErrInvalidPlan)
		}
		return Plan{
			Argv: []string{opts.Nvim, "--clean", "-u", opts.InitFile, "-i", "NONE", "-n", "-c", Launcher},
			Env: append([]string{
				EnvNoAutoInstall + "=1",
				EnvNoWatcherInstall + "=1",
				EnvWatcherPath + "=",
				EnvNvimLog + "=" + opts.LogFile,
			}, reviewEnv...),
		}, nil
	case EditorUser:
		return Plan{
			Argv: []string{opts.Nvim, "-c", Launcher},
			Env:  reviewEnv,
		}, nil
	}
	return Plan{}, fmt.Errorf("%w: unknown editor mode %q", ErrInvalidPlan, mode)
}

// encodeArgs renders the arguments as compact JSON. HTML escaping is off so
// the value stays readable in process listings; it changes no decoded value.
// Validation guarantees valid UTF-8, so no string is altered by encoding.
func encodeArgs(args []string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(args) // a string slice always encodes
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}
