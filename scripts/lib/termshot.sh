# shellcheck shell=bash
# Shared terminal-to-image rendering for scripts/screenshot.sh and
# scripts/record.sh.
#
# A frame is captured from a real tmux pane with its colors, then drawn by a
# terminal emulator in headless Chrome and saved as a PNG. Nothing is
# simulated: the image is what the terminal shows.

# termshot_cell_width and termshot_cell_height are Menlo at 15px, in tenths of
# a pixel; the rest is the frame padding and the title bar, so an image ends
# where the terminal does.
termshot_cell_width=92
termshot_cell_height=187
termshot_pad_width=50
termshot_pad_height=84

# termshot_band_height is the caption band a film reserves under the terminal,
# in whole pixels: two lines of subtitle and the space around them. The band
# is drawn inside the frame rather than over the screen, so a caption never
# covers the status bar, and it is reserved for every frame of a scene that
# captions anything, because a film has one size throughout.
termshot_band_height=104

# The frames are drawn with a patched font, so the separators of the status bar
# and the nerd icon set appear as they do in a terminal that has one. It is the
# Meslo Nerd Font, whose advance width is the Menlo one the image sizes above
# were measured from, pinned to a release tag and a checksum and downloaded
# once into the cache.
termshot_font_url="https://raw.githubusercontent.com/ryanoasis/nerd-fonts/v3.4.0/patched-fonts/Meslo/S/Regular/MesloLGSNerdFontMono-Regular.ttf"
termshot_font_sha256=3cb52e923ca3981cecca9ea7307186e424e8521cbc5643fd0b5b5b1d7daa53d9

# termshot_font_ok reports whether the file $1 is the pinned font.
termshot_font_ok() {
  local sum
  [[ -s $1 ]] || return 1
  sum=$(shasum -a 256 "$1" | cut -d" " -f1) || return 1
  [[ $sum == "$termshot_font_sha256" ]]
}

# termshot_font prints the path of the patched font, downloading it the first
# time. LYNA_TMUX_RECORD_FONT names a local file to use instead, which is how a
# machine without network access records.
termshot_font() {
  local dir path
  if [[ -n ${LYNA_TMUX_RECORD_FONT:-} ]]; then
    [[ -s ${LYNA_TMUX_RECORD_FONT} ]] || return 1
    printf '%s\n' "$LYNA_TMUX_RECORD_FONT"
    return 0
  fi
  dir=${XDG_CACHE_HOME:-$HOME/.cache}/lyna-tmux/recorder
  path=$dir/MesloLGSNerdFontMono-Regular.ttf
  if termshot_font_ok "$path"; then
    printf '%s\n' "$path"
    return 0
  fi
  command -v curl >/dev/null 2>&1 || return 1
  mkdir -p "$dir" || return 1
  curl -fsSL --max-time 180 -o "$path.part" "$termshot_font_url" || { rm -f "$path.part"; return 1; }
  if ! termshot_font_ok "$path.part"; then
    rm -f "$path.part"
    return 1
  fi
  mv "$path.part" "$path" || return 1
  printf '%s\n' "$path"
}

