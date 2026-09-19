# Agents in the workspace

A workspace runs one agent per Claude pane. A team runs several: a lead you talk to and the
teammates it opens for itself. This page is how that works here: where a teammate lands, how the
window is arranged around it, the rail that lists every agent, and how you start one yourself.

Everything below is read from the agents themselves. The panes are ours and carry what the hooks
report; the team files are Claude Code's own and are only ever read.

## Starting a team

```sh
lmux team                 # this project, agent teams on, in the team layout
lmux team ~/src/api -l trio
```

`lmux team` is `lmux create` with agent teams turned on for the launch. It opens the `team` layout
unless `-l` names another: the agents rail on the left, the lead beside it, and the room the
teammates open into.

To run every workspace as a team, set `claude.teams = true` in the configuration file and use
`lmux create` as usual. Ask the lead for teammates the way you would anywhere else; nothing here
starts them behind your back.

Agent teams are an experimental feature of Claude Code, and whether a build of it offers them to
your account is Claude Code's to decide. A workspace opened with `lmux team` is a normal workspace
when they are off: the rail, the spawn form, the transcripts and the task list all work, and the
teammate panes described below appear as soon as Claude Code opens teammates.

## Where a teammate opens

Claude Code decides to open a teammate; `claude.teammate_mode` decides where it lands.

| Mode | Where the teammate runs |
| --- | --- |
| `lmux` (default) | A pane of this workspace, labeled with its name, placed by the pane policy, drawn with the workspace's own border and listed on the rail. |
| `auto` | Wherever Claude Code opens it, which from inside tmux is a pane of the lead's window with its own styling. |
| `in-process` | Inside the lead's own session, with no pane of its own. |
| `iterm2` | As a terminal split, outside tmux. |

In `lmux` mode the workspace hands Claude Code a small launcher script, which runs inside the pane
the teammate was opened in. It labels that pane, applies the pane policy, puts back the border and
the arrangement, and then runs the agent in place, so the session Claude Code expects is the
session that runs.

**A team never fails to start.** If the launcher cannot be written, if the pane cannot be labeled,
if the server does not answer, the agent starts exactly where Claude Code put it and the reason
goes to the diagnostic log. `lmux doctor` reads the last line of that log and says which of the
three happened: placed, placed but not arranged, or opened the agent's own way.

## The pane policy

A teammate keeps its place beside the lead while all of this holds:

- the window is the lead's alone, with no shell, changes view or review of yours in it;
- the workspace allows one more teammate there (`workspace.agent_panes`, 3 by default, 0 to 8);
- the panes that would result are still readable, which is at least 80 by 14 cells each.

Otherwise it opens as a window of its own, named after it, and the window it leaves goes back to
the arrangement it had before any agent was opened in it. The rail is the map: one key reaches a
teammate wherever it went. `w` on the rail moves an agent out to its own window at any time, and
puts the window it leaves back the same way.

## The agents rail

The rail is a pane of the workspace, 28 cells wide (`ui.sidebar_width`, 20 to 60), listing every
agent it can see:

| Section | What it holds |
| --- | --- |
| lead | The agent the workspace was opened for, which leads the team when there is one. |
| teammates | The agents of the lead's team, including any that Claude Code opened somewhere this server cannot see. |
| subagents | The subagents an agent of this workspace is running. |
| elsewhere | The agents of the other workspaces on this server. |

Each row carries a state glyph, the name, the agent definition it runs, how long it has been doing
what it is doing, and the task it holds when the team has a shared task list. A working agent turns
a spinner, a row that has just arrived fades in, and a section opening or closing takes three
frames. Nothing is drawn on a timer: the animation stops when there is nothing moving, and the rows
themselves are redrawn on the hooks the agents fire, with a fallback reading every ten seconds for
what no hook reports, such as a pane you closed yourself.

States are `busy`, `waiting` (the agent needs you), `idle`, `failed` (its process stopped and the
status is still on screen) and `gone` (a member of the team that runs in no pane of this server).

### Keys

