# DownBox

Lightweight self-hosted download station with web UI. Upload, download and share files via tunnel. Single binary, runs on a Raspberry Pi.

![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8)
![Size](https://img.shields.io/badge/binary-~7MB-green)
![Arch](https://img.shields.io/badge/arch-amd64%20|%20i386%20|%20arm64%20|%20armv7-blue)
![Docker](https://img.shields.io/badge/docker-~44MB-blue)

## Install

```bash
curl -sL meumeu.dev/downbox/install | bash
```

Custom port:

```bash
curl -sL meumeu.dev/downbox/install | PORT=9090 ARIA2_PORT=6801 bash
```

### Docker

```bash
docker run -d -p 8080:8080 -v ~/Downloads:/downloads meumeudev/downbox
```

Or with docker-compose:

```bash
docker compose up -d
```

## Features

- **aria2 engine** — HTTP, FTP, BitTorrent, magnet links. 16 connections per download
- **File upload** — Upload files directly from the web UI with progress tracking
- **File browser** — Browse, preview, download, delete, rename
- **Share links** — Share any file with a direct link (local or public via tunnel)
- **Remote access** — Cloudflare Tunnel or Bore. Access from anywhere
- **DNS-over-HTTPS** — Encrypted DNS via Cloudflare, Google, Quad9, Mullvad, NextDNS or custom
- **IP blocklist** — Built-in SOCKS5 filtering proxy with pre-configured blocklists
- **VPN interface binding** — Route downloads through a specific network interface (tun0, wg0)
- **Docker support** — Multi-stage Alpine image, ~44MB, non-root
- **Password auth** — Mandatory authentication, salted password hash, session tokens
- **Tiny footprint** — ~7MB binary, ~25MB RAM. No Docker, no database, 0 Go dependencies

## Security

- Mandatory password authentication (auto-generated on first run)
- Salted SHA256 password hash (plaintext never stored)
- Session tokens (256-bit, HTTPOnly cookies)
- SSRF protection (DNS pinning, redirect validation, private IP blocking)
- Path traversal protection (EvalSymlinks, O_NOFOLLOW, prefix check)
- XSS protection (CSP, Content-Type enforcement, Alpine.js x-text)
- Rate limiting on login
- Config file permissions 0600
- Security headers (X-Frame-Options, X-Content-Type-Options, CSP, Referrer-Policy)

## Usage

```bash
downbox start              # Start as daemon
downbox stop               # Stop
downbox restart             # Restart
downbox status              # Show status
downbox update              # Update to latest version
downbox init                # Generate config file
downbox help                # Show help
```

## Config

> **Never commit `downbox.conf` to git** — it contains password hash and tunnel tokens. Gitignored by default.

Config file is searched in order:
1. `./downbox.conf`
2. `~/.config/downbox/downbox.conf`
3. `/etc/downbox/downbox.conf`

```ini
# Server
port: 8080
download-dir: ~/Downloads

# Tunnel (choose one)
tunnel: bore
bore-server: bore.pub
bore-secret: your-secret

# Or Cloudflare Tunnel
# tunnel: cloudflared
# cloudflared-token: eyJ...
# cloudflared-hostname: dl.example.com

# Privacy & security
doh-url: https://cloudflare-dns.com/dns-query
blocklist-url: https://raw.githubusercontent.com/sahsu/ipfilter/master/ipfilter.dat
interface: tun0
exclude-trackers: *
proxy: socks5://127.0.0.1:9050
```

All settings are also configurable via the web UI.

## Architecture

```
┌─────────────────────────────────────┐
│          Go binary (DownBox)         │
│  ┌────────┐ ┌──────────┐ ┌───────┐ │
│  │embed FS│ │ HTTP Srv  │ │ SOCKS5│ │
│  │(WebUI) │ │ net/http  │ │ Proxy │ │
│  └────────┘ └────┬─────┘ └───┬───┘ │
│    ┌─────────────┼────────┐   │     │
│  aria2 RPC    File API  Tunnel│ DoH │
│  client       (os ops)  Mgr  │     │
└────┼─────────────┼────────┼───┼─────┘
  aria2c       filesystem  CF/Bore
```

- **0 external Go dependencies** — stdlib only
- **Frontend** — Alpine.js, embedded in binary
- **Tunnel** — Cloudflare Tunnel (token) or Bore (free/self-hosted)
- **Proxy** — Built-in SOCKS5 with DoH + IP blocklist filtering

## Build from source

```bash
git clone https://github.com/meumeu-dev/downbox.git
cd downbox
go build -ldflags="-s -w" .
```

## License

MIT
