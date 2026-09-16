package claudecfg

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// ConfigFileName is the Claude Code state file that records, among other
// things, which project directories the user has accepted the trust prompt
// for. It sits next to the configuration directory, not inside it.
const ConfigFileName = ".claude.json"

// ConfigFile is the path of that state file for a Claude home directory:
// ~/.claude.json for the default ~/.claude, and <dir>/.claude.json when
// CLAUDE_CONFIG_DIR points elsewhere.
func ConfigFile(claudeHome, home string) string {
	if claudeHome == filepath.Join(home, ".claude") {
		return filepath.Join(home, ConfigFileName)
	}
	return filepath.Join(claudeHome, ConfigFileName)
}

// Trust is what the state file says about one project directory.
type Trust int

// Trust states. Until a project is trusted, Claude Code runs no hooks in it,
// which leaves the workspace status line without agent state.
const (
	// TrustUnknown means the file has no entry for the directory, so Claude
	// Code has never opened it and will ask the first time it does.
	TrustUnknown Trust = iota
	// TrustRefused means the entry exists and the prompt was not accepted.
	TrustRefused
	// TrustAccepted means hooks run in that directory.
	TrustAccepted
)

// claudeState is the part of the state file this package reads. Every other
// key, including the conversation history, is ignored.
type claudeState struct {
	Projects map[string]struct {
		HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
	} `json:"projects"`
}

// ProjectTrust reports whether Claude Code has been trusted for dir. The
// lookup is by exact directory, the way Claude Code records it: trust is not
// inherited from a parent directory.
func ProjectTrust(data []byte, dir string) (Trust, error) {
	var state claudeState
	if err := json.Unmarshal(data, &state); err != nil {
		return TrustUnknown, fmt.Errorf("decode %s: %w", ConfigFileName, err)
	}
	project, ok := state.Projects[filepath.Clean(dir)]
	switch {
	case !ok:
		return TrustUnknown, nil
	case project.HasTrustDialogAccepted:
		return TrustAccepted, nil
	default:
		return TrustRefused, nil
	}
}
