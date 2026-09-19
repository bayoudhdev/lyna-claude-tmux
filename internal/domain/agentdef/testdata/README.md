# Where these fixtures come from

`agents/explore.md` and `agents/reviewer.md` carry the shape of the definitions that sit in
`.claude/agents` on this machine: the same keys (`name`, `description`, `tools`, `model`, `effort`,
`maxTurns`, `color`, `disallowedTools`, `permissionMode`), the same two ways of writing a list, and
the same order. The text is written for this repository rather than copied, because a definition
holds the prompt of somebody's own agent and this repository is public.

`agents/folded.md`, `agents/crlf.md`, `agents/no-name.md` and `agents/no-header.md` are the cases a
parser meets and a person writes: a description folded into one line, a file written on a platform
that ends its lines with a carriage return and opens with a byte order mark, a definition that names
no agent, and a markdown file that is not a definition at all.

Nothing here reads `~/.claude` or a project's real `.claude/agents`: these tests read fixtures, and
the loader that will read the real directories is given a directory to read.
