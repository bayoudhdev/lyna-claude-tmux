package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/transcript"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/watch"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// The reader redraws on what the transcript gains, which the file watch
// reports, on the agents of the workspace, which the hooks signal, and on
// nothing else. The fallback catches what neither reports: a file the watch
// could not be attached to.
const (
	TranscriptDebounce = 120 * time.Millisecond
	TranscriptIdle     = 10 * time.Second
	// transcriptTimeout bounds one reading of the server and of the file.
	transcriptTimeout = 3 * time.Second
)

// Errors the reader refuses to open on.
var (
	// ErrTranscriptPane reports a pane that is not an agent of the workspace
	// the reader was opened for.
	ErrTranscriptPane = errors.New("the transcript reader follows an agent of this workspace")
	// ErrNoTranscript reports an agent no hook has named a transcript for,
	// which is a pane where no session has started.
	ErrNoTranscript = errors.New("no transcript: no Claude Code session has started in that pane")
)

// TranscriptRequest is one run of the transcript reader.
type TranscriptRequest struct {
	// Session is the workspace the reader works in outside tmux; inside a pane
	// the pane says which workspace it belongs to.
	Session string
	// Pane is the pane of the agent to read.
	Pane string
	// Agent is a subagent of that pane, read instead of the agent itself. It
	// is the identifier the agents view carries for a subagent row.
	Agent string
}

// TranscriptView is everything the reader runs on: how to read the transcript,
// how to wait for it to change, and the model's own options.
type TranscriptView struct {
	Options tui.TranscriptOptions
	// Read takes what the transcript gained since the last reading.
	Read func(ctx context.Context) tui.TranscriptUpdate
	// Signal blocks until an agent of the workspace changes, which is how a
	// session starting or ending in the pane reaches the reader.
	Signal func(ctx context.Context) error
	// Watch pokes on every write to the transcript, and returns when ctx is
	// done.
	Watch func(ctx context.Context, poke func())
}

