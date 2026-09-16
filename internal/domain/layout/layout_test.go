package layout

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name          string
		layout        string
		width, height int
		want          string
	}{
		{name: "explicit kept", layout: Quad, width: 10, height: 10, want: Quad},
		{name: "auto unknown size", layout: Auto, want: Duo},
		{name: "auto wide and tall", layout: Auto, width: 240, height: 60, want: Trio},
		{name: "auto exact trio threshold", layout: Auto, width: AutoTrioWidth, height: AutoTrioHeight, want: Trio},
		{name: "auto wide but short", layout: Auto, width: 240, height: 30, want: Duo},
		{name: "auto medium", layout: Auto, width: 150, height: 50, want: Duo},
		{name: "auto exact duo threshold", layout: Auto, width: AutoDuoWidth, height: 20, want: Duo},
		{name: "auto narrow", layout: Auto, width: 90, height: 50, want: Solo},
		{name: "auto with only a width", layout: Auto, width: 240, want: Duo},
		{name: "auto with only a height", layout: Auto, height: 60, want: Duo},
		{name: "auto with a negative size", layout: Auto, width: -1, height: -1, want: Duo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.layout, tc.width, tc.height); got != tc.want {
				t.Fatalf("Resolve(%q, %d, %d) = %q, want %q", tc.layout, tc.width, tc.height, got, tc.want)
			}
		})
	}
}

