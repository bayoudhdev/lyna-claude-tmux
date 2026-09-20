# The git workstation

The workstation is the repository in one pane: the branches, the history and what is in front of
you, side by side, with every operation on a key. It reads the project with git and runs git, and
nothing it does is hidden: anything that cannot be undone shows the exact command before it runs.

```sh
lmux git                    # the project in the current directory
lmux git --dir ~/src/api    # another project
lmux git --session api      # outside tmux, following that workspace
```

`Alt+G`, or `G` after the prefix, opens it as a column on the right of the current window and
closes it again. The `git` layout opens it beside the agent from the start:

```sh
lmux create ~/src/api -l git
```

Inside a workspace it follows that workspace: the same hooks that refresh the changes pane refresh
it, so a file the agent writes and a commit it makes are on screen without a key being pressed.

## The three regions

| Region | What it holds |
|---|---|
| Refs | the branches of the project, the branches of its remotes, its worktrees, its stashes and its tags, each group foldable |
| History | the commits, with the lanes that show how they were merged, the refs that point at them, the author and when |
| Detail | the commit the cursor is on, file by file with the lines added and removed, or the working tree |

`tab` and `shift+tab` move between them, `w` shows the working tree in the detail region, `r` reads
the repository again, and `?` lists every key of the region you are in. The mouse works everywhere:
clicking a region moves the keys there, clicking a row selects it, and the wheel scrolls.

In the refs region `enter` reads the history of the row, `space` folds a group and `/` filters. In
the history `enter` shows the commit in the detail region, `/` searches it, `n` and `N` walk the
hits, and walking off the end reads the page before it. In the detail `enter` opens the diff of a
file.

## Every key

The keys mean the same thing wherever they are pressed: `c` checks out whatever the cursor is on,
`b` opens a branch at it, `d` gets rid of it. What reaches a remote is a capital letter, so no push
is one lower case letter away.

Anywhere in the workstation:

| Key | What it does |
|---|---|
| `F` | fetch the remote and prune what is gone from it |
| `L` | pull |
| `P` | push the branch |
| `W` | push over what the remote holds, holding to the commit the workstation read |
| `O` | push the branch and make it follow the one it lands on |
| `y` | carry on the rebase, the cherry pick or the merge git stopped in the middle of |
| `!` | leave the commit it stopped on out and carry on |
| `Z` | put the branch back where it stood before that operation started |

On a row of the refs region:

| Key | What it does |
|---|---|
| `c` | check it out: a branch, a branch of a remote (followed by one of the same name here), a tag with no branch on it |
| `b` | open a branch at it |
| `m` | merge it into the branch the working tree is on |
| `B` | replay the branch the working tree is on onto it |
| `i` | rename a branch |
| `d` | get rid of it: delete a branch or a tag, drop a stash, remove a worktree |
| `u` | make the branch the working tree is on follow that branch of a remote |
| `p` | pop a stash |
| `a` | apply a stash, leaving it in the list |
| `A` | open a worktree of the project on it |

On a commit of the history:

| Key | What it does |
|---|---|
| `c` | check the commit out, with no branch on it |
| `b` | open a branch at it |
| `A` | open a worktree on it |
| `t` | tag it |
| `T` | tag it with a message of its own |
| `y` | cherry pick it onto the branch the working tree is on |
| `v` | revert it |
| `m` | move the branch here, keeping what it held in the working tree |
| `M` | move the branch here, keeping what it held staged |
| `H` | move the branch here and write the working tree over with it |
| `e` | reword it |
| `d` | drop it from the history |
| `S` | fold it into the commit before it, joining the two messages |
| `f` | fold it into the commit before it, keeping that message |
| `[`, `]` | move it one place earlier or later |
| `p` | write its patch into `.claude/patches` of the project |
| `o` | copy its object name |

On the working tree in the detail region:

| Key | What it does |
|---|---|
| `s`, `S` | stage the file, stage every change |
| `u`, `U` | unstage the file, unstage everything |
| `x`, `X` | discard the file, discard every change of the tracked files |
| `C` | commit what is staged |
| `M` | amend the last commit with what is staged, keeping its message |
| `e` | write the message of HEAD again |

The keys that rewrite a history (`e`, `d`, `S`, `f`, `[` and `]`) run a rebase with a plan
written for them, put the changes of the working tree away for the time of it and bring them back.
A rebase that stops on a conflict is a banner across the top of the workstation, with `y`, `!` and
`Z` offered on it.

## What asks first

An operation that cannot be undone by pressing the same key again puts a form up: what it is about
to do, what that costs, and the command it runs, written out exactly as it will be run. The command
is not typed out by hand anywhere: it is built by the same code that runs it, so what the form shows
cannot drift from what happens.

| It asks | With |
|---|---|
| discarding one file, dropping a stash, removing a worktree, deleting a merged branch, deleting a tag | one key |
| discarding every change, moving a branch and writing the tree over, deleting a branch no other holds, pushing over a remote | the name of the branch, the stash or the worktree typed back |
| committing, rewording, branching, tagging, opening a worktree | the message or the name |

An operation that runs more than one command shows none of them rather than showing the first,
which would be a reading and not the change.

## What it reads

One reading holds everything the three regions draw, so no region is ever drawn from a reading
another one has moved past. It is taken when the workstation opens, when a file of the repository
changes, when an agent of the workspace signals an edit, and every twenty seconds with nothing else
having said anything.

The history is read a page at a time (400 commits), and the page before the oldest commit is read
when the cursor walks off the end of it or when `enter` on a ref asks for a history the page does
not reach.

## What it never does

- It never runs a command through a shell: every git command is an argument list, and nothing a
  branch, a path or a message holds can become part of it.
- It never force pushes without holding to the commit it read: `W` stops if the remote moved since.
- It never deletes the branch the working tree is on, and never deletes a branch of a remote by
  mistake: that is a push that deletes it, and it says so.
- It never touches a repository other than the one it was opened on.

## Configuration

| Key | What it changes |
|---|---|
| `workspace.split_ratio` | how wide the workstation opens beside the agent, as what the ratio leaves it |
| `workspace.layout = "git"` | opens every new workspace with it |
| `ui.theme`, `ui.icons`, `ui.color` | how it is drawn, as for every view |

See [docs/configuration.md](configuration.md) for the file itself and [docs/keys.md](keys.md) for
the keys of the workspace around it.
