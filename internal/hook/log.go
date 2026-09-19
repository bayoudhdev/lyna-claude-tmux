package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// LogMaxBytes caps the diagnostic log before it rotates.
const LogMaxBytes = 256 << 10

// The names the teammate launcher logs under. It writes one line for every
// teammate, the ones it took over as well as the ones it could not, so the
// last of these lines always says how the most recent teammate opened.
const (
	// LogTeammate is a teammate labeled as a pane of the workspace and placed
	// by the pane policy.
	LogTeammate = "teammate"
	// LogTeammateUnplaced is a teammate labeled as a pane of the workspace and
	// left where Claude Code opened it, because its window could not be
	// arranged.
	LogTeammateUnplaced = "teammate-unplaced"
	// LogTeammateFallback is a teammate the workspace could not take over,
	// which runs the way Claude Code opens it.
	LogTeammateFallback = "teammate-fallback"
)

// Log appends one line to the diagnostic log at path, which is where every
// part of lyna-tmux that runs inside a pane of the agent reports what it could
// not do: a hook, and the teammate launcher. An empty path logs nothing, and a
// line that cannot be written is dropped rather than reported, since there is
// nowhere left to report it without disturbing Claude Code.
func Log(path string, now time.Time, name, format string, args ...any) {
	if path == "" {
		return
	}
	line := logLine(now, name, fmt.Sprintf(format, args...))
	err := fsx.AppendCapped(path, []byte(line), LogMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		if fsx.EnsurePrivateDir(filepath.Dir(path)) == nil {
			_ = fsx.AppendCapped(path, []byte(line), LogMaxBytes)
		}
	}
}

// logLine is one line of the log: when, who, and what, on a single line
// whatever the message held.
func logLine(now time.Time, name, message string) string {
	return now.UTC().Format(time.RFC3339) + " " + name + ": " + sanitize.Line(message)
}

// LogEntry is one line of the diagnostic log.
type LogEntry struct {
	At      time.Time
	Name    string
	Message string
}

// LastLogEntry returns the last line of a log that was written under one of
// names.
//
// A line that is not in the shape Log writes is skipped rather than reported:
// several processes append to the log and it is cut when it rotates, so a torn
// line is something a reader meets, not a fault.
func LastLogEntry(data []byte, names ...string) (LogEntry, bool) {
	var (
		last  LogEntry
		found bool
	)
	for line := range strings.Lines(string(data)) {
		e, ok := parseLogLine(strings.TrimSuffix(line, "\n"))
		if ok && slices.Contains(names, e.Name) {
			last, found = e, true
		}
	}
	return last, found
}

// parseLogLine reads one line in the shape logLine writes. A name never holds
// ": ", which is what ends it; the message may. A line cut before that
// separator is refused, since what is left of it may read as a name. The name
// is not checked here: the caller keeps only the names it asked for.
func parseLogLine(line string) (LogEntry, bool) {
	stamp, rest, _ := strings.Cut(line, " ")
	at, err := time.Parse(time.RFC3339, stamp)
	name, message, ok := strings.Cut(rest, ": ")
	if err != nil || !ok {
		return LogEntry{}, false
	}
	return LogEntry{At: at, Name: name, Message: message}, true
}
