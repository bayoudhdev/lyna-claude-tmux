# Configuration

`lyna-tmux` reads one TOML file, `config.toml`. It is optional: with no file at all, every
value below falls back to its built-in default.

There is deliberately no per-project configuration file. A repository cannot change how
`lyna-tmux` launches Claude Code. What a repository can carry is the settings Claude Code
itself honors in a project: `lmux init --project` adds the sandbox part of
`.claude/settings.json`, the `.worktreeinclude` patterns for local environment files, and
the `.gitignore` entries for worktrees and personal settings. Existing files are merged,
never replaced, and every change is shown and confirmed unless `--yes` is passed. See
[sandbox.md](sandbox.md).

## Where the file lives

The location follows the XDG base directory variables on every platform, so one dotfiles
layout works everywhere.

| Environment | Configuration file |
| --- | --- |
| `LYNA_TMUX_HOME` set to an absolute path | `$LYNA_TMUX_HOME/config/config.toml` |
| `XDG_CONFIG_HOME` set to an absolute path | `$XDG_CONFIG_HOME/lyna-tmux/config.toml` |
| neither | `~/.config/lyna-tmux/config.toml` |

Rules that apply to that lookup:

- `LYNA_TMUX_HOME` overrides all four roots (config, data, state, cache) at once, which is
  how test runs and demo recordings stay away from your own files.
- A relative value in `LYNA_TMUX_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`
  or `XDG_CACHE_HOME` is ignored, as the specification requires.
- The file may be a symbolic link, so it can live in a dotfiles repository. `lyna-tmux` reads
  through the link but never replaces it: `config init` and `setup` refuse a symlink and tell
  you to edit the file it points to. `config edit` opens it as usual.
- Directories are created with mode `0700` and files with mode `0600`.
- At most 1 MiB of the file is read.

Print the path without guessing:

```sh
lmux config path
```

### The other directories lyna-tmux owns

| Path | Contents |
| --- | --- |
| `<config>/config.toml` | this file |
| `<config>/tmux.local.conf` | your own tmux settings, sourced last so they win |
| `<state>/tmux.conf` | the generated tmux configuration, rewritten by `lyna-tmux`, never edited by hand |
| `<state>/settings/` | the per-launch Claude Code settings files |
| `<state>/lyna-tmux.log` | the size-capped diagnostic log |
| `<data>/review/` | the pinned review plugin and its generated init file |
| `<cache>/agents.json` | the last agent snapshot, used to paint the picker instantly |

`<config>`, `<state>`, `<data>` and `<cache>` are `$LYNA_TMUX_HOME/{config,state,data,cache}`
when `LYNA_TMUX_HOME` is set, otherwise the `lyna-tmux` directory under `XDG_CONFIG_HOME`,
`XDG_STATE_HOME` (`~/.local/state`), `XDG_DATA_HOME` (`~/.local/share`) and `XDG_CACHE_HOME`
(`~/.cache`).

## Working with the file

| Command | What it does |
| --- | --- |
| `lmux config path` | prints the configuration file path |
| `lmux config init` | writes the documented template with the defaults |
| `lmux config init --force` | replaces an existing file, keeping a `.bak` copy |
| `lmux config show` | prints the effective configuration: the file merged over the defaults |
| `lmux config validate` | checks the file and reports every problem |
| `lmux config edit` | opens the file in your editor, then checks it |

Details worth knowing:

- `config init` keeps an existing file and fails unless `--force` is passed. With `--force`
  the old file is first copied next to it as `config.toml.bak`.
- `config edit` opens `$VISUAL`, then `$EDITOR`, then `vi`. A missing file is created from
  the template first. The file is checked when the editor exits, and changes apply the next
  time a `lyna-tmux` command runs.
- `config show` prints the merged result, so it is the fastest way to see which default a
  key currently has. It is encoded output: comments are not part of it.
- `lmux setup` also writes this file, from its own questions. It keeps a `.bak` copy of
  the previous file, and it writes encoded TOML, so comments in the replaced file are lost.
  It then offers to carry out what the answers need, one question at a time, so the next
  command works instead of failing on a choice that was only written down: building and
  starting the project's dev container at container isolation, writing the completion script
  of your login shell, installing the review plugin. The sandbox runtime of process isolation
  is never installed for you; setup prints the command. `--yes` accepts every one of these
  questions. Your shell startup file is never edited: when zsh needs an `fpath` line, setup
  prints it.
