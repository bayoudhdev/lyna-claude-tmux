# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- A demonstration film, `docs/assets/demo.mp4`: every feature, example by example, in eight minutes, with English subtitles that carry the command as it is typed and what it does. It is recorded from real terminals by `scripts/record-video.sh`, one scene per chapter under `docs/video`, and each chapter also stands alone as `docs/assets/demo-<chapter>.mp4`. The chapter list with its timestamps is in `docs/video.md`.
- `scripts/record.sh` records a film as well as a picture: `caption` writes the subtitle every frame after it carries, `chapter` opens a title card and a chapter mark, and `--srt` writes the same captions as a sidecar track. The caption is drawn in a band under the terminal, so a scene that captions anything keeps one size for the whole film.

### Fixed

- A path the screen cut short is rewritten in a recording as well: every prefix of the home directory has a rule of its own, so a truncated path no longer carries the first letters of the account that recorded it.
- `lmux doctor` no longer breaks the Option as Meta advice mid-word: the guidance names the terminal's setting, and what lyna-tmux offers instead is the line under it, which the report wraps.
- `scripts/record-docs.sh` resolves the paths it is given before it runs the recorder from the project directory, where a relative one named something else.

## [1.3.0] - 2026-09-20

### Added

- The `monokai` theme, and it is the one a workspace opens with: an acid green accent and a cyan second accent on charcoal, orange while an agent works, magenta when it needs you. Every surface follows it, since none of them holds a color of its own: the status bar, the pane borders, the menus and popups, the agents rail, the session picker, the git workstation and its forms, the sandbox views and the wizard. A configuration that names a theme keeps it, and the other thirteen are unchanged.
- The status bar is drawn in segments: the brand block hands the workspace name to the window list, the active tab is a block of its own, and the right half is one raised segment carrying the agents, the sandbox and the branch, closed by the clock in the accent. The active pane wears its label the same way.
- `ui.status_style` says how one segment is joined to the next: `powerline` ends each with a pointed separator, `plain` lets a background end where the next starts, and `auto`, the default, takes `powerline` only with `icons = "nerd"`, which already asks for the patched font those glyphs come from. `lmux setup` offers the three, and `lmux doctor` reports the one in use and what to change when a terminal draws a box where a separator should be.
- The review editor is colored from the workspace palette: the chrome, the diff groups, the syntax groups and the capture groups nvim-treesitter sets on a parsed buffer, each from the palette slot that already means the same thing. The parser is started where Neovim has one for the language, and the syntax rules color the buffer where it has not.

### Changed

- The pictures in the documentation are recorded with a patched font, so they show the icon set and the pointed separators a terminal with one draws. The recorder downloads the font it draws with, pinned to a release tag and a checksum.

## [1.2.0] - 2026-09-20

### Added

- The git workstation, `lmux git`, `Alt+G` or `G` after the prefix: the branches, the branches of the remotes, the worktrees, the stashes and the tags on the left, the history with its lanes in the middle, and the commit or the working tree on the right. It follows the workspace it runs in, so a file the agent writes and a commit it makes are on screen without a key being pressed.
- Every operation of a repository on a key, on whatever the cursor is on: stage, unstage and discard, commit, amend and reword, branch, check out, rename, delete, merge, rebase, cherry pick, revert, move a branch, tag, stash, open and remove a worktree, write a patch, copy an object name, and the answers to a rebase that stopped on a conflict. What reaches a remote is a capital: fetch and prune, pull, push, push over what the remote holds, push and follow.
- A form before anything that cannot be undone by pressing the same key again. It says what the operation costs and shows the command it runs, built by the same code that runs it rather than written out beside it, so what you agreed to is what happens. What would lose commits asks for the name of the branch typed back.
- The `git` layout, the agent with the workstation beside it, and a pane role of its own with a border that reads the branch icon.
- `git branch --merged` behind the form that deletes a branch, so it says that the commits are in the branch it was merged into when they are, and asks for the name back when they are not.
- `lmux doctor` reports the git of the project: the version, whether the directory is a worktree of a repository and which one, what git is in the middle of, the worktrees that are open and where HEAD stands.
- Benchmarks of the workstation: one redraw and one reading over a page of history, in [docs/benchmarks.md](docs/benchmarks.md).

### Changed

- A push over a remote holds its lease to the commit the workstation last read, so a commit pushed by someone else in between stops it instead of being written over.
- `workspace.split_ratio` now decides the width of the pane beside the agent in the `duo`, `trio` and `git` layouts alike.

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
- `lmux review` reports a download stopped part way as the cancelation it was, instead of as a file whose checksum does not match the pinned release. A server that stops writing when it sees the request canceled ends the body cleanly, so the bytes that did arrive were read as the whole file.

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

[Unreleased]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/bayoudhdev/lyna-claude-tmux/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/bayoudhdev/lyna-claude-tmux/releases/tag/v1.0.0
