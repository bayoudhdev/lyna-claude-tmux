package review

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NvimVersion is a Neovim release as `nvim --version` reports it.
type NvimVersion struct {
	Major, Minor, Patch int
	// Suffix is the build suffix including its leading '-' or '+'
	// ("-dev-123+gabc1234"), empty for releases.
	Suffix string
}

// Neovim versions lyna-tmux knows about.
var (
	// MinNvim is the oldest supported release: it has nvim_cmd and vim.json.
	MinNvim = NvimVersion{Major: 0, Minor: 9}
	// RecommendedNvim is the release codediff.nvim recommends.
	RecommendedNvim = NvimVersion{Major: 0, Minor: 10}
)

// ErrNvimVersion reports output that does not start with a Neovim version line.
var ErrNvimVersion = errors.New("review: unrecognized Neovim version")

// Bounds on version text; real versions are far shorter.
const (
	maxVersionDigits = 6
	maxVersionSuffix = 64
)

// ParseNvimVersion reads the first line of `nvim --version` output,
// "NVIM v0.12.5" optionally followed by a build suffix.
func ParseNvimVersion(output string) (NvimVersion, error) {
	line, _, _ := strings.Cut(output, "\n")
	line = strings.TrimSuffix(line, "\r")
	rest, ok := strings.CutPrefix(line, "NVIM v")
	if !ok {
		return NvimVersion{}, fmt.Errorf("%w: %q", ErrNvimVersion, clip(line))
	}
	end := strings.IndexAny(rest, "-+")
	core, suffix := rest, ""
	if end >= 0 {
		core, suffix = rest[:end], rest[end:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return NvimVersion{}, fmt.Errorf("%w: %q", ErrNvimVersion, clip(line))
	}
	var nums [3]int
	for i, p := range parts {
		n, err := versionNumber(p)
		if err != nil {
			return NvimVersion{}, fmt.Errorf("%w: %q", ErrNvimVersion, clip(line))
		}
		nums[i] = n
	}
	if !validSuffix(suffix) {
		return NvimVersion{}, fmt.Errorf("%w: %q", ErrNvimVersion, clip(line))
	}
	return NvimVersion{Major: nums[0], Minor: nums[1], Patch: nums[2], Suffix: suffix}, nil
}

func versionNumber(s string) (int, error) {
	if s == "" || len(s) > maxVersionDigits {
		return 0, ErrNvimVersion
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrNvimVersion
		}
	}
	return strconv.Atoi(s)
}

func validSuffix(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > maxVersionSuffix || len(s) < 2 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+'
		if !ok {
			return false
		}
	}
	return true
}

func clip(s string) string {
	const limit = 80
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}

// String renders the canonical form, "v0.12.5-dev-123+gabc".
func (v NvimVersion) String() string {
	return fmt.Sprintf("v%d.%d.%d%s", v.Major, v.Minor, v.Patch, v.Suffix)
}

// AtLeast compares the release numbers, ignoring the suffix: a development
// build of 0.10 already has the 0.10 APIs.
func (v NvimVersion) AtLeast(o NvimVersion) bool {
	if v.Major != o.Major {
		return v.Major > o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor > o.Minor
	}
	return v.Patch >= o.Patch
}

// Supported reports whether the version can run a review.
func (v NvimVersion) Supported() bool { return v.AtLeast(MinNvim) }

// Recommended reports whether the version is the recommended one or newer.
func (v NvimVersion) Recommended() bool { return v.AtLeast(RecommendedNvim) }
