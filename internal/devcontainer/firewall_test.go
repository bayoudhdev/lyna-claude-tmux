package devcontainer

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bayoudhdev/lyna-claude-tmux/internal/testutil/golden"
)

// Stub programs for init-firewall.sh. Each appends its argv to $STUB_LOG, so
// the golden log is the exact sequence of firewall commands the script runs.
var firewallStubs = map[string]string{
	"id": `echo "${STUB_UID:-0}"`,
	"iptables": `echo "iptables $*" >> "$STUB_LOG"
exit 0`,
	"ip6tables": `echo "ip6tables $*" >> "$STUB_LOG"
if [ "$1" = "-S" ] && [ "${STUB_IPV6:-1}" = 0 ]; then exit 1; fi
exit 0`,
	"ipset": `echo "ipset $*" >> "$STUB_LOG"
exit 0`,
	// getent ahostsv4 NAME prints "ADDR STREAM NAME" lines from $STUB_HOSTS
	// ("name addr" per line) and exits 2 when the name is unknown, like glibc.
	"getent": `found=0
while read -r name addr; do
  if [ "$name" = "$2" ]; then echo "$addr STREAM $2"; echo "$addr DGRAM"; found=1; fi
done < "$STUB_HOSTS"
[ "$found" = 1 ] || exit 2`,
	// curl succeeds only for hosts listed in $STUB_REACHABLE.
	"curl": `echo "curl $*" >> "$STUB_LOG"
for arg in "$@"; do url=$arg; done
host=${url#https://}
if grep -qxF "$host" "$STUB_REACHABLE"; then exit 0; fi
exit 7`,
}

const (
	defaultAllowlist = "# comment\n\napi.anthropic.com\n  github.com  # trailing comment\n"
	defaultHosts     = "api.anthropic.com 160.79.104.10\ngithub.com 140.82.121.3\ngithub.com 140.82.121.4\n"
	defaultResolv    = "# generated\nnameserver 192.168.65.7\nnameserver fd00::1\nsearch lan\nnameserver 127.0.0.11"
)

func TestFirewallScript(t *testing.T) {
	bash := requireBash(t)
	cases := []struct {
		name      string
		allowlist string
		hosts     string
		reachable string
		env       []string
		args      []string
		wantExit  int
		wantOut   string
		wantErr   string
	}{
		{
			name:      "success",
			allowlist: defaultAllowlist, hosts: defaultHosts, reachable: "api.anthropic.com\ngithub.com\n",
			wantOut: "init-firewall: egress limited to 2 domains",
		},
		{
			name:      "success without ipv6",
			allowlist: "api.anthropic.com\n", hosts: defaultHosts, reachable: "api.anthropic.com\n",
			env:     []string{"STUB_IPV6=0"},
			wantOut: "init-firewall: self-test: api.anthropic.com is reachable",
		},
		{
			name:      "probe skips allowlisted example hosts",
			allowlist: "api.anthropic.com\nexample.com\n", hosts: defaultHosts + "example.com 93.184.215.14\n", reachable: "api.anthropic.com\nexample.com\n",
			wantOut: "init-firewall: self-test: example.net is blocked",
		},
		{
			name:      "not root changes nothing",
			allowlist: defaultAllowlist, hosts: defaultHosts, reachable: "api.anthropic.com\n",
			env:      []string{"STUB_UID=1000"},
			wantExit: 1, wantErr: "must run as root",
		},
		{
			name:      "arguments are refused before any change",
			allowlist: defaultAllowlist, hosts: defaultHosts, reachable: "api.anthropic.com\n",
			args:     []string{"evil.example"},
			wantExit: 1, wantErr: "takes no arguments",
		},
		{
			name:      "invalid domain blocks egress",
			allowlist: "api.anthropic.com\nevil.com;rm\n", hosts: defaultHosts, reachable: "api.anthropic.com\n",
			wantExit: 1, wantErr: "invalid domain in",
		},
		{
			name:      "numeric domain blocks egress",
			allowlist: "10.0.0.1\n", hosts: defaultHosts, reachable: "",
			wantExit: 1, wantErr: "invalid domain in",
		},
		{
			name:      "empty allowlist blocks egress",
			allowlist: "# nothing\n", hosts: defaultHosts, reachable: "",
			wantExit: 1, wantErr: "names no domains",
		},
		{
			name:      "unresolvable domain blocks egress",
			allowlist: "api.anthropic.com\nmissing.example\n", hosts: defaultHosts, reachable: "api.anthropic.com\n",
			wantExit: 1, wantErr: "cannot resolve missing.example",
		},
		{
			name:      "reachable probe fails the self-test",
			allowlist: defaultAllowlist, hosts: defaultHosts, reachable: "api.anthropic.com\nexample.com\n",
			wantExit: 1, wantErr: "self-test failed: example.com is outside the allowlist but reachable",
		},
		{
			name:      "unreachable allowed domain fails the self-test",
			allowlist: defaultAllowlist, hosts: defaultHosts, reachable: "",
			wantExit: 1, wantErr: "self-test failed: allowed domain api.anthropic.com is unreachable",
		},
		{
			name:      "every probe host allowed",
			allowlist: "example.com\nexample.net\nexample.org\n", hosts: "example.com 1.1.1.1\nexample.net 1.1.1.2\nexample.org 1.1.1.3\n", reachable: "example.com\n",
			wantExit: 1, wantErr: "self-test needs a host outside the allowlist",
		},
	}
	// The stubs are shared: they read their data from per-case files named in
	// the environment. Writing them once also avoids paying the host's first
	// execution check of a new executable in every case.
	stubDir := t.TempDir()
	for name, body := range firewallStubs {
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			files := map[string]string{"allowlist": tc.allowlist, "hosts": tc.hosts, "reachable": tc.reachable, "resolv.conf": defaultResolv, "log": ""}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, append([]string{filepath.Join("templates", "init-firewall.sh")}, tc.args...)...)
			cmd.Env = append([]string{
				"PATH=" + stubDir + ":/usr/bin:/bin",
				"LYNA_TMUX_FIREWALL_ALLOWLIST=" + filepath.Join(dir, "allowlist"),
				"LYNA_TMUX_FIREWALL_RESOLV=" + filepath.Join(dir, "resolv.conf"),
				"STUB_LOG=" + filepath.Join(dir, "log"),
				"STUB_HOSTS=" + filepath.Join(dir, "hosts"),
				"STUB_REACHABLE=" + filepath.Join(dir, "reachable"),
			}, tc.env...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exit := 0
			if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != tc.wantExit {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantOut) || !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stdout %q should contain %q; stderr %q should contain %q", stdout.String(), tc.wantOut, stderr.String(), tc.wantErr)
			}
			log, err := os.ReadFile(filepath.Join(dir, "log"))
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "firewall/"+strings.ReplaceAll(tc.name, " ", "-")+".log", log)
		})
	}
}

// TestFirewallScriptSyntax parses the script with the system bash, which is
// 3.2 on macOS: the script must not depend on newer bash features so the
// behavioral test above runs everywhere.
func TestFirewallScriptSyntax(t *testing.T) {
	bash := requireBash(t)
	if out, err := exec.Command(bash, "-n", filepath.Join("templates", "init-firewall.sh")).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func requireBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("init-firewall.sh runs in a Linux container; needs a POSIX host to test")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	return bash
}
