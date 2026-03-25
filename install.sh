#!/bin/sh
set -e

# DownBox installer
# Usage: curl -sL meumeu.dev/downbox/install | bash
#        curl -sL meumeu.dev/downbox/install | PORT=9090 bash

REPO="meumeu-dev/downbox"
INSTALL_DIR="/usr/local/bin"
PORT="${PORT:-8080}"
ARIA2_PORT="${ARIA2_PORT:-6800}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info() { printf "${CYAN}[*]${NC} %s\n" "$1"; }
ok()   { printf "${GREEN}[+]${NC} %s\n" "$1"; }
warn() { printf "${YELLOW}[!]${NC} %s\n" "$1"; }
err()  { printf "${RED}[-]${NC} %s\n" "$1"; exit 1; }

# Check OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
[ "$OS" = "linux" ] || err "DownBox only supports Linux (got: $OS)"

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)       ARCH="amd64" ;;
    i386|i686)    ARCH="i386" ;;
    aarch64)      ARCH="arm64" ;;
    armv7*)       ARCH="armv7" ;;
    *)            err "Unsupported architecture: $ARCH" ;;
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
DOWNLOAD_URL="https://github.com/$REPO/releases/latest/download/downbox-$ARCH"

TMPFILE=$(mktemp /tmp/downbox.XXXXXXXXXX)
trap 'rm -f "$TMPFILE"' EXIT

if ! curl -fSL --progress-bar "$DOWNLOAD_URL" -o "$TMPFILE" < /dev/null; then
    err "Download failed. Check https://github.com/$REPO/releases"
fi

chmod +x "$TMPFILE"
$SUDO mv "$TMPFILE" "$INSTALL_DIR/downbox"
ok "DownBox installed to $INSTALL_DIR/downbox"

# Optional: install bore
if ! command -v bore >/dev/null 2>&1; then
    info "Installing bore (optional tunnel tool)..."
    case "$ARCH" in
        amd64) BORE_ARCH="x86_64-unknown-linux-musl" ;;
        arm64) BORE_ARCH="aarch64-unknown-linux-musl" ;;
        *)     BORE_ARCH="" ;;
    esac
    if [ -n "$BORE_ARCH" ]; then
        BORE_VER=$(curl -sL "https://api.github.com/repos/ekzhang/bore/releases/latest" < /dev/null | grep '"tag_name"' | cut -d'"' -f4)
        if [ -n "$BORE_VER" ] && curl -fSL "https://github.com/ekzhang/bore/releases/download/$BORE_VER/bore-$BORE_VER-$BORE_ARCH.tar.gz" -o /tmp/bore.tar.gz < /dev/null 2>/dev/null; then
            tar xzf /tmp/bore.tar.gz -C /tmp
            $SUDO mv /tmp/bore "$INSTALL_DIR/bore"
            rm -f /tmp/bore.tar.gz
            ok "bore installed"
        else
            warn "Could not install bore (optional, skip)"
        fi
    fi
fi

# Generate config with chosen port
if [ ! -f "$HOME/.config/downbox/downbox.conf" ]; then
    mkdir -p "$HOME/.config/downbox"
    cat > "$HOME/.config/downbox/downbox.conf" <<CONF
# DownBox configuration
setup: false
port: $PORT
download-dir: ~/Downloads
aria2-port: $ARIA2_PORT
tunnel: none
CONF
    ok "Config created (~/.config/downbox/downbox.conf) port=$PORT aria2=$ARIA2_PORT"
fi

# Check if ports are available
check_port() {
    if command -v ss >/dev/null 2>&1; then
        ss -tlnp 2>/dev/null | grep -q ":$1 " && return 1
    elif command -v netstat >/dev/null 2>&1; then
        netstat -tlnp 2>/dev/null | grep -q ":$1 " && return 1
    fi
    return 0
}

if ! check_port "$PORT"; then
    warn "Port $PORT is already in use. Set PORT=XXXX to use a different port"
fi
if ! check_port "$ARIA2_PORT"; then
    warn "aria2 port $ARIA2_PORT is already in use. Set ARIA2_PORT=XXXX to change it"
fi

# Start
info "Starting DownBox..."
downbox start -port "$PORT" 2>/dev/null || downbox -port "$PORT" 2>/dev/null &

echo ""
printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
printf "${GREEN}  DownBox installed!${NC}\n"
printf "${GREEN}  Open ${CYAN}http://localhost:${PORT}${GREEN} to configure${NC}\n"
printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
echo ""
echo "  downbox start    Start"
echo "  downbox stop     Stop"
echo "  downbox status   Status"
echo ""
