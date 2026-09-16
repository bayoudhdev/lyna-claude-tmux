// Package claudecfg builds what lyna-tmux hands to Claude Code for one launch:
// the per-launch settings document (hooks, status line, sandbox, permissions,
// team and worktree options), the argument vector and environment of the
// claude process, and the project files `lyna-tmux init --project` proposes.
//
// Everything here is pure: callers read and write files. Setting keys and
// flags were checked against the Claude Code documentation and the schema
// and option parser compiled into the claude 2.1.272 binary.
package claudecfg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
)

// ErrInvalid is wrapped by every validation failure of this package.
var ErrInvalid = errors.New("claudecfg: invalid value")

// StatusLineMode decides whether the lyna-tmux status line replaces the user's.
type StatusLineMode string

// Status line modes.
const (
	// StatusLineAuto installs the lyna-tmux status line only when the user has none.
	StatusLineAuto StatusLineMode = "auto"
	// StatusLineLyna always installs the lyna-tmux status line for the session.
	StatusLineLyna StatusLineMode = "lyna"
	// StatusLineOff never installs it.
	StatusLineOff StatusLineMode = "off"
)

// Values accepted for pass-through options. The empty string always means
// "leave it to Claude Code".
var (
	worktreeBaseRefs = []string{"fresh", "head"}
	workflowSizes    = []string{"small", "medium", "large", "unrestricted"}
)

// SettingsInput describes one per-launch settings document.
type SettingsInput struct {
	// Bin is the absolute path of the running lyna-tmux binary, embedded in
	// hook and status line commands.
	Bin string
	// StatusLine is the configured mode; empty means auto.
	StatusLine StatusLineMode
	// UserHasStatusLine is the result of UserHasStatusLine on the user's own
	// settings file; it only matters in auto mode.
	UserHasStatusLine bool
	// Sandbox is the resolved profile; its fragments and environment are written as is.
	Sandbox sandbox.Resolution
	// Env adds entries to the settings "env" block. Keys the sandbox resolution
	// sets cannot be overridden.
	Env map[string]string
	// Teams shows agent team teammates as tmux panes.
	Teams bool
	// WorktreeBaseRef is worktree.baseRef: fresh, head or empty.
	WorktreeBaseRef string
	// WorkflowSize is workflowSizeGuideline: small, medium, large, unrestricted or empty.
	WorkflowSize string
}

type document struct {
	Env                   map[string]string    `json:"env,omitempty"`
	Hooks                 hookTable            `json:"hooks"`
	Permissions           *sandbox.Permissions `json:"permissions,omitempty"`
	Sandbox               sandbox.Settings     `json:"sandbox"`
	StatusLine            *statusLine          `json:"statusLine,omitempty"`
	TeammateMode          string               `json:"teammateMode,omitempty"`
	Worktree              *worktree            `json:"worktree,omitempty"`
	WorkflowSizeGuideline string               `json:"workflowSizeGuideline,omitempty"`
}

type statusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type worktree struct {
	BaseRef string `json:"baseRef"`
}

// hookCommand is one handler. Async hooks run in the background, so a slow
// tmux server never delays a Claude turn.
type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookEvent struct {
	name   string
	groups []hookGroup
}

// hookTable keeps events in registration order; a map would sort them.
type hookTable []hookEvent

// MarshalJSON writes the events as one object in table order.
func (h hookTable) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, ev := range h {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeCompact(&buf, ev.name); err != nil {
			return nil, err
		}
		buf.WriteByte(':')
		if err := encodeCompact(&buf, ev.groups); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// HookCommand is the shell command a settings hook runs for an event.
func HookCommand(bin string, event hookevent.Event) string {
	return shellQuote(bin) + " hook " + string(event)
}

// StatusLineCommand is the shell command of the settings status line.
func StatusLineCommand(bin string) string {
	return shellQuote(bin) + " statusline"
}

// BuildSettings renders the per-launch settings document: deterministic
// bytes, two-space indentation, a trailing newline, no HTML escaping.
func BuildSettings(in SettingsInput) ([]byte, error) {
	if err := checkBin(in.Bin); err != nil {
		return nil, err
	}
	mode, err := ParseStatusLineMode(string(in.StatusLine))
	if err != nil {
		return nil, err
	}
	if err := oneOf("worktree base", in.WorktreeBaseRef, worktreeBaseRefs); err != nil {
		return nil, err
	}
	if err := oneOf("workflow size", in.WorkflowSize, workflowSizes); err != nil {
		return nil, err
	}
	env, err := mergeEnv(in.Sandbox.Env, in.Env)
	if err != nil {
		return nil, err
	}

	doc := document{Env: env, Hooks: hooks(in.Bin), Sandbox: in.Sandbox.Sandbox}
	if perms := in.Sandbox.Permissions; !perms.IsZero() {
		doc.Permissions = &perms
	}
	if mode == StatusLineLyna || (mode == StatusLineAuto && !in.UserHasStatusLine) {
		doc.StatusLine = &statusLine{Type: "command", Command: StatusLineCommand(in.Bin)}
	}
	if in.Teams {
		doc.TeammateMode = "tmux"
	}
	if in.WorktreeBaseRef != "" {
		doc.Worktree = &worktree{BaseRef: in.WorktreeBaseRef}
	}
	doc.WorkflowSizeGuideline = in.WorkflowSize

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("claudecfg: encode settings: %w", err)
	}
	return buf.Bytes(), nil
}

