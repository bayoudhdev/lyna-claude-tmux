package claudecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// existingProject is a team file with unknown keys, a partial sandbox block,
// an overlapping deny rule and a credential entry the team configured itself.
const existingProject = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "model": "opus",
  "permissions": {
    "allow": ["Bash(go test:*)"],
    "deny": ["Read(.env)", "WebFetch"]
  },
  "sandbox": {
    "enabled": true,
    "filesystem": {"allowWrite": ["/tmp/build"]},
    "credentials": {"files": [{"path": "~/.ssh", "mode": "deny"}]}
  },
  "hooks": {}
}
`

func renderProjectChange(c ProjectChange) []byte {
	var b bytes.Buffer
	b.WriteString("=== data ===\n")
	b.Write(c.Data)
	b.WriteString("=== diff ===\n")
	b.WriteString(c.Diff)
	return b.Bytes()
}

func TestProjectSettingsGolden(t *testing.T) {
	strict := resolve(t, sandbox.Input{
		Profile:    sandbox.Strict,
		Ecosystems: []sandbox.Ecosystem{sandbox.EcosystemGo},
		Extras:     sandbox.Extras{AllowWrite: []string{"/tmp/build", "~/.cache/go-build"}, ExcludedCommands: []string{"docker"}},
	})
	cases := []struct {
		name     string
		golden   string
		existing string
		res      sandbox.Resolution
	}{
		{name: "new standard file", golden: "project/new-standard.golden", existing: "", res: resolve(t, sandbox.Input{})},
		{name: "strict merged into a team file", golden: "project/merge-strict.golden", existing: existingProject, res: strict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change, err := ProjectSettings([]byte(tc.existing), tc.res)
			if err != nil {
				t.Fatalf("ProjectSettings: %v", err)
			}
			if !change.Changed || change.Diff == "" || !json.Valid(change.Data) {
				t.Fatalf("change = %+v, want a valid changed document with a diff", change)
			}
			golden.Assert(t, tc.golden, renderProjectChange(change))

			// Applying the same resolution again changes nothing.
			again, err := ProjectSettings(change.Data, tc.res)
			if err != nil {
				t.Fatalf("second ProjectSettings: %v", err)
			}
			if again.Changed || again.Diff != "" || !bytes.Equal(again.Data, change.Data) {
				t.Fatalf("second merge changed the file:\n%s", again.Diff)
			}
		})
	}
}

func TestProjectSettingsKeepsProjectChoices(t *testing.T) {
	strict := resolve(t, sandbox.Input{Profile: sandbox.Strict})
	change, err := ProjectSettings([]byte(existingProject), strict)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema      string `json:"$schema"`
		Model       string `json:"model"`
		Permissions struct {
			Allow                               []string `json:"allow"`
			Deny                                []string `json:"deny"`
			DisableBypassPermissionsMode        *string  `json:"disableBypassPermissionsMode"`
			BlockReadsOutsideWorkingDirectories bool     `json:"blockReadsOutsideWorkingDirectories"`
		} `json:"permissions"`
		Sandbox struct {
			Network *struct {
				StrictAllowlist *bool `json:"strictAllowlist"`
			} `json:"network"`
			EnableWeakerNestedSandbox *bool `json:"enableWeakerNestedSandbox"`
			Credentials               struct {
				Files []sandbox.CredentialFile `json:"files"`
			} `json:"credentials"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(change.Data, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "unknown keys kept", ok: doc.Schema != "" && doc.Model == "opus"},
		{name: "allow list untouched", ok: slices.Equal(doc.Permissions.Allow, []string{"Bash(go test:*)"})},
		{name: "deny keeps order and gains missing rule once", ok: slices.Equal(doc.Permissions.Deny, []string{"Read(.env)", "WebFetch", "Read(.env.*)"})},
		{name: "block reads outside working directories", ok: doc.Permissions.BlockReadsOutsideWorkingDirectories},
		{name: "no launcher-only bypass key", ok: doc.Permissions.DisableBypassPermissionsMode == nil},
		{name: "strict allowlist is not written to a project file", ok: doc.Sandbox.Network != nil && doc.Sandbox.Network.StrictAllowlist == nil},
		{name: "weaker nested sandbox is not written", ok: doc.Sandbox.EnableWeakerNestedSandbox == nil},
		{name: "existing credential entry first and not duplicated", ok: len(doc.Sandbox.Credentials.Files) == len(sandbox.CredentialFiles()) && doc.Sandbox.Credentials.Files[0].Path == "~/.ssh"},
		{name: "top level key order kept", ok: bytes.Index(change.Data, []byte(`"model"`)) < bytes.Index(change.Data, []byte(`"permissions"`)) && bytes.Index(change.Data, []byte(`"sandbox"`)) < bytes.Index(change.Data, []byte(`"hooks"`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.ok {
				t.Fatalf("check failed on:\n%s", change.Data)
			}
		})
	}
}

