package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// uninstallNames resolves the executable uninstall removes and the link the
// installers leave under the name the command had in 1.0.0. Run through that
// link, exe is the link itself, and the binary is the file next to it the link
// points at; run under its own name, the link is the one beside it that points
// back. A link to anything else, in either direction, belongs to whoever made
// it and is left alone, named to the user by uninstallBinary.
func uninstallNames(exe string) (bin, alias string) {
	if exe == "" {
		return "", ""
	}
	dir := filepath.Dir(exe)
	sibling := func(name string) string { return filepath.Join(dir, name) }
	linksTo := func(link, target string) bool {
		got, err := os.Readlink(link)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(got) {
			got = filepath.Join(filepath.Dir(link), got)
		}
		return filepath.Clean(got) == filepath.Clean(target)
	}
	if filepath.Base(exe) == xdg.CommandWas && linksTo(exe, sibling(xdg.Command)) {
		return sibling(xdg.Command), exe
	}
	if link := sibling(xdg.CommandWas); link != exe && linksTo(link, exe) {
		return exe, link
	}
	return exe, ""
}

// uninstallBinary decides what happens to the executable at exe: uninstall
// removes it, or leaves it to the user with the reason and the command.
// Uninstall removes it only when it is a regular file the user (uid) owns, in
// a directory the process can write, outside every package manager prefix: a
// manager keeps a record of the package that only its own command updates,
// and the link it put in its bin directory would be left dangling. A link is
// never followed: whatever it points at was placed by something else. An
// absent file is nothing to do. Unlinking the running executable is allowed
// on darwin and linux, the process keeps its mapped image, so the running
// binary needs no special case.
func uninstallBinary(exe string, uid int) (remove string, manual *UninstallManual, err error) {
	if exe == "" {
		return "", nil, nil
	}
	info, err := os.Lstat(exe)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	keep := func(reason, how string) (string, *UninstallManual, error) {
		return "", &UninstallManual{Step: fmt.Sprintf("remove the binary %s: %s", exe, reason), How: how}, nil
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(exe)
		if err != nil {
			return "", nil, err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(exe), target)
		}
		if m, ok := uninstallManaged(target); ok {
			return keep("it is a link to "+target+", and "+m.reason, m.how)
		}
		return keep("it is a link to "+target, "rm "+exe+" "+target)
	}
	if !info.Mode().IsRegular() {
		return keep("it is not a regular file", "rm "+exe)
	}
	if m, ok := uninstallManaged(exe); ok {
		return keep(m.reason, m.how)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
		return keep("another user owns it", "sudo rm "+exe)
	}
	if err := unix.Access(filepath.Dir(exe), unix.W_OK); err != nil {
		return keep("you cannot write to "+filepath.Dir(exe), "sudo rm "+exe)
	}
	return exe, nil, nil
}

// uninstallManager is the package manager whose prefix holds a path: the
// reason uninstall leaves the file alone and the command that removes it.
type uninstallManager struct {
	reason string
	how    string
}

// uninstallManaged reports the package manager whose prefix holds path. The
// Homebrew cellar is recognized by its directory name because its prefix
// differs per platform and per install; the Nix store and the system prefix
// have fixed roots.
func uninstallManaged(path string) (uninstallManager, bool) {
	clean := filepath.Clean(path)
	switch {
	case slices.Contains(strings.Split(clean, string(filepath.Separator)), "Cellar"):
		return uninstallManager{reason: "Homebrew installed it", how: "brew uninstall " + xdg.AppName}, true
	case strings.HasPrefix(clean, "/nix/store/"):
		return uninstallManager{reason: "the Nix store holds it", how: "nix profile remove " + xdg.AppName}, true
	case strings.HasPrefix(clean, "/usr/"):
		return uninstallManager{reason: "it is in the system prefix /usr", how: "sudo rm " + clean}, true
	}
	return uninstallManager{}, false
}
