package app

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

// devcontainerProject creates a repository at <root>/src/My App with a
// subdirectory, and returns the repository root.
func devcontainerProject(t *testing.T, h Host) string {
	t.Helper()
	root := filepath.Join(h.Home, "src", "My App")
	mkdir(t, filepath.Join(root, ".git"))
	mkdir(t, filepath.Join(root, "services", "web"))
	return root
}

// elfHeader is the smallest ELF executable header debug/elf parses, for a
// 64-bit little-endian machine.
func elfHeader(machine elf.Machine) []byte {
	b := make([]byte, 64)
	copy(b, elf.ELFMAG)
	b[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	b[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(b[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(b[18:], uint16(machine))
	binary.LittleEndian.PutUint32(b[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(b[52:], 64)
	binary.LittleEndian.PutUint16(b[54:], 56)
	binary.LittleEndian.PutUint16(b[58:], 64)
	return b
}

// writeELF writes a fake Linux binary for machine and returns its path.
func writeELF(t *testing.T, path string, machine elf.Machine) string {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, elfHeader(machine), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeCheckout writes a minimal checkout of the lyna-tmux module below dir,
// whose cmd/lmux builds in a second, and returns its root.
func fakeCheckout(t *testing.T, root string) string {
	t.Helper()
	for name, content := range map[string]string{
		"go.mod":                      "module " + devcontainer.ModulePath + "\n",
		"internal/version/version.go": "package version\n\nvar Version = \"\"\n",
		"cmd/lmux/main.go": "package main\n\nimport (\n\t\"fmt\"\n\n\t\"" + devcontainer.ModulePath +
			"/internal/version\"\n)\n\nfunc main() { fmt.Println(version.Version) }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		mkdir(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// goOnPath is the real toolchain, or the test is skipped.
func goOnPath(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	return bin
}

func TestDevcontainerInit(t *testing.T) {
	const arch = "amd64"
	released := version.Info{Version: "v1.4.0", Commit: "abc123", Date: "2026-09-16T00:00:00Z"}
	dev := version.Info{Version: "dev"}
	cases := []struct {
		name   string
		config string
		// bins are the programs on PATH; "go" maps to the real toolchain.
		bins []string
		// before edits the project before init; cwd and dir default to the
		// project's services/web subdirectory and the request's Dir.
		before func(t *testing.T, root string)
		req    func(t *testing.T, root string) DevcontainerInitRequest
		// exe is the machine of this executable; zero means one for another
		// architecture, so a linux host only stages it when it fits.
		exe elf.Machine
		// wantSource is a fragment of the reported source.
		wantSource string
		// wantStaged is whether a binary and the .gitignore entry are written.
		wantStaged bool
		// wantVersion is the release pinned in the Dockerfile, if any.
		wantVersion string
		wantErr     error
		errHas      []string
		// errNot is text the failure must not hold, for wording that only
		// reads right in one of the shapes a reason is written in.
		errNot []string
	}{
		{
			name: "release of this executable when nothing can be built", config: "[sandbox]\nallowed_domains = [\"pkg.internal.example\"]\n",
			req:        func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: released} },
			wantSource: "release v1.4.0", wantVersion: "v1.4.0",
		},
		{
			name: "a pinned release from the flag",
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Version: "v2.0.0", Identity: released}
			},
			wantSource: "release v2.0.0", wantVersion: "v2.0.0",
		},
		{
			name: "an invalid release from the flag",
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Version: "2.0.0", Identity: released}
			},
			errHas: []string{"invalid lmux version"},
		},
		{
			name: "a staged binary from the flag",
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Binary: writeELF(t, filepath.Join(root, "build", "lmux"), elf.EM_X86_64), Identity: dev}
			},
			wantSource: "staged from", wantStaged: true,
		},
		{
			name: "a binary for another architecture is refused",
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Binary: writeELF(t, filepath.Join(root, "build", "lmux"), elf.EM_AARCH64), Identity: dev}
			},
			errHas: []string{"the image needs linux/amd64"},
		},
		{
			name: "a binary that is not ELF is refused",
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				path := filepath.Join(root, "build", "lmux")
				mkdir(t, filepath.Dir(path))
				if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				return DevcontainerInitRequest{Binary: path, Identity: dev}
			},
			errHas: []string{"not an ELF binary"},
		},
		{
			name: "a missing binary is refused",
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Binary: filepath.Join(root, "nope"), Identity: released}
			},
			wantErr: os.ErrNotExist,
		},
		{
			name: "binary and version together are refused",
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Binary: writeELF(t, filepath.Join(root, "build", "lmux"), elf.EM_X86_64), Version: "v2.0.0", Identity: released}
			},
			wantErr: devcontainer.ErrSourceConflict, errHas: []string{"--binary", "--version"},
		},
		{
			name: "this executable on a linux host", exe: elf.EM_X86_64,
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{OS: "linux", Identity: dev}
			},
			wantSource: "this executable", wantStaged: true,
		},
		{
			name: "this executable is not used on another OS", exe: elf.EM_X86_64,
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{OS: "darwin", Identity: released}
			},
			wantSource: "release v1.4.0", wantVersion: "v1.4.0",
		},
		{
			name: "this executable is skipped when it is another architecture",
			bins: []string{"go"},
			req: func(_ *testing.T, _ string) DevcontainerInitRequest {
				return DevcontainerInitRequest{OS: "linux", Identity: released}
			},
			before:     func(t *testing.T, root string) { fakeCheckout(t, filepath.Join(root, "services")) },
			wantSource: "built from", wantStaged: true,
		},
		{
			name: "built from the checkout containing the target", bins: []string{"go"},
			before:     func(t *testing.T, root string) { fakeCheckout(t, filepath.Join(root, "services")) },
			req:        func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: dev} },
			wantSource: "built from", wantStaged: true,
		},
		{
			name: "built from the checkout the command runs in", bins: []string{"go"},
			before: func(t *testing.T, root string) { fakeCheckout(t, filepath.Join(root, "tool")) },
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: filepath.Join(root, "services", "web"), Cwd: filepath.Join(root, "tool"), Named: true, Identity: dev}
			},
			wantSource: "built from", wantStaged: true,
		},
		{
			name: "a build that fails is reported", bins: []string{"go"},
			before: func(t *testing.T, root string) {
				checkout := fakeCheckout(t, filepath.Join(root, "services"))
				if err := os.WriteFile(filepath.Join(checkout, "cmd", "lmux", "main.go"), []byte("package main\n\nfunc main() { undefined() }\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			req:    func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: released} },
			errHas: []string{"go build in", "undefined: undefined"},
		},
		{
			name: "the checkout wins over the release", bins: []string{"go"},
			before:     func(t *testing.T, root string) { fakeCheckout(t, filepath.Join(root, "services")) },
			req:        func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: released} },
			wantSource: "built from", wantStaged: true,
		},
		{
			name: "no checkout, go on PATH, not a release", bins: []string{"go"},
			req:     func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: dev} },
			wantErr: ErrDevcontainerNoBinary, errHas: []string{"is not a release", "--binary", "--version vX.Y.Z", "linux/amd64", "is not inside a checkout of " + devcontainer.ModulePath},
			// init ran where it writes, so the directory is named once.
			errNot: []string{"neither "},
		},
		{
			name: "a named target away from the working directory names both", bins: []string{"go"},
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: filepath.Join(root, "services", "web"), Cwd: root, Named: true, Identity: dev}
			},
			wantErr: ErrDevcontainerNoBinary,
			errHas:  []string{"neither ", " nor ", "is inside a checkout of " + devcontainer.ModulePath},
		},
		{
			name:    "no go, not a release",
			req:     func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: dev} },
			wantErr: ErrDevcontainerNoBinary, errHas: []string{"go is not on PATH", "darwin/amd64 build", "--binary", "--version"},
		},
		{
			name: "a pseudo-version is not a release",
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Identity: version.Info{Version: "v0.0.0-20260916120000-abcdef123456"}}
			},
			wantErr: ErrDevcontainerNoBinary,
		},
		{
			name: "keeps existing files",
			before: func(t *testing.T, root string) {
				mkdir(t, filepath.Join(root, "services", "web", ".devcontainer"))
				if err := os.WriteFile(filepath.Join(root, "services", "web", ".devcontainer", "Dockerfile"), []byte("FROM mine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			req:     func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: released} },
			wantErr: devcontainer.ErrExists, errHas: []string{"--force"},
		},
		{
			name: "force replaces them",
			before: func(t *testing.T, root string) {
				mkdir(t, filepath.Join(root, "services", "web", ".devcontainer"))
				if err := os.WriteFile(filepath.Join(root, "services", "web", ".devcontainer", "Dockerfile"), []byte("FROM mine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			req: func(*testing.T, string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Force: true, Identity: released}
			},
			wantSource: "release v1.4.0", wantVersion: "v1.4.0",
		},
		{
			name: "the staged binary is replaced with force",
			before: func(t *testing.T, root string) {
				writeELF(t, filepath.Join(root, "services", "web", ".devcontainer", "lmux"), elf.EM_AARCH64)
			},
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Force: true, Binary: writeELF(t, filepath.Join(root, "build", "lmux"), elf.EM_X86_64), Identity: dev}
			},
			wantSource: "staged from", wantStaged: true,
		},
		{
			name:   "invalid configured domain",
			config: "[sandbox]\nallowed_domains = [\"not a domain\"]\n",
			req:    func(*testing.T, string) DevcontainerInitRequest { return DevcontainerInitRequest{Identity: released} },
			errHas: []string{"domain"},
		},
		{
			name: "an unnamed target away from the working directory is refused",
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: root, Cwd: filepath.Join(root, "services", "web"), Identity: released}
			},
			wantErr: ErrDevcontainerUnnamed, errHas: []string{"devcontainer init "},
		},
		{
			name: "a named target away from the working directory is written",
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: filepath.Join(root, "services", "web"), Cwd: root, Named: true, Identity: released}
			},
			wantSource: "release v1.4.0", wantVersion: "v1.4.0",
		},
		{
			name: "a relative named target is resolved against the working directory",
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: "web", Cwd: filepath.Join(root, "services"), Named: true, Identity: released}
			},
			wantSource: "release v1.4.0", wantVersion: "v1.4.0",
		},
		{
			name: "a target that is a file is refused",
			req: func(t *testing.T, root string) DevcontainerInitRequest {
				path := filepath.Join(root, "services", "web", "notes.txt")
				if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				return DevcontainerInitRequest{Dir: path, Cwd: root, Named: true, Identity: released}
			},
			errHas: []string{"not a directory"},
		},
		{
			name: "a missing target is refused",
			req: func(_ *testing.T, root string) DevcontainerInitRequest {
				return DevcontainerInitRequest{Dir: filepath.Join(root, "nope"), Cwd: root, Named: true, Identity: released}
			},
			wantErr: os.ErrNotExist,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bins := map[string]string{}
			for _, b := range tc.bins {
				if b == "go" {
					bins[b] = goOnPath(t)
				}
			}
			h := isolationHost(t, bins)
			exe := tc.exe
			if exe == elf.EM_NONE {
				exe = elf.EM_AARCH64
			}
			writeELF(t, h.Exe, exe)
			root := devcontainerProject(t, h)
			if tc.config != "" {
				sandboxWriteConfig(t, h.Getenv("LYNA_TMUX_HOME"), tc.config)
			}
			if tc.before != nil {
				tc.before(t, root)
			}
			req := tc.req(t, root)
			target := filepath.Join(root, "services", "web")
			if req.Cwd == "" {
				req.Cwd = target
			}
			if req.OS == "" {
				req.OS = "darwin"
			}
			req.Arch = arch
			res, err := DevcontainerInit(t.Context(), h, req)
			if tc.wantErr != nil || len(tc.errHas) > 0 {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				for _, want := range tc.errHas {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("err = %v, want it to contain %q", err, want)
					}
				}
				for _, unwanted := range tc.errNot {
					if strings.Contains(err.Error(), unwanted) {
						t.Fatalf("err = %v, want it not to contain %q", err, unwanted)
					}
				}
				// A refused init writes nothing anywhere below the project,
				// existing files apart.
				for _, dir := range []string{root, filepath.Join(root, "services")} {
					if _, statErr := os.Lstat(filepath.Join(dir, ".devcontainer")); statErr == nil {
						t.Fatalf("a refused init created %s/.devcontainer", dir)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Dir != target || !strings.Contains(res.Source, tc.wantSource) {
				t.Fatalf("result %+v, want dir %s and source containing %q", res, target, tc.wantSource)
			}
			want := []string{
				filepath.Join(target, ".devcontainer", "Dockerfile"),
				filepath.Join(target, ".devcontainer", "allowed-domains"),
				filepath.Join(target, ".devcontainer", "devcontainer.json"),
				filepath.Join(target, ".devcontainer", "init-firewall.sh"),
			}
			if tc.wantStaged {
				// Write reports paths in name order; the .gitignore entry
				// comes last, added after the files are in place.
				want = append(want, filepath.Join(target, ".devcontainer", "lmux"), filepath.Join(target, ".devcontainer", ".gitignore"))
			}
			if !slices.Equal(res.Written, want) {
				t.Fatalf("written %q, want %q", res.Written, want)
			}
			// The repository root above the target is never written to: that
			// is the regression a project-root walk introduced.
			if _, err := os.Lstat(filepath.Join(root, ".devcontainer")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("init wrote into the repository root: %v", err)
			}
			dockerfile, err := os.ReadFile(want[0])
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wantStaged:
				binary := filepath.Join(target, ".devcontainer", "lmux")
				assertMode(t, binary, 0o755)
				data, err := os.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				if err := devcontainer.ValidateBinary(data, arch); err != nil {
					t.Fatalf("staged binary: %v", err)
				}
				if !strings.Contains(string(dockerfile), devcontainer.Digest(data)) {
					t.Fatalf("Dockerfile does not pin the staged digest:\n%s", dockerfile)
				}
				ignore, err := os.ReadFile(filepath.Join(target, ".devcontainer", ".gitignore"))
				if err != nil || !strings.Contains(string(ignore), "\nlmux\n") {
					t.Fatalf(".gitignore = %q, %v", ignore, err)
				}
				if strings.Contains(string(dockerfile), "install-lyna-tmux.sh") {
					t.Fatalf("a staged image still downloads a release:\n%s", dockerfile)
				}
			default:
				if !strings.Contains(string(dockerfile), "--version "+tc.wantVersion+" ") {
					t.Fatalf("Dockerfile does not pin %s:\n%s", tc.wantVersion, dockerfile)
				}
				if _, err := os.Lstat(filepath.Join(target, ".devcontainer", ".gitignore")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a release image wrote a .gitignore: %v", err)
				}
			}
			// What init wrote is what up will build.
			src, err := devcontainer.DetectSource(target)
			if err != nil {
				t.Fatal(err)
			}
			files, err := devcontainer.Render(devcontainer.Options{Project: "web", Source: src, AllowedDomains: nil})
			if err != nil {
				t.Fatal(err)
			}
			if tc.config == "" {
				if err := devcontainer.Verify(target, files); err != nil {
					t.Fatal(err)
				}
			}
			domains, err := os.ReadFile(want[1])
			if err != nil {
				t.Fatal(err)
			}
			if tc.config != "" && !strings.Contains(string(domains), "pkg.internal.example\n") {
				t.Fatalf("configured domain missing:\n%s", domains)
			}
			js, err := os.ReadFile(want[2])
			if err != nil || !strings.Contains(string(js), "source=lyna-tmux-claude-web,") {
				t.Fatalf("devcontainer.json does not name the directory: %v\n%s", err, js)
			}
		})
	}
}

