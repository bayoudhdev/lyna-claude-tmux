package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
	// The rail opens at the width it had before the width was a setting.
	if got := Default().UI.SidebarWidth; got != layout.RailWidth {
		t.Fatalf("ui.sidebar_width defaults to %d, want %d", got, layout.RailWidth)
	}
}

func TestTemplateDecodesToDefault(t *testing.T) {
	cfg, err := Decode(Template())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("template drifted from defaults:\n got %+v\nwant %+v", cfg, Default())
	}
}

// TestTemplateDocumentsChoices keeps the template comments in step with the
// accepted values: the comment block above each enumerated top-level key names
// every value the validator accepts.
func TestTemplateDocumentsChoices(t *testing.T) {
	comments := templateComments(t)
	for key, values := range choices {
		if strings.HasPrefix(key, "layouts.") {
			continue
		}
		t.Run(key, func(t *testing.T) {
			text, ok := comments[key]
			if !ok {
				t.Fatalf("template has no documented %s", key)
			}
			words := strings.FieldsFunc(text, func(r rune) bool {
				return !strings.ContainsRune("_-", r) && !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
			for _, v := range values {
				if !slices.Contains(words, v) {
					t.Errorf("comment for %s does not mention %q:\n%s", key, v, text)
				}
			}
		})
	}
}

// templateComments maps each "section.key" assignment of the template to the
// comment lines directly above it.
func templateComments(t *testing.T) map[string]string {
	t.Helper()
	section := regexp.MustCompile(`^\[([a-z_]+)\]$`)
	assign := regexp.MustCompile(`^([a-z_]+) = `)
	out := map[string]string{}
	current := ""
	var block []string
	for _, line := range strings.Split(string(Template()), "\n") {
		switch {
		case section.MatchString(line):
			current = section.FindStringSubmatch(line)[1]
			block = nil
		case strings.HasPrefix(line, "#"):
			block = append(block, line)
		case assign.MatchString(line) && current != "":
			out[current+"."+assign.FindStringSubmatch(line)[1]] = strings.Join(block, "\n")
			block = nil
		default:
			block = nil
		}
	}
	return out
}

// TestTemplateExamplesAreValid uncomments the documented examples so the
// file never teaches a key or shape that the decoder rejects.
func TestTemplateExamplesAreValid(t *testing.T) {
	example := regexp.MustCompile(`^# ((?:[a-z_]+ = \[.*\])|(?:\[layouts\.[a-z]+\])|(?:panes = \[)|(?:  \{ role.*)|\])$`)
	var b strings.Builder
	uncommented := 0
	for _, line := range strings.Split(string(Template()), "\n") {
		if m := example.FindStringSubmatch(line); m != nil {
			line = m[1]
			uncommented++
		}
		b.WriteString(line + "\n")
	}
	if uncommented < 12 {
		t.Fatalf("only %d example lines found; the pattern no longer matches the template", uncommented)
	}
	cfg, err := Decode([]byte(b.String()))
	if err != nil {
		t.Fatalf("examples do not decode: %v\n%s", err, b.String())
	}
	if len(cfg.Layouts["tests"].Panes) != 3 || len(cfg.Sandbox.AllowedDomains) != 2 || len(cfg.Claude.Args) != 1 {
		t.Fatalf("examples not applied: %+v", cfg)
	}
}

func TestDecodeOverridesDefaults(t *testing.T) {
	cfg, err := Decode([]byte(`
[ui]
theme = "light"
alt_keys = false
sidebar_width = 44

[claude]
model = "claude-opus-5[1m]"
effort = "ultracode"
add_dirs = ["~/shared", "/opt/lib"]

[sandbox]
profile = "strict"
allowed_domains = ["registry.npmjs.org", "*.example.com"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.UI.Theme = "light"
	want.UI.AltKeys = false
	want.UI.SidebarWidth = 44
	want.Claude.Model = "claude-opus-5[1m]"
	want.Claude.Effort = "ultracode"
	want.Claude.AddDirs = []string{"~/shared", "/opt/lib"}
	want.Sandbox.Profile = "strict"
	want.Sandbox.AllowedDomains = []string{"registry.npmjs.org", "*.example.com"}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestDecodeEmptyListsNormalize(t *testing.T) {
	cfg, err := Decode([]byte("[claude]\nargs = []\n[sandbox]\ndeny_read = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("empty lists should equal defaults: %+v", cfg)
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct {
		name     string
		doc      string
		wantLine int
		wantKey  string
		wantMsg  string
	}{
		{"unknown section", "[colors]\nfg = \"red\"\n", 1, "colors", "unknown key"},
		{"unknown key", "[ui]\ntheme = \"lyna\"\nthemes = \"x\"\n", 3, "ui.themes", "unknown key"},
		{"syntax", "[ui\ntheme = 1\n", 1, "", ""},
		{"wrong type", "[ui]\nclock = \"yes\"\n", 2, "", ""},
		{"duplicate key", "[ui]\nclock = true\nclock = false\n", 3, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(tc.doc))
			var derrs DecodeErrors
			if !errors.As(err, &derrs) || len(derrs) == 0 {
				t.Fatalf("err = %v (%T), want DecodeErrors", err, err)
			}
			p := derrs[0]
			if p.Line != tc.wantLine {
				t.Errorf("line = %d, want %d (%v)", p.Line, tc.wantLine, err)
			}
			if tc.wantKey != "" && p.Key != tc.wantKey {
				t.Errorf("key = %q, want %q", p.Key, tc.wantKey)
			}
			if tc.wantMsg != "" && p.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", p.Message, tc.wantMsg)
			}
			if !strings.HasPrefix(err.Error(), "invalid config: line ") {
				t.Errorf("error text %q lacks position", err.Error())
			}
		})
	}
}

func TestDecodeReportsAllUnknownKeys(t *testing.T) {
	_, err := Decode([]byte("[ui]\na = 1\nb = 2\n[popup]\nc = 3\n"))
	var derrs DecodeErrors
	if !errors.As(err, &derrs) {
		t.Fatalf("err = %v", err)
	}
	var keys []string
	for _, p := range derrs {
		keys = append(keys, p.Key)
	}
	if want := []string{"ui.a", "ui.b", "popup.c"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantKey string // empty means valid
	}{
		{"default", func(*Config) {}, ""},
		{"theme", func(c *Config) { c.UI.Theme = "dracula" }, "ui.theme"},
		{"icons", func(c *Config) { c.UI.Icons = "emoji" }, "ui.icons"},
		{"color", func(c *Config) { c.UI.Color = "8" }, "ui.color"},
		{"status position", func(c *Config) { c.UI.StatusPosition = "left" }, "ui.status_position"},
		{"empty theme", func(c *Config) { c.UI.Theme = "" }, "ui.theme"},
		{"sidebar a cell narrower than the narrowest", func(c *Config) { c.UI.SidebarWidth = 19 }, "ui.sidebar_width"},
		{"sidebar the narrowest", func(c *Config) { c.UI.SidebarWidth = 20 }, ""},
		{"sidebar the widest", func(c *Config) { c.UI.SidebarWidth = 60 }, ""},
		{"sidebar a cell wider than the widest", func(c *Config) { c.UI.SidebarWidth = 61 }, "ui.sidebar_width"},
		{"sidebar with no width", func(c *Config) { c.UI.SidebarWidth = 0 }, "ui.sidebar_width"},
		{"sidebar below nothing", func(c *Config) { c.UI.SidebarWidth = -28 }, "ui.sidebar_width"},
		{"review user editor", func(c *Config) { c.Review.Editor = "user" }, ""},
		{"review editor", func(c *Config) { c.Review.Editor = "vim" }, "review.editor"},
		{"review empty editor", func(c *Config) { c.Review.Editor = "" }, "review.editor"},
		{"review inline", func(c *Config) { c.Review.Layout = "inline" }, ""},
		{"review diff layout", func(c *Config) { c.Review.Layout = "unified" }, "review.layout"},
		{"builtin layout", func(c *Config) { c.Workspace.Layout = "quad" }, ""},
		{"review layout", func(c *Config) { c.Workspace.Layout = "review" }, ""},
		{"unknown layout", func(c *Config) { c.Workspace.Layout = "grid" }, "workspace.layout"},
		{"custom layout selected", func(c *Config) {
			c.Workspace.Layout = "pair"
			c.Layouts = map[string]Layout{"pair": {Panes: []Pane{{Role: "claude"}, {Role: "shell", Split: "down"}}}}
		}, ""},
		{"split ratio low", func(c *Config) { c.Workspace.SplitRatio = 19 }, "workspace.split_ratio"},
		{"split ratio high", func(c *Config) { c.Workspace.SplitRatio = 81 }, "workspace.split_ratio"},
		{"split ratio bounds", func(c *Config) { c.Workspace.SplitRatio = 80 }, ""},
		{"history low", func(c *Config) { c.Workspace.HistoryLimit = 999 }, "workspace.history_limit"},
		{"history high", func(c *Config) { c.Workspace.HistoryLimit = 2000001 }, "workspace.history_limit"},
		{"prefix C-a", func(c *Config) { c.Workspace.Prefix = "C-a" }, ""},
		{"prefix C-Space", func(c *Config) { c.Workspace.Prefix = "C-Space" }, ""},
		{"prefix M-S-F12", func(c *Config) { c.Workspace.Prefix = "M-S-F12" }, ""},
		{"prefix word", func(c *Config) { c.Workspace.Prefix = "ctrl-b" }, "workspace.prefix"},
		{"prefix injection", func(c *Config) { c.Workspace.Prefix = "C-b ; run x" }, "workspace.prefix"},
		{"prefix empty", func(c *Config) { c.Workspace.Prefix = "" }, "workspace.prefix"},
		{"shell absolute", func(c *Config) { c.Workspace.Shell = "/bin/zsh" }, ""},
		{"shell relative", func(c *Config) { c.Workspace.Shell = "zsh" }, "workspace.shell"},
		{"shell control char", func(c *Config) { c.Workspace.Shell = "/bin/zsh\n" }, "workspace.shell"},

		{"command name", func(c *Config) { c.Claude.Command = "claude" }, ""},
		{"command absolute", func(c *Config) { c.Claude.Command = "/opt/claude/bin/claude" }, ""},
		{"command relative path", func(c *Config) { c.Claude.Command = "bin/claude" }, "claude.command"},
		{"command with args", func(c *Config) { c.Claude.Command = "claude --verbose" }, "claude.command"},
		{"args control char", func(c *Config) { c.Claude.Args = []string{"--x\x1b[2J"} }, "claude.args[1]"},
		{"args empty entry", func(c *Config) { c.Claude.Args = []string{"--ok", ""} }, "claude.args[2]"},
		{"model alias", func(c *Config) { c.Claude.Model = "opus" }, ""},
		{"model arn", func(c *Config) {
			c.Claude.Model = "arn:aws:bedrock:us-east-1:123:inference-profile/us.anthropic.claude-opus-5"
		}, ""},
		{"model space", func(c *Config) { c.Claude.Model = "opus 5" }, "claude.model"},
		{"model leading dash", func(c *Config) { c.Claude.Model = "--dangerously-skip-permissions" }, "claude.model"},
		{"effort max", func(c *Config) { c.Claude.Effort = "max" }, ""},
		{"effort ultracode", func(c *Config) { c.Claude.Effort = "ultracode" }, ""},
		{"effort bad", func(c *Config) { c.Claude.Effort = "extreme" }, "claude.effort"},
		{"mode manual", func(c *Config) { c.Claude.PermissionMode = "manual" }, ""},
		{"agent panes none", func(c *Config) { c.Workspace.AgentPanes = 0 }, ""},
		{"agent panes as many as a window takes", func(c *Config) { c.Workspace.AgentPanes = layout.MaxAgentPanes }, ""},
		{"agent panes past what a window takes", func(c *Config) { c.Workspace.AgentPanes = layout.MaxAgentPanes + 1 }, "workspace.agent_panes"},
		{"agent panes below none", func(c *Config) { c.Workspace.AgentPanes = -1 }, "workspace.agent_panes"},
		{"mode bypass", func(c *Config) { c.Claude.PermissionMode = "bypassPermissions" }, ""},
		{"mode bad", func(c *Config) { c.Claude.PermissionMode = "yolo" }, "claude.permission_mode"},
		{"statusline empty", func(c *Config) { c.Claude.Statusline = "" }, "claude.statusline"},
		{"teammate mode in process", func(c *Config) { c.Claude.TeammateMode = "in-process" }, ""},
		{"teammate mode empty", func(c *Config) { c.Claude.TeammateMode = "" }, "claude.teammate_mode"},
		{"teammate mode is not the backend name", func(c *Config) { c.Claude.TeammateMode = "tmux" }, "claude.teammate_mode"},
		{"worktree head", func(c *Config) { c.Claude.WorktreeBase = "head" }, ""},
		{"worktree bad", func(c *Config) { c.Claude.WorktreeBase = "main" }, "claude.worktree_base"},
		{"workflow large", func(c *Config) { c.Claude.WorkflowSize = "large" }, ""},
		{"workflow bad", func(c *Config) { c.Claude.WorkflowSize = "huge" }, "claude.workflow_size"},
		{"add dirs relative", func(c *Config) { c.Claude.AddDirs = []string{"../lib"} }, "claude.add_dirs[1]"},
		{"mcp config home", func(c *Config) { c.Claude.MCPConfig = []string{"~/mcp.json"} }, ""},
		{"plugin dirs tilde user", func(c *Config) { c.Claude.PluginDirs = []string{"~bob/x"} }, "claude.plugin_dirs[1]"},

		{"profile off", func(c *Config) { c.Sandbox.Profile = "off" }, ""},
		{"profile bad", func(c *Config) { c.Sandbox.Profile = "none" }, "sandbox.profile"},
		{"isolation container", func(c *Config) { c.Sandbox.Isolation = "container" }, ""},
		{"isolation bad", func(c *Config) { c.Sandbox.Isolation = "vm" }, "sandbox.isolation"},
		{"allow write relative", func(c *Config) { c.Sandbox.AllowWrite = []string{"tmp"} }, "sandbox.allow_write[1]"},
		{"domain wildcard", func(c *Config) { c.Sandbox.AllowedDomains = []string{"*.github.com"} }, ""},
		{"domain url", func(c *Config) { c.Sandbox.AllowedDomains = []string{"https://github.com"} }, "sandbox.allowed_domains[1]"},
		{"domain port", func(c *Config) { c.Sandbox.AllowedDomains = []string{"github.com:443"} }, "sandbox.allowed_domains[1]"},
		{"domain inner wildcard", func(c *Config) { c.Sandbox.AllowedDomains = []string{"a.*.com"} }, "sandbox.allowed_domains[1]"},
		{"domain too long", func(c *Config) {
			c.Sandbox.AllowedDomains = []string{strings.Repeat("a.", 127) + "com"}
		}, "sandbox.allowed_domains[1]"},
		{"excluded commands", func(c *Config) { c.Sandbox.ExcludedCommands = []string{"docker", "git push"} }, ""},
		{"list too long", func(c *Config) { c.Sandbox.ExcludedCommands = make([]string, maxListLen+1) }, "sandbox.excluded_commands"},

		{"popup cells", func(c *Config) { c.Popup.Width = "120" }, ""},
		{"popup 100%", func(c *Config) { c.Popup.Height = "100%" }, ""},
		{"popup small percent", func(c *Config) { c.Popup.Width = "9%" }, "popup.width"},
		{"popup big percent", func(c *Config) { c.Popup.Width = "101%" }, "popup.width"},
		{"popup few cells", func(c *Config) { c.Popup.Height = "19" }, "popup.height"},
		{"popup many cells", func(c *Config) { c.Popup.Height = "1001" }, "popup.height"},
		{"popup unit", func(c *Config) { c.Popup.Height = "90px" }, "popup.height"},
		{"prefix ai-", func(c *Config) { c.Popup.SessionPrefix = "ai-" }, ""},
		{"prefix empty", func(c *Config) { c.Popup.SessionPrefix = "" }, "popup.session_prefix"},
		{"prefix dot", func(c *Config) { c.Popup.SessionPrefix = "ai." }, "popup.session_prefix"},
		{"prefix leading dash", func(c *Config) { c.Popup.SessionPrefix = "-ai" }, "popup.session_prefix"},
		{"prefix too long", func(c *Config) { c.Popup.SessionPrefix = strings.Repeat("a", 17) }, "popup.session_prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			assertProblem(t, cfg.Validate(), tc.wantKey)
		})
	}
}

// TestClaudeCommandProblem pins the rule a command given outside the
// configuration file, such as a tmux option in plugin mode, is held to.
func TestClaudeCommandProblem(t *testing.T) {
	cases := []struct {
		name, command string
		wantProblem   bool
	}{
		{name: "empty is the default", command: ""},
		{name: "name on PATH", command: "claude"},
		{name: "absolute path", command: "/opt/agents/bin/claude"},
		{name: "tilde path", command: "~/bin/claude", wantProblem: true},
		{name: "relative path", command: "bin/claude", wantProblem: true},
		{name: "arguments in the command", command: "claude --verbose", wantProblem: true},
		{name: "tab", command: "claude\t", wantProblem: true},
		{name: "control character", command: "/bin/claude\x1b", wantProblem: true},
		{name: "invalid UTF-8", command: "claude\xff", wantProblem: true},
		{name: "overlong", command: "/" + strings.Repeat("a", maxValueBytes), wantProblem: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := ClaudeCommandProblem(tc.command)
			if (msg != "") != tc.wantProblem {
				t.Fatalf("ClaudeCommandProblem(%q) = %q, want problem %v", tc.command, msg, tc.wantProblem)
			}
			if tc.wantProblem && !strings.Contains(msg, "must be a command name on PATH or an absolute path") {
				t.Fatalf("ClaudeCommandProblem(%q) = %q", tc.command, msg)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		name, in string
		want     bool
	}{
		{name: "empty", in: "", want: true},
		{name: "plain", in: "--append-system-prompt 'be brief'", want: true},
		{name: "accented", in: "é●", want: true},
		{name: "tab", in: "a\tb"},
		{name: "newline", in: "a\nb"},
		{name: "escape", in: "\x1b[2J"},
		{name: "nul", in: "a\x00b"},
		{name: "delete", in: "a\x7fb"},
		{name: "invalid UTF-8", in: "a\xffb"},
		{name: "at the bound", in: strings.Repeat("a", maxValueBytes), want: true},
		{name: "over the bound", in: strings.Repeat("a", maxValueBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.in); got != tc.want {
				t.Fatalf("CleanText(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateLayouts(t *testing.T) {
	claude := Pane{Role: "claude"}
	cases := []struct {
		name    string
		layouts map[string]Layout
		wantKey string
	}{
		{"valid three panes", map[string]Layout{"tests": {Panes: []Pane{
			claude,
			{Role: "changes", Split: "right", Size: 35},
			{Role: "command", Split: "down", Size: 30, Parent: 1, Command: "go test ./..."},
		}}}, ""},
		{"worktree claude panes", map[string]Layout{"pair": {Panes: []Pane{
			{Role: "claude", Worktree: true},
			{Role: "claude", Split: "right", Worktree: true},
		}}}, ""},
		{"builtin name", map[string]Layout{"duo": {Panes: []Pane{claude}}}, "layouts.duo"},
		{"review is a builtin name", map[string]Layout{"review": {Panes: []Pane{claude}}}, "layouts.review"},
		{"review pane role", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "review", Split: "right"}}}}, ""},
		{"bad name", map[string]Layout{"My Layout": {Panes: []Pane{claude}}}, "layouts.My Layout"},
		{"no panes", map[string]Layout{"x": {}}, "layouts.x.panes"},
		{"too many panes", map[string]Layout{"x": {Panes: slices.Repeat([]Pane{{Role: "claude", Split: "right"}}, maxPanes+1)}}, "layouts.x.panes"},
		{"no claude pane", map[string]Layout{"x": {Panes: []Pane{{Role: "shell"}}}}, "layouts.x.panes"},
		{"bad role", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "editor", Split: "right"}}}}, "layouts.x.panes[2].role"},
		{"first pane split", map[string]Layout{"x": {Panes: []Pane{{Role: "claude", Split: "right"}}}}, "layouts.x.panes[1]"},
		{"missing split", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell"}}}}, "layouts.x.panes[2].split"},
		{"size too small", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell", Split: "down", Size: 5}}}}, "layouts.x.panes[2].size"},
		{"parent is self", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell", Split: "down", Parent: 2}}}}, "layouts.x.panes[2].parent"},
		{"negative parent", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell", Split: "down", Parent: -1}}}}, "layouts.x.panes[2].parent"},
		{"command missing", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "command", Split: "down"}}}}, "layouts.x.panes[2].command"},
		{"command multiline", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "command", Split: "down", Command: "a\nb"}}}}, "layouts.x.panes[2].command"},
		{"command on shell", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell", Split: "down", Command: "ls"}}}}, "layouts.x.panes[2].command"},
		{"worktree on shell", map[string]Layout{"x": {Panes: []Pane{claude, {Role: "shell", Split: "down", Worktree: true}}}}, "layouts.x.panes[2].worktree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Layouts = tc.layouts
			assertProblem(t, cfg.Validate(), tc.wantKey)
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := Default()
	cfg.UI.Theme = "x"
	cfg.Sandbox.Profile = "x"
	cfg.Popup.Width = "x"
	var probs Problems
	if !errors.As(cfg.Validate(), &probs) {
		t.Fatal("want Problems")
	}
	if len(probs) != 3 {
		t.Fatalf("got %d problems, want 3: %v", len(probs), probs)
	}
	if msg := probs.Error(); !strings.Contains(msg, `ui.theme: must be one of `+strings.Join(Choices("ui.theme"), ", ")+` (got "x")`) {
		t.Fatalf("message %q", msg)
	}
}

func assertProblem(t *testing.T, err error, wantKey string) {
	t.Helper()
	if wantKey == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var probs Problems
	if !errors.As(err, &probs) {
		t.Fatalf("err = %v, want problem on %s", err, wantKey)
	}
	for _, p := range probs {
		if p.Key == wantKey {
			return
		}
	}
	t.Fatalf("problems %v do not include key %s", probs, wantKey)
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.toml")
	invalid := filepath.Join(dir, "invalid.toml")
	link := filepath.Join(dir, "link.toml")
	big := filepath.Join(dir, "big.toml")
	for path, body := range map[string]string{
		valid:   "[ui]\ntheme = \"ansi\"\n",
		invalid: "[ui]\ntheme = \"nope\"\n",
		big:     "# " + strings.Repeat("x", MaxFileSize) + "\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		path         string
		wantFound    bool
		wantTheme    string
		wantIs       error
		wantProblems bool
		errPrefix    string
	}{
		{"missing uses defaults", filepath.Join(dir, "missing.toml"), false, "monokai", nil, false, ""},
		{"valid", valid, true, "ansi", nil, false, ""},
		{"symlink followed", link, true, "ansi", nil, false, ""},
		{"invalid names file", invalid, true, "", nil, true, invalid + ": invalid config"},
		{"too large", big, false, "", fsx.ErrTooLarge, false, "read " + big},
		{"directory", dir, false, "", nil, false, "read " + dir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, found, err := Load(tc.path)
			if found != tc.wantFound {
				t.Errorf("found = %v, want %v", found, tc.wantFound)
			}
			if tc.errPrefix == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.UI.Theme != tc.wantTheme {
					t.Fatalf("theme = %q, want %q", cfg.UI.Theme, tc.wantTheme)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.errPrefix) {
				t.Fatalf("err = %v, want prefix %q", err, tc.errPrefix)
			}
			var probs Problems
			if tc.wantProblems && !errors.As(err, &probs) {
				t.Fatalf("err %v does not unwrap to Problems", err)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err %v is not %v", err, tc.wantIs)
			}
		})
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	full := Default()
	full.UI.Theme = "light"
	full.UI.AllowPassthrough = true
	full.UI.SidebarWidth = 36
	full.Workspace.Layout = "review"
	full.Workspace.Shell = "/bin/zsh"
	full.Workspace.AgentPanes = 5
	full.Claude = Claude{
		Command: "/opt/claude", Args: []string{"--verbose", `quote"d`}, Model: "opus", Effort: "xhigh",
		PermissionMode: "plan", Statusline: "lyna", Fullscreen: true, Teams: true, TeammateMode: "in-process", WorktreeBase: "head",
		WorkflowSize: "small", Bell: false, AddDirs: []string{"~/a"}, MCPConfig: []string{"/m.json"}, PluginDirs: []string{"~/p"},
	}
	full.Sandbox = Sandbox{
		Profile: "strict", Isolation: "process", AllowWrite: []string{"~/.cache"}, DenyRead: []string{"~/Documents"},
		AllowedDomains: []string{"*.example.com"}, ExcludedCommands: []string{"docker"},
	}
	full.Layouts = map[string]Layout{"tests": {Panes: []Pane{
		{Role: "claude", Worktree: true},
		{Role: "command", Split: "right", Size: 40, Parent: 1, Command: `echo "hi" # not a comment`},
	}}}

	for name, cfg := range map[string]Config{"default": Default(), "full": full} {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			data, err := Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decode(data)
			if err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			if !reflect.DeepEqual(got, cfg) {
				t.Fatalf("round trip changed config:\n got %+v\nwant %+v\n%s", got, cfg, data)
			}
		})
	}
}

// TestThemeChoicesCoverPalettes keeps ui.theme in step with the built-in
// palettes: a palette the validator does not list cannot be configured, and
// a listed name without a palette fails at start-up.
func TestThemeChoicesCoverPalettes(t *testing.T) {
	got := slices.Sorted(slices.Values(Choices("ui.theme")))
	if want := theme.Names(); !slices.Equal(got, want) {
		t.Fatalf("ui.theme choices %v, want the palettes %v", got, want)
	}
	if Choices("ui.theme")[0] != Default().UI.Theme {
		t.Fatalf("the default theme %q must be listed first", Default().UI.Theme)
	}
}

func TestChoices(t *testing.T) {
	cases := []struct {
		key  string
		want []string
	}{
		{"ui.theme", []string{"monokai", "lyna", "slate", "dusk", "contrast", "nord", "rose", "mono", "solar-dark", "earth-dark", "light", "solar-light", "earth-light", "ansi"}},
		{"sandbox.profile", []string{"standard", "strict", "off"}},
		{"workspace.layout", []string{"solo", "duo", "trio", "quad", "review", "team", "git", "auto"}},
		{"layouts.panes.role", []string{"claude", "shell", "changes", "review", "command", "agents", "git"}},
		{"review.editor", []string{"isolated", "user"}},
		{"review.layout", []string{"default", "inline", "side-by-side"}},
		{"claude.model", nil},
		{"nope", nil},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := Choices(tc.key)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Choices(%q) = %v, want %v", tc.key, got, tc.want)
			}
			if len(got) > 0 {
				got[0] = "mutated"
				if Choices(tc.key)[0] == "mutated" {
					t.Fatal("Choices returned shared backing array")
				}
			}
		})
	}
}

