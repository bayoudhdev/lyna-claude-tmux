package review

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/bayoudhdev/lyna-claude-tmux/internal/domain/review"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// Fake native files served by the test release server.
const (
	macLib   = "fake darwin arm64 diff library"
	linuxLib = "fake linux amd64 diff library"
	gompLib  = "fake linux amd64 openmp runtime"
)

// installFixture is a plugin repository, a TLS release server and a review
// directory, wired into install options.
type installFixture struct {
	repo    pluginRepo
	srv     *httptest.Server
	plain   *httptest.Server
	dir     string
	hits    atomic.Int64
	started chan struct{}
}

func pluginTree(version, marker string) map[string]string {
	return map[string]string{
		"VERSION":               version + "\n",
		"plugin/codediff.lua":   "-- " + marker + "\n",
		"lua/codediff/init.lua": "return {}\n",
	}
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	f := &installFixture{
		repo: newPluginRepo(t,
			pluginTree("4.0.6", "first"),
			pluginTree("4.0.6", "second"),
			pluginTree("9.9.9", "wrong version"),
		),
		dir:     filepath.Join(t.TempDir(), "data", "review"),
		started: make(chan struct{}),
	}
	f.plain = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(macLib))
	}))
	t.Cleanup(f.plain.Close)
	var once atomic.Bool
	mux := http.NewServeMux()
	serve := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			f.hits.Add(1)
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/dl/mac.dylib", serve(macLib))
	mux.HandleFunc("/dl/linux.so", serve(linuxLib))
	mux.HandleFunc("/dl/gomp.so.1", serve(gompLib))
	mux.HandleFunc("/dl/tampered.dylib", serve(macLib+" with a backdoor"))
	mux.HandleFunc("/dl/large-declared.dylib", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(make([]byte, 4096))
	})
	mux.HandleFunc("/dl/large-streamed.dylib", func(w http.ResponseWriter, _ *http.Request) {
		for range 8 {
			_, _ = w.Write(make([]byte, 1024))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/dl/insecure-redirect.dylib", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.plain.URL+"/mac.dylib", http.StatusFound)
	})
	mux.HandleFunc("/dl/secure-redirect.dylib", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dl/mac.dylib", http.StatusFound)
	})
	mux.HandleFunc("/dl/stall.dylib", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		if once.CompareAndSwap(false, true) {
			close(f.started)
		}
		<-r.Context().Done()
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// pin describes the fixture release; name overrides the darwin asset path.
func (f *installFixture) pin(commit int, darwinAsset string) domain.Pin {
	return domain.Pin{
		Repo:    f.repo.URL,
		Commit:  f.repo.Commits[commit],
		Version: "4.0.6",
		Assets: map[string][]domain.Asset{
			"darwin/arm64": {{Name: darwinAsset, File: "libvscode_diff_4.0.6.dylib", SHA256: sha(macLib)}},
			"linux/amd64": {
				{Name: "linux.so", File: "libvscode_diff_4.0.6.so", SHA256: sha(linuxLib)},
				{Name: "gomp.so.1", File: domain.LibgompFile, SHA256: sha(gompLib)},
			},
		},
	}
}

func (f *installFixture) options(commit int, darwinAsset string) InstallOptions {
	return InstallOptions{
		ReviewDir:     f.dir,
		Pin:           f.pin(commit, darwinAsset),
		GOOS:          "darwin",
		GOARCH:        "arm64",
		ReleaseURL:    f.srv.URL + "/dl",
		HTTPClient:    f.srv.Client(),
		Environ:       f.repo.env,
		MaxAssetBytes: 2048,
	}
}

func TestInstall(t *testing.T) {
	cases := []struct {
		name string
		// before runs first, for example a good installation to protect.
		before func(t *testing.T, f *installFixture)
		edit   func(t *testing.T, f *installFixture, o *InstallOptions)
		// ctx replaces the default test context.
		ctx     func(t *testing.T, f *installFixture) context.Context
		wantErr error
		errHas  string
		check   func(t *testing.T, f *installFixture, res InstallResult)
	}{
		{
			name: "fresh darwin install",
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				assertInstalled(t, f, 0, res, map[string]string{"libvscode_diff_4.0.6.dylib": macLib})
				if res.Reused {
					t.Error("fresh install reported Reused")
				}
			},
		},
		{
			name: "linux installs libgomp beside the library",
			edit: func(_ *testing.T, _ *installFixture, o *InstallOptions) { o.GOOS, o.GOARCH = "linux", "amd64" },
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				assertInstalled(t, f, 0, res, map[string]string{"libvscode_diff_4.0.6.so": linuxLib, "libgomp.so.1": gompLib})
			},
		},
		{
			name: "https redirect is followed",
			edit: func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(0, "secure-redirect.dylib") },
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				assertInstalled(t, f, 0, res, map[string]string{"libvscode_diff_4.0.6.dylib": macLib})
			},
		},
		{
			name:   "verified install is reused without network",
			before: installGood,
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				if !res.Reused {
					t.Error("second install did not reuse the verified one")
				}
				if got := f.hits.Load(); got != 1 {
					t.Errorf("release server hits = %d, want 1 (first install only)", got)
				}
				assertInstalled(t, f, 0, res, map[string]string{"libvscode_diff_4.0.6.dylib": macLib})
			},
		},
		{
			name:   "force reinstall replaces the previous commit",
			before: installGood,
			edit: func(_ *testing.T, f *installFixture, o *InstallOptions) {
				*o = f.options(1, "mac.dylib")
				o.Force = true
			},
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				assertInstalled(t, f, 1, res, map[string]string{"libvscode_diff_4.0.6.dylib": macLib})
				plugin, _ := os.ReadFile(filepath.Join(f.dir, domain.PluginDirName, "plugin", "codediff.lua"))
				if string(plugin) != "-- second\n" {
					t.Errorf("plugin file = %q, want the second commit's", plugin)
				}
			},
		},
		{
			name:   "a modified library is repaired without force",
			before: installGood,
			edit: func(t *testing.T, f *installFixture, _ *InstallOptions) {
				lib := filepath.Join(f.dir, domain.PluginDirName, "libvscode_diff_4.0.6.dylib")
				if err := os.WriteFile(lib, []byte("modified"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, f *installFixture, res InstallResult) {
				if res.Reused {
					t.Error("modified install was reused")
				}
				assertInstalled(t, f, 0, res, map[string]string{"libvscode_diff_4.0.6.dylib": macLib})
			},
		},
		{
			name:    "commit missing from the repository",
			edit:    func(_ *testing.T, _ *installFixture, o *InstallOptions) { o.Pin.Commit = strings.Repeat("ab", 20) },
			errHas:  "git fetch failed",
			wantErr: nil,
		},
		{
			name: "checked out commit differs from the pin",
			edit: func(t *testing.T, _ *installFixture, o *InstallOptions) {
				self, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				o.Git = self
				o.Environ = append(slices.Clone(o.Environ), envFakeGit+"=1", envRealGit+"="+requireGit(t))
			},
			wantErr: ErrVerify,
			errHas:  wrongHead,
		},
		{
			name:    "wrong VERSION",
			edit:    func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(2, "mac.dylib") },
			wantErr: ErrVerify,
			errHas:  `VERSION is "9.9.9"`,
		},
		{
			name:   "checksum mismatch keeps the previous installation",
			before: installGood,
			edit: func(_ *testing.T, f *installFixture, o *InstallOptions) {
				*o = f.options(1, "tampered.dylib")
				o.Force = true
			},
			wantErr: ErrChecksum,
		},
		{
			name:    "declared oversize body",
			edit:    func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(0, "large-declared.dylib") },
			wantErr: fsx.ErrTooLarge,
		},
		{
			name:    "streamed oversize body",
			edit:    func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(0, "large-streamed.dylib") },
			wantErr: fsx.ErrTooLarge,
		},
		{
			name:   "missing asset",
			edit:   func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(0, "absent.dylib") },
			errHas: "404 Not Found",
		},
		{
			name:    "plain http release URL",
			edit:    func(_ *testing.T, f *installFixture, o *InstallOptions) { o.ReleaseURL = f.plain.URL },
			wantErr: ErrInsecureURL,
		},
		{
			name:    "redirect to http",
			edit:    func(_ *testing.T, f *installFixture, o *InstallOptions) { *o = f.options(0, "insecure-redirect.dylib") },
			wantErr: ErrInsecureURL,
		},
		{
			name:    "unsupported platform",
			edit:    func(_ *testing.T, _ *installFixture, o *InstallOptions) { o.GOOS, o.GOARCH = "windows", "amd64" },
			wantErr: domain.ErrUnsupportedPlatform,
			check: func(t *testing.T, f *installFixture, _ InstallResult) {
				if _, err := os.Lstat(f.dir); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("review directory created for an unsupported platform: %v", err)
				}
			},
		},
		{
			name:    "git missing",
			edit:    func(t *testing.T, _ *installFixture, o *InstallOptions) { o.Git = filepath.Join(t.TempDir(), "no-git") },
			wantErr: ErrGitMissing,
		},
		{
			name: "destination is a symlink",
			edit: func(t *testing.T, f *installFixture, _ *InstallOptions) {
				mustMkdir(t, f.dir)
				if err := os.Symlink(t.TempDir(), filepath.Join(f.dir, domain.PluginDirName)); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name: "review directory is a symlink",
			edit: func(t *testing.T, f *installFixture, _ *InstallOptions) {
				mustMkdir(t, filepath.Dir(f.dir))
				if err := os.Symlink(t.TempDir(), f.dir); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name: "destination is a file",
			edit: func(t *testing.T, f *installFixture, _ *InstallOptions) {
				mustMkdir(t, f.dir)
				if err := os.WriteFile(filepath.Join(f.dir, domain.PluginDirName), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrNotDir,
		},
		{
			name:   "cancel during download",
			before: installGood,
			edit: func(_ *testing.T, f *installFixture, o *InstallOptions) {
				*o = f.options(1, "stall.dylib")
				o.Force = true
			},
			ctx: func(t *testing.T, f *installFixture) context.Context {
				ctx, cancel := context.WithCancel(testContext(t, 30*time.Second))
				go func() {
					<-f.started
					cancel()
				}()
				return ctx
			},
			wantErr: context.Canceled,
		},
		{
			name: "another install holds the lock",
			edit: func(t *testing.T, f *installFixture, _ *InstallOptions) {
				mustMkdir(t, f.dir)
				unlock, err := lockDir(f.dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(unlock)
			},
			wantErr: ErrBusy,
		},
		{
			name:    "invalid pin",
			edit:    func(_ *testing.T, _ *installFixture, o *InstallOptions) { o.Pin.Commit = "main" },
			wantErr: domain.ErrInvalidPin,
		},
		{
			name:   "relative review directory",
			edit:   func(_ *testing.T, _ *installFixture, o *InstallOptions) { o.ReviewDir = "data/review" },
			errHas: "is not absolute",
		},
		{
			name: "review directory Neovim cannot load from",
			edit: func(t *testing.T, _ *installFixture, o *InstallOptions) {
				o.ReviewDir = filepath.Join(t.TempDir(), "it's", "review")
			},
			errHas: "which Neovim cannot load plugins from",
			check: func(t *testing.T, f *installFixture, _ InstallResult) {
				if got := f.hits.Load(); got != 0 {
					t.Errorf("release server hits = %d, want none", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t)
			if tc.before != nil {
				tc.before(t, f)
			}
			dest := filepath.Join(f.dir, domain.PluginDirName)
			previous := map[string]string{}
			if tc.before != nil {
				previous = snapshot(t, dest)
			}
			opts := f.options(0, "mac.dylib")
			if tc.edit != nil {
				tc.edit(t, f, &opts)
			}
			ctx := testContext(t, 60*time.Second)
			if tc.ctx != nil {
				ctx = tc.ctx(t, f)
			}
			res, err := Install(ctx, opts)
			failed := tc.wantErr != nil || tc.errHas != ""
			switch {
			case !failed && err != nil:
				t.Fatalf("Install() error = %v", err)
			case failed && err == nil:
				t.Fatalf("Install() succeeded, want error %v %q", tc.wantErr, tc.errHas)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("Install() error = %v, want %v", err, tc.wantErr)
			case tc.errHas != "" && !strings.Contains(err.Error(), tc.errHas):
				t.Fatalf("Install() error = %q, want it to contain %q", err, tc.errHas)
			}
			if left := stagingLeftovers(t, f.dir); len(left) > 0 {
				t.Errorf("staging directories left behind: %v", left)
			}
			if failed && tc.before != nil {
				if after := snapshot(t, dest); !maps.Equal(previous, after) {
					t.Errorf("failed install changed the previous installation")
				}
			}
			if failed && tc.before == nil && filepath.IsAbs(opts.ReviewDir) {
				if info, err := os.Lstat(dest); err == nil && info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
					t.Errorf("failed install left a plugin directory")
				}
			}
			if tc.check != nil {
				tc.check(t, f, res)
			}
		})
	}
}

func installGood(t *testing.T, f *installFixture) {
	t.Helper()
	if _, err := Install(testContext(t, 60*time.Second), f.options(0, "mac.dylib")); err != nil {
		t.Fatalf("good install: %v", err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// assertInstalled checks the installation on disk against a fixture commit.
func assertInstalled(t *testing.T, f *installFixture, commit int, res InstallResult, native map[string]string) {
	t.Helper()
	dest := filepath.Join(f.dir, domain.PluginDirName)
	if res.PluginDir != dest || res.Commit != f.repo.Commits[commit] || res.Version != "4.0.6" {
		t.Errorf("result = %+v, want dir %s commit %s", res, dest, f.repo.Commits[commit])
	}
	if !slices.Equal(slices.Sorted(slices.Values(res.Files)), slices.Sorted(maps.Keys(native))) {
		t.Errorf("result files = %v, want %v", res.Files, slices.Sorted(maps.Keys(native)))
	}
	head, err := readHead(dest)
	if err != nil || head != f.repo.Commits[commit] {
		t.Errorf("HEAD = %q, %v; want %s", head, err, f.repo.Commits[commit])
	}
	for name, want := range native {
		path := filepath.Join(dest, name)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, %v; want 0644", name, info.Mode().Perm(), err)
		}
	}
	for _, dir := range []string{f.dir, dest} {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, %v; want 0700", dir, info.Mode().Perm(), err)
		}
	}
	for _, hook := range []string{"hooks"} {
		if _, err := os.Stat(filepath.Join(dest, ".git", hook)); err == nil {
			t.Errorf(".git/%s exists; templates must not be copied", hook)
		}
	}
}

func TestGitEnv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "adds prompt guard", in: []string{"PATH=/bin"}, want: []string{"PATH=/bin", "GIT_TERMINAL_PROMPT=0"}},
		{
			name: "drops repository redirection",
			in:   []string{"GIT_DIR=/x", "GIT_WORK_TREE=/y", "GIT_INDEX_FILE=/z", "GIT_OBJECT_DIRECTORY=/o", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/a", "GIT_COMMON_DIR=/c", "GIT_NAMESPACE=n", "GIT_PREFIX=p", "GIT_QUARANTINE_PATH=/q", "HOME=/h"},
			want: []string{"HOME=/h", "GIT_TERMINAL_PROMPT=0"},
		},
		{name: "replaces prompt setting", in: []string{"GIT_TERMINAL_PROMPT=1", "GIT_CONFIG_GLOBAL=/dev/null"}, want: []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitEnv(tc.in); !slices.Equal(got, tc.want) {
				t.Fatalf("gitEnv() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitMessage(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "empty uses the exit error", stderr: "", want: "exit status 128"},
		{name: "joins lines", stderr: "fatal: one\nfatal: two\n", want: "fatal: one; fatal: two"},
		{name: "strips escapes", stderr: "fatal: \x1b[31mred\x1b[0m", want: "fatal: red"},
		{name: "keeps the tail", stderr: strings.Repeat("a", 600) + "END", want: "..." + strings.Repeat("a", 509) + "END"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitMessage(tc.stderr, errors.New("exit status 128")); got != tc.want {
				t.Fatalf("gitMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSwapIntoRestoresPrevious(t *testing.T) {
	cases := []struct {
		name        string
		previous    bool
		wantContent string
	}{
		{name: "previous installation restored", previous: true, wantContent: "old"},
		{name: "no previous installation", previous: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "codediff.nvim")
			if tc.previous {
				writeTree(t, dest, map[string]string{"VERSION": "old"})
			}
			// A staged directory that does not exist makes the second rename fail.
			err := swapInto(filepath.Join(root, "missing"), dest, filepath.Join(root, "previous"))
			if err == nil {
				t.Fatal("swapInto() succeeded with a missing staged directory")
			}
			data, readErr := os.ReadFile(filepath.Join(dest, "VERSION"))
			if tc.previous && (readErr != nil || string(data) != tc.wantContent) {
				t.Fatalf("previous installation = %q, %v; want restored", data, readErr)
			}
			if !tc.previous && !errors.Is(readErr, fs.ErrNotExist) {
				t.Fatalf("destination appeared: %v", readErr)
			}
		})
	}
}
