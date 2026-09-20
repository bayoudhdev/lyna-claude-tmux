#!/usr/bin/env bash
# Default-deny egress firewall for the lyna-tmux dev container.
#
# Runs as root at container start, either through the sudoers rule the image
# installs (postStartCommand) or through docker exec --user root. It resolves
# every domain of the root-owned allowlist to IPv4 addresses, allows DNS to
# the configured resolvers and connections to those addresses, rejects all
# other egress, then proves the result: a host outside the allowlist must be
# unreachable and the first allowed domain must be reachable.
#
# Any failure after the checks for root and arguments leaves egress blocked:
# a container whose firewall did not come up must not keep an open network.
#
# LYNA_TMUX_FIREWALL_ALLOWLIST and LYNA_TMUX_FIREWALL_RESOLV name the files
# the script reads. `lmux sandbox devcontainer up` points the first at the
# allowlist it rendered, so a stale image cannot widen it; sudo resets the
# environment, so the container user cannot set either.
set -Eeuo pipefail
set -f
IFS=$'\n\t'

allowlist=${LYNA_TMUX_FIREWALL_ALLOWLIST:-/usr/local/etc/lyna-tmux/allowed-domains}
resolv_conf=${LYNA_TMUX_FIREWALL_RESOLV:-/etc/resolv.conf}
ipset_name=lyna-tmux-allowed
ipset_next=lyna-tmux-allowed-next
started=0

log() { printf 'init-firewall: %s\n' "$*"; }

fail() {
  printf 'init-firewall: %s\n' "$*" >&2
  exit 1
}

ipv6_available() { ip6tables -S OUTPUT >/dev/null 2>&1; }

# Leaves loopback as the only egress. Every step runs even if one fails.
block_all() {
  iptables -F OUTPUT || true
  iptables -A OUTPUT -o lo -j ACCEPT || true
  iptables -P OUTPUT DROP || true
  iptables -P FORWARD DROP || true
  if ipv6_available; then
    ip6tables -F OUTPUT || true
    ip6tables -A OUTPUT -o lo -j ACCEPT || true
    ip6tables -P OUTPUT DROP || true
    ip6tables -P FORWARD DROP || true
  fi
}

on_exit() {
  local status=$?
  if [ "$status" -ne 0 ] && [ "$started" = 1 ]; then
    block_all
    printf 'init-firewall: failed; egress stays blocked until this script succeeds\n' >&2
  fi
}
trap on_exit EXIT

valid_domain() {
  local domain=$1
  local pattern='^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
  local letter='[a-z]'
  [ "${#domain}" -le 253 ] || return 1
  [[ $domain =~ $pattern ]] || return 1
  # A top-level label without a letter would make an IP address look like a name.
  [[ ${domain##*.} =~ $letter ]]
}

valid_ipv4() {
  local pattern='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
  [[ $1 =~ $pattern ]]
}

# Prints the validated domains, one per line. Comments and blank lines are skipped.
read_allowlist() {
  local line domain
  [ -f "$allowlist" ] || fail "missing allowlist $allowlist"
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%%#*}
    domain=${line//[[:space:]]/}
    [ -n "$domain" ] || continue
    valid_domain "$domain" || fail "invalid domain in $allowlist: $domain"
    printf '%s\n' "$domain"
  done <"$allowlist"
}

# Prints the IPv4 name servers of resolv.conf.
read_resolvers() {
  local keyword server
  [ -r "$resolv_conf" ] || return 0
  while IFS=$' \t' read -r keyword server _ || [ -n "$keyword" ]; do
    if [ "$keyword" = nameserver ] && valid_ipv4 "${server:-}"; then
      printf '%s\n' "$server"
    fi
  done <"$resolv_conf"
}

# Prints the IPv4 addresses of one domain.
resolve() {
  local out address
  out=$(getent ahostsv4 "$1" | awk '{ print $1 }' | sort -u) || true
  [ -n "$out" ] || fail "cannot resolve $1"
  for address in $out; do
    valid_ipv4 "$address" || fail "unexpected address $address for $1"
    printf '%s\n' "$address"
  done
}

apply_rules() {
  local resolvers=$1 server
  iptables -F OUTPUT
  iptables -A OUTPUT -o lo -j ACCEPT
  iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  for server in $resolvers; do
    iptables -A OUTPUT -d "$server" -p udp --dport 53 -j ACCEPT
    iptables -A OUTPUT -d "$server" -p tcp --dport 53 -j ACCEPT
  done
  iptables -A OUTPUT -m set --match-set "$ipset_name" dst -j ACCEPT
  iptables -A OUTPUT -j REJECT --reject-with icmp-admin-prohibited
  iptables -P OUTPUT DROP
  iptables -P FORWARD DROP
  if ipv6_available; then
    ip6tables -F OUTPUT
    ip6tables -A OUTPUT -o lo -j ACCEPT
    ip6tables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    ip6tables -A OUTPUT -j REJECT --reject-with icmp6-adm-prohibited
    ip6tables -P OUTPUT DROP
    ip6tables -P FORWARD DROP
  fi
}

self_test() {
  local domains=$1 probe="" host first
  # The allowlist is matched line by line in the shell rather than through a
  # pipeline: a reader that stops at the first line leaves the writer on a
  # closed pipe, and under pipefail that signal would be read as a firewall
  # that failed to come up.
  for host in example.com example.net example.org; do
    case $'\n'$domains$'\n' in
    *$'\n'"$host"$'\n'*) ;;
    *)
      probe=$host
      break
      ;;
    esac
  done
  [ -n "$probe" ] || fail "self-test needs a host outside the allowlist; remove example.com, example.net or example.org"
  if curl --silent --output /dev/null --connect-timeout 5 --max-time 10 "https://$probe"; then
    fail "self-test failed: $probe is outside the allowlist but reachable"
  fi
  log "self-test: $probe is blocked"
  first=${domains%%$'\n'*}
  if ! curl --silent --output /dev/null --connect-timeout 5 --max-time 15 "https://$first"; then
    fail "self-test failed: allowed domain $first is unreachable"
  fi
  log "self-test: $first is reachable"
}

main() {
  local domains resolvers domain addresses address
  [ "$(id -u)" = 0 ] || fail "must run as root: sudo /usr/local/bin/init-firewall.sh"
  [ "$#" -eq 0 ] || fail "takes no arguments; the allowlist is $allowlist"
  started=1

  domains=$(read_allowlist)
  [ -n "$domains" ] || fail "$allowlist names no domains"
  resolvers=$(read_resolvers)

  # Resolve everything before touching a rule, into a set swapped in at once.
  ipset create "$ipset_name" hash:ip family inet -exist
  ipset create "$ipset_next" hash:ip family inet -exist
  ipset flush "$ipset_next"
  for domain in $domains; do
    addresses=$(resolve "$domain")
    for address in $addresses; do
      ipset add "$ipset_next" "$address" -exist
    done
    log "allowed $domain"
  done
  ipset swap "$ipset_next" "$ipset_name"
  ipset destroy "$ipset_next"

  apply_rules "$resolvers"
  self_test "$domains"
  log "egress limited to $(printf '%s\n' "$domains" | awk 'END { print NR }') domains"
}

main "$@"
