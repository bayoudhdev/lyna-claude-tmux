package claude

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// The state Claude Code keeps about a team lives under its own configuration
// directory. Everything here reads it and nothing writes it: the files belong
// to the agent, which rewrites them as the team works.
const (
	teamsDir = "teams"
	tasksDir = "tasks"
	// teamConfigFile is the team itself: its members and where each one runs.
	teamConfigFile = "config.json"
	// maxTaskFiles bounds one reading of the shared task list. A team with
	// more open tasks than this shows the first of them, which is what the
	// order they are read in is for.
	maxTaskFiles = 256
)

// ErrTeamName reports a team name that names no directory: a name is used as
// a path here, so one that is empty or that could leave the teams directory
// is refused rather than cleaned.
var ErrTeamName = errors.New("claude: the team name is not one Claude Code writes")

// TeamDir is the directory Claude Code keeps a team's own file in, and
// TasksDir the one it keeps the team's shared task list in.
func TeamDir(claudeHome, name string) (string, error) {
	slug, err := teamSlug(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(claudeHome, teamsDir, slug), nil
}

// TasksDir is the directory of the shared task list of a team.
func TasksDir(claudeHome, name string) (string, error) {
	slug, err := teamSlug(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(claudeHome, tasksDir, slug), nil
}

// teamSlug is the directory name of a team, which is the name with everything
// a path could be built from already replaced by Claude Code itself. The
// result is checked rather than trusted: it is joined onto a path here.
func teamSlug(name string) (string, error) {
	slug := team.Slug(name)
	if slug == "" || slug == "." || slug == ".." {
		return "", ErrTeamName
	}
	return slug, nil
}

// ReadTeam reads the team file Claude Code writes. A team that has no file
// yet, which is a team whose first teammate is still starting, reads as no
// team at all rather than as a failure.
func ReadTeam(claudeHome, name string) (team.Config, error) {
	dir, err := TeamDir(claudeHome, name)
	if err != nil {
		return team.Config{}, err
	}
	data, err := fsx.ReadFileLimited(filepath.Join(dir, teamConfigFile), team.MaxConfigSize)
	if errors.Is(err, fs.ErrNotExist) {
		return team.Config{}, nil
	}
	if err != nil {
		return team.Config{}, err
	}
	return team.ParseConfig(data)
}

// ReadTasks reads the shared task list of a team, in the order the list is
// worked through. A file that is not a task is skipped: the directory is
// written by another program while this reads it, so a file caught half
// written is one task missing from one reading and not a failed reading.
func ReadTasks(claudeHome, name string) ([]team.Task, error) {
	dir, err := TasksDir(claudeHome, name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// ReadDir sorts by file name, so the cap takes the same tasks every time
	// whatever order the file system holds the directory in.
	tasks := make([]team.Task, 0, min(len(entries), maxTaskFiles))
	for _, e := range entries {
		if len(tasks) == maxTaskFiles {
			break
		}
		if e.IsDir() {
			continue
		}
		if _, ok := team.TaskID(e.Name()); !ok {
			continue
		}
		data, err := fsx.ReadFileLimited(filepath.Join(dir, e.Name()), team.MaxTaskSize)
		if err != nil {
			continue
		}
		t, err := team.ParseTask(data)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	team.SortTasks(tasks)
	return tasks, nil
}
