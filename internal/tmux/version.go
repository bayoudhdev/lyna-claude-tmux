package tmux

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MinMajor and MinMinor are the oldest tmux release lyna-tmux supports.
const (
	MinMajor = 3
	MinMinor = 3
)

// ErrUnsupported is returned for a tmux older than MinMajor.MinMinor.
var ErrUnsupported = fmt.Errorf("tmux %d.%d or newer is required", MinMajor, MinMinor)

// Version is a parsed `tmux -V` result.
type Version struct {
	Major  int
	Minor  int
	Suffix string // patch letter ("a", "c") or pre-release tag ("rc2")
	Raw    string
}

// ParseVersion parses the output of `tmux -V`: "tmux 3.7c", "tmux next-3.8",
// "tmux 3.2-rc4", "tmux master" or "tmux openbsd-7.6".
func ParseVersion(out string) (Version, error) {
	raw := strings.TrimSpace(out)
	v := strings.TrimSpace(strings.TrimPrefix(raw, "tmux"))
	if v == "" {
		return Version{}, fmt.Errorf("tmux: empty version string %q", out)
	}
	switch {
	case v == "master":
		// Built from the development branch: newer than any release.
		return Version{Major: 99, Raw: raw}, nil
	case strings.HasPrefix(v, "next-"):
		// "next-3.8" is the branch that will become 3.8, at any point of that
		// cycle: it carries the number of a release whose features it may not
		// all have yet. The features of the release before it are the ones it
		// certainly has, so that is what the generator may rely on.
		ver, err := parseNumeric(strings.TrimPrefix(v, "next-"))
		if err == nil {
			ver = ver.beforeRelease()
		}
		ver.Suffix, ver.Raw = "next", raw
		return ver, err
	case strings.HasPrefix(v, "openbsd-"):
		ver, err := openBSD(strings.TrimPrefix(v, "openbsd-"))
		ver.Raw = raw
		return ver, err
	}
	ver, err := parseNumeric(v)
	ver.Raw = raw
	return ver, err
}

// beforeRelease is the release published before v: one minor back, and for a
// first minor the last minor of the previous major, which tmux has never gone
// past 9 of.
func (v Version) beforeRelease() Version {
	switch {
	case v.Minor > 0:
		return Version{Major: v.Major, Minor: v.Minor - 1}
	case v.Major > 0:
		return Version{Major: v.Major - 1, Minor: 9}
	default:
		return Version{}
	}
}

func parseNumeric(v string) (Version, error) {
	majorStr, rest, ok := strings.Cut(v, ".")
	if !ok {
		return Version{}, fmt.Errorf("tmux: cannot parse version %q", v)
	}
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return Version{}, fmt.Errorf("tmux: cannot parse major version %q: %w", v, err)
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return Version{}, fmt.Errorf("tmux: cannot parse minor version %q", v)
	}
	minor, err := strconv.Atoi(rest[:i])
	if err != nil {
		return Version{}, fmt.Errorf("tmux: cannot parse minor version %q: %w", v, err)
	}
	return Version{Major: major, Minor: minor, Suffix: strings.TrimLeft(rest[i:], "-")}, nil
}

// openBSD maps an OpenBSD base system release to the tmux release it ships.
func openBSD(v string) (Version, error) {
	rel, err := parseNumeric(v)
	if err != nil {
		return Version{}, err
	}
	switch {
	case rel.Major > 7 || rel.Major == 7 && rel.Minor >= 7:
		return Version{Major: 3, Minor: 5}, nil
	case rel.Major == 7 && rel.Minor >= 5:
		return Version{Major: 3, Minor: 4}, nil
	case rel.Major == 7 && rel.Minor >= 2:
		return Version{Major: 3, Minor: 3}, nil
	default:
		return Version{Major: 3, Minor: 2}, nil
	}
}

// AtLeast reports whether v is major.minor or newer.
func (v Version) AtLeast(major, minor int) bool {
	return v.Major > major || v.Major == major && v.Minor >= minor
}

// Supported reports whether v meets the minimum version.
func (v Version) Supported() bool { return v.AtLeast(MinMajor, MinMinor) }

// String renders "3.7c".
func (v Version) String() string {
	if v.Major == 99 {
		return "master"
	}
	return fmt.Sprintf("%d.%d%s", v.Major, v.Minor, v.Suffix)
}

// Feature is a tmux capability that appeared after the minimum supported release.
type Feature int

// Features gated by version. Everything the generator emits unconditionally
// exists in tmux 3.3.
const (
	FeatureUserRanges         Feature = iota + 1 // 3.4: #[range=user|id] and mouse_status_range
	FeatureMenuStyles                            // 3.4: menu-style, menu-selected-style, menu-border-style, menu-border-lines, display-menu -b/-s/-S/-H
	FeatureConfirmFlags                          // 3.4: confirm-before -c and -y
	FeatureMessageLine                           // 3.4: message-line
	FeatureExtendedKeysFormat                    // 3.5: extended-keys-format
	FeatureAllowSetTitle                         // 3.5: allow-set-title
	FeaturePaneScrollbars                        // 3.6: pane-scrollbars, pane-scrollbars-style, pane-scrollbars-position
	FeaturePopupAnyKey                           // 3.6: display-popup -k
	FeatureFocusFollowsMouse                     // 3.7: focus-follows-mouse
)

var featureVersions = map[Feature][2]int{
	FeatureUserRanges:         {3, 4},
	FeatureMenuStyles:         {3, 4},
	FeatureConfirmFlags:       {3, 4},
	FeatureMessageLine:        {3, 4},
	FeatureExtendedKeysFormat: {3, 5},
	FeatureAllowSetTitle:      {3, 5},
	FeaturePaneScrollbars:     {3, 6},
	FeaturePopupAnyKey:        {3, 6},
	FeatureFocusFollowsMouse:  {3, 7},
}

var errUnknownFeature = errors.New("tmux: unknown feature")

// Has reports whether v provides f. Unknown features are reported as missing.
func (v Version) Has(f Feature) bool {
	mm, ok := featureVersions[f]
	return ok && v.AtLeast(mm[0], mm[1])
}

// FeatureMinimum returns the first release providing f.
func FeatureMinimum(f Feature) (major, minor int, err error) {
	mm, ok := featureVersions[f]
	if !ok {
		return 0, 0, errUnknownFeature
	}
	return mm[0], mm[1], nil
}
