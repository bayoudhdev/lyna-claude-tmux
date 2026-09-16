package config

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ErrEditUnsupported reports a document where the key cannot be set by
// changing one value or adding one line: the section is an inline table
// without the key, an array of tables or a plain value, or the key is itself a
// table.
var ErrEditUnsupported = errors.New("the key is defined in a form lyna-tmux cannot edit in place")

// SetString returns a copy of the TOML document data with section.key set to
// the string value. The edit is minimal, so comments, ordering, spacing and
// every other byte of the file stay as the user wrote them:
//
//   - an existing key, under a [section] table, as a top-level dotted key or
//     inside an inline table, has only its value replaced (a trailing comment
//     stays);
//   - a missing key in an existing [section] table gets one line, placed after
//     a commented-out assignment of the same key when the table has one (so it
//     stays next to its documentation), otherwise after the table's last key,
//     with that line's indentation;
//   - a section defined only by top-level dotted keys gets one more dotted key
//     after them;
//   - a missing section is appended as a new table.
//
// Commented-out lines are never taken for the key. The value is written as a
// basic (double-quoted) string with the escapes the TOML encoder uses, and new
// lines follow the file's line endings. data must be a valid TOML document;
// section and key must be bare keys and value valid UTF-8.
func SetString(data []byte, section, key, value string) ([]byte, error) {
	for _, k := range []string{section, key} {
		if !bareKey(k) {
			return nil, fmt.Errorf("config: %q is not a bare key", k)
		}
	}
	if !utf8.ValidString(value) {
		return nil, errors.New("config: value is not valid UTF-8")
	}
	// The AST parser checks syntax only; decoding also rejects redefined keys
	// and tables, which a line edit could otherwise make worse.
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, decodeError(err)
	}
	s, err := scanEdit(data, section, key)
	if err != nil {
		return nil, decodeError(err)
	}
	quoted := string(quoteBasic(value))
	switch {
	case s.unsupported:
		return nil, fmt.Errorf("%s.%s: %w", section, key, ErrEditUnsupported)
	case s.found:
		return slices.Concat(data[:s.valueStart], []byte(quoted), data[s.valueEnd:]), nil
	case s.comment.set:
		return insertLine(data, s.comment, key+" = "+quoted), nil
	case s.table.set:
		return insertLine(data, s.table, key+" = "+quoted), nil
	case s.dotted.set:
		return insertLine(data, s.dotted, section+"."+key+" = "+quoted), nil
	}
	return appendTable(data, section, key+" = "+quoted), nil
}

// editAnchor is a line a new line is inserted after.
type editAnchor struct {
	set bool
	// at is the offset of the start of the next line, or len(data) when the
	// anchor line is the last one and has no line break.
	at     int
	indent string
}

func anchorAfter(data []byte, line, end int) editAnchor {
	at := len(data)
	if i := bytes.IndexByte(data[end:], '\n'); i >= 0 {
		at = end + i + 1
	}
	return editAnchor{set: true, at: at, indent: indentOf(data, line)}
}

// editScan is what scanEdit found about section.key.
type editScan struct {
	found                bool
	valueStart, valueEnd int
	unsupported          bool
	// comment anchors on a commented-out assignment of the key inside the
	// [section] table, table on the table's last line with content, dotted on
	// the last top-level dotted key of the section.
	comment, table, dotted editAnchor
}

func scanEdit(data []byte, section, key string) (editScan, error) {
	var s editScan
	p := unstable.Parser{KeepComments: true}
	p.Reset(data)
	var header []string
	inTable, top := false, true
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Comment:
			if inTable && commentedAssignment(e.Data, key) {
				s.comment = anchorAfter(data, int(e.Raw.Offset), rangeEnd(e.Raw))
			}
		case unstable.Table, unstable.ArrayTable:
			parts, last := keyParts(e.Key())
			header, top = parts, false
			inTable = e.Kind == unstable.Table && slices.Equal(header, []string{section})
			if inTable {
				s.table = anchorAfter(data, int(last.Raw.Offset), rangeEnd(last.Raw))
			}
			isArray := e.Kind == unstable.ArrayTable && slices.Equal(header, []string{section})
			if isArray || len(header) >= 2 && header[0] == section && header[1] == key {
				s.unsupported = true
			}
		case unstable.KeyValue:
			parts, _ := keyParts(e.Key())
			s.visit(data, e, header, section, key)
			if inTable {
				s.table = anchorAfter(data, int(e.Raw.Offset), rangeEnd(e.Raw))
			}
			if top && len(parts) >= 2 && parts[0] == section {
				s.dotted = anchorAfter(data, int(e.Raw.Offset), rangeEnd(e.Raw))
			}
		}
	}
	return s, p.Error()
}

