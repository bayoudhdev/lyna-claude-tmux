#!/bin/sh
# Installs the lyna-tmux binary from a GitHub release.
#
#   curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/bayoudhdev/lyna-claude-tmux/main/scripts/install.sh | sh
#   sh install.sh --version v1.0.0 --prefix "$HOME/bin"
#
# Every request uses HTTPS with TLS 1.2 or newer, redirects included. The
# archive is checked against the release's checksums.txt before anything is
# installed, and a mismatch aborts. The script never uses sudo: choose a
# --prefix you can write to.
set -eu

default_base_url=https://github.com/bayoudhdev/lyna-claude-tmux/releases
max_redirects=10

tmp=""
staged=""
cleanup() {
  if [ -n "$staged" ]; then rm -f "$staged"; fi
  if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

usage() {
  cat <<EOF
Usage: install.sh [--version vX.Y.Z] [--prefix DIR] [--base-url URL]

Installs lyna-tmux from a GitHub release after verifying its SHA-256 checksum.

Options:
  --version vX.Y.Z  release to install (default: the latest release)
  --prefix DIR      directory for the lmux binary (default: \$HOME/.local/bin)
  --base-url URL    https URL of the releases (default: $default_base_url)
  -h, --help        show this help
EOF
}

say() { printf '%s\n' "$*"; }

fail() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

usage_error() {
  printf 'install.sh: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

require_https() {
  case $1 in
  *[[:space:]]* | *[[:cntrl:]]*) fail "refusing URL with spaces or control characters: $1" ;;
  https://?*) ;;
  *) fail "refusing non-https URL: $1" ;;
  esac
}

valid_version() {
  case $1 in
  "" | *[!0-9A-Za-z.-]*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
}

parse_args() {
  version=""
  prefix=""
  base_url=$default_base_url
  while [ "$#" -gt 0 ]; do
    case $1 in
    --version | --prefix | --base-url)
      [ "$#" -ge 2 ] || usage_error "$1 needs a value"
      case $1 in
      --version) version=$2 ;;
      --prefix) prefix=$2 ;;
      --base-url) base_url=$2 ;;
      esac
      shift 2
      ;;
    --version=*) version=${1#*=} && shift ;;
    --prefix=*) prefix=${1#*=} && shift ;;
    --base-url=*) base_url=${1#*=} && shift ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) usage_error "unknown argument: $1" ;;
    esac
  done

  require_https "$base_url"
  base_url=${base_url%/}

  if [ -n "$version" ]; then
    case $version in
    v*) ;;
    *) version=v$version ;;
    esac
    valid_version "$version" || fail "invalid version $version (expected vX.Y.Z)"
  fi

  if [ -z "$prefix" ]; then
    [ -n "${HOME:-}" ] || fail "HOME is not set; pass --prefix DIR"
    prefix=$HOME/.local/bin
  fi
  case $prefix in
  /*) ;;
  *) prefix=$(pwd)/$prefix ;;
  esac
  prefix=${prefix%/}
}

detect_platform() {
  os=$(uname -s)
  case $os in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported operating system: $os (release binaries exist for Linux and macOS)" ;;
  esac
  arch=$(uname -m)
  case $arch in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch (release binaries exist for amd64 and arm64)" ;;
  esac
  # A shell translated by Rosetta 2 reports x86_64 on Apple silicon, where the
  # native binary is the right one.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -in sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
}

detect_tools() {
  if command -v curl >/dev/null 2>&1; then
    downloader=curl
  elif command -v wget >/dev/null 2>&1; then
    downloader=wget
  else
    fail "curl or wget is required"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256_tool=sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    sha256_tool=shasum
  else
    fail "sha256sum or shasum is required to verify the download"
  fi
}

# wget_request fetches one URL without following redirects. It prints the
# response headers and fails when the response is not a success.
wget_request() {
  wget --secure-protocol=TLSv1_2 --max-redirect=0 --server-response --quiet \
    --output-document="$2" "$1" 2>&1
}

# location prints the last Location header of wget response headers.
location() {
  printf '%s\n' "$1" | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'
}

# wget has no option that keeps redirects on https, so they are followed here
# one hop at a time, each one checked by require_https.
wget_fetch() {
  wget_url=$1
  wget_hops=0
  while :; do
    require_https "$wget_url"
    if wget_response=$(wget_request "$wget_url" "$2"); then
      return 0
    fi
    wget_next=$(location "$wget_response")
    [ -n "$wget_next" ] || fail "download failed: $wget_url"
    wget_hops=$((wget_hops + 1))
    [ "$wget_hops" -le "$max_redirects" ] || fail "too many redirects: $1"
    case $wget_next in
    *://*) wget_url=$wget_next ;;
    /*) wget_url=$(printf '%s\n' "$wget_url" | sed 's|^\(https://[^/]*\).*|\1|')$wget_next ;;
    *) fail "unsupported redirect from $wget_url to $wget_next" ;;
    esac
  done
}

fetch() {
  require_https "$1"
  case $downloader in
  curl)
    # --proto also applies to every redirect, so a hop to http fails.
    curl --proto '=https' --tlsv1.2 -fsSL --max-redirs "$max_redirects" \
      -o "$2" "$1" || fail "download failed: $1"
    ;;
  wget) wget_fetch "$1" "$2" ;;
  esac
}

# The latest release page redirects to .../tag/<version>, and a repository that
# has been renamed answers with a hop of its own first, so the chain is
# followed until it names a tag. Only the redirect targets are read, never a
# page, and every hop is https.
resolve_latest() {
  latest_url=$base_url/latest
  latest_hops=0
  latest_next=$latest_url
  while :; do
    require_https "$latest_next"
    case $downloader in
    curl)
      target=$(curl --proto '=https' --tlsv1.2 -sS -o /dev/null -w '%{redirect_url}' "$latest_next") ||
        fail "cannot reach $latest_next"
      ;;
    wget)
      target=$(location "$(wget_request "$latest_next" /dev/null || true)")
      ;;
    esac
    version=${target##*/}
    case $target in
    */tag/"$version") break ;;
    "") fail "cannot determine the latest release from $latest_url; pass --version vX.Y.Z" ;;
    esac
    latest_hops=$((latest_hops + 1))
    [ "$latest_hops" -le "$max_redirects" ] || fail "too many redirects: $latest_url"
    latest_next=$target
  done
  valid_version "$version" || fail "latest release has an unexpected tag: $version"
}

