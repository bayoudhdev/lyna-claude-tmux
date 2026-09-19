package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/team"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tui"
)

// TestOpenTasksWithoutAWorkspace covers every way the task list can be asked
// to follow a workspace it cannot find.
func TestOpenTasksWithoutAWorkspace(t *testing.T) {
	cases := []struct {
		name    string
		tmux    string
		pane    string
		session string
		// want is the error the caller matches on, or says what a message
		// carries when nobody matches on the failure.
		want error
		says string
	}{
		{name: "outside tmux with no workspace named", want: ErrTasksNoWorkspace, says: "pass --session with a workspace name"},
		{name: "in tmux with no pane and no workspace named", tmux: "/tmp/outer,1,0", want: ErrTasksNoWorkspace},
		{name: "a workspace that is not running", session: "gone", want: ErrNoWorkspace},
		{name: "a name no workspace can carry", session: "../etc", says: "invalid session name"},
		{name: "a pane of no server", tmux: "/tmp/outer,1,0", pane: "%404", says: "cannot read the workspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHost(t)
			openServer(t, h)
			h.env["TMUX"], h.env["TMUX_PANE"] = c.tmux, c.pane
			h.refreshEnviron()
			_, err := OpenTasks(tmuxtest.Context(t), h.Host, TasksRequest{Session: c.session})
			switch {
			case err == nil:
				t.Fatal("the list opened on no workspace")
			case c.says != "" && !strings.Contains(err.Error(), c.says):
				t.Fatalf("error %v, want one saying %q", err, c.says)
			case c.want != nil && !errors.Is(err, c.want):
				t.Fatalf("error %v, want %v", err, c.want)
			}
		})
	}
}

// taskIDs lists the identifiers of a reading, in the order it holds them.
func taskIDs(u tui.TasksUpdate) []string {
	ids := make([]string, len(u.Tasks))
	for i, task := range u.Tasks {
		ids[i] = task.ID
	}
	return ids
}

