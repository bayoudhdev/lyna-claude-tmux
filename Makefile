SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := build
MAKEFLAGS += --no-builtin-rules --no-print-directory

GO ?= go
BIN := bin/lyna-tmux
MODULE := github.com/bayoudhdev/lyna-claude-tmux

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

# Packages held to the coverage floor (AGENTS.md, testing policy item 8).
COVER_MIN ?= 85
COVER_PKGS ?= ./internal/domain/... ./internal/tmux ./internal/sanitize ./internal/hook ./internal/config \
	./internal/doctor ./internal/termx ./internal/devcontainer
FUZZTIME ?= 10s

.PHONY: build install test test-short golden fuzz cover bench lint fmt check tidy-check clean help

## build: compile bin/lyna-tmux
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/lyna-tmux

## install: install lyna-tmux into GOBIN
install:
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/lyna-tmux

## test: unit and integration tests with the race detector, shuffled
test:
	$(GO) test -race -shuffle=on -count=1 ./...

## test-short: unit tests only (skips tests that need tmux)
test-short:
	$(GO) test -short -count=1 ./...

## golden: regenerate golden files (review the diff afterwards)
golden:
	LYNA_TMUX_UPDATE_GOLDEN=1 $(GO) test -count=1 ./...

## fuzz: run every fuzz target for FUZZTIME
fuzz:
	GO="$(GO)" scripts/fuzz.sh "$(FUZZTIME)"

## cover: per-package coverage report and floor
cover:
	GO="$(GO)" scripts/cover.sh "$(COVER_MIN)" $(COVER_PKGS)

## bench: start-up benchmarks, written to docs/benchmarks.md
bench: build
	scripts/bench.sh --bin $(BIN) --output docs/benchmarks.md

## lint: formatting, vet, static analysis, shell scripts, workflows, text guard
lint: tidy-check
	$(GO) vet ./...
	golangci-lint run ./...
	shellcheck -x scripts/*.sh scripts/lib/*.sh lyna-tmux.tmux internal/devcontainer/templates/init-firewall.sh
	@if command -v actionlint >/dev/null 2>&1; then \
		actionlint; \
	else \
		echo "actionlint is not installed: skipping the workflow lint, CI runs it on a pinned version"; \
	fi
	scripts/check-text.sh

## fmt: apply gofumpt and import grouping
fmt:
	golangci-lint fmt ./...

## tidy-check: go.mod and go.sum are tidy and verified
tidy-check:
	$(GO) mod verify
	$(GO) mod tidy -diff

## check: the pre-commit gate
check: lint test fuzz cover

clean:
	rm -rf bin dist coverage

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
