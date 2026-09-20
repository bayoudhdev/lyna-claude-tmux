package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// formSizes are the frame sizes a form is drawn at: the pane of a workstation
// and a narrow one.
var formSizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "64x22", width: 64, height: 22, ansi: true},
	{name: "34x16", width: 34, height: 16},
}

func newTestForm(t *testing.T, f GitForm) *GitFormModel {
	t.Helper()
	m := NewGitForm(GitFormOptions{Styles: goldenStyles(t), Width: 64, Height: 22})
	m.Ask(f)
	return m
}

func formFrame(m *GitFormModel) string { return ansi.Strip(m.View().Content) }

// formKeys sends keys and returns the messages the commands produced.
func formKeys(m *GitFormModel, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := m.Update(press(k))
		out = append(out, collect(cmd)...)
	}
	return out
}

// formAnswer is the single message a form reports, or false when it reported
// nothing.
func formAnswer(t *testing.T, msgs []tea.Msg) (GitFormDoneMsg, bool) {
	t.Helper()
	if len(msgs) == 0 {
		return GitFormDoneMsg{}, false
	}
	if len(msgs) > 1 {
		t.Fatalf("the form reported %d messages, want one: %+v", len(msgs), msgs)
	}
	done, ok := msgs[0].(GitFormDoneMsg)
	if !ok {
		t.Fatalf("the form reported %+v, want an answer", msgs[0])
	}
	return done, true
}

func TestGitFormFrames(t *testing.T) {
	t.Parallel()
	states := []struct {
		name string
		form GitForm
		keys []string
	}{
		{
			name: "discard-file",
			form: DiscardFileForm("internal/tui/gitform.go", []string{"git", "restore", "--", "internal/tui/gitform.go"}),
		},
		{
			name: "discard-all",
			form: DiscardAllForm("feat/git-workstation", 12, []string{"git", "restore", "--staged", "--worktree", "--", "."}),
		},
		{
			name: "discard-all-typed",
			form: DiscardAllForm("feat/git-workstation", 12, []string{"git", "restore", "--staged", "--worktree", "--", "."}),
			keys: []string{"feat/git-workstation"},
		},
		{
			name: "discard-all-mistyped",
			form: DiscardAllForm("feat/git-workstation", 12, []string{"git", "restore", "--staged", "--worktree", "--", "."}),
			keys: []string{"main", "enter"},
		},
		{
			name: "reset-hard",
			form: ResetHardForm("main", "a1b2c3d", 3, []string{"git", "reset", "--hard", "--", "a1b2c3d"}),
		},
		{
			name: "reset-hard-nothing-behind",
			form: ResetHardForm("main", "a1b2c3d", 0, []string{"git", "reset", "--hard", "--", "a1b2c3d"}),
		},
		{
			name: "delete-branch-merged",
			form: DeleteBranchForm("fix/border", true, []string{"git", "branch", "--delete", "--", "fix/border"}),
		},
		{
			name: "delete-branch-unmerged",
			form: DeleteBranchForm("fix/border", false, []string{"git", "branch", "-D", "--", "fix/border"}),
		},
		{
			name: "drop-stash",
			form: DropStashForm(2, "the lanes of the graph", []string{"git", "stash", "drop", "--", "stash@{2}"}),
		},
		{
			name: "clear-stashes",
			form: ClearStashesForm(4, []string{"git", "stash", "clear"}),
		},
		{
			name: "remove-worktree",
			form: RemoveWorktreeForm("review", "/home/dev/src/acme/.claude/worktrees/review", false,
				[]string{"git", "worktree", "remove", "--", "/home/dev/src/acme/.claude/worktrees/review"}),
		},
		{
			name: "remove-worktree-dirty",
			form: RemoveWorktreeForm("review", "/home/dev/src/acme/.claude/worktrees/review", true,
				[]string{"git", "worktree", "remove", "--force", "--", "/home/dev/src/acme/.claude/worktrees/review"}),
		},
		{
			name: "force-push-lease",
			form: ForcePushForm("origin", "feat/git-workstation", true,
				[]string{"git", "push", "--force-with-lease=feat/git-workstation:a1b2c3d", "--", "origin", "feat/git-workstation"}),
		},
		{
			name: "force-push",
			form: ForcePushForm("origin", "feat/git-workstation", false,
				[]string{"git", "push", "--force", "--", "origin", "feat/git-workstation"}),
		},
		{
			name: "text",
			form: TextForm("commit", "Commit what is staged", "4 files, +512 -16.", "a subject in the imperative", true, nil),
		},
		{
			name: "text-typed",
			form: TextForm("commit", "Commit what is staged", "4 files, +512 -16.", "a subject in the imperative", true, nil),
			keys: []string{"read one commit in full"},
		},
		{
			name: "text-empty",
			form: TextForm("commit", "Commit what is staged", "4 files, +512 -16.", "a subject in the imperative", true, nil),
			keys: []string{"enter"},
		},
		{
			name: "text-refused",
			form: TextForm("branch", "Open a branch here", "The branch starts at a1b2c3d.", "a name", true,
				func(string) error { return errors.New("git branch: a name holds no space") }),
			keys: []string{"two words", "enter"},
		},
		{
			name: "nothing",
		},
	}
	for _, size := range formSizes {
		for _, st := range states {
			t.Run(st.name+"-"+size.name, func(t *testing.T) {
				t.Parallel()
				m := NewGitForm(GitFormOptions{Styles: goldenStyles(t), Width: size.width, Height: size.height})
				if st.form.Title != "" {
					m.Ask(st.form)
				}
				formKeys(m, st.keys...)
				assertFrame(t, "gitform/"+st.name+"-"+size.name, m, size.width, size.height, size.ansi)
			})
		}
	}
}

func TestGitFormConfirmKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		keys     []string
		answered bool
		ok       bool
	}{
		{name: "y runs it", keys: []string{"y"}, answered: true, ok: true},
		{name: "enter runs it", keys: []string{"enter"}, answered: true, ok: true},
		{name: "n backs out", keys: []string{"n"}, answered: true},
		{name: "esc backs out", keys: []string{"esc"}, answered: true},
		{name: "q backs out", keys: []string{"q"}, answered: true},
		{name: "ctrl+c backs out", keys: []string{"ctrl+c"}, answered: true},
		{name: "any other key waits", keys: []string{"x", "k", "space"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestForm(t, DiscardFileForm("a.txt", []string{"git", "restore", "--", "a.txt"}))
			done, answered := formAnswer(t, formKeys(m, tc.keys...))
			switch {
			case answered != tc.answered:
				t.Fatalf("the form answered = %v, want %v", answered, tc.answered)
			case !answered:
				if !m.Asking() {
					t.Fatal("the form closed without answering")
				}
				return
			}
			if done.OK != tc.ok || done.ID != "discard-file" || done.Value != "" {
				t.Fatalf("the form reported %+v, want ok = %v for discard-file", done, tc.ok)
			}
			if m.Asking() {
				t.Fatal("the form is still up after answering")
			}
		})
	}
}

func TestGitFormTypesTheNameBack(t *testing.T) {
	t.Parallel()
	const branch = "feat/git-workstation"
	cases := []struct {
		name     string
		keys     []string
		answered bool
		ok       bool
	}{
		{name: "the name typed back runs it", keys: []string{branch, "enter"}, answered: true, ok: true},
		{name: "trailing space is not a mistake", keys: []string{branch, "space", "enter"}, answered: true, ok: true},
		{name: "another name does not", keys: []string{"main", "enter"}},
		{name: "half the name does not", keys: []string{"feat/git", "enter"}},
		{name: "one key of habit does not", keys: []string{"y"}},
		{name: "enter on an empty field does not", keys: []string{"enter"}},
		{name: "esc backs out", keys: []string{branch, "esc"}, answered: true},
		{name: "ctrl+c backs out", keys: []string{branch, "ctrl+c"}, answered: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestForm(t, DiscardAllForm(branch, 3, []string{"git", "restore", "--staged", "--worktree", "--", "."}))
			done, answered := formAnswer(t, formKeys(m, tc.keys...))
			if answered != tc.answered {
				t.Fatalf("the form answered = %v, want %v", answered, tc.answered)
			}
			if !answered {
				return
			}
			if done.OK != tc.ok {
				t.Fatalf("the form reported %+v, want ok = %v", done, tc.ok)
			}
			if tc.ok && done.Value != branch {
				t.Fatalf("the form reported %q, want the name typed back", done.Value)
			}
		})
	}
}

