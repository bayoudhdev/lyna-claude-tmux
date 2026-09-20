package scripts_test

// The installer is tested against a local HTTPS release server. The test CA
// reaches the downloaders through their standard trust settings: curl reads
// CURL_CA_BUNDLE and GNU wget reads ca_certificate from the file named by
// WGETRC. The script therefore has no test-only switch that could weaken TLS
// verification, and the https-only rules run unchanged.

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

const latestVersion = "v1.2.3"

// release is what the fake server publishes for one version.
type release struct {
	assets map[string][]byte
	// checksums overrides the generated checksums.txt when non-nil.
	checksums []byte
}

type releaseServer struct {
	tls, plain *httptest.Server
	caFile     string

	mu       sync.Mutex
	releases map[string]*release
	latest   string
	// assetRedirect is the base the download URLs redirect to; empty serves
	// the assets from the TLS server through a relative redirect.
	assetRedirect string
	requests      []string
	plainRequests int
}

func binaryFor(version string) []byte {
	return []byte("#!/bin/sh\necho lmux " + version + "\n")
}

func assetName(version, goos, arch string) string {
	return "lyna-tmux_" + strings.TrimPrefix(version, "v") + "_" + goos + "_" + arch + ".tar.gz"
}

type tarEntry struct {
	name, body, link string
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			hdr = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.link}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newRelease(t *testing.T, version string) *release {
	t.Helper()
	r := &release{assets: map[string][]byte{}}
	for _, goos := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			r.assets[assetName(version, goos, arch)] = tarGz(t,
				tarEntry{name: "LICENSE", body: "MIT"},
				tarEntry{name: "lmux", body: string(binaryFor(version))},
			)
		}
	}
	return r
}

func (r *release) checksumFile() []byte {
	if r.checksums != nil {
		return r.checksums
	}
	names := make([]string, 0, len(r.assets))
	for name := range r.assets {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		sum := sha256.Sum256(r.assets[name])
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return []byte(b.String())
}

func newReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	s := &releaseServer{
		releases: map[string]*release{latestVersion: newRelease(t, latestVersion), "v1.0.0": newRelease(t, "v1.0.0")},
		latest:   latestVersion,
	}
	s.tls = httptest.NewTLSServer(http.HandlerFunc(s.serveTLS))
	t.Cleanup(s.tls.Close)
	s.plain = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.plainRequests++
		s.mu.Unlock()
		http.Error(w, "plain HTTP must never be used", http.StatusTeapot)
	}))
	t.Cleanup(s.plain.Close)

	s.caFile = filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.tls.Certificate().Raw})
	if err := os.WriteFile(s.caFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *releaseServer) serveTLS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case strings.HasPrefix(r.URL.Path, "/renamed/"):
		// A repository that was renamed answers every path under its old name
		// with a redirect to the new one, which for the latest release comes
		// before the tag redirect below.
		http.Redirect(w, r, s.tls.URL+strings.TrimPrefix(r.URL.Path, "/renamed"), http.StatusMovedPermanently)
	case r.URL.Path == "/releases/latest":
		if s.latest == "" {
			http.NotFound(w, r)
			return
		}
		// GitHub answers with an absolute URL of the tag page.
		http.Redirect(w, r, s.tls.URL+"/releases/tag/"+s.latest, http.StatusFound)
	case len(parts) == 4 && parts[0] == "releases" && parts[1] == "download":
		rel, ok := s.releases[parts[2]]
		switch {
		case !ok:
			http.NotFound(w, r)
		case parts[3] == "checksums.txt":
			_, _ = w.Write(rel.checksumFile())
		case rel.assets[parts[3]] != nil:
			// Release assets live on another host; the redirect is relative
			// here so both absolute and relative Location handling run.
			target := "/assets/" + parts[2] + "/" + parts[3]
			if s.assetRedirect != "" {
				target = s.assetRedirect + target
			}
			http.Redirect(w, r, target, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	case len(parts) == 3 && parts[0] == "assets":
		if rel, ok := s.releases[parts[1]]; ok && rel.assets[parts[2]] != nil {
			_, _ = w.Write(rel.assets[parts[2]])
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *releaseServer) seen() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests), s.plainRequests
}

