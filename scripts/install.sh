#!/usr/bin/env sh
set -eu

REPO="akhanatoon/quench"
INSTALL_DIR="${QUENCH_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

TAG="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$TAG" ]; then
  echo "Failed to resolve latest release tag for ${REPO}" >&2
  exit 1
fi
VER="${TAG#v}"
URL="https://github.com/${REPO}/releases/download/${TAG}/quench_${VER}_${OS}_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading quench ${TAG} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o "$TMP/quench.tar.gz"
tar -xzf "$TMP/quench.tar.gz" -C "$TMP"

mkdir -p "$INSTALL_DIR"
install -m 755 "$TMP/quench" "$INSTALL_DIR/quench"

echo "Installed quench to ${INSTALL_DIR}/quench"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add ${INSTALL_DIR} to your PATH: export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
