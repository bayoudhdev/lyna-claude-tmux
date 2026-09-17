#!/usr/bin/env bash
# tpm entry point for lmux plugin mode.
#
# tpm runs every executable *.tmux file of a plugin inside the tmux server
# (TMUX names that server) and discards its output. This file only hands over
# to the installed lmux binary, which renders the plugin configuration and
# sources it on this server. It never downloads or installs anything: when the
# binary is missing or fails, it tells the user what to do in a tmux message.
set -uo pipefail

# Hook array index for the one-shot notice; high enough to leave the indexes
# users pick by hand alone.
readonly hook_index=4217
readonly repo_url=https://github.com/bayoudhdev/lyna-claude-tmux
# Messages are expanded as tmux formats: keep them free of # and % characters
# and of single quotes (they are embedded in a quoted hook command).
readonly missing_message="lmux is not installed: see $repo_url for install steps, then reload tmux"
readonly failed_message='lmux plugin tmux --apply failed: run it in a shell inside tmux to see the error'

# find_lmux resolves the binary. lyna-tmux is the name the command had in
# 1.0.0 and is still installed as a link to it, so an installation from then
# that was never updated is found too.
find_lmux() {
  local name candidate
  for name in lmux lyna-tmux; do
    if candidate=$(command -v "$name"); then
      printf '%s\n' "$candidate"
      return 0
    fi
    # The installer's default location, often missing from the PATH the tmux
    # server started with.
    if [ -n "${HOME:-}" ] && [ -x "$HOME/.local/bin/$name" ]; then
      printf '%s\n' "$HOME/.local/bin/$name"
      return 0
    fi
  done
  return 1
}

# notify shows a message on every attached client. tpm usually runs while tmux
# loads its configuration at server start, before any client is attached; the
# message is then shown once to the first client, through hooks that remove
# themselves. A new client attaching fires client-attached, and one creating
# its session with new-session fires client-session-changed.
notify() {
  local message=$1 clients client once
  clients=$(tmux list-clients -F '#{client_name}' 2>/dev/null) || clients=""
  if [ -n "$clients" ]; then
    while IFS= read -r client; do
      tmux display-message -d 0 -c "$client" "$message"
    done <<<"$clients"
    return 0
  fi
  once="display-message -d 0 '$message' ; set-hook -gu 'client-attached[$hook_index]' ; set-hook -gu 'client-session-changed[$hook_index]'"
  tmux set-hook -g "client-attached[$hook_index]" "$once"
  tmux set-hook -g "client-session-changed[$hook_index]" "$once"
}

main() {
  local bin
  if ! bin=$(find_lmux); then
    notify "$missing_message"
    return 0
  fi
  if ! "$bin" plugin tmux --apply; then
    notify "$failed_message"
  fi
  return 0
}

main
