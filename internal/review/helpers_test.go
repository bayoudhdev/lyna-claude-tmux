package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/xdg"
)

// Environment of the git test double: when envFakeGit is set the test binary
// behaves as git, reporting a wrong HEAD commit and running the real git
// named by envRealGit for every other command.
const (
	envFakeGit = "LYNA_TMUX_TEST_FAKE_GIT"
	envRealGit = "LYNA_TMUX_TEST_REAL_GIT"
	wrongHead  = "0000000000000000000000000000000000000000"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFakeGit) == "1" {
		os.Exit(fakeGit(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeGit(args []string) int {
	for _, a := range args {
		if a == "rev-parse" {
			fmt.Println(wrongHead)
			return 0
		}
	}
	cmd := exec.Command(os.Getenv(envRealGit), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	return 0
}

// testContext bounds one test step, well inside the test binary timeout.
func testContext(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func requireGit(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	return bin
}

// gitTestEnv isolates git from the developer's configuration.
func gitTestEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

func runGit(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=lyna-test", "-c", "user.email=test@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pluginRepo is a bare repository serving plugin-shaped commits over file://.
type pluginRepo struct {
	URL     string
	Commits []string
	env     []string
}

// newPluginRepo commits each file set in order (each one replaces the
// previous tree) and publishes the history as a bare repository.
func newPluginRepo(t *testing.T, trees ...map[string]string) pluginRepo {
	t.Helper()
	requireGit(t)
	env := gitTestEnv(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, env, work, "init", "-q")
	var commits []string
	for i, tree := range trees {
		runGit(t, env, work, "rm", "-rq", "--ignore-unmatch", ".")
		writeTree(t, work, tree)
		runGit(t, env, work, "add", "-A")
		runGit(t, env, work, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("commit %d", i))
		commits = append(commits, runGit(t, env, work, "rev-parse", "HEAD"))
	}
	bare := filepath.Join(root, "plugin.git")
	runGit(t, env, root, "clone", "-q", "--bare", work, bare)
	return pluginRepo{URL: "file://" + bare, Commits: commits, env: env}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// testPaths returns lyna-tmux paths rooted in a temporary LYNA_TMUX_HOME.
func testPaths(t *testing.T) xdg.Paths {
	t.Helper()
	root := t.TempDir()
	paths, err := xdg.Resolve(func(k string) string {
		if k == "LYNA_TMUX_HOME" {
			return root
		}
		return ""
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// copyDir copies a fixture tree.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// stagingLeftovers lists staging directories left in the review directory.
func stagingLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// snapshot maps every file under dir to its content, for before and after comparisons.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
