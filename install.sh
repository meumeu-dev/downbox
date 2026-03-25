#!/bin/sh
set -e

# DownBox installer
# Usage: curl -sL https://dl.meumeu.dev/install.sh | bash

REPO="freelux/downbox"
INSTALL_DIR="/usr/local/bin"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info() { printf "${CYAN}[*]${NC} %s\n" "$1"; }
ok() { printf "${GREEN}[+]${NC} %s\n" "$1"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$1"; }
err() { printf "${RED}[-]${NC} %s\n" "$1"; exit 1; }

# Check OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
[ "$OS" = "linux" ] || err "DownBox only supports Linux (got: $OS)"

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    armv7*)  ARCH="armv7" ;;
    *)       err "Unsupported architecture: $ARCH" ;;
esac

info "Detected: linux/$ARCH"

# sudo helper
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        err "Not running as root and sudo not available"
    fi
fi

# Install aria2
if ! command -v aria2c >/dev/null 2>&1; then
    info "Installing aria2..."
    if command -v apt-get >/dev/null 2>&1; then
        $SUDO apt-get update -qq && $SUDO apt-get install -y -qq aria2
    elif command -v apk >/dev/null 2>&1; then
        $SUDO apk add --quiet aria2
    elif command -v dnf >/dev/null 2>&1; then
        $SUDO dnf install -y -q aria2
    elif command -v pacman >/dev/null 2>&1; then
        $SUDO pacman -S --noconfirm aria2
    else
        warn "Could not detect package manager. Install aria2 manually."
    fi
    ok "aria2 installed"
else
    ok "aria2 already installed"
fi

# Download DownBox
info "Downloading DownBox (linux/$ARCH)..."
LATEST=$(curl -sI "https://github.com/$REPO/releases/latest" | grep -i "location:" | grep -oP 'tag/\K[^\s\r]+')
if [ -z "$LATEST" ]; then
    # Fallback: download from main branch build
    DOWNLOAD_URL="https://github.com/$REPO/releases/latest/download/downbox-$ARCH"
else
    DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST/downbox-$ARCH"
fi

curl -fsSL "$DOWNLOAD_URL" -o /tmp/downbox || err "Download failed. Check https://github.com/$REPO/releases"
chmod +x /tmp/downbox
$SUDO mv /tmp/downbox "$INSTALL_DIR/downbox"
ok "DownBox installed to $INSTALL_DIR/downbox"

# Optional: install bore
if ! command -v bore >/dev/null 2>&1; then
    info "Installing bore (tunnel tool)..."
    BORE_VER=$(curl -sL "https://api.github.com/repos/ekzhang/bore/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
    if [ -n "$BORE_VER" ]; then
        case "$ARCH" in
            amd64) BORE_ARCH="x86_64-unknown-linux-musl" ;;
            arm64) BORE_ARCH="aarch64-unknown-linux-musl" ;;
            *)     BORE_ARCH="" ;;
        esac
        if [ -n "$BORE_ARCH" ]; then
            curl -fsSL "https://github.com/ekzhang/bore/releases/download/$BORE_VER/bore-$BORE_VER-$BORE_ARCH.tar.gz" -o /tmp/bore.tar.gz
            tar xzf /tmp/bore.tar.gz -C /tmp
            $SUDO mv /tmp/bore "$INSTALL_DIR/bore"
            rm -f /tmp/bore.tar.gz
            ok "bore installed"
        fi
    fi
fi

# Init config
downbox init 2>/dev/null || true

# Start
info "Starting DownBox..."
downbox start

echo ""
printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
printf "${GREEN}  DownBox installed successfully!${NC}\n"
printf "${GREEN}  Open http://localhost:8080 to configure${NC}\n"
printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
echo ""
echo "Commands:"
echo "  downbox start    Start DownBox"
echo "  downbox stop     Stop DownBox"
echo "  downbox status   Check status"
echo ""