| Key | Action |
| --- | --- |
| arrows, `j`, `k`, wheel | Move the cursor |
| click | Select the row under the pointer |
| `enter`, double click | Focus that agent's pane |
| `z` | Zoom that pane, and back |
| `w` | Move that agent to a window of its own, and put the window it leaves back |
| `s` | Start an agent (the spawn form) |
| `m` | Type a message at that agent, after showing the exact text |
| `x` | Stop that teammate, through its lead or by closing its pane |
| `r` | Read what that agent is writing (its transcript) |
| `t` | Show the shared task list of the team |
| `space` | Fold or unfold the section |
| `/` | Filter the rows |
| `q`, `esc` | Close the rail, when it is a popup |

A key that does not apply to the row the cursor is on says so under the rows rather than doing
nothing: a subagent takes no message of its own, the lead is not stopped from here, and an agent of
another workspace is steered from that workspace. Each of these opens a popup over the rail, and
each is a command of its own: `lmux message`, `lmux stop`, `lmux transcript` and `lmux tasks`.

The footer counts the shared task list, how many of its tasks are blocked, and what the agents of
the workspace have spent between them, read from the transcripts Claude Code writes.

`Alt+A`, or `A` after the prefix, opens the rail of the current window and closes the one that is
there.

### When the rail is on screen

`ui.agents_sidebar` decides:

| Value | Behavior |
| --- | --- |
| `auto` (default) | Opens with the first teammate that joins the workspace and closes with the last one. |
| `always` | Opens with the workspace, whichever layout it opens, and stays. |
| `key` | Never opens by itself; the key opens and closes it. |
| `off` | Never opens by itself, and no key opens it: neither `Alt+A` nor the prefix key is installed, and the key menu lists neither. |

The `team` layout carries the rail whatever this says, and so does any custom layout with an
`agents` pane in it.

## Starting an agent from the workspace

`lmux spawn`, or `s` on the rail, opens a form in a popup:

1. **Target.** Ask the lead, so the agent joins the conversation you are having; or start one of
   our own, with a conversation of its own in a new window.
2. **Agent.** The definitions this project and your own configuration offer, read from their
   frontmatter, plus the agent with no definition of its own. Then the model and the effort, each
   empty by default, which runs the agent on whatever the workspace runs.
3. **Work.** A worktree of its own or the project directory (`claude.agent_worktree` chooses the
   answer the form starts on), a name for the window, and the prompt.

The form then shows the exact sentence it would send. Asking the lead types that sentence into the
lead's pane and submits it, the way you would have typed it. What is shown is what is sent: a
result whose text is not the text of its own request is refused rather than rebuilt.

A lead that is waiting on you is never typed at, because the Enter that submits the message would
answer the question or the permission prompt it is showing. Answer it first, then ask again.

## What the workspace never does

- **It never writes anything of Claude Code's.** The team file, the shared task list and the
  mailboxes are read, never written. Nothing here writes under the Claude Code configuration
  directory.
- **It never touches a tmux server it did not create.** When a team runs on the agent's own server,
  its members are listed on the rail as members with no pane of ours, and nothing is sent to that
  server.
- **It never types into a pane it cannot identify.** Only a pane id is a target, never a name or a
  pattern, which tmux would resolve against whatever is running now. The text typed is folded onto
  one line with every control sequence removed, so a prompt cannot end the paste and have the rest
  read as keys.
- **It never adopts a pane that is not an agent of ours.** A shell you opened in the agents' window
  makes that window yours, and the next teammate opens in a window of its own rather than
  rearranging it.

## When something is not right

`lmux doctor` has an `Agent teams` row:

- whether teams are on, and whether `claude.teammate_mode` opens teammates in panes of the
  workspace at all;
- how the last teammate actually opened, which is the one thing the settings cannot say, read from
  the line the launcher wrote for it;
- the diagnostic log that lists every teammate that opened, for when the last one is not the one
  you are asking about.

A teammate that was labeled but whose window could not be arranged is a warning, and `w` on the
rail is the fix. A teammate that opened the agent's own way carries the reason it fell back.

## Configuration

Every key below is described in [configuration.md](configuration.md).

| Key | What it decides |
| --- | --- |
| `claude.teams` | Agent teams in every workspace, rather than only in one opened with `lmux team`. |
| `claude.teammate_mode` | Where a teammate opens. |
| `claude.agent_worktree` | The answer the spawn form starts the worktree question on. |
| `workspace.agent_panes` | How many teammates share the lead's window. |
| `ui.agents_sidebar` | When the rail is on screen, and whether a key opens it at all. |
| `ui.sidebar_width` | How wide the rail opens, in cells. |
