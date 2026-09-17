package devcontainer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/fsx"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// ModulePath is the Go module lyna-tmux is built from. Build compiles its
// cmd/lmux package and ModuleRoot recognizes a checkout by it.
const ModulePath = "github.com/bayoudhdev/lyna-claude-tmux"

// BinaryPath is where the image installs the command.
const BinaryPath = "/usr/local/bin/" + xdg.Command

// MaxBinaryBytes bounds a binary read for staging or hashing. A release
// binary is a few tens of megabytes.
const MaxBinaryBytes int64 = 256 << 20

// Source says where the lyna-tmux binary of the image comes from. Exactly one
// field is set: a release exists only once it is published, and a checkout or
// a binary of one's own is what a build needs before then.
type Source struct {
	// Version is the release (vX.Y.Z) install.sh downloads and verifies when
	// the image builds.
	Version string
	// BinarySHA256 is the hex digest of the binary staged at FileBinary. The
	// build copies that file into the image and checks it against the
	// digest, so the Dockerfile pins the binary it was rendered for.
	BinarySHA256 string
}

// Errors of the binary source.
var (
	// ErrBinaryMissing is returned when the Dockerfile copies a staged binary
	// that is not next to it.
	ErrBinaryMissing = errors.New("devcontainer: the staged lyna-tmux binary is missing")
	// ErrSourceConflict is returned when a staged binary and a release are
	// both asked for.
	ErrSourceConflict = errors.New("devcontainer: a staged binary and a release version are mutually exclusive")
	// ErrNoSource is returned when neither a staged binary nor a release is
	// given.
	ErrNoSource = errors.New("devcontainer: no lyna-tmux source (a staged binary or a release version)")
)

var (
	digestPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9a-f]{64}$`) })
	// pseudoPattern matches the module pseudo-version suffix of an untagged
	// commit; the Go toolchain reports one, and no release carries it.
	pseudoPattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`-[0-9]{14}-[0-9a-f]{12}$`) })
	// releaseLine finds the version the rendered install step pins; only a
	// version matching versionPattern is accepted from it.
	releaseLine = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(regexp.QuoteMeta(installScript+" --prefix /usr/local/bin --version ") + `(\S+)`)
	})
	// ldflagValue is what an identity field may hold to be stamped through
	// -ldflags, which the go command splits on spaces and quotes.
	ldflagValue = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9A-Za-z._:+/-]+$`) })
)

// Dockerfile fragments DetectSource looks for. The template writes both, and
// TestDetectSourceRoundTrip pins that they stay in step.
const (
	installScript = "sh /tmp/install-lyna-tmux.sh"
	stagedLine    = "COPY " + xdg.Command + " " + BinaryPath
)

func (s Source) validate() error {
	switch {
	case s.Version != "" && s.BinarySHA256 != "":
		return ErrSourceConflict
	case s.Version != "":
		if !versionPattern().MatchString(s.Version) {
			return fmt.Errorf("devcontainer: invalid lmux version %q (vX.Y.Z)", s.Version)
		}
	case s.BinarySHA256 != "":
		if !digestPattern().MatchString(s.BinarySHA256) {
			return fmt.Errorf("devcontainer: invalid binary digest %q (64 hex characters)", s.BinarySHA256)
		}
	default:
		return ErrNoSource
	}
	return nil
}

// IsRelease reports whether v names a published release: a tag of the form
// vX.Y.Z with an optional pre-release suffix, and not the pseudo-version the
// toolchain gives an untagged commit.
func IsRelease(v string) bool {
	return versionPattern().MatchString(v) && !pseudoPattern().MatchString(v)
}

// Digest is the hex SHA-256 of data, the form sha256sum prints.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// elfTarget is the ELF identity of a Linux binary for one GOARCH.
type elfTarget struct {
	class   elf.Class
	data    elf.Data
	machine elf.Machine
}

// elfTargets lists the architectures debian:13-slim is published for.
var elfTargets = map[string]elfTarget{
	"amd64":   {elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64},
	"arm64":   {elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64},
	"386":     {elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_386},
	"arm":     {elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_ARM},
	"riscv64": {elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_RISCV},
	"ppc64le": {elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_PPC64},
	"s390x":   {elf.ELFCLASS64, elf.ELFDATA2MSB, elf.EM_S390},
}

// ValidateBinary checks that data is an ELF executable a Linux image for
// goarch can run: the class, byte order and machine of that architecture, an
// executable or position-independent type, and a System V or Linux ABI. A
// Mach-O or Windows build of lyna-tmux, or a Linux build for another
// architecture, would otherwise only fail once the container starts.
func ValidateBinary(data []byte, goarch string) error {
	want, ok := elfTargets[goarch]
	if !ok {
		return fmt.Errorf("devcontainer: no Linux image architecture for %q", goarch)
	}
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("devcontainer: not an ELF binary (%w); the image needs a linux/%s build of lyna-tmux", err, goarch)
	}
	defer func() { _ = f.Close() }()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("devcontainer: ELF file of type %s is not an executable", f.Type)
	}
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("devcontainer: ELF binary targets %s, the image needs Linux", f.OSABI)
	}
	if f.Class != want.class || f.Data != want.data || f.Machine != want.machine {
		return fmt.Errorf("devcontainer: ELF binary is built for %s (%s), the image needs linux/%s", f.Machine, f.Class, goarch)
	}
	return nil
}

