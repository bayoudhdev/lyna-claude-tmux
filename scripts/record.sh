#!/usr/bin/env bash
# Records a documentation animation from a real terminal session.
#
# A scene file drives a throwaway tmux server of a fixed size: it runs a
# command, sends keys, and captures the screen at the moments worth showing.
# Every capture is drawn by a terminal emulator in headless Chrome, and the
# frames are assembled into an animated GIF. Nothing is simulated.
#
# Usage: scripts/record.sh --scene FILE [--out FILE.gif] [--still FILE.png]
#                          [--mp4 FILE.mp4] [--width PIXELS] [--fps N]
#                          [--redact FROM=TO]... [--dry-run] [--keep DIR]
#
# --redact rewrites every capture before it is drawn, which is how a recording
# made in a real home directory ships without naming it.
#
# Scene directives, one per line, # starts a comment:
#   title TEXT          window title drawn above the screen
#   size COLS ROWS      terminal size, default 112 34
#   dir PATH            working directory of the session
#   env NAME=VALUE      variable for the session, repeatable
#   pre COMMAND...      run before recording, repeatable, to reach a known
#                       starting point, for example lmux kill --all
#   run COMMAND...      the command the session runs, required, once
#   keys ARGS...        tmux send-keys arguments, for example M-a or C-c
#   type TEXT           the rest of the line typed literally
#   enter               a carriage return
#   wait SECONDS        let the screen settle, captures nothing
#   frame [SECONDS]     capture the screen, held SECONDS, default 1.2
#   film COUNT GAP      capture COUNT frames GAP seconds apart
set -euo pipefail

fail() {
  printf 'record.sh: %s\n' "$*" >&2
  exit 1
}