// TestReviewChoicesParse keeps the review choices in step with the values the
// review domain accepts.
func TestReviewChoicesParse(t *testing.T) {
	for _, v := range Choices("review.editor") {
		if _, err := review.ParseEditorMode(v); err != nil {
			t.Errorf("review.editor %q: %v", v, err)
		}
	}
	for _, v := range Choices("review.layout") {
		if _, err := review.ParseLayout(v); err != nil {
			t.Errorf("review.layout %q: %v", v, err)
		}
	}
}

func TestTemplateReturnsCopy(t *testing.T) {
	a := Template()
	a[0] = 'X'
	if Template()[0] == 'X' {
		t.Fatal("Template returned shared backing array")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(Template())
	f.Add([]byte("[layouts.x]\npanes = [{ role = \"claude\" }, { role = \"shell\", split = \"down\", parent = 1 }]\n"))
	f.Add([]byte("[ui]\ntheme = \"\\u0000\"\n"))
	f.Add([]byte("a = [[[[[[[[[["))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Decode(data)
		if err != nil {
			if err.Error() == "" {
				t.Fatal("empty error message")
			}
			return
		}
		if verr := cfg.Validate(); verr != nil {
			t.Fatalf("Decode returned an invalid config: %v", verr)
		}
		out, err := Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal of decoded config failed: %v", err)
		}
		again, err := Decode(out)
		if err != nil {
			t.Fatalf("re-decode failed: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(again, cfg) {
			t.Fatalf("round trip changed config:\n%+v\n%+v", cfg, again)
		}
	})
}

// TestRailKey covers every value of ui.agents_sidebar: only off is the choice
// of a user who wants no rail at all, and it is the only one that takes the
// rail key with it.
func TestRailKey(t *testing.T) {
	cases := []struct {
		name    string
		sidebar string
		want    bool
	}{
		{name: "auto opens it with the first agent", sidebar: SidebarAuto, want: true},
		{name: "always opens it with the workspace", sidebar: SidebarAlways, want: true},
		{name: "key is the key alone", sidebar: SidebarKey, want: true},
		{name: "off is no rail at all", sidebar: SidebarOff},
		{name: "the default", sidebar: Default().UI.AgentsSidebar, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (UI{AgentsSidebar: tc.sidebar}).RailKey(); got != tc.want {
				t.Fatalf("RailKey of %q = %v, want %v", tc.sidebar, got, tc.want)
			}
		})
	}
	// Every accepted value is covered, so a fifth one cannot be added without
	// deciding what it does to the key.
	if got, want := len(Choices("ui.agents_sidebar")), 4; got != want {
		t.Fatalf("ui.agents_sidebar accepts %d values, want %d", got, want)
	}
}