// ModuleRoot returns the nearest ancestor of dir (dir included) holding a
// go.mod, when that go.mod declares ModulePath: the checkout Build compiles.
// The search stops at the first go.mod, as the go command does, so a
// directory inside some other module is never taken for the checkout.
func ModuleRoot(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for cur := abs; ; {
		gomod, err := fsx.ReadFileNoFollow(filepath.Join(cur, "go.mod"), MaxFileBytes)
		if err == nil {
			return cur, modulePathOf(gomod) == ModulePath
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// modulePathOf reads the module directive of a go.mod.
func modulePathOf(gomod []byte) string {
	for line := range strings.SplitSeq(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// Identity is stamped into a built binary, so that lmux version inside
// the container reports the build the image came from.
type Identity struct {
	Version, Commit, Date string
}

// ldflags renders the linker flags of a build: symbols stripped, and every
// identity field the go command can carry through its space-separated flag
// list.
func (id Identity) ldflags() string {
	flags := []string{"-s", "-w"}
	for _, f := range []struct{ name, value string }{{"Version", id.Version}, {"Commit", id.Commit}, {"Date", id.Date}} {
		if f.value != "" && ldflagValue().MatchString(f.value) {
			flags = append(flags, "-X", ModulePath+"/internal/version."+f.name+"="+f.value)
		}
	}
	return strings.Join(flags, " ")
}

// Build compiles cmd/lmux of the checkout at root into a static
// linux/goarch executable with the go binary at goBin, and returns its bytes.
// The build runs with the environment of this process, so it shares the
// module and build caches of the user. The binary is identified as the
// executable that staged it (id) rather than by the checkout's version
// control state, which the go command would otherwise need to read: a
// checkout whose repository is not readable must still build.
func Build(ctx context.Context, goBin, root, goarch string, id Identity) ([]byte, error) {
	if _, ok := elfTargets[goarch]; !ok {
		return nil, fmt.Errorf("devcontainer: no Linux image architecture for %q", goarch)
	}
	tmp, err := os.MkdirTemp("", "lyna-tmux-build-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "lmux")
	cmd := exec.CommandContext(ctx, goBin, "build", "-trimpath", "-buildvcs=false", "-ldflags", id.ldflags(), "-o", out, "./cmd/lmux")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(output.String()); msg != "" {
			return nil, fmt.Errorf("devcontainer: go build in %s: %w: %s", root, err, msg)
		}
		return nil, fmt.Errorf("devcontainer: go build in %s: %w", root, err)
	}
	data, err := fsx.ReadFileNoFollow(out, MaxBinaryBytes)
	if err != nil {
		return nil, err
	}
	if err := ValidateBinary(data, goarch); err != nil {
		return nil, err
	}
	return data, nil
}

// DetectSource reads back the source init recorded in the Dockerfile under
// dir, so that a later Up renders the same files. The Dockerfile only picks
// which rendering to compare against: a staged binary is hashed from disk and
// a version is accepted only in the release form, and Verify then holds the
// file to that rendering byte for byte. A Dockerfile that copies a binary
// which is not there fails here with ErrBinaryMissing, before docker build
// would fail on the COPY.
func DetectSource(dir string) (Source, error) {
	dockerfile := filepath.Join(dir, filepath.FromSlash(FileDockerfile))
	text, err := fsx.ReadFileNoFollow(dockerfile, MaxFileBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Source{}, fmt.Errorf("%s: %w; run `lmux sandbox devcontainer init`", dockerfile, ErrMissing)
	case err != nil:
		return Source{}, err
	}
	if bytes.Contains(text, []byte(stagedLine)) {
		binary := filepath.Join(dir, filepath.FromSlash(FileBinary))
		data, err := fsx.ReadFileNoFollow(binary, MaxBinaryBytes)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return Source{}, fmt.Errorf("%s: %w; run `lmux sandbox devcontainer init --force` to stage it again", binary, ErrBinaryMissing)
		case err != nil:
			return Source{}, err
		}
		return Source{BinarySHA256: Digest(data)}, nil
	}
	if m := releaseLine().FindSubmatch(text); m != nil && versionPattern().Match(m[1]) {
		return Source{Version: string(m[1])}, nil
	}
	return Source{}, fmt.Errorf("%s: %w; run `lmux sandbox devcontainer init --force` to restore it", dockerfile, ErrModified)
}

// ignoreHeader explains the entry IgnoreBinary adds.
const ignoreHeader = "# The lmux binary staged by lmux sandbox devcontainer init is built for this machine.\n"

// IgnoreBinary makes sure the .gitignore of the .devcontainer directory under
// dir lists the staged binary, creating the file when there is none and
// appending to one that lacks the entry. It reports the path and whether the
// file changed. The file belongs to the user, so it is never rewritten
// wholesale and needs no --force.
func IgnoreBinary(dir string) (string, bool, error) {
	target := filepath.Join(dir, filepath.FromSlash(FileGitignore))
	entry := strings.TrimPrefix(FileBinary, Dir+"/")
	existing, err := fsx.ReadFileNoFollow(target, MaxFileBytes)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return target, false, err
	}
	for line := range strings.SplitSeq(string(existing), "\n") {
		if l := strings.TrimSpace(line); l == entry || l == "/"+entry {
			return target, false, nil
		}
	}
	mode := fs.FileMode(0o644)
	if info, statErr := os.Lstat(target); statErr == nil {
		mode = info.Mode().Perm()
	}
	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ignoreHeader + entry + "\n"
	if err := fsx.WriteFileAtomic(target, []byte(content), mode); err != nil {
		return target, false, err
	}
	return target, true, nil
}