// toolbox builds a PATH directory with only the tools the installer may use,
// a uname stub reporting the platform under test, and the chosen downloader
// and checksum tools.
func toolbox(t *testing.T, goos, arch, downloader string, checksum bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "toolbox")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{"awk", "cat", "chmod", "cp", "grep", "gzip", "ln", "mkdir", "mktemp", "mv", "rm", "sed", "tail", "tar", "tr"}
	if downloader != "" {
		tools = append(tools, downloader)
	}
	if checksum {
		tools = append(tools, "sha256sum", "shasum")
	}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			if tool == "gzip" || tool == "sha256sum" || tool == "shasum" {
				continue // tar may decompress itself; one checksum tool is enough
			}
			t.Skipf("%s is not installed", tool)
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if checksum && !fileExists(filepath.Join(dir, "sha256sum")) && !fileExists(filepath.Join(dir, "shasum")) {
		t.Skip("neither sha256sum nor shasum is installed")
	}
	uname := fmt.Sprintf("#!/bin/sh\ncase $1 in\n-s) echo %s ;;\n-m) echo %s ;;\n*) exit 1 ;;\nesac\n", goos, arch)
	writeExecutable(t, filepath.Join(dir, "uname"), uname)
	return dir
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// installShells are the shells the POSIX installer must run under.
func installShells(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh targets Linux and macOS")
	}
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}
	return shells
}

func scriptPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

type installCase struct {
	name       string
	downloader string // curl (default) or wget
	noChecksum bool
	goos, arch string // uname -s and -m; Linux and x86_64 by default
	// args replace the default arguments (--base-url for the test server).
	args  func(s *releaseServer, home string) []string
	setup func(t *testing.T, s *releaseServer, home string)
	// stubs replaces tools of the toolbox once it is built.
	stubs func(t *testing.T, tools, home string)
	// onPath adds the default prefix to PATH.
	onPath bool

	wantExit     int
	wantOut      []string
	wantErr      string
	wantVersion  string // installed binary version; empty means nothing installed
	wantDir      func(home string) string
	wantRequests func(s *releaseServer) []string
	// noAlias skips the check that the link under the name of 1.0.0 is there,
	// for a case where something else already holds that name.
	noAlias bool
	// check runs extra assertions once the installer has exited.
	check func(t *testing.T, home string)
}

// replaceTool overwrites one tool of a toolbox with a shell stub.
func replaceTool(t *testing.T, tools, name, script string) {
	t.Helper()
	path := filepath.Join(tools, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	writeExecutable(t, path, script)
}

// realTool is the absolute path of a system tool, for a stub to fall back on.
func realTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return path
}

// stubMktemp forces the staging file name the installer gets, so a test can
// plant a symlink at it. Directory requests go to the real mktemp.
func stubMktemp(t *testing.T, tools, staged string) {
	t.Helper()
	replaceTool(t, tools, "mktemp", "#!/bin/sh\ncase $1 in\n-d) exec "+realTool(t, "mktemp")+" \"$@\" ;;\nesac\nprintf '%s\\n' '"+staged+"'\n")
}

// recordChmod appends every path the installer chmods to log, which is the
// staging file it created.
func recordChmod(t *testing.T, tools, log string) {
	t.Helper()
	replaceTool(t, tools, "chmod", "#!/bin/sh\nprintf '%s\\n' \"$2\" >> '"+log+"'\nexec "+realTool(t, "chmod")+" \"$@\"\n")
}

func defaultArgs(s *releaseServer, _ string) []string {
	return []string{"--base-url", s.tls.URL + "/releases"}
}

func latestRequests(asset string) func(*releaseServer) []string {
	return func(*releaseServer) []string {
		return []string{
			"GET /releases/latest",
			"GET /releases/download/v1.2.3/checksums.txt",
			"GET /releases/download/v1.2.3/" + asset,
			"GET /assets/v1.2.3/" + asset,
		}
	}
}

