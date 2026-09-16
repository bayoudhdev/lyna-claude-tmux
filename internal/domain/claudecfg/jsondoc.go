package claudecfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxJSONDepth bounds nesting so a hostile project file cannot exhaust the stack.
const maxJSONDepth = 256

var errTooDeep = errors.New("claudecfg: JSON nesting is too deep")

type nodeKind int

const (
	kindScalar nodeKind = iota
	kindObject
	kindArray
)

// node is a JSON value that remembers member order, so rewriting a file the
// user maintains keeps its keys where they were.
type node struct {
	kind    nodeKind
	scalar  any // string, json.Number, bool or nil
	members []member
	items   []*node
}

type member struct {
	key   string
	value *node
}

// parseJSON decodes exactly one JSON value.
func parseJSON(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := parseValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("claudecfg: unexpected data after the JSON value")
	}
	return n, nil
}

func parseValue(dec *json.Decoder, depth int) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return &node{kind: kindScalar, scalar: tok}, nil
	}
	if depth >= maxJSONDepth {
		return nil, errTooDeep
	}
	switch delim {
	case '{':
		n := &node{kind: kindObject}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("claudecfg: object key is %T, not a string", keyTok)
			}
			value, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			n.members = append(n.members, member{key: key, value: value})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	case '[':
		n := &node{kind: kindArray}
		for dec.More() {
			item, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, item)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	}
	return nil, fmt.Errorf("claudecfg: unexpected delimiter %q", delim)
}

// get returns the value of the last member named key, as encoding/json would.
func (n *node) get(key string) *node {
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			return n.members[i].value
		}
	}
	return nil
}

// set replaces the last member named key or appends a new member.
func (n *node) set(key string, value *node) {
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			n.members[i].value = value
			return
		}
	}
	n.members = append(n.members, member{key: key, value: value})
}

// encode writes n with two-space indentation, the layout json.MarshalIndent uses.
func (n *node) encode(buf *bytes.Buffer, indent string) error {
	switch n.kind {
	case kindObject:
		if len(n.members) == 0 {
			buf.WriteString("{}")
			return nil
		}
		buf.WriteString("{\n")
		for i, m := range n.members {
			buf.WriteString(indent + "  ")
			if err := encodeCompact(buf, m.key); err != nil {
				return err
			}
			buf.WriteString(": ")
			if err := m.value.encode(buf, indent+"  "); err != nil {
				return err
			}
			if i < len(n.members)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "}")
	case kindArray:
		if len(n.items) == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteString("[\n")
		for i, item := range n.items {
			buf.WriteString(indent + "  ")
			if err := item.encode(buf, indent+"  "); err != nil {
				return err
			}
			if i < len(n.items)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "]")
	default:
		return encodeCompact(buf, n.scalar)
	}
	return nil
}

// marshalNode renders a document with a trailing newline.
func marshalNode(n *node) ([]byte, error) {
	var buf bytes.Buffer
	if err := n.encode(&buf, ""); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// toNode converts a Go value into a node through its JSON encoding.
func toNode(v any) (*node, error) {
	var buf bytes.Buffer
	if err := encodeCompact(&buf, v); err != nil {
		return nil, err
	}
	return parseJSON(buf.Bytes())
}
