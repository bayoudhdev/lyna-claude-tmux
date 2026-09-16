package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// fakeSystem is an in-memory machine for checks. Every map is read-only once a
// test starts, so the concurrent checks in Run can share it.
type fakeSystem struct {
	goos    string
	env     map[string]string
	bins    map[string]string // executable name -> path
	files   map[string]string // path -> content
	errs    map[string]error  // path -> read error
	outputs map[string]fakeOutput
	exists  map[string]bool
}

type fakeOutput struct {
	out string
	err error
}

var errNotFound = errors.New("executable file not found in $PATH")

func (f fakeSystem) deps() Deps {
	return Deps{
		GOOS: f.goos,
		LookPath: func(name string) (string, error) {
			if p, ok := f.bins[name]; ok {
				return p, nil
			}
			return "", fmt.Errorf("exec: %q: %w", name, errNotFound)
		},
		Getenv: func(k string) string { return f.env[k] },
		ReadFile: func(path string, limit int64) ([]byte, error) {
			if err, ok := f.errs[path]; ok {
				return nil, err
			}
			content, ok := f.files[path]
			if !ok {
				return nil, fmt.Errorf("open %s: %w", path, fs.ErrNotExist)
			}
			if int64(len(content)) > limit {
				return nil, fsx.ErrTooLarge
			}
			return []byte(content), nil
		},
		Run: func(ctx context.Context, argv []string) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("fake: command run without a deadline")
			}
			o, ok := f.outputs[strings.Join(argv, " ")]
			if !ok {
				return "", fmt.Errorf("fake: unexpected command %q", argv)
			}
			return o.out, o.err
		},
		Exists:         func(path string) bool { return f.exists[path] },
		ClaudeHome:     "/home/u/.claude",
		AltKeys:        true,
		Prefix:         "C-b",
		SandboxProfile: "standard",
		Isolation:      "bash",
		Timeout:        time.Second,
	}
}

const (
	osReleaseUbuntu = "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n"
	osReleaseFedora = "NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=41\n"
	procWSL1        = "Linux version 4.4.0-19041-Microsoft (Microsoft@Microsoft.com) (gcc version 5.4.0 (GCC) ) #1237-Microsoft Sat Sep 11 14:32:00 PST 2021\n"
	procWSL2        = "Linux version 5.15.153.1-microsoft-standard-WSL2 (root@941d701f84f1) (gcc (GCC) 12.2.0, GNU ld (GNU Binutils) 2.40) #1 SMP Fri Mar 29 23:14:13 UTC 2024\n"
	procLinux       = "Linux version 6.8.0-45-generic (buildd@lcy02-amd64-075) (x86_64-linux-gnu-gcc-13 (Ubuntu 13.2.0-23ubuntu4) 13.2.0) #45-Ubuntu SMP PREEMPT_DYNAMIC\n"
)
