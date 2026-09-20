package tmux

import (
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/theme"
)

func TestActionSeq(t *testing.T) {
	e := testEnv()
	cases := []struct {
		name    string
		binding keys.Binding
		want    string
	}{
		{"pane prev", keys.Binding{Action: keys.ActionPanePrev}, "select-pane -t :.-"},
		{"pane next", keys.Binding{Action: keys.ActionPaneNext}, "select-pane -t :.+"},
		{"resize left", keys.Binding{Action: keys.ActionResizeLeft}, "resize-pane -L 5"},
		{"resize right", keys.Binding{Action: keys.ActionResizeRight}, "resize-pane -R 5"},
		{"resize up", keys.Binding{Action: keys.ActionResizeUp}, "resize-pane -U 3"},
		{"resize down", keys.Binding{Action: keys.ActionResizeDown}, "resize-pane -D 3"},
		{"split right", keys.Binding{Action: keys.ActionSplitRight}, "split-window -h -c '#{pane_current_path}' ; set-option -p @lt_role shell"},
		{"split down", keys.Binding{Action: keys.ActionSplitDown}, "split-window -v -c '#{pane_current_path}' ; set-option -p @lt_role shell"},
		{"zoom", keys.Binding{Action: keys.ActionZoom}, "resize-pane -Z"},
		{"close", keys.Binding{Action: keys.ActionClosePane}, "confirm-before -p 'Close pane #P? (y/n)' kill-pane"},
		{
			"focus claude",
			keys.Binding{Action: keys.ActionFocusClaude},
			"run-shell -C '#{?#{P:#{?#{==:#{@lt_role},claude},#{pane_id} ,}},select-pane -t #{s/ .*//:#{P:#{?#{==:#{@lt_role},claude},#{pane_id} ,}}},display-message '\\''no Claude pane in this window'\\''}'",
		},
		{
			"new window",
			keys.Binding{Action: keys.ActionNewWindow},
			"new-window -c '#{?#{@lt_project},#{@lt_project},#{pane_current_path}}' ; set-option -p @lt_role shell",
		},
		{"agents rail", keys.Binding{Action: keys.ActionAgentsRail}, "run-shell -C '#{@lt_do_agents_rail}'"},
		{"git workstation", keys.Binding{Action: keys.ActionGitWork}, "run-shell -C '#{@lt_do_git_work}'"},
		{"window", keys.Binding{Action: keys.ActionWindow, Arg: "3"}, "select-window -t :3"},
		{"tree", keys.Binding{Action: keys.ActionTree}, "choose-tree -Zs"},
		{"menu", keys.Binding{Action: keys.ActionMenu}, "run-shell -C '#{@lt_do_menu_keys}'"},
		{
			"agents",
			keys.Binding{Action: keys.ActionAgents},
			"display-popup -E -w 90% -h 85% -d '#{pane_current_path}' -T ' agents ' ''\\''/opt/lyna tools/bin/lmux'\\'' agents --popup'",
		},
		{
			"review",
			keys.Binding{Action: keys.ActionReview},
			"display-popup -E -w 95% -h 95% -d '#{pane_current_path}' -T ' review ' ''\\''/opt/lyna tools/bin/lmux'\\'' review --popup'",
		},
		{"scratch", keys.Binding{Action: keys.ActionScratch}, "display-popup -E -w 80% -h 70% -d '#{pane_current_path}' -T ' scratch '"},
		{"send prefix", keys.Binding{Action: keys.ActionSendPrefix}, "send-prefix"},
		{"detach", keys.Binding{Action: keys.ActionDetach}, "detach-client"},
		{"reload", keys.Binding{Action: keys.ActionReload}, "source-file /state/lyna-tmux/tmux.conf ; display-message 'lyna-tmux: configuration reloaded'"},
		{"unknown escaped", keys.Binding{Action: "#{x}%"}, "display-message 'lyna-tmux: unknown action ##{x}%%'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.ActionSeq(tc.binding).String(); got != tc.want {
				t.Fatalf("ActionSeq = %s\nwant       %s", got, tc.want)
			}
		})
	}
}

