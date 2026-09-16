package doctor

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/keys"
)

// FuzzTmuxKey checks that any keystroke either maps to a canonical tmux key
// (stable under keys.Normalize) or is rejected, without panicking.
func FuzzTmuxKey(f *testing.F) {
	for _, s := range []string{"alt+a", "ctrl++", "+", "shift+alt+LEFT", "cmd+k", "f12", "ctrl+", "++", "alt+\xff", "shift+é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, stroke string) {
		key, ok := TmuxKey(stroke)
		if !ok {
			if key != "" {
				t.Fatalf("rejected keystroke %q returned key %q", stroke, key)
			}
			return
		}
		if key == "" {
			t.Fatalf("accepted keystroke %q produced an empty key", stroke)
		}
		if keys.Normalize(key) != key {
			t.Fatalf("key %q for %q is not canonical (%q)", key, stroke, keys.Normalize(key))
		}
		if strings.ContainsAny(key, " \t\n") {
			t.Fatalf("key %q for %q contains whitespace", key, stroke)
		}
	})
}

// FuzzParseKeybindings checks that arbitrary file content never panics and
// that every collision names a key that is really taken.
func FuzzParseKeybindings(f *testing.F) {
	f.Add([]byte(`{"bindings": [{"context": "Chat", "bindings": {"alt+a": "x", "ctrl+u": null}}]}`))
	f.Add([]byte(`{"bindings": [{"context": "Global", "bindings": {"ctrl+b ctrl+x": "y"}}]}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{`))
	workspace := keys.Defaults(keys.Options{AltKeys: true, Prefix: "C-b"})
	taken := map[string]bool{"C-b": true}
	for _, b := range workspace {
		if b.Table == keys.TableRoot {
			taken[keys.Normalize(b.Key)] = true
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		bindings, err := ParseKeybindings(data)
		if err != nil {
			return
		}
		for _, c := range Collisions(bindings, workspace, "C-b") {
			if !taken[c.TmuxKey] {
				t.Fatalf("collision on %q, which lyna-tmux does not bind", c.TmuxKey)
			}
		}
	})
}

// FuzzParseOSRelease checks that parsing never panics and yields lower-case
// identifiers without surrounding quotes.
func FuzzParseOSRelease(f *testing.F) {
	f.Add([]byte(osReleaseUbuntu))
	f.Add([]byte("ID=\"\"\nID_LIKE='a b'\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		id, like := ParseOSRelease(data)
		for _, v := range append([]string{id}, like...) {
			if v != strings.ToLower(v) {
				t.Fatalf("identifier %q is not lower case", v)
			}
		}
		for _, v := range like {
			if v == "" || strings.ContainsAny(v, " \t") {
				t.Fatalf("ID_LIKE entry %q is not a single word", v)
			}
		}
	})
}

// FuzzParseVersion checks that a parsed version renders back to a string the
// parser reads as the same version.
func FuzzParseVersion(f *testing.F) {
	for _, s := range []string{"2.1.273 (Claude Code)", "NVIM v0.11.4", "1.2", "x", "99999999999.1.1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, ok := ParseVersion(s)
		if !ok {
			return
		}
		if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
			t.Fatalf("negative component in %+v from %q", v, s)
		}
		again, ok := ParseVersion(v.String())
		if !ok || again != v {
			t.Fatalf("round trip of %q: %+v -> %q -> %+v", s, v, v.String(), again)
		}
		if !utf8.ValidString(v.String()) {
			t.Fatalf("invalid UTF-8 in %q", v.String())
		}
	})
}