- `lmux theme <name>` writes only `ui.theme`, in place: comments, ordering and spacing
  stay exactly as you wrote them.

## How the file is read

The document is decoded on top of the defaults:

- A missing key keeps its default value. A missing table is the same as an empty one.
- **An unknown key is an error.** A typo never passes silently.
- Every syntax problem and every invalid value is reported at once, with its line or key.

```console
$ lmux config validate
ERROR  /home/you/.config/lyna-tmux/config.toml: invalid config: line 3: ui.themes: unknown key.

$ lmux config validate
ERROR  /home/you/.config/lyna-tmux/config.toml: invalid config:
workspace.layout: must be one of solo, duo, trio, quad, review, auto or a [layouts.<name>] table (got "nope");
workspace.split_ratio: must be between 20 and 80 (got 95).
```

Limits that apply to every list key (`claude.args`, `claude.add_dirs`, `claude.mcp_config`,
`claude.plugin_dirs`, `sandbox.allow_write`, `sandbox.deny_read`, `sandbox.allowed_domains`,
`sandbox.excluded_commands`): at most 256 entries, each entry non-empty, at most 4096 bytes,
valid UTF-8 and free of control characters, so the value can reach tmux, argv and JSON
unchanged.

## `[ui]`

Look and input model of the workspace.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `theme` | string | `lyna`, `slate`, `dusk`, `contrast`, `nord`, `rose`, `mono`, `solar-dark`, `earth-dark`, `light`, `solar-light`, `earth-light`, `ansi` | `"lyna"` | Palette of the status line, pane borders, menus and popups. The first nine are dark, the next three light, and `ansi` uses the terminal's own palette. `lmux theme` lists them with swatches and `lmux theme preview [name]` draws a workspace in each one. |
| `icons` | string | `auto`, `unicode`, `nerd`, `ascii` | `"auto"` | Glyph set in the status line, borders and pickers. `auto` picks `unicode` when the effective locale (`LC_ALL`, then `LC_CTYPE`, then `LANG`) is UTF-8, and `ascii` otherwise, including under the East Asian locales (`ja`, `zh`, `ko`), where terminals draw the ambiguous width glyphs two cells wide and every segment would sit one cell further right than the layout expects. Set `icons = "unicode"` to use them anyway. `nerd` needs a patched font and is never chosen automatically. |
| `color` | string | `auto`, `truecolor`, `256`, `16` | `"auto"` | Color depth the theme is rendered at. `auto` reads `COLORTERM=truecolor` or `24bit` as 24-bit, a `TERM` containing `256color` as 256 colors, `xterm-direct` and `tmux-direct` as 24-bit, anything else as the basic 16. |
| `status_position` | string | `top`, `bottom` | `"bottom"` | Which edge the status line sits on. |
| `clock` | boolean | `true`, `false` | `true` | Draws the `%H:%M` clock at the right end of the status line. |
| `alt_keys` | boolean | `true`, `false` | `true` | Installs the prefix-free Alt bindings. See [keys.md](keys.md). |
| `mouse` | boolean | `true`, `false` | `true` | Turns the tmux `mouse` option on or off. With it off, clicks, menus and wheel scrolling never reach tmux. |
| `allow_passthrough` | boolean | `true`, `false` | `false` | Lets programs in panes send escape sequences straight to your terminal. Off by default: pane output cannot drive your terminal or clipboard. The pane running the agent always allows them, whatever this says, since that is where its desktop notifications and its progress bar come from. |
| `focus_events` | boolean | `true`, `false` | `false` | Tells a pane when it gains or loses focus. Off by default, as in tmux itself: a program that asks for focus events without reading them prints each one as typed text, and the agent client is one of them. Turn it on for an editor that reloads a file when its pane regains focus. |

There is no `[keys]` table. Key bindings are configured by `ui.alt_keys` and
`workspace.prefix` only; anything else under a `keys` name is rejected as an unknown key.

The clipboard is never configurable from here: the generated tmux configuration always sets
`set-clipboard external`, so only copy mode writes the system clipboard and a program in a
pane cannot.

## `[workspace]`

