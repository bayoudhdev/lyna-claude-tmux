#!/usr/bin/env bash
# Records the demonstration film: every chapter, then the film they make.
#
# Each scene of --scenes is a chapter. They are recorded one after another by
# scripts/record-docs.sh --film, which gives them the environment every other
# recording has: a configuration and a tmux server of their own, the fixture
# binaries, and the home directory, account name and git address rewritten to
# neutral ones before a frame is drawn.
#
# The chapters are then joined without being encoded again, their subtitles
# are shifted by the second each chapter starts at, and the chapter list is
# printed for docs/video.md. A chapter also stays on its own, so a page can
# embed a single feature.
#
# Every chapter must be recorded at the same terminal size: the film is one
# picture from beginning to end, and joining frames of two sizes is refused.
#
# Usage: scripts/record-video.sh --project DIR [--bin PATH] [--scenes DIR]
#                                [--assets DIR] [--out FILE] [--home NAME]
#                                [--redact FROM=TO]... [--only NAME]...
#                                [--width PIXELS] [--fps N] [--dry-run]
set -euo pipefail

fail() {
  printf 'record-video.sh: %s\n' "$*" >&2
  exit 1
}

usage_error() {
  printf 'record-video.sh: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

usage() {
  sed -n '2,21p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)

project=""
bin=""
scenes=$root/docs/video
assets=$root/docs/assets
out=""
driver=$here/record-docs.sh
width=1280
fps=20
demo_home=""
only=()
extra=()
dry_run=0
while (($# > 0)); do
  case $1 in
  --project | --bin | --scenes | --assets | --out | --driver | --only | --home | --redact | --width | --fps)
    (($# >= 2)) || usage_error "$1 needs a value"
    case $1 in
    --project) project=$2 ;;
    --bin) bin=$2 ;;
    --scenes) scenes=$2 ;;
    --assets) assets=$2 ;;
    --out) out=$2 ;;
    --driver) driver=$2 ;;
    --only) only+=("$2") ;;
    --home) demo_home=$2 ;;
    --width) width=$2 ;;
    --fps) fps=$2 ;;
    --redact)
      [[ $2 == *=* ]] || usage_error "--redact takes FROM=TO"
      extra+=(--redact "$2")
      ;;
    esac
    shift 2
    ;;
  --dry-run)
    dry_run=1
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *) usage_error "unknown argument: $1" ;;
  esac
done

[[ -n $project ]] || usage_error "--project is required"
[[ -d $scenes ]] || fail "$scenes is not a directory"
[[ -x $driver ]] || fail "$driver is not executable"
[[ -n $out ]] || out=$assets/demo.mp4
command -v ffmpeg >/dev/null 2>&1 || fail "ffmpeg is required to join the chapters"
command -v ffprobe >/dev/null 2>&1 || fail "ffprobe is required to time the chapters"

