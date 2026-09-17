// Package keys defines the workspace key bindings and the keys Claude Code
// uses itself, so no binding lyna-tmux installs without a prefix can take a
// key away from Claude.
package keys

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Table is the tmux key table a binding lives in.
type Table string

const (
	// TableRoot bindings fire without the prefix key.
	TableRoot Table = "root"
	// TablePrefix bindings fire after the prefix key.
	TablePrefix Table = "prefix"
)

// Action is what a binding does. The tmux adapter maps each action to tmux
// commands; the domain only names them.
type Action string

// Actions.
const (
	ActionPanePrev    Action = "pane-prev"
	ActionPaneNext    Action = "pane-next"
	ActionResizeLeft  Action = "resize-left"
	ActionResizeRight Action = "resize-right"
	ActionResizeUp    Action = "resize-up"
	ActionResizeDown  Action = "resize-down"
	ActionSplitRight  Action = "split-right"
	ActionSplitDown   Action = "split-down"
	ActionZoom        Action = "zoom"
	ActionClosePane   Action = "close-pane"
	ActionNewWindow   Action = "new-window"
	ActionWindow      Action = "window" // Arg is the window index
	ActionTree        Action = "tree"
	ActionAgents      Action = "agents"
	ActionReview      Action = "review"
	ActionScratch     Action = "scratch"
	ActionFocusClaude Action = "focus-claude"
	ActionMenu        Action = "menu"
	ActionSendPrefix  Action = "send-prefix"
	ActionDetach      Action = "detach"
	ActionReload      Action = "reload"
)

// Group labels used by `lyna-tmux keys` and the which-key menu.
const (
	GroupPanes   = "Panes"
	GroupWindows = "Windows"
	GroupTools   = "Tools"
	GroupSession = "Session"
)

// Binding is one key binding.
type Binding struct {
	Key    string // tmux key name, for example M-\ or M-S-Left
	Table  Table
	Action Action
	Arg    string
	Group  string
	Help   string
}

// Options select which bindings are generated.
type Options struct {
	// AltKeys installs the Alt (Meta) bindings that work without the prefix.
	AltKeys bool
	// Prefix is the tmux prefix key, for example C-b.
	Prefix string
}

