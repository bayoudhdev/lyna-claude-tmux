package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// editorStatusEnv names the file the pane's shell writes the editor's exit
// status to. tmux has a format for that status, but it is empty on some
// supported versions (3.4 for Neovim), so the status comes from the parent
// of the shell that became the editor.
const editorStatusEnv = "LYNA_TMUX_TEST_EXIT"

// editorWrapper hands the TmuxShellCommand string to the shell under test
// with -c, exactly as tmux hands a new-window body to its default-shell, and
// records the exit status the shell ends with: it replaces itself with the
// editor, so that status is the editor's. The file appears with its final
// content, so a reader never sees a partial write.
const editorWrapper = `f=$` + editorStatusEnv + `
"$1" -c "$2"
s=$?
printf '%s' "$s" > "$f.tmp" && mv "$f.tmp" "$f"
exit "$s"
`

// TestTmuxReviewWindow opens the review the way tmux runs it: the
// TmuxShellCommand string is the body of new-window on an isolated server,
// run by the shell under test as its -c argument, in a real terminal.
func TestTmuxReviewWindow(t *testing.T) {
	requireNvim(t)
	tmuxtest.Require(t)
	// Words each shell, or tmux, would otherwise interpret.
	hostile := []string{
		`q"uote`, `back\slash`, `trailing\`, "%self", "it's", "$HOME", "`id`",
		"#{pane_id}", "a;b", "~root", "!touch pwned", "dir with spaces/日本.go",
	}
	cases := []struct {
		name       string
		shell      string
		req        domain.Request
		noPlugin   bool
		wantStatus string
		// screen must all be visible before a key closes the editor.
		screen []string
	}{
		{name: "posix default-shell", shell: "sh", req: domain.Request{Layout: domain.LayoutInline, Paths: hostile}, wantStatus: "0"},
		{name: "fish default-shell", shell: "fish", req: domain.Request{Layout: domain.LayoutSideBySide, Paths: hostile}, wantStatus: "0"},
		{name: "revision range under fish", shell: "fish", req: domain.Request{Mode: domain.ModeRevision, Revisions: []string{"main...HEAD"}, Paths: []string{"%self"}}, wantStatus: "0"},
		{
			name:       "missing plugin keeps the reason on screen until a key",
			shell:      "sh",
			noPlugin:   true,
			wantStatus: "3",
			screen:     []string{"lyna-tmux review: the :CodeDiff command is not available.", "lyna-tmux review install"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shell, err := exec.LookPath(tc.shell)
			if err != nil {
				t.Skipf("%s is not installed", tc.shell)
			}
			srv := tmuxtest.Start(t)
			ctx := tmuxtest.Context(t)
			s := newNvimSandbox(t)
			paths := reviewPaths(t, !tc.noPlugin)
			if _, err := Prepare(paths, domain.InitOptions{}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			out := t.TempDir()
			fargsFile := filepath.Join(out, "fargs.json")
			stateFile := filepath.Join(out, "state.json")
			exitFile := filepath.Join(out, "exit")

			command, err := TmuxShellCommand(tc.req, LaunchOptions{Paths: paths, Dir: dir, Environ: s.env})
			if err != nil {
				t.Fatal(err)
			}
			// The pane keeps its screen once the editor has exited, for the
			// log of a failed case.
			if _, err := srv.Client.Run(ctx, "set-option", "-wg", "remain-on-exit", "on"); err != nil {
				t.Fatalf("set-option remain-on-exit: %v", err)
			}
			// The pane gets the sandbox home and configuration, so neither the
			// shell nor the editor can read the developer's own.
			args := []string{"-d", "-P", "-F", "#{pane_id}", "-t", tmux.ExactSession("base"), "-c", tmux.FormatEscape(dir)}
			for _, kv := range append(slices.Clone(s.env), "LYNA_TMUX_TEST_FARGS="+fargsFile, "LYNA_TMUX_TEST_STATE="+stateFile, editorStatusEnv+"="+exitFile) {
				args = append(args, "-e", kv)
			}
			pane, err := srv.Client.Run(ctx, "new-window", append(args, "/bin/sh", "-c", editorWrapper, "sh", shell, command)...)
			if err != nil {
				t.Fatalf("new-window: %v", err)
			}
			pane = strings.TrimSpace(pane)
			capture := func() string {
				screen, _ := srv.Client.Run(ctx, "capture-pane", "-p", "-t", pane)
				return screen
			}
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("command: %s\nscreen:\n%s", command, capture())
				}
			})
			// exited reports the editor's status once it has ended; the file
			// appears only then.
			exited := func() (string, bool) {
				data, err := os.ReadFile(exitFile)
				return string(data), err == nil
			}

			if len(tc.screen) > 0 {
				tmuxtest.WaitFor(t, "the failure reason on screen", func() bool {
					screen := capture()
					for _, want := range tc.screen {
						if !strings.Contains(screen, want) {
							return false
						}
					}
					return true
				})
				if status, ok := exited(); ok {
					t.Fatalf("the editor exited %s before a key was pressed", status)
				}
				if _, err := srv.Client.Run(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
					t.Fatal(err)
				}
			}
			var status string
			tmuxtest.WaitFor(t, "the review window to exit", func() bool {
				var ok bool
				status, ok = exited()
				return ok
			})
			if status != tc.wantStatus {
				t.Fatalf("editor exit status = %s, want %s", status, tc.wantStatus)
			}

			if tc.noPlugin {
				if _, err := os.Stat(fargsFile); err == nil {
					t.Fatal(":CodeDiff ran although the plugin is missing")
				}
				return
			}
			var fargs []string
			readJSON(t, fargsFile, &fargs)
			want, _ := tc.req.Fargs()
			if !slices.Equal(fargs, want) {
				t.Fatalf(":CodeDiff received %q, want %q", fargs, want)
			}
			var st editorState
			readJSON(t, stateFile, &st)
			if resolved, _ := filepath.EvalSymlinks(dir); st.Cwd != dir && st.Cwd != resolved {
				t.Errorf("editor ran in %q, want %q", st.Cwd, dir)
			}
			if len(st.RuntimePath) == 0 || st.RuntimePath[0] != PluginDir(paths) {
				t.Errorf("runtimepath = %q, want the plugin directory first", st.RuntimePath)
			}
			for _, entry := range slices.Concat(st.RuntimePath, st.PackPath) {
				for _, root := range s.forbiddenRoots(t) {
					if strings.HasPrefix(entry, root) {
						t.Errorf("isolated editor path %q is under %q", entry, root)
					}
				}
			}
			if got := s.loadedMarkers(t); len(got) != 0 {
				t.Errorf("isolated editor loaded %v", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
				t.Error("a path argument ran a command")
			}
		})
	}
}
