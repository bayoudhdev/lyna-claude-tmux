package vcs

import (
	"errors"
	"strings"
	"testing"
)

const (
	todoA = "37bb21d0a2ca10df4f0da04ad0a4a56f9f7f8f0b"
	todoB = "a6360f0b9d7b64a39a3ee7f1f1b58c1a6b1a5a2c"
	todoC = "e3e7fad4e1b4a7f7b2a3f9ca2ff6f0b1df0e9a4d"
)

// steps writes a list of picks over the commits named.
func steps(oids ...string) Todo {
	t := Todo{}
	for _, oid := range oids {
		t.Steps = append(t.Steps, TodoStep{Action: TodoPick, OID: oid, Subject: "commit " + oid[:2]})
	}
	return t
}

// order reads a list back as the commits it replays, with their actions.
func order(t Todo) []string {
	out := make([]string, len(t.Steps))
	for i, s := range t.Steps {
		if s.Action == TodoOther {
			out[i] = "other " + s.Raw
			continue
		}
		out[i] = s.Action.String() + " " + s.OID[:2]
	}
	return out
}

func TestParseTodo(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    []string
		wantErr bool
	}{
		{name: "an empty list"},
		{
			name: "the list git writes",
			data: "pick " + todoA + " # commit number 1\npick " + todoB + " # commit number 2\n\n# Rebase abc onto def (2 commands)\n#\n",
			want: []string{"pick 37", "pick a6"},
		},
		{
			name: "the commands written short",
			data: "p " + todoA + " # one\nr " + todoB + " # two\ns " + todoC + " # three\n",
			want: []string{"pick 37", "reword a6", "squash e3"},
		},
		{
			name: "a subject written with no hash sign",
			data: "pick " + todoA + " commit number 1\n",
			want: []string{"pick 37"},
		},
		{
			name: "a command this does not model",
			data: "pick " + todoA + " # one\nexec make check\nbreak\n",
			want: []string{"pick 37", "other exec make check", "other break"},
		},
		{
			name:    "a command with no commit on it",
			data:    "pick\n",
			wantErr: true,
		},
		{
			name:    "a commit that is no commit",
			data:    "pick not-a-hash # one\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTodo([]byte(tc.data))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTodo() = %+v, want a failure", got)
				}
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseTodo() error = %v, want %v", err, ErrMalformed)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTodo() error = %v", err)
			}
			if gotOrder := order(got); !equalStrings(gotOrder, tc.want) {
				t.Fatalf("ParseTodo() = %v, want %v", gotOrder, tc.want)
			}
		})
	}
}

func TestParseTodoCapsTheList(t *testing.T) {
	var b strings.Builder
	for range MaxTodoSteps + 1 {
		b.WriteString("pick " + todoA + " # one\n")
	}
	if got, err := ParseTodo([]byte(b.String())); err == nil {
		t.Fatalf("ParseTodo() read %d commands, want the list refused", len(got.Steps))
	}
}

func TestTodoRenderReadsBack(t *testing.T) {
	list := Todo{Steps: []TodoStep{
		{Action: TodoPick, OID: todoA, Subject: "commit number 1"},
		{Action: TodoFixup, OID: todoB},
		{Action: TodoOther, Raw: "exec make check"},
	}}
	want := "pick " + todoA + " # commit number 1\nfixup " + todoB + "\nexec make check\n"
	if got := string(list.Render()); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
	back, err := ParseTodo(list.Render())
	if err != nil {
		t.Fatalf("ParseTodo() error = %v", err)
	}
	if !equalStrings(order(back), order(list)) {
		t.Fatalf("the list read back as %v, want %v", order(back), order(list))
	}
}

func TestNewTodoFromHistory(t *testing.T) {
	// A history reads newest first; a todo list is written oldest first.
	commits := []Commit{
		{OID: todoC, Subject: "commit number 3"},
		{OID: todoB, Subject: "commit number 2"},
		{OID: todoA, Subject: "commit number 1"},
	}
	got := NewTodoFromHistory(commits)
	want := []string{"pick 37", "pick a6", "pick e3"}
	if !equalStrings(order(got), want) {
		t.Fatalf("NewTodoFromHistory() = %v, want %v", order(got), want)
	}
	if got.Steps[0].Subject != "commit number 1" {
		t.Fatalf("the first step reads as %+v", got.Steps[0])
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestTodoWith(t *testing.T) {
	cases := []struct {
		name    string
		list    Todo
		oid     string
		action  TodoAction
		want    []string
		wantErr bool
	}{
		{
			name: "a commit reworded", list: steps(todoA, todoB), oid: todoB, action: TodoReword,
			want: []string{"pick 37", "reword a6"},
		},
		{
			name: "a commit folded into the one before it", list: steps(todoA, todoB), oid: todoB, action: TodoFixup,
			want: []string{"pick 37", "fixup a6"},
		},
		{
			name: "a commit left out", list: steps(todoA, todoB), oid: todoA, action: TodoDrop,
			want: []string{"drop 37", "pick a6"},
		},
		{
			name: "a commit stopped on", list: steps(todoA, todoB), oid: todoA, action: TodoEdit,
			want: []string{"edit 37", "pick a6"},
		},
		{name: "the first commit folded into nothing", list: steps(todoA, todoB), oid: todoA, action: TodoSquash, wantErr: true},
		{name: "every commit left out", list: steps(todoA), oid: todoA, action: TodoDrop, wantErr: true},
		{name: "a commit that is not in the list", list: steps(todoA), oid: todoC, action: TodoDrop, wantErr: true},
		{name: "an action this does not model", list: steps(todoA, todoB), oid: todoB, action: TodoOther, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.list.With(tc.oid, tc.action)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("With() = %v, want it refused", order(got))
				}
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("With() error = %v, want %v", err, ErrMalformed)
				}
				return
			}
			if err != nil {
				t.Fatalf("With() error = %v", err)
			}
			if !equalStrings(order(got), tc.want) {
				t.Fatalf("With() = %v, want %v", order(got), tc.want)
			}
			// The list it was built from is left alone.
			if len(tc.list.Steps) > 0 && tc.list.Steps[tc.list.Index(tc.oid)].Action != TodoPick {
				t.Fatalf("With() changed the list it was given: %v", order(tc.list))
			}
		})
	}
}

