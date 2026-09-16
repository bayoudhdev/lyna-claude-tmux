package tui

import (
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// fixedNow is the clock of every golden frame.
var fixedNow = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func clock() time.Time { return fixedNow }

// testStyles builds styles for a palette, depth and icon set by name.
func testStyles(t testing.TB, palette string, depth theme.Depth, icons string) Styles {
	t.Helper()
	p, err := theme.Get(palette)
	if err != nil {
		t.Fatal(err)
	}
	ic, err := theme.GetIcons(icons)
	if err != nil {
		t.Fatal(err)
	}
	return NewStyles(Theme{Palette: p, Depth: depth, Icons: ic})
}

// goldenStyles is the fixed look of the frame goldens: the default palette in
// truecolor with unicode icons.
func goldenStyles(t testing.TB) Styles { return testStyles(t, "lyna", theme.DepthTrue, "unicode") }

// press builds a key press from its String spelling.
func press(s string) tea.KeyPressMsg {
	named := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
	}
	var mod tea.KeyMod
	name := s
	for {
		switch {
		case strings.HasPrefix(name, "ctrl+"):
			mod |= tea.ModCtrl
			name = name[len("ctrl+"):]
			continue
		case strings.HasPrefix(name, "alt+"):
			mod |= tea.ModAlt
			name = name[len("alt+"):]
			continue
		case strings.HasPrefix(name, "shift+"):
			mod |= tea.ModShift
			name = name[len("shift+"):]
			continue
		}
		break
	}
	if code, ok := named[name]; ok {
		return tea.KeyPressMsg{Code: code, Mod: mod}
	}
	if name == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " ", Mod: mod}
	}
	r := []rune(name)[0]
	if mod != 0 {
		return tea.KeyPressMsg{Code: r, Mod: mod}
	}
	return tea.KeyPressMsg{Code: r, Text: name}
}

// run is the outcome of driving a model.
type run struct {
	model tea.Model
	quit  bool
	execs []tea.ExecCommand
}

// drive delivers msgs one by one and runs every resulting command
// synchronously, feeding the messages back, the way the program loop would.
// Timer commands (ticks, cursor blinks) are skipped: they only block, and
// tests send their messages directly.
func drive(t *testing.T, m tea.Model, first tea.Cmd, msgs ...tea.Msg) run {
	t.Helper()
	r := run{model: m}
	r.pump(t, first)
	for _, msg := range msgs {
		next, cmd := r.model.Update(msg)
		r.model = next
		r.pump(t, cmd)
	}
	return r
}

func (r *run) pump(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 10000 {
			t.Fatal("command loop does not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil || timerCmd(c) {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if cmds, ok := sequence(msg); ok {
			queue = append(queue, cmds...)
			continue
		}
		if _, ok := msg.(tea.QuitMsg); ok {
			r.quit = true
			continue
		}
		if exe, ok := execOf(msg); ok {
			r.execs = append(r.execs, exe)
			continue
		}
		next, more := r.model.Update(msg)
		r.model = next
		queue = append(queue, more)
	}
}

func timerCmd(c tea.Cmd) bool {
	name := runtime.FuncForPC(reflect.ValueOf(c).Pointer()).Name()
	return strings.Contains(name, "bubbletea/v2.Tick.") || strings.Contains(name, "bubbletea/v2.Every.") ||
		strings.Contains(name, "cursor.(*Model).Blink")
}

// sequence unpacks tea.Sequence's unexported message.
func sequence(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeFor[tea.Cmd]() {
		return nil, false
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := range cmds {
		cmds[i], _ = v.Index(i).Interface().(tea.Cmd)
	}
	return cmds, true
}

// execOf extracts the command of tea.Exec's unexported message.
func execOf(msg tea.Msg) (tea.ExecCommand, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Struct || v.Type().String() != "tea.execMsg" {
		return nil, false
	}
	addressable := reflect.New(v.Type()).Elem()
	addressable.Set(v)
	f := addressable.FieldByName("cmd")
	if !f.IsValid() {
		return nil, false
	}
	exe, ok := reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem().Interface().(tea.ExecCommand)
	return exe, ok
}

// fakeExec is a terminal handover command.
type fakeExec struct{ name string }

func (fakeExec) Run() error          { return nil }
func (fakeExec) SetStdin(io.Reader)  {}
func (fakeExec) SetStdout(io.Writer) {}
func (fakeExec) SetStderr(io.Writer) {}

type viewer interface{ View() tea.View }

// assertFrame checks a rendered frame's geometry and compares it with a plain
// golden and, when withANSI is set, a golden that keeps the color sequences.
func assertFrame(t *testing.T, name string, m viewer, width, height int, withANSI bool) {
	t.Helper()
	content := m.View().Content
	lines := strings.Split(content, "\n")
	if len(lines) != height {
		t.Errorf("%s: %d lines, want %d", name, len(lines), height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("%s: line %d is %d cells wide, over %d: %q", name, i+1, w, width, ansi.Strip(l))
		}
		if strings.Contains(l, "\t") || strings.ContainsAny(ansi.Strip(l), "\x1b\x07\r") {
			t.Errorf("%s: line %d carries control characters: %q", name, i+1, l)
		}
	}
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	golden.Assert(t, name+".txt.golden", []byte(strings.Join(plain, "\n")+"\n"))
	if withANSI {
		golden.Assert(t, name+".ansi.golden", []byte(content+"\n"))
	}
}

func resize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

// sizes are the golden frame sizes.
var sizes = []struct {
	name          string
	width, height int
	ansi          bool
}{
	{name: "100x30", width: 100, height: 30, ansi: true},
	{name: "60x20", width: 60, height: 20},
}
