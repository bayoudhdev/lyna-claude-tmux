package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/tmuxtest"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/tmux"
)

// Values that stress every quoting layer: tmux argv terminators, conf
// quoting, format expansion and shell metacharacters.
var hostileValues = []string{
	"plain",
	";",
	"a;",
	`a\;`,
	`a\\;`,
	"it's",
	`"double"`,
	"$HOME ~ `id` $(id)",
	"#{pane_id} #[fg=red] ##",
	`back\slash`,
	"space and\ttab",
	"é ● 漢字",
}

// keptByTmux reports whether got is the value tmux kept for want. Two
// versions hand a value back in a form of their own, and neither form means
// anything else:
//   - a control character is printed as its octal escape by tmux 3.4;
//   - a value that starts with a dollar comes back with a backslash in front
//     of it, which is how that version marks text it must not expand.
//
// Either way nothing was run and no character was lost.
func keptByTmux(got, want string) bool {
	if got == want {
		return true
	}
	escaped := strings.ReplaceAll(want, "\x1f", `\037`)
	if strings.HasPrefix(escaped, "$") {
		escaped = `\` + escaped
	}
	return got == escaped
}

func TestIntegrationEscapeArgRoundTrip(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	for i, v := range hostileValues {
		t.Run(v, func(t *testing.T) {
			name := "@lt_probe" + string(rune('a'+i))
			// Two commands in one batch prove escaped data never splits a batch.
			if _, err := srv.Client.Batch(ctx,
				tmux.Command{"set-option", "-g", name, v},
				tmux.Command{"set-option", "-g", name + "_after", "ok"},
			); err != nil {
				t.Fatal(err)
			}
			got, err := srv.Client.ShowOption(ctx, "-g", "", name)
			if err != nil {
				t.Fatal(err)
			}
			if !keptByTmux(got, v) {
				t.Fatalf("stored %q, read %q", v, got)
			}
			after, err := srv.Client.ShowOption(ctx, "-g", "", name+"_after")
			if err != nil || after != "ok" {
				t.Fatalf("second batch command lost: %q %v", after, err)
			}
		})
	}
}

func TestIntegrationConfQuoteRoundTrip(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	var conf strings.Builder
	for i, v := range hostileValues {
		conf.WriteString("set-option -g @lt_conf" + string(rune('a'+i)) + " " + tmux.ConfQuote(v) + "\n")
	}
	path := filepath.Join(t.TempDir(), "probe.conf")
	if err := os.WriteFile(path, []byte(conf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "source-file", path); err != nil {
		t.Fatalf("source-file: %v", err)
	}
	for i, v := range hostileValues {
		t.Run(v, func(t *testing.T) {
			// ConfQuote drops control characters by design.
			want := strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return -1
				}
				return r
			}, v)
			got, err := srv.Client.ShowOption(ctx, "-g", "", "@lt_conf"+string(rune('a'+i)))
			if err != nil {
				t.Fatal(err)
			}
			if !keptByTmux(got, want) {
				t.Fatalf("conf value %q read back as %q", want, got)
			}
		})
	}
}

// TestIntegrationFormatEscape pins how tmux expands escaped text in the two
// kinds of format: data formats (list -F) must yield the input exactly, drawn
// formats (display-message, status line) keep "##[" for the drawing step.
func TestIntegrationFormatEscape(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	cases := []struct {
		name, in string
		// wantDisplay is the display-message output of DrawEscapeTime(in).
		wantDisplay string
	}{
		{name: "variable", in: "#{pane_id} #S", wantDisplay: "#{pane_id} #S"},
		{name: "hashes", in: "## #", wantDisplay: "## #"},
		{name: "style", in: "#[fg=red]x", wantDisplay: "##[fg=red]x"},
		{name: "hash run before bracket", in: "a##[b ###[c", wantDisplay: "a####[b ######[c"},
		{name: "hash then bracket later", in: "#x[", wantDisplay: "#x["},
		{name: "percent", in: "100% %Y", wantDisplay: "100% %Y"},
		{name: "path", in: "/work/50%off/#tag/#[x]", wantDisplay: "/work/50%off/#tag/##[x]"},
		{name: "trailing hash", in: "end#", wantDisplay: "end#"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list, err := srv.Client.Run(ctx, "list-sessions", "-F", tmux.FormatEscape(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSuffix(list, "\n"); got != tc.in {
				t.Fatalf("list -F FormatEscape(%q) -> %q", tc.in, got)
			}
			got, err := srv.Client.Display(ctx, tmux.ExactSession("base"), tmux.DrawEscapeTime(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantDisplay {
				t.Fatalf("display %q -> %q, want %q", tc.in, got, tc.wantDisplay)
			}
		})
	}
}

func TestIntegrationSessionsAndPanes(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	dir := t.TempDir()
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "api-dev", "-c", dir, "sleep 3600"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Batch(ctx,
		tmux.Command{"set-option", "-t", tmux.ExactSession("api-dev"), tmux.OptManaged, "1"},
		tmux.Command{"set-option", "-t", tmux.ExactSession("api-dev"), tmux.OptLayout, "duo"},
		tmux.Command{"split-window", "-d", "-h", "-t", tmux.ExactSession("api-dev"), "-c", dir, "sleep 3600"},
	); err != nil {
		t.Fatal(err)
	}

	sessions, err := srv.Client.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tmux.Session{}
	for _, s := range sessions {
		byName[s.Name] = s
	}
	api, ok := byName["api-dev"]
	if !ok || len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	if !api.Managed || api.Layout != "duo" || api.Windows != 1 || api.Created.IsZero() {
		t.Fatalf("api session = %+v", api)
	}
	if byName["base"].Managed {
		t.Fatal("base session should not be managed")
	}

	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("api-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 2 {
		t.Fatalf("panes = %+v", panes)
	}
	for _, p := range panes {
		if p.SessionName != "api-dev" || !strings.HasPrefix(p.ID, "%") || p.PID == 0 || p.TTY == "" {
			t.Fatalf("pane = %+v", p)
		}
	}
	if _, err := srv.Client.Batch(ctx, tmux.Command{"set-option", "-p", "-t", panes[0].ID, tmux.OptState, "waiting"}); err != nil {
		t.Fatal(err)
	}
	all, err := srv.Client.ListPanes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all panes = %d, want 3", len(all))
	}
	var found bool
	for _, p := range all {
		if p.ID == panes[0].ID {
			found = p.State == "waiting"
		}
	}
	if !found {
		t.Fatal("pane state option not listed")
	}
}

func TestIntegrationHasSessionExact(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	cases := []struct {
		name string
		want bool
	}{
		{"base", true},
		{"bas", false}, // prefix match must not count
		{"b*", false},  // pattern match must not count
		{"missing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := srv.Client.HasSession(ctx, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("HasSession(%q) = %v", tc.name, got)
			}
		})
	}
}

func TestIntegrationNoServer(t *testing.T) {
	bin := tmuxtest.Require(t)
	ctx := tmuxtest.Context(t)
	c := tmux.New(tmux.Options{Bin: bin, Socket: tmux.Socket{Path: filepath.Join(t.TempDir(), "absent")}})
	if s, err := c.ListSessions(ctx); err != nil || s != nil {
		t.Fatalf("ListSessions = %v, %v", s, err)
	}
	if ok, err := c.HasSession(ctx, "x"); err != nil || ok {
		t.Fatalf("HasSession = %v, %v", ok, err)
	}
	_, err := c.Run(ctx, "kill-session", "-t", "=x")
	if !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("kill-session err = %v, want ErrNoServer", err)
	}
}

func TestIntegrationNotFound(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	_, err := srv.Client.Run(ctx, "kill-session", "-t", "=nope")
	if !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	_, err = srv.Client.CapturePane(ctx, "%999", 0)
	if !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("capture err = %v, want ErrNotFound", err)
	}
}

func TestIntegrationCapturePane(t *testing.T) {
	srv := tmuxtest.Start(t)
	ctx := tmuxtest.Context(t)
	// The pane signals a tmux channel once its output is written; wait-for
	// latches the signal, so there is no race and no sleeping.
	script := "printf 'hello \\033[31mred\\033[0m\\n'; " + tmux.ShellQuote(srv.Bin) + " wait-for -S lt-cap-ready; sleep 3600"
	if _, err := srv.Client.Run(ctx, "new-session", "-d", "-s", "cap", "-x", "80", "-y", "10", script); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Client.Run(ctx, "wait-for", "lt-cap-ready"); err != nil {
		t.Fatal(err)
	}
	panes, err := srv.Client.ListPanes(ctx, tmux.ExactSession("cap"))
	if err != nil || len(panes) != 1 {
		t.Fatalf("panes %v %v", panes, err)
	}
	out, err := srv.Client.CapturePane(ctx, panes[0].ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "\x1b[31m") {
		t.Fatalf("capture = %q", out)
	}
}
