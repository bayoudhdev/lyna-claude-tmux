package keys

import (
	"strings"
	"testing"
)

func TestDefaultsHaveNoConflicts(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{name: "alt keys default prefix", opts: Options{AltKeys: true}},
		{name: "alt keys custom prefix", opts: Options{AltKeys: true, Prefix: "C-Space"}},
		{name: "prefix only", opts: Options{Prefix: "C-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := Defaults(tc.opts)
			if c := Conflicts(bs); len(c) != 0 {
				t.Fatalf("Defaults(%+v) conflicts: %v", tc.opts, c)
			}
			for _, b := range bs {
				if b.Key == "" || b.Action == "" || b.Help == "" || b.Group == "" {
					t.Errorf("incomplete binding %+v", b)
				}
				if b.Table != TableRoot && b.Table != TablePrefix {
					t.Errorf("binding %s has table %q", b.Key, b.Table)
				}
				if b.Table == TableRoot && !strings.HasPrefix(b.Key, "M-") {
					t.Errorf("root binding %s does not use Alt", b.Key)
				}
				if b.Action == ActionWindow && b.Arg == "" {
					t.Errorf("window binding %s has no index", b.Key)
				}
			}
		})
	}
}

func TestDefaultsTables(t *testing.T) {
	cases := []struct {
		name       string
		opts       Options
		wantRoot   int
		wantPrefix string
	}{
		{name: "alt keys on", opts: Options{AltKeys: true}, wantRoot: 27, wantPrefix: "C-b"},
		{name: "alt keys off", opts: Options{AltKeys: false, Prefix: "C-a"}, wantRoot: 0, wantPrefix: "C-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, sendPrefix := 0, ""
			for _, b := range Defaults(tc.opts) {
				if b.Table == TableRoot {
					root++
				}
				if b.Action == ActionSendPrefix {
					sendPrefix = b.Key
				}
			}
			if root != tc.wantRoot {
				t.Errorf("root bindings = %d, want %d", root, tc.wantRoot)
			}
			if sendPrefix != tc.wantPrefix {
				t.Errorf("send-prefix key = %q, want %q", sendPrefix, tc.wantPrefix)
			}
		})
	}
}

