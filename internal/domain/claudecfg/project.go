package claudecfg

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
)

// ProjectChange is a proposed rewrite of a project file.
type ProjectChange struct {
	// Data is the full new content. It equals the input when nothing changes.
	Data []byte
	// Diff is LineDiff of the old and new content, empty when unchanged.
	Diff string
	// Changed reports whether Data differs from the input.
	Changed bool
}

// ProjectSettings merges the project-honored part of a sandbox resolution
// into an existing .claude/settings.json document (empty input starts a new
// one). Unknown keys and their order are preserved; lists gain missing
// entries without losing or reordering existing ones; nothing is removed.
//
// Keys Claude Code ignores in a repository file are left out:
// network.strictAllowlist is honored only from user, managed and --settings
// sources. disableBypassPermissionsMode and enableWeakerNestedSandbox are
// launcher and machine decisions, not team policy, so they stay per launch.
func ProjectSettings(existing []byte, res sandbox.Resolution) (ProjectChange, error) {
	// A resolution with the sandbox off would write "enabled": false into a
	// file the whole team reads, turning one machine's choice into everyone's.
	// Turning the sandbox off stays a per-launch decision.
	if !res.Sandbox.Enabled {
		return ProjectChange{}, fmt.Errorf("%w: project settings cannot turn the sandbox off", ErrInvalid)
	}
	source := bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf"))
	root := &node{kind: kindObject}
	if len(bytes.TrimSpace(source)) > 0 {
		parsed, err := parseJSON(source)
		if err != nil {
			return ProjectChange{}, fmt.Errorf("claudecfg: parse project settings: %w", err)
		}
		if parsed.kind != kindObject {
			return ProjectChange{}, fmt.Errorf("%w: project settings must be a JSON object", ErrInvalid)
		}
		root = parsed
	}

	m := merger{}
	sb := res.Sandbox
	sandboxObj := m.object(root, "sandbox")
	m.setBool(sandboxObj, "enabled", sb.Enabled)
	if sb.FailIfUnavailable {
		m.setBool(sandboxObj, "failIfUnavailable", true)
	}
	if sb.AutoAllowBashIfSandboxed != nil {
		m.setBool(sandboxObj, "autoAllowBashIfSandboxed", *sb.AutoAllowBashIfSandboxed)
	}
	if sb.AllowUnsandboxedCommands != nil {
		m.setBool(sandboxObj, "allowUnsandboxedCommands", *sb.AllowUnsandboxedCommands)
	}
	m.addStrings(sandboxObj, "excludedCommands", sb.ExcludedCommands)
	if fs := sb.Filesystem; fs != nil {
		fsObj := m.object(sandboxObj, "filesystem")
		m.addStrings(fsObj, "allowWrite", fs.AllowWrite)
		m.addStrings(fsObj, "denyRead", fs.DenyRead)
		m.addStrings(fsObj, "allowRead", fs.AllowRead)
	}
	if nw := sb.Network; nw != nil && len(nw.AllowedDomains) > 0 {
		m.addStrings(m.object(sandboxObj, "network"), "allowedDomains", nw.AllowedDomains)
	}
	if cr := sb.Credentials; cr != nil {
		crObj := m.object(sandboxObj, "credentials")
		for _, f := range cr.Files {
			m.addEntry(crObj, "files", "path", f.Path, f)
		}
		for _, v := range cr.EnvVars {
			m.addEntry(crObj, "envVars", "name", v.Name, v)
		}
	}

	perms := res.Permissions
	if len(perms.Ask) > 0 || len(perms.Deny) > 0 || perms.BlockReadsOutsideWorkingDirectories {
		permObj := m.object(root, "permissions")
		m.addStrings(permObj, "ask", perms.Ask)
		m.addStrings(permObj, "deny", perms.Deny)
		if perms.BlockReadsOutsideWorkingDirectories {
			m.setBool(permObj, "blockReadsOutsideWorkingDirectories", true)
		}
	}
	if m.err != nil {
		return ProjectChange{}, m.err
	}
	if !m.changed {
		return ProjectChange{Data: existing}, nil
	}
	data, err := marshalNode(root)
	if err != nil {
		return ProjectChange{}, fmt.Errorf("claudecfg: encode project settings: %w", err)
	}
	return ProjectChange{Data: data, Diff: LineDiff(existing, data), Changed: true}, nil
}

// merger applies edits to a document and records whether any took effect.
// The first type conflict stops further edits: an existing key of another
// JSON type is never overwritten.
type merger struct {
	changed bool
	err     error
}

