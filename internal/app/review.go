package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Errors of opening a review. Commands map the first two to the "missing
// prerequisite" exit code and ErrReviewLaunch to the launch failure code.
var (
	// ErrReviewNeovim reports a missing, unreadable or too old Neovim.
	ErrReviewNeovim = errors.New("the review editor cannot start")
	// ErrReviewPlugin reports a plugin that is not installed or not verified.
	ErrReviewPlugin = errors.New("the review editor is not ready")
	// ErrReviewLaunch reports a review that could not be prepared or started.
	ErrReviewLaunch = errors.New("the review could not open")
	// ErrReviewNotRepository reports a directory outside any git working tree.
	ErrReviewNotRepository = errors.New("not a git repository")
)

// reviewPaneTimeout bounds the tmux lookup of ReviewOwnsPane, which runs on
// an error path and must not keep a failing command waiting.
const reviewPaneTimeout = 2 * time.Second

// ReviewPlugin selects the plugin build the review commands install and
// verify. The zero value is the pinned codediff.nvim release for the running
// platform, downloaded from its official locations.
type ReviewPlugin struct {
	// Pin is the verified build; the zero value means the default pin.
	Pin domain.Pin
	// GOOS and GOARCH select the native files; empty means this platform.
	GOOS, GOARCH string
	// RepoURL and ReleaseURL replace the pin's download locations.
	RepoURL, ReleaseURL string
	// HTTPClient downloads the native files; nil uses the default client.
	HTTPClient *http.Client
}

func (p ReviewPlugin) pin() domain.Pin {
	if p.Pin.Commit == "" {
		return domain.DefaultPin()
	}
	return p.Pin
}

func (p ReviewPlugin) statusOptions(h Host, paths xdg.Paths) review.StatusOptions {
	return review.StatusOptions{Paths: paths, Pin: p.pin(), GOOS: p.GOOS, GOARCH: p.GOARCH, LookPath: h.lookPath()}
}

// ReviewInstall installs the plugin into lyna-tmux's review directory, or
// keeps a verified installation unless force is set.
func ReviewInstall(ctx context.Context, h Host, src ReviewPlugin, force bool) (review.InstallResult, error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return review.InstallResult{}, err
	}
	git, err := h.lookPath()("git")
	if err != nil {
		return review.InstallResult{}, fmt.Errorf("%w: %w", review.ErrGitMissing, err)
	}
	return review.Install(ctx, review.InstallOptions{
		ReviewDir:  paths.ReviewDir(),
		Force:      force,
		Pin:        src.pin(),
		GOOS:       src.GOOS,
		GOARCH:     src.GOARCH,
		RepoURL:    src.RepoURL,
		ReleaseURL: src.ReleaseURL,
		Git:        git,
		HTTPClient: src.HTTPClient,
		Environ:    h.Environ,
	})
}

// ReviewState is the state of the review editor for the configured editor.
type ReviewState struct {
	// Editor is the configured editor mode.
	Editor domain.EditorMode
	// Pin is the build the installation is verified against.
	Pin    domain.Pin
	Report review.Report
}

// Ready reports whether a review can start with the configured editor. The
// user's own Neovim needs no plugin installed by lyna-tmux.
func (s ReviewState) Ready() bool {
	if s.Editor == domain.EditorUser {
		return len(s.Report.NvimProblems()) == 0
	}
	return s.Report.Ready()
}

// Problems lists what stands in the way of a review with the configured editor.
func (s ReviewState) Problems() []string {
	if s.Editor == domain.EditorUser {
		return s.Report.NvimProblems()
	}
	return s.Report.Problems()
}

// ReviewStatus inspects the installation and Neovim. It changes nothing.
func ReviewStatus(ctx context.Context, h Host, src ReviewPlugin) (ReviewState, error) {
	paths, cfg, err := LoadConfig(h)
	if err != nil {
		return ReviewState{}, err
	}
	editor, err := domain.ParseEditorMode(cfg.Review.Editor)
	if err != nil {
		return ReviewState{}, err
	}
	return ReviewState{Editor: editor, Pin: src.pin(), Report: review.Status(ctx, src.statusOptions(h, paths))}, nil
}

// ReviewUninstall removes the review directory and reports whether there was
// anything to remove.
func ReviewUninstall(h Host) (removed bool, err error) {
	paths, err := xdg.Resolve(h.Getenv, h.Home)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(paths.ReviewDir()); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := review.Uninstall(paths); err != nil {
		return false, err
	}
	return true, nil
}

