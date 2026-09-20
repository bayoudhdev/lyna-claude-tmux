package doctor

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

func TestTmuxKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"alt+a", "M-a", true},
		{"meta+a", "M-a", true},
		{"opt+a", "M-a", true},
		{"option+A", "M-a", true},
		{"alt+shift+h", "M-H", true},
		{"shift+alt+h", "M-H", true},
		{"ctrl+b", "C-b", true},
		{"control+B", "C-b", true},
		{"ctrl+shift+b", "C-B", true},
		{`alt+\`, `M-\`, true},
		{"alt+-", "M--", true},
		{"alt+[", "M-[", true},
		{"alt+1", "M-1", true},
		{"alt+space", "M-Space", true},
		{"alt+shift+left", "M-S-Left", true},
		{"shift+meta+LEFT", "M-S-Left", true},
		{"escape", "Escape", true},
		{"esc", "Escape", true},
		{"return", "Enter", true},
		{"shift+tab", "S-Tab", true},
		{"pageup", "PageUp", true},
		{"pagedown", "PageDown", true},
		{"home", "Home", true},
		{"end", "End", true},
		{"backspace", "BSpace", true},
		{"alt+delete", "M-DC", true},
		{"up", "Up", true},
		{"f5", "F5", true},
		{"ctrl+F12", "C-F12", true},
		{"ctrl++", "C-+", true},
		{"+", "+", true},
		{" alt+z ", "M-z", true},
		{"cmd+k", "", false},
		{"super+k", "", false},
		{"win+k", "", false},
		{"hyper+k", "", false},
		{"wheelup", "", false},
		{"ctrl+", "", false},
		{"", "", false},
		{"alt+foo", "", false},
		{"f", "f", true},
		{"f123", "", false},
		{"a++", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := TmuxKey(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("TmuxKey(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseKeybindings(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []UserBinding
		wantErr bool
	}{
		{
			name: "documented example with an unbind",
			in: `{
  "$schema": "https://www.schemastore.org/claude-code-keybindings.json",
  "$docs": "https://code.claude.com/docs/en/keybindings",
  "bindings": [
    {"context": "Chat", "bindings": {"ctrl+e": "chat:externalEditor", "ctrl+u": null}}
  ]
}`,
			want: []UserBinding{{Context: "Chat", Keystroke: "ctrl+e", Action: "chat:externalEditor"}},
		},
		{
			name: "sorted by context then keystroke",
			in: `{"bindings": [
  {"context": "Global", "bindings": {"meta+z": "app:redraw", "alt+a": "app:toggleTodos"}},
  {"context": "Chat", "bindings": {"ctrl+x ctrl+k": null, "ctrl+g": "chat:externalEditor"}}
]}`,
			want: []UserBinding{
				{Context: "Chat", Keystroke: "ctrl+g", Action: "chat:externalEditor"},
				{Context: "Global", Keystroke: "alt+a", Action: "app:toggleTodos"},
				{Context: "Global", Keystroke: "meta+z", Action: "app:redraw"},
			},
		},
		{name: "empty object", in: `{}`},
		{name: "empty bindings", in: `{"bindings": []}`},
		{name: "invalid json", in: `{"bindings": [`, wantErr: true},
		{name: "non string action", in: `{"bindings": [{"context": "Chat", "bindings": {"alt+a": 3}}]}`, wantErr: true},
		{name: "bindings not an array", in: `{"bindings": {"alt+a": "x"}}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseKeybindings([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCollisions(t *testing.T) {
	workspace := keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"})
	noAlt := keys.Defaults(keys.Options{AltKeys: false, Prefix: "C-a"})
	ub := func(stroke string) UserBinding {
		return UserBinding{Context: "Chat", Keystroke: stroke, Action: "chat:x"}
	}
	cases := []struct {
		name      string
		bindings  []UserBinding
		workspace []keys.Binding
		prefix    string
		want      []Collision
	}{
		{
			name:      "alt key taken by the agents binding",
			bindings:  []UserBinding{ub("alt+a")},
			workspace: workspace, prefix: "C-b",
			want: []Collision{{Binding: ub("alt+a"), TmuxKey: "M-a", Taken: "lyna-tmux Agents"}},
		},
		{
			name:      "prefix key",
			bindings:  []UserBinding{ub("ctrl+b")},
			workspace: workspace, prefix: "C-b",
			want: []Collision{{Binding: ub("ctrl+b"), TmuxKey: "C-b", Taken: "the tmux prefix (press it twice to send it)"}},
		},
		{
			name:      "chord starting with a taken key",
			bindings:  []UserBinding{ub("alt+g  ctrl+s")},
			workspace: workspace, prefix: "C-b",
			want: []Collision{{Binding: ub("alt+g  ctrl+s"), TmuxKey: "M-g", Taken: "lyna-tmux Review changes"}},
		},
		{
			name:      "chord whose later key is taken is safe",
			bindings:  []UserBinding{ub("ctrl+x alt+a")},
			workspace: workspace, prefix: "C-b",
		},
		{
			name:      "resize binding with modifiers in another order",
			bindings:  []UserBinding{ub("shift+alt+left")},
			workspace: workspace, prefix: "C-b",
			want: []Collision{{Binding: ub("shift+alt+left"), TmuxKey: "M-S-Left", Taken: "lyna-tmux Resize pane left"}},
		},
		{
			name:      "prefix table keys never collide",
			bindings:  []UserBinding{ub("a"), ub("g"), ub("space")},
			workspace: workspace, prefix: "C-b",
		},
		{
			name:      "alt keys disabled and custom prefix",
			bindings:  []UserBinding{ub("alt+a"), ub("ctrl+a"), ub("ctrl+b")},
			workspace: noAlt, prefix: "C-a",
			want: []Collision{{Binding: ub("ctrl+a"), TmuxKey: "C-a", Taken: "the tmux prefix (press it twice to send it)"}},
		},
		{
			name:      "unmappable and blank keystrokes are ignored",
			bindings:  []UserBinding{ub("cmd+a"), ub("   "), ub("wheelup")},
			workspace: workspace, prefix: "C-b",
		},
		{
			name:      "empty prefix",
			bindings:  []UserBinding{ub("ctrl+b")},
			workspace: nil, prefix: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Collisions(tc.bindings, tc.workspace, tc.prefix)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestEveryRootBindingIsReachable pins the join between the workspace key
// table and the keystroke converter: a Claude keystroke exists for every root
// binding, so a user binding on any of them is reported.
func TestEveryRootBindingIsReachable(t *testing.T) {
	strokes := map[string]string{}
	for _, name := range []string{"a", "c", "e", "g", "n", "s", "x", "z", "1", "2", "3", "4", "5", "6", "7", "8", "9", "[", "]", `\`, "-", "space"} {
		k, ok := TmuxKey("alt+" + name)
		if !ok {
			t.Fatalf("TmuxKey(alt+%s) failed", name)
		}
		strokes[k] = "alt+" + name
	}
	for _, name := range []string{"left", "right", "up", "down", "a", "g"} {
		k, ok := TmuxKey("alt+shift+" + name)
		if !ok {
			t.Fatalf("TmuxKey(alt+shift+%s) failed", name)
		}
		strokes[k] = "alt+shift+" + name
	}
	for _, b := range keys.Defaults(keys.Options{AltKeys: true}) {
		if b.Table != keys.TableRoot {
			continue
		}
		stroke, ok := strokes[keys.Normalize(b.Key)]
		if !ok {
			t.Fatalf("no Claude keystroke maps to root binding %s (%s)", b.Key, b.Help)
		}
		if got := Collisions([]UserBinding{{Keystroke: stroke}}, keys.Defaults(keys.Options{AltKeys: true}), "C-b"); len(got) != 1 {
			t.Fatalf("keystroke %s for %s produced %d collisions", stroke, b.Key, len(got))
		}
	}
}

func TestCheckKeybindings(t *testing.T) {
	path := "/home/u/.claude/keybindings.json"
	runCheckCases(t, checkKeybindings, []checkCase{
		{
			name: "unknown claude home",
			edit: func(d *Deps) { d.ClaudeHome = "" },
			want: []Result{{ID: "keybindings", Title: "Claude keybindings", Status: StatusSkip, Detail: "the Claude Code configuration directory is unknown"}},
		},
		{
			name: "no file",
			want: []Result{{ID: "keybindings", Title: "Claude keybindings", Status: StatusOK, Detail: "no " + path + ", so Claude Code uses its default keys"}},
		},
		{
			name: "unreadable",
			sys:  fakeSystem{errs: map[string]error{path: errors.New("permission denied")}},
			want: []Result{{
				ID: "keybindings", Title: "Claude keybindings", Status: StatusWarn,
				Detail: "cannot read " + path + ": permission denied", Fix: "run /keybindings in Claude Code to open and repair the file",
			}},
		},
		{
			name: "too large",
			sys:  fakeSystem{files: map[string]string{path: `{"bindings": []}` + strings.Repeat(" ", maxKeybindingsBytes)}},
			want: []Result{{
				ID: "keybindings", Title: "Claude keybindings", Status: StatusWarn,
				Detail: "cannot read " + path + ": fsx: input exceeds size limit", Fix: "run /keybindings in Claude Code to open and repair the file",
			}},
		},
		{
			name: "invalid json",
			sys:  fakeSystem{files: map[string]string{path: `{`}},
			want: []Result{{
				ID: "keybindings", Title: "Claude keybindings", Status: StatusWarn,
				Detail: "cannot parse " + path + ": unexpected end of JSON input", Fix: "run /keybindings in Claude Code to open and repair the file",
			}},
		},
		{
			name: "no collisions",
			sys:  fakeSystem{files: map[string]string{path: `{"bindings": [{"context": "Chat", "bindings": {"ctrl+e": "chat:externalEditor", "alt+p": null}}]}`}},
			want: []Result{{ID: "keybindings", Title: "Claude keybindings", Status: StatusOK, Detail: "1 custom bindings in " + path + ", none taken by tmux"}},
		},
		{
			name: "collisions",
			sys: fakeSystem{files: map[string]string{path: `{"bindings": [
  {"context": "Global", "bindings": {"alt+a": "app:toggleTodos"}},
  {"context": "Task", "bindings": {"ctrl+b": "task:background"}}
]}`}},
			want: []Result{{
				ID: "keybindings", Title: "Claude keybindings", Status: StatusWarn,
				Detail: "alt+a (Global, app:toggleTodos) never reaches Claude: M-a is lyna-tmux Agents\n" +
					"ctrl+b (Task, task:background) never reaches Claude: C-b is the tmux prefix (press it twice to send it)",
				Fix: "edit " + path + " to use other keys, or set ui.alt_keys = false (and workspace.prefix) in config.toml",
			}},
		},
		{
			name: "alt collisions vanish with alt keys off",
			sys:  fakeSystem{files: map[string]string{path: `{"bindings": [{"context": "Global", "bindings": {"alt+a": "app:toggleTodos"}}]}`}},
			edit: func(d *Deps) { d.AltKeys = false },
			want: []Result{{ID: "keybindings", Title: "Claude keybindings", Status: StatusOK, Detail: "1 custom bindings in " + path + ", none taken by tmux"}},
		},
	})
}
