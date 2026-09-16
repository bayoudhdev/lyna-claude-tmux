package doctor

import (
	"context"
	"errors"
	"io/fs"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
)

// trustID is the identifier of the project trust check.
const trustID = "claude-trust"

// maxClaudeState bounds the read of the Claude Code state file, which keeps a
// per-project history and grows with use.
const maxClaudeState = 32 << 20

// trustFix is what a user does about an untrusted project. Claude Code asks
// once, in the project directory, the first time it starts there.
const trustFix = "start claude once in the project and accept the trust prompt, then reopen the workspace"

// checkProjectTrust reports whether Claude Code has been trusted for the
// project. Claude Code runs no hooks at all in a project the user has not
// trusted, so the workspace status line stays empty of agent state even
// though everything else works.
func checkProjectTrust(_ context.Context, d Deps) []Result {
	r := Result{ID: trustID, Title: "Project trust"}
	if d.ProjectDir == "" || d.Home == "" {
		r.Status, r.Detail = StatusSkip, "no project directory"
		return []Result{r}
	}
	path := claudecfg.ConfigFile(d.ClaudeHome, d.Home)
	data, err := d.ReadFile(path, maxClaudeState)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Claude Code writes the file on its first run, so there is nothing to
		// report yet: it will ask for this project when it starts there.
		r.Status, r.Detail = StatusSkip, "no "+path+" yet"
		return []Result{r}
	case err != nil:
		r.Status, r.Detail, r.Fix = StatusWarn, err.Error(), trustFix
		return []Result{r}
	}
	trust, err := claudecfg.ProjectTrust(data, d.ProjectDir)
	if err != nil {
		r.Status, r.Detail, r.Fix = StatusWarn, err.Error(), trustFix
		return []Result{r}
	}
	switch trust {
	case claudecfg.TrustAccepted:
		r.Status, r.Detail = StatusOK, d.ProjectDir
	case claudecfg.TrustRefused:
		r.Status, r.Detail, r.Fix = StatusWarn,
			"Claude Code is not trusted in "+d.ProjectDir+", so it runs no hooks there and the status line shows no agent state", trustFix
	default:
		r.Status, r.Detail, r.Fix = StatusWarn,
			"Claude Code has never opened "+d.ProjectDir+"; until the trust prompt is accepted it runs no hooks there and the status line shows no agent state", trustFix
	}
	return []Result{r}
}
