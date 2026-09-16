package claude

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is a Claude Code release number.
type Version struct {
	Major, Minor, Patch int
	// Pre is a prerelease suffix without its leading '-', empty for releases.
	Pre string
}

// MinVersion is the oldest Claude Code release every generated setting and
// launch flag is known to work with. It is the newest "Added" entry among
// the features lyna-tmux writes, from the Claude Code changelog:
//
//   - 2.1.257 permissions.blockReadsOutsideWorkingDirectories (strict profile)
//   - 2.1.219 sandbox.network.strictAllowlist and workflowSizeGuideline
//   - 2.1.203 --effort ultracode
//   - 2.1.200 the manual permission mode name
//   - 2.1.187 sandbox.credentials
//   - 2.1.169 id and state in `claude agents --json`
//
// Older releases ignore or reject some of them, so a launch would silently
// run with a weaker sandbox than the profile promises.
var MinVersion = Version{Major: 2, Minor: 1, Patch: 257}

// ErrTooOld matches every version gate failure.
var ErrTooOld = errors.New("claude: Claude Code is older than lyna-tmux supports")

// ErrVersionFormat is returned for output that is not a Claude Code version.
var ErrVersionFormat = errors.New("claude: unrecognized version output")

// ParseVersion reads `claude --version` output such as
// "2.1.272 (Claude Code)". Only the first line is considered; the number may
// carry a "-prerelease" suffix and be followed by a space and free text.
func ParseVersion(output string) (Version, error) {
	line, _, _ := strings.Cut(strings.TrimLeft(output, " \t\r\n"), "\n")
	line = strings.TrimRight(line, " \t\r")
	word, rest, _ := strings.Cut(line, " ")
	if rest != "" && !strings.HasPrefix(rest, "(") {
		return Version{}, fmt.Errorf("%w: %q", ErrVersionFormat, clip(line))
	}
	number, pre, hasPre := strings.Cut(word, "-")
	parts := strings.Split(number, ".")
	if len(parts) != 3 || (hasPre && !validPre(pre)) {
		return Version{}, fmt.Errorf("%w: %q", ErrVersionFormat, clip(line))
	}
	var nums [3]int
	for i, p := range parts {
		n, ok := component(p)
		if !ok {
			return Version{}, fmt.Errorf("%w: %q", ErrVersionFormat, clip(line))
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// component parses a decimal version component of at most 9 digits.
func component(s string) (int, bool) {
	if s == "" || len(s) > 9 || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func validPre(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	return strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-") == ""
}

func clip(s string) string {
	const limit = 80
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

// String renders the version as Claude prints it, without the suffix text.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders versions: -1 when v is older than o, 0 when equal, 1 when
// newer. A prerelease sorts before the release with the same number;
// prerelease suffixes compare as strings.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	case v.Pre < o.Pre:
		return -1
	default:
		return 1
	}
}

// AtLeast reports whether v is o or newer.
func (v Version) AtLeast(o Version) bool { return v.Compare(o) >= 0 }

// CheckVersion returns an ErrTooOld error with upgrade guidance when v is
// older than MinVersion.
func CheckVersion(v Version) error {
	if v.AtLeast(MinVersion) {
		return nil
	}
	return fmt.Errorf("%w: found %s, need %s or newer; run `claude update`", ErrTooOld, v, MinVersion)
}
