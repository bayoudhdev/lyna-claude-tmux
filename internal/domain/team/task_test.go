package team_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
)

func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "a team name is already a path", in: "session-1a2b3c4d", want: "session-1a2b3c4d"},
		{name: "letters digits underscore and dash are kept", in: "Team_9-b", want: "Team_9-b"},
		{name: "a separator cannot survive", in: "../../etc/passwd", want: "------etc-passwd"},
		{name: "spaces and dots become dashes", in: "my team.v2", want: "my-team-v2"},
		{name: "a character outside ascii becomes one dash", in: "\u00e9quipe", want: "-quipe"},
		{name: "nothing stays nothing", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := team.Slug(tc.in); got != tc.want {
				t.Fatalf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTaskID(t *testing.T) {
	cases := []struct {
		name string
		file string
		want string
	}{
		{name: "a task file is named after its task", file: "12.json", want: "12"},
		{name: "an identifier that is not a number", file: "review-api.json", want: "review-api"},
		{name: "the bookkeeping file is not a task", file: ".highwatermark"},
		{name: "a lock file is not a task", file: ".1.json.lock"},
		{name: "a hidden file is not a task", file: ".json"},
		{name: "a file of another kind is not a task", file: "progress.md"},
		{name: "a path is not a file name", file: "sub/1.json"},
		{name: "a windows path is not a file name", file: `sub\1.json`},
		{name: "no name at all", file: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := team.TaskID(tc.file)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("TaskID(%q) = %q, %v; want %q", tc.file, got, ok, tc.want)
			}
		})
	}
}

// taskList reads the fixture directory the way a caller reads a real one: every
// file the directory holds, only the ones TaskID accepts.
func taskList(t *testing.T) []team.Task {
	t.Helper()
	dir := filepath.Join("testdata", "tasks", "list")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var list []team.Task
	for _, e := range entries {
		if _, ok := team.TaskID(e.Name()); !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		task, err := team.ParseTask(data)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		list = append(list, task)
	}
	team.SortTasks(list)
	return list
}

func TestParseTaskReadsTheSharedList(t *testing.T) {
	list := taskList(t)
	if len(list) != 4 {
		t.Fatalf("%d tasks, want 4 (the bookkeeping file must not be one)", len(list))
	}
	cases := []struct {
		name   string
		task   team.Task
		label  string
		status team.Status
		owner  string
	}{
		{name: "a finished task", task: list[0], label: "Map how the project uses git today", status: team.StatusDone, owner: "explore-git"},
		{name: "a running task shows its present tense", task: list[1], label: "Reviewing the request handlers", status: team.StatusRunning, owner: "review-api"},
		{name: "a task nobody has claimed", task: list[2], label: "Bring the failing suite back to green", status: team.StatusPending},
		{name: "a task claimed but not started", task: list[3], label: "Write the release note", status: team.StatusPending, owner: "docs"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.task.Label() != tc.label || tc.task.Status != tc.status || tc.task.Owner != tc.owner {
				t.Fatalf("task %+v, want label %q status %q owner %q", tc.task, tc.label, tc.status, tc.owner)
			}
			if tc.task.Done() != (tc.status == team.StatusDone) {
				t.Fatalf("Done() = %v for status %q", tc.task.Done(), tc.status)
			}
			if want := []string{"1", "2", "3", "4"}[i]; tc.task.ID != want {
				t.Fatalf("task %d has id %q, want %q: SortTasks orders by number", i, tc.task.ID, want)
			}
		})
	}
	if got := list[0].Description; got == "" {
		t.Fatal("the description of a task is what the agent is told, and it is empty")
	}
	if got := list[3].BlockedBy; len(got) != 2 || got[0] != "2" || got[1] != "9" {
		t.Fatalf("blockedBy %v, want the two the file names", got)
	}
}

