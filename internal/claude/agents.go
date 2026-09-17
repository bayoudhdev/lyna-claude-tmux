package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agentdef"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// The directories Claude Code reads agent definitions from: the one a project
// carries and the one under its own configuration directory.
const (
	agentsDir        = "agents"
	projectClaudeDir = ".claude"
)

// MaxAgentDefs bounds one listing. A user with more definitions than this has
// a generated directory, and a form nobody can scroll is worth less than the
// first hundred names in it.
const MaxAgentDefs = 128

// AgentDefs reads the agent definitions a workspace can spawn: the ones the
// project carries in .claude/agents, then the user's own, with the project
// winning where both name the same agent, exactly as Claude Code resolves
// them.
//
// Nothing here fails a listing: a directory that is not there is a project or
// a user with no definitions of their own, and a file that is not a definition
// is skipped rather than reported. The result is sorted by name, so the form
// offers the same order every time.
func AgentDefs(projectRoot, claudeHome string) []agentdef.Def {
	defs := map[string]agentdef.Def{}
	// The user's own definitions are read first, so a project that carries a
	// definition of the same name replaces it rather than adding a second one.
	for _, dir := range []string{
		userAgentsDir(claudeHome),
		projectAgentsDir(projectRoot),
	} {
		for _, d := range readAgentDir(dir) {
			defs[d.Name] = d
		}
	}
	out := make([]agentdef.Def, 0, len(defs))
	for _, d := range defs {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b agentdef.Def) int { return strings.Compare(a.Name, b.Name) })
	if len(out) > MaxAgentDefs {
		out = out[:MaxAgentDefs]
	}
	return out
}

// projectAgentsDir is where a project keeps the definitions it carries, and
// userAgentsDir where the user keeps their own. An empty root names no
// directory rather than the root of the file system.
func projectAgentsDir(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, projectClaudeDir, agentsDir)
}

func userAgentsDir(claudeHome string) string {
	if claudeHome == "" {
		return ""
	}
	return filepath.Join(claudeHome, agentsDir)
}

// readAgentDir reads one directory of definitions.
func readAgentDir(dir string) []agentdef.Def {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return nil
	}
	// ReadDir sorts by file name, so the cap takes the same definitions every
	// time whatever order the file system holds the directory in.
	defs := make([]agentdef.Def, 0, min(len(entries), MaxAgentDefs))
	for _, e := range entries {
		if len(defs) == MaxAgentDefs {
			break
		}
		if e.IsDir() {
			continue
		}
		if _, ok := agentdef.NameFromFile(e.Name()); !ok {
			continue
		}
		data, err := fsx.ReadFileLimited(filepath.Join(dir, e.Name()), agentdef.MaxSize)
		if err != nil {
			continue
		}
		d, err := agentdef.Parse(data)
		if err != nil {
			continue
		}
		defs = append(defs, d)
	}
	return defs
}
