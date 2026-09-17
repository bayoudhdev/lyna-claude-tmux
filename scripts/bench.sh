#!/usr/bin/env bash
# Measures the start-up cost of lyna-tmux commands that run on every tmux
# redraw or Claude hook, against a Go hello world as the floor, and prints a
# Markdown table for docs/benchmarks.md.
#
# Usage: scripts/bench.sh [--dry-run] [--bin PATH] [--runs N] [--output FILE]
#
# GO and HYPERFINE name the go and hyperfine commands to use when they are
# not the ones on PATH.
#
# The commands run outside tmux (TMUX and TMUX_PANE unset) with scratch state
# directories, so `hook Stop` takes its no-op path and nothing touches a real
# tmux server or Claude configuration.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/bench.sh [--dry-run] [--bin PATH] [--runs N] [--output FILE]

Benchmarks lyna-tmux start-up with hyperfine and prints a Markdown table.

Options:
  --dry-run      print the commands without running them
  --bin PATH     benchmark this lmux binary instead of building one
  --runs N       exact number of runs per command (default: hyperfine decides, at least 50)
  --output FILE  write the Markdown to FILE instead of standard output
  -h, --help     show this help

Environment:
  GO             the go command to use (default: go)
  HYPERFINE      the hyperfine command to use (default: hyperfine)
EOF
}

fail() {
  printf 'bench.sh: %s\n' "$*" >&2
  exit 1
}

usage_error() {
  printf 'bench.sh: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

dry_run=0
bin=""
runs=""
output=""
while (($# > 0)); do
  case $1 in
  --dry-run)
    dry_run=1
    shift
    ;;
  --bin | --runs | --output)
    (($# >= 2)) || usage_error "$1 needs a value"
    case $1 in
    --bin) bin=$2 ;;
    --runs) runs=$2 ;;
    --output) output=$2 ;;
    esac
    shift 2
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *) usage_error "unknown argument: $1" ;;
  esac
done

if [[ -n $runs ]] && ! [[ $runs =~ ^[0-9]+$ && $runs -ge 2 ]]; then
  usage_error "--runs must be a whole number of at least 2"
fi
if ((!dry_run)) && [[ -n $bin && ! -x $bin ]]; then
  fail "$bin is not an executable file"
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_bin=${GO:-go}
hyperfine_bin=${HYPERFINE:-hyperfine}

# require fails, naming the tool, when the command standing for it cannot be
# found. Only the tools beyond the POSIX base are looked for: the go
# toolchain and hyperfine, the two a machine may lack.
require() {
  local tool=$1 cmd=$2 hint=$3
  command -v "$cmd" >/dev/null 2>&1 && return
  if [[ $cmd == "$tool" ]]; then
    fail "$tool is required$hint"
  fi
  fail "$tool is required$hint: $cmd not found"
}

# word quotes one argument for a shell, and for the command strings hyperfine
# splits itself when it runs without a shell. Plain words stay bare.
word() {
  if [[ $1 =~ ^[A-Za-z0-9_./:=@%+,-]+$ ]]; then
    printf '%s' "$1"
    return
  fi
  local q="'\\''"
  printf "'%s'" "${1//\'/$q}"
}

# show prints one command for --dry-run.
show() {
  local out="" arg
  for arg in "$@"; do
    out+="${out:+ }$(word "$arg")"
  done
  printf '%s\n' "$out"
}

# run executes a command, or prints it with --dry-run.
run() {
  if ((dry_run)); then
    show "$@"
  else
    "$@"
  fi
}

tmp_root=${TMPDIR:-/tmp}
if ((dry_run)); then
  # The scratch directory is only created for a real run.
  work=${tmp_root%/}/lyna-tmux-bench.XXXXXX