Sessions and panes.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `layout` | string | `solo`, `duo`, `trio`, `quad`, `review`, `auto`, or the name of a `[layouts.<name>]` table | `"auto"` | Layout of the window a workspace opens with. `auto` resolves by client size: 200 by 40 cells or larger gives `trio`, 120 cells or wider gives `duo`, smaller gives `solo`, and an unknown size gives `duo`. |
| `split_ratio` | integer | 20 to 80 | `62` | The Claude pane's share of the window width, in percent, in the built-in layouts that split it (`duo` and `trio`). `quad` and `review` split evenly. |
| `history_limit` | integer | 1000 to 2000000 | `100000` | Scrollback lines kept per pane (tmux `history-limit`). |
| `prefix` | string | a tmux key name | `"C-b"` | The tmux prefix key. Press it twice to send it to the program in the pane. The generated configuration sets no second prefix. |
| `shell` | string | absolute path, or `""` | `""` | Shell started in shell panes. Empty keeps tmux's choice, which is `$SHELL`. |

A tmux key name is up to three of the `C-`, `M-` and `S-` modifiers followed by one printable
ASCII character or one of `Space`, `Tab`, `Enter`, `Escape`, `BSpace`, `Home`, `End`,
`PageUp`, `PageDown`, `F1` to `F12`. `C-a` and `C-Space` are valid prefixes.

## `[claude]`

How Claude Code is launched in managed panes.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `command` | string | a command name on `PATH`, or an absolute path, or `""` | `""` | The executable to run. Empty, or `claude`, looks the command up. A relative path or a path with a `~` is rejected. |
| `args` | list of strings | any single-line values | none | Extra arguments added to every launch, after the flags `lyna-tmux` manages and before arguments you pass after `--`. |
| `model` | string | a model alias or id | `""` | Passed as `--model=`. Empty uses your Claude Code default. |
| `effort` | string | `low`, `medium`, `high`, `xhigh`, `max`, `ultracode`, or `""` | `""` | Passed as `--effort=`. Empty leaves the choice to Claude Code. |
| `permission_mode` | string | `default`, `manual`, `acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`, or `""` | `""` | Passed as `--permission-mode=`. `bypassPermissions` is refused unless the launch uses the `strict` profile or `container` isolation. |
| `statusline` | string | `auto`, `lyna`, `off` | `"auto"` | Whether the `lyna-tmux` status line goes into the per-launch settings. `auto` installs it only when your own Claude Code settings define none. |
| `fullscreen` | boolean | `true`, `false` | `false` | Starts the pane with the flicker-free fullscreen renderer, which has mouse wheel scrolling. |
| `teams` | boolean | `true`, `false` | `false` | Turns agent teams on, with teammates opened as tmux panes. |
| `worktree_base` | string | `fresh`, `head`, or `""` | `""` | Written as `worktree.baseRef` in the per-launch settings: `fresh` bases new worktrees on the origin default branch, `head` on the current `HEAD`. Empty leaves the setting out. |
| `workflow_size` | string | `small`, `medium`, `large`, `unrestricted`, or `""` | `""` | Size guideline written into the per-launch settings for dynamic workflows. Empty leaves it out. |
| `bell` | boolean | `true`, `false` | `true` | Rings the terminal bell when Claude finishes or needs you, and turns tmux `monitor-bell` on so the window tab marks it. |
| `add_dirs` | list of paths | absolute, or starting with `~/` | none | One `--add-dir=` per entry, with `~/` expanded. |
| `mcp_config` | list of paths | absolute, or starting with `~/` | none | One `--mcp-config=` per entry, with `~/` expanded. |
| `plugin_dirs` | list of paths | absolute, or starting with `~/` | none | One `--plugin-dir=` per entry, with `~/` expanded. |

## `[sandbox]`

Which sandbox every launch gets, and what you add to it. What each profile and isolation
level actually protects is in [sandbox.md](sandbox.md); `lmux sandbox profiles` prints
the same facts from the binary.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `profile` | string | `standard`, `strict`, `off` | `"standard"` | The sandbox profile of every launch. `off` in this file is **not honored**: a launch without a sandbox has to be asked for on the command line with `--sandbox off`. |
| `isolation` | string | `bash`, `process`, `container` | `"bash"` | What runs inside the sandbox: Bash commands, the whole Claude process, or the whole workspace in a dev container. |
| `allow_write` | list of paths | absolute, or starting with `~/` | none | Extra directories the sandbox may write, beyond the project. |
| `deny_read` | list of paths | absolute, or starting with `~/` | none | Extra paths the sandbox may not read, beyond the profile's credential list. |
| `allowed_domains` | list of hostnames | a hostname, optionally with a leading `*.` label, at most 253 characters | none | Hosts added to the network allowlist, for example `registry.npmjs.org` or `*.internal.example.com`. |
| `excluded_commands` | list of strings | any single-line values | none | Commands that are not run through the sandbox. |

