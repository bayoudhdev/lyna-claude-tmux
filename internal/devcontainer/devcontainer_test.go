package devcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// Sources the tests render with: a pinned release, and a staged binary whose
// digest is fixed so the golden Dockerfile does not depend on a build.
var (
	release = Source{Version: "v1.2.3"}
	staged  = Source{BinarySHA256: strings.Repeat("ab", 32)}
)

func TestProjectName(t *testing.T) {
	cases := []struct{ dir, want string }{
		{"/home/u/src/lyna-claude-tmux", "lyna-claude-tmux"},
		{"/home/u/src/My Project", "my-project"},
		{"/home/u/src/__weird..name__", "weird-name"},
		{"/home/u/src/Été 2026", "t-2026"},
		{"/home/u/src/api_v2.service", "api-v2-service"},
		{"/home/u/src/---", "workspace"},
		{"/", "workspace"},
		{"", "workspace"},
		{"relative/dir/", "dir"},
		{"/x/" + strings.Repeat("ab", 30), strings.Repeat("ab", 20)},
		{"/x/" + strings.Repeat("a", 39) + "-b", strings.Repeat("a", 39)},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			got := ProjectName(tc.dir)
			if got != tc.want {
				t.Fatalf("ProjectName(%q) = %q, want %q", tc.dir, got, tc.want)
			}
			if err := ValidateProject(got); err != nil {
				t.Fatalf("derived name does not validate: %v", err)
			}
		})
	}
}