func TestGitFormCollectsALine(t *testing.T) {
	t.Parallel()
	refused := errors.New("git branch: a name holds no space")
	cases := []struct {
		name     string
		form     GitForm
		keys     []string
		answered bool
		want     string
	}{
		{
			name:     "a line typed",
			form:     TextForm("commit", "Commit", "4 files.", "a subject", true, nil),
			keys:     []string{"read one commit in full", "enter"},
			answered: true, want: "read one commit in full",
		},
		{
			name:     "the spaces around it are not part of it",
			form:     TextForm("commit", "Commit", "4 files.", "a subject", true, nil),
			keys:     []string{"space", "a subject", "space", "enter"},
			answered: true, want: "a subject",
		},
		{
			name: "an empty field is refused when one is needed",
			form: TextForm("commit", "Commit", "4 files.", "a subject", true, nil),
			keys: []string{"enter"},
		},
		{
			name:     "an empty field goes through when none is",
			form:     TextForm("stash", "Stash", "8 files.", "a message", false, nil),
			keys:     []string{"enter"},
			answered: true,
		},
		{
			name: "a value the check refuses",
			form: TextForm("branch", "Branch", "At a1b2c3d.", "a name", true, func(string) error { return refused }),
			keys: []string{"two words", "enter"},
		},
		{
			name: "a value the check takes",
			form: TextForm("branch", "Branch", "At a1b2c3d.", "a name", true, func(v string) error {
				if strings.Contains(v, " ") {
					return refused
				}
				return nil
			}),
			keys:     []string{"fix/border", "enter"},
			answered: true, want: "fix/border",
		},
		{
			name:     "what was typed is taken back key by key",
			form:     TextForm("branch", "Branch", "At a1b2c3d.", "a name", true, nil),
			keys:     []string{"fix/borderr", "backspace", "enter"},
			answered: true, want: "fix/border",
		},
		{
			name: "ctrl+u empties the field",
			form: TextForm("commit", "Commit", "4 files.", "a subject", true, nil),
			keys: []string{"a subject", "ctrl+u", "enter"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestForm(t, tc.form)
			done, answered := formAnswer(t, formKeys(m, tc.keys...))
			if answered != tc.answered {
				t.Fatalf("the form answered = %v, want %v", answered, tc.answered)
			}
			if !answered {
				if !m.Asking() {
					t.Fatal("a form that refused what was typed closed anyway")
				}
				return
			}
			if !done.OK || done.Value != tc.want || done.ID != tc.form.ID {
				t.Fatalf("the form reported %+v, want %q for %s", done, tc.want, tc.form.ID)
			}
		})
	}
}

// TestGitFormSaysWhyItRefused draws the reason back, which is the whole point
// of catching a value here rather than in a command that failed.
func TestGitFormSaysWhyItRefused(t *testing.T) {
	t.Parallel()
	m := newTestForm(t, TextForm("branch", "Branch", "At a1b2c3d.", "a name", true,
		func(string) error { return errors.New("git branch: a name holds no space") }))
	formKeys(m, "two words", "enter")
	if !strings.Contains(formFrame(m), "a name holds no space") {
		t.Fatalf("the frame does not say why it refused:\n%s", formFrame(m))
	}
	// Typing again takes the reason away: it is about what was refused, not
	// about what is being typed now.
	formKeys(m, "x")
	if strings.Contains(formFrame(m), "a name holds no space") {
		t.Fatalf("the reason is still on screen after more was typed:\n%s", formFrame(m))
	}
}

