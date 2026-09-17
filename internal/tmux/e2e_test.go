package tmux_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// workspace is a generated-configuration server with a real attached client,
// driven through keys and mouse events exactly as a user would.
type workspace struct {
	*tmuxtest.Nested
	env    tmux.Env
	dir    string // project directory, symlinks resolved as tmux reports it
	root   string // temporary root holding everything the test creates
	marker string // file the fake lyna-tmux appends "cwd|args" lines to
}

const (
	clientCols = 150
	clientRows = 40
)

// Menus are recognized by an item label only they draw; titles such as
// "claude" also appear in pane borders.
const (
	keysMenu    = "Focus previous pane"
	paneMenu    = "Swap with next"
	claudeMenu  = "Deep research..."
	sessionMenu = "Reload configuration"
	windowMenu  = "Rename window"
)

// startWorkspace creates a project directory named dirName and attaches a
// client to a server running the generated configuration. The lyna-tmux
// binary the configuration calls is a script recording its working directory
// and arguments.
func startWorkspace(t *testing.T, dirName string) *workspace {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, dirName)
	binDir := filepath.Join(root, "bin dir")
	for _, d := range []string{dir, binDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(root, "calls")
	bin := filepath.Join(binDir, "lyna-tmux")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> " + tmux.ShellQuote(marker) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	env := tmux.Env{
		Bin:         bin,
		ConfPath:    filepath.Join(root, "tmux.conf"),
		PopupWidth:  "60%",
		PopupHeight: "60%",
		Bindings:    keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"}),
	}
	p, err := theme.Get("lyna")
	if err != nil {
		t.Fatal(err)
	}
	icons, err := theme.GetIcons("unicode")
	if err != nil {
		t.Fatal(err)
	}
	conf := tmux.GenerateConf(tmux.ConfOptions{
		Version:        installedVersion(t),
		Look:           tmux.Look{Palette: p, Depth: theme.DepthTrue, Icons: icons},
		Env:            env,
		Prefix:         "C-b",
		Mouse:          true,
		StatusPosition: "bottom",
		HistoryLimit:   1000,
		Shell:          "/bin/sh",
	})
	if err := os.WriteFile(env.ConfPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	n := tmuxtest.StartNested(t, env.ConfPath, dir, clientCols, clientRows)
	w := &workspace{Nested: n, env: env, dir: dir, root: root, marker: marker}
	// The first pane is the Claude pane of a real workspace.
	w.run(t, "set-option", "-p", "-t", "=main:1.1", tmux.OptRole, tmux.RoleClaude)
	return w
}

func (w *workspace) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := w.Inner.Client.Run(tmuxtest.Context(t), args[0], args[1:]...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimRight(out, "\n")
}

// active expands a format against the client's active pane.
func (w *workspace) active(t *testing.T, format string) string {
	t.Helper()
	return w.run(t, "display-message", "-p", "-t", "=main:", format)
}

func (w *workspace) waitActive(t *testing.T, format, want string) {
	t.Helper()
	var got string
	tmuxtest.WaitFor(t, format+" = "+strconv.Quote(want), func() bool {
		got = w.active(t, format)
		if got != want {
			t.Logf("%s = %q", format, got)
		}
		return got == want
	})
}

// calls returns the recorded lyna-tmux invocations. A line still being
// written (no trailing newline yet) is not returned.
func (w *workspace) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(w.marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	return lines[:len(lines)-1]
}

// waitCall waits for invocation number n (1-based) and checks it.
func (w *workspace) waitCall(t *testing.T, n int, want string) {
	t.Helper()
	tmuxtest.WaitFor(t, "lyna-tmux call "+strconv.Itoa(n), func() bool { return len(w.calls(t)) >= n })
	if got := w.calls(t)[n-1]; got != want {
		t.Fatalf("call %d = %q, want %q", n, got, want)
	}
}