func TestTodoMoved(t *testing.T) {
	cases := []struct {
		name    string
		oid     string
		by      int
		want    []string
		wantErr bool
	}{
		{name: "a commit moved earlier", oid: todoC, by: -1, want: []string{"pick 37", "pick e3", "pick a6"}},
		{name: "a commit moved later", oid: todoA, by: 1, want: []string{"pick a6", "pick 37", "pick e3"}},
		{name: "a commit moved to the top", oid: todoC, by: -2, want: []string{"pick e3", "pick 37", "pick a6"}},
		{name: "a commit that does not move", oid: todoB, by: 0, want: []string{"pick 37", "pick a6", "pick e3"}},
		{name: "a commit moved past the top", oid: todoA, by: -1, wantErr: true},
		{name: "a commit moved past the end", oid: todoC, by: 1, wantErr: true},
		{name: "a commit that is not in the list", oid: strings.Repeat("f", 40), by: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := steps(todoA, todoB, todoC)
			got, err := list.Moved(tc.oid, tc.by)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Moved() = %v, want it refused", order(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("Moved() error = %v", err)
			}
			if !equalStrings(order(got), tc.want) {
				t.Fatalf("Moved() = %v, want %v", order(got), tc.want)
			}
			if !equalStrings(order(list), []string{"pick 37", "pick a6", "pick e3"}) {
				t.Fatalf("Moved() changed the list it was given: %v", order(list))
			}
		})
	}
}

func TestTodoValidate(t *testing.T) {
	cases := []struct {
		name string
		list Todo
		ok   bool
	}{
		{name: "a list of picks", list: steps(todoA, todoB), ok: true},
		{
			name: "a fold into the commit before it",
			list: Todo{Steps: []TodoStep{{Action: TodoPick, OID: todoA}, {Action: TodoSquash, OID: todoB}}},
			ok:   true,
		},
		{
			name: "a commit dropped before a fold",
			list: Todo{Steps: []TodoStep{{Action: TodoDrop, OID: todoA}, {Action: TodoFixup, OID: todoB}}},
		},
		{name: "nothing at all", list: Todo{}},
		{
			name: "a commit named twice",
			list: Todo{Steps: []TodoStep{{Action: TodoPick, OID: todoA}, {Action: TodoPick, OID: todoA}}},
		},
		{name: "a commit that is no commit", list: Todo{Steps: []TodoStep{{Action: TodoPick, OID: "nope"}}}},
		{name: "a line with nothing on it", list: Todo{Steps: []TodoStep{{Action: TodoOther}}}},
		{
			name: "commands this does not model alone",
			list: Todo{Steps: []TodoStep{{Action: TodoOther, Raw: "exec make check"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.list.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate() accepted the list")
			}
		})
	}
}

func TestTodoActionString(t *testing.T) {
	cases := []struct {
		action TodoAction
		want   string
	}{
		{TodoPick, "pick"},
		{TodoReword, "reword"},
		{TodoEdit, "edit"},
		{TodoSquash, "squash"},
		{TodoFixup, "fixup"},
		{TodoDrop, "drop"},
		{TodoOther, "other"},
		{TodoAction(42), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.action.String(); got != tc.want {
				t.Fatalf("TodoAction(%d).String() = %q, want %q", tc.action, got, tc.want)
			}
			if tc.action == TodoOther || tc.want == "other" {
				return
			}
			if back, ok := ParseTodoAction(tc.want); !ok || back != tc.action {
				t.Fatalf("ParseTodoAction(%q) = %v, %v", tc.want, back, ok)
			}
		})
	}
}

func FuzzParseTodo(f *testing.F) {
	f.Add("pick " + todoA + " # commit number 1\npick " + todoB + " # two\n")
	f.Add("p " + todoA + "\nexec make check\n# a comment\n\n")
	f.Add("drop " + todoA + "\n")
	f.Fuzz(func(t *testing.T, data string) {
		got, err := ParseTodo([]byte(data))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("ParseTodo() error = %v, want %v", err, ErrMalformed)
			}
			return
		}
		for _, s := range got.Steps {
			if s.Action == TodoOther {
				if s.Raw == "" || strings.Contains(s.Raw, "\n") {
					t.Fatalf("a command kept as %q", s.Raw)
				}
				continue
			}
			if !IsObjectName(s.OID) {
				t.Fatalf("step %+v names %q, which is no commit", s, s.OID)
			}
			if strings.ContainsAny(s.Subject, "\n") {
				t.Fatalf("step %+v holds a line of its own in its subject", s)
			}
		}
		// What was read is written back as the same list.
		back, err := ParseTodo(got.Render())
		if err != nil {
			t.Fatalf("the list written back does not read: %v", err)
		}
		if !equalStrings(order(back), order(got)) {
			t.Fatalf("the list read back as %v, want %v", order(back), order(got))
		}
	})
}
