package tmux

import (
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
)

// MenuItem is one entry of a tmux menu. A zero Seq with an empty Label is a
// separator.
type MenuItem struct {
	Label string
	Key   string
	Seq   Seq
	// Hint is shown right of the label, for example the Alt shortcut.
	Hint string
	// EnabledIf is a format condition; the item is shown disabled when it is
	// false. Empty means always enabled.
	EnabledIf string
}

// Menu is a tmux display-menu.
type Menu struct {
	Title string
	// X and Y position the menu: M (mouse), C (center), P (pane), W (window)
	// or R (right) and S (status line) as tmux defines them.
	X, Y  string
	Items []MenuItem
}

// MenuOpenedOnPane is what a menu item command writes where the id of the pane
// the menu opened on belongs. Seq turns it into a format the menu expands when
// it opens, so the command carries a literal pane id from then on. A command
// that spelled the format itself would have it expanded when the item runs,
// against whatever pane is current at that moment, which under load is not the
// pane the user clicked on.
const MenuOpenedOnPane = "@lt-menu-pane@"

// Seq renders the display-menu command.
//
// tmux expands formats in menu labels and item commands when the menu opens
// and then parses each chosen command. A command that referred to data such
// as #{pane_current_path} directly would be re-parsed with that data spliced
// in, so every item command is format-escaped here: the menu restores the
// original text and each command expands its own arguments when it runs.
// MenuOpenedOnPane is the exception, and the only one: it is left unescaped so
// the menu resolves it at the moment it opens.
func (m Menu) Seq() Seq {
	args := []string{"-T", " " + DrawEscape(m.Title) + " ", "-x", m.X, "-y", m.Y}
	for _, it := range m.Items {
		if it.Label == "" && len(it.Seq) == 0 {
			args = append(args, "")
			continue
		}
		label := DrawEscape(it.Label)
		if it.Hint != "" {
			label += "  " + DrawEscape(it.Hint)
		}
		if it.EnabledIf != "" {
			// A leading '-' shows the item disabled.
			label = "#{?" + it.EnabledIf + ",,-}" + label
		}
		args = append(args, label, it.Key, strings.ReplaceAll(FormatEscape(it.Seq.String()), MenuOpenedOnPane, "#{pane_id}"))
	}
	return Cmd("display-menu", args...)
}

// sendToPane types text into the menu's target pane, optionally submitting it.
func sendToPane(text string, submit bool) Seq {
	s := Cmd("send-keys", "-l", text)
	if submit {
		s = s.Then(Cmd("send-keys", "Enter"))
	}
	return s
}

// isClaudePane is true when the target pane runs Claude.
const isClaudePane = "#{==:#{" + OptRole + "}," + RoleClaude + "}"

// ClaudeMenu sends Claude Code slash commands to the pane. Commands that take
// an argument are typed without submitting so the user can complete them.
func ClaudeMenu() Menu {
	type slash struct {
		label, key, text string
		submit           bool
	}
	items := []slash{
		{"Workflows", "w", "/workflows", true},
		{"Deep research...", "r", "/deep-research ", false},
		{"Effort: ultracode", "u", "/effort ultracode", true},
		{"Effort...", "e", "/effort", true},
		{"Model...", "m", "/model", true},
		{"Plan mode", "p", "/plan", true},
		{"Background tasks", "t", "/tasks", true},
		{"Context usage", "c", "/context", true},
		{"Compact context", "k", "/compact", true},
		{"Security review", "s", "/security-review", true},
		{"Sandbox", "b", "/sandbox", true},
		{"Fullscreen renderer", "f", "/tui fullscreen", true},
		{"Resume conversation...", "R", "/resume", true},
		{"Usage", "U", "/usage", true},
	}
	m := Menu{Title: "claude", X: "M", Y: "M"}
	for _, s := range items {
		m.Items = append(m.Items, MenuItem{Label: s.label, Key: s.key, Seq: sendToPane(s.text, s.submit)})
	}
	return m
}

// PaneMenu is the right-click menu of a pane.
func (e Env) PaneMenu() Menu {
	return Menu{Title: "pane", X: "M", Y: "M", Items: []MenuItem{
		{Label: "Split right", Key: "r", Seq: SplitSeq(true), Hint: hint(e.Bindings, keys.ActionSplitRight)},
		{Label: "Split down", Key: "d", Seq: SplitSeq(false), Hint: hint(e.Bindings, keys.ActionSplitDown)},
		{Label: "Zoom", Key: "z", Seq: Cmd("resize-pane", "-Z"), Hint: hint(e.Bindings, keys.ActionZoom)},
		{Label: "Swap with next", Key: "s", Seq: Cmd("swap-pane", "-D")},
		{Label: "Copy mode", Key: "c", Seq: Cmd("copy-mode")},
		{},
		{Label: "Claude...", Key: "C", Seq: DoSeq(DoMenuClaude), EnabledIf: isClaudePane},
		{
			Label: "Restart Claude", Key: "R", EnabledIf: isClaudePane,
			Seq: Cmd("confirm-before", "-p", "Restart Claude in pane #P? (y/n)", "respawn-pane -k"),
		},
		{Label: "Review changes", Key: "g", Seq: e.ReviewSeq(), Hint: hint(e.Bindings, keys.ActionReview)},
		{Label: "Agents", Key: "a", Seq: e.AgentsSeq(), Hint: hint(e.Bindings, keys.ActionAgents)},
		{},
		{Label: "Close pane", Key: "x", Seq: Cmd("confirm-before", "-p", "Close pane #P? (y/n)", "kill-pane"), Hint: hint(e.Bindings, keys.ActionClosePane)},
	}}
}