## `[popup]`

The tool popups: the agents picker, the live changes view and the sandbox status.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `width` | string | `10%` to `100%`, or a cell count from 20 to 1000 | `"90%"` | Popup width. |
| `height` | string | `10%` to `100%`, or a cell count from 20 to 1000 | `"90%"` | Popup height. |
| `session_prefix` | string | 1 to 16 characters of letters, digits, `_` or `-`, not starting with `-` | `"claude-"` | Prefix of the per-directory popup session names, which are the prefix plus an 8-character hash of the directory. |

The scratch shell popup is always 80 percent by 70 percent and the review popup always 95
percent by 95 percent; `popup.width` and `popup.height` do not change them.

## `[review]`

The live code review: a side-by-side diff explorer that refreshes as files change.

| Key | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `editor` | string | `isolated`, `user` | `"isolated"` | `isolated` runs `nvim` without your configuration, with the pinned and checksum-verified `codediff.nvim` that `lmux review install` puts in the data directory. `user` runs your own `nvim` configuration and plugin install. |
| `layout` | string | `default`, `inline`, `side-by-side` | `"default"` | How diffs are drawn. `default` leaves the choice to the plugin configuration. |

## `[layouts.<name>]`

A custom layout is a named list of panes. Use its name anywhere a layout name is accepted:
`workspace.layout`, `lmux create -l <name>`, `lmux layout <name>`.

```toml
[layouts.tests]
panes = [
  { role = "claude" },
  { role = "changes", split = "right", size = 35 },
  { role = "command", split = "down", size = 30, command = "go test ./...", parent = 2 },
]
```

Rules for the table itself:

- The name is lowercase letters, digits, `_` and `-`, up to 32 characters.
- A built-in name (`solo`, `duo`, `trio`, `quad`, `review`, `auto`) cannot be redefined.
- `panes` lists 1 to 9 panes and needs at least one `claude` pane.
- The first pane is the window itself and takes no `split`, `size` or `parent`; every later
  pane splits an earlier one.
- The window opens focused on the first `claude` pane.

Fields of one pane:

| Field | Type | Values | Default | Effect |
| --- | --- | --- | --- | --- |
| `role` | string | `claude`, `shell`, `changes`, `review`, `command` | required | What runs in the pane: Claude Code with the workspace's per-launch settings, a shell, the live changes view, the review editor, or a shell command. |
| `split` | string | `right`, `down` | required except on the first pane | Direction the pane is created in. |
| `size` | integer | 10 to 90 | omitted, which splits in half | The new pane's share of the pane it splits, in percent. |
| `parent` | integer | 1 to the index of the previous pane | omitted, which means the previous pane | The 1-based index of the earlier pane to split. |
| `command` | string | a single-line command | none | The shell command of a `command` pane. Only `command` panes take one, and they require one. |
| `worktree` | boolean | `true`, `false` | `false` | Runs a `claude` pane in its own git worktree, named `<workspace>-<pane number>` under `<repo>/.claude/worktrees/`. Only `claude` panes may set it. |

## The template

`lmux config init` writes exactly this file. It is the recommended starting point:
every key is present or shown commented out, next to what it does.

