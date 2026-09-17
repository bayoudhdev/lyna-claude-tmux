package app

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// Project files init writes, relative to the project root.
const (
	initSettingsFile        = ".claude/settings.json"
	initWorktreeIncludeFile = ".worktreeinclude"
	initGitignoreFile       = ".gitignore"
)

// initMaxProjectFile bounds the read of an existing project file.
const initMaxProjectFile = 4 << 20

// ErrProjectChanged reports a project file that changed between the preview
// and the write.
var ErrProjectChanged = errors.New("a project file changed since the preview")

// ProjectFile is one proposed change to a file of the project.
type ProjectFile struct {
	// Rel is the slash-separated path below the project root.
	Rel string
	// Path is the absolute path.
	Path string
	// Exists reports whether the file is already there; Old is its content.
	Exists bool
	Old    []byte
	// New is the content to write, equal to Old when nothing changes.
	New []byte
	// Diff is the line diff from Old to New, empty when unchanged.
	Diff    string
	Changed bool
	perm    fs.FileMode
}

// ProjectPlan is what `init --project` proposes for a project.
type ProjectPlan struct {
	Root    string
	Profile sandbox.Profile
	Files   []ProjectFile
}

// Changed reports whether any file would change.
func (p ProjectPlan) Changed() bool {
	for _, f := range p.Files {
		if f.Changed {
			return true
		}
	}
	return false
}

// InitProjectPlan computes the project files for the project that contains
// dir: the sandbox part of .claude/settings.json that Claude Code honors in a
// repository, the .worktreeinclude patterns and the .gitignore entries. The
// shared file takes the configured profile at bash isolation with the
// ecosystems of the project, and none of the user's own additions: isolation
// and extra paths are decisions of each machine, not of the team. Nothing is
// written; a symbolic link anywhere below the root is refused.
func InitProjectPlan(h Host, dir string) (ProjectPlan, error) {
	root, err := projectRoot(dir)
	if err != nil {
		return ProjectPlan{}, err
	}
	_, cfg, err := LoadConfig(h)
	if err != nil {
		return ProjectPlan{}, err
	}
	if cfg.Sandbox.Profile == string(sandbox.Off) {
		return ProjectPlan{}, fmt.Errorf("%w: project settings keep the sandbox on; set sandbox.profile to standard or strict with `lmux config edit`", ErrSandboxOffInConfig)
	}
	profile, err := sandbox.ParseProfile(cfg.Sandbox.Profile)
	if err != nil {
		return ProjectPlan{}, err
	}
	res, err := sandbox.Resolve(sandbox.Input{Profile: profile, Isolation: sandbox.IsolationBash, Ecosystems: sandbox.DetectEcosystems(func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, rel))
		return err == nil
	})})
	if err != nil {
		return ProjectPlan{}, err
	}
	plan := ProjectPlan{Root: root, Profile: res.Profile}
	for _, rel := range []string{initSettingsFile, initWorktreeIncludeFile, initGitignoreFile} {
		f, err := initReadProjectFile(root, rel)
		if err != nil {
			return ProjectPlan{}, err
		}
		switch rel {
		case initSettingsFile:
			change, err := claudecfg.ProjectSettings(f.Old, res)
			if err != nil {
				return ProjectPlan{}, fmt.Errorf("%s: %w", f.Path, err)
			}
			f.New, f.Changed = change.Data, change.Changed
		case initWorktreeIncludeFile:
			var added []string
			f.New, added = claudecfg.MergeLines(f.Old, claudecfg.WorktreeIncludeLines())
			f.Changed = len(added) > 0
		case initGitignoreFile:
			var added []string
			f.New, added = claudecfg.MergeLines(f.Old, claudecfg.GitignoreLines())
			f.Changed = len(added) > 0
		}
		if f.Changed {
			f.Diff = claudecfg.LineDiff(f.Old, f.New)
		}
		plan.Files = append(plan.Files, f)
	}
	return plan, nil
}

// initReadProjectFile reads a project file without following links: every
// component below root must be a real directory, and the file itself a
// regular file or absent.
func initReadProjectFile(root, rel string) (ProjectFile, error) {
	f := ProjectFile{Rel: rel, Path: filepath.Join(root, filepath.FromSlash(rel)), perm: 0o644}
	if err := initCheckParents(root, rel); err != nil {
		return f, err
	}
	info, err := os.Lstat(f.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return f, nil
	case err != nil:
		return f, err
	case info.Mode()&fs.ModeSymlink != 0:
		return f, fmt.Errorf("%s: %w", f.Path, fsx.ErrSymlink)
	case !info.Mode().IsRegular():
		return f, fmt.Errorf("%s is not a regular file", f.Path)
	}
	data, err := fsx.ReadFileNoFollow(f.Path, initMaxProjectFile)
	if err != nil {
		return f, fmt.Errorf("read %s: %w", f.Path, err)
	}
	f.Exists, f.Old, f.perm = true, data, info.Mode().Perm()
	return f, nil
}

// initCheckParents refuses a link or a non-directory among the directories
// between root and rel.
func initCheckParents(root, rel string) error {
	parts := strings.Split(rel, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", current, fsx.ErrSymlink)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: %w", current, fsx.ErrNotDir)
		}
	}
	return nil
}

// ProjectApplied reports what Apply wrote.
type ProjectApplied struct {
	// Written are the absolute paths of the files written.
	Written []string
	// Backup is the copy of the replaced settings file, empty when there was none.
	Backup string
}

// Apply writes the changed files of the plan atomically. Every file must still
// hold the content the plan was computed from, or nothing is written, and an
// existing .claude/settings.json is first copied to settings.json.bak. A file
// keeps its permissions; a new one gets 0644, as repository files do.
func (p ProjectPlan) Apply() (ProjectApplied, error) {
	var out ProjectApplied
	for _, f := range p.Files {
		if !f.Changed {
			continue
		}
		current, err := initReadProjectFile(p.Root, f.Rel)
		if err != nil {
			return out, err
		}
		if current.Exists != f.Exists || !bytes.Equal(current.Old, f.Old) {
			return out, fmt.Errorf("%w: %s; run `lmux init --project` again", ErrProjectChanged, f.Path)
		}
	}
	for _, f := range p.Files {
		if !f.Changed {
			continue
		}
		dir := filepath.Dir(f.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: a repository directory, not private state
			return out, err
		}
		if err := initCheckParents(p.Root, f.Rel); err != nil {
			return out, err
		}
		if f.Rel == initSettingsFile && f.Exists {
			backup := f.Path + ".bak"
			if err := fsx.WriteFileAtomic(backup, f.Old, f.perm); err != nil {
				return out, fmt.Errorf("back up %s: %w", f.Path, err)
			}
			out.Backup = backup
		}
		if err := fsx.WriteFileAtomic(f.Path, f.New, f.perm); err != nil {
			return out, err
		}
		out.Written = append(out.Written, f.Path)
	}
	return out, nil
}
