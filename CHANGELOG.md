# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [1.0.0] - 2026-09-17

### Added

- `lyna-tmux` CLI that starts Claude Code workspaces on a dedicated tmux server with styled split layouts, per-launch Claude Code settings (sandbox, hooks, status line) and no changes to the user's own tmux or Claude Code configuration.
- Layouts `solo`, `duo`, `trio`, `quad` and `auto`, plus custom layouts defined pane by pane in the configuration file, with a mouse-first status bar, right-click menus for panes, window tabs and the session, and Alt keys chosen so they never collide with the keys Claude Code reserves.
- Live agent state in the status bar and on every window tab, fed by asynchronous Claude Code hooks, with the bell of an agent forwarded to the terminal its pane belongs to.
- `lyna-tmux agents`, a picker over every Claude on the machine (workspace panes, sessions started elsewhere and background jobs) with a live preview of the pane, jump, attach, and a stop that re-verifies the process before it signals anything.
- `lyna-tmux review`, a side-by-side diff of the working tree, the index, a revision range or a pull request, running codediff.nvim at a pinned commit whose files are verified against the checksums of its release, in a Neovim that reads none of the user's own configuration.
- The live changes pane of the `trio` layout: the files an agent touches, with the lines added and removed, refreshed as they change.
- `lyna-tmux task` for a window in its own git worktree, `lyna-tmux team` for agent teams as tmux panes, and `lyna-tmux resume` to continue a past conversation inside the workspace.
- The dashboard (`lyna-tmux` with no command), the `lyna-tmux setup` wizard, `lyna-tmux config init|path|edit|validate`, `lyna-tmux keys` and `lyna-tmux theme`, including a Claude Code theme written from the workspace palette.
- Sandbox profiles `standard`, `strict` and `off`, and isolation levels `bash`, `process` and `container`, with the credential files, the token variables, the egress allowlist and the writable paths of a launch derived from the profile and the project's own ecosystems.
- Plugin mode for an existing tmux setup, loaded through the `lyna-tmux.tmux` entry point for tpm; it applies the installed binary's configuration and shows install guidance when the binary is missing, and reads the `@claude_*` options of that server, the command and the arguments a popup runs included.
- `lyna-tmux doctor` with plain text and JSON reports: tmux, Claude Code, git, sandbox prerequisites per platform, truecolor, clipboard, Option as Meta, Claude keybindings that the workspace keys would shadow, Docker and Neovim, each with the exact fix to apply.
- Container isolation through `lyna-tmux sandbox devcontainer init|up|shell|down`: a Debian slim image with a non-root user, the Claude configuration in a per-project volume, no Docker socket and a default-deny egress firewall with a domain allowlist.
- `scripts/install.sh`, an HTTPS-only installer that verifies release checksums and installs without `sudo`.
- Release archives for Linux and macOS (amd64, arm64) with shell completions and a man page, deb, rpm and apk packages, SPDX SBOMs, checksums and build provenance attestations.
- `scripts/bench.sh` start-up benchmarks.

[Unreleased]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/bayoudhdev/lyna-claude-tmux/releases/tag/v1.0.0
