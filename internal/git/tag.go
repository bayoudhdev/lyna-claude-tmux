package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/vcs"
)

// Tags lists the tags of the repository containing dir, in the order git
// keeps its refs.
func (r Runner) Tags(ctx context.Context, dir string) ([]vcs.Tag, error) {
	out, err := r.git(ctx, dir, "for-each-ref", "--format="+vcs.TagFormat, "refs/tags/")
	if err != nil {
		return nil, err
	}
	return vcs.ParseTags(out)
}

// Tag describes a tag to write.
type Tag struct {
	// Name is the tag, Rev the commit it names (HEAD when empty).
	Name string
	Rev  string
	// Message makes it a tag of its own carrying that message, written
	// through a file as a commit message is.
	Message string
	// Force writes over a tag of that name, which leaves whatever pointed at
	// the old one where it stands.
	Force bool
}

// Tag writes a tag in the repository containing dir.
func (r Runner) Tag(ctx context.Context, dir string, t Tag) error {
	if err := vcs.ValidateRefName(t.Name); err != nil {
		return fmt.Errorf("git tag: %w", err)
	}
	args := []string{"tag"}
	if t.Force {
		args = append(args, "--force")
	}
	if t.Message != "" {
		if err := checkMessage(t.Message); err != nil {
			return err
		}
		file, clean, err := messageFile(t.Message)
		if err != nil {
			return err
		}
		defer clean()
		args = append(args, "--annotate", "--file="+file, "--cleanup=whitespace")
	}
	args = append(args, "--", t.Name)
	if t.Rev != "" {
		if err := checkRev(t.Rev); err != nil {
			return err
		}
		args = append(args, t.Rev)
	}
	_, err := r.git(ctx, dir, args...)
	return err
}

// DeleteTag removes a tag from the repository, which leaves the commit it
// named where it stands.
func (r Runner) DeleteTag(ctx context.Context, dir, name string) error {
	if err := vcs.ValidateRefName(name); err != nil {
		return fmt.Errorf("git tag: %w", err)
	}
	_, err := r.git(ctx, dir, "tag", "--delete", "--", name)
	return err
}

// Patch describes the patch files to write.
type Patch struct {
	// Revs are the commits to write, as a range ("main..side") or as commits
	// named one by one. Empty writes what the branch holds that its upstream
	// has not got.
	Revs []string
	// Dir is where the files are written: an absolute path to a directory
	// that is already there.
	Dir string
	// Numbered writes the sequence number in every subject, even when there
	// is only one patch.
	Numbered bool
}

// FormatPatch writes one file per commit and returns the files written, in
// the order git wrote them.
func (r Runner) FormatPatch(ctx context.Context, dir string, p Patch) ([]string, error) {
	if err := checkOutputDir(p.Dir); err != nil {
		return nil, err
	}
	args := []string{"format-patch", "--output-directory", p.Dir}
	if p.Numbered {
		args = append(args, "--numbered")
	}
	if len(p.Revs) == 0 {
		args = append(args, "@{upstream}..HEAD")
	}
	for _, rev := range p.Revs {
		if err := checkRev(rev); err != nil {
			return nil, err
		}
		args = append(args, rev)
	}
	out, err := r.git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// checkOutputDir accepts a directory that is already there and is a
// directory: git would make one from a path of any shape, and a patch is
// never written where a name was mistyped.
func checkOutputDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("git format-patch: %q is not an absolute path", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("git format-patch: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("git format-patch: %s is not a directory", dir)
	}
	return nil
}
