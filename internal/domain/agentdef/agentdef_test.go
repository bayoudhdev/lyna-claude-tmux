package agentdef_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/agentdef"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "agents", name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNameFromFile(t *testing.T) {
	cases := []struct {
		name string
		file string
		want string
	}{
		{name: "a definition is named after its agent", file: "security-reviewer.md", want: "security-reviewer"},
		{name: "a name with a dot", file: "api.v2.md", want: "api.v2"},
		{name: "a file of another kind", file: "README.txt"},
		{name: "a hidden file", file: ".md"},
		{name: "a directory entry that is a path", file: "sub/agent.md"},
		{name: "a windows path", file: `sub\agent.md`},
		{name: "no name at all", file: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := agentdef.NameFromFile(tc.file)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("NameFromFile(%q) = %q, %v; want %q", tc.file, got, ok, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		want    agentdef.Def
	}{
		{
			name: "a definition written the way the product writes one", fixture: "explore",
			want: agentdef.Def{
				Name:        "Explore",
				Description: "Fast read-only search of the project. Use it to locate files, symbols and patterns when only the conclusion is needed.",
				Model:       "opus",
				Color:       "green",
				Effort:      "low",
				Tools:       []string{"Read", "Grep", "Glob", "Bash"},
			},
		},
		{
			name: "a definition written by hand", fixture: "reviewer",
			want: agentdef.Def{
				Name:        "security-reviewer",
				Description: "Reviews a change for injection, authorization and secrets.\nRead only: it reports findings with a file and a line, and never edits.",
				Model:       "inherit",
				Color:       "red",
				Tools:       []string{"Read", "Grep", "Glob"},
				Disallowed:  []string{"Bash", "Write"},
			},
		},
		{
			name: "a description folded into one line", fixture: "folded",
			want: agentdef.Def{
				Name:        "docs",
				Description: "Writes the release note in one paragraph.",
			},
		},
		{
			name: "a definition written on another platform", fixture: "crlf",
			want: agentdef.Def{
				Name:        "crlf",
				Description: "A definition written on another platform.",
				Tools:       []string{"Read", "Grep"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agentdef.Parse(read(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.want.Name || got.Description != tc.want.Description ||
				got.Model != tc.want.Model || got.Color != tc.want.Color || got.Effort != tc.want.Effort {
				t.Fatalf("definition %+v, want %+v", got, tc.want)
			}
			if !slices.Equal(got.Tools, tc.want.Tools) {
				t.Fatalf("tools %v, want %v", got.Tools, tc.want.Tools)
			}
			if !slices.Equal(got.Disallowed, tc.want.Disallowed) {
				t.Fatalf("disallowed tools %v, want %v", got.Disallowed, tc.want.Disallowed)
			}
		})
	}
}

func TestParseRefusals(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		wantErr error
	}{
		{name: "a markdown file that is not a definition", data: read(t, "no-header"), wantErr: agentdef.ErrNoHeader},
		{name: "a definition that names no agent", data: read(t, "no-name"), wantErr: agentdef.ErrNoName},
		{name: "a header nobody closed", data: []byte("---\nname: open\n"), wantErr: agentdef.ErrNoHeader},
		{name: "nothing at all", data: nil, wantErr: agentdef.ErrNoHeader},
		{name: "a file past the cap", data: append([]byte("---\nname: big\n---\n"), make([]byte, agentdef.MaxSize)...), wantErr: agentdef.ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, err := agentdef.Parse(tc.data)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v, want %v (definition %+v)", err, tc.wantErr, def)
			}
		})
	}
}

func TestParseReadsWhatItUnderstandsAndSkipsTheRest(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		check func(t *testing.T, def agentdef.Def)
	}{
		{
			name: "a key of a shape this product does not know is skipped",
			text: "---\nname: a\nmcpServers:\n  api:\n    command: serve\ndescription: kept\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Description != "kept" {
					t.Fatalf("description %q", def.Description)
				}
			},
		},
		{
			name: "a line of prose does not disturb the keys around it",
			text: "---\nname: a\nthis line has: two: colons and is not ours\ncolor: blue\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Color != "blue" {
					t.Fatalf("color %q", def.Color)
				}
			},
		},
		{
			name: "the last spelling of a key wins",
			text: "---\nname: a\nmodel: opus\nmodel: inherit\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Model != "inherit" {
					t.Fatalf("model %q", def.Model)
				}
			},
		},
		{
			name: "a comment at the end of a value is not part of it",
			text: "---\nname: a\nmodel: opus # the one the team runs\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Model != "opus" {
					t.Fatalf("model %q", def.Model)
				}
			},
		},
		{
			name: "a quoted value keeps everything it holds",
			text: "---\nname: a\ndescription: \"reads #tags and \\\"quotes\\\"\"\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Description != `reads #tags and "quotes"` {
					t.Fatalf("description %q", def.Description)
				}
			},
		},
		{
			name: "a definition that names no tool inherits every tool",
			text: "---\nname: a\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Tools != nil {
					t.Fatalf("tools %v, want none named", def.Tools)
				}
			},
		},
		{
			name: "a header closed the way a document ends",
			text: "---\nname: a\ncolor: red\n...\nbody\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Color != "red" {
					t.Fatalf("color %q", def.Color)
				}
			},
		},
		{
			name: "an indented item after a value is not a list",
			text: "---\nname: a\ndescription: one line\n  - not an item\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Description != "one line" {
					t.Fatalf("description %q", def.Description)
				}
			},
		},
		{
			name: "a block scalar ends where the indentation does",
			text: "---\nname: a\ndescription: |\n  first\n  second\ncolor: blue\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Description != "first\nsecond" || def.Color != "blue" {
					t.Fatalf("description %q color %q", def.Description, def.Color)
				}
			},
		},
		{
			name: "a value that begins with a bracket and does not close is a scalar",
			text: "---\nname: a\nmodel: [opus\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Model != "[opus" {
					t.Fatalf("model %q", def.Model)
				}
			},
		},
		{
			name: "a line naming no key does not disturb the keys around it",
			text: "---\nname: a\n: stray\ncolor: blue\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Color != "blue" {
					t.Fatalf("color %q", def.Color)
				}
			},
		},
		{
			name: "a value that begins with a fold marker and keeps going is a scalar",
			text: "---\nname: a\nmodel: >see the note\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if def.Model != ">see the note" {
					t.Fatalf("model %q", def.Model)
				}
			},
		},
		{
			name: "a list is read whether it is written inline or as items",
			text: "---\nname: a\ntools: [Read, \"Grep\"]\n---\n",
			check: func(t *testing.T, def agentdef.Def) {
				if !slices.Equal(def.Tools, []string{"Read", "Grep"}) {
					t.Fatalf("tools %v", def.Tools)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, err := agentdef.Parse([]byte(tc.text))
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, def)
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"explore", "reviewer", "folded", "crlf", "no-name", "no-header"} {
		data, err := os.ReadFile(filepath.Join("testdata", "agents", name+".md"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		def, err := agentdef.Parse(data)
		if err != nil {
			return
		}
		if def.Name == "" {
			t.Fatalf("a definition with no name survived: %+v", def)
		}
		for _, tool := range append(slices.Clone(def.Tools), def.Disallowed...) {
			if tool == "" {
				t.Fatalf("an empty tool name survived: %+v", def)
			}
		}
	})
}