func noRequests(*releaseServer) []string { return nil }

func localBin(home string) string { return filepath.Join(home, ".local", "bin") }

func TestInstallScript(t *testing.T) {
	linuxAmd64 := assetName(latestVersion, "linux", "amd64")
	cases := []installCase{
		{
			name:         "latest release with curl",
			wantOut:      []string{"downloading lyna-tmux v1.2.3 for linux/amd64", "installed lyna-tmux v1.2.3 to ", "is not on your PATH", `fish_add_path "`},
			wantVersion:  latestVersion,
			wantRequests: latestRequests(linuxAmd64),
		},
		{
			name:         "latest release with wget",
			downloader:   "wget",
			wantOut:      []string{"installed lyna-tmux v1.2.3 to "},
			wantVersion:  latestVersion,
			wantRequests: latestRequests(linuxAmd64),
		},
		{
			name: "version pin skips the latest lookup",
			args: func(s *releaseServer, _ string) []string {
				return []string{"--version", "v1.0.0", "--base-url", s.tls.URL + "/releases/"}
			},
			wantVersion: "v1.0.0",
			wantRequests: func(*releaseServer) []string {
				a := assetName("v1.0.0", "linux", "amd64")
				return []string{"GET /releases/download/v1.0.0/checksums.txt", "GET /releases/download/v1.0.0/" + a, "GET /assets/v1.0.0/" + a}
			},
		},
		{
			name:       "version pin without the v prefix, with wget",
			downloader: "wget",
			args: func(s *releaseServer, _ string) []string {
				return []string{"--version=1.0.0", "--base-url=" + s.tls.URL + "/releases"}
			},
			wantVersion: "v1.0.0",
		},
		{
			name:         "macOS on Apple silicon",
			goos:         "Darwin",
			arch:         "arm64",
			wantVersion:  latestVersion,
			wantRequests: latestRequests(assetName(latestVersion, "darwin", "arm64")),
		},
		{
			name:         "Linux aarch64",
			arch:         "aarch64",
			wantVersion:  latestVersion,
			wantRequests: latestRequests(assetName(latestVersion, "linux", "arm64")),
		},
		{
			name: "checksum mismatch aborts and installs nothing",
			setup: func(_ *testing.T, s *releaseServer, _ string) {
				sum := sha256.Sum256([]byte("something else"))
				s.releases[latestVersion].checksums = []byte(hex.EncodeToString(sum[:]) + "  " + assetName(latestVersion, "linux", "amd64") + "\n")
			},
			wantExit:     1,
			wantErr:      "checksum mismatch for " + linuxAmd64,
			wantRequests: latestRequests(linuxAmd64),
		},
		{
			name:       "checksum mismatch with wget aborts",
			downloader: "wget",
			setup: func(_ *testing.T, s *releaseServer, _ string) {
				s.releases[latestVersion].checksums = []byte(strings.Repeat("0", 64) + "  " + assetName(latestVersion, "linux", "amd64") + "\n")
			},
			wantExit: 1,
			wantErr:  "checksum mismatch",
		},
		{
			name: "missing checksum entry aborts",
			setup: func(_ *testing.T, s *releaseServer, _ string) {
				s.releases[latestVersion].checksums = []byte(strings.Repeat("a", 64) + "  lyna-tmux_1.2.3_linux_amd64.tar.gz.sbom.json\n")
			},
			wantExit: 1,
			wantErr:  "has no entry for " + linuxAmd64,
		},
		{
			name: "malformed checksum entry aborts",
			setup: func(_ *testing.T, s *releaseServer, _ string) {
				s.releases[latestVersion].checksums = []byte("not-a-hash  " + assetName(latestVersion, "linux", "amd64") + "\n")
			},
			wantExit: 1,
			wantErr:  "malformed entry",
		},
		{
			name:         "http base URL refused",
			args:         func(s *releaseServer, _ string) []string { return []string{"--base-url", s.plain.URL + "/releases"} },
			wantExit:     1,
			wantErr:      "refusing non-https URL: http://",
			wantRequests: noRequests,
		},
		{
			name:     "redirect to http refused by curl",
			setup:    func(_ *testing.T, s *releaseServer, _ string) { s.assetRedirect = s.plain.URL },
			wantExit: 1,
			wantErr:  "download failed",
		},
		{
			name:       "redirect to http refused with wget",
			downloader: "wget",
			setup:      func(_ *testing.T, s *releaseServer, _ string) { s.assetRedirect = s.plain.URL },
			wantExit:   1,
			wantErr:    "refusing non-https URL: http://",
		},
		{
			name:         "unsupported architecture",
			arch:         "riscv64",
			wantExit:     1,
			wantErr:      "unsupported architecture: riscv64",
			wantRequests: noRequests,
		},
		{
			name:         "unsupported operating system",
			goos:         "FreeBSD",
			wantExit:     1,
			wantErr:      "unsupported operating system: FreeBSD",
			wantRequests: noRequests,
		},
		{
			name: "prefix with spaces",
			args: func(s *releaseServer, home string) []string {
				return []string{"--base-url", s.tls.URL + "/releases", "--prefix", filepath.Join(home, "My Tools", "bin")}
			},
			wantVersion: latestVersion,
			wantDir:     func(home string) string { return filepath.Join(home, "My Tools", "bin") },
			wantOut:     []string{`export PATH="` + "HOME/My Tools/bin" + `:$PATH"`},
		},
		{
			name:        "prefix already on PATH prints no guidance",
			onPath:      true,
			wantVersion: latestVersion,
		},
		{
			name: "upgrade replaces an existing binary",
			setup: func(t *testing.T, _ *releaseServer, home string) {
				if err := os.MkdirAll(localBin(home), 0o755); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, filepath.Join(localBin(home), "lmux"), "old")
			},
			wantVersion: latestVersion,
		},
		{
			// A 1.0.0 install left a binary under the old name. It is not a
			// link to the new one, so it is left alone and named to the user
			// rather than replaced by a link to a different binary.
			name: "a binary under the name of 1.0.0 is kept",
			setup: func(t *testing.T, _ *releaseServer, home string) {
				if err := os.MkdirAll(localBin(home), 0o755); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, filepath.Join(localBin(home), "lyna-tmux"), "old")
			},
			wantVersion: latestVersion,
			noAlias:     true,
			wantOut:     []string{"is not a link to lmux"},
			check: func(t *testing.T, home string) {
				path := filepath.Join(localBin(home), "lyna-tmux")
				if got := mustRead(t, path); string(got) != "old" {
					t.Fatalf("%s = %q, want the binary of 1.0.0 untouched", path, got)
				}
			},
		},
		{
			name: "invalid version refused",
			args: func(s *releaseServer, _ string) []string {
				return []string{"--version", "v1.2.3;rm -rf", "--base-url", s.tls.URL + "/releases"}
			},
			wantExit:     1,
			wantErr:      "invalid version v1.2.3;rm -rf",
			wantRequests: noRequests,
		},
		{
			// A rename puts a redirect of its own before the tag, which the
			// installer follows: an address printed before the repository was
			// renamed keeps working.
			name: "a renamed repository still resolves the latest release",
			args: func(s *releaseServer, _ string) []string {
				return []string{"--base-url", s.tls.URL + "/renamed/releases"}
			},
			wantOut:     []string{"installed lyna-tmux " + latestVersion + " to "},
			wantVersion: latestVersion,
			wantRequests: func(*releaseServer) []string {
				asset := assetName(latestVersion, "linux", "amd64")
				return []string{
					"GET /renamed/releases/latest",
					"GET /releases/latest",
					"GET /renamed/releases/download/v1.2.3/checksums.txt",
					"GET /releases/download/v1.2.3/checksums.txt",
					"GET /renamed/releases/download/v1.2.3/" + asset,
					"GET /releases/download/v1.2.3/" + asset,
					"GET /assets/v1.2.3/" + asset,
				}
			},
		},
		{
			// A rename puts a redirect of its own before the tag, which the
			// installer follows: an address printed before the repository was
			// renamed keeps working.
			name:       "a renamed repository resolves the latest release with wget too",
			downloader: "wget",
			args: func(s *releaseServer, _ string) []string {
				return []string{"--base-url", s.tls.URL + "/renamed/releases"}
			},
			wantOut:     []string{"installed lyna-tmux " + latestVersion + " to "},
			wantVersion: latestVersion,
			wantRequests: func(*releaseServer) []string {
				asset := assetName(latestVersion, "linux", "amd64")
				return []string{
					"GET /renamed/releases/latest",
					"GET /releases/latest",
					"GET /renamed/releases/download/v1.2.3/checksums.txt",
					"GET /releases/download/v1.2.3/checksums.txt",
					"GET /renamed/releases/download/v1.2.3/" + asset,
					"GET /releases/download/v1.2.3/" + asset,
					"GET /assets/v1.2.3/" + asset,
				}
			},
		},
		{
			name:         "no published release",
			setup:        func(_ *testing.T, s *releaseServer, _ string) { s.latest = "" },
			wantExit:     1,
			wantErr:      "cannot determine the latest release",
			wantRequests: func(*releaseServer) []string { return []string{"GET /releases/latest"} },
		},
		{
			name: "archive whose binary is a symlink is refused",
			setup: func(t *testing.T, s *releaseServer, _ string) {
				rel := s.releases[latestVersion]
				rel.assets[assetName(latestVersion, "linux", "amd64")] = tarGz(t, tarEntry{name: "lmux", link: "/bin/sh"})
			},
			wantExit: 1,
			wantErr:  "is not a regular file",
		},
		{
			name: "archive without the binary is refused",
			setup: func(t *testing.T, s *releaseServer, _ string) {
				rel := s.releases[latestVersion]
				rel.assets[assetName(latestVersion, "linux", "amd64")] = tarGz(t, tarEntry{name: "README.md", body: "x"})
			},
			wantExit: 1,
			wantErr:  "does not contain lmux",
		},
		{
			name:         "no checksum tool",
			noChecksum:   true,
			wantExit:     1,
			wantErr:      "sha256sum or shasum is required",
			wantRequests: noRequests,
		},
		{
			name:         "no downloader",
			downloader:   "none",
			wantExit:     1,
			wantErr:      "curl or wget is required",
			wantRequests: noRequests,
		},
		{
			name: "prefix that cannot be written",
			setup: func(t *testing.T, _ *releaseServer, home string) {
				if os.Getuid() == 0 {
					t.Skip("root ignores directory permissions")
				}
				if err := os.MkdirAll(localBin(home), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(localBin(home), 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(localBin(home), 0o755) })
			},
			wantExit: 1,
			wantErr:  "choose a directory you own with --prefix",
		},
		{
			// A prefix another local account can write to is the only way to
			// reach this: the staging name is unpredictable, so the symlink is
			// planted through a stubbed mktemp.
			name: "planted symlink at the staging path is refused",
			setup: func(t *testing.T, _ *releaseServer, home string) {
				if err := os.MkdirAll(localBin(home), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "victim"), []byte("victim"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, "victim"), filepath.Join(localBin(home), plantedStaging)); err != nil {
					t.Fatal(err)
				}
			},
			stubs: func(t *testing.T, tools, home string) {
				stubMktemp(t, tools, filepath.Join(localBin(home), plantedStaging))
			},
			wantExit: 1,
			wantErr:  "refusing to stage",
			check: func(t *testing.T, home string) {
				victim := filepath.Join(home, "victim")
				if got := mustRead(t, victim); string(got) != "victim" {
					t.Fatalf("the installer wrote through the planted symlink: %q", got)
				}
				info, err := os.Stat(victim)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("the installer chmodded the symlink target to %v", info.Mode().Perm())
				}
			},
		},
		{
			name:         "unknown argument",
			args:         func(*releaseServer, string) []string { return []string{"--sudo"} },
			wantExit:     2,
			wantErr:      "unknown argument: --sudo",
			wantRequests: noRequests,
		},
		{
			name:         "missing option value",
			args:         func(*releaseServer, string) []string { return []string{"--prefix"} },
			wantExit:     2,
			wantErr:      "--prefix needs a value",
			wantRequests: noRequests,
		},
		{
			name:         "help",
			args:         func(*releaseServer, string) []string { return []string{"--help"} },
			wantOut:      []string{"Usage: install.sh [--version vX.Y.Z] [--prefix DIR] [--base-url URL]"},
			wantRequests: noRequests,
		},
	}
	script := scriptPath(t, "install.sh")
	for _, shell := range installShells(t) {
		for _, tc := range cases {
			t.Run(filepath.Base(shell)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				runInstallCase(t, shell, script, tc)
			})
		}
	}
}