// statusCell finds the zero-based client cell where text starts on the
// status line.
func (w *workspace) statusCell(t *testing.T, text string) (x, y int) {
	t.Helper()
	lines := strings.Split(w.Screen(t), "\n")
	for y = len(lines) - 1; y >= 0; y-- {
		if i := strings.Index(lines[y], text); i >= 0 {
			return w.cells(t, lines[y][:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, strings.Join(lines, "\n"))
	return 0, 0
}

// cells is the number of cells tmux takes to draw s. It is not the number of
// runes: a character can be drawn two cells wide, and which ones are differs
// between versions, so the drawing is measured by the tmux under test rather
// than counted here.
func (w *workspace) cells(t *testing.T, s string) int {
	t.Helper()
	const option = "@lt_measure"
	w.run(t, "set-option", "-g", option, s)
	out := w.run(t, "display-message", "-p", "#{w:"+option+"}")
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("width of %q: %q", s, out)
	}
	return n
}

// press clicks a mouse button after waiting out the window tmux gives a press
// to become the second click of a double click. tmux 3.4 drops a press that
// lands inside that window whenever the button differs from the one before
// it: no key is produced at all, so nothing the configuration binds can
// answer it. A user pausing between two clicks is what this waits for.
func (w *workspace) press(t *testing.T, button, x, y int) {
	t.Helper()
	time.Sleep(clickTimeout)
	w.Click(t, button, x, y)
}

// chooseItem presses the key of a menu item and waits for the menu to be
// gone. tmux hands every key and mouse event to the menu for as long as it is
// drawn, and a menu is drawn a little longer than the item it ran takes to
// finish, so an event sent as soon as the item took effect is swallowed.
func (w *workspace) chooseItem(t *testing.T, key, menu string) {
	t.Helper()
	w.Keys(t, key)
	w.WaitScreen(t, menu, true)
}

// menuKey returns the key of the keys-menu item whose label starts with label.
func (w *workspace) menuKey(t *testing.T, label string) string {
	t.Helper()
	for _, it := range w.env.KeysMenu().Items {
		if strings.HasPrefix(it.Label, label) {
			return it.Key
		}
	}
	t.Fatalf("no keys menu item %q", label)
	return ""
}

// TestE2EWorkspaceKeysMenusMouse drives one workspace through every binding,
// menu and status button in order; each step starts from the state the
// previous step left.
//
// The status buttons are drawn only where tmux marks a range of the status
// line as clickable, and each step here starts from what the one before it
// left, so a tmux without that cannot run a part of the list. Keys, menus and
// the mouse on panes and window tabs are covered on every supported version by
// TestE2EHostileProjectPath and by the window menu end to end test.
func TestE2EWorkspaceKeysMenusMouse(t *testing.T) {
	if v := installedVersion(t); !v.Has(tmux.FeatureUserRanges) {
		t.Skipf("tmux %s draws no status buttons; they arrived in 3.4", v)
	}
	w := startWorkspace(t, "project")
	dir := w.dir
	steps := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"status line drawn", func(t *testing.T) {
			w.WaitScreen(t, " λ  main ", false)
			w.WaitScreen(t, "⊞ split", false)
		}},
		{"split right", func(t *testing.T) {
			w.Keys(t, `M-\`)
			w.waitActive(t, "#{window_panes}|#{@lt_role}|#{pane_at_right}", "2|shell|1")
			w.waitActive(t, "#{pane_current_path}", dir)
		}},
		{"split down", func(t *testing.T) {
			w.Keys(t, "M--")
			w.waitActive(t, "#{window_panes}|#{@lt_role}|#{pane_at_bottom}|#{pane_index}", "3|shell|1|3")
		}},
		{"zoom and unzoom", func(t *testing.T) {
			w.Keys(t, "M-z")
			w.waitActive(t, "#{window_zoomed_flag}", "1")
			w.Keys(t, "M-z")
			w.waitActive(t, "#{window_zoomed_flag}", "0")
		}},
		{"previous and next pane", func(t *testing.T) {
			w.Keys(t, "M-[")
			w.waitActive(t, "#{pane_index}", "2")
			w.Keys(t, "M-]")
			w.waitActive(t, "#{pane_index}", "3")
		}},
		{"resize", func(t *testing.T) {
			before := w.active(t, "#{pane_height}")
			w.Keys(t, "M-S-Up")
			tmuxtest.WaitFor(t, "pane height change", func() bool { return w.active(t, "#{pane_height}") != before })
		}},
		{"focus Claude pane", func(t *testing.T) {
			w.Keys(t, "M-c")
			w.waitActive(t, "#{pane_index}|#{@lt_role}", "1|claude")
		}},
		{"close pane asks first", func(t *testing.T) {
			w.run(t, "select-pane", "-t", "=main:1.3")
			w.Keys(t, "M-x")
			w.WaitScreen(t, "Close pane 3? (y/n)", false)
			w.Keys(t, "y")
			w.waitActive(t, "#{window_panes}", "2")
		}},
		{"new window and select window", func(t *testing.T) {
			w.Keys(t, "M-n")
			w.waitActive(t, "#{session_windows}|#{window_index}|#{@lt_role}", "2|2|shell")
			w.waitActive(t, "#{pane_current_path}", dir)
			w.Keys(t, "M-1")
			w.waitActive(t, "#{window_index}", "1")
		}},
		{"agents popup", func(t *testing.T) {
			w.Keys(t, "M-a")
			w.waitCall(t, 1, dir+"|agents --popup")
		}},
		{"review popup", func(t *testing.T) {
			w.Keys(t, "M-g")
			w.waitCall(t, 2, dir+"|review --popup")
		}},
		{"prefix agents popup", func(t *testing.T) {
			w.Keys(t, "C-b", "a")
			w.waitCall(t, 3, dir+"|agents --popup")
		}},
		{"keys menu runs an item", func(t *testing.T) {
			w.Keys(t, "M-Space")
			w.WaitScreen(t, keysMenu, false)
			w.chooseItem(t, w.menuKey(t, "Split right"), keysMenu)
			w.waitActive(t, "#{window_panes}|#{@lt_role}", "3|shell")
		}},
		{"prefix menu closes on escape", func(t *testing.T) {
			w.Keys(t, "C-b", "Space")
			w.WaitScreen(t, keysMenu, false)
			w.Keys(t, "Escape")
			w.WaitScreen(t, keysMenu, true)
		}},
		{"right click pane menu", func(t *testing.T) {
			w.run(t, "select-pane", "-t", "=main:1.1")
			w.press(t, 2, 5, 3)
			w.WaitScreen(t, paneMenu, false)
			w.chooseItem(t, "d", paneMenu)
			w.waitActive(t, "#{window_panes}|#{@lt_role}|#{pane_current_path}", "4|shell|"+dir)
		}},
		{"status split button", func(t *testing.T) {
			before := w.active(t, "#{window_panes}")
			x, y := w.statusCell(t, "⊞ split")
			w.press(t, 0, x, y)
			n, _ := strconv.Atoi(before)
			w.waitActive(t, "#{window_panes}", strconv.Itoa(n+1))
		}},
		{"Claude submenu types a slash command", func(t *testing.T) {
			w.press(t, 2, 9, 6)
			w.WaitScreen(t, paneMenu, false)
			w.Keys(t, "C")
			w.WaitScreen(t, claudeMenu, false)
			w.chooseItem(t, "w", claudeMenu)
			tmuxtest.WaitFor(t, "slash command in Claude pane", func() bool {
				return strings.Contains(w.run(t, "capture-pane", "-p", "-t", "=main:1.1"), "/workflows")
			})
		}},
		{"status review button", func(t *testing.T) {
			x, y := w.statusCell(t, "± review")
			w.press(t, 0, x, y)
			w.waitCall(t, 4, dir+"|review --popup")
		}},
		{"Claude items disabled on a shell pane", func(t *testing.T) {
			w.Keys(t, "M-]")
			w.waitActive(t, "#{@lt_role}", "shell")
			x, y := w.statusCell(t, "› shell")
			w.press(t, 2, x, y+1)
			w.WaitScreen(t, paneMenu, false)
			w.Keys(t, "C")
			w.Keys(t, "Escape")
			w.WaitScreen(t, paneMenu, true)
			if strings.Contains(w.Screen(t), claudeMenu) {
				t.Fatal("disabled Claude item opened the submenu")
			}
		}},
		{"status agents button", func(t *testing.T) {
			x, y := w.statusCell(t, "◎ ")
			w.press(t, 0, x, y)
			w.waitCall(t, 5, dir+"|agents --popup")
		}},
		{"right click window tab opens the window menu", func(t *testing.T) {
			x, y := w.statusCell(t, "1:")
			w.press(t, 2, x, y)
			w.WaitScreen(t, windowMenu, false)
			w.Keys(t, "Escape")
			w.WaitScreen(t, windowMenu, true)
		}},
		{"window tab click selects the window", func(t *testing.T) {
			x, y := w.statusCell(t, "2:")
			w.press(t, 0, x, y)
			w.waitActive(t, "#{window_index}", "2")
		}},
		{"right click status left opens the session menu", func(t *testing.T) {
			x, y := w.statusCell(t, " λ ")
			w.press(t, 2, x+1, y)
			w.WaitScreen(t, sessionMenu, false)
			w.Keys(t, "Escape")
			w.WaitScreen(t, sessionMenu, true)
		}},
		{"status menu button opens the session menu", func(t *testing.T) {
			x, y := w.statusCell(t, "≡")
			w.press(t, 0, x, y)
			w.WaitScreen(t, sessionMenu, false)
			w.Keys(t, "Escape")
			w.WaitScreen(t, sessionMenu, true)
		}},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.do) {
			t.Fatalf("step %q failed; later steps depend on it", s.name)
		}
	}
}

// TestE2EHostileProjectPath runs the path-carrying bindings, menus and popups
// in a directory whose name is valid shell, tmux and format syntax. The name
// must reach every command as data.
func TestE2EHostileProjectPath(t *testing.T) {
	name := `it's "q" $(touch pwned) ;#{pane_id} %H #[fg=red] \; ~`
	w := startWorkspace(t, name)
	dir := w.dir
	pwnedAnywhere := func(t *testing.T) {
		t.Helper()
		_ = filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.Name() == "pwned" {
				t.Fatalf("command injection: %s exists", path)
			}
			return nil
		})
	}
	steps := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"split keeps the directory", func(t *testing.T) {
			w.Keys(t, `M-\`)
			w.waitActive(t, "#{window_panes}", "2")
			w.waitActive(t, "#{pane_current_path}", dir)
		}},
		{"popup runs in the directory", func(t *testing.T) {
			w.Keys(t, "M-a")
			w.waitCall(t, 1, dir+"|agents --popup")
		}},
		{"menu split keeps the directory", func(t *testing.T) {
			w.Keys(t, "M-Space")
			w.WaitScreen(t, keysMenu, false)
			w.chooseItem(t, w.menuKey(t, "Split down"), keysMenu)
			w.waitActive(t, "#{window_panes}", "3")
			w.waitActive(t, "#{pane_current_path}", dir)
		}},
		{"pane menu popup runs in the directory", func(t *testing.T) {
			w.press(t, 2, 5, 3)
			w.WaitScreen(t, paneMenu, false)
			w.chooseItem(t, "a", paneMenu)
			w.waitCall(t, 2, dir+"|agents --popup")
		}},
		{"new window keeps the directory", func(t *testing.T) {
			w.Keys(t, "M-n")
			w.waitActive(t, "#{session_windows}", "2")
			w.waitActive(t, "#{pane_current_path}", dir)
		}},
		{"nothing was executed", pwnedAnywhere},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.do) {
			t.Fatalf("step %q failed; later steps depend on it", s.name)
		}
	}
}

// TestE2EAltBracketKeepsArrowKeys sends the bytes a terminal really sends. The
// Alt+[ binding is the escape sequence introducer, so a client that read it
// greedily would take the first two bytes of every arrow key and leave the
// pane with a stray letter. The binding holds only when tmux still resolves
// the longer sequence.
func TestE2EAltBracketKeepsArrowKeys(t *testing.T) {
	w := startWorkspace(t, "acme-api")
	w.run(t, "split-window", "-h", "-t", "=main:1", "-c", w.dir)
	first := w.active(t, "#{pane_id}")
	cases := []struct {
		name, send, want string
		moves            bool
	}{
		{name: "an arrow key stays an arrow key", send: "\x1b[A", moves: false},
		{name: "alt and bracket focus the previous pane", send: "\x1b[", moves: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := w.active(t, "#{pane_id}")
			w.Literal(t, tc.send)
			if tc.moves {
				tmuxtest.WaitFor(t, "the focus to move", func() bool { return w.active(t, "#{pane_id}") != before })
				return
			}
			// The pane must not move, and nothing must enter copy mode: both
			// would mean the sequence was read as the binding.
			time.Sleep(300 * time.Millisecond)
			if got := w.active(t, "#{pane_id}"); got != before {
				t.Fatalf("the focus moved to %s on an arrow key", got)
			}
			if mode := w.active(t, "#{pane_in_mode}"); mode != "0" {
				t.Fatalf("an arrow key put the pane in a mode: %s", mode)
			}
		})
	}
	if w.active(t, "#{pane_id}") == first {
		t.Fatalf("the focus never left the pane the split created")
	}
}