func TestProjectSettingsCases(t *testing.T) {
	std := resolve(t, sandbox.Input{})
	// Applying the standard profile once gives the document every case below
	// has to reach, whatever the input file looked like.
	first, err := ProjectSettings(nil, std)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		existing    string
		wantChanged bool
		wantData    string
	}{
		{name: "an empty file starts a new document", existing: "", wantChanged: true, wantData: string(first.Data)},
		{name: "whitespace only starts a new document", existing: " \n", wantChanged: true, wantData: string(first.Data)},
		{name: "byte order mark is dropped", existing: "\xef\xbb\xbf{}", wantChanged: true, wantData: string(first.Data)},
		{name: "a document that already says it keeps its bytes", existing: string(first.Data), wantData: string(first.Data)},
		{name: "a sandbox that was turned off is turned back on", existing: `{"sandbox":{"enabled":false}}`, wantChanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change, err := ProjectSettings([]byte(tc.existing), std)
			if err != nil {
				t.Fatalf("ProjectSettings: %v", err)
			}
			if change.Changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v, on:\n%s", change.Changed, tc.wantChanged, change.Data)
			}
			if tc.wantData != "" && string(change.Data) != tc.wantData {
				t.Fatalf("data = %q, want %q", change.Data, tc.wantData)
			}
			if !bytes.Contains(change.Data, []byte("\"enabled\": true")) && !bytes.Contains(change.Data, []byte(`"enabled":true`)) {
				t.Fatalf("the sandbox is not enabled:\n%s", change.Data)
			}
			if (change.Diff != "") != tc.wantChanged {
				t.Fatalf("diff = %q, want present only when changed", change.Diff)
			}
		})
	}
	// Standard on an empty file writes credentials and ask rules, not network.
	change, err := ProjectSettings(nil, std)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(change.Data, []byte(`"network"`)) || !bytes.Contains(change.Data, []byte(`"ask"`)) {
		t.Fatalf("standard project settings:\n%s", change.Data)
	}
}

func TestProjectSettingsErrors(t *testing.T) {
	strict := resolve(t, sandbox.Input{Profile: sandbox.Strict})
	cases := []struct {
		name     string
		existing string
		want     string
		invalid  bool
	}{
		{name: "array document", existing: `[]`, want: "must be a JSON object", invalid: true},
		{name: "broken json", existing: `{"sandbox":`, want: "parse project settings"},
		{name: "comments", existing: "{\n// team\n}", want: "parse project settings"},
		{name: "sandbox not an object", existing: `{"sandbox":true}`, want: `"sandbox" in project settings is not an object`, invalid: true},
		{name: "enabled not a bool", existing: `{"sandbox":{"enabled":"yes"}}`, want: `"enabled" in project settings is not true or false`, invalid: true},
		{name: "allowed domains not a list", existing: `{"sandbox":{"network":{"allowedDomains":"github.com"}}}`, want: `"allowedDomains" in project settings is not a list`, invalid: true},
		{name: "credential files not a list", existing: `{"sandbox":{"credentials":{"files":{}}}}`, want: `"files" in project settings is not a list`, invalid: true},
		{name: "permissions not an object", existing: `{"permissions":[]}`, want: `"permissions" in project settings is not an object`, invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProjectSettings([]byte(tc.existing), strict)
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrInvalid) != tc.invalid {
				t.Fatalf("ProjectSettings error = %v, want %q (ErrInvalid %v)", err, tc.want, tc.invalid)
			}
		})
	}
}

