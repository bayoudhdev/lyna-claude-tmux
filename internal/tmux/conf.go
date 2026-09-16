package tmux

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

// ConfOptions describe the generated tmux configuration of the managed server.
type ConfOptions struct {
	Version Version
	Look    Look
	Env     Env

	Prefix           string
	Mouse            bool
	AllowPassthrough bool
	Bell             bool
	StatusPosition   string // top or bottom
	HistoryLimit     int
	// Shell is the default shell of new panes; empty keeps tmux's choice ($SHELL).
	Shell string
	// LocalConf is sourced last when it exists, so user settings win.
	LocalConf string
}

type confWriter struct {
	b strings.Builder
}

// comment writes a comment line. Control characters are dropped so data in a
// comment (a path) cannot end the line and inject configuration.
func (w *confWriter) comment(s string) {
	w.b.WriteString("# ")
	w.b.WriteString(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	w.b.WriteByte('\n')
}

func (w *confWriter) blank() { w.b.WriteByte('\n') }

func (w *confWriter) seq(s Seq) {
	w.b.WriteString(s.Line())
	w.b.WriteByte('\n')
}

// bind writes a key binding whose body is the rest of the line, so body
// commands are separated by escaped semicolons and belong to the binding.
// flag is -T (with table) or -n (table empty).
func (w *confWriter) bind(flag, table, key string, body Seq) {
	head := Command{"bind-key", flag}
	if table != "" {
		head = append(head, table)
	}
	w.b.WriteString(Seq{append(head, key)}.Line())
	w.b.WriteByte(' ')
	w.seq(body)
}

// set writes set-option with flags such as -g, -s, -wg.
func (w *confWriter) set(flags, name, value string) {
	w.seq(Cmd("set-option", flags, name, value))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// GenerateConf renders the configuration of the lyna-tmux server. Every line
// exists in tmux 3.3; newer options are emitted only for versions providing them.
func GenerateConf(o ConfOptions) string {
	v := o.Version
	l := o.Look
	l.Buttons = v.Has(FeatureUserRanges)
	p := l.Palette
	w := &confWriter{}

	w.comment("lyna-tmux generated configuration for tmux " + v.String() + ". Do not edit: it is rewritten.")
	if o.LocalConf != "" {
		w.comment("Put your own settings in " + o.LocalConf + " (sourced at the end).")
	}
	w.blank()

	w.comment("terminal")
	w.set("-s", "default-terminal", "tmux-256color")
	// Array indexes keep these idempotent when the file is sourced again.
	w.set("-s", "terminal-features[90]", "*:extkeys")
	if l.Depth == theme.DepthTrue {
		w.set("-s", "terminal-features[91]", "*:RGB")
	}
	w.set("-s", "extended-keys", "on")
	if v.Has(FeatureExtendedKeysFormat) {
		w.set("-s", "extended-keys-format", "csi-u")
	}
	w.set("-s", "escape-time", "10")
	w.set("-s", "focus-events", "on")
	// Only copy mode writes the clipboard; programs in panes cannot.
	w.set("-s", "set-clipboard", "external")
	w.blank()

	w.comment("behavior")
	w.set("-g", "history-limit", strconv.Itoa(o.HistoryLimit))
	w.set("-g", "base-index", "1")
	w.set("-wg", "pane-base-index", "1")
	w.set("-g", "renumber-windows", "on")
	w.set("-g", "mouse", onOff(o.Mouse))
	w.set("-g", "status-interval", "5")
	w.set("-g", "display-time", "2000")
	w.set("-g", "set-titles", "on")
	w.set("-g", "set-titles-string", "#S: #W")
	w.set("-wg", "allow-rename", "off")
	w.set("-wg", "allow-passthrough", onOff(o.AllowPassthrough))
	if v.Has(FeatureAllowSetTitle) {
		w.set("-wg", "allow-set-title", "off")
	}
	w.set("-wg", "aggressive-resize", "on")
	w.set("-wg", "monitor-bell", onOff(o.Bell))
	w.set("-g", "bell-action", "any")
	w.set("-g", "visual-bell", "off")
	prefix := o.Prefix
	if prefix == "" {
		prefix = "C-b"
	}
	w.set("-g", "prefix", prefix)
	w.set("-g", "prefix2", "None")
	if o.Shell != "" {
		w.set("-g", "default-shell", o.Shell)
	}
	w.blank()

	w.comment("look")
	position := o.StatusPosition
	if position != "top" {
		position = "bottom"
	}
	w.set("-g", "status-position", position)
	w.set("-g", "status-justify", "left")
	w.set("-g", "status-style", "bg="+l.c(p.Bg)+",fg="+l.c(p.Text))
	w.set("-g", "status-left-length", strconv.Itoa(session.MaxNameLen+StatusLeftFixed))
	w.set("-g", "status-right-length", "160")
	w.set("-g", "status-left", l.StatusLeft())
	w.set("-g", "status-right", l.StatusRight())
	w.set("-wg", "window-status-format", l.WindowFormat())
	w.set("-wg", "window-status-current-format", l.WindowCurrentFormat())
	w.set("-wg", "window-status-separator", "")
	w.set("-wg", "window-status-bell-style", "fg="+l.c(p.Waiting)+",bold")
	w.set("-g", "message-style", "bg="+l.c(p.Surface)+",fg="+l.c(p.Text))
	w.set("-g", "message-command-style", "bg="+l.c(p.Surface)+",fg="+l.c(p.Accent))
	w.set("-wg", "mode-style", "bg="+l.c(p.Overlay)+",fg="+l.c(p.Text))
	w.set("-wg", "pane-border-style", "fg="+l.c(p.Border))
	w.set("-wg", "pane-active-border-style", "fg="+l.c(p.Accent))
	w.set("-wg", "pane-border-lines", "heavy")
	w.set("-wg", "pane-border-indicators", "arrows")
	w.set("-wg", "pane-border-status", "top")
	w.set("-wg", "pane-border-format", l.BorderFormat())
	w.set("-wg", "popup-style", "bg="+l.c(p.Bg)+",fg="+l.c(p.Text))
	w.set("-wg", "popup-border-style", "fg="+l.c(p.Accent))
	w.set("-wg", "popup-border-lines", "rounded")
	w.set("-wg", "clock-mode-color", l.c(p.Accent))
	w.set("-g", "display-panes-active-color", l.c(p.Accent))
	w.set("-g", "display-panes-color", l.c(p.Muted))
	if v.Has(FeatureMenuStyles) {
		w.set("-g", "menu-style", "bg="+l.c(p.Surface)+",fg="+l.c(p.Text))
		w.set("-g", "menu-selected-style", "bg="+l.c(p.Accent)+",fg="+l.c(p.Bg))
		w.set("-g", "menu-border-style", "fg="+l.c(p.Accent))
		w.set("-g", "menu-border-lines", "rounded")
	}
	if v.Has(FeaturePaneScrollbars) {
		w.set("-wg", "pane-scrollbars", "modal")
		w.set("-wg", "pane-scrollbars-style", "bg="+l.c(p.Surface)+",fg="+l.c(p.Accent))
	}
	w.blank()

	w.comment("commands shared by menus and the status line")
	for _, r := range o.Env.Registry() {
		w.set("-g", DoOption(r.Name), r.Seq.String())
	}
	w.blank()

	w.comment("keys")
	for _, b := range o.Env.Bindings {
		w.bind("-T", string(b.Table), b.Key, o.Env.ActionSeq(b))
	}
	w.blank()

	w.comment("mouse")
	if l.Buttons {
		w.bind("-n", "", "MouseDown1Status", ClickSeq())
	}
	w.bind("-n", "", "MouseDown1StatusLeft", DoSeq(DoMenuSession))
	w.bind("-n", "", "MouseDown3StatusLeft", DoSeq(DoMenuSession))
	w.bind("-n", "", "MouseDown3Status", DoSeq(DoMenuWindow))
	// A program that asked for mouse events (Claude's fullscreen renderer, an
	// editor) keeps receiving right clicks; Alt+right click always opens the menu.
	selectMouse := Cmd("select-pane", "-t", "=")
	w.bind("-n", "", "MouseDown3Pane", Cmd("if-shell", "-F", "-t", "=", "#{mouse_any_flag}",
		selectMouse.Then(Cmd("send-keys", "-M", "-t", "=")).String(),
		selectMouse.Then(DoSeq(DoMenuPane)).String()))
	w.bind("-n", "", "M-MouseDown3Pane", selectMouse.Then(DoSeq(DoMenuPane)))
	w.blank()

	if o.LocalConf != "" {
		w.comment("user settings")
		w.seq(Cmd("source-file", "-q", GlobEscape(o.LocalConf)))
	}
	return w.b.String()
}

// withMouseTarget makes a display-menu open for the pane or window under the
// mouse, so its commands act there rather than on the active pane.
func withMouseTarget(s Seq) Seq {
	out := make(Seq, len(s))
	for i, c := range s {
		if len(c) > 0 && c[0] == "display-menu" {
			c = append(Command{"display-menu", "-t", "="}, c[1:]...)
		}
		out[i] = c
	}
	return out
}

// ConfFingerprint identifies a generated configuration so a running server is
// only re-sourced when the file content changed.
func ConfFingerprint(conf string) string {
	sum := sha256.Sum256([]byte(conf))
	return hex.EncodeToString(sum[:8])
}
