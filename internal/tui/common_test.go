package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

func TestTextFitting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fn   func(string, int, string) string
		in   string
		w    int
		want string
	}{
		{name: "truncate keeps short text", fn: truncate, in: "abc", w: 5, want: "abc"},
		{name: "truncate adds the ellipsis", fn: truncate, in: "abcdef", w: 4, want: "abc…"},
		{name: "truncate zero width", fn: truncate, in: "abc", w: 0, want: ""},
		{name: "truncate counts wide runes", fn: truncate, in: "日本語", w: 4, want: "日…"},
		{name: "fit pads", fn: fit, in: "ab", w: 4, want: "ab  "},
		{name: "fit truncates", fn: fit, in: "abcdef", w: 3, want: "ab…"},
		{name: "fitRight pads left", fn: fitRight, in: "7m", w: 4, want: "  7m"},
		{name: "fitRight truncates", fn: fitRight, in: "abcdef", w: 3, want: "ab…"},
		{name: "center", fn: center, in: "ab", w: 6, want: "  ab"},
		{name: "center overflow", fn: center, in: "abcdef", w: 3, want: "ab…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.fn(tc.in, tc.w, "…"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: -time.Second, want: "0s"},
		{d: 0, want: "0s"},
		{d: 42 * time.Second, want: "42s"},
		{d: 7*time.Minute + 59*time.Second, want: "7m"},
		{d: 3 * time.Hour, want: "3h"},
		{d: 23*time.Hour + 59*time.Minute, want: "23h"},
		{d: 12 * 24 * time.Hour, want: "12d"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.d); got != tc.want {
			t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestShortPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, home, want string
	}{
		{path: "/home/u/src/app", home: "/home/u", want: "~/src/app"},
		{path: "/home/u/src/app", home: "/home/u/", want: "~/src/app"},
		{path: "/home/u", home: "/home/u", want: "~"},
		{path: "/home/user2/app", home: "/home/u", want: "/home/user2/app"},
		{path: "/srv/app", home: "", want: "/srv/app"},
		{path: "/srv/app", home: "/", want: "/srv/app"},
	}
	for _, tc := range cases {
		if got := shortPath(tc.path, tc.home); got != tc.want {
			t.Errorf("shortPath(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
		}
	}
}

func TestListView(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		start        listView
		delta        int
		n, height    int
		cursor, offs int
	}{
		{name: "empty list resets", start: listView{cursor: 4, offset: 2}, n: 0, height: 3},
		{name: "down inside the window", start: listView{}, delta: 1, n: 10, height: 3, cursor: 1},
		{name: "down scrolls", start: listView{cursor: 2}, delta: 1, n: 10, height: 3, cursor: 3, offs: 1},
		{name: "up scrolls back", start: listView{cursor: 3, offset: 3}, delta: -1, n: 10, height: 3, cursor: 2, offs: 2},
		{name: "clamps past the end", start: listView{cursor: 8, offset: 6}, delta: 50, n: 10, height: 3, cursor: 9, offs: 7},
		{name: "clamps before the start", start: listView{cursor: 1}, delta: -50, n: 10, height: 3},
		{name: "shrinking list pulls the offset", start: listView{cursor: 9, offset: 7}, n: 4, height: 3, cursor: 3, offs: 1},
		{name: "zero height counts as one", start: listView{}, delta: 2, n: 5, height: 0, cursor: 2, offs: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := tc.start
			l.move(tc.delta, tc.n, tc.height)
			if l.cursor != tc.cursor || l.offset != tc.offs {
				t.Errorf("cursor, offset = %d, %d, want %d, %d", l.cursor, l.offset, tc.cursor, tc.offs)
			}
		})
	}
}

func TestNavKeys(t *testing.T) {
	t.Parallel()
	k := newNavKeys()
	cases := []struct {
		key  string
		want int
		ok   bool
	}{
		{key: "up", want: -1, ok: true},
		{key: "k", want: -1, ok: true},
		{key: "ctrl+p", want: -1, ok: true},
		{key: "down", want: 1, ok: true},
		{key: "j", want: 1, ok: true},
		{key: "ctrl+n", want: 1, ok: true},
		{key: "pgup", want: -5, ok: true},
		{key: "ctrl+b", want: -5, ok: true},
		{key: "pgdown", want: 5, ok: true},
		{key: "ctrl+f", want: 5, ok: true},
		{key: "home", want: -20, ok: true},
		{key: "g", want: -20, ok: true},
		{key: "end", want: 20, ok: true},
		{key: "G", want: 20, ok: true},
		{key: "x"},
		{key: "enter"},
	}
	for _, tc := range cases {
		got, ok := k.delta(press(tc.key), 20, 5)
		if got != tc.want || ok != tc.ok {
			t.Errorf("delta(%s) = %d, %v, want %d, %v", tc.key, got, ok, tc.want, tc.ok)
		}
	}
	if got, _ := k.delta(press("pgdown"), 20, 0); got != 1 {
		t.Errorf("page of zero rows moves %d, want 1", got)
	}
}