```toml
# lyna-tmux configuration
#
# Every key is optional: a missing key keeps its default value, and an
# unknown key is an error. Check this file with `lmux config validate`.

[ui]
# Color theme. Dark: lyna, slate, dusk, contrast, nord, rose, mono, solar-dark,
# earth-dark. Light: light, solar-light, earth-light. ansi uses the terminal's
# own palette. Preview them with `lmux theme preview`.
theme = "lyna"
# Icon set: auto, unicode, nerd (needs a Nerd Font), ascii.
icons = "auto"
# Color depth: auto (detected), truecolor, 256, 16.
color = "auto"
# Status bar position: top or bottom.
status_position = "bottom"
clock = true
# Prefix-free Alt (Option) key bindings such as Alt+[ and Alt+] to move between
# panes and Alt+\\ to split. Run `lmux keys` for the full list.
alt_keys = true
mouse = true
# Let programs in panes send escape sequences straight to your terminal.
# Off by default: pane output cannot drive your terminal or clipboard.
allow_passthrough = false
# Tell a pane when it gains or loses focus. Off by default: a program that asks
# for focus events without reading them shows them as typed text, and the agent
# client is one of them.
focus_events = false

[workspace]
# Default layout: solo, duo, trio, quad, review, auto (picks by terminal size), or
# the name of a [layouts.<name>] table below.
layout = "auto"
# Width of the Claude pane in percent when a layout splits it (20-80).
split_ratio = 62
history_limit = 100000
# tmux prefix key. Press it twice to send it to the program in the pane.
prefix = "C-b"
# Shell for shell panes. Empty uses $SHELL.
shell = ""

[claude]
# Claude Code executable. Empty looks up "claude" on PATH.
command = ""
# Extra arguments for every launch.
# args = ["--verbose"]
# Model alias or id. Empty uses your Claude Code default.
model = ""
# Effort: low, medium, high, xhigh, max, ultracode. Empty uses the default.
effort = ""
# Permission mode: default, manual, acceptEdits, plan, auto, dontAsk,
# bypassPermissions.
# bypassPermissions requires the strict sandbox profile or container isolation.
permission_mode = ""
# Status line: auto (keeps yours when you have one), lyna, off.
statusline = "auto"
# Flicker-free fullscreen renderer with mouse wheel scrolling.
fullscreen = false
# Agent teams, with teammates opened as tmux panes.
teams = false
# Base for new worktrees: fresh (origin default branch) or head. Empty uses the default.
worktree_base = ""
# Size guideline for dynamic workflows: small, medium, large, unrestricted.
workflow_size = ""
# Ring the terminal bell when Claude finishes or needs you.
bell = true
# add_dirs = ["~/src/shared-lib"]
# mcp_config = ["~/.config/mcp/servers.json"]
# plugin_dirs = ["~/src/my-plugins"]

[sandbox]
# Profile: standard (sandboxed Bash, credential files hidden) or strict (adds a
# network allowlist and no unsandboxed fallback). Running without a sandbox is
# only possible per launch, with `--sandbox off`.
profile = "standard"
# Isolation: bash (sandboxed Bash tool), process (whole Claude process in the
# sandbox runtime), container (dev container).
isolation = "bash"
# allow_write = ["~/.cache/go-build"]
# deny_read = ["~/Documents"]
# allowed_domains = ["api.example.com", "*.internal.example.com"]
# excluded_commands = ["docker"]

[popup]
width = "90%"
height = "90%"
# Prefix of per-directory popup session names.
session_prefix = "claude-"

[review]
# Live code review with side-by-side diffs in Neovim. isolated runs a private
# Neovim with a pinned, checksum-verified codediff.nvim (`lmux review
# install`) and never touches your Neovim setup; user runs your own Neovim and
# plugin install.
editor = "isolated"
# Diff layout: default, inline, side-by-side.
layout = "default"

# Custom layouts. The first pane is the window itself; each later pane splits
# an earlier one (parent, 1-based, defaults to the previous pane).
#
# [layouts.tests]
# panes = [
#   { role = "claude" },
#   { role = "changes", split = "right", size = 35 },
#   { role = "command", split = "down", size = 30, command = "go test ./..." },
# ]
```

## Example configurations

### A wide screen, a patched font, a locked-down sandbox

```toml
[ui]
theme = "light"
icons = "nerd"
status_position = "top"

[workspace]
layout = "trio"
split_ratio = 70

[claude]
model = "opus"
effort = "high"
bell = true

[sandbox]
profile = "strict"
allowed_domains = ["registry.npmjs.org", "*.internal.example.com"]
allow_write = ["~/.cache/go-build"]
```

### Keyboard-only, your own prefix, your own review editor

No Alt bindings, so every Alt key reaches Claude Code untouched, and a custom layout that
keeps a test command in view.

```toml
[ui]
theme = "ansi"
alt_keys = false
mouse = true

[workspace]
layout = "tests"
prefix = "C-a"
shell = "/bin/zsh"

[claude]
command = "claude"
permission_mode = "plan"
statusline = "lyna"

[review]
editor = "user"
layout = "side-by-side"

[layouts.tests]
panes = [
  { role = "claude" },
  { role = "changes", split = "right", size = 35 },
  { role = "command", split = "down", size = 30, command = "go test ./...", parent = 2 },
]
```

### Four agents in a dev container

```toml
[workspace]
layout = "quad"

[claude]
teams = true
worktree_base = "fresh"
workflow_size = "medium"
fullscreen = true
add_dirs = ["~/src/shared-lib"]

