# The demonstration film

`docs/assets/demo.mp4` is every feature of lyna-tmux, example by example, in eight minutes.
It is recorded from real terminals: the commands run, the keys are sent to a tmux server, and
what the screen answers is what the film shows. The subtitles carry the command as it is typed
and what it does; they are drawn into the picture and shipped again as `docs/assets/demo.srt`
for a player that prefers its own.

| | |
|---|---|
| Film | [docs/assets/demo.mp4](assets/demo.mp4), 1280x860, H.264, 8 minutes |
| Subtitles | [docs/assets/demo.srt](assets/demo.srt) |
| One chapter alone | `docs/assets/demo-<chapter>.mp4`, linked from every row below |
| The scenes | [docs/video](video), one per chapter, in the order they play |

## Chapters

| Time | Chapter | Scene |
|---|---|---|
| 00:00 | [lyna-tmux](assets/demo-01-title.mp4) | `01-title.scene` |
| 00:15 | [Is the machine ready](assets/demo-02-doctor.mp4) | `02-doctor.scene` |
| 00:29 | [The wizard](assets/demo-03-setup.mp4) | `03-setup.scene` |
| 00:42 | [The configuration](assets/demo-04-config.mp4) | `04-config.scene` |
| 00:58 | [The first workspace](assets/demo-05-workspace.mp4) | `05-workspace.scene` |
| 01:17 | [Layouts](assets/demo-06-layouts.mp4) | `06-layouts.scene` |
| 01:31 | [Panes](assets/demo-07-panes.mp4) | `07-panes.scene` |
| 01:45 | [Split and zoom](assets/demo-08-split.mp4) | `08-split.scene` |
| 02:00 | [Windows](assets/demo-09-windows.mp4) | `09-windows.scene` |
| 02:16 | [The menu](assets/demo-10-menu.mp4) | `10-menu.scene` |
| 02:29 | [The key map](assets/demo-11-keys.mp4) | `11-keys.scene` |
| 02:43 | [The dashboard](assets/demo-12-dashboard.mp4) | `12-dashboard.scene` |
| 02:56 | [Workspaces](assets/demo-13-sessions.mp4) | `13-sessions.scene` |
| 03:09 | [Live changes](assets/demo-14-changes.mp4) | `14-changes.scene` |
| 03:26 | [Review](assets/demo-15-review.mp4) | `15-review.scene` |
| 03:43 | [The git workstation](assets/demo-16-git.mp4) | `16-git.scene` |
| 04:12 | [Agents](assets/demo-17-agents.mp4) | `17-agents.scene` |
| 04:26 | [The agents rail](assets/demo-18-rail.mp4) | `18-rail.scene` |
| 04:41 | [Teams](assets/demo-19-team.mp4) | `19-team.scene` |
| 05:04 | [Spawn](assets/demo-20-spawn.mp4) | `20-spawn.scene` |
| 05:20 | [Tasks in worktrees](assets/demo-21-task.mp4) | `21-task.scene` |
| 05:34 | [Resume](assets/demo-22-resume.mp4) | `22-resume.scene` |
| 05:48 | [The scratch shell](assets/demo-23-scratch.mp4) | `23-scratch.scene` |
| 06:00 | [Hooks and state](assets/demo-24-hooks.mp4) | `24-hooks.scene` |
| 06:18 | [Themes](assets/demo-25-theme.mp4) | `25-theme.scene` |
| 06:39 | [Sandbox](assets/demo-26-sandbox.mp4) | `26-sandbox.scene` |
| 06:56 | [Containers](assets/demo-27-devcontainer.mp4) | `27-devcontainer.scene` |
| 07:09 | [Your own tmux and Claude](assets/demo-28-plugin.mp4) | `28-plugin.scene` |
| 07:23 | [A project's own settings](assets/demo-29-init.mp4) | `29-init.scene` |
| 07:36 | [Uninstall](assets/demo-30-uninstall.mp4) | `30-uninstall.scene` |
| 07:48 | [lyna-tmux](assets/demo-31-closing.mp4) | `31-closing.scene` |

## Recording it again

The film is made by one command, against a project of your own and the binary you want it to
show:

```bash
scripts/record-video.sh --project ~/src/acme-api --bin ./lmux
```

Each scene of `docs/video` is recorded as its own chapter by `scripts/record-docs.sh --film`,
with the environment every other recording has: a configuration and a tmux server of its own,
the fixture binaries, and the home directory, the account name and the address git commits
under rewritten to neutral ones before a frame is drawn. The chapters are then joined without
being encoded again, their subtitles moved by the second each chapter starts at, and the
chapter list above printed for this page.

Every chapter is recorded at 134 by 36 cells: a film is one picture from beginning to end, and
the assembler refuses to join two sizes. `--only <chapter>` records one of them, `--width` and
`--fps` change what the renderer draws, and `--dry-run` prints what would be recorded.

The recorder needs tmux, python3, a Chrome (or `CHROME_PATH`), ffmpeg and ffprobe. The project
must be one Claude Code has been trusted in, otherwise no hook runs and the status bar shows no
agent state; the review editor must be installed (`lmux review install`), and the binary must
carry the version it claims, since `lmux sandbox devcontainer init` refuses a development
build.

## What the film never shows

- The home directory, the account name and the address git commits under, which are rewritten
  to neutral ones of the same length, including where a screen cut a path short.
- The account's own tmux server, configuration or workspaces: the scenes run on a server and a
  configuration of their own, and end their workspaces when the pass is over.
- A repository that is not written for the recording: the git chapters run against the one
  `docs/scenes/bin/demo-repo` writes.