// TestTasksReadsTheTeam takes readings on a real server, from a pane of the
// workspace, as the team works: the list in the order the team works through
// it, nothing new when nothing changed, the task a lead adds, and a list that
// cannot be read.
func TestTasksReadsTheTeam(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	openTeammateScene(t, h, s, 240, 60)
	if _, err := Teammate(ctx, h.Host, TeammateRequest{ClaudePath: "/bin/sh", Args: spawnArgs, AgentPanes: 3}); err != nil {
		t.Fatal(err)
	}
	writeTeam(t, h, "session-8f3c1d2a", teamConfig, map[string]string{
		"10.json": `{"id":"10","subject":"ship it","status":"pending","blockedBy":["2"]}`,
		"2.json":  `{"id":"2","subject":"write the tests","owner":"review-api","status":"in_progress"}`,
		"1.json":  `{"id":"1","subject":"read the plan","owner":"team-lead","status":"completed"}`,
	})
	view, err := OpenTasks(ctx, h.Host, TasksRequest{Popup: true})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Options.Popup {
		t.Fatal("the list asked for as a popup does not close like one")
	}
	tasksDir := filepath.Join(h.root, ".claude", "tasks", "session-8f3c1d2a")
	aside := tasksDir + " aside"
	// The steps below take the directory away and give it back, so the list is
	// read as unreadable over more than one reading. It is taken away by
	// putting a file where the directory was rather than by taking the
	// permission off it: root reads a directory whatever its mode says, and
	// the suite runs as root in a container. The directory is put back when
	// the whole test ends, not when the step that took it away does, or the
	// step after it would read a list that is there again.
	t.Cleanup(func() {
		if _, err := os.Stat(aside); err != nil {
			return
		}
		_ = os.Remove(tasksDir)
		_ = os.Rename(aside, tasksDir)
	})

	steps := []struct {
		name string
		// change is done to the files before the reading.
		change    func(t *testing.T)
		wantFresh bool
		wantIDs   []string
		wantErr   bool
	}{
		{name: "the first reading", wantFresh: true, wantIDs: []string{"1", "2", "10"}},
		{name: "nothing changed", wantIDs: []string{"1", "2", "10"}},
		{
			name: "the lead adds a task",
			change: func(t *testing.T) {
				t.Helper()
				writeTeam(t, h, "session-8f3c1d2a", "", map[string]string{"3.json": `{"id":"3","subject":"review it","status":"pending"}`})
			},
			wantFresh: true, wantIDs: []string{"1", "2", "3", "10"},
		},
		{
			name: "a teammate claims one",
			change: func(t *testing.T) {
				t.Helper()
				writeTeam(t, h, "session-8f3c1d2a", "", map[string]string{
					"3.json": `{"id":"3","subject":"review it","owner":"review-api","status":"in_progress"}`,
				})
			},
			wantFresh: true, wantIDs: []string{"1", "2", "3", "10"},
		},
		{
			name: "the list cannot be read",
			change: func(t *testing.T) {
				t.Helper()
				if err := os.Rename(tasksDir, aside); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tasksDir, []byte("not the directory it was"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantFresh: true, wantErr: true,
		},
		{name: "it still cannot", wantErr: true},
		{
			name: "it can again",
			change: func(t *testing.T) {
				t.Helper()
				if err := os.Remove(tasksDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(aside, tasksDir); err != nil {
					t.Fatal(err)
				}
			},
			wantFresh: true, wantIDs: []string{"1", "2", "3", "10"},
		},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.change != nil {
				st.change(t)
			}
			got, fresh := view.Read(ctx)
			if fresh != st.wantFresh {
				t.Fatalf("fresh = %v, want %v", fresh, st.wantFresh)
			}
			if (got.Err != nil) != st.wantErr {
				t.Fatalf("the reading failed with %v, want a failure %v", got.Err, st.wantErr)
			}
			if got.Team != "session-8f3c1d2a" {
				t.Fatalf("the reading is of team %q", got.Team)
			}
			if !slices.Equal(taskIDs(got), st.wantIDs) {
				t.Fatalf("the reading holds %v, want %v", taskIDs(got), st.wantIDs)
			}
		})
	}
}

// TestTasksFollowsItsWorkspace reads the list of the workspace the request
// names, from outside tmux, and a workspace running no team as no list at all.
func TestTasksFollowsItsWorkspace(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	openTeammateScene(t, h, s, 240, 60)
	h.env["TMUX"], h.env["TMUX_PANE"] = "", ""
	h.refreshEnviron()

	view, err := OpenTasks(ctx, h.Host, TasksRequest{Session: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Options.Popup {
		t.Fatal("a list in a terminal closes like a popup")
	}
	got, fresh := view.Read(ctx)
	if !fresh || got.Err != nil || got.Team != "" || len(got.Tasks) != 0 {
		t.Fatalf("the reading of a workspace with no team is %+v, fresh %v", got, fresh)
	}
}

// TestTasksWaitsOnItsWorkspace checks the list is woken by the signal the hooks
// send its own workspace when a task is created or finished.
func TestTasksWaitsOnItsWorkspace(t *testing.T) {
	ctx := tmuxtest.Context(t)
	h := newTestHost(t)
	s := openServer(t, h)
	scene := openTeammateScene(t, h, s, 240, 60)
	view, err := OpenTasks(ctx, h.Host, TasksRequest{})
	if err != nil {
		t.Fatal(err)
	}
	woken := make(chan error, 1)
	go func() { woken <- view.Signal(ctx) }()
	id, err := s.Client.Display(ctx, scene.pane, "#{session_id}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.Run(ctx, "wait-for", "-S", tmux.AgentsChannel(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-woken:
		if err != nil {
			t.Fatalf("the signal failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the list was not woken by its workspace's signal")
	}
}

// TestSameTasks decides which readings draw the same list.
func TestSameTasks(t *testing.T) {
	base := tui.TasksUpdate{Team: "crew", Tasks: []team.Task{
		{ID: "1", Subject: "read", Owner: "a", Status: team.StatusDone, Blocks: []string{"2"}},
		{ID: "2", Subject: "write", Status: team.StatusPending, BlockedBy: []string{"1"}},
	}}
	with := func(change func(u *tui.TasksUpdate)) tui.TasksUpdate {
		u := tui.TasksUpdate{Team: base.Team, Err: base.Err}
		for _, task := range base.Tasks {
			task.Blocks, task.BlockedBy = slices.Clone(task.Blocks), slices.Clone(task.BlockedBy)
			u.Tasks = append(u.Tasks, task)
		}
		change(&u)
		return u
	}
	cases := []struct {
		name string
		b    tui.TasksUpdate
		want bool
	}{
		{name: "the same reading", b: with(func(*tui.TasksUpdate) {}), want: true},
		{name: "another team", b: with(func(u *tui.TasksUpdate) { u.Team = "other" })},
		{name: "a task more", b: with(func(u *tui.TasksUpdate) { u.Tasks = append(u.Tasks, team.Task{ID: "3"}) })},
		{name: "a subject rewritten", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].Subject = "write more" })},
		{name: "a description rewritten", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].Description = "why" })},
		{name: "a task claimed", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].Owner = "b" })},
		{name: "a task started", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].Status = team.StatusRunning })},
		{name: "a running form", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].ActiveForm = "writing" })},
		{name: "a blocker added", b: with(func(u *tui.TasksUpdate) { u.Tasks[1].BlockedBy = append(u.Tasks[1].BlockedBy, "3") })},
		{name: "a task that no longer blocks", b: with(func(u *tui.TasksUpdate) { u.Tasks[0].Blocks = nil })},
		{name: "a failure", b: with(func(u *tui.TasksUpdate) { u.Err = errors.New("permission denied") })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameTasks(base, tc.b); got != tc.want {
				t.Fatalf("sameTasks = %v, want %v", got, tc.want)
			}
		})
	}
	// Two failures that say the same are the same reading, and a list read as
	// missing is the list read as empty.
	failed := tui.TasksUpdate{Err: errors.New("permission denied")}
	if !sameTasks(failed, tui.TasksUpdate{Err: errors.New("permission denied")}) {
		t.Fatal("the same failure twice reads as a change")
	}
	if !sameTasks(tui.TasksUpdate{Team: "crew"}, tui.TasksUpdate{Team: "crew", Tasks: []team.Task{}}) {
		t.Fatal("a missing list and an empty one read as a change")
	}
}