func TestBuiltin(t *testing.T) {
	cases := []struct {
		name     string
		layout   string
		opts     Options
		wantName string
		want     []Pane
	}{
		{
			name: "solo", layout: Solo, opts: Options{SplitRatio: 62}, wantName: Solo,
			want: []Pane{{Role: RoleClaude}},
		},
		{
			name: "duo uses ratio", layout: Duo, opts: Options{SplitRatio: 70}, wantName: Duo,
			want: []Pane{{Role: RoleClaude}, {Role: RoleShell, Split: SplitRight, Size: 30}},
		},
		{
			name: "duo out of range ratio falls back", layout: Duo, opts: Options{SplitRatio: 5}, wantName: Duo,
			want: []Pane{{Role: RoleClaude}, {Role: RoleShell, Split: SplitRight, Size: 38}},
		},
		{
			name: "trio", layout: Trio, opts: Options{SplitRatio: 62}, wantName: Trio,
			want: []Pane{
				{Role: RoleClaude},
				{Role: RoleShell, Split: SplitRight, Size: 38},
				{Role: RoleChanges, Split: SplitDown, Size: 60, Parent: 1},
			},
		},
		{
			name: "quad worktrees named after session", layout: Quad, opts: Options{Session: "api"}, wantName: Quad,
			want: []Pane{
				{Role: RoleClaude, Worktree: "api-1"},
				{Role: RoleClaude, Split: SplitRight, Size: 50, Worktree: "api-2"},
				{Role: RoleClaude, Split: SplitDown, Size: 50, Worktree: "api-3"},
				{Role: RoleClaude, Split: SplitDown, Size: 50, Parent: 1, Worktree: "api-4"},
			},
		},
		{
			name: "quad without session", layout: Quad, wantName: Quad,
			want: []Pane{
				{Role: RoleClaude, Worktree: "agent-1"},
				{Role: RoleClaude, Split: SplitRight, Size: 50, Worktree: "agent-2"},
				{Role: RoleClaude, Split: SplitDown, Size: 50, Worktree: "agent-3"},
				{Role: RoleClaude, Split: SplitDown, Size: 50, Parent: 1, Worktree: "agent-4"},
			},
		},
		{
			name: "review", layout: Review, wantName: Review,
			want: []Pane{{Role: RoleClaude}, {Role: RoleReview, Split: SplitRight, Size: 50}},
		},
		{
			name: "auto resolves by size", layout: Auto, opts: Options{SplitRatio: 62, Width: 80, Height: 24}, wantName: Solo,
			want: []Pane{{Role: RoleClaude}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Builtin(tc.layout, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if !reflect.DeepEqual(got.Panes, tc.want) {
				t.Errorf("Panes = %+v\nwant %+v", got.Panes, tc.want)
			}
			if got.Focus != 0 {
				t.Errorf("Focus = %d, want 0", got.Focus)
			}
		})
	}
}

func TestBuiltinUnknown(t *testing.T) {
	if _, err := Builtin("hexa", Options{}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Builtin(hexa) err = %v, want ErrUnknown", err)
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if names[len(names)-1] != Auto {
		t.Errorf("auto must be listed last: %v", names)
	}
	for _, n := range names {
		if !IsBuiltin(n) {
			t.Errorf("IsBuiltin(%q) = false", n)
		}
		if n == Auto {
			continue
		}
		if _, err := Builtin(n, Options{SplitRatio: 62, Session: "s"}); err != nil {
			t.Errorf("Builtin(%q): %v", n, err)
		}
	}
	if IsBuiltin("custom") {
		t.Error("IsBuiltin(custom) = true")
	}
}

// TestRoles checks that Roles lists exactly the roles Validate accepts, so
// configuration validation built on it cannot drift from plans.
func TestRoles(t *testing.T) {
	roles := Roles()
	if roles[0] != string(RoleClaude) {
		t.Fatalf("claude must be listed first: %v", roles)
	}
	for _, r := range append(roles, "editor", "") {
		t.Run("role "+r, func(t *testing.T) {
			pane := Pane{Role: Role(r), Split: SplitRight, Size: 50}
			if r == string(RoleCommand) {
				pane.Command = "make"
			}
			err := Plan{Name: "x", Panes: []Pane{{Role: RoleClaude}, pane}}.Validate()
			if listed := slices.Contains(roles, r); (err == nil) != listed {
				t.Fatalf("role %q: listed %v, Validate err %v", r, listed, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	claude := Pane{Role: RoleClaude}
	cases := []struct {
		name    string
		plan    Plan
		wantErr string
	}{
		{name: "valid", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleCommand, Command: "htop", Split: SplitDown, Size: 30}}}},
		{name: "empty", plan: Plan{Name: "x"}, wantErr: "needs 1 to 9 panes"},
		{name: "too many", plan: Plan{Name: "x", Panes: make([]Pane, 10)}, wantErr: "needs 1 to 9 panes"},
		{name: "no claude", plan: Plan{Name: "x", Panes: []Pane{{Role: RoleShell}}}, wantErr: "at least one claude pane"},
		{name: "unknown role", plan: Plan{Name: "x", Panes: []Pane{{Role: "editor"}}}, wantErr: "unknown role"},
		{name: "command without command", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleCommand, Split: SplitDown, Size: 30}}}, wantErr: "needs a command"},
		{name: "command on shell", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Command: "ls", Split: SplitDown, Size: 30}}}, wantErr: "only command panes"},
		{name: "first pane split", plan: Plan{Name: "x", Panes: []Pane{{Role: RoleClaude, Split: SplitDown}}}, wantErr: "takes no split"},
		{name: "bad split", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: "left", Size: 30}}}, wantErr: "right or down"},
		{name: "size too small", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: SplitDown, Size: 5}}}, wantErr: "10 to 90"},
		{name: "size too big", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: SplitDown, Size: 95}}}, wantErr: "10 to 90"},
		{name: "parent forward", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: SplitDown, Size: 30, Parent: 1}}}, wantErr: "earlier pane"},
		{name: "parent negative", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: SplitDown, Size: 30, Parent: -1}}}, wantErr: "earlier pane"},
		{name: "worktree on shell", plan: Plan{Name: "x", Panes: []Pane{claude, {Role: RoleShell, Split: SplitDown, Size: 30, Worktree: "w"}}}, wantErr: "only claude panes"},
		{name: "bad worktree", plan: Plan{Name: "x", Panes: []Pane{{Role: RoleClaude, Worktree: "../x"}}}, wantErr: "worktree name"},
		{name: "focus out of range", plan: Plan{Name: "x", Panes: []Pane{claude}, Focus: 1}, wantErr: "focus 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.plan.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateWorktree(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{name: "simple", in: "feature-login", ok: true},
		{name: "dots and underscores", in: "v1.2_fix", ok: true},
		{name: "empty", in: ""},
		{name: "too long", in: strings.Repeat("a", 65)},
		{name: "max length", in: strings.Repeat("a", 64), ok: true},
		{name: "leading dash", in: "-rf"},
		{name: "leading dot", in: ".hidden"},
		{name: "double dot", in: "a..b"},
		{name: "slash", in: "a/b"},
		{name: "space", in: "a b"},
		{name: "unicode", in: "café"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWorktree(tc.in); (err == nil) != tc.ok {
				t.Fatalf("ValidateWorktree(%q) = %v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}

func FuzzValidateWorktree(f *testing.F) {
	for _, s := range []string{"ok", "-x", "..", "a/b", "a\x00b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidateWorktree(s) != nil {
			return
		}
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") || strings.Contains(s, "..") || strings.ContainsAny(s, "/\\ \x00\n") {
			t.Fatalf("accepted unsafe worktree name %q", s)
		}
	})
}

func TestCustom(t *testing.T) {
	cases := []struct {
		name    string
		panes   []CustomPane
		want    Plan
		wantErr string
	}{
		{
			name: "defaults parent and size",
			panes: []CustomPane{
				{Role: "shell"},
				{Role: "claude", Split: "right", Worktree: true},
				{Role: "command", Split: "down", Size: 30, Parent: 1, Command: "htop"},
			},
			want: Plan{Name: "dev", Focus: 1, Panes: []Pane{
				{Role: RoleShell},
				{Role: RoleClaude, Split: SplitRight, Size: 50, Parent: 0, Worktree: "web-2"},
				{Role: RoleCommand, Split: SplitDown, Size: 30, Parent: 0, Command: "htop"},
			}},
		},
		{
			name:    "invalid custom layout",
			panes:   []CustomPane{{Role: "shell"}},
			wantErr: "at least one claude pane",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Custom("dev", "web", tc.panes)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Custom() err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Custom() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
