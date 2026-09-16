package review

import (
	"errors"
	"fmt"
	"strings"
)

// PluginDirName is the directory the plugin is installed into under the review directory.
const PluginDirName = "codediff.nvim"

// Asset is one native file of a plugin release.
type Asset struct {
	// Name is the release download file name.
	Name string
	// File is the name the plugin loads from its root directory.
	File string
	// SHA256 is the lowercase hex digest the download must match.
	SHA256 string
}

// Pin fixes the exact plugin build lyna-tmux installs.
type Pin struct {
	// Repo is the git repository URL.
	Repo string
	// Commit is the full commit id of the release.
	Commit string
	// Version is the content of the VERSION file at that commit.
	Version string
	// Assets are the native files per "goos/goarch".
	Assets map[string][]Asset
}

// DefaultPin is codediff.nvim v4.0.6. The digests come from the release's
// SHA256SUMS; the commit is the one the v4.0.6 tag points to.
func DefaultPin() Pin {
	const v = "4.0.6"
	return Pin{
		Repo:    "https://github.com/esmuellert/codediff.nvim",
		Commit:  "09d9ebef2cc5a5c04db7a349cd6c61bdf84ecc8e",
		Version: v,
		Assets: map[string][]Asset{
			"darwin/arm64": {
				{Name: "libvscode_diff_macos_arm64_" + v + ".dylib", File: LibraryFile(v, "darwin"), SHA256: "4e3577b58adde87fc40356eb238485418b6469c3251ea9a68675c8ae519866a2"},
			},
			"darwin/amd64": {
				{Name: "libvscode_diff_macos_x64_" + v + ".dylib", File: LibraryFile(v, "darwin"), SHA256: "aca253c08c7bf15ed849c6ddcfb830a078825072d1b7e8f44d34367dc7107963"},
			},
			"linux/arm64": {
				{Name: "libvscode_diff_linux_arm64_" + v + ".so", File: LibraryFile(v, "linux"), SHA256: "4b522303071cadc45caf7b482821990757699325b13cfb615da5ddf2af7aaabb"},
				{Name: "libgomp_linux_arm64_" + v + ".so.1", File: LibgompFile, SHA256: "d22c30c542f449e84e9932415575633c0a1f7abb5fa5ece2c0499647a16b2b0c"},
			},
			"linux/amd64": {
				{Name: "libvscode_diff_linux_x64_" + v + ".so", File: LibraryFile(v, "linux"), SHA256: "ac2a1d416dc99f7d864c0362db4b5baa6633fa68ebc90c9c9f3b3a9ae9cce081"},
				{Name: "libgomp_linux_x64_" + v + ".so.1", File: LibgompFile, SHA256: "a34b323345bf3adaaf328c7b59be88cd8b197718c318847ed4be7e7b473747d0"},
			},
		},
	}
}

// LibgompFile is the OpenMP runtime the Linux library finds beside itself.
const LibgompFile = "libgomp.so.1"

// ErrUnsupportedPlatform reports a platform without pinned assets.
var ErrUnsupportedPlatform = errors.New("review: no codediff.nvim build for this platform")

// ErrInvalidPin reports a malformed pin.
var ErrInvalidPin = errors.New("review: invalid plugin pin")

// LibraryFile is the versioned native library name the plugin loads.
func LibraryFile(version, goos string) string {
	return "libvscode_diff_" + version + "." + libraryExt(goos)
}

// IsUnverifiedFile reports whether a file in the plugin root is something the
// plugin would load or run that lyna-tmux never installs: a manual build of the
// diff library (tried when the versioned one fails to load) or a file watcher
// executable (run before any other watcher).
func IsUnverifiedFile(name, goos string) bool {
	return name == "libvscode_diff."+libraryExt(goos) || strings.HasPrefix(name, "codediff-watcher")
}

func libraryExt(goos string) string {
	if goos == "darwin" {
		return "dylib"
	}
	return "so"
}

// AssetsFor returns the assets for a platform.
func (p Pin) AssetsFor(goos, goarch string) ([]Asset, error) {
	assets, ok := p.Assets[goos+"/"+goarch]
	if !ok || len(assets) == 0 {
		return nil, fmt.Errorf("%w: %s/%s (available: macOS and Linux on arm64 and amd64)", ErrUnsupportedPlatform, goos, goarch)
	}
	return assets, nil
}

// ReleaseURL is the download directory of the pinned release.
func (p Pin) ReleaseURL() string {
	return strings.TrimSuffix(p.Repo, "/") + "/releases/download/v" + p.Version
}

// Validate checks the pin's shape: a full lowercase commit id, a version
// usable in file names, and well-formed digests and file names.
func (p Pin) Validate() error {
	if !isHex(p.Commit, 40) {
		return fmt.Errorf("%w: commit %q is not a full lowercase commit id", ErrInvalidPin, p.Commit)
	}
	if p.Version == "" || strings.ContainsAny(p.Version, "/\\ \n") || strings.HasPrefix(p.Version, ".") {
		return fmt.Errorf("%w: version %q", ErrInvalidPin, p.Version)
	}
	for platform, assets := range p.Assets {
		for _, a := range assets {
			if !isHex(a.SHA256, 64) {
				return fmt.Errorf("%w: %s asset %q has digest %q", ErrInvalidPin, platform, a.Name, a.SHA256)
			}
			if !plainFileName(a.Name) || !plainFileName(a.File) {
				return fmt.Errorf("%w: %s asset names %q and %q must be plain file names", ErrInvalidPin, platform, a.Name, a.File)
			}
		}
	}
	return nil
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := range len(s) {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}

func plainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
