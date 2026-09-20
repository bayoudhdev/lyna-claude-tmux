package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/session"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

var choices = map[string][]string{
	// The palettes of internal/domain/theme in the order the template and
	// the theme listing document them: the default first, then the dark
	// sets, the light sets, and the one that borrows the terminal's colors.
	"ui.theme": {
		"monokai", "lyna", "slate", "dusk", "contrast", "nord", "rose", "mono", "solar-dark",
		"earth-dark", "light", "solar-light", "earth-light", "ansi",
	},
	"ui.icons":               {"auto", "unicode", "nerd", "ascii"},
	"ui.color":               {"auto", "truecolor", "256", "16"},
	"ui.status_style":        {theme.StatusAuto, theme.StatusPowerline, theme.StatusPlain},
	"ui.status_position":     {"top", "bottom"},
	"ui.agents_sidebar":      {SidebarAuto, SidebarAlways, SidebarKey, SidebarOff},
	"workspace.layout":       layout.Names(),
	"claude.effort":          {"low", "medium", "high", "xhigh", "max", "ultracode"},
	"claude.permission_mode": {"default", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"},
	"claude.statusline":      {"auto", "lyna", "off"},
	"claude.teammate_mode":   claudecfg.TeammateModes(),
	"claude.worktree_base":   {"fresh", "head"},
	"claude.workflow_size":   {"small", "medium", "large", "unrestricted"},
	"sandbox.profile":        {"standard", "strict", "off"},
	"sandbox.isolation":      {"bash", "process", "container"},
	"layouts.panes.role":     layout.Roles(),
	"layouts.panes.split":    {"right", "down"},
	"review.editor":          {string(review.EditorIsolated), string(review.EditorUser)},
	"review.layout":          {"default", string(review.LayoutInline), string(review.LayoutSideBySide)},
}

// Keys where the empty string means "let Claude decide".
var optionalChoices = map[string]bool{
	"claude.effort":          true,
	"claude.permission_mode": true,
	"claude.worktree_base":   true,
	"claude.workflow_size":   true,
}

const (
	maxPanes      = 9
	maxListLen    = 256
	maxValueBytes = 4096
)

var (
	// tmux key names: optional C-, M-, S- modifiers, then one printable ASCII
	// character or a named key.
	keyPattern = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^(?:[CMS]-){0,3}(?:[!-~]|Space|Tab|Enter|Escape|BSpace|Home|End|PageUp|PageDown|F(?:[1-9]|1[0-2]))$`)
	})
	// Model ids, aliases and provider ARNs.
	modelPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`) })
	// Hostnames with an optional leading wildcard label.
	domainPattern = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^(?:\*\.)?(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	})
	// Custom layout names.
	layoutNamePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`) })
	// Popup sizes: a percentage or a cell count.
	sizePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^([0-9]{1,4})(%?)$`) })
)

// Problem is one invalid value.
type Problem struct {
	Key     string
	Message string
}

func (p Problem) String() string { return p.Key + ": " + p.Message }

// Problems lists every invalid value of a configuration.
type Problems []Problem

func (p Problems) Error() string {
	lines := make([]string, len(p))
	for i, pr := range p {
		lines[i] = pr.String()
	}
	return "invalid config: " + strings.Join(lines, "; ")
}

type validator struct{ problems Problems }

func (v *validator) add(key, format string, args ...any) {
	v.problems = append(v.problems, Problem{Key: key, Message: fmt.Sprintf(format, args...)})
}

