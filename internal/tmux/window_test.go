package tmux

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

func TestValidPaneID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"%0", true},
		{"%123", true},
		{"%", false},
		{"", false},
		{"%-1", false},
		{"%+1", false},
		{"%1a", false},
		{"@1", false},
		{"=api:", false},
		{"% 1", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := ValidPaneID(tc.in); got != tc.want {
				t.Fatalf("ValidPaneID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDescribePaneParse(t *testing.T) {
	row := func(fields ...string) string { return strings.Join(fields, fieldSep) + "\n" }
	cases := []struct {
		name    string
		target  string
		result  Result
		want    PaneContext
		wantErr string
	}{
		{
			name: "pane", target: "%3",
			result: ok(row("%3", "api", "@2", "/src/it's #[x]", "/src", "strict", "container", "200", "50")),
			want: PaneContext{
				ID: "%3", Session: "api", WindowID: "@2", Path: "/src/it's #[x]", Project: "/src",
				Sandbox: "strict", Isolation: "container", WindowWidth: 200, WindowHeight: 50,
			},
		},
		{
			name: "session that is not a workspace", target: "%3",
			result: ok(row("%3", "plain", "@1", "/src", "", "", "", "80", "24")),
			want:   PaneContext{ID: "%3", Session: "plain", WindowID: "@1", Path: "/src", WindowWidth: 80, WindowHeight: 24},
		},
		{name: "empty target", target: "", wantErr: "no target"},
		{name: "missing pane", target: "%9", result: ok(row("", "", "", "", "", "", "", "", "")), wantErr: "target not found: %9"},
		{name: "no server", target: "%9", result: fail("", "no server running on /tmp/x"), wantErr: "no server running"},
		{name: "short row", target: "%3", result: ok(row("%3", "api")), wantErr: "unexpected pane description"},
		{name: "bad pane id", target: "%3", result: ok(row("x", "api", "@2", "/", "", "", "", "1", "1")), wantErr: "unexpected pane description"},
		{name: "bad window id", target: "%3", result: ok(row("%3", "api", "2", "/", "", "", "", "1", "1")), wantErr: "unexpected pane description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: []Result{tc.result}}
			got, err := New(Options{Executor: rec}).DescribePane(context.Background(), tc.target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("DescribePane = %+v, want %+v", got, tc.want)
			}
			if call := rec.calls[0]; call[0] != "display-message" || call[2] != "-t" || call[3] != tc.target {
				t.Fatalf("call %q", call)
			}
		})
	}
}

func TestFindWindow(t *testing.T) {
	list := func(rows ...string) Result { return ok(strings.Join(rows, "\n") + "\n") }
	row := func(id, name string) string { return id + fieldSep + name }
	cases := []struct {
		name      string
		session   string
		window    string
		result    Result
		wantID    string
		wantFound bool
		wantErr   string
	}{
		{
			name: "found", session: "api", window: "fix-login",
			result: list(row("@1", "claude"), row("@2", "fix-login"), row("@3", "resume")),
			wantID: "@2", wantFound: true,
		},
		{name: "first window", session: "api", window: "claude", result: list(row("@1", "claude")), wantID: "@1", wantFound: true},
		{name: "no window of that name", session: "api", window: "spike", result: list(row("@1", "claude"))},
		{name: "prefix is not a match", session: "api", window: "fix", result: list(row("@1", "fix-login"))},
		{name: "suffix is not a match", session: "api", window: "login", result: list(row("@1", "fix-login"))},
		{name: "name with spaces", session: "api", window: "two words", result: list(row("@7", "two words")), wantID: "@7", wantFound: true},
		{name: "no windows", session: "api", window: "claude", result: ok("")},
		{name: "row without a separator is skipped", session: "api", window: "claude", result: list("@1 claude")},
		{name: "row without a window id is skipped", session: "api", window: "claude", result: list(row("1", "claude"))},
		{name: "missing session", session: "nope", window: "claude", result: fail("", "can't find session: nope"), wantErr: "can't find session"},
		{name: "invalid session name", session: "a:b", window: "claude", wantErr: "invalid session name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: []Result{tc.result}}
			id, found, err := New(Options{Executor: rec}).FindWindow(context.Background(), tc.session, tc.window)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id != tc.wantID || found != tc.wantFound {
				t.Fatalf("FindWindow = %q, %v; want %q, %v", id, found, tc.wantID, tc.wantFound)
			}
			want := []string{"list-windows", "-t", ExactSession(tc.session), "-F", "#{window_id}" + fieldSep + "#{window_name}"}
			if !reflect.DeepEqual(rec.calls[0], want) {
				t.Fatalf("call %q, want %q", rec.calls[0], want)
			}
		})
	}
}

