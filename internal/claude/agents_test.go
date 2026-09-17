package claude_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/claude"
)

// def is one agent definition file, as Claude Code writes them.
func def(name, description, model string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\nmodel: " + model +
		"\n---\n\nYou are " + name + ".\n"
}

// writeDefs lays out a directory of agent definitions.
func writeDefs(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAgentDefs reads the definitions a workspace can spawn from the project
// and from the user, and covers the files that are not definitions.
func TestAgentDefs(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeDefs(t, filepath.Join(home, "agents"), map[string]string{
		"reviewer.md":   def("reviewer", "the user's own reviewer", "opus"),
		"writer.md":     def("writer", "writes the documentation", "sonnet"),
		"notes.txt":     def("notes", "not a definition file", ""),
		"empty.md":      "",
		"no-header.md":  "# just a note\n",
		"unreadable.md": def("", "a definition that names nobody", ""),
		"deep":          "",
	})
	if err := os.MkdirAll(filepath.Join(home, "agents", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeDefs(t, filepath.Join(home, "agents", "sub"), map[string]string{
		"hidden.md": def("hidden", "a definition in a directory of its own", ""),
	})
	writeDefs(t, filepath.Join(root, ".claude", "agents"), map[string]string{
		"reviewer.md": def("reviewer", "the project's own reviewer", "haiku"),
		"builder.md":  def("builder", "builds the project", ""),
	})

	defs := claude.AgentDefs(root, home)
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if got, want := strings.Join(names, " "), "builder reviewer writer"; got != want {
		t.Fatalf("definitions %q, want %q", got, want)
	}
	// The project wins where both name the same agent, as Claude Code resolves
	// them, and a definition in a directory of its own is not one of these.
	if defs[1].Model != "haiku" || defs[1].Description != "the project's own reviewer" {
		t.Fatalf("the user's reviewer won: %+v", defs[1])
	}
}

// TestAgentDefsWithoutADirectory covers the workspaces that offer the built-in
// agents alone: no project directory, no user directory, no directory at all.
func TestAgentDefsWithoutADirectory(t *testing.T) {
	home := t.TempDir()
	writeDefs(t, filepath.Join(home, "agents"), map[string]string{
		"writer.md": def("writer", "writes the documentation", ""),
	})
	cases := []struct {
		name, root, home string
		want             int
	}{
		{name: "a project with no definitions", root: t.TempDir(), home: home, want: 1},
		{name: "a user with no definitions", root: t.TempDir(), home: t.TempDir()},
		{name: "no project", home: home, want: 1},
		{name: "no configuration directory", root: t.TempDir()},
		{name: "neither"},
		{name: "a file where the directory goes", root: t.TempDir(), home: fileHome(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(claude.AgentDefs(tc.root, tc.home)); got != tc.want {
				t.Fatalf("%d definitions, want %d", got, tc.want)
			}
		})
	}
}

// fileHome is a configuration directory whose agents directory is a file.
func fileHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "agents"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestAgentDefsAreBounded keeps a generated directory from filling a form
// nobody can scroll, and takes the same definitions every time.
func TestAgentDefsAreBounded(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{}
	for i := range claude.MaxAgentDefs + 20 {
		name := "agent-" + strconv.Itoa(1000+i)
		files[name+".md"] = def(name, "one of many", "")
	}
	writeDefs(t, filepath.Join(home, "agents"), files)
	defs := claude.AgentDefs("", home)
	if len(defs) != claude.MaxAgentDefs {
		t.Fatalf("%d definitions, want %d", len(defs), claude.MaxAgentDefs)
	}
	if defs[0].Name != "agent-1000" {
		t.Fatalf("the listing starts at %q, want the first name in order", defs[0].Name)
	}
}
