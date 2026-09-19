package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/layout"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// referencePath is the configuration reference, from this package directory.
var referencePath = filepath.Join("..", "..", "docs", "configuration.md")

func readReference(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestReferenceShowsTheTemplate keeps the template the reference prints
// identical to the file lmux config init writes, which is what it says.
func TestReferenceShowsTheTemplate(t *testing.T) {
	const fence, end = "```toml\n", "\n```\n"
	_, section, ok := strings.Cut(readReference(t), "\n## The template\n")
	if !ok {
		t.Fatal("the reference has no template section")
	}
	_, block, ok := strings.Cut(section, fence)
	if !ok {
		t.Fatal("the template section has no toml block")
	}
	block, _, ok = strings.Cut(block, end)
	if !ok {
		t.Fatal("the template block is not closed")
	}
	if got := []byte(block + "\n"); !bytes.Equal(got, Template()) {
		t.Fatalf("docs/configuration.md shows a template that is not internal/config/template.toml\n%s", golden.Diff(Template(), got))
	}
}

// referenceRows maps every "section.key" row of the reference's key tables to
// its Values cell. The pane fields of a custom layout are keyed the way the
// validator keys them, under layouts.panes.
func referenceRows(t *testing.T) map[string]string {
	t.Helper()
	heading := regexp.MustCompile("^## `\\[([a-z_]+)(?:\\.<name>)?\\]`$")
	row := regexp.MustCompile("^\\| `([a-z_]+)` \\| [^|]+ \\| ([^|]+) \\|")
	rows := map[string]string{}
	section := ""
	for line := range strings.Lines(readReference(t)) {
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "## ") {
			section = ""
			if m := heading.FindStringSubmatch(line); m != nil {
				section = m[1]
				if section == "layouts" {
					section = "layouts.panes"
				}
			}
			continue
		}
		if m := row.FindStringSubmatch(line); m != nil && section != "" {
			key := section + "." + m[1]
			if _, dup := rows[key]; dup {
				t.Fatalf("the reference describes %s twice", key)
			}
			rows[key] = m[2]
		}
	}
	return rows
}

// configKeys lists every key the file takes, as "section.key".
func configKeys() []string {
	var keys []string
	add := func(section string, typ reflect.Type) {
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
			keys = append(keys, section+"."+name)
		}
	}
	for f := range reflect.TypeFor[Config]().Fields() {
		section, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if f.Type.Kind() == reflect.Map {
			add(section+".panes", reflect.TypeFor[Pane]())
			continue
		}
		add(section, f.Type)
	}
	slices.Sort(keys)
	return keys
}

// TestReferenceDescribesEveryKey holds the reference to the file: a row for
// every key the decoder takes, and no row for a key it rejects.
func TestReferenceDescribesEveryKey(t *testing.T) {
	rows := referenceRows(t)
	documented := make([]string, 0, len(rows))
	for key := range rows {
		documented = append(documented, key)
	}
	slices.Sort(documented)
	keys := configKeys()
	if len(keys) < 40 {
		t.Fatalf("only %d keys found; the walk no longer matches the configuration", len(keys))
	}
	for _, key := range keys {
		if !slices.Contains(documented, key) {
			t.Errorf("docs/configuration.md has no row for %s", key)
		}
	}
	for _, key := range documented {
		if !slices.Contains(keys, key) {
			t.Errorf("docs/configuration.md describes %s, which the configuration does not take", key)
		}
	}
}

// TestReferenceNamesEveryChoice keeps the Values column in step with the
// validator: the row of an enumerated key names every value it accepts.
func TestReferenceNamesEveryChoice(t *testing.T) {
	rows := referenceRows(t)
	for key, values := range choices {
		t.Run(key, func(t *testing.T) {
			cell, ok := rows[key]
			if !ok {
				t.Fatalf("docs/configuration.md has no row for %s", key)
			}
			for _, v := range values {
				if !strings.Contains(cell, "`"+v+"`") {
					t.Errorf("the row for %s does not name %q: %s", key, v, cell)
				}
			}
		})
	}
}

// TestReferenceNamesEveryBuiltinLayout keeps the list of names a custom
// layout cannot take in step with the built-in layouts.
func TestReferenceNamesEveryBuiltinLayout(t *testing.T) {
	const intro = "- A built-in name ("
	_, rule, ok := strings.Cut(readReference(t), intro)
	if !ok {
		t.Fatal("the reference no longer lists the built-in layout names")
	}
	rule, _, _ = strings.Cut(rule, ")")
	var named []string
	for part := range strings.SplitSeq(rule, ", ") {
		named = append(named, strings.Trim(part, "`"))
	}
	if !slices.Equal(named, layout.Names()) {
		t.Fatalf("the reference lists %q as built-in layouts, want %q", named, layout.Names())
	}
}
