#!/usr/bin/env bash
# tpm entry point for lyna-tmux plugin mode.
#
# tpm runs every executable *.tmux file of a plugin inside the tmux server
# (TMUX names that server) and discards its output. This file only hands over
# to the installed lyna-tmux binary, which renders the plugin configuration and
# sources it on this server. It never downloads or installs anything: when the
# binary is missing or fails, it tells the user what to do in a tmux message.
set -uo pipefail

# Hook array index for the one-shot notice; high enough to leave the indexes
# users pick by hand alone.
readonly hook_index=4217
readonly repo_url=https://github.com/bayoudhdev/lyna-claude-tmux
# Messages are expanded as tmux formats: keep them free of # and % characters
# and of single quotes (they are embedded in a quoted hook command).
readonly missing_message="lyna-tmux is not installed: see $repo_url for install steps, then reload tmux"
readonly failed_message='lyna-tmux plugin tmux --apply failed: run it in a shell inside tmux to see the error'

find_lyna_tmux() {
  local candidate
  if candidate=$(command -v lyna-tmux); then
    printf '%s\n' "$candidate"
    return 0
  fi
  # The installer's default location, often missing from the PATH the tmux
  # server started with.
  if [ -n "${HOME:-}" ] && [ -x "$HOME/.local/bin/lyna-tmux" ]; then
    printf '%s\n' "$HOME/.local/bin/lyna-tmux"
    return 0
  fi
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
  if ! bin=$(find_lyna_tmux); then
    notify "$missing_message"
    return 0
  fi
  if ! "$bin" plugin tmux --apply; then
    notify "$failed_message"
  fi
  return 0
}

main
