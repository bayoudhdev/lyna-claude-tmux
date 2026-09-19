package doctor

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/hook"
)

// teamsID is the identifier of the agent teams check.
const teamsID = "teams"

// maxLog bounds the read of the diagnostic log. The log rotates once it grows
// past hook.LogMaxBytes, and the line that takes it past is written whole.
const maxLog = 2 * hook.LogMaxBytes

// checkTeams reports how the teammates of a workspace open: whether agent
// teams are on, whether the workspace opens teammates in panes of its own, and
// how the last teammate actually opened, which is the one thing the settings
// cannot say. The teammate launcher logs a line for every teammate, the ones
// it took over and the ones it could not.
func checkTeams(_ context.Context, d Deps) []Result {
	r := Result{ID: teamsID, Title: "Agent teams"}
	teams := "claude.teams is off, so only a workspace opened with lmux team runs a team"
	if d.Teams {
		teams = "claude.teams is on"
	}
	if !claudecfg.TeammateInWorkspace(d.TeammateMode) {
		r.Status = StatusSkip
		r.Detail = teams + `; claude.teammate_mode = "` + d.TeammateMode +
			`" opens teammates the agent's own way, without the workspace's labels and layout`
		return []Result{r}
	}
	if d.LogFile == "" {
		r.Status, r.Detail = StatusSkip, teams+"; the diagnostic log is unknown, so how teammates opened cannot be read"
		return []Result{r}
	}
	data, err := d.ReadFile(d.LogFile, maxLog)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.Status, r.Detail = StatusWarn, teams+"; the diagnostic log cannot be read: "+err.Error()
		return []Result{r}
	}
	last, ok := hook.LastLogEntry(data, hook.LogTeammate, hook.LogTeammateUnplaced, hook.LogTeammateFallback)
	switch {
	case !ok && d.Teams:
		r.Status, r.Detail = StatusOK, teams+"; no teammate has opened in a workspace yet"
	case !ok:
		r.Status, r.Detail = StatusSkip, teams+"; no teammate has opened in a workspace yet"
	case last.Name == hook.LogTeammate:
		r.Status, r.Detail = StatusOK, teams+"; the last teammate, "+teamsWhen(last.At)+": "+last.Message
	case last.Name == hook.LogTeammateUnplaced:
		r.Status = StatusWarn
		r.Detail = teams + "; the last teammate is a pane of the workspace, but its window could not be arranged, " +
			teamsWhen(last.At) + ": " + last.Message + "\n" + teamsLog(d)
		r.Fix = "select the teammate in the agents rail and press w: it gets a window of its own and this one is put back"
	default:
		// The reason is what to fix: a launcher that could not be written, a
		// pane outside tmux, a server that did not answer. There is no one
		// command for all of them.
		r.Status = StatusWarn
		r.Detail = teams + "; the workspace could not take teammates over, " +
			teamsWhen(last.At) + ": " + last.Message + "\n" + teamsLog(d)
		r.Fix = "fix what the reason above names; the next teammate to open is reported here"
	}
	return []Result{r}
}

// teamsLog says where every teammate that opened is written down.
func teamsLog(d Deps) string { return d.LogFile + " lists every teammate that opened" }

// teamsWhen is when a teammate opened, as the report shows it.
func teamsWhen(at time.Time) string { return "at " + at.UTC().Format("2006-01-02 15:04 UTC") }
