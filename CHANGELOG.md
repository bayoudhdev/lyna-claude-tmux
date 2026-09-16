# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [1.0.0] - 2026-09-16

### Added

- `lyna-tmux` CLI that starts Claude Code workspaces on a dedicated tmux server with styled split layouts, per-launch Claude Code settings (sandbox, hooks, status line) and no changes to the user's own tmux or Claude Code configuration.
- Plugin mode for an existing tmux setup, loaded through the `lyna-tmux.tmux` entry point for tpm; it applies the installed binary's configuration and shows install guidance when the binary is missing.
- `lyna-tmux doctor` with plain text and JSON reports: tmux, Claude Code, git, sandbox prerequisites per platform, truecolor, clipboard, Option as Meta, Claude keybindings that the workspace keys would shadow, Docker and Neovim, each with the exact fix to apply.
- Container isolation through `lyna-tmux sandbox devcontainer init|up|shell|down`: a Debian slim image with a non-root user, the Claude configuration in a per-project volume, no Docker socket and a default-deny egress firewall with a domain allowlist.
- `scripts/install.sh`, an HTTPS-only installer that verifies release checksums and installs without `sudo`.
- Release archives for Linux and macOS (amd64, arm64) with shell completions and a man page, deb, rpm and apk packages, SPDX SBOMs, checksums and build provenance attestations.
- `scripts/bench.sh` start-up benchmarks.

[Unreleased]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/bayoudhdev/lyna-claude-tmux/releases/tag/v1.0.0