// Validate checks every value and reports all problems at once.
func (c Config) Validate() error {
	v := &validator{}

	v.choice("ui.theme", c.UI.Theme)
	v.choice("ui.icons", c.UI.Icons)
	v.choice("ui.color", c.UI.Color)
	v.choice("ui.status_style", c.UI.StatusStyle)
	v.choice("ui.status_position", c.UI.StatusPosition)
	v.choice("ui.agents_sidebar", c.UI.AgentsSidebar)
	if w := c.UI.SidebarWidth; w < layout.MinRailWidth || w > layout.MaxRailWidth {
		v.add("ui.sidebar_width", "must be between %d and %d cells (got %d)", layout.MinRailWidth, layout.MaxRailWidth, w)
	}

	c.validateWorkspace(v)
	c.validateClaude(v)

	v.choice("sandbox.profile", c.Sandbox.Profile)
	v.choice("sandbox.isolation", c.Sandbox.Isolation)
	v.paths("sandbox.allow_write", c.Sandbox.AllowWrite)
	v.paths("sandbox.deny_read", c.Sandbox.DenyRead)
	v.list("sandbox.allowed_domains", c.Sandbox.AllowedDomains, func(s string) string {
		if len(s) > 253 || !domainPattern().MatchString(s) {
			return "must be a hostname such as registry.npmjs.org or *.example.com"
		}
		return ""
	})
	v.list("sandbox.excluded_commands", c.Sandbox.ExcludedCommands, func(string) string { return "" })

	v.choice("review.editor", c.Review.Editor)
	v.choice("review.layout", c.Review.Layout)

	v.popupSize("popup.width", c.Popup.Width)
	v.popupSize("popup.height", c.Popup.Height)
	if p := c.Popup.SessionPrefix; p == "" || len(p) > 16 || session.Validate(p+"00000000") != nil {
		v.add("popup.session_prefix", "must be 1-16 characters of letters, digits, '_' or '-', not starting with '-' (got %q)", p)
	}

	names := make([]string, 0, len(c.Layouts))
	for name := range c.Layouts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		validateLayout(v, name, c.Layouts[name])
	}

	if len(v.problems) == 0 {
		return nil
	}
	return v.problems
}

func (c Config) validateWorkspace(v *validator) {
	w := c.Workspace
	if _, custom := c.Layouts[w.Layout]; !custom && !layout.IsBuiltin(w.Layout) {
		v.add("workspace.layout", "must be one of %s or a [layouts.<name>] table (got %q)", strings.Join(layout.Names(), ", "), w.Layout)
	}
	if w.SplitRatio < 20 || w.SplitRatio > 80 {
		v.add("workspace.split_ratio", "must be between 20 and 80 (got %d)", w.SplitRatio)
	}
	if w.AgentPanes < 0 || w.AgentPanes > layout.MaxAgentPanes {
		v.add("workspace.agent_panes", "must be between 0 and %d (got %d)", layout.MaxAgentPanes, w.AgentPanes)
	}
	if w.HistoryLimit < 1000 || w.HistoryLimit > 2000000 {
		v.add("workspace.history_limit", "must be between 1000 and 2000000 (got %d)", w.HistoryLimit)
	}
	if !keyPattern().MatchString(w.Prefix) {
		v.add("workspace.prefix", "must be a tmux key such as C-b, C-a or C-Space (got %q)", w.Prefix)
	}
	if w.Shell != "" && (!strings.HasPrefix(w.Shell, "/") || !CleanText(w.Shell)) {
		v.add("workspace.shell", "must be an absolute path or empty for $SHELL (got %q)", w.Shell)
	}
}

func (c Config) validateClaude(v *validator) {
	cl := c.Claude
	if msg := ClaudeCommandProblem(cl.Command); msg != "" {
		v.add("claude.command", "%s (got %q)", msg, cl.Command)
	}
	v.list("claude.args", cl.Args, func(string) string { return "" })
	if cl.Model != "" && !modelPattern().MatchString(cl.Model) {
		v.add("claude.model", "must be a model alias or id such as opus or claude-opus-5 (got %q)", cl.Model)
	}
	v.choice("claude.effort", cl.Effort)
	v.choice("claude.permission_mode", cl.PermissionMode)
	v.choice("claude.statusline", cl.Statusline)
	v.choice("claude.teammate_mode", cl.TeammateMode)
	v.choice("claude.worktree_base", cl.WorktreeBase)
	v.choice("claude.workflow_size", cl.WorkflowSize)
	v.paths("claude.add_dirs", cl.AddDirs)
	v.paths("claude.mcp_config", cl.MCPConfig)
	v.paths("claude.plugin_dirs", cl.PluginDirs)
}