func TestValidateProjectAndUser(t *testing.T) {
	projects := []struct {
		name string
		ok   bool
	}{
		{"app", true},
		{"my-app-2", true},
		{"a", true},
		{"", false},
		{"-app", false},
		{"app-", false},
		{"a--b", false},
		{"App", false},
		{"a.b", false},
		{"a_b", false},
		{strings.Repeat("a", 40), true},
		{strings.Repeat("a", 41), false},
	}
	for _, tc := range projects {
		t.Run("project "+tc.name, func(t *testing.T) {
			if err := ValidateProject(tc.name); (err == nil) != tc.ok {
				t.Fatalf("ValidateProject(%q) = %v, want ok=%v", tc.name, err, tc.ok)
			}
		})
	}
	users := []struct {
		name string
		ok   bool
	}{
		{"lyna", true},
		{"_svc", true},
		{"dev-1", true},
		{strings.Repeat("a", 32), true},
		{"root", false},
		{"", false},
		{"1dev", false},
		{"Dev", false},
		{"a b", false},
		{strings.Repeat("a", 33), false},
		{"a;rm", false},
	}
	for _, tc := range users {
		t.Run("user "+tc.name, func(t *testing.T) {
			if err := ValidateUser(tc.name); (err == nil) != tc.ok {
				t.Fatalf("ValidateUser(%q) = %v, want ok=%v", tc.name, err, tc.ok)
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	cases := []struct {
		in      string
		norm    string
		wantErr string
	}{
		{in: "api.anthropic.com", norm: "api.anthropic.com"},
		{in: "  Registry.NPMJS.org. ", norm: "registry.npmjs.org"},
		{in: "xn--bcher-kva.example", norm: "xn--bcher-kva.example"},
		{in: "a-b.c0.io", norm: "a-b.c0.io"},
		{in: "", norm: "", wantErr: "empty domain"},
		{in: "*.github.com", norm: "*.github.com", wantErr: "wildcard"},
		{in: "localhost", norm: "localhost", wantErr: "at least two labels"},
		{in: "10.0.0.1", norm: "10.0.0.1", wantErr: "numeric label"},
		{in: "-bad.com", norm: "-bad.com", wantErr: "invalid label"},
		{in: "bad-.com", norm: "bad-.com", wantErr: "invalid label"},
		{in: "a..com", norm: "a..com", wantErr: "invalid label"},
		{in: "under_score.com", norm: "under_score.com", wantErr: "invalid label"},
		{in: "evil.com\nexample.org", norm: "evil.com\nexample.org", wantErr: "invalid label"},
		{in: strings.Repeat("a", 64) + ".com", norm: strings.Repeat("a", 64) + ".com", wantErr: "invalid label"},
		{in: strings.Repeat("abcdefghi.", 26) + "com", norm: strings.Repeat("abcdefghi.", 26) + "com", wantErr: "longer than 253"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			norm := NormalizeDomain(tc.in)
			if norm != tc.norm {
				t.Fatalf("NormalizeDomain(%q) = %q, want %q", tc.in, norm, tc.norm)
			}
			err := ValidateDomain(norm)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ValidateDomain(%q) = %v, want error containing %q", norm, err, tc.wantErr)
			}
		})
	}
}

func TestRenderGolden(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{name: "defaults", opts: Options{Project: "lyna-claude-tmux", Source: staged}},
		{name: "pinned", opts: Options{
			Project: "api", User: "dev", Timezone: "Africa/Tunis", Source: release, ClaudeVersion: "2.1.273",
			AllowedDomains: []string{"registry.npmjs.org", "PROXY.golang.org.", "github.com", "sum.golang.org"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Render(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(files))
			for name := range files {
				names = append(names, name)
			}
			slices.Sort(names)
			want := []string{FileAllowedDomain, FileDockerfile, FileJSON, FileFirewall}
			slices.Sort(want)
			if !slices.Equal(names, want) {
				t.Fatalf("files = %v, want %v", names, want)
			}
			for _, name := range names {
				golden.Assert(t, tc.name+"/"+strings.TrimPrefix(name, Dir+"/"), files[name])
			}
			var spec map[string]any
			if err := json.Unmarshal(files[FileJSON], &spec); err != nil {
				t.Fatalf("devcontainer.json is not JSON: %v", err)
			}
			if strings.Contains(string(files[FileJSON]), "docker.sock") || strings.Contains(string(files[FileDockerfile]), "docker.sock") {
				t.Fatal("the docker socket must never be mounted")
			}
			// The image installs lyna-tmux exactly one way: the staged copy
			// or the release download, never both and never a floating
			// "latest" that only exists once a release is published.
			dockerfile := string(files[FileDockerfile])
			hasCopy, hasRelease := strings.Contains(dockerfile, stagedLine), strings.Contains(dockerfile, installScript)
			if hasCopy == hasRelease {
				t.Fatalf("Dockerfile copies a binary = %v and downloads a release = %v:\n%s", hasCopy, hasRelease, dockerfile)
			}
			if hasRelease && !strings.Contains(dockerfile, "--version "+tc.opts.Source.Version) {
				t.Fatalf("release install is not pinned to %s:\n%s", tc.opts.Source.Version, dockerfile)
			}
			if strings.Contains(dockerfile, "/main/scripts/install.sh") {
				t.Fatalf("installer fetched from an unpinned branch:\n%s", dockerfile)
			}
		})
	}
}

// TestRenderedFirewallIsTheTemplate pins that the script shipped in the
// image is byte for byte the one the firewall tests execute.
func TestRenderedFirewallIsTheTemplate(t *testing.T) {
	files, err := Render(Options{Project: "p", Source: release})
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join("templates", "init-firewall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(files[FileFirewall]) != string(onDisk) {
		t.Fatal("rendered init-firewall.sh differs from templates/init-firewall.sh")
	}
}

// TestRenderedImageCarriesTheIsolationMarker pins the two facts the isolation
// checks rely on: the image writes the marker they look for, and it creates
// the workspace mount point. Without either one a lyna-tmux dev container
// would be refused the relaxations it is entitled to.
func TestRenderedImageCarriesTheIsolationMarker(t *testing.T) {
	files, err := Render(Options{Project: "p", Source: release})
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(files[FileDockerfile])
	cases := []struct {
		name string
		want string
	}{
		{name: "marker path", want: sandbox.DevContainerMarker},
		{name: "marker is written", want: "> " + sandbox.DevContainerMarker},
		{name: "marker is root owned", want: "chown root:root " + sandbox.DevContainerMarker},
		{name: "marker is read only", want: "chmod 0444 " + sandbox.DevContainerMarker},
		{name: "workspace exists", want: "-m 0755 " + sandbox.DevContainerWorkspace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(dockerfile, tc.want) {
				t.Fatalf("Dockerfile lacks %q:\n%s", tc.want, dockerfile)
			}
		})
	}
}

// TestFirewallScriptPayload pins that the firewall payload carries its own
// script and takes the allowlist from the environment, never from the image.
func TestFirewallScriptPayload(t *testing.T) {
	script, err := FirewallScript()
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(Options{Project: "p", Source: release})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "starts with the allowlist preamble", ok: bytes.HasPrefix(script, []byte("umask 077\n"))},
		{name: "reads the allowlist from the environment", ok: bytes.Contains(script, []byte("\"$"+FirewallAllowlistEnv+"\""))},
		{name: "clears the environment value", ok: bytes.Contains(script, []byte("unset "+FirewallAllowlistEnv+"\n"))},
		{name: "ends with the embedded firewall", ok: bytes.HasSuffix(script, files[FileFirewall])},
		{name: "never sources the image path", ok: !bytes.Contains(script, []byte(". "+FirewallPath))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.ok {
				t.Fatalf("firewall payload:\n%s", script)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	cases := []struct {
		name string
		// change edits the written project before it is verified.
		change  func(t *testing.T, dir string)
		wantErr error
		errHas  string
	}{
		{name: "rendered files pass"},
		{
			name:    "a missing file is refused",
			change:  func(t *testing.T, dir string) { removeFile(t, dir, FileFirewall) },
			wantErr: ErrMissing,
			errHas:  "init-firewall.sh",
		},
		{
			name:    "a rewritten Dockerfile is refused",
			change:  func(t *testing.T, dir string) { writeFile(t, dir, FileDockerfile, "FROM scratch\n") },
			wantErr: ErrModified,
			errHas:  "Dockerfile",
		},
		{
			name:    "a single changed byte is refused",
			change:  func(t *testing.T, dir string) { writeFile(t, dir, FileAllowedDomain, "attacker.example\n") },
			wantErr: ErrModified,
			errHas:  "allowed-domains",
		},
		{
			name: "a file replaced by a link is refused",
			change: func(t *testing.T, dir string) {
				removeFile(t, dir, FileJSON)
				if err := os.Symlink(filepath.Join(dir, "planted.json"), filepath.Join(dir, filepath.FromSlash(FileJSON))); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name: "a file replaced by a directory is refused",
			change: func(t *testing.T, dir string) {
				removeFile(t, dir, FileJSON)
				if err := os.Mkdir(filepath.Join(dir, filepath.FromSlash(FileJSON)), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			errHas: "not a regular file",
		},
		{
			name: "extra files in the directory are not the boundary",
			change: func(t *testing.T, dir string) {
				writeFile(t, dir, Dir+"/README.md", "notes\n")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files, err := Render(Options{Project: "api", Source: release})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Write(dir, files, false); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, dir)
			}
			err = Verify(dir, files)
			switch {
			case tc.wantErr == nil && tc.errHas == "":
				if err != nil {
					t.Fatalf("Verify = %v, want nil", err)
				}
			case err == nil:
				t.Fatal("Verify accepted a project it should refuse")
			default:
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("Verify = %v, want %v", err, tc.wantErr)
				}
				if tc.errHas != "" && !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("Verify = %v, want it to name %q", err, tc.errHas)
				}
			}
		})
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

func TestRenderRejects(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{"missing project", Options{Source: release}, "invalid project name"},
		{"root user", Options{Project: "p", User: "root", Source: release}, "invalid user"},
		{"user injection", Options{Project: "p", User: "a\nRUN curl evil", Source: release}, "invalid user"},
		{"timezone injection", Options{Project: "p", Timezone: "UTC\nRUN id", Source: release}, "invalid timezone"},
		{"timezone traversal", Options{Project: "p", Timezone: "../../etc/passwd", Source: release}, "invalid timezone"},
		{"no source", Options{Project: "p"}, ErrNoSource.Error()},
		{"two sources", Options{Project: "p", Source: Source{Version: "v1.2.3", BinarySHA256: staged.BinarySHA256}}, ErrSourceConflict.Error()},
		{"version without v", Options{Project: "p", Source: Source{Version: "1.2.3"}}, "invalid lyna-tmux version"},
		{"version injection", Options{Project: "p", Source: Source{Version: "v1.2.3 && id"}}, "invalid lyna-tmux version"},
		{"short digest", Options{Project: "p", Source: Source{BinarySHA256: "abc"}}, "invalid binary digest"},
		{"digest injection", Options{Project: "p", Source: Source{BinarySHA256: strings.Repeat("a", 63) + "'"}}, "invalid binary digest"},
		{"upper-case digest", Options{Project: "p", Source: Source{BinarySHA256: strings.Repeat("AB", 32)}}, "invalid binary digest"},
		{"claude channel", Options{Project: "p", Source: release, ClaudeVersion: "nightly"}, "invalid Claude Code version"},
		{"claude injection", Options{Project: "p", Source: release, ClaudeVersion: "latest; id"}, "invalid Claude Code version"},
		{"bad domain", Options{Project: "p", Source: release, AllowedDomains: []string{"good.com", "*.bad.com"}}, "wildcard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Render(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || files != nil {
				t.Fatalf("Render() = %v files, %v; want error containing %q", len(files), err, tc.wantErr)
			}
		})
	}
}