func TestProjectLineLists(t *testing.T) {
	if got := WorktreeIncludeLines(); !slices.Equal(got, []string{".env", ".env.*"}) {
		t.Fatalf("WorktreeIncludeLines() = %v", got)
	}
	if got := GitignoreLines(); !slices.Equal(got, []string{".claude/worktrees/", ".claude/settings.local.json"}) {
		t.Fatalf("GitignoreLines() = %v", got)
	}
}

func TestMergeLines(t *testing.T) {
	cases := []struct {
		name      string
		existing  string
		lines     []string
		want      string
		wantAdded []string
	}{
		{name: "empty file", existing: "", lines: GitignoreLines(), want: ".claude/worktrees/\n.claude/settings.local.json\n", wantAdded: GitignoreLines()},
		{name: "missing final newline", existing: "node_modules", lines: []string{".env"}, want: "node_modules\n.env\n", wantAdded: []string{".env"}},
		{name: "already present", existing: ".env\n.env.*\n", lines: WorktreeIncludeLines(), want: ".env\n.env.*\n"},
		{name: "anchored and unslashed variants match", existing: "/.claude/worktrees\n  .claude/settings.local.json  \n", lines: GitignoreLines(), want: "/.claude/worktrees\n  .claude/settings.local.json  \n"},
		{name: "comment does not count", existing: "# .env\n", lines: []string{".env"}, want: "# .env\n.env\n", wantAdded: []string{".env"}},
		{name: "blank and duplicate input lines", existing: "a\n", lines: []string{" ", "b", " b "}, want: "a\nb\n", wantAdded: []string{"b"}},
		{name: "windows line endings", existing: ".env\r\n", lines: []string{".env", "x"}, want: ".env\r\nx\n", wantAdded: []string{"x"}},
		{
			name: "a negated path is left alone", existing: "!.claude/worktrees/\n", lines: GitignoreLines(),
			want: "!.claude/worktrees/\n.claude/settings.local.json\n", wantAdded: []string{".claude/settings.local.json"},
		},
		{name: "a negation of the anchored form counts", existing: "!/.claude/worktrees\n", lines: []string{".claude/worktrees/"}, want: "!/.claude/worktrees\n"},
		{name: "an ignore and its negation leave the file alone", existing: ".env\n!.env\n", lines: []string{".env"}, want: ".env\n!.env\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, added := MergeLines([]byte(tc.existing), tc.lines)
			if string(out) != tc.want || !slices.Equal(added, tc.wantAdded) {
				t.Fatalf("MergeLines = %q, %q; want %q, %q", out, added, tc.want, tc.wantAdded)
			}
			again, addedAgain := MergeLines(out, tc.lines)
			if !bytes.Equal(again, out) || len(addedAgain) != 0 {
				t.Fatalf("MergeLines is not idempotent: %q, %q", again, addedAgain)
			}
		})
	}
	existing := []byte("keep\n")
	out, _ := MergeLines(existing, []string{"new"})
	if string(existing) != "keep\n" || string(out) != "keep\nnew\n" {
		t.Fatalf("MergeLines modified its input: %q -> %q", existing, out)
	}
}

// TestProjectSettingsRefusesTheSandboxOff pins that a file the whole team
// reads can never be the place the sandbox is turned off.
func TestProjectSettingsRefusesTheSandboxOff(t *testing.T) {
	off := resolve(t, sandbox.Input{Profile: sandbox.Off})
	cases := []struct {
		name     string
		existing string
	}{
		{name: "a new file"},
		{name: "an existing team file", existing: existingProject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change, err := ProjectSettings([]byte(tc.existing), off)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want %v", err, ErrInvalid)
			}
			if change.Changed || change.Data != nil {
				t.Fatalf("change = %+v, want nothing to write", change)
			}
		})
	}
}