func TestGitFormStartsWithAValue(t *testing.T) {
	t.Parallel()
	f := TextForm("message", "Edit the message", "The message of HEAD.", "a subject", true, nil)
	f.Value = "read one commit in full"
	m := newTestForm(t, f)
	if got := m.Value(); got != f.Value {
		t.Fatalf("Value() = %q, want the message it opened with", got)
	}
	if !strings.Contains(formFrame(m), "read one commit in full") {
		t.Fatalf("the frame does not draw the message it opened with:\n%s", formFrame(m))
	}
	done, _ := formAnswer(t, formKeys(m, " again", "enter"))
	if done.Value != "read one commit in full again" {
		t.Fatalf("the form reported %q, want the message as it was edited", done.Value)
	}
}

func TestGitFormAsksOneAtATime(t *testing.T) {
	t.Parallel()
	m := NewGitForm(GitFormOptions{Styles: goldenStyles(t), Width: 64, Height: 22})
	if m.Asking() {
		t.Fatal("a form is up before anything was asked")
	}
	// A key pressed with no form up reports nothing.
	if msgs := formKeys(m, "y", "enter", "esc"); len(msgs) != 0 {
		t.Fatalf("a form asking nothing reported %v", msgs)
	}
	if len(m.Box(64, 22)) != 0 {
		t.Fatal("a form asking nothing drew a box")
	}
	m.Ask(TextForm("a", "A", "the first.", "", true, nil))
	formKeys(m, "typed")
	// Another question replaces the first, field and all.
	m.Ask(DiscardFileForm("a.txt", []string{"git", "restore", "--", "a.txt"}))
	if m.Value() != "" {
		t.Fatalf("Value() = %q, want the field of the first question gone", m.Value())
	}
	done, _ := formAnswer(t, formKeys(m, "y"))
	if done.ID != "discard-file" {
		t.Fatalf("the form reported %+v, want the question that replaced the first", done)
	}
	// Answering twice reports once, whichever key is pressed after it.
	if msgs := formKeys(m, "y", "n", "ctrl+c", "esc"); len(msgs) != 0 {
		t.Fatalf("a form already answered reported %v", msgs)
	}
}

func TestGitFormConstructors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		form   GitForm
		ask    GitAsk
		expect string
		// lose says the form names what cannot be given back.
		lose bool
	}{
		{
			name: "a file discarded", form: DiscardFileForm("a.txt", nil),
			ask: AskConfirm, lose: true,
		},
		{
			name: "everything discarded", form: DiscardAllForm("main", 3, nil),
			ask: AskName, expect: "main", lose: true,
		},
		{
			name: "the tree written over", form: ResetHardForm("main", "a1b2c3d", 2, nil),
			ask: AskName, expect: "main", lose: true,
		},
		{
			name: "a branch already merged", form: DeleteBranchForm("fix/border", true, nil),
			ask: AskConfirm,
		},
		{
			name: "a branch merged nowhere", form: DeleteBranchForm("fix/border", false, nil),
			ask: AskName, expect: "fix/border", lose: true,
		},
		{
			name: "one stash dropped", form: DropStashForm(0, "the lanes", nil),
			ask: AskConfirm, lose: true,
		},
		{
			name: "every stash dropped", form: ClearStashesForm(4, nil),
			ask: AskName, expect: "clear", lose: true,
		},
		{
			name: "a worktree with nothing in it", form: RemoveWorktreeForm("review", "/w/review", false, nil),
			ask: AskConfirm,
		},
		{
			name: "a worktree holding changes", form: RemoveWorktreeForm("review", "/w/review", true, nil),
			ask: AskName, expect: "review", lose: true,
		},
		{
			name: "a push held to what the remote stands on", form: ForcePushForm("origin", "main", true, nil),
			ask: AskName, expect: "main", lose: true,
		},
		{
			name: "a push written over the remote", form: ForcePushForm("origin", "main", false, nil),
			ask: AskName, expect: "main", lose: true,
		},
		{
			name: "a line collected", form: TextForm("commit", "Commit", "4 files.", "a subject", true, nil),
			ask: AskText,
		},
	}
	ids := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.form.Ask != tc.ask || tc.form.Expect != tc.expect {
				t.Fatalf("the form asks %v for %q, want %v for %q", tc.form.Ask, tc.form.Expect, tc.ask, tc.expect)
			}
			if got := len(tc.form.Lose) > 0; got != tc.lose {
				t.Fatalf("the form names what is lost = %v, want %v: %+v", got, tc.lose, tc.form.Lose)
			}
			if tc.form.ID == "" || tc.form.Title == "" || tc.form.Question == "" {
				t.Fatalf("the form is missing its id, its title or its question: %+v", tc.form)
			}
			// Every form that cannot be undone is answered by typing, never
			// by one key.
			if len(tc.form.Lose) > 0 && tc.form.Ask == AskConfirm && tc.form.ID != "discard-file" && tc.form.ID != "drop-stash" {
				t.Fatalf("%s runs on one key although it names what is lost", tc.form.ID)
			}
		})
		ids[tc.form.ID] = true
	}
	if len(ids) != 9 {
		t.Fatalf("the forms carry %d ids, want one per operation: %v", len(ids), ids)
	}
}

func TestCommandLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{name: "nothing at all", want: ""},
		{
			name: "a command of plain words",
			argv: []string{"git", "restore", "--staged", "--", "internal/tui/gitform.go"},
			want: "git restore --staged -- internal/tui/gitform.go",
		},
		{
			name: "a path holding a space",
			argv: []string{"git", "restore", "--", "docs/old guide.md"},
			want: `git restore -- 'docs/old guide.md'`,
		},
		{
			name: "a message holding a quote",
			argv: []string{"git", "commit", "-m", "don't look back"},
			want: `git commit -m 'don'\''t look back'`,
		},
		{
			name: "an argument of nothing",
			argv: []string{"git", "commit", "-m", ""},
			want: "git commit -m ''",
		},
		{
			name: "what a shell would read as more than an argument",
			argv: []string{"git", "log", "a;rm -rf /", "$HOME", "`id`", "a|b", "*"},
			want: `git log 'a;rm -rf /' '$HOME' '` + "`id`" + `' 'a|b' '*'`,
		},
		{
			name: "an option a command of the workstation really carries",
			argv: []string{"git", "push", "--force-with-lease=main:a1b2c3d", "--", "origin", "main"},
			want: "git push --force-with-lease=main:a1b2c3d -- origin main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := commandLine(tc.argv); got != tc.want {
				t.Fatalf("commandLine(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

// TestGitFormFitsTheFrame proves the box gives up its text rather than its
// last lines: whatever the frame, the field and the keys are on screen.
func TestGitFormFitsTheFrame(t *testing.T) {
	t.Parallel()
	forms := []GitForm{
		ResetHardForm("feat/git-workstation", "a1b2c3d", 4, []string{"git", "reset", "--hard", "--", "a1b2c3d"}),
		RemoveWorktreeForm("review", "/home/dev/src/acme/.claude/worktrees/review", true,
			[]string{"git", "worktree", "remove", "--force", "--", "/home/dev/src/acme/.claude/worktrees/review"}),
		DiscardFileForm("internal/tui/gitform.go", []string{"git", "restore", "--", "internal/tui/gitform.go"}),
	}
	for _, f := range forms {
		for _, size := range []struct{ w, h int }{{34, 16}, {40, 12}, {64, 10}, {30, 8}, {80, 24}} {
			m := NewGitForm(GitFormOptions{Styles: goldenStyles(t), Width: size.w, Height: size.h})
			m.Ask(f)
			box := m.Box(size.w, size.h)
			if len(box) > size.h {
				t.Fatalf("%s drew a box of %d lines in a frame of %d:\n%s", f.ID, len(box), size.h,
					ansi.Strip(strings.Join(box, "\n")))
			}
			frame := formFrame(m)
			want := " cancel"
			if f.Ask == AskName {
				want = "to go on"
			}
			if !strings.Contains(frame, want) {
				t.Fatalf("a %dx%d frame of %s lacks %q:\n%s", size.w, size.h, f.ID, want, frame)
			}
		}
	}
}

func TestGitFormResizes(t *testing.T) {
	t.Parallel()
	m := newTestForm(t, ResetHardForm("feat/git-workstation", "a1b2c3d", 4,
		[]string{"git", "reset", "--hard", "--", "a1b2c3d"}))
	for _, size := range []struct{ w, h int }{{20, 6}, {120, 40}, {1, 1}, {64, 22}, {5, 3}} {
		m.Update(resize(size.w, size.h))
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size.h {
			t.Fatalf("a %dx%d form drew %d lines", size.w, size.h, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size.w {
				t.Fatalf("a %dx%d form drew line %d %d cells wide", size.w, size.h, i+1, w)
			}
		}
	}
}

func TestGitFormASCII(t *testing.T) {
	t.Parallel()
	m := NewGitForm(GitFormOptions{
		Styles: testStyles(t, "ansi", theme.Depth16, "ascii"), Width: 64, Height: 22,
	})
	m.Ask(ResetHardForm("main", "a1b2c3d", 2, []string{"git", "reset", "--hard", "--", "a1b2c3d"}))
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"+-", "| ", "x ", "type main to go on"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the ascii frame lacks %q:\n%s", want, frame)
		}
	}
	if strings.ContainsFunc(frame, func(r rune) bool { return r > 0x7e }) {
		t.Errorf("the ascii frame draws glyphs that are not ascii:\n%s", frame)
	}
}

// TestGitFormDrawsNothingItWasGiven proves a branch, a path or a message out
// of a repository is drawn as text and nothing else, however long it is and
// whatever it carries.
func TestGitFormDrawsNothingItWasGiven(t *testing.T) {
	t.Parallel()
	nasty := "a\x1b[31mname\rwith\x07everything"
	forms := []GitForm{
		DiscardFileForm(nasty+"/"+strings.Repeat("deep/", 40)+"file.go", []string{"git", "restore", "--", nasty}),
		DeleteBranchForm(nasty, false, []string{"git", "branch", "-D", "--", nasty}),
		ForcePushForm(nasty, strings.Repeat("long-", 40), false, []string{"git", "push", "--force", "--", nasty}),
		TextForm("commit", nasty, nasty, nasty, true, nil),
	}
	for _, f := range forms {
		m := NewGitForm(GitFormOptions{Styles: goldenStyles(t), Width: 48, Height: 24})
		m.Ask(f)
		formKeys(m, nasty)
		content := m.View().Content
		for i, l := range strings.Split(content, "\n") {
			if strings.ContainsAny(ansi.Strip(l), "\x1b\a\r\t") {
				t.Fatalf("line %d of %s carries control characters: %q", i+1, f.ID, l)
			}
			if w := ansi.StringWidth(l); w > 48 {
				t.Fatalf("line %d of %s is %d cells wide, over the frame", i+1, f.ID, w)
			}
		}
		// The styles here are written as 24-bit colors, so the 16-color red
		// of the data would be the one sequence the frame does not set itself.
		if strings.Contains(content, "\x1b[31m") {
			t.Fatalf("an escape sequence reached the screen of %s:\n%q", f.ID, content)
		}
	}
}

func TestBox(t *testing.T) {
	t.Parallel()
	s := goldenStyles(t)
	cases := []struct {
		name  string
		width int
		title string
		lines []string
		want  []string
	}{
		{
			name: "a box of one line", width: 12, title: "Title", lines: []string{"body"},
			want: []string{"┌─ Title ───┐", "│ body     │", "└──────────┘"},
		},
		{
			name: "a title longer than the box", width: 10, title: "a very long title", lines: nil,
		},
		{name: "a box with no title", width: 8, title: "", lines: []string{"a"}},
		{name: "a box too narrow to draw", width: 3, title: "T", lines: []string{"a"}},
		{name: "a box of no width at all", width: 0, title: "T", lines: []string{"a"}},
		{name: "a line longer than the box", width: 10, title: "T", lines: []string{strings.Repeat("x", 40)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := s.box(tc.width, tc.title, tc.lines)
			if tc.width < 4 {
				if got != nil {
					t.Fatalf("box(%d) = %q, want nothing", tc.width, got)
				}
				return
			}
			if len(got) != len(tc.lines)+2 {
				t.Fatalf("box drew %d lines, want the content between two edges", len(got))
			}
			for i, l := range got {
				if w := ansi.StringWidth(l); w != tc.width {
					t.Fatalf("line %d is %d cells wide, want %d: %q", i+1, w, tc.width, ansi.Strip(l))
				}
			}
		})
	}
}
