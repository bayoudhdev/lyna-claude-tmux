package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
)

// WatchInterval is how often a changes view reads the working tree without
// an event: an edit made from a shell in a nested directory produces none.
const WatchInterval = 3 * time.Second

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

	var (
		cfg    config.Config
		signal func(context.Context) error
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
	return WatchView{
		Watcher: &watch.Watcher{
			Dir:      req.Dir,
			Source:   watch.Runner{Environ: func() []string { return h.Environ }},
			Signal:   signal,
			Interval: WatchInterval,
		},
		Options: tui.ChangesOptions{Styles: tui.NewStyles(look), Popup: req.Popup, Dir: req.Dir, Home: h.Home},
	}, nil
}
