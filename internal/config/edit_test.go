package config

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSetString(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		section string // default "ui"
		key     string // default "theme"
		value   string // default "light"
		want    string
		wantErr error // errors.Is target; nil with errMsg unset means success
		fails   bool
	}{
		// Existing keys: only the value bytes change.
		{name: "existing key", doc: "[ui]\ntheme = \"lyna\"\nicons = \"auto\"\n", want: "[ui]\ntheme = \"light\"\nicons = \"auto\"\n"},
		{name: "existing key keeps spacing and trailing comment", doc: "[ui]\n  theme   =   'lyna'   # dark\n", want: "[ui]\n  theme   =   \"light\"   # dark\n"},
		{name: "existing quoted key", doc: "[ui]\n\"theme\" = \"lyna\"\n", want: "[ui]\n\"theme\" = \"light\"\n"},
		{name: "existing quoted section header", doc: "[ \"ui\" ] # look\ntheme = \"lyna\"\n", want: "[ \"ui\" ] # look\ntheme = \"light\"\n"},
		{name: "existing multi-line value", doc: "[ui]\ntheme = [\n  \"a\",\n  \"b\",\n] # c\nicons = \"auto\"\n", want: "[ui]\ntheme = \"light\" # c\nicons = \"auto\"\n"},
		{name: "existing top-level dotted key", doc: "ui.theme = \"lyna\"\n[workspace]\n", want: "ui.theme = \"light\"\n[workspace]\n"},
		{name: "existing key in inline table", doc: "ui = { icons = \"auto\", theme = \"lyna\" }\n", want: "ui = { icons = \"auto\", theme = \"light\" }\n"},
		{name: "existing key in multi-line inline table", doc: "ui = {\n  # the theme\n  theme = \"lyna\",\n}\n", want: "ui = {\n  # the theme\n  theme = \"light\",\n}\n"},
		{name: "existing key after sub-table header", doc: "[ui.extra]\nx = 1\n[ui]\ntheme = \"ansi\"\n", want: "[ui.extra]\nx = 1\n[ui]\ntheme = \"light\"\n"},
		{name: "fake header inside multi-line string", doc: "[claude]\nargs = \"\"\"\n[ui]\ntheme = \"x\"\n\"\"\"\n", want: "[claude]\nargs = \"\"\"\n[ui]\ntheme = \"x\"\n\"\"\"\n\n[ui]\ntheme = \"light\"\n"},

		// Missing key in an existing table: one inserted line.
		{name: "missing key after last key", doc: "[ui]\nicons = \"auto\"\n\n[workspace]\nlayout = \"duo\"\n", want: "[ui]\nicons = \"auto\"\ntheme = \"light\"\n\n[workspace]\nlayout = \"duo\"\n"},
		{name: "missing key copies indentation", doc: "[ui]\n  icons = \"auto\"\n[claude]\n", want: "[ui]\n  icons = \"auto\"\n  theme = \"light\"\n[claude]\n"},
		{name: "missing key after commented-out assignment", doc: "[ui]\n# Color theme\n# theme = \"lyna\"\n# Icon set\nicons = \"auto\"\n", want: "[ui]\n# Color theme\n# theme = \"lyna\"\ntheme = \"light\"\n# Icon set\nicons = \"auto\"\n"},
		{name: "commented-out key is never the key", doc: "[ui]\n#theme=\"ansi\"\n", want: "[ui]\n#theme=\"ansi\"\ntheme = \"light\"\n"},
		{name: "indented commented-out assignment", doc: "[ui]\nicons = \"auto\"\n  ## theme = 'x'\nclock = true\n", want: "[ui]\nicons = \"auto\"\n  ## theme = 'x'\n  theme = \"light\"\nclock = true\n"},
		{name: "comment before the table is not an anchor", doc: "# theme = \"lyna\"\n[ui]\nicons = \"auto\"\n", want: "# theme = \"lyna\"\n[ui]\nicons = \"auto\"\ntheme = \"light\"\n"},
		{name: "comment in another table is not an anchor", doc: "[ui]\nicons = \"auto\"\n[popup]\n# theme = \"lyna\"\n", want: "[ui]\nicons = \"auto\"\ntheme = \"light\"\n[popup]\n# theme = \"lyna\"\n"},
		{name: "similar commented key is not an anchor", doc: "[ui]\n# themes = 1\n# theme\nicons = \"auto\"\n", want: "[ui]\n# themes = 1\n# theme\nicons = \"auto\"\ntheme = \"light\"\n"},
		{name: "header only without line break", doc: "[ui]", want: "[ui]\ntheme = \"light\"\n"},
		{name: "last key without line break", doc: "[ui]\nicons = \"auto\"", want: "[ui]\nicons = \"auto\"\ntheme = \"light\"\n"},
		{name: "top-level dotted section", doc: "ui.icons = \"auto\"\nui.clock = true\n[workspace]\n", want: "ui.icons = \"auto\"\nui.clock = true\nui.theme = \"light\"\n[workspace]\n"},
		{name: "crlf insertion", doc: "[ui]\r\nicons = \"auto\"\r\n", want: "[ui]\r\nicons = \"auto\"\r\ntheme = \"light\"\r\n"},
		{name: "crlf document, last line without break", doc: "[a]\r\nb = 1\r\n[ui]", want: "[a]\r\nb = 1\r\n[ui]\r\ntheme = \"light\"\r\n"},

		// Missing section: appended table.
		{name: "missing section", doc: "[workspace]\nlayout = \"duo\"\n", want: "[workspace]\nlayout = \"duo\"\n\n[ui]\ntheme = \"light\"\n"},
		{name: "missing section without line break", doc: "[workspace]\nlayout = \"duo\"", want: "[workspace]\nlayout = \"duo\"\n\n[ui]\ntheme = \"light\"\n"},
		{name: "missing section after blank line", doc: "a = 1\n\n", want: "a = 1\n\n[ui]\ntheme = \"light\"\n"},
		{name: "missing section with crlf", doc: "a = 1\r\n", want: "a = 1\r\n\r\n[ui]\r\ntheme = \"light\"\r\n"},
		{name: "empty document", doc: "", want: "[ui]\ntheme = \"light\"\n"},
		{name: "only comments and blank lines", doc: "# nothing\n", want: "# nothing\n\n[ui]\ntheme = \"light\"\n"},
		{name: "blank document", doc: "\n", want: "\n[ui]\ntheme = \"light\"\n"},
		{name: "sub-table defines section implicitly", doc: "[ui.extra]\nx = 1\n", want: "[ui.extra]\nx = 1\n\n[ui]\ntheme = \"light\"\n"},
		{name: "other section and key", doc: "[popup]\nwidth = \"90%\"\n", section: "popup", key: "session_prefix", value: "ai-", want: "[popup]\nwidth = \"90%\"\nsession_prefix = \"ai-\"\n"},

		// Quoting.
		{name: "value escapes", doc: "[ui]\ntheme = \"\"\n", value: "a \"b\" \\ c\n\t\r\b\f\x01\x7f é", want: "[ui]\ntheme = \"a \\\"b\\\" \\\\ c\\n\\t\\r\\b\\f\\u0001\\u007F é\"\n"},
		{name: "empty value", doc: "[ui]\ntheme = \"lyna\"\n", value: "", want: "[ui]\ntheme = \"\"\n"},

		// Refusals.
		{name: "inline table without the key", doc: "ui = { icons = \"auto\" }\n", wantErr: ErrEditUnsupported},
		{name: "inline table with dotted key below", doc: "ui = { theme.x = 1 }\n", wantErr: ErrEditUnsupported},
		{name: "array of tables", doc: "[[ui]]\ntheme = \"a\"\n", wantErr: ErrEditUnsupported},
		{name: "section is a value", doc: "ui = 3\n", wantErr: ErrEditUnsupported},
		{name: "key is a table header", doc: "[ui.theme]\nx = 1\n", wantErr: ErrEditUnsupported},
		{name: "key is an array table header", doc: "[[ui.theme]]\nx = 1\n", wantErr: ErrEditUnsupported},
		{name: "key is a dotted table", doc: "[ui]\ntheme.x = 1\n", wantErr: ErrEditUnsupported},
		{name: "key is a top-level dotted table", doc: "ui.theme.x = 1\n", wantErr: ErrEditUnsupported},
		{name: "invalid document", doc: "[ui\n", fails: true},
		{name: "duplicate key", doc: "[ui]\ntheme = \"a\"\ntheme = \"b\"\n", fails: true},
		{name: "section not a bare key", doc: "", section: "u i", fails: true},
		{name: "key not a bare key", doc: "", key: "a.b", fails: true},
		{name: "value not UTF-8", doc: "", value: "\xff", fails: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			section, key, value := orDefault(tc.section, "ui"), orDefault(tc.key, "theme"), tc.value
			if value == "" && tc.name != "empty value" {
				value = "light"
			}
			doc := []byte(tc.doc)
			original := bytes.Clone(doc)
			got, err := SetString(doc, section, key, value)
			if !bytes.Equal(doc, original) {
				t.Fatalf("input modified: %q", doc)
			}
			if tc.wantErr != nil || tc.fails {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
			assertOnlyKeySet(t, doc, got, section, key, value)
		})
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// TestSetStringTemplate sets every theme in the documented template: the file
// changes only in the theme value and decodes to that theme.
func TestSetStringTemplate(t *testing.T) {
	for _, name := range Choices("ui.theme") {
		t.Run(name, func(t *testing.T) {
			got, err := SetString(Template(), "ui", "theme", name)
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.Replace(Template(), []byte(`theme = "lyna"`), []byte(`theme = "`+name+`"`), 1)
			if !bytes.Equal(got, want) {
				t.Fatalf("template edit differs:\n%s", got)
			}
			cfg, err := Decode(got)
			if err != nil {
				t.Fatal(err)
			}
			wantCfg := Default()
			wantCfg.UI.Theme = name
			if !reflect.DeepEqual(cfg, wantCfg) {
				t.Fatalf("decoded %+v", cfg.UI)
			}
		})
	}
}

// assertOnlyKeySet decodes both documents generically and checks that after
// holds value at section.key and is otherwise identical to before.
func assertOnlyKeySet(t *testing.T, before, after []byte, section, key, value string) {
	t.Helper()
	var b, a map[string]any
	if err := toml.Unmarshal(before, &b); err != nil {
		t.Fatalf("before does not decode: %v", err)
	}
	if err := toml.Unmarshal(after, &a); err != nil {
		t.Fatalf("result does not decode: %v\n%q", err, after)
	}
	if b == nil {
		b = map[string]any{}
	}
	tbl, ok := b[section].(map[string]any)
	if _, exists := b[section]; exists && !ok {
		t.Fatalf("SetString succeeded although %s is not a table", section)
	}
	if !ok {
		tbl = map[string]any{}
		b[section] = tbl
	}
	tbl[key] = value
	if got, _ := a[section].(map[string]any); got == nil || got[key] != value {
		t.Fatalf("%s.%s = %#v, want %q", section, key, a[section], value)
	}
	wantBytes, werr := toml.Marshal(b)
	gotBytes, gerr := toml.Marshal(a)
	if (werr == nil) != (gerr == nil) || !bytes.Equal(wantBytes, gotBytes) {
		t.Fatalf("other keys changed:\nwant %s (%v)\ngot  %s (%v)", wantBytes, werr, gotBytes, gerr)
	}
}

// FuzzSetString checks the edit on arbitrary documents, keys and values: an
// invalid document is refused, and a successful edit decodes with the key
// holding the value and every other key unchanged.
func FuzzSetString(f *testing.F) {
	seeds := []string{
		string(Template()),
		"[ui]\ntheme = \"lyna\"\n",
		"[ui]\n# theme = \"lyna\"\nicons = \"auto\"",
		"ui.icons = \"auto\"\r\n[workspace]\r\n",
		"ui = { theme = 'x' }\n",
		"[[ui]]\n",
		"[ui.theme]\n",
		"a = \"\"\"\n[ui]\n\"\"\"\n",
		"",
	}
	for _, s := range seeds {
		f.Add([]byte(s), "ui", "theme", "light")
		f.Add([]byte(s), "popup", "session_prefix", "a\"b\\\n")
	}
	f.Fuzz(func(t *testing.T, doc []byte, section, key, value string) {
		out, err := SetString(doc, section, key, value)
		var probe map[string]any
		if toml.Unmarshal(doc, &probe) != nil {
			if err == nil {
				t.Fatalf("invalid document accepted: %q", doc)
			}
			return
		}
		if err != nil {
			return
		}
		assertOnlyKeySet(t, doc, out, section, key, value)
	})
}

// FuzzSetStringConfig checks the property the theme command relies on: a
// valid configuration stays valid with only ui.theme changed, unless the
// edit is refused as unsupported.
func FuzzSetStringConfig(f *testing.F) {
	f.Add(Template(), uint8(1))
	f.Add([]byte("[ui]\nicons = \"ascii\"\n[popup]\nwidth = \"80%\"\n"), uint8(2))
	f.Add([]byte("ui.clock = false\n"), uint8(0))
	f.Add([]byte("ui = { theme = \"ansi\" }\n"), uint8(0))
	f.Add([]byte("[layouts.tests]\npanes = [{ role = \"claude\" }]\n"), uint8(1))
	themes := Choices("ui.theme")
	f.Fuzz(func(t *testing.T, doc []byte, pick uint8) {
		before, err := Decode(doc)
		if err != nil {
			return
		}
		value := themes[int(pick)%len(themes)]
		out, err := SetString(doc, "ui", "theme", value)
		if err != nil {
			if !errors.Is(err, ErrEditUnsupported) {
				t.Fatalf("valid configuration refused: %v\n%q", err, doc)
			}
			return
		}
		after, err := Decode(out)
		if err != nil {
			t.Fatalf("result does not decode: %v\n%q", err, out)
		}
		before.UI.Theme = value
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("configuration changed beyond ui.theme:\n%+v\n%+v", after, before)
		}
	})
}