func validateLayout(v *validator, name string, l Layout) {
	key := "layouts." + name
	if !layoutNamePattern().MatchString(name) {
		v.add(key, "layout names use lowercase letters, digits, '_' and '-', up to 32 characters")
	}
	if layout.IsBuiltin(name) {
		v.add(key, "%q is a built-in layout and cannot be redefined", name)
	}
	if len(l.Panes) == 0 || len(l.Panes) > maxPanes {
		v.add(key+".panes", "must list between 1 and %d panes (got %d)", maxPanes, len(l.Panes))
		return
	}
	claudePanes := 0
	for i, p := range l.Panes {
		pk := key + ".panes[" + strconv.Itoa(i+1) + "]"
		if p.Role == "claude" {
			claudePanes++
		}
		if !slices.Contains(choices["layouts.panes.role"], p.Role) {
			v.add(pk+".role", "must be one of %s (got %q)", strings.Join(choices["layouts.panes.role"], ", "), p.Role)
		}
		if i == 0 {
			if p.Split != "" || p.Size != 0 || p.Parent != 0 {
				v.add(pk, "the first pane is the window itself and takes no split, size or parent")
			}
		} else {
			if !slices.Contains(choices["layouts.panes.split"], p.Split) {
				v.add(pk+".split", "must be right or down (got %q)", p.Split)
			}
			if p.Size != 0 && (p.Size < 10 || p.Size > 90) {
				v.add(pk+".size", "must be between 10 and 90, or omitted for an even split (got %d)", p.Size)
			}
			if p.Parent < 0 || p.Parent > i {
				v.add(pk+".parent", "must name an earlier pane, 1 to %d (got %d)", i, p.Parent)
			}
		}
		switch {
		case p.Role == "command" && (strings.TrimSpace(p.Command) == "" || !CleanText(p.Command)):
			v.add(pk+".command", "a command pane needs a single-line command")
		case p.Role != "command" && p.Command != "":
			v.add(pk+".command", "only command panes take a command")
		}
		if p.Worktree && p.Role != "claude" {
			v.add(pk+".worktree", "only claude panes can run in a worktree")
		}
	}
	if claudePanes == 0 {
		v.add(key+".panes", "needs at least one claude pane")
	}
}

func (v *validator) choice(key, value string) {
	if value == "" && optionalChoices[key] {
		return
	}
	if !slices.Contains(choices[key], value) {
		v.add(key, "must be one of %s (got %q)", strings.Join(choices[key], ", "), value)
	}
}

func (v *validator) popupSize(key, value string) {
	m := sizePattern().FindStringSubmatch(value)
	if m == nil {
		v.add(key, "must be a percentage such as 90%% or a cell count such as 120 (got %q)", value)
		return
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "%" && (n < 10 || n > 100) {
		v.add(key, "percentage must be between 10%% and 100%% (got %q)", value)
	}
	if m[2] == "" && (n < 20 || n > 1000) {
		v.add(key, "cell count must be between 20 and 1000 (got %q)", value)
	}
}

// paths accepts absolute paths and paths under the home directory ("~/").
func (v *validator) paths(key string, values []string) {
	v.list(key, values, func(s string) string {
		if !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~/") {
			return "must be an absolute path or start with ~/"
		}
		return ""
	})
}

func (v *validator) list(key string, values []string, check func(string) string) {
	if len(values) > maxListLen {
		v.add(key, "lists at most %d entries (got %d)", maxListLen, len(values))
		return
	}
	for i, s := range values {
		ik := key + "[" + strconv.Itoa(i+1) + "]"
		if s == "" || !CleanText(s) {
			v.add(ik, "must be a non-empty single-line value without control characters")
			continue
		}
		if msg := check(s); msg != "" {
			v.add(ik, "%s (got %q)", msg, s)
		}
	}
}

// ClaudeCommandProblem reports why command cannot name the Claude executable,
// or "" when it can: one clean word that is a name looked up on PATH or an
// absolute path. An empty command is the default and never a problem. It is
// the rule behind claude.command, exported so a command that reaches a launch
// from elsewhere, such as a tmux option in plugin mode, is held to the same
// one.
func ClaudeCommandProblem(command string) string {
	if command != "" && (!CleanText(command) || strings.ContainsAny(command, " \t") ||
		(strings.Contains(command, "/") && !strings.HasPrefix(command, "/"))) {
		return "must be a command name on PATH or an absolute path"
	}
	return ""
}

// CleanText reports whether s is bounded, valid UTF-8 text without control
// characters (tabs included), so it can reach tmux, argv and JSON unchanged.
func CleanText(s string) bool {
	if len(s) > maxValueBytes || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}
