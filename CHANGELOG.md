# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] - 2026-09-20

### Added

- Every teammate of an agent team opens as a pane of the workspace: labeled with its name, drawn with the workspace's own border and state, placed by a pane policy, and listed on the agents rail. `claude.teammate_mode` chooses this or one of the ways Claude Code opens teammates itself. A team never fails to start over this: if the launcher cannot be written or the pane cannot be taken over, the agent runs exactly where Claude Code put it and the reason goes to the diagnostic log.
- The agents rail, a pane listing every agent the workspace can see, grouped into the lead, its teammates, the subagents they run and the agents of other workspaces on the server, each with its state, what it is running and the task it holds. It opens with the first teammate and closes with the last (`ui.agents_sidebar`), or with `Alt+A` and `A` after the prefix. Rows are redrawn on the hooks the agents fire, with a reading every ten seconds for what no hook reports, so an idle rail costs nothing.
- `lmux spawn`, and `s` on the rail, start an agent from the workspace: the agent definitions the project and your own configuration offer, a model, an effort, a worktree of its own or the project directory, and a prompt. It either asks the lead, typing the exact sentence it showed you into the lead's pane, or opens a session of its own in a new window.
- The `team` layout, the agents rail beside the lead with the room its teammates open into, which `lmux team` now opens by default.
- `lmux message` and `lmux stop`, and `m` and `x` on the rail, steer a team without typing at the lead: a message is shown exactly as it will be typed before it is sent, and a teammate is stopped by asking its lead or, after its name is typed out, by closing its pane. Each checks the agent again when the form opens and again before it acts, so a pane that changed hands in between is left alone.
- `lmux transcript`, and `r` on the rail, follow what an agent is writing, a subagent of a pane included, and `lmux tasks`, `t` on the rail, shows the shared task list with what each task is blocked by. The footer of the rail counts the list and what the agents of the workspace have spent between them, read from the transcripts Claude Code writes.
- `workspace.agent_panes` (how many teammates share the lead's window before the next one opens as a window of its own), `ui.agents_sidebar` (when the rail is on screen, `off` installing no key for it at all), `ui.sidebar_width` (how wide the rail opens, 20 to 60 cells) and `claude.agent_worktree` (the answer the spawn form starts the worktree question on).
- `lmux doctor` reports how teammates open: whether teams are on, whether the workspace opens them in panes of its own, and how the last teammate actually opened, read from the line the launcher wrote for it.
- [docs/agents.md](docs/agents.md), how a team runs in the workspace, with every key and every configuration key.

### Fixed

- Text typed into an agent's pane is folded onto one line with every control sequence removed. A prompt carrying the end of a bracketed paste would have had the rest of it read as key presses.
- The configuration reference documented neither `claude.teammate_mode` nor `workspace.agent_panes`, and the template it printed was no longer the file `lmux config init` writes. Tests now hold the reference to the configuration: a row for every key the file takes, every accepted value named, and the template byte for byte.
- A window that still holds the rail or another teammate is arranged for the agents that stay when one of them moves to a window of its own. It was given back the arrangement it had with no agent in it, which no longer fits its panes, so the window kept the agent's own layout and the teammate was reported as unplaced although its pane had moved.
- The teammate launcher hands lyna-tmux the pane's server in a variable of its own and starts it with `TMUX` cleared, putting it back for the agent. A terminal library reads `TMUX` while the process starts and asks that server what colors it supports, which on a team Claude Code opened on a server of its own was a command sent to a server lyna-tmux did not create.

## [1.0.1] - 2026-09-17

### Added

- Every workspace action has a binding after the prefix as well as under Alt, so a terminal that does not send Option as Meta still reaches all of them. `lmux keys` prints both columns.
- `create` builds and starts the dev container a workspace needs when its isolation is `container`, asking first on a terminal and taking `--start-container` without one.
- `doctor --fix` carries out the fixes lyna-tmux can apply itself, one at a time on a terminal, with `--yes` and `--dry-run` for scripts. What it cannot touch is listed instead, with the exact command or setting.
- `setup` carries out what the settings it just saved need: the dev container, shell completions and the review plugin, with `--yes` for non-interactive use.
- `doctor` reports whether `Shift+Enter` adds a line to the agent's prompt in this terminal, with the step that terminal needs: nothing for the ones that report the key themselves, `/terminal-setup` in Claude Code for iTerm2 and the VS Code terminal, and `Option+Enter` for a terminal that cannot send the key at all.
- The live changes view is a list you move through: arrows, `j`, `k` and the wheel move the cursor, a click selects, `Enter` or a double click reviews that file, `o` reviews the whole working tree, and the keys work in a pane of a layout as well as in the popup.

### Changed

- The command is `lmux`. It is shorter to type and it is what every message, every help text and every example now uses. `lyna-tmux` keeps working: the release archives, the install script, the Homebrew cask, the Linux packages and the dev container image all put it beside the binary as a link to it, and the tmux plugin and the Claude Code plugin hooks look the old name up when the new one is not on PATH. Nothing moves on disk: the configuration, data, state and cache directories, the socket name and the `LYNA_TMUX_*` variables are unchanged. A `.devcontainer` directory generated before this release stages a binary under the old name and its image no longer builds; regenerate it with `lmux sandbox devcontainer init --force`.
- Desktop notifications and the progress bar of the agent reach the terminal again: the pane running the agent allows tmux passthrough, and the server keeps it off everywhere else, so a pane running your own programs still cannot drive your terminal or your clipboard. `ui.allow_passthrough` still decides the rest of the server.
- A workspace tells the agent how to alert you, from the terminal it was started in: from inside a pane the only terminal the agent can see is tmux, which is why it fell back to nothing. iTerm2 gets a notification and the bell, kitty gets its own notification, every other terminal gets the bell that the status line, the window tab and the pane border already show.
- The changes view reads the working tree when something changed instead of every three seconds. A reading still runs when nothing else has for fifteen seconds, which catches an edit that produces neither a file event nor a hook signal.

### Fixed

- A pane whose program fails stays on screen with the reason. Only the agent's pane did; a changes, review or command pane that could not start closed at once, so the layout looked as if the pane had never been asked for and the error went with it. The border of a dead pane reads `exited` with the status tmux reports. A shell pane is still yours and keeps tmux's behavior.
- A failed `docker build` names the image and the step that failed instead of printing the whole log.

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

[Unreleased]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/bayoudhdev/lyna-claude-tmux/releases/tag/v1.0.0
