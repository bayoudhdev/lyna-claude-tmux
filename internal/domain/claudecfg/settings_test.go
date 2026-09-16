package claudecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/hookevent"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// goldenBin has a space so the goldens show the quoting a shell needs.
const goldenBin = "/Users/dev/My Tools/lyna-tmux"

func resolve(t *testing.T, in sandbox.Input) sandbox.Resolution {
	t.Helper()
	res, err := sandbox.Resolve(in)
	if err != nil {
		t.Fatalf("sandbox.Resolve: %v", err)
	}
	return res
}

func TestBuildSettingsGolden(t *testing.T) {
	ecosystems := sandbox.DetectEcosystems(func(rel string) bool { return rel == "go.mod" || rel == "package.json" })
	cases := []struct {
		name   string
		golden string
		in     SettingsInput
	}{
		{
			name:   "standard profile",
			golden: "settings/standard.golden",
			in:     SettingsInput{Bin: goldenBin, Sandbox: resolve(t, sandbox.Input{Profile: sandbox.Standard})},
		},
		{
			name:   "strict profile with go and node detected",
			golden: "settings/strict-go-node.golden",
			in: SettingsInput{Bin: goldenBin, Sandbox: resolve(t, sandbox.Input{
				Profile:    sandbox.Strict,
				Ecosystems: ecosystems,
				Extras:     sandbox.Extras{AllowWrite: []string{"~/.cache/go-build"}, AllowedDomains: []string{"mirror.example.org"}},
			})},
		},
		{
			name:   "off profile keeps user status line",
			golden: "settings/off.golden",
			in:     SettingsInput{Bin: goldenBin, UserHasStatusLine: true, Sandbox: resolve(t, sandbox.Input{Profile: sandbox.Off})},
		},
		{
			name:   "teams with lyna status line worktree base and workflow size",
			golden: "settings/teams.golden",
			in: SettingsInput{
				Bin: goldenBin, StatusLine: StatusLineLyna, UserHasStatusLine: true, Teams: true,
				WorktreeBaseRef: "head", WorkflowSize: "small",
				Env:     map[string]string{"HTTPS_PROXY": "http://proxy.internal:3128"},
				Sandbox: resolve(t, sandbox.Input{Profile: sandbox.Standard, Isolation: sandbox.IsolationContainer}),
			},
		},
		{
			name:   "process isolation turns the bash sandbox off and scrubs subprocesses",
			golden: "settings/process-standard.golden",
			in:     SettingsInput{Bin: goldenBin, Sandbox: resolve(t, sandbox.Input{Profile: sandbox.Standard, Isolation: sandbox.IsolationProcess})},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildSettings(tc.in)
			if err != nil {
				t.Fatalf("BuildSettings: %v", err)
			}
			golden.Assert(t, tc.golden, got)
			if !json.Valid(got) || !bytes.HasSuffix(got, []byte("}\n")) {
				t.Fatalf("settings are not a JSON document with a trailing newline:\n%s", got)
			}
			again, _ := BuildSettings(tc.in)
			if !bytes.Equal(got, again) {
				t.Fatal("BuildSettings is not deterministic")
			}
		})
	}
}

// decoded mirrors the settings keys the tests inspect.
type decoded struct {
	Env   map[string]string `json:"env"`
	Hooks map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Async   *bool  `json:"async"`
		} `json:"hooks"`
	} `json:"hooks"`
	Permissions           map[string]any    `json:"permissions"`
	Sandbox               map[string]any    `json:"sandbox"`
	StatusLine            map[string]string `json:"statusLine"`
	TeammateMode          *string           `json:"teammateMode"`
	Worktree              map[string]string `json:"worktree"`
	WorkflowSizeGuideline *string           `json:"workflowSizeGuideline"`
}

func build(t *testing.T, in SettingsInput) decoded {
	t.Helper()
	data, err := BuildSettings(in)
	if err != nil {
		t.Fatalf("BuildSettings: %v", err)
	}
	var d decoded
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("decode settings: %v\n%s", err, data)
	}
	return d
}