else
  # Every tool is checked before the scratch directory exists, so a missing
  # one is reported as such rather than as a failed build, and leaves
  # nothing behind.
  require go "$go_bin" " (set GO to the toolchain to use)"
  require hyperfine "$hyperfine_bin" " (brew install hyperfine, apt-get install hyperfine or cargo install hyperfine)"
  work=$(mktemp -d "${tmp_root%/}/lyna-tmux-bench.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  mkdir -p "$work/hello" "$work/home" "$work/claude"
  cat >"$work/hello/main.go" <<'EOF'
package main

import "fmt"

func main() { fmt.Println("hello") }
EOF
  cat >"$work/hook-stop.json" <<EOF
{"session_id":"bench","transcript_path":"","cwd":"$work","hook_event_name":"Stop","stop_hook_active":false}
EOF
  cat >"$work/statusline.json" <<EOF
{"session_id":"bench","cwd":"$work","model":{"id":"bench-model","display_name":"Bench"},"workspace":{"current_dir":"$work","project_dir":"$work"},"output_style":{"name":"default"},"cost":{"total_cost_usd":0.42,"total_duration_ms":65000,"total_lines_added":12,"total_lines_removed":3},"context_window":{"used_percentage":37,"context_window_size":200000},"version":"bench"}
EOF
fi

ldflags="-s -w"
run "$go_bin" build -trimpath -ldflags "$ldflags" -o "$work/hello-go" "$work/hello/main.go"
if [[ -z $bin ]]; then
  bin=$work/lmux
  run "$go_bin" -C "$root" build -trimpath -ldflags "$ldflags" -o "$bin" ./cmd/lmux
fi

names=("go hello world" "lmux version" "lmux hook Stop" "lmux statusline")
commands=(
  "$(word "$work/hello-go")"
  "$(word "$bin") version"
  "$(word "$bin") hook Stop"
  "$(word "$bin") statusline"
)
inputs=("" "" "$work/hook-stop.json" "$work/statusline.json")

run_flags=(--min-runs 50)
if [[ -n $runs ]]; then
  run_flags=(--runs "$runs")
fi

for i in "${!names[@]}"; do
  args=("$hyperfine_bin" --shell=none --warmup 10 "${run_flags[@]}" --export-csv "$work/bench-$i.csv" --command-name "${names[$i]}")
  if [[ -n ${inputs[$i]} ]]; then
    args+=(--input "${inputs[$i]}")
  fi
  args+=("${commands[$i]}")
  # hyperfine's own report goes to standard error; standard output is the table.
  if ((dry_run)); then
    show env -u TMUX -u TMUX_PANE LYNA_TMUX_HOME="$work/home" CLAUDE_CONFIG_DIR="$work/claude" "${args[@]}"
  else
    env -u TMUX -u TMUX_PANE LYNA_TMUX_HOME="$work/home" CLAUDE_CONFIG_DIR="$work/claude" "${args[@]}" >&2
  fi
done

((dry_run)) && exit 0

# table turns the per-command CSV exports (seconds) into one Markdown table in
# milliseconds, relative to the first command.
table() {
  printf '| Command | Mean [ms] | Min [ms] | Max [ms] | User [ms] | System [ms] | Relative |\n'
  printf '|:---|---:|---:|---:|---:|---:|---:|\n'
  local i
  for i in "${!names[@]}"; do
    tail -n +2 "$work/bench-$i.csv"
  done | awk -F, '
    NR == 1 { base = $2 }
    {
      printf "| `%s` | %.1f ± %.1f | %.1f | %.1f | %.1f | %.1f | %.2f |\n",
        $1, $2 * 1000, $3 * 1000, $7 * 1000, $8 * 1000, $5 * 1000, $6 * 1000, $2 / base
    }'
}

report() {
  local version
  version=$("$bin" version 2>/dev/null | head -n 1) || version="unknown"
  # The machine comes from the Go toolchain, not uname: a shell translated by
  # Rosetta reports the translated architecture, not the one that runs here.
  printf '%s\n\n' "Measured with $("$hyperfine_bin" --version) on $(uname -s) $("$go_bin" env GOARCH), $("$go_bin" version | cut -d' ' -f3), lmux ${version#lmux }."
  table
}

if [[ -n $output ]]; then
  report >"$output"
else
  report
fi