// OpenTranscript prepares the reader of one agent's transcript.
//
// The pane names the agent: inside a workspace pane the reader follows the
// workspace that pane belongs to, and outside tmux the workspace of the
// request on the lyna-tmux server. A pane of another workspace, and a pane no
// session has started in, are refused rather than read. The transcript itself
// is only ever read, and only under the Claude configuration directory.
func OpenTranscript(ctx context.Context, h Host, req TranscriptRequest) (TranscriptView, error) {
	if !tmux.ValidPaneID(req.Pane) {
		return TranscriptView{}, fmt.Errorf("%w: pass --to with the pane of an agent", ErrTranscriptPane)
	}
	client, cfg, workspace, err := transcriptWorkspace(ctx, h, req.Session)
	if err != nil {
		return TranscriptView{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, transcriptTimeout)
	defer cancel()
	panes, err := client.ListPanes(readCtx, "")
	if err != nil {
		return TranscriptView{}, err
	}
	pane, ok := paneByID(panes, req.Pane)
	if !ok || pane.SessionName != workspace {
		return TranscriptView{}, fmt.Errorf("%w: %s is not a pane of %s", ErrTranscriptPane, sanitize.Line(req.Pane), sanitize.Line(workspace))
	}
	path, title := transcriptOf(pane, req.Agent, workspace)
	if path == "" {
		return TranscriptView{}, fmt.Errorf("%w: %s", ErrNoTranscript, sanitize.Line(title))
	}
	claudeHome := xdg.ClaudeHome(h.Getenv, h.Home)
	// A transcript that is not there yet is one a subagent has not written,
	// and is waited for; one that is not a transcript at all is refused here,
	// where the reader can say so.
	if _, err := claude.TranscriptFile(claudeHome, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return TranscriptView{}, err
	}
	look, err := tui.ThemeFromConfig(cfg.UI, h.Getenv)
	if err != nil {
		return TranscriptView{}, err
	}
	r := &transcriptReader{claudeHome: claudeHome, path: path}
	return TranscriptView{
		Options: tui.TranscriptOptions{Styles: tui.NewStyles(look), Title: title},
		Read:    r.read,
		Signal:  watch.TmuxAgentsSignal(client, req.Pane),
		Watch:   r.watch,
	}, nil
}

// transcriptWorkspace resolves the server the pane is on and the workspace the
// reader works in: the one holding the pane the reader runs in, or the one the
// request names on the lyna-tmux server.
func transcriptWorkspace(ctx context.Context, h Host, name string) (*tmux.Client, config.Config, string, error) {
	if name != "" {
		if err := session.Validate(name); err != nil {
			return nil, config.Config{}, "", err
		}
	}
	if socket, inside := tmux.SocketFromEnv(h.Getenv("TMUX")); inside {
		_, cfg, err := LoadConfig(h)
		if err != nil {
			return nil, config.Config{}, "", err
		}
		client := tmux.New(tmux.Options{Bin: h.TmuxBin, Socket: socket, Env: ServerEnviron(h.Environ)})
		pane := h.Getenv("TMUX_PANE")
		if !tmux.ValidPaneID(pane) {
			if name == "" {
				return nil, config.Config{}, "", fmt.Errorf("%w: pass --session", ErrTranscriptPane)
			}
			return client, cfg, name, nil
		}
		readCtx, cancel := context.WithTimeout(ctx, transcriptTimeout)
		defer cancel()
		workspace, err := client.Display(readCtx, pane, "#{session_name}")
		if err != nil || workspace == "" {
			return nil, config.Config{}, "", fmt.Errorf("cannot read the workspace of pane %s: %w", sanitize.Line(pane), err)
		}
		return client, cfg, workspace, nil
	}
	if name == "" {
		return nil, config.Config{}, "", fmt.Errorf("%w: outside tmux, pass --session with a workspace name", ErrTranscriptPane)
	}
	s, err := OpenServer(ctx, h)
	if err != nil {
		return nil, config.Config{}, "", err
	}
	ok, err := s.Client.HasSession(ctx, name)
	if err != nil {
		return nil, config.Config{}, "", err
	}
	if !ok {
		return nil, config.Config{}, "", fmt.Errorf("%w: %s", ErrNoWorkspace, name)
	}
	return s.Client, s.Config, name, nil
}

func paneByID(panes []tmux.Pane, id string) (tmux.Pane, bool) {
	for _, p := range panes {
		if p.ID == id {
			return p, true
		}
	}
	return tmux.Pane{}, false
}

// transcriptOf is the file the reader follows and the agent it belongs to: the
// pane's own transcript, or the one of a subagent it started, which is derived
// from it.
func transcriptOf(pane tmux.Pane, agent, workspace string) (path, title string) {
	title = pane.Agent
	if title == "" {
		title = workspace
	}
	if agent == "" {
		return pane.Transcript, title
	}
	return team.SubagentTranscript(pane.Transcript, agent), agent
}

// transcriptReader follows one transcript: it reads what the file gained since
// the last reading and turns it into the entries the view draws.
//
// read is called by the refresh loop alone, and watch runs beside it, so only
// what they share is guarded.
type transcriptReader struct {
	claudeHome, path string
	cursor           claude.TranscriptCursor
	lines            transcript.Lines

	mu    sync.Mutex
	fsw   *fsnotify.Watcher
	armed bool
}

// read returns the entries the transcript gained, reading it in bounded reads
// until it has caught up with the file.
func (r *transcriptReader) read(ctx context.Context) tui.TranscriptUpdate {
	ctx, cancel := context.WithTimeout(ctx, transcriptTimeout)
	defer cancel()
	// A transcript that was not there when the reader opened, and one whose
	// file was replaced, are followed from the first reading that finds them.
	r.arm()
	update := tui.TranscriptUpdate{At: time.Now()}
	for {
		chunk, err := claude.ReadTranscript(r.claudeHome, r.path, r.cursor)
		if err != nil {
			// A transcript a subagent has not written yet is nothing to show,
			// not a failure to report.
			if !errors.Is(err, fs.ErrNotExist) {
				update.Err = err
			}
			return update
		}
		if chunk.Restart {
			r.lines, update.Restart, update.Entries = transcript.Lines{}, true, nil
		}
		r.lines.Feed(chunk.Data, func(line []byte) {
			update.Entries = append(update.Entries, transcript.Entries(line)...)
		})
		r.cursor = chunk.Next
		if !chunk.More || ctx.Err() != nil {
			return update
		}
	}
}

// watch pokes on every write to the transcript. Without file events the agents
// signal and the idle fallback still refresh, so a watch that cannot be opened
// is not a failure.
func (r *transcriptReader) watch(ctx context.Context, poke func()) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer func() { _ = fsw.Close() }()
	r.mu.Lock()
	r.fsw = fsw
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.fsw, r.armed = nil, false
		r.mu.Unlock()
	}()
	r.arm()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				// The file this watch followed is gone: the next reading
				// follows the one that took its name.
				r.mu.Lock()
				r.armed = false
				r.mu.Unlock()
			}
			poke()
		case _, ok := <-fsw.Errors:
			if !ok {
				return
			}
			// An overflow loses events; reading again recovers what was lost.
			poke()
		}
	}
}

// arm watches the transcript file itself, which is where its writes are seen.
// Only the file the reader is allowed to read is watched, and only once.
func (r *transcriptReader) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fsw == nil || r.armed {
		return
	}
	file, err := claude.TranscriptFile(r.claudeHome, r.path)
	if err != nil {
		return
	}
	if err := r.fsw.Add(file); err == nil {
		r.armed = true
	}
}