// Defaults returns the workspace bindings. Root bindings use Alt so they
// never reach Claude's own Ctrl bindings; the same actions are always
// available after the prefix so a terminal without Option-as-Meta still works.
func Defaults(o Options) []Binding {
	var out []Binding
	if o.AltKeys {
		out = append(out,
			Binding{Key: "M-[", Table: TableRoot, Action: ActionPanePrev, Group: GroupPanes, Help: "Focus previous pane"},
			Binding{Key: "M-]", Table: TableRoot, Action: ActionPaneNext, Group: GroupPanes, Help: "Focus next pane"},
			Binding{Key: "M-S-Left", Table: TableRoot, Action: ActionResizeLeft, Group: GroupPanes, Help: "Resize pane left"},
			Binding{Key: "M-S-Right", Table: TableRoot, Action: ActionResizeRight, Group: GroupPanes, Help: "Resize pane right"},
			Binding{Key: "M-S-Up", Table: TableRoot, Action: ActionResizeUp, Group: GroupPanes, Help: "Resize pane up"},
			Binding{Key: "M-S-Down", Table: TableRoot, Action: ActionResizeDown, Group: GroupPanes, Help: "Resize pane down"},
			Binding{Key: `M-\`, Table: TableRoot, Action: ActionSplitRight, Group: GroupPanes, Help: "Split right"},
			Binding{Key: "M--", Table: TableRoot, Action: ActionSplitDown, Group: GroupPanes, Help: "Split down"},
			Binding{Key: "M-z", Table: TableRoot, Action: ActionZoom, Group: GroupPanes, Help: "Zoom pane"},
			Binding{Key: "M-x", Table: TableRoot, Action: ActionClosePane, Group: GroupPanes, Help: "Close pane"},
			Binding{Key: "M-c", Table: TableRoot, Action: ActionFocusClaude, Group: GroupPanes, Help: "Focus Claude pane"},
			Binding{Key: "M-n", Table: TableRoot, Action: ActionNewWindow, Group: GroupWindows, Help: "New window"},
		)
		for i := 1; i <= 9; i++ {
			n := strconv.Itoa(i)
			out = append(out, Binding{Key: "M-" + n, Table: TableRoot, Action: ActionWindow, Arg: n, Group: GroupWindows, Help: "Window " + n})
		}
		out = append(out,
			Binding{Key: "M-e", Table: TableRoot, Action: ActionTree, Group: GroupWindows, Help: "Sessions and windows"},
			Binding{Key: "M-a", Table: TableRoot, Action: ActionAgents, Group: GroupTools, Help: "Agents"},
			Binding{Key: "M-g", Table: TableRoot, Action: ActionReview, Group: GroupTools, Help: "Review changes"},
			Binding{Key: "M-s", Table: TableRoot, Action: ActionScratch, Group: GroupTools, Help: "Scratch shell"},
			Binding{Key: "M-Space", Table: TableRoot, Action: ActionMenu, Group: GroupTools, Help: "Menu"},
		)
	}
	prefix := o.Prefix
	if prefix == "" {
		prefix = "C-b"
	}
	// The prefix table carries every action the Alt keys carry, because Alt
	// reaches tmux only on a terminal configured to send Option as Meta, and
	// most terminals are not. Keys that tmux binds itself keep the meaning
	// tmux gives them (x closes, z zooms, c opens a window, w chooses, o
	// moves on) so the habit a tmux user already has keeps working, with this
	// workspace's own prompt and help behind it.
	out = append(out,
		Binding{Key: prefix, Table: TablePrefix, Action: ActionSendPrefix, Group: GroupSession, Help: "Send the prefix key to the pane"},
		Binding{Key: "o", Table: TablePrefix, Action: ActionPaneNext, Group: GroupPanes, Help: "Focus next pane"},
		Binding{Key: ";", Table: TablePrefix, Action: ActionPanePrev, Group: GroupPanes, Help: "Focus previous pane"},
		Binding{Key: "H", Table: TablePrefix, Action: ActionResizeLeft, Group: GroupPanes, Help: "Resize pane left"},
		Binding{Key: "L", Table: TablePrefix, Action: ActionResizeRight, Group: GroupPanes, Help: "Resize pane right"},
		Binding{Key: "K", Table: TablePrefix, Action: ActionResizeUp, Group: GroupPanes, Help: "Resize pane up"},
		Binding{Key: "J", Table: TablePrefix, Action: ActionResizeDown, Group: GroupPanes, Help: "Resize pane down"},
		Binding{Key: `\`, Table: TablePrefix, Action: ActionSplitRight, Group: GroupPanes, Help: "Split right"},
		Binding{Key: "-", Table: TablePrefix, Action: ActionSplitDown, Group: GroupPanes, Help: "Split down"},
		Binding{Key: "z", Table: TablePrefix, Action: ActionZoom, Group: GroupPanes, Help: "Zoom pane"},
		Binding{Key: "x", Table: TablePrefix, Action: ActionClosePane, Group: GroupPanes, Help: "Close pane"},
		Binding{Key: "C", Table: TablePrefix, Action: ActionFocusClaude, Group: GroupPanes, Help: "Focus Claude pane"},
		Binding{Key: "c", Table: TablePrefix, Action: ActionNewWindow, Group: GroupWindows, Help: "New window"},
	)
	for i := 1; i <= 9; i++ {
		n := strconv.Itoa(i)
		out = append(out, Binding{Key: n, Table: TablePrefix, Action: ActionWindow, Arg: n, Group: GroupWindows, Help: "Window " + n})
	}
	out = append(out,
		Binding{Key: "w", Table: TablePrefix, Action: ActionTree, Group: GroupWindows, Help: "Sessions and windows"},
		Binding{Key: "a", Table: TablePrefix, Action: ActionAgents, Group: GroupTools, Help: "Agents"},
		Binding{Key: "g", Table: TablePrefix, Action: ActionReview, Group: GroupTools, Help: "Review changes"},
		Binding{Key: "S", Table: TablePrefix, Action: ActionScratch, Group: GroupTools, Help: "Scratch shell"},
		Binding{Key: "Space", Table: TablePrefix, Action: ActionMenu, Group: GroupTools, Help: "Menu"},
		Binding{Key: "r", Table: TablePrefix, Action: ActionReload, Group: GroupSession, Help: "Reload configuration"},
		Binding{Key: "d", Table: TablePrefix, Action: ActionDetach, Group: GroupSession, Help: "Detach"},
	)
	return out
}

// Reserved is a key Claude Code binds by default, in tmux key notation.
type Reserved struct {
	Key    string
	Claude string // what the key does in Claude Code
}

// ClaudeReserved lists the default Claude Code key bindings that a terminal
// delivers as distinct keys, taken from the default keybinding table of
// Claude Code 2.1.x and its prompt editor. Binding any of them in the tmux
// root table would swallow the key before Claude sees it.
var ClaudeReserved = []Reserved{
	{"C-c", "interrupt"},
	{"C-d", "exit"},
	{"C-t", "toggle todos"},
	{"C-o", "toggle transcript"},
	{"C-r", "search history"},
	{"C-l", "clear input"},
	{"C-x", "chord prefix (kill agents, queue submit, external editor)"},
	{"C-j", "newline"},
	{"C-_", "undo"},
	{"C-g", "external editor"},
	{"C-s", "stash prompt"},
	{"C-v", "paste image"},
	{"C-b", "background task, page up in transcript"},
	{"C-e", "show all in transcript"},
	{"C-u", "half page up"},
	{"C-f", "page down in transcript"},
	{"C-n", "next item"},
	{"C-p", "previous item"},
	{"C-]", "open artifact"},
	{"M-p", "model picker"},
	{"M-o", "fast mode"},
	{"M-t", "thinking toggle"},
	{"M-w", "workflow keyword toggle"},
	{"M-j", "toggle terminal"},
	{"M-m", "cycle permission mode (Windows)"},
	{"M-v", "paste image (Windows)"},
	{"M-Up", "previous file in diff list"},
	{"M-Down", "next file in diff list"},
	{"M-b", "word left"},
	{"M-f", "word right"},
	{"M-d", "delete word"},
	{"M-y", "yank"},
	{"M-BSpace", "delete word left"},
	{"M-Left", "word left"},
	{"M-Right", "word right"},
	{"M-Enter", "newline"},
	{"BTab", "cycle permission mode"},
	{"S-Enter", "newline"},
	{"Enter", "submit"},
	{"Escape", "cancel"},
	{"Tab", "accept completion"},
	{"Up", "previous history"},
	{"Down", "next history"},
	{"Space", "push to talk"},
	{"PageUp", "scroll up"},
	{"PageDown", "scroll down"},
}

// Normalize returns the canonical spelling of a tmux key name: modifiers in
// C, M, S order and a named key spelled as tmux prints it. "S-M-left" and
// "M-S-Left" both become "M-S-Left". Single printable characters keep their
// case because tmux distinguishes M-h from M-H.
func Normalize(key string) string {
	mods := map[byte]bool{}
	rest := key
	for len(rest) > 2 && rest[1] == '-' && strings.IndexByte("CMScms", rest[0]) >= 0 {
		mods[upper(rest[0])] = true
		rest = rest[2:]
	}
	if len(rest) > 1 {
		for _, name := range namedKeys {
			if strings.EqualFold(rest, name) {
				rest = name
				break
			}
		}
	}
	var b strings.Builder
	for _, m := range []byte{'C', 'M', 'S'} {
		if mods[m] {
			b.WriteByte(m)
			b.WriteByte('-')
		}
	}
	b.WriteString(rest)
	return b.String()
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

var namedKeys = []string{
	"Up", "Down", "Left", "Right", "Home", "End", "PageUp", "PageDown", "Enter", "Escape",
	"Tab", "BTab", "BSpace", "Space", "IC", "DC",
	"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12",
}

// Conflict is a problem in a set of bindings.
type Conflict struct {
	Binding Binding
	// Reserved is set when the binding takes a Claude Code key.
	Reserved *Reserved
	// Duplicate is set when another binding uses the same key in the same table.
	Duplicate *Binding
}

func (c Conflict) String() string {
	if c.Reserved != nil {
		return fmt.Sprintf("%s (%s) takes Claude Code's %s key (%s)", c.Binding.Key, c.Binding.Help, c.Reserved.Key, c.Reserved.Claude)
	}
	return fmt.Sprintf("%s is bound twice in the %s table (%s, %s)", c.Binding.Key, c.Binding.Table, c.Duplicate.Help, c.Binding.Help)
}

// Conflicts reports root-table bindings on Claude Code keys and keys bound
// twice in one table. A prefix-table binding never conflicts with Claude: the
// prefix already consumed the keystroke.
func Conflicts(bindings []Binding) []Conflict {
	reserved := make(map[string]*Reserved, len(ClaudeReserved))
	for i := range ClaudeReserved {
		reserved[Normalize(ClaudeReserved[i].Key)] = &ClaudeReserved[i]
	}
	var out []Conflict
	seen := map[string]int{}
	for i, b := range bindings {
		k := Normalize(b.Key)
		if b.Table == TableRoot {
			if r, ok := reserved[k]; ok {
				out = append(out, Conflict{Binding: b, Reserved: r})
			}
		}
		id := string(b.Table) + "\x00" + k
		if j, dup := seen[id]; dup {
			out = append(out, Conflict{Binding: b, Duplicate: &bindings[j]})
			continue
		}
		seen[id] = i
	}
	return out
}

// IsReserved reports whether key is a Claude Code key.
func IsReserved(key string) bool {
	k := Normalize(key)
	return slices.ContainsFunc(ClaudeReserved, func(r Reserved) bool { return Normalize(r.Key) == k })
}

// humanNames spells tmux key names the way keyboards label them.
var humanNames = map[string]string{
	"BTab":   "Shift+Tab",
	"BSpace": "Backspace",
	"Escape": "Esc",
	"DC":     "Delete",
	"IC":     "Insert",
}

// Human renders a tmux key name for people: "M-S-Left" becomes
// "Alt+Shift+Left" and "C-b" becomes "Ctrl+b". Character keys keep their case,
// since tmux tells M-h and M-H apart.
func Human(key string) string {
	k := Normalize(key)
	var b strings.Builder
	for len(k) > 2 && k[1] == '-' {
		switch k[0] {
		case 'C':
			b.WriteString("Ctrl+")
		case 'M':
			b.WriteString("Alt+")
		case 'S':
			b.WriteString("Shift+")
		}
		k = k[2:]
	}
	if name, ok := humanNames[k]; ok {
		k = name
	}
	b.WriteString(k)
	return b.String()
}