// visit checks one key-value whose table path is prefix, descending into an
// inline table that holds the section.
func (s *editScan) visit(data []byte, kv *unstable.Node, prefix []string, section, key string) {
	parts, last := keyParts(kv.Key())
	path := slices.Concat(prefix, parts)
	switch {
	case len(path) == 2 && path[0] == section && path[1] == key:
		s.found = true
		s.valueStart = valueStart(data, rangeEnd(last.Raw))
		s.valueEnd = rangeEnd(kv.Raw)
	case len(path) > 2 && path[0] == section && path[1] == key:
		s.unsupported = true
	case len(path) == 1 && path[0] == section:
		value := kv.Value()
		if value.Kind != unstable.InlineTable {
			s.unsupported = true
			return
		}
		it := value.Children()
		for it.Next() {
			if child := it.Node(); child.Kind == unstable.KeyValue {
				s.visit(data, child, path, section, key)
			}
		}
		// An inline table is closed: a key cannot be added to it by line.
		if !s.found {
			s.unsupported = true
		}
	}
}

// keyParts returns the decoded parts of a key and its last part node.
func keyParts(it unstable.Iterator) ([]string, *unstable.Node) {
	var parts []string
	var last *unstable.Node
	for it.Next() {
		last = it.Node()
		parts = append(parts, string(last.Data))
	}
	return parts, last
}

func rangeEnd(r unstable.Range) int { return int(r.Offset + r.Length) }

// valueStart returns the offset of the value after a key ending at keyEnd:
// past optional blanks, the equals sign and more blanks.
func valueStart(data []byte, keyEnd int) int {
	i := keyEnd
	for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
		i++
	}
	if i < len(data) && data[i] == '=' {
		i++
	}
	for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
		i++
	}
	return i
}

// commentedAssignment reports whether a comment is a commented-out assignment
// of key, such as `# theme = "lyna"`.
func commentedAssignment(comment []byte, key string) bool {
	rest := bytes.TrimLeft(comment, "#")
	rest = bytes.TrimLeft(rest, " \t")
	after, ok := bytes.CutPrefix(rest, []byte(key))
	if !ok {
		return false
	}
	after = bytes.TrimLeft(after, " \t")
	return len(after) > 0 && after[0] == '='
}

func indentOf(data []byte, offset int) string {
	start := bytes.LastIndexByte(data[:offset], '\n') + 1
	end := start
	for end < offset && (data[end] == ' ' || data[end] == '\t') {
		end++
	}
	return string(data[start:end])
}

// lineBreak returns the line ending the document uses.
func lineBreak(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func insertLine(data []byte, a editAnchor, line string) []byte {
	eol := lineBreak(data)
	var lead string
	if a.at == 0 || data[a.at-1] != '\n' {
		// The anchor is the last line and has no line break of its own.
		lead = eol
	} else if a.at < 2 || data[a.at-2] != '\r' {
		eol = "\n"
	}
	return slices.Concat(data[:a.at], []byte(lead+a.indent+line+eol), data[a.at:])
}

func appendTable(data []byte, section, line string) []byte {
	eol := lineBreak(data)
	out := slices.Clip(data)
	if len(out) > 0 {
		if out[len(out)-1] != '\n' {
			out = append(out, eol...)
		}
		if !bytes.HasSuffix(out, []byte("\n\n")) && !bytes.HasSuffix(out, []byte("\n\r\n")) && len(bytes.TrimSpace(out)) > 0 {
			out = append(out, eol...)
		}
	}
	return append(out, "["+section+"]"+eol+line+eol...)
}

func bareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// quoteBasic spells s as a TOML basic string with the escapes of the TOML
// encoder: quote, backslash and the named control escapes, \uXXXX for other
// control characters. s must be valid UTF-8.
func quoteBasic(s string) []byte {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for i := range len(s) {
		c := s[i]
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 || c == 0x7f {
				b = fmt.Appendf(b, "\\u%04X", c)
				continue
			}
			b = append(b, c)
		}
	}
	return append(b, '"')
}