[sandbox]
isolation = "container"

[popup]
width = "80%"
height = "70%"
session_prefix = "cc-"
```

Check any of these with `lmux config validate` before relying on them.

## Precedence

A value is taken from the first source that supplies it:

1. **A command line flag.** It applies to that invocation only and is never written back.
2. **The configuration file.**
3. **The built-in default** listed in the tables above.

Flags that override a configuration key:

| Flag | Commands | Key it overrides |
| --- | --- | --- |
| `-l`, `--layout` | `create`, `team` | `workspace.layout` |
| `--model` | `create`, `team` | `claude.model` |
| `--effort` | `create`, `team` | `claude.effort` |
| `--mode` | `create`, `team` | `claude.permission_mode` |
| `--sandbox` | `create`, `team` | `sandbox.profile` |
| the `[profile]` argument | `sandbox show` | `sandbox.profile` |
| `--isolation` | `create`, `team`, `sandbox show` | `sandbox.isolation` |
| `--inline`, `--side-by-side` | `review` | `review.layout` |
| `--user-nvim` | `review` | `review.editor` |

Two cases do not follow the simple rule:

- `sandbox.profile = "off"` in the file is refused. Launching without a sandbox has to be an
  explicit request on the command line: `lmux create --sandbox off`.
- `lmux team` turns agent teams on for that launch even when `claude.teams = false`. It
  can only add teams, never take them away, so a file with `claude.teams = true` keeps them
  on for `lmux create` as well.

Per launch only, with no configuration key at all:

| Flag or argument | Effect |
| --- | --- |
| `-c`, `--continue` | continues the most recent conversation |
| `-n`, `--name` | names the workspace, instead of the project directory name |
| `-d`, `--detach` | starts the workspace without attaching |
| everything after `--` | passed straight to `claude`, after `claude.args` |
| `--pane`, `-s`/`--session` | target a pane or workspace for `layout`, `split` and `task` |

In plugin mode, where `lyna-tmux` runs inside a tmux server you started yourself, these
options set on that server are read, so a configuration written for the plugin this project
derives from keeps working unchanged:

| tmux option | What it does here |
| --- | --- |
| `@claude_launch_key` | prefix key that opens the Claude popup of the current directory |
| `@claude_list_key` | prefix key that opens the agents picker |
| `@claude_forward_bell` | forwards a bell from a popup session to the window it was started from |
| `@claude_session_prefix` | name prefix of popup sessions, over `popup.session_prefix` |
| `@claude_command` | the command a popup runs, in place of `claude.command` |
| `@claude_args` | its arguments, in place of `claude.args` |
| `@claude_popup_width`, `@claude_popup_height` | popup size, over `popup.width` and `popup.height` |

Nothing else is read from tmux options. `@claude_fzf_options` has no effect: the picker is
built into `lyna-tmux` and needs no external program to filter with.

`@claude_command` follows the rule `claude.command` does: a command name looked up on
`PATH`, or an absolute path, and nothing else. `@claude_args` is written the way it would be
for a shell, `--append-system-prompt "be brief"`, and is split into arguments without
running one, so a `$`, a backquote or a `*` stays the text you typed. Both replace their
configuration counterpart rather than adding to it, so what a popup runs is what the tmux
options say. These two are read for popups only: `lmux create` takes its command and
its arguments from the configuration file.

## When a change takes effect

| Change | How it reaches a running workspace |
| --- | --- |
| Look and key bindings (`[ui]`, `workspace.prefix`, `workspace.history_limit`, `workspace.shell`, `[popup]`) | The generated `<state>/tmux.conf` is rewritten by any `lyna-tmux` command that opens the server, for example `lmux ls`. A running server sources it again when you create or attach a workspace, or from the dashboard. The prefix binding `r` sources the generated file on the spot. |
| `ui.theme` through `lmux theme <name>` | Written and applied to the running workspaces immediately. |
| Claude launch settings (`[claude]`, `[sandbox]`) | Written per launch into `<state>/settings/`. Panes that are already running keep the settings they started with; open a new pane, window or workspace to pick up the change. |
| `[layouts.<name>]` and `workspace.layout` | Used when a window is laid out: `lmux create`, `lmux layout <name>`. |

Your own tmux settings belong in `<config>/tmux.local.conf`. The generated configuration
sources it last, so anything you set there wins over the generated lines, and it survives
every rewrite.
