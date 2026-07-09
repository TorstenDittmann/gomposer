#!/bin/sh
# gomposer installer. POSIX sh, no bashisms.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/gomposer/main/install.sh | sh
#
# Environment overrides:
#   GOMPOSER_INSTALL_DIR   Target directory. Default: /usr/local/bin, falling
#                          back to $HOME/.local/bin if the default is not
#                          writable without sudo.
#   GOMPOSER_VERSION       Release tag to install (e.g. v0.1.0). Default:
#                          latest published release.

set -eu

REPO="TorstenDittmann/gomposer"
BIN_NAME="gomposer"

log()  { printf '%s\n' "$*"; }
err()  { printf 'install.sh: error: %s\n' "$*" >&2; exit 1; }
info() { printf 'install.sh: %s\n' "$*"; }

need() {
  command -v "$1" >/dev/null 2>&1 || err "required command '$1' not found in PATH"
}

need uname
need tar
need mktemp

DOWNLOADER=""
if command -v curl >/dev/null 2>&1; then
  DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
  DOWNLOADER="wget"
else
  err "need curl or wget to download the release"
fi

# --- detect OS + arch -------------------------------------------------------

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) err "unsupported OS: $OS (supported: darwin, linux)" ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) err "unsupported architecture: $ARCH (supported: amd64, arm64)" ;;
esac

info "detected platform: ${OS}/${ARCH}"

# --- resolve version --------------------------------------------------------

download_to_stdout() {
  # $1 = url
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL "$1"
  else
    wget -qO- "$1"
  fi
}

download_to_file() {
  # $1 = url, $2 = destination path
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL -o "$2" "$1"
  else
    wget -qO "$2" "$1"
  fi
}

VERSION="${GOMPOSER_VERSION:-}"
if [ -z "$VERSION" ]; then
  info "resolving latest release..."
  API_URL="https://api.github.com/repos/${REPO}/releases/latest"
  # Extract "tag_name":"vX.Y.Z" from the JSON without depending on jq.
  VERSION=$(download_to_stdout "$API_URL" | \
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  if [ -z "$VERSION" ]; then
    err "could not determine latest release from $API_URL"
  fi
fi
info "installing gomposer $VERSION"

# --- download + verify ------------------------------------------------------

VER_NO_V=${VERSION#v}
ARCHIVE="${BIN_NAME}_${VER_NO_V}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="${BIN_NAME}_${VER_NO_V}_checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

info "downloading $ARCHIVE"
download_to_file "${BASE_URL}/${ARCHIVE}"   "${TMPDIR}/${ARCHIVE}"
download_to_file "${BASE_URL}/${CHECKSUMS}" "${TMPDIR}/${CHECKSUMS}"

info "verifying checksum"
EXPECTED=$(grep " ${ARCHIVE}$" "${TMPDIR}/${CHECKSUMS}" | awk '{print $1}')
if [ -z "$EXPECTED" ]; then
  err "no checksum entry for $ARCHIVE in $CHECKSUMS"
fi
if command -v sha256sum >/dev/null 2>&1; then
  GOT=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  GOT=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')
else
  err "no sha256sum or shasum in PATH"
fi
if [ "$EXPECTED" != "$GOT" ]; then
  err "checksum mismatch: expected $EXPECTED, got $GOT"
fi

info "extracting"
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"
if [ ! -f "${TMPDIR}/${BIN_NAME}" ]; then
  err "archive did not contain expected binary '${BIN_NAME}'"
fi
chmod +x "${TMPDIR}/${BIN_NAME}"

# --- pick install dir -------------------------------------------------------

# Priority:
#   1. explicit GOMPOSER_INSTALL_DIR
#   2. /usr/local/bin if writable (no sudo)
#   3. $HOME/.local/bin (create if needed, warn if not on PATH)
INSTALL_DIR="${GOMPOSER_INSTALL_DIR:-}"
if [ -z "$INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
fi

mkdir -p "$INSTALL_DIR"
DEST="${INSTALL_DIR}/${BIN_NAME}"
info "installing to $DEST"

if ! mv "${TMPDIR}/${BIN_NAME}" "$DEST" 2>/dev/null; then
  err "could not move binary into $INSTALL_DIR — set GOMPOSER_INSTALL_DIR to a writable path or re-run with sudo"
fi

# --- PATH check + success ---------------------------------------------------

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ON_PATH=1 ;;
  *) ON_PATH=0 ;;
esac

log ""
log "gomposer installed to $DEST"
"$DEST" --version || true

if [ "$ON_PATH" -eq 0 ]; then
  log ""
  log "NOTE: $INSTALL_DIR is not in your PATH."
  log "Add this line to your shell profile (~/.zshrc, ~/.bashrc, etc.):"
  log "  export PATH=\"$INSTALL_DIR:\$PATH\""
fi
