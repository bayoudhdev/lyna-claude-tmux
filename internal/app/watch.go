package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/git"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// WatchIdle is how long a changes view waits, with nothing else having
// refreshed it, before reading the working tree anyway: an edit made from a
// shell in a nested directory produces neither a file event nor a hook
// signal. A change that does produce one is drawn as it happens, and puts
// this read off again.
const WatchIdle = 15 * time.Second

// ErrWatchNoSession reports a changes view with no workspace to follow.
var ErrWatchNoSession = errors.New("no workspace to follow")

// WatchRequest describes a live changes view.
type WatchRequest struct {
	// Dir is the absolute directory whose working tree the view shows.
	Dir string
	// Session is the workspace whose changes channel the view waits on when
	// the process does not run in a tmux pane.
	Session string
	// Popup lets q and esc close the view.
	Popup bool
}

// WatchView is a changes view ready to run: the watcher that feeds it and the
// view options, without the updates channel the caller connects.
type WatchView struct {
	Watcher *watch.Watcher
	Options tui.ChangesOptions
}

// Size of the review popup the changes view opens, as tmux reads it.
const (
	watchReviewWidth  = "95%"
	watchReviewHeight = "95%"
)

// watchReview opens reviews for a changes view drawn in a tmux pane. The
// review runs in a popup over that pane, so it covers the changes while it is
// open and gives the pane back when it closes.
type watchReview struct {
	// run sends one command to the tmux server the pane belongs to.
	run func(ctx context.Context, cmd tmux.Command) error
	// src resolves the working tree the status paths are relative to. It is
	// read again at every open, so a directory that becomes a repository
	// while the view is open is reviewed without restarting it.
	src watch.Source
	// exe is the lyna-tmux binary the popup runs, pane the pane it is drawn
	// over and dir the watched directory.
	exe, pane, dir string
}

// open reviews one path of the working tree, or all of it when path is empty.
// The command reports a failure to the view: the popup never opened, so
// nothing else would say so.
func (r watchReview) open(ctx context.Context, path string) tea.Cmd {
	return func() tea.Msg {
		if err := r.review(ctx, path); err != nil {
			return tui.ChangesNote("review: " + err.Error())
		}
		return nil
	}
}

func (r watchReview) review(ctx context.Context, path string) error {
	repo, err := r.src.Repo(ctx, r.dir)
	if err != nil {
		return err
	}
	argv := []string{r.exe, "review", "--popup", "--dir", repo.Root}
	if path != "" {
		// A status path is relative to the top of the working tree, which is
		// where the review is opened, so it is a pathspec there as it stands.
		argv = append(argv, "--", path)
	}
	cmd, err := tmux.PopupSpec{
		Pane: r.pane, Width: watchReviewWidth, Height: watchReviewHeight,
		Dir: repo.Root, Title: popupReviewTitle, Argv: argv,
	}.Command()
	if err != nil {
		return err
	}
	return r.run(ctx, cmd)
}

// popupReviewTitle is drawn on the border of the review popup.
const popupReviewTitle = "review"

// OpenWatch resolves a changes view. In a tmux pane or popup ($TMUX and
// $TMUX_PANE) the view talks to that server and follows the session of the
// pane, whose name is read again before every wait so a renamed workspace
// keeps refreshing. Elsewhere it follows the named session on the lyna-tmux
// server, which must exist.
func OpenWatch(ctx context.Context, h Host, req WatchRequest) (WatchView, error) {
	if !filepath.IsAbs(req.Dir) {
		return WatchView{}, fmt.Errorf("changes directory %q is not absolute", req.Dir)
	}
	if info, err := os.Stat(req.Dir); err != nil {
		return WatchView{}, fmt.Errorf("changes directory: %w", err)
	} else if !info.IsDir() {
		return WatchView{}, fmt.Errorf("changes directory %s is not a directory", sanitize.Line(req.Dir))
	}
	if req.Session != "" {
		if err := session.Validate(req.Session); err != nil {
			return WatchView{}, err
		}
	}

	source := git.Runner{Environ: func() []string { return h.Environ }}
	var (
		cfg    config.Config
		signal func(context.Context) error
		review *watchReview
	)
	if socket, ok := tmux.SocketFromEnv(h.Getenv("TMUX")); ok {
		_, loaded, err := LoadConfig(h)
		if err != nil {
			return WatchView{}, err
		}
		cfg = loaded
		client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: ServerEnviron(h.Environ)})
		switch pane := h.Getenv("TMUX_PANE"); {
		case pane != "":
			// display-message succeeds with empty output for a pane that
			// does not exist (tmux 3.7), so the name itself is checked.
			name, err := client.Display(ctx, pane, "#{session_name}")
			if err == nil && name == "" {
				err = watch.ErrNoSession
			}
			if err != nil {
				return WatchView{}, fmt.Errorf("cannot read the workspace of pane %s: %w", sanitize.Line(pane), err)
			}
			signal = watch.TmuxPaneSignal(client, pane)
			// tmux shows one popup per client and closes the one already
			// open: a view that is itself a popup would be taken off the
			// screen by its own action, so only a pane opens reviews.
			if !req.Popup && h.Exe != "" {
				review = &watchReview{
					run: func(ctx context.Context, cmd tmux.Command) error {
						_, err := client.Batch(ctx, cmd)
						return err
					},
					src: source, exe: h.Exe, pane: pane, dir: req.Dir,
				}
			}
		case req.Session != "":
			signal = watch.TmuxSignal(client, req.Session)
		default:
			return WatchView{}, fmt.Errorf("%w: pass --session", ErrWatchNoSession)
		}
	} else {
		if req.Session == "" {
			return WatchView{}, fmt.Errorf("%w: outside tmux, pass --session with a workspace name", ErrWatchNoSession)
		}
		s, err := OpenServer(ctx, h)
		if err != nil {
			return WatchView{}, err
		}
		ok, err := s.Client.HasSession(ctx, req.Session)
		if err != nil {
			return WatchView{}, err
		}
		if !ok {
			return WatchView{}, fmt.Errorf("%w: %s", ErrNoWorkspace, req.Session)
		}
		cfg = s.Config
		signal = watch.TmuxSignal(s.Client, req.Session)
	}

	look, err := tui.ThemeFromConfig(cfg.UI, h.Getenv)
	if err != nil {
		return WatchView{}, err
	}
	opts := tui.ChangesOptions{Styles: tui.NewStyles(look), Popup: req.Popup, Dir: req.Dir, Home: h.Home}
	if review != nil {
		opts.Open = func(path string) tea.Cmd { return review.open(ctx, path) }
		opts.OpenReview = func() tea.Cmd { return review.open(ctx, "") }
	}
	return WatchView{
		Watcher: &watch.Watcher{Dir: req.Dir, Source: source, Signal: signal, Idle: WatchIdle},
		Options: opts,
	}, nil
}