func hooks(bin string) hookTable {
	var table hookTable
	for _, reg := range hookevent.Registrations() {
		group := hookGroup{
			Matcher: reg.Matcher,
			Hooks:   []hookCommand{{Type: "command", Command: HookCommand(bin, reg.Event), Async: true}},
		}
		i := slices.IndexFunc(table, func(e hookEvent) bool { return e.name == string(reg.Event) })
		if i < 0 {
			table = append(table, hookEvent{name: string(reg.Event)})
			i = len(table) - 1
		}
		table[i].groups = append(table[i].groups, group)
	}
	return table
}

// ParseStatusLineMode maps a configuration value to a mode; empty means auto.
func ParseStatusLineMode(s string) (StatusLineMode, error) {
	switch StatusLineMode(s) {
	case "", StatusLineAuto:
		return StatusLineAuto, nil
	case StatusLineLyna, StatusLineOff:
		return StatusLineMode(s), nil
	}
	return "", fmt.Errorf("%w: status line must be auto, lyna or off (got %q)", ErrInvalid, s)
}

// SettingsFileName is the content-addressed file name of a settings document:
// the first 16 hex digits of its SHA-256 and ".json". Identical launches share
// one file and a changed configuration never overwrites a file in use.
func SettingsFileName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]) + ".json"
}

var settingsFileNamePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9a-f]{16}\.json$`) })

// IsSettingsFileName reports whether name has the SettingsFileName shape.
func IsSettingsFileName(name string) bool { return settingsFileNamePattern().MatchString(name) }

// UserHasStatusLine reports whether a user settings document defines a
// statusLine. Empty input means no settings file content. The document is
// only read, never rewritten.
func UserHasStatusLine(settingsJSON []byte) (bool, error) {
	data := bytes.TrimSpace(bytes.TrimPrefix(settingsJSON, []byte("\xef\xbb\xbf")))
	if len(data) == 0 {
		return false, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("claudecfg: read user settings: %w", err)
	}
	if doc == nil {
		return false, fmt.Errorf("%w: user settings must be a JSON object", ErrInvalid)
	}
	raw, ok := doc["statusLine"]
	return ok && string(raw) != "null", nil
}

var envNamePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`) })

func mergeEnv(base, extra map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(base)+len(extra))
	for _, m := range []map[string]string{base, extra} {
		for k, v := range m {
			if !envNamePattern().MatchString(k) {
				return nil, fmt.Errorf("%w: environment variable name %q", ErrInvalid, k)
			}
			if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				return nil, fmt.Errorf("%w: environment variable %s has a NUL byte or invalid UTF-8", ErrInvalid, k)
			}
			if prev, ok := out[k]; ok && prev != v {
				return nil, fmt.Errorf("%w: environment variable %s is set by the sandbox profile and cannot be overridden", ErrInvalid, k)
			}
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func checkBin(bin string) error {
	if !strings.HasPrefix(bin, "/") || !printable(bin) {
		return fmt.Errorf("%w: lyna-tmux binary must be an absolute path without control characters (got %q)", ErrInvalid, bin)
	}
	return nil
}

func oneOf(what, value string, allowed []string) error {
	if value == "" || slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%w: %s must be one of %s (got %q)", ErrInvalid, what, strings.Join(allowed, ", "), value)
}

// printable accepts bounded, valid UTF-8 text without control characters.
func printable(s string) bool {
	return len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func encodeCompact(buf *bytes.Buffer, v any) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	// Encode terminates each value with a newline; the enclosing object must not contain it.
	buf.Truncate(buf.Len() - 1)
	return nil
}