func TestWaitingOn(t *testing.T) {
	list := taskList(t)
	cases := []struct {
		name string
		task team.Task
		want []string
	}{
		{name: "a task waiting on a task that is finished waits on nothing", task: list[2]},
		{name: "a task waiting on a running task waits on it", task: list[3], want: []string{"2"}},
		{name: "a task waiting on nothing", task: list[0]},
		{name: "a blocker the list no longer holds is not one", task: team.Task{ID: "x", BlockedBy: []string{"99"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := team.WaitingOn(tc.task, list)
			if len(got) != len(tc.want) {
				t.Fatalf("WaitingOn = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("WaitingOn = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCount(t *testing.T) {
	cases := []struct {
		name string
		list []team.Task
		want team.Counts
	}{
		{
			name: "the fixture list", list: taskList(t),
			// Task 4 waits on task 2, which is running: one blocked.
			want: team.Counts{Total: 4, Pending: 2, Running: 1, Done: 1, Blocked: 1},
		},
		{name: "no tasks at all"},
		{
			name: "a status from a newer release is counted in the total only",
			list: []team.Task{{ID: "1", Status: "canceled"}},
			want: team.Counts{Total: 1},
		},
		{
			name: "a finished task waiting on an unfinished one is not blocked",
			list: []team.Task{{ID: "1", Status: team.StatusPending}, {ID: "2", Status: team.StatusDone, BlockedBy: []string{"1"}}},
			want: team.Counts{Total: 2, Pending: 1, Done: 1},
		},
		{
			name: "a task waiting on two open tasks is blocked once",
			list: []team.Task{
				{ID: "1", Status: team.StatusPending},
				{ID: "2", Status: team.StatusRunning},
				{ID: "3", Status: team.StatusPending, BlockedBy: []string{"1", "2"}},
			},
			want: team.Counts{Total: 3, Pending: 2, Running: 1, Blocked: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := team.Count(tc.list); got != tc.want {
				t.Fatalf("Count = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseTaskRefusals(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantErr error
		check   func(t *testing.T, task team.Task)
	}{
		{name: "not json at all", data: []byte("{")},
		{name: "a task that is a list", data: []byte("[]")},
		{name: "a task with no identifier", data: []byte(`{"subject":"do the thing"}`), wantErr: team.ErrNoTaskID},
		{
			name: "a file past the cap", wantErr: team.ErrTooLarge,
			data: append([]byte(`{"id":"1"}`), make([]byte, team.MaxTaskSize)...),
		},
		{
			name: "a status and fields from a newer release",
			data: func() []byte {
				data, err := os.ReadFile(filepath.Join("testdata", "tasks", "newer-claude.json"))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}(),
			check: func(t *testing.T, task team.Task) {
				if task.ID != "11" || task.Status != "canceled" {
					t.Fatalf("task %+v, want the status kept as it was read", task)
				}
				if task.Label() != "Ship the sidebar" {
					t.Fatalf("label %q", task.Label())
				}
			},
		},
		{
			name: "a running task with no present tense falls back to its subject",
			data: []byte(`{"id":"1","subject":"Ship it","status":"in_progress"}`),
			check: func(t *testing.T, task team.Task) {
				if task.Label() != "Ship it" {
					t.Fatalf("label %q", task.Label())
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, err := team.ParseTask(tc.data)
			switch {
			case tc.check != nil:
				if err != nil {
					t.Fatal(err)
				}
				tc.check(t, task)
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
			case err == nil:
				t.Fatalf("no error, task %+v", task)
			}
		})
	}
}

func TestSortTasksOrdersIdentifiersThatAreNotNumbers(t *testing.T) {
	list := []team.Task{{ID: "b"}, {ID: "10"}, {ID: "2"}, {ID: "a"}}
	team.SortTasks(list)
	want := []string{"2", "10", "a", "b"}
	for i, id := range want {
		if list[i].ID != id {
			t.Fatalf("order %v, want %v", list, want)
		}
	}
}

func FuzzParseTask(f *testing.F) {
	for _, path := range []string{
		filepath.Join("testdata", "tasks", "list", "1.json"),
		filepath.Join("testdata", "tasks", "list", "4.json"),
		filepath.Join("testdata", "tasks", "newer-claude.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		task, err := team.ParseTask(data)
		if err != nil {
			return
		}
		if task.ID == "" {
			t.Fatalf("a task with no identifier survived: %+v", task)
		}
		if task.Label() == "" && task.Subject != "" {
			t.Fatalf("a task with a subject has no label: %+v", task)
		}
		list := []team.Task{task}
		if c := team.Count(list); c.Total != 1 {
			t.Fatalf("Count over one task reports %+v", c)
		}
		if got := team.WaitingOn(task, list); len(got) > len(task.BlockedBy) {
			t.Fatalf("WaitingOn = %v, longer than blockedBy %v", got, task.BlockedBy)
		}
	})
}

func FuzzSlug(f *testing.F) {
	for _, s := range []string{"session-1a2b3c4d", "../etc", "my team.v2", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := team.Slug(s)
		for _, r := range got {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				t.Fatalf("Slug(%q) = %q, which holds %q", s, got, r)
			}
		}
		if team.Slug(got) != got {
			t.Fatalf("Slug is not stable: %q then %q", got, team.Slug(got))
		}
	})
}