func TestRenderDeduplicatesDomains(t *testing.T) {
	files, err := Render(Options{Project: "p", Source: release, AllowedDomains: []string{"API.anthropic.com", "example.dev", "example.dev."}})
	if err != nil {
		t.Fatal(err)
	}
	var domains []string
	for line := range strings.SplitSeq(string(files[FileAllowedDomain]), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			domains = append(domains, line)
		}
	}
	want := append(slices.Clone(DefaultDomains), "example.dev")
	if !slices.Equal(domains, want) {
		t.Fatalf("domains = %v, want %v", domains, want)
	}
}

func TestVolume(t *testing.T) {
	if got := Volume("api"); got != "lyna-tmux-claude-api" {
		t.Fatalf("Volume() = %q", got)
	}
}

func TestWrite(t *testing.T) {
	files := map[string][]byte{
		FileJSON:     []byte("{}\n"),
		FileFirewall: []byte("#!/usr/bin/env bash\n"),
		FileBinary:   []byte("\x7fELF"),
	}
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		files   map[string][]byte
		force   bool
		wantErr error
		errText string
		// untouched lists files that must keep their content after a refused write.
		untouched map[string]string
	}{
		{name: "fresh directory", files: files},
		{
			name:  "existing file refused without force",
			files: files,
			setup: func(t *testing.T, dir string) {
				mustWrite(t, filepath.Join(dir, ".devcontainer", "devcontainer.json"), "mine")
			},
			wantErr:   ErrExists,
			untouched: map[string]string{".devcontainer/devcontainer.json": "mine"},
		},
		{
			name:  "existing file replaced with force",
			files: files,
			force: true,
			setup: func(t *testing.T, dir string) {
				mustWrite(t, filepath.Join(dir, ".devcontainer", "devcontainer.json"), "mine")
			},
		},
		{
			name:  "symlinked file refused even with force",
			files: files,
			force: true,
			setup: func(t *testing.T, dir string) {
				mustWrite(t, filepath.Join(dir, "victim"), "secret")
				mustMkdir(t, filepath.Join(dir, ".devcontainer"))
				mustSymlink(t, filepath.Join(dir, "victim"), filepath.Join(dir, ".devcontainer", "init-firewall.sh"))
			},
			wantErr:   fsx.ErrSymlink,
			untouched: map[string]string{"victim": "secret"},
		},
		{
			name:  "symlinked directory refused",
			files: files,
			setup: func(t *testing.T, dir string) {
				elsewhere := filepath.Join(dir, "elsewhere")
				mustMkdir(t, elsewhere)
				mustSymlink(t, elsewhere, filepath.Join(dir, ".devcontainer"))
			},
			wantErr: fsx.ErrSymlink,
		},
		{
			name:  "directory component that is a file",
			files: files,
			setup: func(t *testing.T, dir string) {
				mustWrite(t, filepath.Join(dir, ".devcontainer"), "not a dir")
			},
			wantErr: fsx.ErrNotDir,
		},
		{
			name:  "target that is a directory",
			files: files,
			force: true,
			setup: func(t *testing.T, dir string) {
				mustMkdir(t, filepath.Join(dir, ".devcontainer", "devcontainer.json"))
			},
			errText: "is not a regular file",
		},
		{name: "escaping name", files: map[string][]byte{"../outside": nil}, errText: "invalid file name"},
		{name: "absolute name", files: map[string][]byte{"/etc/passwd": nil}, errText: "invalid file name"},
		{name: "backslash name", files: map[string][]byte{`a\b`: nil}, errText: "invalid file name"},
		{name: "empty name", files: map[string][]byte{"": nil}, errText: "invalid file name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			written, err := Write(dir, tc.files, tc.force)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.errText != "":
				if err == nil || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.errText)
				}
			case err != nil:
				t.Fatal(err)
			}
			if err != nil {
				if len(written) != 0 {
					t.Fatalf("refused write still wrote %v", written)
				}
				// Every case seeds files other than the firewall script, so a
				// regular file there means the refused write wrote something.
				if info, statErr := os.Lstat(filepath.Join(dir, ".devcontainer", "init-firewall.sh")); statErr == nil && info.Mode().IsRegular() {
					t.Fatal("a refused write created files")
				}
				for name, content := range tc.untouched {
					got, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
					if readErr != nil || string(got) != content {
						t.Fatalf("%s changed to %q (%v)", name, got, readErr)
					}
				}
				return
			}
			if len(written) != len(tc.files) {
				t.Fatalf("written = %v", written)
			}
			for name, content := range tc.files {
				path := filepath.Join(dir, filepath.FromSlash(name))
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				wantMode := fs.FileMode(0o644)
				if strings.HasSuffix(name, ".sh") || name == FileBinary {
					wantMode = 0o755
				}
				if info.Mode().Perm() != wantMode {
					t.Fatalf("%s mode %v, want %v", name, info.Mode().Perm(), wantMode)
				}
				got, _ := os.ReadFile(path)
				if string(got) != string(content) {
					t.Fatalf("%s = %q, want %q", name, got, content)
				}
			}
		})
	}
}

func TestWriteReportsMkdirFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := Write(dir, map[string][]byte{FileJSON: []byte("{}")}, false); err == nil {
		t.Fatal("Write into a read-only directory succeeded")
	}
	if _, err := Write(dir, map[string][]byte{"top-level": []byte("x")}, false); err == nil {
		t.Fatal("WriteFileAtomic into a read-only directory succeeded")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
