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
# background reaches the edge of the image the way it does on screen.
termshot_capture() {
  tmux -L "$1" capture-pane -e -p -N -t "$2" >"$3"
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

# termshot_html writes a page that draws the capture $1 at $2 columns by $3
# rows under the window title $4, to the file $5. $6 is a scratch directory.
termshot_html() {
  local ansi=$1 cols=$2 rows=$3 title=$4 out=$5 work=$6
  local json
  json=$work/$(basename "$out").json
  termshot_json "$ansi" "$json"
  cat >"$out" <<HTML
<!doctype html>
<meta charset="utf-8">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.css">
<style>
  body { margin: 0; background: #0d1117; font-family: ui-monospace, Menlo, monospace; }
  #frame { padding: 22px; background: #0d1117; display: inline-block; border-radius: 10px; }
  #bar { height: 26px; display: flex; align-items: center; gap: 8px; padding: 0 6px 12px; }
  .dot { width: 12px; height: 12px; border-radius: 50%; }
  #title { color: #7d8590; font-size: 13px; margin-left: 8px; }
  .xterm-viewport { overflow: hidden !important; }
</style>
<div id="frame">
  <div id="bar">
    <span class="dot" style="background:#ff5f56"></span>
    <span class="dot" style="background:#ffbd2e"></span>
    <span class="dot" style="background:#27c93f"></span>
    <span id="title"></span>
  </div>
  <div id="term"></div>
</div>
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.js"></script>
<script>
  document.getElementById("title").textContent = $(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$title");
  const screenText = $(cat "$json");
  const term = new Terminal({
    cols: $cols,
    rows: $rows,
    fontSize: 15,
    fontFamily: "Menlo, ui-monospace, monospace",
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
# into the image $5.
termshot_png() {
  local chrome=$1 html=$2 cols=$3 rows=$4 out=$5
  local width=$((cols * termshot_cell_width / 10 + termshot_pad_width))
  local height=$((rows * termshot_cell_height / 10 + termshot_pad_height))
  "$chrome" --headless --disable-gpu --no-sandbox --hide-scrollbars \
    --virtual-time-budget=6000 --window-size="$width,$height" \
    --screenshot="$out" "file://$html" >/dev/null 2>&1
  [[ -s $out ]]
}
