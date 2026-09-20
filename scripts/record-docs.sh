#!/usr/bin/env bash
# Records every documentation scene into docs/assets.
#
# A scene with one frame becomes a PNG, a scene with several becomes a GIF and
# a PNG of its last frame, so a page can show either. The recordings run
# against a real project, with the real binary: build it first, or pass --bin.
#
# Nothing that names whoever records reaches the result: the home directory,
# the account name and the address git commits under are rewritten to neutral
# ones of the same length before a capture is drawn, and --redact takes
# anything else that must go. A scene whose first lines contain
# "# fixtures: off" runs without the fixture binaries on PATH.
#
# Usage: scripts/record-docs.sh --project DIR [--bin PATH] [--assets DIR]
#                               [--home NAME] [--redact FROM=TO]...
#                               [--only NAME]... [--dry-run]
set -euo pipefail

fail() {
  printf 'record-docs.sh: %s\n' "$*" >&2
  exit 1
}

usage_error() {
  printf 'record-docs.sh: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

usage() {
  sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)

project=""
bin=""
assets=$root/docs/assets
scenes=$root/docs/scenes
record=$here/record.sh
demo_home=""
only=()
extra_redactions=()
dry_run=0
while (($# > 0)); do
  case $1 in
  --project | --bin | --assets | --scenes | --record | --only | --home | --redact)
    (($# >= 2)) || usage_error "$1 needs a value"
    case $1 in
    --project) project=$2 ;;
    --bin) bin=$2 ;;
    --assets) assets=$2 ;;
    --scenes) scenes=$2 ;;
    --record) record=$2 ;;
    --only) only+=("$2") ;;
    --home) demo_home=$2 ;;
    --redact)
      [[ $2 == *=* ]] || usage_error "--redact takes FROM=TO"
      extra_redactions+=(--redact "$2")
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
[[ -d $project ]] || fail "$project is not a directory"
[[ -d $scenes ]] || fail "$scenes is not a directory"
[[ -x $record ]] || fail "$record is not executable"
# A scene that prints where the binary is, as uninstall does, must print a path
# a reader could have: the one given is copied under a directory named with as
# many characters as ".local/bin", which the redaction below puts back.
record_bin=$HOME/.recordbin
if [[ -n $bin ]]; then
  [[ -x $bin ]] || fail "$bin is not an executable file"
  rm -rf "$record_bin"
  mkdir -p "$record_bin"
  cp "$bin" "$record_bin/lmux"
  PATH=$record_bin:$PATH
  export PATH
fi
command -v lmux >/dev/null 2>&1 || fail "lmux is not on PATH; build it or pass --bin"

# The fixtures answer for a machine that is not the recorder's; each one is
# inert unless the scene that needs it asks, so this is safe for every scene.
# A scene that prints the path of the Claude binary would print the fixture
# instead of a real installation, so such a scene opts out with a
# "# fixtures: off" line and runs against the machine's own PATH.
# The recording shell comes before the fixtures, so a scene that opts out of
# them still opens its panes with it.
if [[ -d $scenes/bin ]]; then
  PATH=$scenes/bin:$PATH
  export PATH
fi
machine_path=$PATH
if [[ -d $scenes/fixtures/bin ]]; then
  PATH=$scenes/fixtures/bin:$PATH
  export PATH
fi
# Every pane the scenes open runs this shell rather than the account's own,
# which would draw its own prompt into the pictures. tmux takes the shell of a
# new pane from SHELL, and hands it down to the workspace tmux server a scene
# starts; a scene that opens a shell of its own runs it by name, as demo-shell.
if [[ -x $scenes/bin/demo-shell ]]; then
  SHELL=$scenes/bin/demo-shell
  export SHELL
fi

export LYNA_TMUX_DEMO_PROJECT=$project
# The fixture answers for every process of the recording, including the tmux
# server a pre step starts, which is what the agent popup is a child of.
export LYNA_TMUX_DEMO_AGENTS=1

# The pictures show what the tool does out of the box, not how whoever records
# has configured it, so the scenes run against a freshly written default
# configuration rather than the account's own. The directory is named with as
# many characters as ".config" so the redaction that puts it back reads the
# same width, and a screen laid out in columns stays in line.
record_config=$HOME/.reccfg
XDG_CONFIG_HOME=$record_config
export XDG_CONFIG_HOME

# The scenes open, kill and list workspaces, so they run on a tmux server of
# their own: whatever the account has open of its own is never a target.
LYNA_TMUX_SOCKET_NAME=lt-record
export LYNA_TMUX_SOCKET_NAME

