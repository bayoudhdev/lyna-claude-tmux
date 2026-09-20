package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	todoOne = "37bb21d0a2ca10df4f0da04ad0a4a56f9f7f8f0b"
	todoTwo = "a6360f0b9d7b64a39a3ee7f1f1b58c1a6b1a5a2c"
)

// TestRebaseTodoCommand runs the command the way git runs it: the plan file
// the workstation wrote, then the todo file git is waiting on.
func TestRebaseTodoCommand(t *testing.T) {
	cases := []struct {
		name    string
		plan    string
		todo    string
		args    func(plan, todo string) []string
		want    string
		wantErr bool
	}{
		{
			name: "a plan put where git reads it",
			plan: "pick " + todoOne + " # one\nfixup " + todoTwo + " # two\n",
			todo: "pick " + todoOne + "\npick " + todoTwo + "\n\n# Rebase abc onto def\n",
			want: "pick " + todoOne + " # one\nfixup " + todoTwo + " # two\n",
		},
		{
			name:    "a plan that folds a commit into nothing",
			plan:    "squash " + todoOne + " # one\n",
			todo:    "pick " + todoOne + "\n",
			wantErr: true,
		},
		{
			name:    "a plan that is not there",
			todo:    "pick " + todoOne + "\n",
			args:    func(_, todo string) []string { return []string{"rebase-todo", "nowhere", todo} },
			wantErr: true,
		},
		{
			name:    "a file that names no commit",
			plan:    "pick " + todoOne + " # one\n",
			todo:    "not a todo list\n",
			wantErr: true,
		},
		{
			name:    "one argument alone",
			plan:    "pick " + todoOne + " # one\n",
			todo:    "pick " + todoOne + "\n",
			args:    func(plan, _ string) []string { return []string{"rebase-todo", plan} },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGlueEnv(t)
			dir := t.TempDir()
			plan := filepath.Join(dir, "plan")
			todo := filepath.Join(dir, "git-rebase-todo")
			if tc.plan != "" {
				if err := os.WriteFile(plan, []byte(tc.plan), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(todo, []byte(tc.todo), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"rebase-todo", plan, todo}
			if tc.args != nil {
				args = tc.args(plan, todo)
			}
			code, stdout, stderr := e.run(t, "", args...)
			data, err := os.ReadFile(todo)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr {
				if code == 0 {
					t.Fatalf("the command exited 0, want it refused (%q, %q)", stdout, stderr)
				}
				if string(data) != tc.todo {
					t.Fatalf("the todo list reads as %q, want it left alone", data)
				}
				return
			}
			if code != 0 {
				t.Fatalf("the command exited %d: %q %q", code, stdout, stderr)
			}
			if string(data) != tc.want {
				t.Fatalf("the todo list reads as %q, want %q", data, tc.want)
			}
			if stdout != "" {
				t.Fatalf("the command printed %q, want nothing", stdout)
			}
		})
	}
}

// TestRebaseTodoCommandIsHidden keeps the sequence editor out of the help:
// git runs it, nobody types it.
func TestRebaseTodoCommandIsHidden(t *testing.T) {
	e := newGlueEnv(t)
	code, stdout, stderr := e.run(t, "", "--help")
	if code != 0 {
		t.Fatalf("help exited %d: %q", code, stderr)
	}
	if strings.Contains(stdout, "rebase-todo") {
		t.Fatalf("the help lists the sequence editor:\n%s", stdout)
	}
}
