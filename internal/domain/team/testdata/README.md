# Where these fixtures come from

`config/team.json` and `config/lead-only.json` are the files Claude Code 2.1.274 wrote for real
teams, copied from `~/.claude/teams/<team>/config.json` and scrubbed: the working directories became
`/home/dev/acme-api`, the session identifiers were replaced, and the spawn prompts were shortened to
one line. Nothing else was changed, so the field names, the types and the shape of a member entry
are the ones the product writes, including `tmuxPaneId: "leader"` for the lead of an in-process team
and `%0`, `%1`, `%2` for teammates the tmux backend opened.

`config/newer-claude.json` is written by hand on purpose. It carries fields no release writes today
(`policy`, `capabilities`, `windowPaneId`) and leaves out fields today's release always writes, so
the parser is held to ignoring what it does not know and to filling in what is missing. A file like
it is what a newer Claude Code looks like from here.

`inbox/empty.json` is a real capture as well: it is the mailbox of a lead that received nothing,
copied byte for byte from `~/.claude/teams/<team>/inboxes/<agent>.json`. Every mailbox on the
machine held exactly that, so it is the only mailbox a capture could give.

`tasks/list/*`, `tasks/newer-claude.json` and `inbox/team-lead.json` are written against the shapes
Claude Code 2.1.274 validates, read out of the installed binary rather than guessed: a task is
`{id, subject, description, activeForm?, owner?, status, blocks[], blockedBy[], metadata?}` with
`status` one of `pending`, `in_progress`, `completed`, stored as `<id>.json` beside a
`.highwatermark` file in `~/.claude/tasks/<team>/`; a mailbox entry is
`{type?, from, text, timestamp, read?, color?, summary?}` in a top-level array, and the product
itself drops an entry that does not match, which is why `inbox/broken.json` holds three that do not.
No team on this machine had written a task, so there was nothing to capture; replace these with a
capture the first time a real team fills a list.

To refresh them, run a team, copy the file, and scrub it again. Never point a test at
`~/.claude`: these tests read fixtures, and nothing lyna-tmux ships writes under that directory.
