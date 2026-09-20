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

# The chapters run with the recording shell and the fixture binaries the
# documentation scenes use, which live beside them rather than beside the
# chapters.
driver_args=(--project "$project" --scenes "$scenes" --fixtures "$root/docs/scenes"
  --assets "$work" --film --width "$width" --fps "$fps")
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

# stamp_awk is the arithmetic both steps below need: a time in seconds read
# from a cue, and a time written back the way a subtitle track spells it.
stamp_awk='
function secs(t,  p) { gsub(",", ".", t); split(t, p, ":"); return p[1] * 3600 + p[2] * 60 + p[3] }
function stamp(s,  ms, h, m, sec) {
  ms = int(s * 1000 + 0.5)
  h = int(ms / 3600000); ms %= 3600000
  m = int(ms / 60000); ms %= 60000
  sec = int(ms / 1000); ms %= 1000
  return sprintf("%02d:%02d:%02d,%03d", h, m, sec, ms)
}'

# Every cue of a chapter moves by the seconds the chapters before it are worth.
# The number a cue carries is dropped here and written again at the end, so the
# track is numbered from one over the whole film.
: >"$work/shifted.srt"
while IFS=$'\t' read -r name offset path; do
  [[ -s $path ]] || continue
  awk -v offset="$offset" "$stamp_awk"'
  /-->/ {
    split($0, t, " --> ")
    print stamp(secs(t[1]) + offset) " --> " stamp(secs(t[2]) + offset)
    blank = 0
    next
  }
  /^[0-9]+$/ && (FNR == 1 || blank) { blank = 0; next }
  { print; blank = ($0 ~ /^[[:space:]]*$/) }
  ' "$path" >>"$work/shifted.srt"
done <"$work/offsets.txt"

awk 'BEGIN { RS = ""; ORS = "\n\n" } { print ++n "\n" $0 }' "$work/shifted.srt" >"$work/demo.srt"

# The chapter list: every mark the recorder printed, moved by the offset of the
# chapter it was printed under. The log names a chapter before it records it,
# so a mark belongs to the last name seen.
awk -F'\t' "$stamp_awk"'
FNR == NR { offset[$1] = $2 + 0; next }
/^[0-9A-Za-z._-]+: [0-9]+ frames$/ {
  scene = $0
  sub(/:.*/, "", scene)
  next
}
$1 ~ /^chapter: [0-9.]+$/ && (scene in offset) {
  at = $1
  sub(/^chapter: /, "", at)
  print stamp(offset[scene] + at) "\t" $2
}
' "$work/offsets.txt" "$work/record.log"

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
