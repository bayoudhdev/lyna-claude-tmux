#!/usr/bin/env bash
# Records every documentation scene into docs/assets.
#
# A scene with one frame becomes a PNG, a scene with several becomes a GIF and
# a PNG of its last frame, so a page can show either. The recordings run
# against a real project, with the real binary: build it first, or pass --bin.
#
# The home directory of whoever records is never in the result: every capture
# has it rewritten to a neutral one before it is drawn. A scene whose first
# lines contain "# fixtures: off" runs without the fixture binaries on PATH.
#
# Usage: scripts/record-docs.sh --project DIR [--bin PATH] [--assets DIR]
#                               [--home NAME] [--only NAME]... [--dry-run]
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
  sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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
dry_run=0
while (($# > 0)); do
  case $1 in
  --project | --bin | --assets | --scenes | --record | --only | --home)
    (($# >= 2)) || usage_error "$1 needs a value"
    case $1 in
    --project) project=$2 ;;
    --bin) bin=$2 ;;
    --assets) assets=$2 ;;
    --scenes) scenes=$2 ;;
    --record) record=$2 ;;
    --only) only+=("$2") ;;
    --home) demo_home=$2 ;;
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
if [[ -n $bin ]]; then
  [[ -x $bin ]] || fail "$bin is not an executable file"
  PATH=$(cd "$(dirname "$bin")" && pwd):$PATH
  export PATH
fi
command -v lyna-tmux >/dev/null 2>&1 || fail "lyna-tmux is not on PATH; build it or pass --bin"

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
redact=(
  --redact "$(dirname "$project")=$demo_dir/src"
  --redact "$HOME=$demo_dir"
  --redact "$account=$demo_home"
)

# wanted is true when the scene was asked for, or when none was.
wanted() {
  ((${#only[@]} == 0)) && return 0
  local name
  for name in "${only[@]}"; do
    [[ $name == "$1" || $name == "$1.scene" ]] && return 0
  done
  return 1
}

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