func TestBuildSettingsHooksFollowRegistrations(t *testing.T) {
	data, err := BuildSettings(SettingsInput{Bin: "/usr/local/bin/lyna-tmux", Sandbox: resolve(t, sandbox.Input{})})
	if err != nil {
		t.Fatal(err)
	}
	d := build(t, SettingsInput{Bin: "/usr/local/bin/lyna-tmux", Sandbox: resolve(t, sandbox.Input{})})
	regs := hookevent.Registrations()
	if len(d.Hooks) != len(regs) {
		t.Fatalf("got %d hook events, want %d", len(d.Hooks), len(regs))
	}
	last := -1
	for _, reg := range regs {
		t.Run(string(reg.Event), func(t *testing.T) {
			groups := d.Hooks[string(reg.Event)]
			if len(groups) != 1 || len(groups[0].Hooks) != 1 {
				t.Fatalf("event %s: %+v, want one group with one handler", reg.Event, groups)
			}
			g := groups[0]
			switch {
			case reg.Matcher == "" && g.Matcher != nil:
				t.Errorf("matcher = %q, want omitted", *g.Matcher)
			case reg.Matcher != "" && (g.Matcher == nil || *g.Matcher != reg.Matcher):
				t.Errorf("matcher = %v, want %q", g.Matcher, reg.Matcher)
			}
			h := g.Hooks[0]
			if h.Type != "command" || h.Async == nil || !*h.Async {
				t.Errorf("handler %+v, want type command and async true", h)
			}
			if want := "/usr/local/bin/lyna-tmux hook " + string(reg.Event); h.Command != want {
				t.Errorf("command = %q, want %q", h.Command, want)
			}
			// Events appear in registration order in the document.
			at := bytes.Index(data, []byte(`"`+string(reg.Event)+`": [`))
			if at <= last {
				t.Errorf("event %s at offset %d is not after the previous event (%d)", reg.Event, at, last)
			}
			last = at
		})
	}
}

func TestBuildSettingsOptions(t *testing.T) {
	std := resolve(t, sandbox.Input{})
	strict := resolve(t, sandbox.Input{Profile: sandbox.Strict})
	bin := "/opt/lyna/bin/lyna-tmux"
	cases := []struct {
		name  string
		in    SettingsInput
		check func(t *testing.T, d decoded)
	}{
		{
			name: "auto status line without a user status line",
			in:   SettingsInput{Bin: bin, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				want := map[string]string{"type": "command", "command": bin + " statusline"}
				if !mapsEqual(d.StatusLine, want) {
					t.Fatalf("statusLine = %v, want %v", d.StatusLine, want)
				}
			},
		},
		{
			name: "auto status line keeps the user's",
			in:   SettingsInput{Bin: bin, StatusLine: StatusLineAuto, UserHasStatusLine: true, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.StatusLine != nil {
					t.Fatalf("statusLine = %v, want omitted", d.StatusLine)
				}
			},
		},
		{
			name: "lyna status line replaces the user's",
			in:   SettingsInput{Bin: bin, StatusLine: StatusLineLyna, UserHasStatusLine: true, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.StatusLine["command"] != bin+" statusline" {
					t.Fatalf("statusLine = %v", d.StatusLine)
				}
			},
		},
		{
			name: "status line off",
			in:   SettingsInput{Bin: bin, StatusLine: StatusLineOff, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.StatusLine != nil {
					t.Fatalf("statusLine = %v, want omitted", d.StatusLine)
				}
			},
		},
		{
			name: "teams set teammate mode tmux",
			in:   SettingsInput{Bin: bin, Teams: true, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.TeammateMode == nil || *d.TeammateMode != "tmux" {
					t.Fatalf("teammateMode = %v, want tmux", d.TeammateMode)
				}
			},
		},
		{
			name: "no teams no teammate mode",
			in:   SettingsInput{Bin: bin, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.TeammateMode != nil || d.Worktree != nil || d.WorkflowSizeGuideline != nil {
					t.Fatalf("unexpected optional keys: %+v", d)
				}
			},
		},
		{
			name: "worktree base and workflow size",
			in:   SettingsInput{Bin: bin, WorktreeBaseRef: "fresh", WorkflowSize: "unrestricted", Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.Worktree["baseRef"] != "fresh" || d.WorkflowSizeGuideline == nil || *d.WorkflowSizeGuideline != "unrestricted" {
					t.Fatalf("worktree = %v, workflowSizeGuideline = %v", d.Worktree, d.WorkflowSizeGuideline)
				}
			},
		},
		{
			name: "strict env scrub lands in env block with extra env",
			in:   SettingsInput{Bin: bin, Sandbox: strict, Env: map[string]string{"NO_PROXY": "localhost", sandbox.EnvSubprocessScrub: "1"}},
			check: func(t *testing.T, d decoded) {
				want := map[string]string{"NO_PROXY": "localhost", sandbox.EnvSubprocessScrub: "1"}
				if !mapsEqual(d.Env, want) {
					t.Fatalf("env = %v, want %v", d.Env, want)
				}
			},
		},
		{
			name: "standard has no env block",
			in:   SettingsInput{Bin: bin, Sandbox: std},
			check: func(t *testing.T, d decoded) {
				if d.Env != nil {
					t.Fatalf("env = %v, want omitted", d.Env)
				}
			},
		},
		{
			name: "off writes sandbox disabled",
			in:   SettingsInput{Bin: bin, Sandbox: resolve(t, sandbox.Input{Profile: sandbox.Off, Isolation: sandbox.IsolationContainer})},
			check: func(t *testing.T, d decoded) {
				if len(d.Sandbox) != 1 || d.Sandbox["enabled"] != false {
					t.Fatalf("sandbox = %v, want exactly enabled false", d.Sandbox)
				}
				if d.Permissions != nil {
					t.Fatalf("permissions = %v, want omitted when nothing is set", d.Permissions)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, build(t, tc.in))
		})
	}
}

