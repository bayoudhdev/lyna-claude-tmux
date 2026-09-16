package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitConfigCLI(t *testing.T) {
	e := newInfraEnv(t)
	configFile := filepath.Join(e.host.Home, "config", "config.toml")
	code, stdout, stderr := e.run(t, "init")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"Wrote " + configFile, "Next steps:", "lyna-tmux doctor", "lyna-tmux init --project", "lyna-tmux create"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	data, err := os.ReadFile(configFile)
	if err != nil || !strings.Contains(string(data), "[sandbox]") {
		t.Fatalf("configuration file %q, %v", data, err)
	}
	code, stdout, stderr = e.run(t, "init")
	if code != 0 || !strings.Contains(stdout, "Kept "+configFile) {
		t.Fatalf("second init: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if again, _ := os.ReadFile(configFile); string(again) != string(data) {
		t.Fatal("the second init rewrote the configuration file")
	}
	cases := []struct {
		name   string
		args   []string
		errHas string
	}{
		{name: "directory without --project", args: []string{"init", "."}, errHas: "init --project"},
		{name: "--yes without --project", args: []string{"init", "--yes"}, errHas: "init --project"},
		{name: "--dry-run without --project", args: []string{"init", "--dry-run"}, errHas: "init --project"},
		{name: "too many arguments", args: []string{"init", "--project", "a", "b"}, errHas: "accepts at most 1 arg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, stderr := e.run(t, tc.args...); code != 1 || !containsFolded(stderr, tc.errHas) {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
		})
	}
}

func TestInitProjectCLI(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		term      bool
		stdin     string
		config    string
		wantCode  int
		wantWrite bool
		outHas    []string
		errHas    []string
	}{
		{
			name: "dry run shows the diff and writes nothing", args: []string{"init", "--project", "--dry-run"},
			outHas: []string{".claude/settings.json (new file)", "+  \"sandbox\": {", ".worktreeinclude (new file)", "+.env", ".gitignore (new file)", "Dry run: nothing was written."},
		},
		{
			name: "--yes applies without a terminal", args: []string{"init", "--project", "--yes"}, wantWrite: true,
			outHas: []string{"Wrote ", ".claude/settings.json"},
		},
		{
			name: "confirmed on a terminal", args: []string{"init", "--project"}, term: true, stdin: "y\n", wantWrite: true,
			outHas: []string{"Apply these changes to", "[y/N]", "Wrote "},
		},
		{
			name: "declined", args: []string{"init", "--project"}, term: true, stdin: "n\n", wantCode: 1,
			errHas: []string{"canceled"},
		},
		{
			name: "without a terminal and without --yes", args: []string{"init", "--project"}, wantCode: 1,
			errHas: []string{"--yes", "--dry-run"},
		},
		{
			name: "the sandbox may not be off", args: []string{"init", "--project", "--yes"}, config: "[sandbox]\nprofile = \"off\"\n", wantCode: 1,
			errHas: []string{"lyna-tmux config edit"},
		},
		{
			name: "strict profile", args: []string{"init", "--project", "--yes"}, config: "[sandbox]\nprofile = \"strict\"\n", wantWrite: true,
			outHas: []string{"sandbox profile strict"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.term = Terminal{Interactive: tc.term}
			e.stdin = strings.NewReader(tc.stdin)
			if tc.config != "" {
				sandboxWriteConfig(t, e, tc.config)
			}
			project := infraProject(t, e, "api")
			e.cwd = project
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			settings := filepath.Join(project, ".claude", "settings.json")
			_, err := os.Stat(settings)
			if (err == nil) != tc.wantWrite {
				t.Fatalf("settings written %v, want %v", err == nil, tc.wantWrite)
			}
			if !tc.wantWrite {
				return
			}
			for _, name := range []string{".worktreeinclude", ".gitignore"} {
				if _, err := os.Stat(filepath.Join(project, name)); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
			code, stdout, stderr = e.run(t, "init", "--project", "--yes")
			if code != 0 || !strings.Contains(stdout, "already has the lyna-tmux project settings") {
				t.Fatalf("second run: exit %d\n%s\n%s", code, stdout, stderr)
			}
		})
	}
}

func TestInitProjectBacksUpAndMergesCLI(t *testing.T) {
	e := newInfraEnv(t)
	project := infraProject(t, e, "api")
	e.cwd = project
	settings := filepath.Join(project, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"model\": \"opus\"\n}\n"
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run(t, "init", "--project", "--yes")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Saved the previous file to "+settings+".bak") {
		t.Fatalf("no backup line:\n%s", stdout)
	}
	if backup, _ := os.ReadFile(settings + ".bak"); string(backup) != original {
		t.Fatalf("backup %q", backup)
	}
	merged, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(merged), `"model": "opus"`) || !strings.Contains(string(merged), `"sandbox"`) {
		t.Fatalf("merged settings %q, %v", merged, err)
	}
	ignore, err := os.ReadFile(filepath.Join(project, ".gitignore"))
	if err != nil || !strings.HasPrefix(string(ignore), "dist/\n") || !strings.Contains(string(ignore), ".claude/worktrees/") {
		t.Fatalf("gitignore %q, %v", ignore, err)
	}
}
