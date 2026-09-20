package tmux

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

func testEnv() Env {
	return Env{
		Bin:         "/opt/lyna tools/bin/lmux",
		ConfPath:    "/state/lyna-tmux/tmux.conf",
		PopupWidth:  "90%",
		PopupHeight: "85%",
		Bindings:    keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"}),
	}
}

// menuItems splits display-menu arguments into the title and the item triples.
func menuItems(t *testing.T, s Seq) (title string, items [][]string) {
	t.Helper()
	if len(s) != 1 || s[0][0] != "display-menu" {
		t.Fatalf("not a display-menu: %v", s)
	}
	args := s[0][1:]
	for len(args) > 0 {
		switch args[0] {
		case "-T":
			title = args[1]
			args = args[2:]
			continue
		case "-x", "-y", "-t":
			args = args[2:]
			continue
		}
		if args[0] == "" {
			items = append(items, nil)
			args = args[1:]
			continue
		}
		if len(args) < 3 {
			t.Fatalf("truncated menu item %q", args)
		}
		items = append(items, args[:3])
		args = args[3:]
	}
	return title, items
}

func TestMenuSeq(t *testing.T) {
	cases := []struct {
		name      string
		menu      Menu
		wantTitle string
		wantItems [][]string
	}{
		{
			name:      "plain item",
			menu:      Menu{Title: "t", X: "M", Y: "M", Items: []MenuItem{{Label: "Zoom", Key: "z", Seq: Cmd("resize-pane", "-Z")}}},
			wantTitle: " t ",
			wantItems: [][]string{{"Zoom", "z", "resize-pane -Z"}},
		},
		{
			name:      "hint appended",
			menu:      Menu{X: "M", Y: "M", Items: []MenuItem{{Label: "Zoom", Key: "z", Hint: "M-z", Seq: Cmd("resize-pane", "-Z")}}},
			wantTitle: "  ",
			wantItems: [][]string{{"Zoom  M-z", "z", "resize-pane -Z"}},
		},
		{
			name:      "separator",
			menu:      Menu{X: "M", Y: "M", Items: []MenuItem{{}, {Label: "a", Key: "a", Seq: Cmd("x")}}},
			wantTitle: "  ",
			wantItems: [][]string{nil, {"a", "a", "x"}},
		},
		{
			name:      "enabled condition prefixes label",
			menu:      Menu{X: "M", Y: "M", Items: []MenuItem{{Label: "Claude", Key: "C", EnabledIf: "#{pane_active}", Seq: Cmd("x")}}},
			wantTitle: "  ",
			wantItems: [][]string{{"#{?#{pane_active},,-}Claude", "C", "x"}},
		},
		{
			name: "data is format escaped",
			menu: Menu{Title: "#{host}", X: "M", Y: "M", Items: []MenuItem{{
				Label: "#[fg=red]#{pane_id}", Key: "a", Hint: "#x",
				Seq: Cmd("split-window", "-c", "#{pane_current_path}"),
			}}},
			wantTitle: " ##{host} ",
			wantItems: [][]string{{"##[fg=red]##{pane_id}  ##x", "a", "split-window -c '##{pane_current_path}'"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, items := menuItems(t, tc.menu.Seq())
			if title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", title, tc.wantTitle)
			}
			if !slices.EqualFunc(items, tc.wantItems, slices.Equal) {
				t.Fatalf("items = %q, want %q", items, tc.wantItems)
			}
		})
	}
}

// unescapeMenu is what tmux does to a menu item when the menu opens: expand
// formats. For FormatEscape'd text that is exactly un-doubling '#'.
func unescapeMenu(s string) string { return strings.ReplaceAll(s, "##", "#") }

func TestMenusWellFormed(t *testing.T) {
	e := testEnv()
	menus := map[string]Menu{
		"claude":  ClaudeMenu(),
		"pane":    e.PaneMenu(),
		"window":  e.WindowMenu(),
		"session": e.SessionMenu(),
	}
	for name, m := range menus {
		t.Run(name, func(t *testing.T) {
			_, items := menuItems(t, m.Seq())
			seenKeys := map[string]bool{}
			for _, it := range items {
				if it == nil {
					continue
				}
				label, key, cmd := it[0], it[1], it[2]
				if key == "" || seenKeys[key] {
					t.Errorf("item %q: key %q empty or duplicated", label, key)
				}
				seenKeys[key] = true
				if err := checkFormat(label); err != nil {
					t.Errorf("label %q: %v", label, err)
				}
				// Every '#' in a command must be doubled so opening the menu
				// restores the command text exactly. The one exception is the
				// pane the menu opened on, which the menu resolves as it opens
				// (MenuOpenedOnPane): only that format may be single.
				bare := strings.ReplaceAll(cmd, "#{pane_id}", "")
				if strings.Count(strings.ReplaceAll(bare, "##", ""), "#") != 0 {
					t.Errorf("item %q command not format escaped: %q", label, cmd)
				}
				if unescapeMenu(cmd) == "" {
					t.Errorf("item %q has no command", label)
				}
			}
		})
	}
}

