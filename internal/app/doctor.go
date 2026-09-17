package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/doctor"
	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Doctor rows the app layer adds around the machine checks.
const (
	doctorConfigID = "config"
	doctorNestedID = "nesting"
	doctorReviewID = "review"
	// doctorShortCommit is how much of a commit id a report line shows.
	doctorShortCommit = 12
)

// DoctorRun runs every doctor check as a launch from h would see the machine.
// sys supplies the system access (doctor.System in production); its
// environment and PATH lookup are replaced by the host's, and the Claude
// directory, the minimum Claude Code release and the configured keys and
// sandbox are filled in. dir is the working directory the report is about:
// its project root decides the Claude Code trust check, and an empty dir, or
// one outside a project, skips that row. A configuration that does not load
// is a failed row and the other checks run with the defaults. The report
// starts with the configuration and ends with the review editor.
func DoctorRun(ctx context.Context, h Host, sys doctor.Deps, dir string) doctor.Report {
	cfg := config.Default()
	row := doctor.Result{ID: doctorConfigID, Title: "Configuration"}
	paths, pathsErr := xdg.Resolve(h.Getenv, h.Home)
	if pathsErr != nil {
		row.Status, row.Detail, row.Fix = doctor.StatusFail, pathsErr.Error(), "set HOME, or LYNA_TMUX_HOME to an absolute directory"
	} else {
		loaded, found, err := config.Load(paths.ConfigFile())
		switch {
		case err != nil:
			row.Status, row.Detail, row.Fix = doctor.StatusFail, err.Error(), "lyna-tmux config edit"
		case found:
			cfg = loaded
			row.Status, row.Detail = doctor.StatusOK, paths.ConfigFile()
		default:
			row.Status, row.Detail = doctor.StatusOK, "no file at "+paths.ConfigFile()+", using the defaults (lyna-tmux config init writes one)"
		}
	}

	sys.Getenv = h.Getenv
	sys.LookPath = h.lookPath()
	sys.ClaudeHome = xdg.ClaudeHome(h.Getenv, h.Home)
	sys.ClaudeMinVersion = claude.MinVersion.String()
	sys.AltKeys = cfg.UI.AltKeys
	sys.Prefix = cfg.Workspace.Prefix
	sys.SandboxProfile = cfg.Sandbox.Profile
	sys.Isolation = cfg.Sandbox.Isolation
	sys.Home = h.Home
	if dir != "" {
		// Claude Code records trust for the directory it starts in, which for
		// a workspace is the project root, not the directory the user is in.
		if root, err := session.ProjectRoot(dir); err == nil {
			sys.ProjectDir = root
		}
	}

	results := append([]doctor.Result{row, doctorNesting(h)}, doctor.Run(ctx, sys)...)
	if pathsErr != nil {
		results = append(results, doctor.Result{ID: doctorReviewID, Title: "Review editor", Status: doctor.StatusSkip, Detail: "the lyna-tmux directories are unknown"})
		return doctor.NewReport(results)
	}
	editor, err := domain.ParseEditorMode(cfg.Review.Editor)
	if err != nil {
		editor = domain.EditorIsolated
	}
	opts := ReviewPlugin{}.statusOptions(h, paths)
	if sys.Run != nil {
		opts.Run = doctorReviewRun(sys)
	}
	return doctor.NewReport(append(results, doctorReviewResult(editor, review.Status(ctx, opts))))
}

// doctorNesting reports a terminal that already runs a tmux of its own. A
// workspace attached there is a tmux inside a tmux: the outer session reads
// the prefix key first, and the replies the terminal sends the inner server
// arrive too late to be recognized and are typed into the focused pane, the
// agent prompt included.
func doctorNesting(h Host) doctor.Result {
	res := doctor.Result{ID: doctorNestedID, Title: "Terminal"}
	name := h.Getenv(session.EnvSocketName)
	if name == "" {
		name = tmux.DefaultSocketName
	}
	socket, ok := (&Server{SocketName: name}).ForeignTmux(h)
	if !ok {
		res.Status, res.Detail = doctor.StatusOK, "not inside another tmux session"
		return res
	}
	res.Status = doctor.StatusWarn
	res.Detail = "inside the tmux server " + socket + ", so a workspace attached from here runs one tmux inside another"
	res.Fix = "detach that session and run lyna-tmux from the terminal, or attach with --nested"
	return res
}

// doctorReviewRun adapts the doctor command runner, with its per-command
// timeout, to the review status probe.
func doctorReviewRun(sys doctor.Deps) func(ctx context.Context, bin string, args ...string) ([]byte, error) {
	timeout := sys.Timeout
	if timeout <= 0 {
		timeout = doctor.DefaultTimeout
	}
	return func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := sys.Run(ctx, append([]string{bin}, args...))
		return []byte(out), err
	}
}

// doctorReviewResult reports whether the review popup can open with the
// configured editor. A plugin that is installed but does not match its pin
// fails: lyna-tmux would load files it did not verify. A missing plugin or
// Neovim only degrades the review popup, so it warns.
func doctorReviewResult(editor domain.EditorMode, r review.Report) doctor.Result {
	res := doctor.Result{ID: doctorReviewID, Title: "Review editor"}
	nvim := strings.Join(r.NvimProblems(), "; ")
	if editor == domain.EditorUser {
		if nvim != "" {
			res.Status, res.Detail = doctor.StatusWarn, "review.editor = \"user\" opens reviews in your own Neovim: "+nvim
			return res
		}
		res.Status, res.Detail = doctor.StatusOK, fmt.Sprintf("review.editor = \"user\": reviews open in your own Neovim %s", r.NvimVersion)
		return res
	}
	switch {
	case r.PlatformProblem != "":
		res.Status, res.Detail = doctor.StatusWarn, r.PlatformProblem
	case r.DirProblem != "":
		res.Status, res.Detail, res.Fix = doctor.StatusFail, r.DirProblem, "lyna-tmux review install"
	case !r.Installed:
		res.Status, res.Detail, res.Fix = doctor.StatusWarn, "codediff.nvim is not installed, so the review popup cannot open", "lyna-tmux review install"
	case !r.PluginOK():
		res.Status, res.Detail, res.Fix = doctor.StatusFail, strings.Join(r.PluginProblems(), "\n"), "lyna-tmux review install --force"
	case nvim != "":
		res.Status, res.Detail = doctor.StatusWarn, "codediff.nvim is verified, but "+nvim
	default:
		commit := r.Commit[:min(doctorShortCommit, len(r.Commit))]
		res.Status = doctor.StatusOK
		files := fmt.Sprintf("%d native files match their checksums", len(r.Assets))
		if len(r.Assets) == 1 {
			files = "its native file matches its checksum"
		}
		res.Detail = fmt.Sprintf("codediff.nvim %s (commit %s), %s; Neovim %s", r.Version, commit, files, r.NvimVersion)
		if w := r.Warnings(); len(w) > 0 {
			res.Status, res.Detail = doctor.StatusWarn, res.Detail+"\n"+strings.Join(w, "\n")
		}
	}
	return res
}