sha256_of() {
  case $sha256_tool in
  sha256sum) sha256sum "$1" ;;
  shasum) shasum -a 256 "$1" ;;
  esac | awk '{ print tolower($1) }'
}

verify() {
  expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print tolower($1) }' "$tmp/checksums.txt")
  case $expected in
  "") fail "checksums.txt of $version has no entry for $asset" ;;
  *[!0-9a-f]*) fail "checksums.txt of $version has a malformed entry for $asset" ;;
  esac
  [ "${#expected}" -eq 64 ] || fail "checksums.txt of $version has a malformed entry for $asset"
  actual=$(sha256_of "$tmp/$asset")
  if [ "$actual" != "$expected" ]; then
    fail "checksum mismatch for $asset: expected $expected, got $actual; nothing was installed"
  fi
}

install_binary() {
  mkdir -p "$tmp/extract"
  tar -xzf "$tmp/$asset" -C "$tmp/extract" lmux 2>/dev/null ||
    fail "$asset does not contain lmux"
  if [ -L "$tmp/extract/lmux" ] || [ ! -f "$tmp/extract/lmux" ]; then
    fail "lmux in $asset is not a regular file"
  fi

  mkdir -p "$prefix" 2>/dev/null || fail "cannot create $prefix; choose a directory you own with --prefix"
  if [ ! -d "$prefix" ] || [ ! -w "$prefix" ]; then
    fail "cannot write to $prefix; choose a directory you own with --prefix"
  fi
  # Stage next to the target and rename, so a running lmux is replaced
  # atomically and an interrupted install leaves the old binary in place.
  # mktemp picks an unpredictable name and creates the file exclusively, so a
  # prefix shared with another local account leaves nothing to plant a symlink
  # at; the file type is checked before cp writes through it anyway.
  staged=$(mktemp "$prefix/.lmux.install.XXXXXXXXXX") ||
    fail "cannot create a staging file in $prefix; choose a directory you own with --prefix"
  if [ -L "$staged" ] || [ ! -f "$staged" ]; then
    fail "refusing to stage on $staged: not a regular file"
  fi
  if ! { cp "$tmp/extract/lmux" "$staged" && chmod 0755 "$staged" && mv -f "$staged" "$prefix/lmux"; }; then
    fail "cannot install into $prefix"
  fi
  staged=""
  # lyna-tmux is the name the command had in 1.0.0. It stays as a link to the
  # new one, so a script or a shell alias written then still runs. A link is
  # replaced; anything else in its place is left alone and reported.
  if [ -e "$prefix/lyna-tmux" ] && [ ! -L "$prefix/lyna-tmux" ]; then
    say "kept $prefix/lyna-tmux as it is: it is not a link to lmux"
  elif ! ln -sf lmux "$prefix/lyna-tmux"; then
    say "could not link $prefix/lyna-tmux to lmux; the command is lmux"
  fi
}

path_guidance() {
  case ":${PATH:-}:" in
  *":$prefix:"*) return 0 ;;
  esac
  say ""
  say "$prefix is not on your PATH. Add it in your shell profile:"
  say "  sh, bash, zsh:  export PATH=\"$prefix:\$PATH\""
  say "  fish:           fish_add_path \"$prefix\""
}

main() {
  parse_args "$@"
  detect_platform
  detect_tools

  [ -n "$version" ] || resolve_latest
  asset=lyna-tmux_${version#v}_${os}_${arch}.tar.gz
  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t lyna-tmux)

  say "downloading lyna-tmux $version for $os/$arch"
  fetch "$base_url/download/$version/checksums.txt" "$tmp/checksums.txt"
  fetch "$base_url/download/$version/$asset" "$tmp/$asset"
  verify
  install_binary

  say "installed lyna-tmux $version to $prefix/lmux"
  path_guidance
}

main "$@"
