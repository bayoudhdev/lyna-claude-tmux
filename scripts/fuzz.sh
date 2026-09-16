#!/usr/bin/env bash
# Runs every Fuzz* target in the module for a fixed time each.
# Usage: scripts/fuzz.sh [duration]   (default 10s)
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
go_bin="${GO:-go}"
fuzztime="${1:-10s}"

count=0
while IFS= read -r -d '' file; do
	dir="./$(dirname "$file")"
	while IFS= read -r target; do
		echo "==> $dir $target ($fuzztime)"
		"$go_bin" test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$dir"
		count=$((count + 1))
	done < <(sed -n 's/^func \(Fuzz[A-Za-z0-9_]*\)(.*/\1/p' "$file")
done < <(git ls-files -z --cached --others --exclude-standard -- '*_test.go')

if ((count == 0)); then
	echo "fuzz: no fuzz targets found" >&2
	exit 1
fi
echo "fuzz: $count targets passed"