# The recordings are made in a real account, so the home directory and the
# account name are rewritten to neutral ones. Both replacements are the same
# length as what they replace, because a screen is laid out in columns: a
# shorter name would pull a status bar and every box border out of line. The
# home directory goes first, so the account name inside it is already gone.
account=$(id -un)
if [[ -n $demo_home ]]; then
  ((${#demo_home} == ${#account})) ||
    fail "$account is ${#account} characters and $demo_home is ${#demo_home}: columns would shift; --home takes a name of ${#account} characters"
else
  # Nobody has to count the letters of their own account name, so the default
  # is the neutral one padded to it.
  demo_home=developer
  while ((${#demo_home} < ${#account})); do demo_home+=x; done
  demo_home=${demo_home:0:${#account}}
fi
demo_dir=$(dirname "$HOME")/$demo_home
# The project itself is shown where the documentation puts it, under src. Its
# parent is rewritten rather than the project, so a scene that lists a sibling
# project reads the same way. Rules are applied longest first, so this one wins
# over the home directory it starts with.
# neutral_like is a replacement for text that must not be published, as long
# as the text itself: a screen is laid out in columns, and a shorter or longer
# replacement would pull every status bar and box border out of line.
neutral_like() {
  local want=${#1} out=$2
  while ((${#out} < want)); do out+=x; done
  printf '%s' "${out:0:want}"
}

redact=(
  --redact "$record_bin=$demo_dir/.local/bin"
  --redact "$record_config=$demo_dir/.config"
  # The scenes run on a server of their own, which a report that names the
  # socket would print; the name it has for a reader is the one it has for
  # everybody.
  --redact "$LYNA_TMUX_SOCKET_NAME=lyna-tmux"
  --redact "$(dirname "$project")=$demo_dir/src"
  --redact "$HOME=$demo_dir"
  --redact "$account=$demo_home"
)

# The address git commits under is on screen wherever a recording shows a
# commit or a status line that names one, and it is the recorder's own. Every
# address git would use is rewritten, the repository's over the global one.
for address in \
  "$(git -C "$project" config --get user.email 2>/dev/null || true)" \
  "${GIT_AUTHOR_EMAIL-}" "${GIT_COMMITTER_EMAIL-}" "${EMAIL-}"; do
  [[ -n $address && $address != *=* ]] || continue
  case " ${redact[*]} " in
  *" --redact $address="*) continue ;;
  esac
  redact+=(--redact "$address=$(neutral_like "$address" dev@example.com)")
done

redact+=(${extra_redactions[@]+"${extra_redactions[@]}"})

# wanted is true when the scene was asked for, or when none was.
wanted() {
  ((${#only[@]} == 0)) && return 0
  local name
  for name in "${only[@]}"; do
    [[ $name == "$1" || $name == "$1.scene" ]] && return 0
  done
  return 1
}

# The configuration is written once every argument has been read, so a dry run
# and a command line that is refused leave the account's directories alone.
if ((dry_run == 0)); then
  rm -rf "$record_config"
  mkdir -p "$record_config"
  trap 'rm -rf "$record_config" "$record_bin"' EXIT
  lmux config init >/dev/null || fail "could not write the recording configuration"
  # The frames are drawn with a patched font, so the scenes run with the icon
  # set that font is for. The status bar then takes its pointed separators
  # from the same rule a terminal with such a font follows, which is what a
  # reader of the pictures gets by installing the font and nothing else.
  config_file=$(lmux config path) || fail "could not find the recording configuration"
  python3 - "$config_file" <<'EDIT' || fail "could not set the icon set of the recording configuration"
import io
import sys

path = sys.argv[1]
text = io.open(path, encoding="utf-8").read()
if 'icons = "auto"' not in text:
    sys.exit("the configuration template no longer sets icons")
io.open(path, "w", encoding="utf-8").write(text.replace('icons = "auto"', 'icons = "nerd"', 1))
EDIT
  # A scene that opens a review needs the plugin a review runs, and a machine
  # without it would be recorded showing the warning instead of the editor.
  lmux review status >/dev/null 2>&1 ||
    fail "the review plugin is not ready; run: lmux review install"
fi

recorded=0
for scene in "$scenes"/*.scene; do
  name=$(basename "$scene" .scene)
  wanted "$name" || continue
  frames=$("$record" --scene "$scene" --still /dev/null --dry-run | sed -n 's/^frames: //p')
  [[ -n $frames ]] || fail "$scene: the recorder reported no frame count"
  args=(--scene "$scene" --still "$assets/$name.png" "${redact[@]}")
  if ((frames > 1)); then
    args+=(--out "$assets/$name.gif")
  fi
  scene_path=$PATH
  if grep -q '^# fixtures: off' "$scene"; then
    scene_path=$machine_path
  fi
  printf '%s: %s frames\n' "$name" "$frames"
  if ((dry_run)); then
    printf '  %s %s\n' "$record" "${args[*]}"
  else
    (cd "$project" && PATH=$scene_path "$record" "${args[@]}")
  fi
  recorded=$((recorded + 1))
done

((recorded > 0)) || fail "no scene matched"
printf 'recorded %s scenes into %s\n' "$recorded" "$assets"
