# Security policy

## Supported versions

Security fixes land on `main` and ship in the next patch release of the latest minor version. Older releases do not receive backports; upgrade to the latest release.

| Version | Supported |
| --- | --- |
| Latest release (`v1.x`) | Yes |
| Earlier releases | No |

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue, pull request or discussion for a suspected vulnerability.

Include what you can of the following:

- the lyna-tmux version (`lyna-tmux version`), operating system, tmux version and Claude Code version;
- the isolation level in use (`sandbox.profile`, `sandbox.isolation`);
- steps to reproduce, and the impact you observed or expect;
- whether the issue is already public anywhere.

The maintainers aim to acknowledge a report within three business days and to agree on a disclosure date with the reporter. Fixes are released first, then published as a GitHub security advisory that credits the reporter unless they ask otherwise.

## Scope

In scope:

- the `lyna-tmux` binary and everything it generates: tmux configuration, per-launch Claude Code settings, hook and status line handlers, the tmux plugin entry point (`lyna-tmux.tmux`);
- escaping of text that reaches tmux or a terminal (pane titles, branch names, paths, agent output);
- the sandbox profiles and the dev container files (`lyna-tmux sandbox devcontainer init`), including the egress firewall;
- `scripts/install.sh` and the release artifacts.

Out of scope: vulnerabilities in tmux, Claude Code, Docker or the operating system sandbox themselves (report those upstream), and attacks that require an attacker who already controls the user's account.

## Security model

- lyna-tmux runs its workspaces on a dedicated tmux server and never reads or writes the user's own tmux configuration or Claude Code settings. Per-launch settings are written with mode 0600 in 0700 directories, atomically, and symlinks are refused.
- Processes are started with argument vectors, never through a shell with interpolated data. Text shown in a terminal is sanitized first.
- Hooks read at most 1 MiB of standard input, never execute it and always exit 0.
- `bypassPermissions` is refused unless the sandbox profile is `strict` or isolation is `container`.
- The dev container runs Claude Code as a non-root user, never mounts the Docker socket, keeps the Claude configuration in a per-project named volume and starts with a default-deny egress firewall. The firewall allows DNS to the configured resolvers and the addresses of a root-owned domain allowlist, verifies itself at start and leaves egress blocked if any step fails. Addresses are resolved when the container starts; rerun the firewall (`lyna-tmux sandbox devcontainer up`) when an allowed service moves.
- `lyna-tmux doctor` only reports: it never installs packages or changes system settings.

## Verifying a release

Every release publishes `checksums.txt`, an SPDX SBOM per archive and a build provenance attestation for the archives, packages and checksum file.

```sh
# Integrity: compare the download with the published checksums.
sha256sum --ignore-missing -c checksums.txt

# Provenance: prove the archive was built by this repository's release workflow.
gh attestation verify lyna-tmux_1.0.0_linux_amd64.tar.gz --repo bayoudhdev/lyna-claude-tmux
```

`scripts/install.sh` downloads over HTTPS only (TLS 1.2 or newer, redirects included), verifies the SHA-256 checksum before installing and never uses `sudo`.