func TestBuildSettingsErrors(t *testing.T) {
	std := resolve(t, sandbox.Input{})
	strict := resolve(t, sandbox.Input{Profile: sandbox.Strict})
	cases := []struct {
		name string
		in   SettingsInput
		want string
	}{
		{name: "relative bin", in: SettingsInput{Bin: "lyna-tmux", Sandbox: std}, want: "absolute path"},
		{name: "empty bin", in: SettingsInput{Sandbox: std}, want: "absolute path"},
		{name: "newline in bin", in: SettingsInput{Bin: "/bin/lyna\n-tmux", Sandbox: std}, want: "control characters"},
		{name: "bad status line", in: SettingsInput{Bin: "/b", StatusLine: "always", Sandbox: std}, want: "status line must be"},
		{name: "bad worktree base", in: SettingsInput{Bin: "/b", WorktreeBaseRef: "main", Sandbox: std}, want: "worktree base must be"},
		{name: "bad workflow size", in: SettingsInput{Bin: "/b", WorkflowSize: "huge", Sandbox: std}, want: "workflow size must be"},
		{name: "bad env name", in: SettingsInput{Bin: "/b", Env: map[string]string{"1BAD": "x"}, Sandbox: std}, want: "environment variable name"},
		{name: "env value with NUL", in: SettingsInput{Bin: "/b", Env: map[string]string{"GOOD": "a\x00b"}, Sandbox: std}, want: "NUL byte"},
		{name: "env override of sandbox value", in: SettingsInput{Bin: "/b", Env: map[string]string{sandbox.EnvSubprocessScrub: "0"}, Sandbox: strict}, want: "cannot be overridden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildSettings(tc.in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildSettings error = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildSettingsDoesNotEscapeHTML(t *testing.T) {
	data, err := BuildSettings(SettingsInput{Bin: "/tmp/a&b<c>/lyna-tmux", Sandbox: resolve(t, sandbox.Input{})})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"command": "'/tmp/a&b<c>/lyna-tmux' hook SessionStart"`)) {
		t.Fatalf("command was escaped or misquoted:\n%s", data)
	}
}

func TestParseStatusLineMode(t *testing.T) {
	cases := []struct {
		in      string
		want    StatusLineMode
		wantErr bool
	}{
		{in: "", want: StatusLineAuto},
		{in: "auto", want: StatusLineAuto},
		{in: "lyna", want: StatusLineLyna},
		{in: "off", want: StatusLineOff},
		{in: "on", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseStatusLineMode(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("ParseStatusLineMode(%q) = %q, %v", tc.in, got, err)
			}
		})
	}
}

func TestSettingsFileName(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "e3b0c44298fc1c14.json"},
		{name: "document", data: "{}\n", want: "ca3d163bab055381.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SettingsFileName([]byte(tc.data))
			if got != tc.want || !IsSettingsFileName(got) {
				t.Fatalf("SettingsFileName = %q, want %q (valid shape)", got, tc.want)
			}
		})
	}
	if SettingsFileName([]byte("a")) == SettingsFileName([]byte("b")) {
		t.Fatal("different content shares a file name")
	}
}

func TestIsSettingsFileName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "0123456789abcdef.json", want: true},
		{in: "0123456789ABCDEF.json"},
		{in: "0123456789abcde.json"},
		{in: "0123456789abcdef.json.tmp"},
		{in: ".0123456789abcdef.json"},
		{in: "settings.json"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := IsSettingsFileName(tc.in); got != tc.want {
				t.Fatalf("IsSettingsFileName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestUserHasStatusLine(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		want    bool
		wantErr bool
	}{
		{name: "empty file", data: "", want: false},
		{name: "whitespace", data: " \n\t", want: false},
		{name: "no status line", data: `{"theme":"dark"}`, want: false},
		{name: "command status line", data: `{"statusLine":{"type":"command","command":"~/bin/sl"}}`, want: true},
		{name: "null status line", data: `{"statusLine":null}`, want: false},
		{name: "nested key does not count", data: `{"env":{"statusLine":"x"}}`, want: false},
		{name: "byte order mark", data: "\xef\xbb\xbf{\"statusLine\":{}}", want: true},
		{name: "duplicate key last wins", data: `{"statusLine":{},"statusLine":null}`, want: false},
		{name: "array document", data: `[]`, wantErr: true},
		{name: "null document", data: `null`, wantErr: true},
		{name: "broken json", data: `{"statusLine":`, wantErr: true},
		{name: "comments are not json", data: "{// c\n}", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UserHasStatusLine([]byte(tc.data))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("UserHasStatusLine(%q) = %v, %v; want %v, error %v", tc.data, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func FuzzUserHasStatusLine(f *testing.F) {
	for _, s := range []string{"", "{}", `{"statusLine":{}}`, `{"statusLine":null}`, "[1]", "\xef\xbb\xbf{}", `{"a":{"statusLine":1}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := UserHasStatusLine(data)
		if err != nil {
			if got {
				t.Fatal("error with a positive answer")
			}
			return
		}
		trimmed := bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
		if len(trimmed) == 0 {
			if got {
				t.Fatal("empty input reported a status line")
			}
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			t.Fatalf("accepted input encoding/json rejects: %v", err)
		}
		if want := doc["statusLine"] != nil; got != want {
			t.Fatalf("UserHasStatusLine = %v, want %v for %q", got, want, data)
		}
	})
}

func TestHookAndStatusLineCommands(t *testing.T) {
	cases := []struct {
		name string
		bin  string
		hook string
		sl   string
	}{
		{name: "plain path", bin: "/usr/bin/lyna-tmux", hook: "/usr/bin/lyna-tmux hook Stop", sl: "/usr/bin/lyna-tmux statusline"},
		{name: "space", bin: "/a b/lyna-tmux", hook: "'/a b/lyna-tmux' hook Stop", sl: "'/a b/lyna-tmux' statusline"},
		{name: "single quote", bin: "/it's/lyna-tmux", hook: `'/it'\''s/lyna-tmux' hook Stop`, sl: `'/it'\''s/lyna-tmux' statusline`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HookCommand(tc.bin, hookevent.Stop); got != tc.hook {
				t.Errorf("HookCommand = %q, want %q", got, tc.hook)
			}
			if got := StatusLineCommand(tc.bin); got != tc.sl {
				t.Errorf("StatusLineCommand = %q, want %q", got, tc.sl)
			}
		})
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	return !slices.ContainsFunc(keys, func(k string) bool { v, ok := b[k]; return !ok || v != a[k] })
}
