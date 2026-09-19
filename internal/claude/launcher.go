package claude

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/claudecfg"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// launcherFile is the mode of a written launcher: read and run by its owner
// only. Claude Code runs the file, so it needs the execute bit, and nobody
// else on the machine may read or replace what an agent is started with.
const launcherFile fs.FileMode = 0o700

// launcherShebang is what claudecfg.TeammateLauncher renders first, and the
// only thing WriteLauncher accepts: the file is made executable, so it writes
// nothing it did not render itself.
const launcherShebang = "#!/bin/sh\n"

// WriteLauncher stores a rendered teammate launcher in launchersDir under its
// content-addressed name and returns the file path. The directory is created
// with mode 0700 and the file written atomically with mode 0700; a symbolic
// link at either is refused.
//
// The name follows the script, which holds nothing but the two binaries a
// workspace resolved, so a machine keeps one file per pair of paths however
// many workspaces are open, and a moved binary writes a new one beside it
// rather than changing the file a running team is starting teammates with.
func WriteLauncher(launchersDir, script string) (string, error) {
	if !filepath.IsAbs(launchersDir) {
		return "", fmt.Errorf("claude: launcher directory must be absolute (got %q)", launchersDir)
	}
	if !strings.HasPrefix(script, launcherShebang) {
		return "", errors.New("claude: refusing to write an executable file that is not a rendered launcher")
	}
	if err := fsx.EnsurePrivateDir(launchersDir); err != nil {
		return "", fmt.Errorf("claude: launcher directory: %w", err)
	}
	path := filepath.Join(launchersDir, claudecfg.LauncherFileName(script))
	if err := fsx.WriteFileAtomic(path, []byte(script), launcherFile); err != nil {
		return "", fmt.Errorf("claude: write launcher: %w", err)
	}
	return path, nil
}
