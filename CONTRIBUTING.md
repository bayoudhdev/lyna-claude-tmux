# Contributing to lyna-tmux

Thank you for helping. This guide covers the practical steps; [AGENTS.md](AGENTS.md) holds the binding rules (testing policy, security rules, authoring rules) for every contributor, human or agent. Read it before opening a pull request.

Security issues are not reported here: follow [SECURITY.md](SECURITY.md).

## Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Go | as in `go.mod` or newer | build and tests |
| tmux | 3.3 or newer | integration tests (skipped with `-short`) |
| git | any recent | tests, text guard, release |
| golangci-lint | v2.13.2 | `make lint`, `make fmt` |
| shellcheck | any recent | `make lint` |
| actionlint | v1.7.12, optional | `make lint` skips it when it is missing; CI runs it |
| Neovim | 0.9 or newer, optional | review integration tests |
| hyperfine | optional | `scripts/bench.sh` |
| goreleaser | v2, optional | `goreleaser check` |
| Docker | optional | dev container end-to-end test |

On Apple silicon, put the arm64 toolchain first: `export PATH=/opt/homebrew/bin:$PATH`.

## Workflow

1. Fork the repository and create a branch from `main`.
2. Make a focused change with its tests in the same commit (AGENTS.md, testing policy).
3. Run the gate and fix everything it reports:

   ```sh
   make check
   ```

4. Open a pull request that describes the behavior change, how you verified it and, for anything drawn in a terminal (status line, menus, TUI), how it looked when rendered.

CI runs the test suite (`go test -race -shuffle=on`) on five runners: Ubuntu 24.04, macOS arm64, macOS x86_64, Debian 12 with tmux 3.3a and Ubuntu with tmux 3.6 built from source. The single-runner jobs run once each on Ubuntu 24.04: lint (`go mod verify`, `go mod tidy -diff`, `go vet`, golangci-lint, shellcheck, actionlint, the text guard), `govulncheck`, the fuzz smoke run, the coverage floor and `goreleaser check` with a snapshot build. A pull request merges only when CI is green.

## Useful commands

```sh
make build             # bin/lmux
make test              # race detector, shuffled, with tmux integration tests
make test-short        # unit tests only
make golden            # regenerate golden files, then review the diff
make fuzz              # every fuzz target for 10s
make cover             # coverage floor for the gated packages
make lint              # tidy check, vet, golangci-lint, shellcheck, actionlint, text guard
make fmt               # gofumpt and goimports
scripts/bench.sh       # start-up benchmarks as a Markdown table
```

`scripts/bench.sh` runs the `go` and `hyperfine` on PATH; set `GO` or
`HYPERFINE` to point either one somewhere else.

Optional end-to-end test of the dev container (builds an image, needs network access):

```sh
LYNA_TMUX_E2E_DOCKER=1 go test -run TestDockerLifecycleE2E -v ./internal/devcontainer/
```

## Tests in brief

- Table-driven, with named nominal, edge and failure cases.
- tmux-facing code is tested against an isolated tmux server (`internal/testutil/tmuxtest`), never the one you are working in.
- Generated files are compared to reviewed golden files (`internal/testutil/golden`).
- Parsers and escapers have fuzz targets.
- No sleeps, no network, no access to your real `~/.tmux.conf` or `~/.claude`.

## Commits

Write small commits with imperative subjects in the Conventional Commits form, because release notes are grouped from them:

```text
feat(layout): add the quad layout
fix(hook): keep the waiting state after a denied permission
sec(statusline): strip control characters from branch names
perf(statusline): skip the git lookup outside repositories
docs: explain plugin mode
```

A `!` after the type marks a breaking change (`feat(config)!: rename ui.theme values`).

## Releases

Maintainers release by pushing a `vX.Y.Z` tag on a green `main`. The release workflow runs the whole CI gate again on the tagged commit, then builds the archives and packages, generates SBOMs and checksums, and creates the GitHub release as a draft. It attests build provenance for every artifact and only then makes the release public, so a published release always has the attestation [SECURITY.md](SECURITY.md) tells users to verify. Move the `Unreleased` entries of [CHANGELOG.md](CHANGELOG.md) under the new version before tagging.

## License

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE).
