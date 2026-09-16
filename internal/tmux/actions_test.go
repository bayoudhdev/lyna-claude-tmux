package tmux

import (
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
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
		{"window", keys.Binding{Action: keys.ActionWindow, Arg: "3"}, "select-window -t :3"},
		{"tree", keys.Binding{Action: keys.ActionTree}, "choose-tree -Zs"},
		{"menu", keys.Binding{Action: keys.ActionMenu}, "run-shell -C '#{@lt_do_menu_keys}'"},
		{
			"agents",
			keys.Binding{Action: keys.ActionAgents},
			"display-popup -E -w 90% -h 85% -d '#{pane_current_path}' -T ' agents ' ''\\''/opt/lyna tools/bin/lyna-tmux'\\'' agents --popup'",
		},
		{
			"review",
			keys.Binding{Action: keys.ActionReview},
			"display-popup -E -w 95% -h 95% -d '#{pane_current_path}' -T ' review ' ''\\''/opt/lyna tools/bin/lyna-tmux'\\'' review --popup'",
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
		{DoReview, "display-popup", false},
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
		{"plain", "/usr/local/bin/lyna-tmux", []string{"agents", "--popup"}, "/usr/local/bin/lyna-tmux agents --popup"},
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