func (m *merger) object(parent *node, key string) *node {
	if m.err != nil || parent == nil {
		return nil
	}
	if existing := parent.get(key); existing != nil {
		if existing.kind != kindObject {
			m.err = fmt.Errorf("%w: %q in project settings is not an object", ErrInvalid, key)
			return nil
		}
		return existing
	}
	obj := &node{kind: kindObject}
	parent.set(key, obj)
	m.changed = true
	return obj
}

func (m *merger) setBool(obj *node, key string, value bool) {
	if m.err != nil || obj == nil {
		return
	}
	if existing := obj.get(key); existing != nil {
		current, isBool := existing.scalar.(bool)
		if existing.kind != kindScalar || !isBool {
			m.err = fmt.Errorf("%w: %q in project settings is not true or false", ErrInvalid, key)
			return
		}
		if current == value {
			return
		}
	}
	obj.set(key, &node{kind: kindScalar, scalar: value})
	m.changed = true
}

func (m *merger) array(obj *node, key string) *node {
	if existing := obj.get(key); existing != nil {
		if existing.kind != kindArray {
			m.err = fmt.Errorf("%w: %q in project settings is not a list", ErrInvalid, key)
			return nil
		}
		return existing
	}
	arr := &node{kind: kindArray}
	obj.set(key, arr)
	return arr
}

func (m *merger) addStrings(obj *node, key string, values []string) {
	if m.err != nil || obj == nil || len(values) == 0 {
		return
	}
	arr := m.array(obj, key)
	if arr == nil {
		return
	}
	for _, v := range values {
		if !containsString(arr, v) {
			arr.items = append(arr.items, &node{kind: kindScalar, scalar: v})
			m.changed = true
		}
	}
}

// addEntry appends an object entry unless one with the same identifying
// field already exists; an existing entry is kept as the project wrote it.
func (m *merger) addEntry(obj *node, key, idField, id string, entry any) {
	if m.err != nil || obj == nil {
		return
	}
	arr := m.array(obj, key)
	if arr == nil {
		return
	}
	for _, item := range arr.items {
		if item.kind == kindObject {
			if f := item.get(idField); f != nil && f.kind == kindScalar && f.scalar == id {
				return
			}
		}
	}
	n, err := toNode(entry)
	if err != nil {
		m.err = err
		return
	}
	arr.items = append(arr.items, n)
	m.changed = true
}

func containsString(arr *node, s string) bool {
	for _, item := range arr.items {
		if item.kind == kindScalar && item.scalar == s {
			return true
		}
	}
	return false
}

// WorktreeIncludeLines are the .worktreeinclude patterns init proposes: local
// environment files, which a fresh worktree checkout would not have. Claude
// copies only files that match and are gitignored.
func WorktreeIncludeLines() []string { return []string{".env", ".env.*"} }

// GitignoreLines are the .gitignore entries init proposes: worktree checkouts
// and personal project settings stay out of commits.
func GitignoreLines() []string { return []string{".claude/worktrees/", ".claude/settings.local.json"} }

// MergeLines appends each line not already present to a line-oriented file
// (.gitignore syntax) and returns the new content and the lines added. It is
// idempotent. A present line matches after trimming whitespace, one leading
// "/" and trailing "/", which gitignore treats as the same entry for these
// paths. A path the file un-ignores with a "!" line is left alone: appending
// the entry after that line would ignore it again and quietly undo a decision
// the project made. Existing content is kept byte for byte.
func MergeLines(existing []byte, lines []string) (out []byte, added []string) {
	present, negated := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		key, no := lineKey(l)
		if no {
			negated[key] = true
			continue
		}
		present[key] = true
	}
	out = append([]byte(nil), existing...)
	for _, l := range lines {
		key, _ := lineKey(l)
		if key == "" || present[key] || negated[key] {
			continue
		}
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, strings.TrimSpace(l)...)
		out = append(out, '\n')
		present[key] = true
		added = append(added, strings.TrimSpace(l))
	}
	return out, added
}

// lineKey is the path a .gitignore line names, and whether the line un-ignores
// it. Leading "!" marks a negation, and one leading "/" and any trailing "/"
// are noise for the entries this package adds.
func lineKey(l string) (key string, negated bool) {
	l = strings.TrimSpace(l)
	if negated = strings.HasPrefix(l, "!"); negated {
		l = strings.TrimPrefix(l, "!")
	}
	l = strings.TrimPrefix(l, "/")
	return strings.TrimRight(l, "/"), negated
}