// TestPrefixCoversEveryAltAction pins the promise the Alt bindings rest on:
// everything they reach is reachable after the prefix as well. Alt arrives
// only on a terminal set to send Option as Meta, and on one that is not, a key
// with no prefix binding is an action with no way to run it. An earlier
// version of this test listed the actions it considered essential by hand, and
// closing a pane was not among them.
func TestPrefixCoversEveryAltAction(t *testing.T) {
	// The argument is part of the action: window 3 is not window 1.
	type act struct{ action, arg string }
	prefixed := map[act]string{}
	for _, b := range Defaults(Options{AltKeys: true}) {
		if b.Table == TablePrefix {
			prefixed[act{string(b.Action), b.Arg}] = b.Key
		}
	}
	root := 0
	for _, b := range Defaults(Options{AltKeys: true}) {
		if b.Table != TableRoot {
			continue
		}
		root++
		if _, ok := prefixed[act{string(b.Action), b.Arg}]; !ok {
			t.Errorf("%s (%s, %s) has no prefix binding", b.Key, b.Action, b.Help)
		}
	}
	if root == 0 {
		t.Fatal("no root bindings to cover")
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "already canonical", in: "M-S-Left", want: "M-S-Left"},
		{name: "reordered modifiers", in: "S-M-Left", want: "M-S-Left"},
		{name: "lowercase modifiers and key", in: "c-m-up", want: "C-M-Up"},
		{name: "letter case kept", in: "M-H", want: "M-H"},
		{name: "letter lowercase kept", in: "M-h", want: "M-h"},
		{name: "dash key", in: "M--", want: "M--"},
		{name: "backslash", in: `M-\`, want: `M-\`},
		{name: "plain named", in: "btab", want: "BTab"},
		{name: "single char", in: "a", want: "a"},
		{name: "function key", in: "c-f12", want: "C-F12"},
		{name: "unknown named key kept", in: "M-Foo", want: "M-Foo"},
		{name: "bare modifier letter", in: "C", want: "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"M-S-Left", "s-m-left", "C-b", "M--", "-", "M-", "C-M-S-", "Space"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		once := Normalize(s)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize is not idempotent: %q -> %q -> %q", s, once, twice)
		}
	})
}

func TestConflicts(t *testing.T) {
	cases := []struct {
		name         string
		bindings     []Binding
		wantReserved []string
		wantDup      []string
	}{
		{
			name:     "no conflicts",
			bindings: []Binding{{Key: "M-a", Table: TableRoot, Help: "a"}, {Key: "a", Table: TablePrefix, Help: "b"}},
		},
		{
			name:         "root takes claude alt key",
			bindings:     []Binding{{Key: "M-j", Table: TableRoot, Help: "focus down"}},
			wantReserved: []string{"M-j"},
		},
		{
			name:         "root takes claude key with other spelling",
			bindings:     []Binding{{Key: "m-up", Table: TableRoot, Help: "focus up"}},
			wantReserved: []string{"m-up"},
		},
		{
			name:     "prefix table may reuse claude keys",
			bindings: []Binding{{Key: "C-c", Table: TablePrefix, Help: "x"}, {Key: "M-j", Table: TablePrefix, Help: "y"}},
		},
		{
			name:     "duplicate in same table",
			bindings: []Binding{{Key: "M-a", Table: TableRoot, Help: "one"}, {Key: "M-a", Table: TableRoot, Help: "two"}},
			wantDup:  []string{"M-a"},
		},
		{
			name:     "same key in different tables is fine",
			bindings: []Binding{{Key: "M-a", Table: TableRoot, Help: "one"}, {Key: "M-a", Table: TablePrefix, Help: "two"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReserved, gotDup []string
			for _, c := range Conflicts(tc.bindings) {
				if c.String() == "" {
					t.Error("empty conflict message")
				}
				if c.Reserved != nil {
					gotReserved = append(gotReserved, c.Binding.Key)
				}
				if c.Duplicate != nil {
					gotDup = append(gotDup, c.Binding.Key)
				}
			}
			if strings.Join(gotReserved, ",") != strings.Join(tc.wantReserved, ",") {
				t.Errorf("reserved conflicts = %v, want %v", gotReserved, tc.wantReserved)
			}
			if strings.Join(gotDup, ",") != strings.Join(tc.wantDup, ",") {
				t.Errorf("duplicate conflicts = %v, want %v", gotDup, tc.wantDup)
			}
		})
	}
}

func TestConflictMessages(t *testing.T) {
	cs := Conflicts([]Binding{
		{Key: "M-p", Table: TableRoot, Help: "picker"},
		{Key: "M-a", Table: TableRoot, Help: "one"},
		{Key: "M-a", Table: TableRoot, Help: "two"},
	})
	if len(cs) != 2 {
		t.Fatalf("got %d conflicts, want 2: %v", len(cs), cs)
	}
	if got := cs[0].String(); !strings.Contains(got, "model picker") || !strings.Contains(got, "M-p") {
		t.Errorf("reserved message = %q", got)
	}
	if got := cs[1].String(); !strings.Contains(got, "bound twice") || !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("duplicate message = %q", got)
	}
}

func TestIsReserved(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"M-w", true}, {"m-w", true}, {"BTab", true}, {"C-b", true}, {"M-[", false}, {"M-a", false}, {"M-g", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := IsReserved(tc.key); got != tc.want {
				t.Fatalf("IsReserved(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestReservedTableIsCanonical(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range ClaudeReserved {
		if Normalize(r.Key) != r.Key {
			t.Errorf("reserved key %q is not canonical (%q)", r.Key, Normalize(r.Key))
		}
		if seen[r.Key] {
			t.Errorf("reserved key %q listed twice", r.Key)
		}
		seen[r.Key] = true
		if r.Claude == "" {
			t.Errorf("reserved key %q has no description", r.Key)
		}
	}
}

func TestHuman(t *testing.T) {
	cases := []struct{ in, want string }{
		{"C-b", "Ctrl+b"},
		{"M-[", "Alt+["},
		{`M-\`, `Alt+\`},
		{"M--", "Alt+-"},
		{"M-S-Left", "Alt+Shift+Left"},
		{"s-m-left", "Alt+Shift+Left"},
		{"M-H", "Alt+H"},
		{"BTab", "Shift+Tab"},
		{"M-BSpace", "Alt+Backspace"},
		{"Escape", "Esc"},
		{"C-_", "Ctrl+_"},
		{"S-Enter", "Shift+Enter"},
		{"M-Space", "Alt+Space"},
		{"-", "-"},
		{"C-", "C-"},
		{"DC", "Delete"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := Human(tc.in); got != tc.want {
				t.Fatalf("Human(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