// TestEveryDefaultBindingHasAction guards the switch in ActionSeq: a new
// action added to the key table must be mapped, not fall through to the
// unknown-action message.
func TestEveryDefaultBindingHasAction(t *testing.T) {
	e := testEnv()
	for _, alt := range []bool{true, false} {
		for _, b := range keys.Defaults(keys.Options{AltKeys: alt}) {
			t.Run(string(b.Table)+"/"+b.Key, func(t *testing.T) {
				if s := e.ActionSeq(b).String(); strings.Contains(s, "unknown action") {
					t.Fatalf("binding %+v is not mapped", b)
				}
			})
		}
	}
}

// TestAgentsRailSeq pins the toggle text. Both branches live in one
// conditional, whose commas separate them: the close branch is a format tmux
// walks over as a whole, so its own commas are left alone, while the open
// branch is plain text where a comma of the installation path would end the
// branch.
func TestAgentsRailSeq(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{
			"toggle",
			testEnv(),
			`run-shell -C '#{?#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}},` +
				`kill-pane -t #{s/ .*//:#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}}},` +
				`split-window -b -h -l 28 -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`'\''/opt/lyna tools/bin/lmux'\'' agents --rail --auto` +
				` ; set-option -p @lt_role agents ; last-pane}'`,
		},
		{
			"comma in the path",
			Env{Bin: "/opt/a,b/lmux"},
			`run-shell -C '#{?#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}},` +
				`kill-pane -t #{s/ .*//:#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}}},` +
				`split-window -b -h -l 28 -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/a#,b/lmux agents --rail --auto` +
				` ; set-option -p @lt_role agents ; last-pane}'`,
		},
		{
			"the width the workspace asks for",
			Env{Bin: "/opt/lmux", RailWidth: 44},
			`run-shell -C '#{?#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}},` +
				`kill-pane -t #{s/ .*//:#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}}},` +
				`split-window -b -h -l 44 -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/lmux agents --rail --auto` +
				` ; set-option -p @lt_role agents ; last-pane}'`,
		},
		{
			"a width past the widest rail",
			Env{Bin: "/opt/lmux", RailWidth: 400},
			`run-shell -C '#{?#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}},` +
				`kill-pane -t #{s/ .*//:#{P:#{?#{==:#{@lt_role},agents},#{pane_id} ,}}},` +
				`split-window -b -h -l 60 -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/lmux agents --rail --auto` +
				` ; set-option -p @lt_role agents ; last-pane}'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.AgentsRailSeq().String(); got != tc.want {
				t.Fatalf("AgentsRailSeq = %s\nwant             %s", got, tc.want)
			}
		})
	}
}

// TestGitWorkSeq pins the toggle text of the git workstation. It is built the
// way the rail toggle is, so the same reading of the conditional holds: the
// close branch is formats the conditional walks over whole, and the open
// branch is plain text where a comma of the installation path would be read as
// the separator of the branch after it.
func TestGitWorkSeq(t *testing.T) {
	const list = `#{P:#{?#{==:#{@lt_role},git},#{pane_id} ,}}`
	toggle := func(open string) string {
		return `run-shell -C '#{?` + list + `,kill-pane -t #{s/ .*//:` + list + `},` + open + `}'`
	}
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{
			"toggle",
			testEnv(),
			toggle(`split-window -h -f -l 38% -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`'\''/opt/lyna tools/bin/lmux'\'' git ; set-option -p @lt_role git`),
		},
		{
			"comma in the path",
			Env{Bin: "/opt/a,b/lmux"},
			toggle(`split-window -h -f -l 38% -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/a#,b/lmux git ; set-option -p @lt_role git`),
		},
		{
			"the share the workspace leaves beside the agent",
			Env{Bin: "/opt/lmux", SplitRatio: 70},
			toggle(`split-window -h -f -l 30% -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/lmux git ; set-option -p @lt_role git`),
		},
		{
			"a ratio no window can be split at",
			Env{Bin: "/opt/lmux", SplitRatio: 95},
			toggle(`split-window -h -f -l 38% -t #{s/ .*//:#{P:#{pane_id} }} ` +
				`/opt/lmux git ; set-option -p @lt_role git`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.env.GitWorkSeq().String(); got != tc.want {
				t.Fatalf("GitWorkSeq = %s\nwant          %s", got, tc.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	e := testEnv()
	reg := e.Registry()
	names := map[string]Seq{}
	for _, r := range reg {
		if _, dup := names[r.Name]; dup {
			t.Fatalf("registered twice: %s", r.Name)
		}
		names[r.Name] = r.Seq
	}
	cases := []struct {
		name      string
		wantFirst string
		mouse     bool
	}{
		{DoSplitRight, "split-window", false},
		{DoAgents, "display-popup", false},
		{DoAgentsRail, "run-shell", false},
		{DoReview, "display-popup", false},
		{DoGitWork, "run-shell", false},
		{DoSandbox, "display-popup", false},
		{DoMenuKeys, "display-menu", false},
		{DoMenuSession, "display-menu", true},
		{DoMenuWindow, "display-menu", true},
		{DoMenuPane, "display-menu", true},
		{DoMenuClaude, "display-menu", false},
	}
	if len(cases) != len(reg) {
		t.Fatalf("%d registered commands, %d cases", len(reg), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := names[tc.name]
			if !ok {
				t.Fatal("not registered")
			}
			if s[0][0] != tc.wantFirst {
				t.Fatalf("first command %q, want %q", s[0][0], tc.wantFirst)
			}
			hasTarget := len(s[0]) > 2 && s[0][1] == "-t" && s[0][2] == "="
			if hasTarget != tc.mouse {
				t.Fatalf("mouse target = %v, want %v", hasTarget, tc.mouse)
			}
			// Registered text is parsed once by run-shell -C: it must parse back
			// into the same commands.
			back, err := parseSeq(s.String())
			if err != nil || !slices.EqualFunc(back, s, slices.Equal) {
				t.Fatalf("stored text does not parse back: %v", err)
			}
		})
	}
}

// TestRegisteredReferences checks every DoSeq reachable from menus, bindings
// and the click handler names a registered command.
func TestRegisteredReferences(t *testing.T) {
	e := testEnv()
	registered := map[string]bool{}
	var all strings.Builder
	for _, r := range e.Registry() {
		registered[r.Name] = true
		all.WriteString(r.Seq.String())
	}
	for _, b := range e.Bindings {
		all.WriteString(e.ActionSeq(b).String())
	}
	all.WriteString(ClickSeq().String())
	text := strings.ReplaceAll(all.String(), "##", "#")
	refs := 0
	for rest := text; ; {
		i := strings.Index(rest, "#{@lt_do_")
		if i < 0 {
			break
		}
		rest = rest[i+len("#{@lt_do_"):]
		name := rest[:strings.IndexByte(rest, '}')]
		if !registered[name] {
			t.Errorf("reference to unregistered command %q", name)
		}
		refs++
	}
	if refs < 7 {
		t.Fatalf("only %d references found; the scan is broken", refs)
	}
}

func TestDoSeq(t *testing.T) {
	if got := DoSeq(DoMenuPane).String(); got != "run-shell -C '#{@lt_do_menu_pane}'" {
		t.Fatal(got)
	}
}

func TestBinCommand(t *testing.T) {
	cases := []struct {
		name, bin string
		args      []string
		want      string
	}{
		{"plain", "/usr/local/bin/lmux", []string{"agents", "--popup"}, "/usr/local/bin/lmux agents --popup"},
		{"space in path", "/Users/a b/lyna-tmux", []string{"watch"}, "'/Users/a b/lyna-tmux' watch"},
		{"hash in path", "/w/#1/lyna-tmux", []string{"x"}, "'/w/##1/lyna-tmux' x"},
		{"quote in path", "/w/it's/lyna-tmux", nil, `'/w/it'\''s/lyna-tmux'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Env{Bin: tc.bin}).binCommand(tc.args...); got != tc.want {
				t.Fatalf("binCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithMouseTarget(t *testing.T) {
	cases := []struct {
		name string
		in   Seq
		want Seq
	}{
		{"menu", Cmd("display-menu", "-x", "M", "a"), Seq{{"display-menu", "-t", "=", "-x", "M", "a"}}},
		{"other commands kept", Cmd("select-pane", "-t", "=").Then(Cmd("display-menu", "x")), Seq{{"select-pane", "-t", "="}, {"display-menu", "-t", "=", "x"}}},
		{"empty command kept", Seq{nil}, Seq{nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := make(Seq, len(tc.in))
			for i, c := range tc.in {
				in[i] = slices.Clone(c)
			}
			got := withMouseTarget(tc.in)
			if !slices.EqualFunc(got, tc.want, slices.Equal) {
				t.Fatalf("withMouseTarget = %q, want %q", got, tc.want)
			}
			if !slices.EqualFunc(tc.in, in, slices.Equal) {
				t.Fatalf("input modified: %q", tc.in)
			}
		})
	}
}

// TestRegistryWithoutTheRailToggle pins what a workspace that never opens the
// agents rail by itself registers: everything but the rail toggle, and nothing
// left referring to it.
func TestRegistryWithoutTheRailToggle(t *testing.T) {
	cases := []struct {
		name     string
		env      Env
		wantRail bool
	}{
		{name: "the rail has a key", env: testEnv(), wantRail: true},
		{name: "the rail is off", env: railOffEnv(), wantRail: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered := map[string]bool{}
			for _, r := range tc.env.Registry() {
				registered[r.Name] = true
			}
			if got := registered[DoAgentsRail]; got != tc.wantRail {
				t.Fatalf("%s registered = %v, want %v", DoAgentsRail, got, tc.wantRail)
			}
			// Every other command is registered whatever the rail does.
			for _, name := range []string{DoSplitRight, DoAgents, DoReview, DoGitWork, DoSandbox, DoMenuKeys, DoMenuSession, DoMenuWindow, DoMenuPane, DoMenuClaude} {
				if !registered[name] {
					t.Errorf("%s is not registered", name)
				}
			}
		})
	}
}

// railOffEnv is the installation of a workspace configured never to open the
// agents rail on its own: no key for it, and no toggle to register.
func railOffEnv() Env {
	e := testEnv()
	e.Bindings = keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b", NoAgentsRail: true})
	e.NoAgentsRail = true
	return e
}

// TestGeneratedConfWithoutTheRailKey reads the generated configuration of a
// workspace that never opens the agents rail by itself: no binding in either
// table, no entry in the key menu, and no stored toggle for anything to run.
// With the rail on a key, all three are there.
func TestGeneratedConfWithoutTheRailKey(t *testing.T) {
	conf := func(e Env) string {
		return GenerateConf(ConfOptions{
			Version: Version{Major: 3, Minor: 7},
			Look:    Look{Palette: mustPalette(t, "lyna"), Depth: theme.Depth256, Icons: mustIcons(t, "ascii")},
			Env:     e,
		})
	}
	rail := []string{
		"bind-key -T root M-A ",
		"bind-key -T prefix A ",
		"set-option -g " + DoOption(DoAgentsRail) + " ",
		"Agents rail",
	}
	cases := []struct {
		name string
		env  Env
		want bool
	}{
		{name: "the rail has a key", env: testEnv(), want: true},
		{name: "the rail is off", env: railOffEnv(), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := conf(tc.env)
			for _, part := range rail {
				if got := strings.Contains(text, part); got != tc.want {
					t.Errorf("conf contains %q = %v, want %v", part, got, tc.want)
				}
			}
			// The picker keeps its own key and its own menu entry: off is about
			// the rail alone.
			for _, part := range []string{"bind-key -T root M-a ", "bind-key -T prefix a ", "set-option -g " + DoOption(DoAgents) + " "} {
				if !strings.Contains(text, part) {
					t.Errorf("conf lacks %q", part)
				}
			}
		})
	}
}
