package hook

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
)

const (
	// PluginName is the name of the Claude Code plugin and of its marketplace.
	PluginName = "lyna-tmux"
	// PluginBinary is the command the plugin's hooks look up on PATH. Users of
	// the plugin install lyna-tmux themselves; the plugin never ships a binary.
	PluginBinary = "lyna-tmux"
	// PluginHooksPath is where hooks.json lives, relative to the repository root.
	PluginHooksPath = "plugins/lyna-tmux/hooks/hooks.json"

	pluginHooksDescription = "Mirror Claude Code agent state (busy, waiting, idle, subagents, branch) onto the tmux pane it runs in and ring the tmux bell when the agent needs you. Requires lyna-tmux in an absolute PATH directory; does nothing outside tmux or inside panes lyna-tmux launched itself."
)

// PluginCommand is the shell-form command a plugin hook runs for ev. It runs
// the handler only when every PATH element is absolute and the binary resolves
// to an absolute path, and otherwise exits 0 without output, so installing the
// plugin before the binary never produces hook errors. The event name is one
// of the fixed hookevent constants.
//
// The PATH test is the point. A PATH holding ".", an empty element or any
// relative element resolves the name against the working directory, which is
// the repository Claude Code was started in, so a checkout that ships a
// lyna-tmux file would have every hook event run it with the session
// environment. The resolved path is checked as well, for the shells that
// report a relative hit as it stands; bash reports it already joined to the
// working directory, which is why the PATH itself has to be the test. The rest
// of lyna-tmux refuses a relative PATH hit the same way.
//
// ":$PATH:" puts a colon on both ends, so every element is preceded by one:
// an element that does not start with "/" is then a colon followed by some
// other character, empty elements ("::") included.
func PluginCommand(ev hookevent.Event) string {
	return `case ":$PATH:" in *:[!/]*) exit 0;; esac; ` +
		"bin=$(command -v " + PluginBinary + " 2>/dev/null) || exit 0; " +
		`case "$bin" in /*) ;; *) exit 0;; esac; ` +
		`exec "$bin" hook --plugin ` + string(ev)
}

type pluginHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Async keeps the hook off Claude Code's turn, as in the per-launch settings.
	Async bool `json:"async"`
}

type pluginGroup struct {
	Matcher string          `json:"matcher,omitempty"`
	Hooks   []pluginHandler `json:"hooks"`
}

// PluginHooks renders the plugin's hooks/hooks.json: one asynchronous command
// hook per hookevent.Registrations entry, events in registration order.
func PluginHooks() ([]byte, error) {
	var order []hookevent.Event
	groups := map[hookevent.Event][]pluginGroup{}
	for _, r := range hookevent.Registrations() {
		if _, seen := groups[r.Event]; !seen {
			order = append(order, r.Event)
		}
		groups[r.Event] = append(groups[r.Event], pluginGroup{
			Matcher: r.Matcher,
			Hooks:   []pluginHandler{{Type: "command", Command: PluginCommand(r.Event), Async: true}},
		})
	}

	// encoding/json sorts map keys; the object is assembled by hand to keep
	// the registration order readers of the file expect.
	var buf bytes.Buffer
	desc, err := marshal(pluginHooksDescription)
	if err != nil {
		return nil, err
	}
	buf.WriteString(`{"description":`)
	buf.Write(desc)
	buf.WriteString(`,"hooks":{`)
	for i, ev := range order {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := marshal(string(ev))
		if err != nil {
			return nil, err
		}
		value, err := marshal(groups[ev])
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteString("}}")

	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, fmt.Errorf("hook: render plugin hooks: %w", err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// marshal encodes v without HTML escaping, so shell redirections in hook
// commands stay readable in the committed file.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