func runInstallCase(t *testing.T, shell, script string, tc installCase) {
	downloader := cmp.Or(tc.downloader, "curl")
	if downloader == "none" {
		downloader = ""
	} else if _, err := exec.LookPath(downloader); err != nil {
		t.Skipf("%s is not installed", downloader)
	}
	s := newReleaseServer(t)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if tc.setup != nil {
		tc.setup(t, s, home)
	}
	tools := toolbox(t, cmp.Or(tc.goos, "Linux"), cmp.Or(tc.arch, "x86_64"), downloader, !tc.noChecksum)
	if tc.stubs != nil {
		tc.stubs(t, tools, home)
	}
	wgetrc := filepath.Join(t.TempDir(), "wgetrc")
	if err := os.WriteFile(wgetrc, []byte("ca_certificate = "+s.caFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := tools
	if tc.onPath {
		path = localBin(home) + ":" + tools
	}
	args := defaultArgs(s, home)
	if tc.args != nil {
		args = tc.args(s, home)
	}

	cmd := exec.Command(shell, append([]string{script}, args...)...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + path, "HOME=" + home, "CURL_CA_BUNDLE=" + s.caFile, "WGETRC=" + wgetrc, "LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	out := strings.ReplaceAll(stdout.String(), home, "HOME")
	if exit != tc.wantExit || !strings.Contains(stderr.String(), tc.wantErr) {
		t.Fatalf("exit %d (want %d)\nstdout:\n%s\nstderr:\n%s\nwant stderr containing %q", exit, tc.wantExit, stdout.String(), stderr.String(), tc.wantErr)
	}
	for _, want := range tc.wantOut {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout does not contain %q:\n%s", want, out)
		}
	}
	if tc.onPath && strings.Contains(out, "not on your PATH") {
		t.Fatalf("PATH guidance printed although the prefix is on PATH:\n%s", out)
	}
	if tc.check != nil {
		tc.check(t, home)
	}

	requests, plain := s.seen()
	if plain != 0 {
		t.Fatalf("%d plain HTTP requests", plain)
	}
	if tc.wantRequests != nil {
		if want := tc.wantRequests(s); !slices.Equal(requests, want) {
			t.Fatalf("requests\n got  %q\n want %q", requests, want)
		}
	}
	dir := localBin(home)
	if tc.wantDir != nil {
		dir = tc.wantDir(home)
	}
	installed := filepath.Join(dir, "lmux")
	if tc.wantVersion == "" {
		if got, readErr := os.ReadFile(installed); readErr == nil && string(got) != "old" {
			t.Fatalf("a failed install left %s = %q", installed, got)
		}
		assertNoStaging(t, dir)
		return
	}
	info, err := os.Lstat(installed)
	if err != nil {
		t.Fatalf("binary not installed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode %v, want a regular 0755 file", info.Mode())
	}
	if got := mustRead(t, installed); !bytes.Equal(got, binaryFor(tc.wantVersion)) {
		t.Fatalf("installed %q, want the %s binary", got, tc.wantVersion)
	}
	if !tc.noAlias {
		// The name the command had in 1.0.0 stays as a link beside it, so a
		// script or a shell alias written then still runs.
		alias := filepath.Join(dir, "lyna-tmux")
		target, linkErr := os.Readlink(alias)
		if linkErr != nil {
			t.Fatalf("no link under the name of 1.0.0: %v", linkErr)
		}
		if target != "lmux" {
			t.Fatalf("link to %q, want a relative link to lmux", target)
		}
	}
	assertNoStaging(t, dir)
}

func assertNoStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lmux.install.") {
			t.Fatalf("staging file left behind: %s", e.Name())
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// plantedStaging is the staging file name the stubbed mktemp hands the
// installer in the symlink case.
const plantedStaging = ".lmux.install.planted"

// TestInstallScriptStagingPathIsUnpredictable installs twice into the same
// prefix and compares the staging names the installer created. A name an
// attacker can compute (the process id, a fixed suffix) is a symlink target
// they can plant before cp runs.
func TestInstallScriptStagingPathIsUnpredictable(t *testing.T) {
	log := filepath.Join(t.TempDir(), "chmod.log")
	prefix := filepath.Join(t.TempDir(), "bin")
	tc := installCase{
		args: func(s *releaseServer, _ string) []string {
			return []string{"--base-url", s.tls.URL + "/releases", "--prefix", prefix}
		},
		stubs:       func(t *testing.T, tools, _ string) { recordChmod(t, tools, log) },
		wantVersion: latestVersion,
		wantDir:     func(string) string { return prefix },
	}
	for _, name := range []string{"first install", "second install"} {
		t.Run(name, func(t *testing.T) { runInstallCase(t, "/bin/sh", scriptPath(t, "install.sh"), tc) })
	}

	staged := strings.Fields(string(mustRead(t, log)))
	if len(staged) != 2 {
		t.Fatalf("recorded staging paths %q, want one per install", staged)
	}
	want := regexp.MustCompile(`^` + regexp.QuoteMeta(filepath.Join(prefix, ".lmux.install.")) + `[A-Za-z0-9._-]{6,}$`)
	for _, path := range staged {
		if !want.MatchString(path) {
			t.Fatalf("staging path %q is not a random name next to the target", path)
		}
	}
	if staged[0] == staged[1] {
		t.Fatalf("both installs staged at %q, so the name is predictable", staged[0])
	}
}

// TestInstallScriptStagingContract pins how the staging file is created:
// a random name from mktemp (which creates it exclusively) and a refusal to
// write through anything that is not a regular file.
func TestInstallScriptStagingContract(t *testing.T) {
	src := string(mustRead(t, scriptPath(t, "install.sh")))
	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "the name comes from mktemp", pattern: `staged=\$\(mktemp `, want: true},
		{name: "the template is random", pattern: `\.lmux\.install\.X{6,}`, want: true},
		{name: "the process id is not the name", pattern: `staged=[^\n]*\$\$`, want: false},
		{name: "a symlink is refused", pattern: `\[ -L "\$staged" \]`, want: true},
		{name: "a non-regular file is refused", pattern: `\[ ! -f "\$staged" \]`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := regexp.MustCompile(tc.pattern).MatchString(src); got != tc.want {
				t.Fatalf("scripts/install.sh matches %q = %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestInstallScriptNeverEscalates checks every command line of the installer
// (comments aside) for privilege escalation.
func TestInstallScriptNeverEscalates(t *testing.T) {
	for i, line := range strings.Split(string(mustRead(t, scriptPath(t, "install.sh"))), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		for _, word := range strings.FieldsFunc(code, func(r rune) bool { return r < 'a' || r > 'z' }) {
			if word == "sudo" || word == "doas" || word == "su" {
				t.Fatalf("install.sh line %d runs %s: %s", i+1, word, line)
			}
		}
	}
}
