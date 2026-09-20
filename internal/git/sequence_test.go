package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

const (
	seqA = "37bb21d0a2ca10df4f0da04ad0a4a56f9f7f8f0b"
	seqB = "a6360f0b9d7b64a39a3ee7f1f1b58c1a6b1a5a2c"
)

func TestWriteTodo(t *testing.T) {
	const written = "pick " + seqA + " # one\nfixup " + seqB + " # two\n"
	cases := []struct {
		name    string
		plan    string
		todo    string
		noPlan  bool
		want    string
		wantErr bool
	}{
		{
			name: "a plan put where git reads it",
			plan: written,
			todo: "pick " + seqA + " # one\npick " + seqB + " # two\n\n# Rebase abc onto def\n",
			want: written,
		},
		{
			name: "a plan that drops a commit",
			plan: "drop " + seqA + " # one\npick " + seqB + " # two\n",
			todo: "pick " + seqA + "\npick " + seqB + "\n",
			want: "drop " + seqA + " # one\npick " + seqB + " # two\n",
		},
		{
			name:    "a plan that folds the first commit into nothing",
			plan:    "squash " + seqA + " # one\n",
			todo:    "pick " + seqA + "\n",
			wantErr: true,
		},
		{
			name:    "a plan that replays nothing at all",
			plan:    "drop " + seqA + " # one\n",
			todo:    "pick " + seqA + "\n",
			wantErr: true,
		},
		{
			name:    "a plan that is no todo list",
			plan:    "pick not-a-hash\n",
			todo:    "pick " + seqA + "\n",
			wantErr: true,
		},
		{
			name:    "a plan with nothing in it",
			plan:    "# nothing here\n",
			todo:    "pick " + seqA + "\n",
			wantErr: true,
		},
		{
			name:    "a file that is not the todo list of a rebase",
			plan:    written,
			todo:    "this is not a todo list at all\n",
			wantErr: true,
		},
		{
			name:    "a plan that is not there",
			noPlan:  true,
			todo:    "pick " + seqA + "\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			plan := filepath.Join(dir, "plan")
			todo := filepath.Join(dir, "git-rebase-todo")
			if !tc.noPlan {
				if err := os.WriteFile(plan, []byte(tc.plan), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(todo, []byte(tc.todo), 0o600); err != nil {
				t.Fatal(err)
			}
			err := WriteTodo(plan, todo)
			data, readErr := os.ReadFile(todo)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("WriteTodo() went through, want it refused")
				}
				if string(data) != tc.todo {
					t.Fatalf("the todo list reads as %q, want it left alone", data)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteTodo() error = %v", err)
			}
			if string(data) != tc.want {
				t.Fatalf("the todo list reads as %q, want %q", data, tc.want)
			}
		})
	}
}

// TestWriteTodoRefusesLinks keeps the editor to plain files: a link put where
// the plan or the todo list belongs would have it read or write elsewhere.
func TestWriteTodoRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("pick "+seqA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "plan")
	todo := filepath.Join(dir, "git-rebase-todo")
	if err := os.WriteFile(plan, []byte("pick "+seqA+" # one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(todo, []byte("pick "+seqA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a plan that is a link", func(t *testing.T) {
		link := filepath.Join(dir, "linked-plan")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(link) })
		if err := WriteTodo(link, todo); err == nil {
			t.Fatal("WriteTodo() read through a link")
		}
	})
	t.Run("a todo list that is a link", func(t *testing.T) {
		link := filepath.Join(dir, "linked-todo")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(link) })
		if err := WriteTodo(plan, link); err == nil {
			t.Fatal("WriteTodo() wrote through a link")
		}
		data, err := os.ReadFile(elsewhere)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "pick "+seqA+"\n" {
			t.Fatalf("the file behind the link reads as %q", data)
		}
	})
}