func TestDevcontainerDir(t *testing.T) {
	h := isolationHost(t, nil)
	root := devcontainerProject(t, h)
	sub := filepath.Join(root, "services", "web")
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		cwd     string
		dir     string
		want    string
		wantErr error
		errHas  string
	}{
		{name: "the working directory, not the repository root", cwd: sub, want: sub},
		{name: "a named absolute directory", cwd: root, dir: sub, want: sub},
		{name: "a named relative directory", cwd: root, dir: "services/web", want: sub},
		{name: "a named parent", cwd: sub, dir: "..", want: filepath.Join(root, "services")},
		{name: "a trailing slash is cleaned", cwd: sub + "/", want: sub},
		{name: "no working directory", errHas: "no working directory"},
		{name: "a missing directory", cwd: root, dir: "nope", wantErr: os.ErrNotExist},
		{name: "a file", cwd: root, dir: "file", errHas: "not a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DevcontainerDir(tc.cwd, tc.dir)
			if tc.wantErr != nil || tc.errHas != "" {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("DevcontainerDir(%q, %q) = %q, %v; want %q", tc.cwd, tc.dir, got, err, tc.want)
			}
		})
	}
}

func TestDevcontainerTarget(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		needFiles bool
		wantErr   error
		errHas    string
	}{
		{name: "without files when none are needed"},
		{name: "files needed but missing", needFiles: true, wantErr: ErrDevcontainerMissing, errHas: "lmux sandbox devcontainer init"},
		{name: "files present", needFiles: true, setup: func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, ".devcontainer"))
			if err := os.WriteFile(filepath.Join(dir, ".devcontainer", "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Dockerfile is a directory", needFiles: true, setup: func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, ".devcontainer", "Dockerfile"))
		}, wantErr: ErrDevcontainerMissing, errHas: "not a regular file"},
		{name: "Dockerfile is a link", needFiles: true, setup: func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, ".devcontainer"))
			if err := os.Symlink("/etc/hosts", filepath.Join(dir, ".devcontainer", "Dockerfile")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: ErrDevcontainerMissing, errHas: "not a regular file"},
		// Files in the repository root do not count: the target is the
		// directory itself, never an ancestor.
		{name: "files in the repository root are not the directory's", needFiles: true, setup: func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(filepath.Dir(dir), ".devcontainer"))
			if err := os.WriteFile(filepath.Join(filepath.Dir(dir), ".devcontainer", "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, wantErr: ErrDevcontainerMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			dir := filepath.Join(root, "services")
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			got, err := DevcontainerTarget(dir, tc.needFiles)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := devcontainer.Target{Dir: dir, Project: "services", User: devcontainer.DefaultUser}
			if got != want {
				t.Fatalf("target %+v, want %+v", got, want)
			}
		})
	}
	if _, err := DevcontainerTarget("/nonexistent/lyna-tmux-test", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	h := isolationHost(t, nil)
	file := filepath.Join(h.Home, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DevcontainerTarget(file, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file as target: %v", err)
	}
}

func TestContainerCreateFor(t *testing.T) {
	cases := []struct {
		name        string
		config      func(c *config.Config)
		inside      bool
		sub         string
		req         CreateRequest
		detach      bool
		wantHandled bool
		wantArgs    []string
		wantErr     error
	}{
		{name: "bash isolation stays on this host", req: CreateRequest{}},
		{name: "process isolation stays on this host", req: CreateRequest{Launch: LaunchOptions{Isolation: "process"}}},
		{
			name: "container from the flag", req: CreateRequest{Launch: LaunchOptions{Isolation: "container"}}, wantHandled: true,
			wantArgs: []string{"create", "--sandbox=standard", "--isolation=container", "/workspace"},
		},
		{
			name: "container from the configuration, in a subdirectory", config: func(c *config.Config) { c.Sandbox.Isolation, c.Sandbox.Profile = "container", "strict" },
			sub: "services/web", wantHandled: true,
			wantArgs: []string{"create", "--sandbox=strict", "--isolation=container", "/workspace/services/web"},
		},
		{
			name: "every flag is passed on", sub: "services", detach: true, wantHandled: true,
			req: CreateRequest{Layout: "trio", Name: "api", Launch: LaunchOptions{
				Isolation: "container", Model: "opus", Effort: "high", PermissionMode: "bypassPermissions", Sandbox: "off", Continue: true,
				ExtraArgs: []string{"--verbose", "-p", "--layout=x"},
			}},
			wantArgs: []string{
				"create", "--layout=trio", "--name=api", "--model=opus", "--effort=high", "--mode=bypassPermissions", "--sandbox=off",
				"--isolation=container", "--continue", "--detach", "/workspace/services", "--", "--verbose", "-p", "--layout=x",
			},
		},
		{
			name: "agent teams run the team command in the container", wantHandled: true,
			req:      CreateRequest{Launch: LaunchOptions{Isolation: "container", Teams: true}},
			wantArgs: []string{"team", "--sandbox=standard", "--isolation=container", "/workspace"},
		},
		{
			name: "the terminal size reaches the inner create", wantHandled: true,
			req:      CreateRequest{Width: 200, Height: 50, Launch: LaunchOptions{Isolation: "container"}},
			wantArgs: []string{"create", "--sandbox=standard", "--width=200", "--height=50", "--isolation=container", "/workspace"},
		},
		{
			name: "a size of zero is left out", wantHandled: true,
			req:      CreateRequest{Width: 0, Height: 40, Launch: LaunchOptions{Isolation: "container"}},
			wantArgs: []string{"create", "--sandbox=standard", "--height=40", "--isolation=container", "/workspace"},
		},
		{name: "inside the container the workspace is created here", inside: true, req: CreateRequest{Launch: LaunchOptions{Isolation: "container"}}},
		{name: "off in the configuration is refused", config: func(c *config.Config) { c.Sandbox.Isolation, c.Sandbox.Profile = "container", "off" }, wantHandled: true, wantErr: ErrSandboxOffInConfig},
		{name: "unknown isolation", req: CreateRequest{Launch: LaunchOptions{Isolation: "vm"}}, wantHandled: true, wantErr: sandbox.ErrInvalid},
		{name: "unknown profile", req: CreateRequest{Launch: LaunchOptions{Isolation: "container", Sandbox: "loose"}}, wantHandled: true, wantErr: sandbox.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolationInsideContainer(t, tc.inside)
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			cfg := config.Default()
			if tc.config != nil {
				tc.config(&cfg)
			}
			s := &Server{Config: cfg}
			tc.req.Dir = filepath.Join(root, filepath.FromSlash(tc.sub))
			got, handled, err := s.ContainerCreateFor(tc.req, tc.detach)
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v (err %v)", handled, tc.wantHandled, err)
			}
			// A request it could not decide about is still its own: a caller
			// that stops at an unhandled request must never drop the failure.
			if err != nil && !handled {
				t.Fatalf("err %v reported with handled false", err)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantHandled {
				return
			}
			// The create keeps the project root of the session commands: the
			// container is the repository's, wherever the launch happens.
			if got.Target != (devcontainer.Target{Dir: root, Project: "my-app", User: devcontainer.DefaultUser}) || !slices.Equal(got.Args, tc.wantArgs) {
				t.Fatalf("got %+v\nwant args %q", got, tc.wantArgs)
			}
		})
	}
}