func TestClicks(t *testing.T) {
	t.Parallel()
	var c clicks
	steps := []struct {
		at     time.Duration
		row    int
		double bool
	}{
		{at: 0, row: 2},
		{at: 100 * time.Millisecond, row: 2, double: true},
		{at: 200 * time.Millisecond, row: 2},
		{at: 300 * time.Millisecond, row: 3},
		{at: 300*time.Millisecond + doubleClick + time.Millisecond, row: 3},
		{at: 300*time.Millisecond + doubleClick + 2*time.Millisecond, row: 3, double: true},
	}
	for i, st := range steps {
		if got := c.click(fixedNow.Add(st.at), st.row); got != st.double {
			t.Errorf("step %d: double = %v, want %v", i, got, st.double)
		}
	}
}

func TestLineInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		start   string
		limit   int
		keys    []string
		want    string
		handled bool
	}{
		{name: "types runes", keys: []string{"a", "b", "space", "é"}, want: "ab é", handled: true},
		{name: "backspace removes one rune", start: "abé", keys: []string{"backspace"}, want: "ab", handled: true},
		{name: "backspace on empty", keys: []string{"backspace"}, want: "", handled: true},
		{name: "ctrl+u clears", start: "some text", keys: []string{"ctrl+u"}, want: "", handled: true},
		{name: "ctrl+w removes a word", start: "one two  ", keys: []string{"ctrl+w"}, want: "one ", handled: true},
		{name: "ctrl+w on one word", start: "one", keys: []string{"ctrl+w"}, want: "", handled: true},
		{name: "limit stops input", start: "abc", limit: 3, keys: []string{"d"}, want: "abc", handled: true},
		{name: "ctrl keys are not text", start: "a", keys: []string{"ctrl+a"}, want: "a"},
		{name: "named keys are not text", start: "a", keys: []string{"left"}, want: "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := lineInput{value: tc.start, limit: tc.limit}
			var handled bool
			for _, k := range tc.keys {
				handled = in.update(press(k))
			}
			if in.value != tc.want || handled != tc.handled {
				t.Errorf("value, handled = %q, %v, want %q, %v", in.value, handled, tc.want, tc.handled)
			}
		})
	}
	in := lineInput{}
	in.insert("a\x1b[31mb\x07c\x7fd")
	if in.value != "a[31mbcd" {
		t.Errorf("control characters inserted: %q", in.value)
	}
}

func TestLineAndBar(t *testing.T) {
	t.Parallel()
	s := goldenStyles(t)
	cases := []struct {
		name  string
		got   string
		width int
		plain string
	}{
		{name: "line pads", got: s.line(8, nil, seg("ab", s.Text)), width: 8, plain: "ab      "},
		{name: "line truncates the overflowing segment", got: s.line(5, nil, seg("abc", s.Text), seg("defg", s.Muted), seg("h", s.Text)), width: 5, plain: "abcd…"},
		{name: "line with background pads in color", got: s.line(4, &s.Selected, seg("a", s.Text)), width: 4, plain: "a   "},
		{name: "bar puts right segments flush right", got: s.bar(10, []segment{seg("left", s.Text)}, []segment{seg("rt", s.Muted)}), width: 10, plain: "left    rt"},
		{name: "bar drops right segments that do not fit", got: s.bar(6, []segment{seg("left", s.Text)}, []segment{seg("right", s.Muted)}), width: 6, plain: "left  "},
		{name: "rule with title", got: s.rule(12, "preview"), width: 12, plain: "─ preview ──"},
		{name: "rule without title", got: s.rule(3, ""), width: 3, plain: "───"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if w := ansi.StringWidth(tc.got); w != tc.width {
				t.Errorf("width = %d, want %d", w, tc.width)
			}
			if p := ansi.Strip(tc.got); p != tc.plain {
				t.Errorf("plain = %q, want %q", p, tc.plain)
			}
		})
	}
	ascii := testStyles(t, "lyna", theme.Depth16, "ascii")
	if got := ansi.Strip(ascii.rule(6, "")); got != "------" {
		t.Errorf("ascii rule = %q", got)
	}
	bg := s.line(4, &s.Selected, seg("a", s.Text))
	if strings.Count(bg, "48;2;") < 2 {
		t.Errorf("background not applied to the segment and the padding: %q", bg)
	}
}

func TestScreen(t *testing.T) {
	t.Parallel()
	got := screen([]string{"abcdef", "x"}, 4, 3)
	if got != "abcd\nx\n" {
		t.Errorf("screen = %q", got)
	}
	if screen([]string{"a"}, 4, 0) != "" {
		t.Error("zero height draws lines")
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	if w, h := sizeOr(0, -1); w != defaultWidth || h != defaultHeight {
		t.Errorf("sizeOr defaults = %d, %d", w, h)
	}
	if w, h := sizeOr(10, 5); w != 10 || h != 5 {
		t.Errorf("sizeOr = %d, %d", w, h)
	}
	if ctxOr(nil) == nil { //nolint:staticcheck // nil is the case under test
		t.Error("ctxOr(nil) is nil")
	}
	ctx := context.WithValue(context.Background(), ctxKey{}, 1)
	if ctxOr(ctx) != ctx {
		t.Error("ctxOr replaced a context")
	}
	if nowOr(nil)().IsZero() || !nowOr(clock)().Equal(fixedNow) {
		t.Error("nowOr")
	}
}

type ctxKey struct{}