# usage_error is a command line or a scene file that cannot be read; the
# recorder has not started anything yet.
usage_error() {
  printf 'record.sh: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

usage() {
  sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=scripts/lib/termshot.sh
. "$here/lib/termshot.sh"

scene=""
out=""
still=""
mp4=""
width=900
fps=20
dry_run=0
keep=""
redactions=()
while (($# > 0)); do
  case $1 in
  --scene | --out | --still | --mp4 | --width | --fps | --keep | --redact)
    (($# >= 2)) || usage_error "$1 needs a value"
    case $1 in
    --scene) scene=$2 ;;
    --out) out=$2 ;;
    --still) still=$2 ;;
    --mp4) mp4=$2 ;;
    --width) width=$2 ;;
    --fps) fps=$2 ;;
    --keep) keep=$2 ;;
    --redact)
      [[ $2 == *=* ]] || usage_error "--redact takes FROM=TO"
      # A screen is laid out in columns, and a replacement of another length
      # pulls every status bar and box border after it out of line. It is also
      # the only way a value can be rewritten where the terminal wrapped it,
      # which is what keeps a home directory out of the picture.
      redact_from=${2%%=*} redact_to=${2#*=}
      ((${#redact_from} == ${#redact_to})) ||
        usage_error "--redact $redact_from is ${#redact_from} characters and $redact_to is ${#redact_to}: a replacement must be as long as what it replaces"
      redactions+=("$2")
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

[[ -n $scene ]] || usage_error "--scene is required"
[[ -f $scene ]] || usage_error "$scene is not a file"
[[ -n $out || -n $still || -n $mp4 ]] || usage_error "one of --out, --still or --mp4 is required"
[[ $width =~ ^[0-9]+$ ]] || usage_error "--width takes a whole number of pixels"
if ! [[ $fps =~ ^[1-9][0-9]*$ ]]; then usage_error "--fps takes a whole number of frames"; fi

# ---------------------------------------------------------------- scene file

title=""
cols=112
rows=34
dir=""
run=()
envs=()
pres=()
# steps are the timed directives, in order, one per line: the verb and its
# arguments. They are replayed once the session is up.
steps=()
line_no=0
while IFS= read -r line || [[ -n $line ]]; do
  line_no=$((line_no + 1))
  line=${line%%#*}
  # Strip the leading and trailing blanks without a subshell.
  line=${line#"${line%%[![:space:]]*}"}
  line=${line%"${line##*[![:space:]]}"}
  [[ -n $line ]] || continue
  verb=${line%%[[:space:]]*}
  rest=""
  if [[ $verb != "$line" ]]; then
    rest=${line#"$verb"}
    rest=${rest#"${rest%%[![:space:]]*}"}
  fi
  case $verb in
  title) title=$rest ;;
  size)
    # shellcheck disable=SC2086 # two whitespace separated numbers
    set -- $rest
    (($# == 2)) || usage_error "$scene:$line_no: size takes COLS and ROWS"
    [[ $1 =~ ^[0-9]+$ && $2 =~ ^[0-9]+$ ]] || usage_error "$scene:$line_no: size takes whole numbers"
    cols=$1 rows=$2
    ;;
  dir) dir=$rest ;;
  env)
    [[ $rest == *=* ]] || usage_error "$scene:$line_no: env takes NAME=VALUE"
    envs+=("$rest")
    ;;
  pre)
    [[ -n $rest ]] || usage_error "$scene:$line_no: pre takes a command"
    pres+=("$rest")
    ;;
  run)
    ((${#run[@]} == 0)) || usage_error "$scene:$line_no: run is allowed once"
    [[ -n $rest ]] || usage_error "$scene:$line_no: run takes a command"
    # shellcheck disable=SC2206 # the scene author writes shell words
    run=($rest)
    ;;
  keys | type | wait | frame | film | enter)
    steps+=("$verb${rest:+ $rest}")
    ;;
  *) usage_error "$scene:$line_no: unknown directive: $verb" ;;
  esac
done <"$scene"

((${#run[@]} > 0)) || usage_error "$scene: no run directive"
frames_wanted=0
for step in ${steps[@]+"${steps[@]}"}; do
  case $step in
  "frame"*) frames_wanted=$((frames_wanted + 1)) ;;
  "film "*)
    # shellcheck disable=SC2086 # count and gap
    set -- ${step#film }
    (($# >= 2)) || usage_error "$scene: film takes COUNT and GAP"
    [[ $1 =~ ^[0-9]+$ ]] || usage_error "$scene: film takes a whole COUNT"
    frames_wanted=$((frames_wanted + $1))
    ;;
  esac
done
((frames_wanted > 0)) || usage_error "$scene: no frame or film directive"

if ((dry_run)); then
  printf 'scene: %s\n' "$scene"
  printf 'title: %s\n' "$title"
  printf 'size: %s %s\n' "$cols" "$rows"
  printf 'dir: %s\n' "$dir"
  for e in ${envs[@]+"${envs[@]}"}; do printf 'env: %s\n' "$e"; done
  for p in ${pres[@]+"${pres[@]}"}; do printf 'pre: %s\n' "$p"; done
  for r in ${redactions[@]+"${redactions[@]}"}; do printf 'redact: %s\n' "$r"; done
  printf 'run: %s\n' "${run[*]}"
  printf 'frames: %s\n' "$frames_wanted"
  for step in ${steps[@]+"${steps[@]}"}; do printf 'step: %s\n' "$step"; done
  exit 0
fi

# ------------------------------------------------------------------ recording

command -v tmux >/dev/null 2>&1 || fail "tmux is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
chrome=$(termshot_chrome) || fail "no Chrome found; set CHROME_PATH"
# The patched font is what draws the status bar separators and the nerd icon
# set; without it those glyphs would be boxes, so a recording stops here
# rather than shipping a picture of them.
font=$(termshot_font) || fail "could not get the patched font; set LYNA_TMUX_RECORD_FONT to a local .ttf"
if [[ -n $out || -n $mp4 ]]; then
  command -v ffmpeg >/dev/null 2>&1 || fail "ffmpeg is required to assemble an animation"
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/lyna-tmux-rec.XXXXXX")
socket=lt-rec-$$
cleanup() {
  tmux -L "$socket" kill-server >/dev/null 2>&1 || true
  if [[ -n $keep ]]; then
    mkdir -p "$keep"
    cp -R "$work/." "$keep/"
  fi
  rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/frames"
termshot_conf "$work/tmux.conf"

# The scene reaches its starting point before anything is captured; a step
# that fails here is a scene that no longer matches the tool it records.
for p in ${pres[@]+"${pres[@]}"}; do
  # shellcheck disable=SC2086 # the scene author writes shell words
  (
    if [[ -n $dir ]]; then cd "$dir"; fi
    set -- $p
    "$@"
  ) || fail "pre step failed: $p"
done

new_session=(tmux -L "$socket" -f "$work/tmux.conf" new-session -d -x "$cols" -y "$rows" -s shot)
if [[ -n $dir ]]; then new_session+=(-c "$dir"); fi
new_session+=(-e COLORTERM=truecolor -e LYNA_TMUX_SHOT=1)
for e in ${envs[@]+"${envs[@]}"}; do new_session+=(-e "$e"); done
# The pane must outlive a command that prints and exits, so the screen is
# still there to capture; the shell keeps it open and the trap kills it.
quoted=$(printf '%q ' "${run[@]}")
new_session+=("$quoted; sleep 900")
"${new_session[@]}"

frame_no=0
durations=()
capture_frame() {
  frame_no=$((frame_no + 1))
  local file
  file=$(printf '%s/frames/%04d.ansi' "$work" "$frame_no")
  termshot_capture "$socket" shot "$file"
  if ((${#redactions[@]} > 0)); then
    python3 - "$file" "${redactions[@]}" <<'PY'
import re
import sys

path = sys.argv[1]
with open(path, "rb") as f:
    data = f.read().decode("utf-8", "replace")
table = dict(rule.partition("=")[::2] for rule in sys.argv[2:])

# A capture is a grid: the terminal breaks a path that reaches the right edge
# over two rows, and a rule read line by line would walk straight past it. The
# printable characters are gathered into one string, with the position each one
# came from, so a value is found wherever the screen happens to have split it.
escape = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b.")
chars = list(data)
flat = []
where = []
i = 0
while i < len(data):
    m = escape.match(data, i)
    if m:
        i = m.end()
        continue
    if data[i] not in "\n\r":
        flat.append(data[i])
        where.append(i)
    i += 1
flat = "".join(flat)

# Every replacement is as long as what it replaces, so it is written over those
# characters and each one keeps its column. Longest rule first, over the text as
# it was captured: what a rule writes is never read by another.
if table:
    rules = re.compile("|".join(re.escape(src) for src in sorted(table, key=len, reverse=True)))
    for m in rules.finditer(flat):
        for offset, ch in enumerate(table[m.group(0)]):
            chars[where[m.start() + offset]] = ch
data = "".join(chars)

with open(path, "w") as f:
    f.write(data)
PY
  fi
  durations+=("$1")
}

for step in ${steps[@]+"${steps[@]}"}; do
  verb=${step%%[[:space:]]*}
  rest=${step#"$verb"}
  rest=${rest#"${rest%%[![:space:]]*}"}
  case $verb in
  keys)
    # shellcheck disable=SC2086 # send-keys arguments come from the scene
    tmux -L "$socket" send-keys -t shot $rest
    ;;
  type) tmux -L "$socket" send-keys -t shot -l -- "$rest" ;;
  enter) tmux -L "$socket" send-keys -t shot Enter ;;
  wait) sleep "${rest:-1}" ;;
  frame) capture_frame "${rest:-1.2}" ;;
  film)
    # shellcheck disable=SC2086 # count and gap
    set -- $rest
    count=$1 gap=$2
    for ((i = 0; i < count; i++)); do
      capture_frame "$gap"
      if ((i + 1 < count)); then sleep "$gap"; fi
    done
    ;;
  esac
done
tmux -L "$socket" kill-server >/dev/null 2>&1 || true

# A scene that captured the command line's own error banner is a scene that no
# longer matches the tool it records, and the picture would ship the error
# instead of the screen. The frames are read before anything is drawn, so such
# a scene fails here rather than in the documentation.
if ! bad=$(python3 - "$work"/frames/*.ansi <<'PY'
import pathlib
import re
import sys

# The same escapes the redaction strips: what is left is what the screen says.
escape = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b.")
bad = []
for path in sys.argv[1:]:
    text = escape.sub("", pathlib.Path(path).read_text("utf-8", "replace"))
    if any(line.strip() == "ERROR" for line in text.splitlines()):
        bad.append(pathlib.Path(path).name)
if bad:
    print(", ".join(bad))
    sys.exit(1)
PY
); then
  fail "the command failed on screen in $bad: the scene no longer matches the tool it records"
fi

# ------------------------------------------------------------------ rendering

# Chrome starts once per frame, so frames render in batches rather than one
# after another; four at a time keeps a laptop responsive.
pending=0
for ((n = 1; n <= frame_no; n++)); do
  ansi=$(printf '%s/frames/%04d.ansi' "$work" "$n")
  html=$(printf '%s/frames/%04d.html' "$work" "$n")
  png=$(printf '%s/frames/%04d.png' "$work" "$n")
  termshot_html "$ansi" "$cols" "$rows" "$title" "$html" "$work" "$font"
  termshot_png "$chrome" "$html" "$cols" "$rows" "$png" &
  pending=$((pending + 1))
  if ((pending >= 4)); then
    # A frame that failed to render is reported below, by name.
    wait || true
    pending=0
  fi
done
wait || true
for ((n = 1; n <= frame_no; n++)); do
  png=$(printf '%s/frames/%04d.png' "$work" "$n")
  [[ -s $png ]] || fail "Chrome wrote no image for frame $n"
done

write_to() {
  mkdir -p "$(dirname "$1")"
  cp "$2" "$1"
  printf 'wrote %s\n' "$1"
}

if [[ -n $still ]]; then
  write_to "$still" "$(printf '%s/frames/%04d.png' "$work" "$frame_no")"
fi

if [[ -n $out || -n $mp4 ]]; then
  : >"$work/frames.txt"
  for ((n = 1; n <= frame_no; n++)); do
    printf "file '%s'\nduration %s\n" "$(printf '%s/frames/%04d.png' "$work" "$n")" "${durations[n - 1]}" >>"$work/frames.txt"
  done
  # The concat demuxer ignores the duration of the last entry, so it is named
  # twice and the animation ends on the screen it should end on.
  printf "file '%s'\n" "$(printf '%s/frames/%04d.png' "$work" "$frame_no")" >>"$work/frames.txt"
fi

if [[ -n $out ]]; then
  ffmpeg -y -loglevel error -f concat -safe 0 -i "$work/frames.txt" \
    -filter_complex "fps=$fps,scale=$width:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=3" \
    -loop 0 "$work/out.gif"
  write_to "$out" "$work/out.gif"
fi

if [[ -n $mp4 ]]; then
  ffmpeg -y -loglevel error -f concat -safe 0 -i "$work/frames.txt" \
    -vf "fps=$fps,scale=$width:-2:flags=lanczos,format=yuv420p" \
    -movflags +faststart "$work/out.mp4"
  write_to "$mp4" "$work/out.mp4"
fi
