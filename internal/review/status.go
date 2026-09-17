package review

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// File names under the review directory.
const (
	InitFileName = "init.lua"
	LogFileName  = "nvim.log"
)

// PluginDir is the installed plugin directory.
func PluginDir(paths xdg.Paths) string {
	return filepath.Join(paths.ReviewDir(), domain.PluginDirName)
}

// InitFile is the generated init file of the isolated editor.
func InitFile(paths xdg.Paths) string { return filepath.Join(paths.ReviewDir(), InitFileName) }

// LogFile receives the isolated editor's Neovim log.
func LogFile(paths xdg.Paths) string { return filepath.Join(paths.ReviewDir(), LogFileName) }

// versionTimeout bounds `nvim --version`, which starts no UI and loads no configuration.
const versionTimeout = 5 * time.Second

// StatusOptions configure Status. Zero values use the running platform,
// the default pin, PATH lookup and real process execution.
type StatusOptions struct {
	Paths        xdg.Paths
	Pin          domain.Pin
	GOOS, GOARCH string
	// Nvim is the Neovim executable name or path; empty means "nvim".
	Nvim string
	// LookPath resolves Nvim; nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Run executes a binary and returns its standard output; nil runs it.
	Run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// AssetStatus is the state of one pinned native file.
type AssetStatus struct {
	File       string
	Present    bool
	ChecksumOK bool
	// Problem explains a missing, unreadable or modified file.
	Problem string
}

// Report is the state of the review editor.
type Report struct {
	PluginDir string
	Platform  string
	// PlatformProblem is set when no build is pinned for the platform.
	PlatformProblem string
	Installed       bool
	// DirProblem is set when the plugin path exists but is not a real directory.
	DirProblem string
	Commit     string
	CommitOK   bool
	Version    string
	VersionOK  bool
	Assets     []AssetStatus
	// Unverified lists files in the plugin root the plugin would load or run
	// although lyna-tmux did not install them.
	Unverified []string
	// InitFile reports whether the generated init file exists.
	InitFile bool

	Nvim           string
	NvimVersion    domain.NvimVersion
	NvimFound      bool
	NvimProblem    string
	NvimSupported  bool
	NvimRecommends bool
}

// PluginOK reports whether the pinned plugin is installed and verified.
func (r Report) PluginOK() bool {
	if !r.Installed || r.DirProblem != "" || !r.CommitOK || !r.VersionOK || r.PlatformProblem != "" || len(r.Unverified) > 0 {
		return false
	}
	for _, a := range r.Assets {
		if !a.ChecksumOK {
			return false
		}
	}
	return true
}

// Ready reports whether an isolated review can start: a verified plugin and
// a supported Neovim. The init file is written at launch, so it is not required.
func (r Report) Ready() bool {
	return r.PluginOK() && r.NvimFound && r.NvimSupported
}

// Problems lists what stands in the way, one sentence each, safe to print:
// the plugin problems, then the Neovim problems.
func (r Report) Problems() []string {
	return append(r.PluginProblems(), r.NvimProblems()...)
}

// PluginProblems lists what is wrong with the installed plugin; a review in
// the user's own Neovim does not depend on it.
func (r Report) PluginProblems() []string {
	var out []string
	switch {
	case r.PlatformProblem != "":
		out = append(out, r.PlatformProblem)
	case r.DirProblem != "":
		out = append(out, r.DirProblem)
	case !r.Installed:
		out = append(out, "codediff.nvim is not installed (run: lmux review install)")
	default:
		if !r.CommitOK {
			out = append(out, fmt.Sprintf("codediff.nvim is at commit %q, not the pinned one (run: lmux review install --force)", r.Commit))
		}
		if !r.VersionOK {
			out = append(out, fmt.Sprintf("codediff.nvim VERSION is %q, not the pinned one (run: lmux review install --force)", r.Version))
		}
		for _, a := range r.Assets {
			if !a.ChecksumOK {
				out = append(out, a.Problem)
			}
		}
		for _, name := range r.Unverified {
			out = append(out, fmt.Sprintf("unverified file %q in the plugin directory would be loaded; remove it or reinstall", name))
		}
	}
	return out
}

// NvimProblems lists what is wrong with the Neovim on PATH.
func (r Report) NvimProblems() []string {
	var out []string
	switch {
	case !r.NvimFound:
		out = append(out, r.NvimProblem)
	case !r.NvimSupported:
		out = append(out, fmt.Sprintf("Neovim %s is too old; %s or newer is required", r.NvimVersion, domain.MinNvim))
	}
	return out
}

// Warnings lists conditions that do not block a review.
func (r Report) Warnings() []string {
	if r.NvimFound && r.NvimSupported && !r.NvimRecommends {
		return []string{fmt.Sprintf("Neovim %s works; %s or newer is recommended", r.NvimVersion, domain.RecommendedNvim)}
	}
	return nil
}

func (o StatusOptions) resolved() StatusOptions {
	if o.Pin.Commit == "" {
		o.Pin = domain.DefaultPin()
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.Nvim == "" {
		o.Nvim = "nvim"
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = runOutput
	}
	return o
}

func runOutput(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// Status inspects the installation and the Neovim on PATH. It changes nothing.
func Status(ctx context.Context, opts StatusOptions) Report {
	opts = opts.resolved()
	dir := PluginDir(opts.Paths)
	r := Report{PluginDir: dir, Platform: opts.GOOS + "/" + opts.GOARCH}
	r.InitFile = regularFile(InitFile(opts.Paths))
	statusNvim(ctx, opts, &r)

	assets, err := opts.Pin.AssetsFor(opts.GOOS, opts.GOARCH)
	if err != nil {
		r.PlatformProblem = err.Error()
	}
	if err := checkRealDir(dir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.Installed = true
			r.DirProblem = sanitize.Line(err.Error()) + " (remove it, then run: lmux review install)"
		}
		return r
	}
	r.Installed = true
	if commit, err := readHead(dir); err == nil {
		r.Commit = sanitize.Line(commit)
		r.CommitOK = commit == opts.Pin.Commit
	}
	if version, err := readVersion(dir); err == nil {
		r.Version = sanitize.Line(version)
		r.VersionOK = version == opts.Pin.Version
	}
	for _, a := range assets {
		r.Assets = append(r.Assets, assetStatus(dir, a))
	}
	r.Unverified = unverifiedFiles(dir, opts.GOOS)
	return r
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func assetStatus(dir string, a domain.Asset) AssetStatus {
	s := AssetStatus{File: a.File}
	ok, err := fileMatches(filepath.Join(dir, a.File), a.SHA256)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.Problem = fmt.Sprintf("%s is missing (run: lmux review install --force)", a.File)
	case err != nil:
		s.Present = true
		s.Problem = fmt.Sprintf("%s cannot be verified: %s", a.File, sanitize.Line(err.Error()))
	case !ok:
		s.Present = true
		s.Problem = fmt.Sprintf("%s does not match its pinned checksum (run: lmux review install --force)", a.File)
	default:
		s.Present, s.ChecksumOK = true, true
	}
	return s
}

func unverifiedFiles(dir, goos string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if domain.IsUnverifiedFile(e.Name(), goos) {
			out = append(out, sanitize.Line(e.Name()))
		}
	}
	slices.Sort(out)
	return out
}

func statusNvim(ctx context.Context, opts StatusOptions, r *Report) {
	path, err := opts.LookPath(opts.Nvim)
	if err != nil {
		r.NvimProblem = fmt.Sprintf("Neovim (%s) was not found on PATH; install Neovim %s or newer", sanitize.Line(opts.Nvim), domain.MinNvim)
		return
	}
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}
	r.Nvim = path
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := opts.Run(ctx, path, "--version")
	if err != nil {
		r.NvimProblem = fmt.Sprintf("%s --version failed: %s", sanitize.Line(path), sanitize.Line(err.Error()))
		return
	}
	v, err := domain.ParseNvimVersion(string(out))
	if err != nil {
		r.NvimProblem = sanitize.Line(err.Error())
		return
	}
	r.NvimFound = true
	r.NvimVersion = v
	r.NvimSupported = v.Supported()
	r.NvimRecommends = v.Recommended()
}

// Uninstall removes the review directory: the plugin, its native files, the
// init file and the editor log. A symbolic link in its place is refused
// rather than followed. A missing directory is not an error.
func Uninstall(paths xdg.Paths) error {
	dir := paths.ReviewDir()
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
		return fmt.Errorf("review: refusing to remove %q", dir)
	}
	if err := checkRealDir(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("review: remove %s: %w", dir, err)
	}
	return nil
}