chapters=()
for scene in "$scenes"/*.scene; do
  [[ -e $scene ]] || fail "$scenes holds no scene"
  name=$(basename "$scene" .scene)
  if ((${#only[@]} > 0)); then
    wanted=0
    for pick in "${only[@]}"; do
      if [[ $pick == "$name" || $pick == "$name.scene" ]]; then wanted=1; fi
    done
    ((wanted)) || continue
  fi
  chapters+=("$name")
done
((${#chapters[@]} > 0)) || fail "no chapter matched"

work=$(mktemp -d "${TMPDIR:-/tmp}/lyna-tmux-film.XXXXXX")
trap 'rm -rf "$work"' EXIT

driver_args=(--project "$project" --scenes "$scenes" --assets "$work"
  --film --width "$width" --fps "$fps")
if [[ -n $bin ]]; then driver_args+=(--bin "$bin"); fi
if [[ -n $demo_home ]]; then driver_args+=(--home "$demo_home"); fi
for name in "${chapters[@]}"; do driver_args+=(--only "$name"); done
driver_args+=(${extra[@]+"${extra[@]}"})
if ((dry_run)); then
  driver_args+=(--dry-run)
  printf '%s %s\n' "$driver" "${driver_args[*]}"
  for name in "${chapters[@]}"; do printf 'chapter: %s\n' "$name"; done
  exit 0
fi

"$driver" "${driver_args[@]}" | tee "$work/record.log"

# ------------------------------------------------------------------- joining

# seconds prints the duration of the film $1, which is where the one after it
# starts.
seconds() {
  ffprobe -v error -show_entries format=duration -of default=nk=1:nw=1 "$1"
}

height=""
: >"$work/list.txt"
for name in "${chapters[@]}"; do
  segment=$work/$name.mp4
  [[ -s $segment ]] || fail "$name: the recorder wrote no film"
  size=$(ffprobe -v error -select_streams v:0 -show_entries stream=height -of default=nk=1:nw=1 "$segment")
  if [[ -z $height ]]; then
    height=$size
  elif [[ $size != "$height" ]]; then
    fail "$name is $size pixels tall and the chapters before it are $height: every scene of the film needs the same size"
  fi
  printf "file '%s'\n" "$segment" >>"$work/list.txt"
done

ffmpeg -y -loglevel error -f concat -safe 0 -i "$work/list.txt" -c copy "$work/demo.mp4"

# The cues of a chapter are written for a film that starts at zero, so each
# one is moved by the seconds the chapters before it are worth. The marks the
# recorder printed move the same way, and become the chapter list.
: >"$work/offsets.txt"
offset=0
for name in "${chapters[@]}"; do
  printf '%s\t%s\t%s\n' "$name" "$offset" "$work/$name.srt" >>"$work/offsets.txt"
  offset=$(awk -v a="$offset" -v b="$(seconds "$work/$name.mp4")" 'BEGIN { printf "%.3f", a + b }')
done

python3 - "$work/offsets.txt" "$work/record.log" "$work/demo.srt" <<'SHIFT'
import io
import re
import sys

rows = [line.split("\t") for line in io.open(sys.argv[1], encoding="utf-8").read().splitlines() if line]
marks = io.open(sys.argv[2], encoding="utf-8").read().splitlines()
stamp = re.compile(r"(\d\d):(\d\d):(\d\d),(\d\d\d)")


def to_seconds(text):
    h, m, s, ms = (int(part) for part in stamp.match(text).groups())
    return h * 3600 + m * 60 + s + ms / 1000.0


def to_stamp(seconds):
    ms = int(round(seconds * 1000))
    h, ms = divmod(ms, 3600000)
    m, ms = divmod(ms, 60000)
    s, ms = divmod(ms, 1000)
    return "%02d:%02d:%02d,%03d" % (h, m, s, ms)


cues = []
for name, offset, path in rows:
    offset = float(offset)
    try:
        text = io.open(path, encoding="utf-8").read()
    except OSError:
        continue
    for block in text.strip().split("\n\n"):
        lines = block.splitlines()
        if len(lines) < 3:
            continue
        start, _, end = lines[1].partition(" --> ")
        cues.append((to_seconds(start) + offset, to_seconds(end) + offset, lines[2:]))

with io.open(sys.argv[3], "w", encoding="utf-8") as f:
    for i, (start, end, body) in enumerate(cues, 1):
        f.write("%d\n%s --> %s\n%s\n\n" % (i, to_stamp(start), to_stamp(end), "\n".join(body)))

# The chapter list: every mark the recorder printed, moved by the offset of
# the chapter it was printed under. The log names a chapter before it records
# it, so a mark belongs to the last name seen.
offsets = {name: float(offset) for name, offset, _ in rows}
current = None
for line in marks:
    scene = re.match(r"^([0-9A-Za-z._-]+): \d+ frames$", line)
    if scene:
        current = scene.group(1)
        continue
    mark = re.match(r"^chapter: ([0-9.]+)\t(.*)$", line)
    if mark and current in offsets:
        print("%s\t%s" % (to_stamp(offsets[current] + float(mark.group(1))), mark.group(2)))
SHIFT

write_to() {
  mkdir -p "$(dirname "$1")"
  cp "$2" "$1"
  printf 'wrote %s\n' "$1"
}

write_to "$out" "$work/demo.mp4"
write_to "${out%.mp4}.srt" "$work/demo.srt"
for name in "${chapters[@]}"; do
  write_to "$assets/demo-$name.mp4" "$work/$name.mp4"
done
printf 'the film is %s seconds long\n' "$(seconds "$work/demo.mp4")"