// TestSequenceEditorIsReadByAShell runs the editor string the way git runs
// it, through a shell, with the binary sitting behind a path a shell would
// otherwise take apart.
func TestSequenceEditorIsReadByAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell")
	}
	cases := []struct {
		name string
		dir  string
		plan string
	}{
		{name: "plain paths", dir: "tools", plan: "plan"},
		{name: "a path holding a space", dir: "my tools", plan: "a plan"},
		{name: "a path holding a quote", dir: "o'brien", plan: "o'brien plan"},
		{name: "a path holding a dollar sign", dir: "$HOME", plan: "$(id)"},
		{name: "a path holding a backtick", dir: "`id`", plan: "`id` plan"},
		{name: "a path holding a semicolon", dir: "a;rm -rf x", plan: "b;id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), tc.dir)
			if err := os.MkdirAll(base, 0o750); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(base, "lmux")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			plan := filepath.Join(base, tc.plan)
			// git runs `sh -c '<editor> "$@"' - <todo>`, which is the editor
			// string followed by the file it is waiting on.
			out, err := exec.Command(sh, "-c", SequenceEditor(bin, plan)+` "$@"`, "-", "/tmp/todo").Output()
			if err != nil {
				t.Fatalf("the shell refused the editor string %q: %v", SequenceEditor(bin, plan), err)
			}
			got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
			want := []string{"rebase-todo", plan, "/tmp/todo"}
			if !slices.Equal(got, want) {
				t.Fatalf("the shell read %v, want %v", got, want)
			}
		})
	}
}

// TestShellQuote runs every quoted string through a shell and checks the word
// that comes out is the one that went in.
func TestShellQuote(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell")
	}
	cases := []string{
		"plain",
		"/usr/local/bin/lmux",
		"a space",
		"a'quote",
		`a"double`,
		"a$VAR",
		"a`id`",
		"a;id",
		"a\nnewline",
		"a\\backslash",
		"*",
		"",
	}
	for _, in := range cases {
		t.Run(strings.ReplaceAll(in, "\n", "\\n"), func(t *testing.T) {
			out, err := exec.Command(sh, "-c", "printf '%s' "+shellQuote(in)).Output()
			if err != nil {
				t.Fatalf("the shell refused %q: %v", shellQuote(in), err)
			}
			if string(out) != in {
				t.Fatalf("the shell read %q, want %q", out, in)
			}
		})
	}
}

func FuzzShellQuote(f *testing.F) {
	f.Add("/usr/local/bin/lmux")
	f.Add("a'quote")
	f.Add("a $VAR `id`")
	f.Fuzz(func(t *testing.T, s string) {
		quoted := shellQuote(s)
		if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
			t.Fatalf("shellQuote(%q) = %q, which is no quoted word", s, quoted)
		}
		// Nothing a shell acts on survives outside the quotes.
		inside := quoted[1 : len(quoted)-1]
		for _, part := range strings.Split(inside, `'\''`) {
			if strings.Contains(part, "'") {
				t.Fatalf("shellQuote(%q) = %q, with a quote left open", s, quoted)
			}
		}
	})
}

// TestWriteTodoKeepsWhatItWrote proves the file git reads back is the plan,
// parsed as the same list.
func TestWriteTodoKeepsWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan")
	todo := filepath.Join(dir, "git-rebase-todo")
	list := vcs.Todo{Steps: []vcs.TodoStep{
		{Action: vcs.TodoPick, OID: seqA, Subject: "one"},
		{Action: vcs.TodoReword, OID: seqB, Subject: "two"},
	}}
	if err := os.WriteFile(plan, list.Render(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(todo, []byte("pick "+seqA+"\npick "+seqB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteTodo(plan, todo); err != nil {
		t.Fatalf("WriteTodo() error = %v", err)
	}
	data, err := os.ReadFile(todo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := vcs.ParseTodo(data)
	if err != nil {
		t.Fatalf("ParseTodo() error = %v", err)
	}
	if len(got.Steps) != 2 || got.Steps[1].Action != vcs.TodoReword || got.Steps[1].OID != seqB {
		t.Fatalf("the todo list reads as %+v", got.Steps)
	}
	info, err := os.Stat(todo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the todo list is %v", info.Mode().Perm())
	}
}
