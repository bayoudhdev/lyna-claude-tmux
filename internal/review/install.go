// Package review installs, verifies and launches the review editor: the
// pinned codediff.nvim plugin in lyna-tmux's own data directory and the
// Neovim command that opens it. The decisions (what is pinned, which
// arguments and which Neovim command line) come from internal/domain/review.
package review

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/sanitize"
)

// Defaults of an installation.
const (
	// MaxAssetBytes bounds one native download; the real libraries are a few MiB.
	MaxAssetBytes = 32 << 20
	// DefaultInstallTimeout applies when the caller's context has no deadline.
	DefaultInstallTimeout = 10 * time.Minute
	// maxRedirects matches the standard client's own limit.
	maxRedirects = 10
)

// Errors of an installation.
var (
	// ErrInsecureURL reports a download or redirect that is not HTTPS.
	ErrInsecureURL = errors.New("review: downloads must use https")
	// ErrChecksum reports a download whose SHA256 differs from the pin.
	ErrChecksum = errors.New("review: checksum mismatch")
	// ErrVerify reports a fetched plugin that is not the pinned commit or version.
	ErrVerify = errors.New("review: fetched plugin does not match the pin")
	// ErrGitMissing reports that git could not be started.
	ErrGitMissing = errors.New("review: git is required to install codediff.nvim")
	// ErrBusy reports another installation running on the same directory.
	ErrBusy = errors.New("review: another installation is in progress")
)

// InstallOptions configure Install. Only ReviewDir is required.
type InstallOptions struct {
	// ReviewDir is xdg.Paths.ReviewDir().
	ReviewDir string
	// Force reinstalls even when a verified installation exists.
	Force bool
	// Pin is the plugin build; the zero value means domain.DefaultPin().
	Pin domain.Pin
	// GOOS and GOARCH select the native assets; empty means the running platform.
	GOOS, GOARCH string
	// RepoURL overrides Pin.Repo (tests use a local repository).
	RepoURL string
	// ReleaseURL overrides Pin.ReleaseURL().
	ReleaseURL string
	// Git is the git executable; empty means "git" from PATH.
	Git string
	// HTTPClient downloads the assets; nil means a client with the default transport.
	HTTPClient *http.Client
	// Environ is the environment git runs with; nil means os.Environ().
	Environ []string
	// MaxAssetBytes overrides the per-download size cap.
	MaxAssetBytes int64
}

// InstallResult describes the installation on disk.
type InstallResult struct {
	// PluginDir is where the plugin lives.
	PluginDir string
	Commit    string
	Version   string
	// Files are the native files installed into the plugin root.
	Files []string
	// Reused is true when a verified installation was already in place.
	Reused bool
}

func (o InstallOptions) resolved() InstallOptions {
	if o.Pin.Commit == "" {
		o.Pin = domain.DefaultPin()
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.RepoURL == "" {
		o.RepoURL = o.Pin.Repo
	}
	if o.ReleaseURL == "" {
		o.ReleaseURL = o.Pin.ReleaseURL()
	}
	if o.Git == "" {
		o.Git = "git"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.MaxAssetBytes <= 0 {
		o.MaxAssetBytes = MaxAssetBytes
	}
	return o
}

// Install puts the pinned plugin and its checksum-verified native files into
// <ReviewDir>/codediff.nvim. Everything is prepared in a private staging
// directory beside the destination and moved into place with rename, and the
// previous installation is removed only once the new one is in place: a
// failure at any step leaves the previous installation as it was and no
// staging directory behind.
func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	opts = opts.resolved()
	if err := opts.Pin.Validate(); err != nil {
		return InstallResult{}, err
	}
	assets, err := opts.Pin.AssetsFor(opts.GOOS, opts.GOARCH)
	if err != nil {
		return InstallResult{}, err
	}
	dest := filepath.Join(opts.ReviewDir, domain.PluginDirName)
	// Refuse a location Neovim cannot load the plugin from before any download.
	if err := domain.ValidatePluginDir(dest); err != nil {
		return InstallResult{}, err
	}
	if err := fsx.EnsurePrivateDir(opts.ReviewDir); err != nil {
		return InstallResult{}, err
	}
	if err := checkRealDir(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return InstallResult{}, err
	}

	unlock, err := lockDir(opts.ReviewDir)
	if err != nil {
		return InstallResult{}, err
	}
	defer unlock()

	files := make([]string, len(assets))
	for i, a := range assets {
		files[i] = a.File
	}
	result := InstallResult{PluginDir: dest, Commit: opts.Pin.Commit, Version: opts.Pin.Version, Files: files}
	if !opts.Force && verifiedInstall(dest, opts.Pin, assets) {
		result.Reused = true
		return result, nil
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultInstallTimeout)
		defer cancel()
	}

	stage, err := os.MkdirTemp(opts.ReviewDir, ".staging-")
	if err != nil {
		return InstallResult{}, fmt.Errorf("review: create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	staged := filepath.Join(stage, domain.PluginDirName)
	if err := fetchPlugin(ctx, opts, staged); err != nil {
		return InstallResult{}, err
	}
	for _, a := range assets {
		if err := download(ctx, opts, opts.ReleaseURL+"/"+a.Name, filepath.Join(staged, a.File), a.SHA256); err != nil {
			return InstallResult{}, err
		}
	}
	if err := swapInto(staged, dest, filepath.Join(stage, "previous")); err != nil {
		return InstallResult{}, err
	}
	return result, nil
}

// checkRealDir fails unless path is a directory reached without following a
// final symbolic link.
func checkRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s: %w", path, fsx.ErrSymlink)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", path, fsx.ErrNotDir)
	}
	return nil
}