// ReviewRequest is one review to open.
type ReviewRequest struct {
	// Request is what to compare. An empty Layout means the configured one.
	Request domain.Request
	// Editor replaces the configured editor mode when set.
	Editor domain.EditorMode
	// Dir is the absolute directory the review opens in.
	Dir string
}

// ReviewLaunch checks everything a review needs and returns the editor
// command, ready for process replacement: a valid request inside a git
// repository, a supported Neovim and, for the isolated editor, the verified
// plugin with an init file rendered from the configured theme.
func ReviewLaunch(ctx context.Context, h Host, src ReviewPlugin, req ReviewRequest) (review.Launch, error) {
	paths, cfg, err := LoadConfig(h)
	if err != nil {
		return review.Launch{}, err
	}
	editor := req.Editor
	if editor == "" {
		if editor, err = domain.ParseEditorMode(cfg.Review.Editor); err != nil {
			return review.Launch{}, err
		}
	}
	configured, err := domain.ParseLayout(cfg.Review.Layout)
	if err != nil {
		return review.Launch{}, err
	}
	if req.Request.Layout == domain.LayoutDefault {
		req.Request.Layout = configured
	}
	if err := req.Request.Validate(); err != nil {
		return review.Launch{}, err
	}
	if err := reviewCheckRepository(ctx, h, req.Dir); err != nil {
		return review.Launch{}, err
	}

	report := review.Status(ctx, src.statusOptions(h, paths))
	if problems := report.NvimProblems(); len(problems) > 0 {
		return review.Launch{}, fmt.Errorf("%w: %s", ErrReviewNeovim, strings.Join(problems, "; "))
	}
	if editor == domain.EditorIsolated {
		if problems := report.PluginProblems(); len(problems) > 0 {
			return review.Launch{}, fmt.Errorf("%w: %s", ErrReviewPlugin, strings.Join(problems, "; "))
		}
		look, err := tui.ThemeFromConfig(cfg.UI, h.Getenv)
		if err != nil {
			return review.Launch{}, err
		}
		icons, err := domain.ParseIcons(look.Icons.Name)
		if err != nil {
			return review.Launch{}, err
		}
		if _, err := review.Prepare(paths, domain.InitOptions{
			Colors:    domain.ColorsFromPalette(look.Palette),
			Scheme:    domain.SchemeFromPalette(look.Palette),
			Icons:     icons,
			TrueColor: look.Depth == theme.DepthTrue,
			Light:     !look.Palette.Dark,
			Layout:    configured,
		}); err != nil {
			return review.Launch{}, fmt.Errorf("%w: %w", ErrReviewLaunch, err)
		}
	}
	l, err := review.Command(req.Request, review.LaunchOptions{
		Paths: paths, Mode: editor, Dir: req.Dir, LookPath: h.lookPath(), Environ: h.Environ,
	})
	switch {
	case errors.Is(err, review.ErrNvimMissing):
		return review.Launch{}, fmt.Errorf("%w: %w", ErrReviewNeovim, err)
	case errors.Is(err, review.ErrNotInstalled):
		return review.Launch{}, fmt.Errorf("%w: %w", ErrReviewPlugin, err)
	case err != nil:
		return review.Launch{}, fmt.Errorf("%w: %w", ErrReviewLaunch, err)
	}
	return l, nil
}

// reviewCheckRepository fails unless dir is an existing directory inside a
// git working tree: every review mode compares git revisions.
func reviewCheckRepository(ctx context.Context, h Host, dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("review directory %q is not absolute", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("review directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("review directory %s is not a directory", sanitize.Line(dir))
	}
	runner := git.Runner{Environ: func() []string { return h.Environ }}
	_, err = runner.Repo(ctx, dir)
	switch {
	case errors.Is(err, git.ErrNotRepository):
		return fmt.Errorf("%w: %s (a review shows git changes; run it inside a repository)", ErrReviewNotRepository, sanitize.Line(dir))
	case errors.Is(err, git.ErrNotInstalled):
		return fmt.Errorf("git is required for a review: %w", err)
	}
	return err
}

// ReviewOwnsPane reports whether the process with this pid is the program of
// the tmux pane it runs in, as in a layout's review pane: when it exits, the
// pane closes and takes any message with it. A command typed at a shell
// prompt is not the pane's program.
func ReviewOwnsPane(ctx context.Context, h Host, pid int) bool {
	pane := h.Getenv("TMUX_PANE")
	socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX"))
	if pane == "" || !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, reviewPaneTimeout)
	defer cancel()
	client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: ServerEnviron(h.Environ)})
	out, err := client.Display(ctx, pane, "#{pane_pid}")
	return err == nil && out == strconv.Itoa(pid)
}
