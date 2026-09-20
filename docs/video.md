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
| One chapter alone | `docs/assets/demo-<chapter>.mp4`, one file per row below |
| The scenes | [docs/video](video), one per chapter, in the order they play |

## Chapters

| Time | Chapter | Scene |
|---|---|---|
| 00:00 | lyna-tmux | `01-title.scene` |
| 00:15 | Is the machine ready | `02-doctor.scene` |
| 00:29 | The wizard | `03-setup.scene` |
| 00:42 | The configuration | `04-config.scene` |
| 00:58 | The first workspace | `05-workspace.scene` |
| 01:17 | Layouts | `06-layouts.scene` |
| 01:31 | Panes | `07-panes.scene` |
| 01:45 | Split and zoom | `08-split.scene` |
| 02:00 | Windows | `09-windows.scene` |
| 02:16 | The menu | `10-menu.scene` |
| 02:29 | The key map | `11-keys.scene` |
| 02:43 | The dashboard | `12-dashboard.scene` |
| 02:56 | Workspaces | `13-sessions.scene` |
| 03:09 | Live changes | `14-changes.scene` |
| 03:26 | Review | `15-review.scene` |
| 03:43 | The git workstation | `16-git.scene` |
| 04:12 | Agents | `17-agents.scene` |
| 04:26 | The agents rail | `18-rail.scene` |
| 04:41 | Teams | `19-team.scene` |
| 05:04 | Spawn | `20-spawn.scene` |
| 05:20 | Tasks in worktrees | `21-task.scene` |
| 05:34 | Resume | `22-resume.scene` |
| 05:48 | The scratch shell | `23-scratch.scene` |
| 06:00 | Hooks and state | `24-hooks.scene` |
| 06:18 | Themes | `25-theme.scene` |
| 06:39 | Sandbox | `26-sandbox.scene` |
| 06:56 | Containers | `27-devcontainer.scene` |
| 07:09 | Your own tmux and Claude | `28-plugin.scene` |
| 07:23 | A project's own settings | `29-init.scene` |
| 07:36 | Uninstall | `30-uninstall.scene` |
| 07:48 | lyna-tmux | `31-closing.scene` |

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
