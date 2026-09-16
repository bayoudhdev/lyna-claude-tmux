package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/config"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/devcontainer"
	"github.com/bayoudhdev/lyna-claude-tmux/internal/domain/sandbox"
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

func TestDevcontainerInit(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		before  func(t *testing.T, root string)
		force   bool
		wantErr error
		errHas  string
	}{
		{name: "renders every file with the configured domains", config: "[sandbox]\nallowed_domains = [\"pkg.internal.example\"]\n"},
		{name: "keeps existing files", before: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, ".devcontainer"))
			if err := os.WriteFile(filepath.Join(root, ".devcontainer", "Dockerfile"), []byte("FROM mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, wantErr: devcontainer.ErrExists, errHas: "--force"},
		{name: "force replaces them", force: true, before: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, ".devcontainer"))
			if err := os.WriteFile(filepath.Join(root, ".devcontainer", "Dockerfile"), []byte("FROM mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid configured domain", config: "[sandbox]\nallowed_domains = [\"not a domain\"]\n", errHas: "domain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			if tc.config != "" {
				sandboxWriteConfig(t, h.Getenv("LYNA_TMUX_HOME"), tc.config)
			}
			if tc.before != nil {
				tc.before(t, root)
			}
			written, err := DevcontainerInit(h, filepath.Join(root, "services", "web"), tc.force)
			if tc.wantErr != nil || tc.errHas != "" {
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{
				filepath.Join(root, ".devcontainer", "Dockerfile"),
				filepath.Join(root, ".devcontainer", "allowed-domains"),
				filepath.Join(root, ".devcontainer", "devcontainer.json"),
				filepath.Join(root, ".devcontainer", "init-firewall.sh"),
			}
			if !slices.Equal(written, want) {
				t.Fatalf("written %q, want %q", written, want)
			}
			domains, err := os.ReadFile(want[1])
			if err != nil {
				t.Fatal(err)
			}
			if tc.config != "" && !strings.Contains(string(domains), "pkg.internal.example\n") {
				t.Fatalf("configured domain missing:\n%s", domains)
			}
			js, err := os.ReadFile(want[2])
			if err != nil || !strings.Contains(string(js), "source=lyna-tmux-claude-my-app,") {
				t.Fatalf("devcontainer.json does not name the project: %v\n%s", err, js)
			}
		})
	}
}

func TestDevcontainerTarget(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, root string)
		needFiles bool
		wantErr   error
		errHas    string
	}{
		{name: "without files when none are needed"},
		{name: "files needed but missing", needFiles: true, wantErr: ErrDevcontainerMissing, errHas: "lyna-tmux sandbox devcontainer init"},
		{name: "files present", needFiles: true, setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, ".devcontainer"))
			if err := os.WriteFile(filepath.Join(root, ".devcontainer", "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Dockerfile is a directory", needFiles: true, setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, ".devcontainer", "Dockerfile"))
		}, wantErr: ErrDevcontainerMissing, errHas: "not a regular file"},
		{name: "Dockerfile is a link", needFiles: true, setup: func(t *testing.T, root string) {
			mkdir(t, filepath.Join(root, ".devcontainer"))
			if err := os.Symlink("/etc/hosts", filepath.Join(root, ".devcontainer", "Dockerfile")); err != nil {
				t.Fatal(err)
			}
		}, wantErr: ErrDevcontainerMissing, errHas: "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := isolationHost(t, nil)
			root := devcontainerProject(t, h)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			got, err := DevcontainerTarget(filepath.Join(root, "services"), tc.needFiles)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.wantErr, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := devcontainer.Target{Dir: root, Project: "my-app", User: devcontainer.DefaultUser}
			if got != want {
				t.Fatalf("target %+v, want %+v", got, want)
			}
		})
	}
	if _, err := DevcontainerTarget("/nonexistent/lyna-tmux-test", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
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
			if got.Target != (devcontainer.Target{Dir: root, Project: "my-app", User: devcontainer.DefaultUser}) || !slices.Equal(got.Args, tc.wantArgs) {
				t.Fatalf("got %+v\nwant args %q", got, tc.wantArgs)
			}
		})
	}
}
