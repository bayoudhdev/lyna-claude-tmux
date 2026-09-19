// Package agentdef reads an agent definition: the markdown file with a
// front matter header that Claude Code loads from .claude/agents in a project
// and from the user's own directory.
//
// The package opens no file. It reads the bytes of one definition and answers
// what the spawn form needs to offer that agent: its name, what it is for, the
// model it asks for, its color and the tools it is allowed. The body of the
// file is the agent's own system prompt; it belongs to the agent, this product
// never reads it and never sends it anywhere.
package agentdef

import (
	"errors"
	"fmt"
	"strings"
)

// MaxSize bounds a definition. The header is a handful of lines and the body is
// a prompt; past this it is not a definition anybody wrote by hand.
const MaxSize = 1 << 20

// Default is the agent type Claude Code uses when a caller names none.
const Default = "general-purpose"

// Errors this package reports on its own.
var (
	// ErrTooLarge reports a file past MaxSize.
	ErrTooLarge = errors.New("agentdef: file is too large")
	// ErrNoHeader reports a file with no front matter, which is a markdown
	// file that happens to sit in the directory rather than a definition.
	ErrNoHeader = errors.New("agentdef: file has no front matter")
	// ErrNoName reports a definition that names no agent. Nothing can spawn
	// it, so it is not offered.
	ErrNoName = errors.New("agentdef: definition has no name")
)

// Def is one agent definition.
type Def struct {
	// Name is the agent type, the value the spawn form and the Task tool use.
	Name string
	// Description says when to use the agent. The form shows it as it is.
	Description string
	// Model is the alias or the identifier the definition asks for, empty when
	// it asks for none and "inherit" when it asks for the caller's model.
	Model string
	// Color is the color the definition asks for, empty when it asks for none.
	Color string
	// Effort is the reasoning effort the definition asks for, a named level or
	// a number, empty when it asks for none.
	Effort string
	// Tools are the tools the agent is allowed. An empty list means the
	// definition names none, which is how a definition inherits every tool.
	Tools []string
	// Disallowed are the tools the definition takes away.
	Disallowed []string
}

// NameFromFile returns the agent a definition file is named after. Claude Code
// names the file after the agent, and the name in the front matter is the one
// that counts, so this is only for listing a directory.
func NameFromFile(file string) (string, bool) {
	if file == "" || strings.ContainsAny(file, `/\`) {
		return "", false
	}
	name, ok := strings.CutSuffix(file, ".md")
	if !ok || name == "" || strings.HasPrefix(name, ".") {
		return "", false
	}
	return name, true
}

// Parse reads one definition.
func Parse(data []byte) (Def, error) {
	if len(data) > MaxSize {
		return Def{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	header, err := header(string(data))
	if err != nil {
		return Def{}, err
	}
	fields := readFields(header)
	def := Def{
		Name:        fields.scalar("name"),
		Description: fields.scalar("description"),
		Model:       fields.scalar("model"),
		Color:       fields.scalar("color"),
		Effort:      fields.scalar("effort"),
		Tools:       fields.list("tools"),
		Disallowed:  fields.list("disallowedtools"),
	}
	if def.Name == "" {
		return Def{}, ErrNoName
	}
	return def, nil
}

// header returns the lines of the front matter. A definition opens with a line
// of three dashes and closes with one, which is the shape Claude Code writes
// and the only one it reads.
func header(text string) ([]string, error) {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, ErrNoHeader
	}
	var out []string
	for line := range strings.SplitSeq(rest, "\n") {
		if trimmed := strings.TrimRight(line, " \t"); trimmed == "---" || trimmed == "..." {
			return out, nil
		}
		out = append(out, line)
	}
	return nil, ErrNoHeader
}

// value is one entry of the front matter, either a scalar or a list. A key
// written as both keeps whichever the file gave last, the way a later line of
// the same key wins.
type value struct {
	scalar string
	list   []string
	isList bool
}

type fields map[string]*value

func (f fields) scalar(key string) string {
	v, ok := f[key]
	if !ok || v.isList {
		return ""
	}
	return v.scalar
}

// list returns a key read as a list: the items of a block list, or a scalar
// split on commas, which is how the tools of an agent are usually written.
func (f fields) list(key string) []string {
	v, ok := f[key]
	if !ok {
		return nil
	}
	if v.isList {
		return v.list
	}
	var out []string
	for item := range strings.SplitSeq(v.scalar, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// readFields reads the keys of the front matter. It understands what the
// product writes and what a person writes by hand: a scalar, a quoted scalar, a
// block scalar introduced by | or >, a list written inline between brackets or
// on commas, and a list written as indented items. A line it does not
// understand is skipped rather than failing the file, so one key nobody here
// knows never costs the form the name and the description it needs.
func readFields(lines []string) fields {
	out := fields{}
	var current *value
	var block []string
	var blockKey string
	var folded bool
	flush := func() {
		if blockKey == "" {
			return
		}
		joiner := "\n"
		if folded {
			joiner = " "
		}
		out[blockKey] = &value{scalar: strings.TrimRight(strings.Join(block, joiner), " \n")}
		blockKey, block, folded = "", nil, false
	}
	for _, line := range lines {
		indented := line != strings.TrimLeft(line, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case blockKey != "" && (indented || trimmed == ""):
			block = append(block, trimmed)
			continue
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			flush()
			continue
		case indented && strings.HasPrefix(trimmed, "- ") && current != nil && current.scalar == "":
			current.isList = true
			current.list = append(current.list, unquote(strings.TrimPrefix(trimmed, "- ")))
			continue
		}
		flush()
		key, raw, ok := strings.Cut(trimmed, ":")
		if !ok {
			current = nil
			continue
		}
		key = strings.ToLower(key)
		raw = strings.TrimSpace(raw)
		if fold, isBlock := blockScalar(raw); isBlock {
			blockKey, folded = key, fold
			current = nil
			continue
		}
		v := &value{}
		if items, isInline := inlineList(raw); isInline {
			v.isList, v.list = true, items
		} else {
			v.scalar = unquote(raw)
		}
		out[key] = v
		current = v
	}
	flush()
	return out
}

// blockScalar reports a value introduced by | or >, and whether the lines that
// follow are folded into one line.
func blockScalar(raw string) (fold, ok bool) {
	if raw == "" {
		return false, false
	}
	switch raw[0] {
	case '|', '>':
	default:
		return false, false
	}
	// The chomping indicators - and + change the trailing newlines of the
	// value, which a header of a few lines never depends on.
	if strings.Trim(raw[1:], "-+0123456789 \t") != "" {
		return false, false
	}
	return raw[0] == '>', true
}

// inlineList reads a list written between brackets.
func inlineList(raw string) ([]string, bool) {
	inner, ok := strings.CutPrefix(raw, "[")
	if !ok {
		return nil, false
	}
	inner, ok = strings.CutSuffix(inner, "]")
	if !ok {
		return nil, false
	}
	var out []string
	for item := range strings.SplitSeq(inner, ",") {
		if item = unquote(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out, true
}

// unquote removes the quotes around a value and the comment a line may end
// with. A quoted value keeps everything it holds, including a number sign.
func unquote(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 {
		switch raw[0] {
		case '"', '\'':
			if raw[len(raw)-1] == raw[0] {
				inner := raw[1 : len(raw)-1]
				if raw[0] == '"' {
					inner = strings.ReplaceAll(inner, `\"`, `"`)
					inner = strings.ReplaceAll(inner, `\\`, `\`)
				}
				return inner
			}
		}
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw)
}
