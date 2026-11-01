#!/usr/bin/env bash
set -e

# Linux & macOS Quick Installer for ForwardTo Agent
# Run in terminal:
#   curl -sSL https://forwardto.cloud/install.sh | bash

REPO="syedn-tech/forwardTo-Websocket-Agent"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="forwardto-agent"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  armv7l|armhf)
    ARCH="armv7"
    ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

echo ">>> Detected OS: $OS, Architecture: $ARCH"

DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_NAME}-${OS}-${ARCH}"

TMP_FILE="/tmp/${BINARY_NAME}"
echo ">>> Downloading ${BINARY_NAME} from ${DOWNLOAD_URL}..."

if command -v curl >/dev/null 2>&1; then
  curl -sSL -f -o "$TMP_FILE" "$DOWNLOAD_URL"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$TMP_FILE" "$DOWNLOAD_URL"
else
  echo "Error: curl or wget is required."
  exit 1
fi

chmod +x "$TMP_FILE"

if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP_FILE" "$INSTALL_DIR/$BINARY_NAME"
else
  echo ">>> Installing to $INSTALL_DIR requires sudo privileges:"
  sudo mv "$TMP_FILE" "$INSTALL_DIR/$BINARY_NAME"
fi

echo "✓ Successfully installed forwardto-agent to $INSTALL_DIR/$BINARY_NAME"
echo ""
echo "Run the agent with:"
echo "  forwardto-agent --team-key=<TEAM_KEY> --client-id=<CLIENT_ID> --token=<TOKEN>"
