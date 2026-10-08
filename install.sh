#!/bin/sh
# Install faehigkeiten (and the short alias fgk) from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/Joessst-Dev/faehigkeiten/main/install.sh | sh
#
# Environment:
#   FAEHIGKEITEN_VERSION      version to install, e.g. 0.1.0 (default: latest)
#   FAEHIGKEITEN_INSTALL_DIR  target directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="Joessst-Dev/faehigkeiten"
BIN="faehigkeiten"

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "curl or wget is required"
  fi
}

need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) die "unsupported OS: $os (on Windows use: scoop install faehigkeiten)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

# The latest release is resolved from the redirect of /releases/latest, which,
# unlike the GitHub API, is not rate limited for anonymous clients.
latest_version() {
  url="https://github.com/$REPO/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    final=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$url") || return 1
  else
    final=$(wget -S --spider "$url" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n1 | tr -d '\r')
  fi
  case "$final" in
    */tag/*) printf '%s\n' "${final##*/tag/}" ;;
    *) return 1 ;;
  esac
}

version="${FAEHIGKEITEN_VERSION:-}"
if [ -z "$version" ]; then
  version=$(latest_version) || die "could not determine latest release"
  [ -n "$version" ] || die "could not determine latest release"
fi
version=${version#v}

archive="${BIN}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $BIN $version for $os/$arch"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || die "no checksum for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" "$BIN"

dir="${FAEHIGKEITEN_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/$BIN" "$dir/$BIN"
ln -sf "$BIN" "$dir/fgk"

say "installed $dir/$BIN (alias: fgk)"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "note: $dir is not on your PATH" ;;
esac