// WindowMenu is the right-click menu of a window tab.
func (e Env) WindowMenu() Menu {
	items := []MenuItem{
		{Label: "New window", Key: "n", Seq: e.ActionSeq(keys.Binding{Action: keys.ActionNewWindow}), Hint: hint(e.Bindings, keys.ActionNewWindow)},
		{Label: "Rename window", Key: "r", Seq: Cmd("command-prompt", "-I", "#W", "-p", "rename window:", `rename-window -- "%%%"`)},
		{},
	}
	// Every concrete built-in layout, numbered; auto only makes sense at
	// creation, when the terminal size is known. The pane the menu opened on is
	// resolved by the menu itself (MenuOpenedOnPane), so the layout lands on the
	// window whose tab was clicked even when another window is current by the
	// time the item runs. A pane id is a safe shell word.
	n := 0
	for _, name := range layout.Names() {
		if name == layout.Auto {
			continue
		}
		n++
		item := MenuItem{Label: "Layout: " + name, Key: strconv.Itoa(n), Seq: Cmd("run-shell", e.binCommand("layout", name)+" --pane "+MenuOpenedOnPane)}
		items = append(items, item)
	}
	items = append(items,
		MenuItem{Label: "Even tiles", Key: "t", Seq: Cmd("select-layout", "tiled")},
		MenuItem{},
		MenuItem{Label: "Close window", Key: "x", Seq: Cmd("confirm-before", "-p", "Close window #W? (y/n)", "kill-window")},
	)
	return Menu{Title: "window", X: "M", Y: "S", Items: items}
}

// SessionMenu is the right-click menu of the status line's session block.
func (e Env) SessionMenu() Menu {
	return Menu{Title: "lyna-tmux", X: "M", Y: "S", Items: []MenuItem{
		{Label: "Agents", Key: "a", Seq: e.AgentsSeq(), Hint: hint(e.Bindings, keys.ActionAgents)},
		{Label: "Review changes", Key: "g", Seq: e.ReviewSeq(), Hint: hint(e.Bindings, keys.ActionReview)},
		{Label: "Live changes", Key: "l", Seq: e.ChangesSeq()},
		{Label: "Sandbox status", Key: "b", Seq: e.SandboxSeq()},
		{Label: "Sessions and windows", Key: "w", Seq: Cmd("choose-tree", "-Zs"), Hint: hint(e.Bindings, keys.ActionTree)},
		{Label: "Scratch shell", Key: "s", Seq: e.ScratchSeq(), Hint: hint(e.Bindings, keys.ActionScratch)},
		{},
		{Label: "Reload configuration", Key: "R", Seq: e.ActionSeq(keys.Binding{Action: keys.ActionReload})},
		{Label: "Detach", Key: "d", Seq: Cmd("detach-client")},
	}}
}

// menuMnemonics are the keys the key help menu hands out, in order. q is left
// out because tmux closes a menu on q, so an item keyed q could never be
// chosen; the digits follow the letters so a workspace with custom bindings
// keeps a key on every line rather than leaving the last ones to the mouse.
const menuMnemonics = "abcdefghijklmnoprstuvwxyz0123456789"

// KeysMenu is the key help menu: every installed Alt binding with its key,
// runnable from the menu.
func (e Env) KeysMenu() Menu {
	m := Menu{Title: "keys", X: "C", Y: "C"}
	group := ""
	letters := menuMnemonics
	n := 0
	for _, b := range e.Bindings {
		if b.Table != keys.TableRoot || b.Action == keys.ActionMenu || b.Action == keys.ActionWindow {
			continue
		}
		if b.Group != group && group != "" {
			m.Items = append(m.Items, MenuItem{})
		}
		group = b.Group
		key := ""
		if n < len(letters) {
			key = string(letters[n])
		}
		n++
		m.Items = append(m.Items, MenuItem{Label: b.Help, Key: key, Hint: b.Key, Seq: e.ActionSeq(b)})
	}
	if n == 0 {
		m.Items = append(m.Items,
			MenuItem{Label: "Agents", Key: "a", Seq: e.AgentsSeq()},
			MenuItem{Label: "Review changes", Key: "g", Seq: e.ReviewSeq()},
			MenuItem{Label: "Scratch shell", Key: "s", Seq: e.ScratchSeq()},
		)
	}
	return m
}

// hint returns the root-table key bound to an action, or "".
func hint(bs []keys.Binding, a keys.Action) string {
	for _, b := range bs {
		if b.Action == a && b.Table == keys.TableRoot {
			return b.Key
		}
	}
	for _, b := range bs {
		if b.Action == a && b.Table == keys.TablePrefix {
			return "prefix " + b.Key
		}
	}
	return ""
}

// Status line click targets, used as user range names.
const (
	RangeSplit   = "split"
	RangeAgents  = "agents"
	RangeReview  = "review"
	RangeMenu    = "menu"
	RangeSandbox = "sandbox"
)

// ClickSeq dispatches a left click on the status line: user ranges run their
// registered command, anything else (a window tab) selects the clicked window
// as tmux does by default. The dispatch is one format expanded by run-shell -C,
// so no command is nested inside another.
func ClickSeq() Seq {
	branches := []struct{ rng, do string }{
		{RangeSplit, DoSplitRight},
		{RangeAgents, DoAgents},
		{RangeReview, DoReview},
		{RangeMenu, DoMenuSession},
		{RangeSandbox, DoSandbox},
	}
	f := "select-window -t ="
	for i := len(branches) - 1; i >= 0; i-- {
		b := branches[i]
		f = cond("#{==:#{mouse_status_range},"+b.rng+"}", "#{"+DoOption(b.do)+"}", f)
	}
	return Cmd("run-shell", "-C", f)
}
