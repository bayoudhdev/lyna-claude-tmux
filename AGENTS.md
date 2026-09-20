# Contributor and agent guide

This file binds every contributor: humans, coding agents and subagents. Read it before changing anything.

## Project

`lyna-tmux` is a Go CLI that turns tmux into a ready-made Claude Code workspace: styled split layouts, agent picker, per-launch Claude settings (sandbox, hooks, statusline) and a dedicated tmux server that never reads or writes the user's own tmux or Claude settings.

- Module: `github.com/bayoudhdev/lyna-claude-tmux`, binary `lmux`, entry `cmd/lmux`.
- Layers: `internal/cli` (flags, thin) -> `internal/app` (use cases) -> `internal/domain/*` (pure logic) and adapters (`internal/tmux`, `internal/claude`, `internal/git`, `internal/hook`, `internal/statusline`, `internal/watch`, `internal/tui`, `internal/config`, `internal/fsx`, `internal/xdg`, `internal/procx`, `internal/termx`, `internal/sanitize`).
- Domain packages import no adapters. Hook and statusline code paths import no TUI packages (start-up cost).

## Testing policy (mandatory, no exceptions)

Every change ships with tests. Nothing is done until its tests exist, run and pass.

1. **New code gets tests in the same change.** New function, command, flag, option, layout, key binding, menu entry, config key, hook event, settings field or template: each gets tests that exercise its behavior. A bug fix gets a regression test that fails before the fix and passes after.
2. **Table-driven by default.** One `cases` slice with named nominal, edge and failure cases, run through `t.Run(tc.name, ...)`. No copy-pasted test bodies.
3. **Red-green.** Run the tests. When one fails, fix the code (or the expectation, if it was wrong) and rerun until green. Never skip, `t.Skip` without an environmental reason, comment out, delete or weaken a failing test to get green.
4. **Test the real contract, not only the parts.**
   - Anything that builds tmux commands, formats or conf lines needs an integration test against a real, isolated tmux server (`internal/testutil/tmuxtest`: random `-L` socket, `-f /dev/null`, killed on cleanup). Unit tests on argv alone are not enough: tmux has already disproved two argv-only assumptions in this repo.
   - Anything that launches or configures Claude uses `internal/testutil/fakeclaude`, which records argv, env and the settings file.
   - Generated artifacts (tmux conf, settings JSON, devcontainer files, TUI frames) have golden files under `testdata/`, compared through `internal/testutil/golden` (`golden.Assert(t, "name.golden", got)`), regenerated only with `make golden` (`LYNA_TMUX_UPDATE_GOLDEN=1`) and reviewed in the diff. No per-package `-update` flags.
5. **Fuzz every parser and escaper.** Quoting, format escaping, terminal sanitizing, config decoding and hook stdin decoding each have a `Fuzz*` target with a round-trip or safety property.
6. **No sleeps for synchronization.** Wait on an event: `tmux wait-for` channels, file notifications, or a bounded poll of a condition with a deadline taken from the test context.
7. **Hermetic.** Tests never touch the real `~/.tmux.conf`, `~/.claude`, the user's tmux server or the network. Use `t.TempDir()`, `LYNA_TMUX_HOME`, `CLAUDE_CONFIG_DIR` and isolated sockets. Tests pass with `-race` and in any order (`-shuffle=on`).
8. **Coverage gate.** `internal/domain/...`, `internal/tmux`, `internal/sanitize`, `internal/hook`, `internal/config`, `internal/doctor`, `internal/termx`, `internal/git` and `internal/devcontainer` stay at 85% or more (`make cover`, which gates exactly the packages in the Makefile's `COVER_PKGS`).
9. **Verification gate before calling work done:** `make check` green (format, vet, lint, race tests, integration tests, fuzz smoke, coverage, text guard). Report the exact command and its result. Green tests prove the code compiles and the tested parts behave; a user-visible surface (TUI, status bar, menus) is verified only once it has been rendered and looked at.

Useful commands (always use the arm64 toolchain on Apple silicon, `export PATH=/opt/homebrew/bin:$PATH`):

```sh
make test              # unit + integration, race detector, shuffled
make fuzz              # 10s per fuzz target
make cover             # coverage report and gate
make golden            # regenerate golden files, then review the diff
make lint              # gofumpt, go vet, golangci-lint, shellcheck, actionlint, text guard
make check             # everything above, the pre-commit gate
go test ./internal/tmux/ -run TestIntegration -v   # one package
```

## Security rules

- Execute processes with argv only (`exec.Command(bin, args...)`), never through `sh -c` with interpolated data.
- tmux data goes through `internal/tmux` helpers: `escapeArg` (argv batches), `ConfQuote` (conf files), `FormatEscape` / `FormatEscapeTime` (formats, strftime contexts), `ShellQuote` (commands tmux hands to a shell), `ExactSession` (targets).
- Session names are restricted to `[A-Za-z0-9_-]` (no dots or colons: tmux targets use them as separators), worktree names to `[A-Za-z0-9._-]`; neither may start with `-`.
- Text from panes, agents, paths or branches shown in a terminal passes through `internal/sanitize`.
- State files: directories 0700, files 0600, atomic writes via `internal/fsx`, symlinks refused.
- Hooks read at most 1 MiB of stdin, never execute stdin content and always exit 0.

## Authoring rules

- No em-dash character (U+2014) anywhere: code, comments, docs, commit messages. Use commas, colons, periods or parentheses. `scripts/check-text.sh` enforces it.
- No emojis in code, docs or output.
- Do not name other products to describe behavior. Functional identifiers (environment variable values, file paths, license notices) are exempt.
- Production-ready changes only: no stubs, placeholder commands or TODO-driven partial features.
- Comments explain why, not what, and match the surrounding density.
- Remove dead code in the same change that makes it dead.

## Git

- Small logical commits with imperative subjects.
- Subagents and parallel workers make no git mutations (no commit, stash, checkout, reset, rebase, push); the lead integrates and commits.
