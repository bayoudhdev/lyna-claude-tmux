# Gallery

Every screen lyna-tmux draws, captured from a real terminal. Each picture is the last frame of the
scene named beside it, and the animations of the same scenes are in the
[README](../README.md). The scenes themselves, and how to record them again, are in
[scenes](scenes/README.md), and the whole tool in motion is the [demonstration film](video.md).

## The workspace

| | |
|---|---|
| **The workspace** (`workspace.scene`)<br>Claude, a shell and the live changes pane, opened by one command. | ![The workspace](assets/workspace.png) |
| **Panes** (`split.scene`)<br>Split right, split down and zoom, with a shell in every new pane. | ![Splitting panes](assets/split.png) |
| **Focus and size** (`panes.scene`)<br>The focus moves with `Alt+]` and `Alt+[`, the size with `Alt+Shift+arrows`. | ![Moving the focus](assets/panes.png) |
| **Windows** (`windows.scene`)<br>The tree of every workspace and window on the server. | ![The session tree](assets/windows.png) |
| **The menu** (`menu.scene`)<br>Every action with its key, grouped, under `Alt+Space`. | ![The menu](assets/menu.png) |
| **Scratch shell** (`scratch.scene`)<br>A shell in a popup over the panes, gone when it exits. | ![A scratch shell](assets/scratch.png) |
| **Layouts** (`layouts.scene`)<br>`quad`: four agents, each in its own git worktree. | ![The quad layout](assets/layouts.png) |

## Agents

| | |
|---|---|
| **The agents rail** (`rail.scene`)<br>Every agent the workspace can see, grouped, with its state and how long it has been at it. | ![The agents rail](assets/rail.png) |
| **A team at work** (`team.scene`)<br>The agent, what it runs beside it, the shared task list and a transcript. | ![The rail while a team works](assets/team.png) |
| **Starting an agent** (`spawn.scene`)<br>The form `s` opens: definition, model, effort, worktree and prompt. | ![The spawn form](assets/spawn.png) |
| **The picker** (`agents.scene`)<br>Every agent on the machine, what it is doing, and where it runs. | ![The agent picker](assets/agents.png) |
| **Tasks** (`task.scene`)<br>A task window with its own worktree, listed by git. | ![A task worktree](assets/task.png) |

## Changes

| | |
|---|---|
| **Review** (`review.scene`)<br>The working tree in a side-by-side diff, file list on the left. | ![The diff review](assets/review.png) |
| **Live changes** (`changes.scene`)<br>The same list, kept in view while the agent works. | ![The live changes pane](assets/changes.png) |
| **The git workstation** (`git.scene`)<br>The refs of the project, the history with its lanes, and the commit in front of you. | ![The git workstation](assets/git.png) |

## Managing workspaces

| | |
|---|---|
| **The dashboard** (`dashboard.scene`)<br>Workspaces, agents and one key per action. | ![The dashboard](assets/dashboard.png) |
| **From the command line** (`sessions.scene`)<br>`ls` and `rename`, with the layout and sandbox of each workspace. | ![Listing workspaces](assets/sessions.png) |
| **Uninstall** (`uninstall.scene`)<br>What it removes, listed before anything is removed. | ![Uninstall](assets/uninstall.png) |

## Sandbox

| | |
|---|---|
| **Status and profiles** (`sandbox.scene`)<br>What the sandbox is doing now, and what `strict` changes. | ![The sandbox status](assets/sandbox.png) |
| **Dev container** (`devcontainer.scene`)<br>The files lyna-tmux renders for container isolation. | ![The dev container files](assets/devcontainer.png) |

## Setup

| | |
|---|---|
| **Doctor** (`doctor.scene`)<br>Every requirement, with the command that fixes what is missing. | ![The doctor report](assets/doctor.png) |
| **The wizard** (`setup.scene`)<br>Theme, layout, sandbox and model defaults. | ![The setup wizard](assets/setup.png) |
| **Configuration** (`config.scene`)<br>The documented file, and where it lives. | ![The configuration file](assets/config.png) |
| **Themes** (`theme.scene`)<br>The palettes, and the one the workspace uses. | ![The themes](assets/theme.png) |
| **Keys** (`keys.scene`)<br>The workspace map and the keys left to Claude Code. | ![The key map](assets/keys.png) |
| **Plugin mode** (`plugin.scene`)<br>What lyna-tmux adds to a tmux setup you already have. | ![Plugin mode](assets/plugin.png) |
