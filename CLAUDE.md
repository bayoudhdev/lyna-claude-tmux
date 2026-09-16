# Claude Code instructions

@AGENTS.md

## Claude-specific rules

- The testing policy in AGENTS.md is a hard gate for the main session and for every subagent. A subagent prompt must carry the files it owns, the tests it must add for each thing it creates, and the acceptance command (`make check`, or `go test -race ./<its packages>/...` while the tree is mid-wave). Output from a subagent is not accepted until the lead reruns those tests on disk.
- Before reporting a task complete, run the tests for every package touched, then `make check`, and quote the result. Failing or missing tests keep the task open.
- Never run `lyna-tmux` commands against the user's real tmux server or Claude settings during development. Use `LYNA_TMUX_HOME=$(mktemp -d)` and `LYNA_TMUX_SOCKET_NAME=lt-dev-<random>`.