# termshot_chrome prints the path of a Chrome to render with, from CHROME_PATH
# or the usual install locations. It fails when there is none.
termshot_chrome() {
  if [[ -n ${CHROME_PATH:-} ]]; then
    printf '%s\n' "$CHROME_PATH"
    return 0
  fi
  local candidate
  for candidate in \
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    "$(command -v google-chrome || true)" \
    "$(command -v chromium || true)"; do
    if [[ -n $candidate && -x $candidate ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

# termshot_conf writes the configuration of a recording tmux server to $1. A
# capture is only as colorful as the terminal the program believed it had, so
# the server advertises 24-bit color, and its own status bar is off because the
# screen being recorded draws one.
termshot_conf() {
  cat >"$1" <<'CONF'
set -g default-terminal "tmux-256color"
set -sa terminal-features ",tmux-256color:RGB"
set -g status off
set -g escape-time 10
set -g focus-events on
CONF
}

# termshot_capture writes the screen of pane $2 on server $1 to the file $3.
# -N keeps the trailing spaces of every line, so a status bar or a pane
# background reaches the edge of the image the way it does on screen. The
# capture ends in whatever style its last cell had, and an emulator paints
# everything after it in that style, so the reset ends the screen where the
# screen ends.
termshot_capture() {
  local screen
  # The capture ends with a newline, which would open a row past the screen,
  # and the emulator would fill that row with the background the last cell
  # had. The substitution drops it, and the reset ends the style with the
  # screen so nothing after it is painted in the color of the clock.
  screen=$(tmux -L "$1" capture-pane -e -p -N -t "$2")
  printf '%s\033[0m' "$screen" >"$3"
}

# termshot_json writes the capture $1 to $2 as a JSON string, so the emulator
# receives it byte for byte, escapes and all.
termshot_json() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "rb") as f:
    data = f.read().decode("utf-8", "replace")
with open(sys.argv[2], "w") as f:
    json.dump(data, f)
PY
}

# termshot_caption_html prints the caption $1 as the two spans the band
# draws: the first line is the command as it is typed, the second says what it
# does. The text comes from a scene file, so it is escaped rather than trusted.
termshot_caption_html() {
  python3 - "$1" <<'CAPTION'
import html
import sys

lines = [line for line in sys.argv[1].split("\n") if line.strip()]
classes = ["cmd", "say"]
out = []
for i, line in enumerate(lines[:2]):
    out.append('<div class="%s">%s</div>' % (classes[i], html.escape(line)))
print("".join(out))
CAPTION
}

# termshot_html writes a page that draws the capture $1 at $2 columns by $3
# rows under the window title $4, to the file $5. $6 is a scratch directory,
# and $7 the patched font the page draws with; the font is copied next to the
# page so its URL needs no quoting, whatever the directory is called. $8 is 1
# to reserve the caption band under the terminal, and $9 the caption itself,
# whose two lines are separated by a newline.
termshot_html() {
  local ansi=$1 cols=$2 rows=$3 title=$4 out=$5 work=$6 font=${7:-} band=${8:-0} caption=${9:-}
  local json family="Menlo, ui-monospace, monospace" face="" band_html=""
  json=$work/$(basename "$out").json
  if [[ -n $font ]]; then
    cp -f "$font" "$(dirname "$out")/font.ttf"
    family="LynaRecorder, Menlo, ui-monospace, monospace"
    face='@font-face { font-family: "LynaRecorder"; src: url("font.ttf") format("truetype"); }'
  fi
  if ((band)); then
    band_html="<div id=\"caption\">$(termshot_caption_html "$caption")</div>"
  fi
  termshot_json "$ansi" "$json"
  cat >"$out" <<HTML
<!doctype html>
<meta charset="utf-8">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.css">
<style>
  $face
  body { margin: 0; background: #0d1117; font-family: ui-monospace, Menlo, monospace; }
  #frame { padding: 22px; background: #0d1117; display: inline-block; border-radius: 10px; }
  #bar { height: 26px; display: flex; align-items: center; gap: 8px; padding: 0 6px 12px; }
  .dot { width: 12px; height: 12px; border-radius: 50%; }
  #title { color: #7d8590; font-size: 13px; margin-left: 8px; }
  .xterm-viewport { overflow: hidden !important; }
  #caption {
    height: ${termshot_band_height}px;
    box-sizing: border-box;
    padding: 18px 10px 0;
    display: flex;
    flex-direction: column;
    justify-content: flex-start;
    gap: 6px;
    font-family: $family;
    text-align: center;
  }
  #caption .cmd { color: #a4e400; font-size: 19px; font-weight: 700; }
  #caption .say { color: #e6e6e6; font-size: 17px; }
</style>
<div id="frame">
  <div id="bar">
    <span class="dot" style="background:#ff5f56"></span>
    <span class="dot" style="background:#ffbd2e"></span>
    <span class="dot" style="background:#27c93f"></span>
    <span id="title"></span>
  </div>
  <div id="term"></div>
  $band_html
</div>
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.js"></script>
<script>
  document.getElementById("title").textContent = $(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$title");
  const screenText = $(cat "$json");
  const term = new Terminal({
    cols: $cols,
    rows: $rows,
    fontSize: 15,
    fontFamily: "$family",
    theme: { background: "#0d1117", foreground: "#e6edf3" },
    scrollback: 0,
    convertEol: true,
    allowTransparency: false,
    cursorBlink: false,
  });
  term.open(document.getElementById("term"));
  term.write(screenText, () => {
    window.requestAnimationFrame(() => {
      document.title = "ready";
    });
  });
</script>
HTML
}

# termshot_png renders the page $2 with the Chrome $1 at $3 columns by $4 rows
# into the image $5. $6 is 1 when the page reserves the caption band, whose
# height the window must make room for.
termshot_png() {
  local chrome=$1 html=$2 cols=$3 rows=$4 out=$5 band=${6:-0}
  local width=$((cols * termshot_cell_width / 10 + termshot_pad_width))
  local height=$((rows * termshot_cell_height / 10 + termshot_pad_height))
  if ((band)); then height=$((height + termshot_band_height)); fi
  # The page loads its font from the file beside it, which a file:// document
  # may only do with this flag.
  "$chrome" --headless --disable-gpu --no-sandbox --hide-scrollbars \
    --allow-file-access-from-files \
    --virtual-time-budget=6000 --window-size="$width,$height" \
    --screenshot="$out" "file://$html" >/dev/null 2>&1
  [[ -s $out ]]
}
