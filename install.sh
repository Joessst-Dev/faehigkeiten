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

version="${FAEHIGKEITEN_VERSION:-}"
if [ -z "$version" ]; then
  tmpv=$(mktemp)
  fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmpv" || die "could not determine latest release"
  version=$(sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$tmpv" | head -n1)
  rm -f "$tmpv"
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
