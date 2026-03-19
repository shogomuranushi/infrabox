#!/bin/sh
set -e

REPO="shogomuranushi/infrabox"
BINARY="ib"
INSTALL_DIR="/usr/local/bin"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)          ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Get latest version
VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": *"\(.*\)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest version"; exit 1
fi

echo "Installing ${BINARY} ${VERSION} (${OS}/${ARCH})..."

URL="https://github.com/${REPO}/releases/download/${VERSION}/ib_${OS}_${ARCH}.tar.gz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" | tar -xz -C "$TMP" ib

install -m 755 "$TMP/ib" "${INSTALL_DIR}/${BINARY}"

echo "Installed: $(${INSTALL_DIR}/${BINARY} --version 2>/dev/null || echo ${VERSION})"
echo "Run 'ib init' to configure your endpoint."
