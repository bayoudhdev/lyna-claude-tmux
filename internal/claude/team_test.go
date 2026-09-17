package claude_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

// teamHome lays out a Claude configuration directory the way Claude Code
// lays one out for a team: the team's own file, and one file per task.
func teamHome(t *testing.T, name string, config string, tasks map[string]string) string {
	t.Helper()
	home := t.TempDir()
	slug := team.Slug(name)
	if config != "" {
		dir := filepath.Join(home, "teams", slug)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if tasks != nil {
		dir := filepath.Join(home, "tasks", slug)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for file, body := range tasks {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home
}

const teamFile = `{
  "name": "session-8f3c1d2a",
  "createdAt": 1789024519411,
  "leadAgentId": "team-lead@session-8f3c1d2a",
  "leadSessionId": "8f3c1d2a-6dd2-4c3a-924b-74473d771695",
  "members": [
    {"agentId": "team-lead@session-8f3c1d2a", "name": "team-lead", "agentType": "team-lead",
     "tmuxPaneId": "leader", "backendType": "in-process"},
    {"agentId": "review-api@session-8f3c1d2a", "name": "review-api", "agentType": "api-developer",
     "tmuxPaneId": "%7", "backendType": "tmux", "active": true}
  ]
}`

func task(id, subject, owner, status string) string {
	return `{"id":"` + id + `","subject":"` + subject + `","owner":"` + owner + `","status":"` + status + `"}`
}

func TestReadTeam(t *testing.T) {
	home := teamHome(t, "session-8f3c1d2a", teamFile, nil)
	cfg, err := claude.ReadTeam(home, "session-8f3c1d2a")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "session-8f3c1d2a" || len(cfg.Members) != 2 {
		t.Fatalf("team %+v", cfg)
	}
	if m, ok := cfg.ByPane("%7"); !ok || m.Name != "review-api" {
		t.Fatalf("the teammate of pane %%7 is %+v", m)
	}
}

// TestReadTeamWithoutFiles covers a team whose file is not there yet, which is
// a team whose first teammate is still starting: nothing to show, and nothing
// to report.
func TestReadTeamWithoutFiles(t *testing.T) {
	home := teamHome(t, "session-8f3c1d2a", "", nil)
	cfg, err := claude.ReadTeam(home, "session-8f3c1d2a")
	if err != nil || len(cfg.Members) != 0 {
		t.Fatalf("ReadTeam = %+v, %v", cfg, err)
	}
	tasks, err := claude.ReadTasks(home, "session-8f3c1d2a")
	if err != nil || len(tasks) != 0 {
		t.Fatalf("ReadTasks = %+v, %v", tasks, err)
	}
}

// TestReadTeamNames keeps every name inside the directory Claude Code keeps
// its teams in: a name is used as a path here, so one that names nothing is
// refused, and one that carries a separator is a directory of its own rather
// than a way out.
func TestReadTeamNames(t *testing.T) {
	home := t.TempDir()
	teams := filepath.Join(home, "teams") + string(filepath.Separator)
	tasks := filepath.Join(home, "tasks") + string(filepath.Separator)
	for _, name := range []string{"..", "/", "../..", ".", "../../etc/passwd", "session-8f3c1d2a/../.."} {
		t.Run("name "+name, func(t *testing.T) {
			dir, err := claude.TeamDir(home, name)
			if err != nil {
				t.Fatalf("TeamDir(%q) error = %v", name, err)
			}
			if !strings.HasPrefix(dir, teams) || strings.Contains(dir, "..") {
				t.Fatalf("TeamDir(%q) = %q", name, dir)
			}
			if dir, err = claude.TasksDir(home, name); err != nil || !strings.HasPrefix(dir, tasks) {
				t.Fatalf("TasksDir(%q) = %q, %v", name, dir, err)
			}
			// Nothing is there under that name, which is not a failure.
			if cfg, err := claude.ReadTeam(home, name); err != nil || len(cfg.Members) != 0 {
				t.Fatalf("ReadTeam(%q) = %+v, %v", name, cfg, err)
			}
			if list, err := claude.ReadTasks(home, name); err != nil || len(list) != 0 {
				t.Fatalf("ReadTasks(%q) = %+v, %v", name, list, err)
			}
		})
	}
	// A team with no name at all names no directory.
	for _, read := range []func() error{
		func() error { _, err := claude.ReadTeam(home, ""); return err },
		func() error { _, err := claude.ReadTasks(home, ""); return err },
		func() error { _, err := claude.TeamDir(home, ""); return err },
		func() error { _, err := claude.TasksDir(home, ""); return err },
	} {
		if err := read(); !errors.Is(err, claude.ErrTeamName) {
			t.Fatalf("a team with no name read with error %v", err)
		}
	}
}

// TestReadTeamRefusesAFileTooLarge reads nothing from a file no product wrote.
func TestReadTeamRefusesAFileTooLarge(t *testing.T) {
	home := teamHome(t, "session-8f3c1d2a", "{"+strings.Repeat(" ", team.MaxConfigSize)+"}", nil)
	if _, err := claude.ReadTeam(home, "session-8f3c1d2a"); err == nil {
		t.Fatal("a team file past the limit was read")
	}
}

func TestReadTasks(t *testing.T) {
	home := teamHome(t, "session-8f3c1d2a", teamFile, map[string]string{
		"2.json":          task("2", "write the tests", "build-api", "pending"),
		"1.json":          task("1", "review the router", "review-api", "in_progress"),
		"10.json":         task("10", "ship it", "", "pending"),
		"notes.md":        "not a task",
		".hidden.json":    task("x", "hidden", "", "pending"),
		"half-written.js": "{",
		"broken.json":     "{not json",
	})
	tasks, err := claude.ReadTasks(home, "session-8f3c1d2a")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tk := range tasks {
		ids = append(ids, tk.ID)
	}
	if strings.Join(ids, " ") != "1 2 10" {
		t.Fatalf("tasks %v, want the list in its own order", ids)
	}
	if c := team.Count(tasks); c != (team.Counts{Total: 3, Pending: 2, Running: 1}) {
		t.Fatalf("counts %+v", c)
	}
}

// TestReadTasksIsBounded reads a team with more tasks than a view shows, and
// takes the same ones every time.
func TestReadTasksIsBounded(t *testing.T) {
	files := map[string]string{}
	for i := range 300 {
		id := string(rune('a'+i/26)) + string(rune('a'+i%26))
		files[id+".json"] = task(id, "task "+id, "review-api", "pending")
	}
	home := teamHome(t, "session-8f3c1d2a", teamFile, files)
	first, err := claude.ReadTasks(home, "session-8f3c1d2a")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 256 {
		t.Fatalf("%d tasks read, want the reading bounded", len(first))
	}
	again, err := claude.ReadTasks(home, "session-8f3c1d2a")
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].ID != again[i].ID {
			t.Fatalf("two readings took different tasks: %q then %q", first[i].ID, again[i].ID)
		}
	}
}

// TestReadTasksUnreadableDirectory reports a directory that is there and
// cannot be read, which is not the same as a team with no tasks.
func TestReadTasksUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	home := teamHome(t, "session-8f3c1d2a", teamFile, map[string]string{"1.json": task("1", "s", "", "pending")})
	dir, err := claude.TasksDir(home, "session-8f3c1d2a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := claude.ReadTasks(home, "session-8f3c1d2a"); err == nil {
		t.Fatal("an unreadable task directory read as no tasks")
	}
}
