package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandDir turns a directory argument into an absolute, cleaned path, the
// same way for every command that takes one: "~" is the home directory, "~/x"
// starts there, a relative path starts at the working directory, and an empty
// argument is the working directory itself. The working directory is only
// asked for when it is needed, so a command given an absolute path works in a
// directory that no longer exists.
//
// A bare tilde is its own case: CutPrefix leaves it in the remainder, which
// would turn "~" into "<home>/~".
func expandDir(dir, home string, getwd func() (string, error)) (string, error) {
	switch rest, ok := strings.CutPrefix(dir, "~/"); {
	case ok:
		dir = filepath.Join(home, rest)
	case dir == "~":
		dir = home
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, dir), nil
}

// existingDir resolves a directory argument with expandDir and reports one
// that is not an existing directory, for the forms where the answer comes from
// the user rather than from a pane.
func existingDir(dir, home string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("a directory is required")
	}
	resolved, err := expandDir(strings.TrimSpace(dir), home, getwd)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("no such directory: %s", resolved)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}
