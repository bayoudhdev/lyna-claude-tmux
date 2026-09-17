# Keys and mouse

A `lyna-tmux` workspace installs two kinds of key binding:

- **Workspace keys**, in the tmux root table, which fire without the prefix. They all use
  Alt (Option on macOS), so none of them can take a key away from Claude Code.
- **Prefix keys**, which fire after the tmux prefix. They cover the same actions, so a
  terminal that cannot send Meta still reaches everything.

`lyna-tmux keys` prints what your configuration installs right now, and `lyna-tmux keys
--json` prints the same as machine-readable JSON. Run it after changing `ui.alt_keys` or
`workspace.prefix`; both live in [configuration.md](configuration.md).

## Workspace keys, no prefix

Alt is Option on macOS. Enable Option as Meta in your terminal first; see
[Option as Meta, per terminal](#option-as-meta-per-terminal).

### Panes

| Key | Action | Detail |
| --- | --- | --- |
| `Alt+[` | Focus previous pane | |
| `Alt+]` | Focus next pane | |
| `Alt+Shift+Left` | Resize pane left | 5 cells per press |
| `Alt+Shift+Right` | Resize pane right | 5 cells per press |
| `Alt+Shift+Up` | Resize pane up | 3 cells per press |
| `Alt+Shift+Down` | Resize pane down | 3 cells per press |
| `Alt+\` | Split right | New pane is a shell in the current pane's directory |
| `Alt+-` | Split down | New pane is a shell in the current pane's directory |
| `Alt+z` | Zoom pane | |
| `Alt+x` | Close pane | Asks `Close pane #P? (y/n)` first |
| `Alt+c` | Focus Claude pane | Selects the first Claude pane of the window, or says there is none |

### Windows

| Key | Action | Detail |
| --- | --- | --- |
| `Alt+n` | New window | Opens in the workspace's project directory, or the current pane's directory when the session has none, as a shell pane |
| `Alt+1..9` | Window 1 to 9 | Windows are numbered from 1 |
| `Alt+e` | Sessions and windows | The tmux session and window tree |

### Tools

| Key | Action | Detail |
| --- | --- | --- |
| `Alt+a` | Agents | Agents picker, in a popup sized by `popup.width` and `popup.height` |
| `Alt+g` | Review changes | Review popup, always 95 percent by 95 percent |
| `Alt+s` | Scratch shell | Throwaway shell popup, 80 percent by 70 percent, in the current pane's directory |
| `Alt+Space` | Menu | The key menu, centered: every installed Alt binding except this one and the window numbers, each with its key and runnable from the menu |

## After the prefix

The prefix is `Ctrl+b` unless `workspace.prefix` says otherwise. These bindings follow the
prefix you configured; the table below assumes the default.

### Panes

| Key | Action |
| --- | --- |
| `\` | Split right |
| `-` | Split down |
| `C` | Focus Claude pane |

### Tools

| Key | Action |
| --- | --- |
| `a` | Agents |
| `g` | Review changes |
| `S` | Scratch shell |
| `Space` | Menu |

### Session

| Key | Action | Detail |
| --- | --- | --- |
| `Ctrl+b` | Send the prefix key to the pane | The prefix pressed twice |
| `r` | Reload configuration | Sources the generated tmux configuration and reports `lyna-tmux: configuration reloaded` |
| `d` | Detach | Leaves the workspace running |

Where a prefix binding has an Alt counterpart it runs the same action; `Send the prefix key
to the pane`, `Reload configuration` and `Detach` exist only here. A prefix binding can never
collide with Claude Code, because the prefix already consumed the keystroke.

## Turning the Alt bindings off

```toml
[ui]
alt_keys = false
```

What changes:

- No binding is installed in the tmux root table. Every Alt key, including the ones Claude
  Code uses and any you bound yourself, reaches the program in the pane untouched.
- The prefix table is unchanged: all ten bindings above stay, so splits, the agents picker,
  the review popup, the scratch shell, the menu, reload and detach remain reachable.
- The menu has nothing left to list, so it falls back to three entries: Agents, Review
  changes and Scratch shell.
- Mouse behavior is unchanged. It is controlled by `ui.mouse`, not by `ui.alt_keys`.
- `lyna-tmux doctor` skips its Option as Meta check and reports `Alt key bindings are
  disabled (ui.alt_keys = false)`.

Running `lyna-tmux keys` with the setting off prints only the prefix section and the keys
left to Claude Code.

Changing `workspace.prefix` is the other half of this: set it to `C-a`, `C-Space` or any
tmux key name if `Ctrl+b` is in your way. Remember that the prefix key itself stops reaching
Claude Code until you press it twice.

## Mouse

Everything below needs `ui.mouse = true`, the default. With `mouse` off, tmux delivers no
mouse events at all, so clicks, menus and the wheel do nothing.

### Status bar buttons

The clickable regions need tmux 3.4 or newer, which is where tmux gained user ranges in
status formats. On tmux 3.3 the `split`, `review` and menu labels are not drawn at all; the
agents block and the sandbox shield are still shown, they are simply not clickable. The
labels do not depend on `ui.mouse`, so with the mouse off they stay on screen and do
nothing.

| Where you click | What happens |
| --- | --- |
| `split` | Splits the current pane to the right |
| `review` | Opens the review popup |
| The menu glyph | Opens the workspace menu |
| The agents block, showing the Claude pane count or the waiting count | Opens the agents picker |
| The sandbox shield | Opens the sandbox status popup |
| A window tab | Selects that window |
| The session block at the left end, left or right click | Opens the workspace menu |

The workspace menu holds: Agents, Review changes, Live changes, Sandbox status, Sessions and
windows, Scratch shell, Reload configuration, Detach.

### Right-click menus

| Where you right click | Menu |
| --- | --- |
| The window list in the status line | New window, Rename window, `Layout: solo` and one entry per other built-in layout (`duo`, `trio`, `quad`, `review`), Even tiles, Close window, all acting on the window under the pointer |
| Inside a pane | Split right, Split down, Zoom, Swap with next, Copy mode, Claude, Restart Claude, Review changes, Agents, Close pane |

In the pane menu, `Claude` and `Restart Claude` are enabled only on a Claude pane. `Claude`
opens a submenu that types a slash command into that pane: Workflows, Deep research, Effort
(`ultracode` directly, or the picker), Model, Plan mode, Background tasks, Context usage,
Compact context, Security review, Sandbox, Fullscreen renderer, Resume conversation, Usage.
Commands that need an argument are typed without being submitted, so you can finish them.

A `Layout:` entry opens a new window laid out that way, with the panes `lyna-tmux create`
would start; the windows already open are left as they are. `Even tiles` retiles the window
under the pointer. `auto` is not offered, because it only makes sense at creation, when the
terminal size is known.

A program that asked for mouse events, such as the fullscreen renderer or an editor, keeps
receiving right clicks itself. `Alt` plus right click always opens the pane menu instead.

`Close pane`, `Close window` and `Restart Claude` ask for confirmation first.

### Wheel

`ui.mouse = true` sets the tmux `mouse` option to `on`, which hands the wheel to the pane
under the pointer. `lyna-tmux` binds no wheel key of its own, so scrolling behaves exactly as
tmux does it: a program that asked for mouse events receives the wheel, and otherwise tmux
scrolls that pane's history. On tmux 3.6 and newer the generated configuration also turns on
modal pane scrollbars, so a scrolled pane shows one.

`claude.fullscreen = true` starts Claude Code with the flicker-free fullscreen renderer,
which handles the wheel itself.

## Inside the live changes view

The changes view reads the keys itself, in the `changes` pane of a layout as well as in the
popup, so the pane is a list you move through and not one you can only read the top of.

| Key or click | What happens |
| --- | --- |
| `Down`, `j`, `Ctrl+n`, wheel down | Next file |
| `Up`, `k`, `Ctrl+p`, wheel up | Previous file |
| `PageDown`, `Ctrl+f`, `PageUp`, `Ctrl+b` | One screen of files |
| `g`, `Home` and `G`, `End` | First and last file |
| Click | Select the file under the pointer |
| `Enter`, double click | Review that file alone |
| `o` | Review the whole working tree |
| `q`, `Esc` | Close, in the popup only |

The review opens in a popup over the pane and gives it back when it closes. In a pane of a
layout `q` and `Esc` do nothing, so a stray key never takes the changes out of the layout;
`Ctrl+c` closes the view everywhere. The review keys do nothing in the changes popup: tmux
shows one popup per client, so opening a second would close the first.

The list is redrawn when a file changes, not on a timer: Claude's edits arrive through the
hook, git and top-level edits through file events, and anything neither reports is picked up
by a reading that runs when nothing else has for fifteen seconds.

## Keys left to Claude Code

No workspace binding may sit in the tmux root table on a key Claude Code uses: tmux would
swallow the keystroke before Claude ever saw it. These are the keys that are kept free.

| Key | In Claude Code |
| --- | --- |
| `Ctrl+c` | interrupt |
| `Ctrl+d` | exit |
| `Ctrl+t` | toggle todos |
| `Ctrl+o` | toggle transcript |
| `Ctrl+r` | search history |
| `Ctrl+l` | clear input |
| `Ctrl+x` | chord prefix (kill agents, queue submit, external editor) |
| `Ctrl+j` | newline |
| `Ctrl+_` | undo |
| `Ctrl+g` | external editor |
| `Ctrl+s` | stash prompt |
| `Ctrl+v` | paste image |
| `Ctrl+b` | background task, page up in transcript (press `Ctrl+b` twice, the prefix takes it first) |
| `Ctrl+e` | show all in transcript |
| `Ctrl+u` | half page up |
| `Ctrl+f` | page down in transcript |
| `Ctrl+n` | next item |
| `Ctrl+p` | previous item |
| `Ctrl+]` | open artifact |
| `Alt+p` | model picker |
| `Alt+o` | fast mode |
| `Alt+t` | thinking toggle |
| `Alt+w` | workflow keyword toggle |
| `Alt+j` | toggle terminal |
| `Alt+m` | cycle permission mode (Windows) |
| `Alt+v` | paste image (Windows) |
| `Alt+Up` | previous file in diff list |
| `Alt+Down` | next file in diff list |
| `Alt+b` | word left |
| `Alt+f` | word right |
| `Alt+d` | delete word |
| `Alt+y` | yank |
| `Alt+Backspace` | delete word left |
| `Alt+Left` | word left |
| `Alt+Right` | word right |
| `Alt+Enter` | newline |
| `Shift+Tab` | cycle permission mode |
| `Shift+Enter` | newline |
| `Enter` | submit |
| `Esc` | cancel |
| `Tab` | accept completion |
| `Up` | previous history |
| `Down` | next history |
| `Space` | push to talk |
| `PageUp` | scroll up |
| `PageDown` | scroll down |

The note on `Ctrl+b` is printed only while `Ctrl+b` is your prefix. Choose another prefix and
`Ctrl+b` reaches Claude Code directly.

Two checks keep this honest:

- `lyna-tmux keys` ends with a `Conflicts` section whenever a root binding lands on one of
  these keys, or a key is bound twice in one table. A clean run prints no such section.
- `lyna-tmux doctor` reads your own `keybindings.json` from the Claude Code configuration
  directory (`$CLAUDE_CONFIG_DIR`, or `~/.claude`) and warns about each binding whose first
  keystroke is a workspace root key or the prefix, naming the key and what takes it. Later
  keystrokes of a chord are safe: once the first key reaches Claude Code, tmux has no
  binding for the rest.

## Option as Meta, per terminal

On Linux and other non-macOS platforms, Alt already sends Meta and there is nothing to do.
On macOS, Option inserts composed characters until you change one setting. `lyna-tmux doctor`
identifies your terminal from `TERM_PROGRAM`, from `LC_TERMINAL`, or from the marker
variables terminals export (`KITTY_WINDOW_ID`, `ALACRITTY_WINDOW_ID`, `ALACRITTY_SOCKET`,
`WEZTERM_PANE`, `WEZTERM_EXECUTABLE`, `GHOSTTY_RESOURCES_DIR`), then prints the exact
setting. These are the ones it knows:

| Terminal | Identified by | Setting |
| --- | --- | --- |
| Terminal | `TERM_PROGRAM=Apple_Terminal` | `Terminal > Settings > Profiles > Keyboard`: enable "Use Option as Meta Key" |
| iTerm2 | `TERM_PROGRAM=iTerm.app`, or `LC_TERMINAL=iTerm2` | `iTerm2 > Settings > Profiles > Keys > General`: set "Left Option key" and "Right Option key" to "Esc+" |
| WezTerm | `TERM_PROGRAM=WezTerm`, `WEZTERM_PANE`, `WEZTERM_EXECUTABLE`, `TERM=wezterm` | Already sends the left Option key as Alt. To change both keys: `config.send_composed_key_when_left_alt_is_pressed = false` and `config.send_composed_key_when_right_alt_is_pressed = false` in `wezterm.lua` |
| Ghostty | `TERM_PROGRAM=ghostty`, `GHOSTTY_RESOURCES_DIR`, `TERM=xterm-ghostty` | `macos-option-as-alt = true` in the Ghostty config |
| kitty | `TERM_PROGRAM=kitty`, `KITTY_WINDOW_ID`, `TERM=xterm-kitty` | `macos_option_as_alt yes` in `kitty.conf` |
| Alacritty | `TERM_PROGRAM=Alacritty`, `ALACRITTY_WINDOW_ID`, `ALACRITTY_SOCKET`, `TERM=alacritty` | `[window] option_as_alt = "Both"` in `alacritty.toml` |
| VS Code terminal | `TERM_PROGRAM=vscode` | `"terminal.integrated.macOptionIsMeta": true` in `settings.json` |
| Anything else | nothing above matched | Enable the terminal's "Option as Meta" (or "Option as Alt") setting; the same actions stay available after the tmux prefix |

Inside tmux, `TERM_PROGRAM` names tmux rather than the outer terminal, which is why the
marker variables and `LC_TERMINAL` matter: they survive, because the tmux server inherits
them.

`doctor` cannot read a terminal's setting, so this check is a warning with the instructions,
never a failure. If you would rather not change the terminal at all, set `ui.alt_keys =
false` and use the prefix bindings.

## Reloading the configuration

The key bindings, the status line and the mouse behavior all come from one generated file,
`<state>/tmux.conf`. You never edit it: it is rewritten from `config.toml`. The `<state>` and
`<config>` directories are the ones listed in [configuration.md](configuration.md).

1. Edit `config.toml`, for example with `lyna-tmux config edit`, and check it with
   `lyna-tmux config validate`.
2. Regenerate the tmux configuration by running any command that opens the server, for
   example `lyna-tmux ls`. The file is rewritten only when its content actually changed.
3. Load it into the running server. Creating or attaching a workspace, and the dashboard,
   do this by themselves whenever the file changed. To do it by hand, press the prefix and
   then `r`, or choose `Reload configuration` from the workspace menu.

Order matters: the prefix binding `r` sources the generated file as it is on disk, so run
step 2 before it if you have only edited `config.toml`.

Two shortcuts:

- `lyna-tmux theme <name>` writes `ui.theme` and restyles the running workspaces at once.
- `lyna-tmux setup` applies its result the same way when it finishes.

Claude Code panes are not affected by a reload: their settings are written per launch. Open
a new pane, window or workspace for a change under `[claude]` or `[sandbox]`.

Your own tmux settings go in `<config>/tmux.local.conf`, which the generated configuration
sources last, so they win and survive every rewrite.

## Plugin mode

When you use `lyna-tmux` from a tmux server you run yourself, none of the bindings above are
installed. That mode adds two prefix keys instead, `y` for the Claude popup of the current
directory and `u` for the agents picker, both changeable with `--launch-key` and
`--list-key` or the `@claude_launch_key` and `@claude_list_key` options. Run `lyna-tmux
plugin tmux --help` for the details.