func TestSplitPaneCommands(t *testing.T) {
	proc := PaneProcess{Argv: []string{"/bin/claude", "--name=api"}, Env: []string{"A=1"}, Options: map[string]string{OptSettings: "/s.json"}}
	cases := []struct {
		name string
		spec SplitSpec
		want [][]string
	}{
		{
			name: "claude to the right",
			spec: SplitSpec{Pane: "%3", Window: "@2", Dir: "/src/#{x}", Role: layout.RoleClaude, Proc: proc},
			want: [][]string{{
				"split-window", "-t", "%3", "-h", "-c", "/src/##{x}", "-P", "-F", "#{pane_id}", "-e", "A=1", "--", "/bin/claude", "--name=api",
				";", "set-option", "-p", "-t", "@2", OptRole, "claude",
				";", "set-option", "-p", "-t", "@2", "remain-on-exit", "failed",
				";", "set-option", "-p", "-t", "@2", OptSettings, "/s.json",
			}},
		},
		{
			// A workstation whose program failed keeps its pane, so the reason
			// stays readable; a shell pane is the user's and keeps tmux's own
			// behavior.
			name: "the git workstation to the right",
			spec: SplitSpec{Pane: "%3", Window: "@2", Dir: "/src", Role: layout.RoleGit, Proc: PaneProcess{Argv: []string{"/bin/lmux", "git", "--dir", "/src"}}},
			want: [][]string{{
				"split-window", "-t", "%3", "-h", "-c", "/src", "-P", "-F", "#{pane_id}", "--", "/bin/lmux", "git", "--dir", "/src",
				";", "set-option", "-p", "-t", "@2", OptRole, "git",
				";", "set-option", "-p", "-t", "@2", "remain-on-exit", "failed",
			}},
		},
		{
			name: "shell below",
			spec: SplitSpec{Pane: "%3", Window: "@2", Down: true, Dir: "/src", Role: layout.RoleShell},
			want: [][]string{{
				"split-window", "-t", "%3", "-v", "-c", "/src", "-P", "-F", "#{pane_id}",
				";", "set-option", "-p", "-t", "@2", OptRole, "shell",
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: []Result{ok("%7\n")}}
			id, err := New(Options{Executor: rec}).SplitPane(context.Background(), tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if id != "%7" || !reflect.DeepEqual(rec.calls, tc.want) {
				t.Fatalf("id %q calls\n%q\nwant\n%q", id, rec.calls, tc.want)
			}
		})
	}
}

func TestSplitPaneFailures(t *testing.T) {
	good := SplitSpec{Pane: "%3", Window: "@2", Dir: "/src", Role: layout.RoleShell}
	with := func(f func(*SplitSpec)) SplitSpec { s := good; f(&s); return s }
	cases := []struct {
		name     string
		spec     SplitSpec
		results  []Result
		wantErr  string
		wantKill bool
	}{
		{name: "pane name refused", spec: with(func(s *SplitSpec) { s.Pane = "=api:" }), wantErr: "not a pane id"},
		{name: "window name refused", spec: with(func(s *SplitSpec) { s.Window = "main" }), wantErr: "not a window id"},
		{name: "relative directory", spec: with(func(s *SplitSpec) { s.Dir = "src" }), wantErr: "not absolute"},
		{name: "unknown role", spec: with(func(s *SplitSpec) { s.Role = "scratch" }), wantErr: "unknown role"},
		{name: "invalid process", spec: with(func(s *SplitSpec) { s.Proc = PaneProcess{Env: []string{"no-equals"}} }), wantErr: "KEY=VALUE"},
		{name: "split refused", spec: good, results: []Result{fail("", "no space for new pane")}, wantErr: "no space"},
		{name: "unexpected output", spec: good, results: []Result{ok("garbage\n")}, wantErr: "unexpected output"},
		{name: "tagging failure removes the pane", spec: good, results: []Result{fail("%8\n", "invalid option"), ok("")}, wantErr: "invalid option", wantKill: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scripted{results: tc.results}
			_, err := New(Options{Executor: rec}).SplitPane(context.Background(), tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			killed := len(rec.calls) > 0 && reflect.DeepEqual(rec.calls[len(rec.calls)-1], []string{"kill-pane", "-t", "%8"})
			if killed != tc.wantKill {
				t.Fatalf("calls %q, want kill %v", rec.calls, tc.wantKill)
			}
		})
	}
}
