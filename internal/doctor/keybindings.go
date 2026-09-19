package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

// KeybindingsFile is the Claude Code user key binding file inside the Claude
// configuration directory (https://code.claude.com/docs/en/keybindings).
const KeybindingsFile = "keybindings.json"

// maxKeybindingsBytes bounds the read of a user-edited file.
const maxKeybindingsBytes = 1 << 20

// UserBinding is one keystroke the user bound in keybindings.json.
type UserBinding struct {
	Context   string `json:"context"`
	Keystroke string `json:"keystroke"`
	Action    string `json:"action"`
}

// keybindingsDoc is the file format: {"bindings": [{"context": "Chat",
// "bindings": {"ctrl+e": "chat:externalEditor", "ctrl+u": null}}]}.
type keybindingsDoc struct {
	Bindings []struct {
		Context  string             `json:"context"`
		Bindings map[string]*string `json:"bindings"`
	} `json:"bindings"`
}

// ParseKeybindings decodes keybindings.json. Unbound keys (null actions) are
// dropped because they free a key instead of taking one. The result is sorted
// by context, then keystroke.
func ParseKeybindings(data []byte) ([]UserBinding, error) {
	var doc keybindingsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var out []UserBinding
	for _, block := range doc.Bindings {
		for stroke, action := range block.Bindings {
			if action == nil {
				continue
			}
			out = append(out, UserBinding{Context: block.Context, Keystroke: stroke, Action: *action})
		}
	}
	slices.SortFunc(out, func(a, b UserBinding) int {
		if c := strings.Compare(a.Context, b.Context); c != 0 {
			return c
		}
		return strings.Compare(a.Keystroke, b.Keystroke)
	})
	return out, nil
}

// specialKeys maps Claude Code key names to tmux key names.
var specialKeys = map[string]string{
	"escape": "Escape", "esc": "Escape",
	"enter": "Enter", "return": "Enter",
	"tab": "Tab", "space": "Space",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"pageup": "PageUp", "pagedown": "PageDown",
	"home": "Home", "end": "End",
	"backspace": "BSpace", "delete": "DC",
}

// TmuxKey converts one Claude Code keystroke ("alt+a", "ctrl+shift+left") to
// the tmux key name tmux would see. It reports false for keys tmux cannot
// bind: the Command/Super modifier, mouse wheel events and unknown names.
// Claude Code reads letters case-insensitively and spells Shift explicitly, so
// shift plus a letter becomes the upper-case letter, as tmux reports it.
func TmuxKey(stroke string) (string, bool) {
	stroke = strings.TrimSpace(stroke)
	var parts []string
	switch {
	case stroke == "":
		return "", false
	case stroke == "+":
		parts = []string{"+"}
	case strings.HasSuffix(stroke, "++"):
		// "ctrl++" binds the plus key itself.
		parts = append(strings.Split(strings.TrimSuffix(stroke, "++"), "+"), "+")
	default:
		parts = strings.Split(stroke, "+")
	}
	var ctrl, meta, shift bool
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "ctrl", "control":
			ctrl = true
		case "alt", "opt", "option", "meta":
			meta = true
		case "shift":
			shift = true
		default:
			return "", false
		}
	}
	key := parts[len(parts)-1]
	lower := strings.ToLower(key)
	switch {
	case specialKeys[lower] != "":
		key = specialKeys[lower]
	case len(lower) >= 2 && len(lower) <= 3 && lower[0] == 'f' && isDigits(lower[1:]):
		key = "F" + lower[1:]
	case len([]rune(key)) == 1:
		key = lower
		if shift && lower[0] >= 'a' && lower[0] <= 'z' && len(lower) == 1 {
			key = strings.ToUpper(lower)
			shift = false
		}
	default:
		return "", false
	}
	var b strings.Builder
	if ctrl {
		b.WriteString("C-")
	}
	if meta {
		b.WriteString("M-")
	}
	if shift {
		b.WriteString("S-")
	}
	b.WriteString(key)
	return keys.Normalize(b.String()), true
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// Collision is a user key binding tmux takes before Claude Code sees it.
type Collision struct {
	Binding UserBinding `json:"binding"`
	TmuxKey string      `json:"tmux_key"`
	// Taken describes what lyna-tmux does with the key.
	Taken string `json:"taken"`
}

// Collisions reports user bindings whose first keystroke is a lyna-tmux
// root-table key or the tmux prefix. Later keystrokes of a chord are safe:
// once the first key reaches Claude, tmux has no binding for the rest.
func Collisions(bindings []UserBinding, workspace []keys.Binding, prefix string) []Collision {
	taken := map[string]string{}
	if prefix != "" {
		taken[keys.Normalize(prefix)] = "the tmux prefix (press it twice to send it)"
	}
	for _, b := range workspace {
		if b.Table == keys.TableRoot {
			taken[keys.Normalize(b.Key)] = "lyna-tmux " + b.Help
		}
	}
	var out []Collision
	for _, ub := range bindings {
		strokes := strings.Fields(ub.Keystroke)
		if len(strokes) == 0 {
			continue
		}
		key, ok := TmuxKey(strokes[0])
		if !ok {
			continue
		}
		if what, hit := taken[key]; hit {
			out = append(out, Collision{Binding: ub, TmuxKey: key, Taken: what})
		}
	}
	return out
}

func checkKeybindings(_ context.Context, d Deps) []Result {
	r := Result{ID: "keybindings", Title: "Claude keybindings"}
	if d.ClaudeHome == "" {
		r.Status, r.Detail = StatusSkip, "the Claude Code configuration directory is unknown"
		return []Result{r}
	}
	path := filepath.Join(d.ClaudeHome, KeybindingsFile)
	data, err := d.ReadFile(path, maxKeybindingsBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Status, r.Detail = StatusOK, "no "+path+", so Claude Code uses its default keys"
		return []Result{r}
	case err != nil:
		r.Status, r.Detail = StatusWarn, fmt.Sprintf("cannot read %s: %v", path, err)
		r.Fix = "run /keybindings in Claude Code to open and repair the file"
		return []Result{r}
	}
	bindings, err := ParseKeybindings(data)
	if err != nil {
		r.Status, r.Detail = StatusWarn, fmt.Sprintf("cannot parse %s: %v", path, err)
		r.Fix = "run /keybindings in Claude Code to open and repair the file"
		return []Result{r}
	}
	workspace := keys.Defaults(keys.Options{AltKeys: d.AltKeys, Prefix: d.Prefix, NoAgentsRail: d.NoAgentsRail})
	collisions := Collisions(bindings, workspace, d.Prefix)
	if len(collisions) == 0 {
		r.Status, r.Detail = StatusOK, fmt.Sprintf("%d custom bindings in %s, none taken by tmux", len(bindings), path)
		return []Result{r}
	}
	lines := make([]string, 0, len(collisions))
	for _, c := range collisions {
		lines = append(lines, fmt.Sprintf("%s (%s, %s) never reaches Claude: %s is %s",
			c.Binding.Keystroke, c.Binding.Context, c.Binding.Action, c.TmuxKey, c.Taken))
	}
	r.Status = StatusWarn
	r.Detail = strings.Join(lines, "\n")
	r.Fix = "edit " + path + " to use other keys, or set ui.alt_keys = false (and workspace.prefix) in config.toml"
	return []Result{r}
}