func TestClaudeMenu(t *testing.T) {
	_, items := menuItems(t, ClaudeMenu().Seq())
	byKey := map[string]string{}
	for _, it := range items {
		byKey[it[1]] = unescapeMenu(it[2])
	}
	cases := []struct {
		key, want string
	}{
		{"w", "send-keys -l /workflows ; send-keys Enter"},
		{"r", "send-keys -l '/deep-research '"},
		{"u", "send-keys -l '/effort ultracode' ; send-keys Enter"},
		{"f", "send-keys -l '/tui fullscreen' ; send-keys Enter"},
		{"b", "send-keys -l /sandbox ; send-keys Enter"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := byKey[tc.key]; got != tc.want {
				t.Fatalf("item %q = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestPaneMenuClaudeItemsGated(t *testing.T) {
	_, items := menuItems(t, testEnv().PaneMenu().Seq())
	gated := 0
	for _, it := range items {
		if it != nil && strings.HasPrefix(it[0], "#{?"+isClaudePane+",,-}") {
			gated++
		}
	}
	if gated != 2 {
		t.Fatalf("%d Claude-only items gated, want 2", gated)
	}
}

func TestKeysMenuSeq(t *testing.T) {
	cases := []struct {
		name      string
		bindings  []keys.Binding
		wantItems int
		wantSeps  int
		wantHints []string
		notLabels []string
	}{
		{
			name:      "defaults",
			bindings:  keys.Defaults(keys.Options{AltKeys: true}),
			wantItems: 18, // 28 root bindings minus 9 window keys and the menu key
			wantSeps:  2,  // panes | windows | tools
			wantHints: []string{`M-\`, "M-a", "M-e"},
			notLabels: []string{"Menu", "Window 1"},
		},
		{
			name:      "prefix only falls back to tools",
			bindings:  keys.Defaults(keys.Options{AltKeys: false}),
			wantItems: 3,
		},
		{name: "none", bindings: nil, wantItems: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv()
			e.Bindings = tc.bindings
			_, items := menuItems(t, e.KeysMenu().Seq())
			n, seps := 0, 0
			labels := ""
			for _, it := range items {
				if it == nil {
					seps++
					continue
				}
				n++
				labels += it[0] + "\n"
			}
			if n != tc.wantItems || seps != tc.wantSeps {
				t.Fatalf("items=%d seps=%d, want %d and %d:\n%s", n, seps, tc.wantItems, tc.wantSeps, labels)
			}
			for _, h := range tc.wantHints {
				if !strings.Contains(labels, "  "+DrawEscape(h)+"\n") {
					t.Errorf("hint %q missing:\n%s", h, labels)
				}
			}
			for _, l := range tc.notLabels {
				if strings.Contains(labels, l+"  ") {
					t.Errorf("label %q listed", l)
				}
			}
		})
	}
}

// TestMenuMnemonicsAreReachable covers every menu the workspace opens: tmux
// closes a menu on q and on Escape, so an item keyed q could never be chosen,
// and two items sharing a key would make the second one unreachable.
func TestMenuMnemonicsAreReachable(t *testing.T) {
	e := testEnv()
	e.Bindings = keys.Defaults(keys.Options{AltKeys: true})
	// More bindings than there are letters before q, so the menu has to walk
	// past it: with q in the list, the item keyed q would be unreachable.
	many := testEnv()
	for i := range 10 {
		many.Bindings = append(many.Bindings, keys.Binding{
			Table: keys.TableRoot, Action: keys.ActionZoom, Group: "panes",
			Key: "M-" + string(rune('A'+i)), Help: "Action " + strconv.Itoa(i),
		})
	}
	menus := map[string]Menu{
		"keys":       e.KeysMenu(),
		"keys twice": many.KeysMenu(),
		"pane":       e.PaneMenu(),
		"window":     e.WindowMenu(),
		"session":    e.SessionMenu(),
		"claude":     ClaudeMenu(),
	}
	for name, menu := range menus {
		t.Run(name, func(t *testing.T) {
			seen := map[string]string{}
			items := 0
			for _, it := range menu.Items {
				if it.Label == "" {
					continue
				}
				items++
				if it.Key == "" {
					t.Errorf("item %q has no key, so the mouse is the only way to it", it.Label)
					continue
				}
				if it.Key == "q" || it.Key == "Escape" {
					t.Errorf("item %q is keyed %q, which closes the menu", it.Label, it.Key)
				}
				if first, ok := seen[it.Key]; ok {
					t.Errorf("items %q and %q share the key %q", first, it.Label, it.Key)
				}
				seen[it.Key] = it.Label
			}
			if items == 0 {
				t.Fatal("no items")
			}
		})
	}
}

func TestHint(t *testing.T) {
	bs := []keys.Binding{
		{Key: "a", Table: keys.TablePrefix, Action: keys.ActionAgents},
		{Key: "M-a", Table: keys.TableRoot, Action: keys.ActionAgents},
		{Key: "g", Table: keys.TablePrefix, Action: keys.ActionReview},
	}
	cases := []struct {
		action keys.Action
		want   string
	}{
		{keys.ActionAgents, "M-a"},
		{keys.ActionReview, "prefix g"},
		{keys.ActionZoom, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.action), func(t *testing.T) {
			if got := hint(bs, tc.action); got != tc.want {
				t.Fatalf("hint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClickSeq(t *testing.T) {
	got := ClickSeq()
	if len(got) != 1 || len(got[0]) != 3 || got[0][0] != "run-shell" || got[0][1] != "-C" {
		t.Fatalf("ClickSeq = %q", got)
	}
	f := got[0][2]
	if err := checkFormat(f); err != nil {
		t.Fatal(err)
	}
	// Walk the conditional chain: each level maps one range to its registered
	// command, the innermost else selects the clicked window.
	var ranges []string
	for strings.HasPrefix(f, "#{?") {
		parts := splitTop(f[3 : len(f)-1])
		if len(parts) != 3 {
			t.Fatalf("level %q has %d parts", f, len(parts))
		}
		rng := strings.TrimSuffix(strings.TrimPrefix(parts[0], "#{==:#{mouse_status_range},"), "}")
		name := strings.TrimSuffix(strings.TrimPrefix(parts[1], "#{@lt_do_"), "}")
		ranges = append(ranges, rng+"="+name)
		f = parts[2]
	}
	if f != "select-window -t =" {
		t.Fatalf("fallback = %q", f)
	}
	want := []string{"split=split_right", "agents=agents", "review=review", "menu=menu_session", "sandbox=sandbox"}
	if !slices.Equal(ranges, want) {
		t.Fatalf("ranges = %v, want %v", ranges, want)
	}
}

// parseSeq parses a command string rendered by Seq.String, the subset of the
// tmux grammar ConfToken produces: bare words, single-quoted runs with '\”
// for a quote, commands separated by " ; ".
func parseSeq(s string) (Seq, error) {
	var out Seq
	var cmd Command
	var tok strings.Builder
	inTok, quoted := false, false
	flush := func() {
		if inTok {
			cmd = append(cmd, tok.String())
			tok.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\'' {
				quoted = false
			} else {
				tok.WriteByte(c)
			}
		case c == '\'':
			quoted, inTok = true, true
		case c == '\\' && i+1 < len(s) && s[i+1] == '\'':
			tok.WriteByte('\'')
			inTok = true
			i++
		case c == ' ':
			flush()
		case c == ';' && !inTok:
			out = append(out, cmd)
			cmd = nil
		default:
			tok.WriteByte(c)
			inTok = true
		}
	}
	if quoted {
		return nil, errUnterminated
	}
	flush()
	if len(cmd) > 0 {
		out = append(out, cmd)
	}
	return out, nil
}

var errUnterminated = errors.New("unterminated quote")

func FuzzSeqStringRoundTrip(f *testing.F) {
	for _, s := range []string{"plain", "a b", "it's", "';'", `\`, "#{x}", ";", ""} {
		f.Add(s, "second")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		clean := func(s string) string {
			return strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, s)
		}
		in := Cmd("cmd", a).Then(Cmd("next", b))
		got, err := parseSeq(in.String())
		if err != nil {
			t.Fatal(err)
		}
		want := Seq{{"cmd", clean(a)}, {"next", clean(b)}}
		if !slices.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("round trip %q -> %q", want, got)
		}
	})
}

// TestWindowMenuResolvesTheClickedPane pins when the pane id of a layout item
// is decided: the menu expands it as it opens, on the window whose tab was
// clicked. Escaped like every other format, it would instead be expanded when
// the item runs, against whatever pane is current then, and the layout would
// land on the wrong window.
func TestWindowMenuResolvesTheClickedPane(t *testing.T) {
	e := testEnv()
	_, items := menuItems(t, e.WindowMenu().Seq())
	layouts := 0
	for _, it := range items {
		if it == nil || !strings.HasPrefix(it[0], "Layout: ") {
			continue
		}
		layouts++
		cmd := it[2]
		if !strings.Contains(cmd, " --pane #{pane_id}") {
			t.Errorf("item %q does not take the pane the menu opened on: %q", it[0], cmd)
		}
		if strings.Contains(cmd, "##{pane_id}") {
			t.Errorf("item %q escapes the pane format, so it resolves when the item runs: %q", it[0], cmd)
		}
	}
	if layouts == 0 {
		t.Fatal("the window menu has no layout item")
	}
}
