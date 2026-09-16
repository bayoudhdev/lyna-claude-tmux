#!/usr/bin/env bash
# Enforces a per-package statement coverage floor.
# Usage: scripts/cover.sh <min-percent> <package pattern>...
# A package without tests fails: every gated package must be tested.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
go_bin="${GO:-go}"

if (($# < 2)); then
	echo "usage: $0 <min-percent> <package>..." >&2
	exit 2
fi
min="$1"
shift

mkdir -p coverage
out="$("$go_bin" test -count=1 -coverprofile=coverage/cover.out -covermode=atomic "$@" 2>&1)" || {
	echo "$out"
	exit 1
}
echo "$out"

# Untested packages print as "?  pkg [no test files]" or, with a cover
# profile, as an indented "pkg coverage: 0.0% of statements" with no "ok".
awk -v min="$min" '
	/^\?/ && /no test files/ { printf "cover: %s has no tests\n", $2; bad = 1; next }
	/^[ \t]/ && /coverage:/ { printf "cover: %s has no tests\n", $1; bad = 1; next }
	/^ok/ {
		pct = ""
		for (i = 1; i <= NF; i++) if ($i == "coverage:") pct = $(i + 1)
		if (pct == "") next
		sub("%", "", pct)
		if (pct + 0 < min + 0) { printf "cover: %s at %s%% is below %s%%\n", $2, pct, min; bad = 1 }
	}
	END { exit bad }
' <<<"$out" || {
	echo "cover: floor of ${min}% not met" >&2
	exit 1
}
echo "cover: every package at or above ${min}%"