// verifiedInstall reports whether dest already holds the pinned build.
func verifiedInstall(dest string, pin domain.Pin, assets []domain.Asset) bool {
	if checkRealDir(dest) != nil {
		return false
	}
	if commit, err := readHead(dest); err != nil || commit != pin.Commit {
		return false
	}
	if version, err := readVersion(dest); err != nil || version != pin.Version {
		return false
	}
	for _, a := range assets {
		if ok, err := fileMatches(filepath.Join(dest, a.File), a.SHA256); err != nil || !ok {
			return false
		}
	}
	return true
}

// gitEnv is the environment for git: prompts are disabled, and variables
// that point git at another repository, index or object store are removed so
// a caller running inside a git hook cannot redirect the installation.
func gitEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
			"GIT_TERMINAL_PROMPT", "GIT_PREFIX", "GIT_QUARANTINE_PATH":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0")
}

// git runs one git command in dir with hooks disabled.
func git(ctx context.Context, opts InstallOptions, dir string, args ...string) (string, error) {
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "advice.detachedHead=false", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, opts.Git, argv...)
	cmd.Env = gitEnv(opts.Environ)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("review: git %s: %w", args[0], ctxErr)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", fmt.Errorf("%w: %w", ErrGitMissing, err)
		}
		return "", fmt.Errorf("review: git %s failed: %s", args[0], gitMessage(stderr.String(), err))
	}
	return stdout.String(), nil
}

// gitMessage keeps the last lines of git's error output, made safe to print.
func gitMessage(stderr string, err error) string {
	const limit = 512
	msg := strings.TrimSpace(sanitize.Plain(stderr))
	if len(msg) > limit {
		msg = "..." + msg[len(msg)-limit:]
	}
	if msg == "" {
		return err.Error()
	}
	return strings.ReplaceAll(msg, "\n", "; ")
}

// fetchPlugin clones exactly the pinned commit into dir and verifies it.
func fetchPlugin(ctx context.Context, opts InstallOptions, dir string) error {
	if err := os.Mkdir(dir, fsx.PrivateDir); err != nil {
		return fmt.Errorf("review: create %s: %w", dir, err)
	}
	steps := [][]string{
		{"init", "-q", "--template="},
		{"fetch", "-q", "--depth", "1", "--no-tags", opts.RepoURL, opts.Pin.Commit},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, step := range steps {
		if _, err := git(ctx, opts, dir, step...); err != nil {
			return err
		}
	}
	out, err := git(ctx, opts, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(out); got != opts.Pin.Commit {
		return fmt.Errorf("%w: checked out commit %q, pinned %s", ErrVerify, sanitize.Line(got), opts.Pin.Commit)
	}
	version, err := readVersion(dir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVerify, err)
	}
	if version != opts.Pin.Version {
		return fmt.Errorf("%w: VERSION is %q, pinned %s", ErrVerify, sanitize.Line(version), opts.Pin.Version)
	}
	return nil
}

// readVersion reads the first line of the plugin's VERSION file.
func readVersion(pluginDir string) (string, error) {
	data, err := fsx.ReadFileNoFollow(filepath.Join(pluginDir, "VERSION"), 256)
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line), nil
}

// readHead reads the detached HEAD commit without running git.
func readHead(pluginDir string) (string, error) {
	data, err := fsx.ReadFileNoFollow(filepath.Join(pluginDir, ".git", "HEAD"), 256)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// download fetches rawURL into path, hashing while streaming, and removes
// nothing itself: the staging directory it writes into is discarded on failure.
func download(ctx context.Context, opts InstallOptions, rawURL, path, want string) error {
	if err := requireHTTPS(rawURL); err != nil {
		return err
	}
	client := *opts.HTTPClient
	parentCheck := opts.HTTPClient.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("%w: redirect to %s", ErrInsecureURL, req.URL.Redacted())
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("review: more than %d redirects downloading %s", maxRedirects, rawURL)
		}
		if parentCheck != nil {
			return parentCheck(req, via)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("review: download %s: %w", rawURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("review: download %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("review: download %s: %s", rawURL, resp.Status)
	}
	if resp.ContentLength > opts.MaxAssetBytes {
		return fmt.Errorf("review: download %s: %d bytes: %w", rawURL, resp.ContentLength, fsx.ErrTooLarge)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("review: create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, opts.MaxAssetBytes+1))
	if err != nil {
		return fmt.Errorf("review: download %s: %w", rawURL, err)
	}
	if n > opts.MaxAssetBytes {
		return fmt.Errorf("review: download %s: more than %d bytes: %w", rawURL, opts.MaxAssetBytes, fsx.ErrTooLarge)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("%w: %s has sha256 %s, pinned %s", ErrChecksum, filepath.Base(path), got, want)
	}
	// The plugin root is private (0700); the library itself carries the
	// usual shared library mode.
	if err := f.Chmod(0o644); err != nil {
		return fmt.Errorf("review: chmod %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("review: sync %s: %w", path, err)
	}
	return f.Close()
}

func requireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: %q", ErrInsecureURL, rawURL)
	}
	return nil
}

// fileMatches reports whether path is a regular file, reached without
// following a final link, with the given SHA256.
func fileMatches(path, want string) (bool, error) {
	data, err := fsx.ReadFileNoFollow(path, MaxAssetBytes)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == want, nil
}

// swapInto moves staged to dest. An existing dest is first moved to backup
// (inside the staging directory, which the caller removes) and moved back
// if the new installation cannot be put in place.
func swapInto(staged, dest, backup string) error {
	hadPrevious := false
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("review: move previous installation aside: %w", err)
		}
		hadPrevious = true
	}
	if err := os.Rename(staged, dest); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("review: install into %s: %w (restoring the previous installation also failed: %w)", dest, err, restoreErr)
			}
		}
		return fmt.Errorf("review: install into %s: %w", dest, err)
	}
	return nil
}
