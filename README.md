# DownBox

Lightweight self-hosted download station with web UI. Upload, download and share files via tunnel. Single binary, runs on a Raspberry Pi.

![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8)
![Size](https://img.shields.io/badge/binary-~7MB-green)
![Arch](https://img.shields.io/badge/arch-amd64%20|%20i386%20|%20arm64%20|%20armv7-blue)

## Install

```bash
curl -sL meumeu.dev/downbox/install | bash
```

Custom port:

```bash
curl -sL meumeu.dev/downbox/install | PORT=9090 bash
```

Then open `http://localhost:8080` (or your custom port) to configure.

## Features

- **aria2 engine** — HTTP, FTP, BitTorrent, magnet links. 16 connections per download.
- **File upload** — Upload files directly from the web UI with progress tracking.
- **File browser** — Browse, preview, download, delete. Share files with a direct link.
- **Remote access** — Cloudflare Tunnel or Bore (custom server + secret supported). Access from anywhere.
- **Setup wizard** — First-run web wizard to configure everything.
- **Tiny footprint** — ~7MB binary, ~25MB RAM. No Docker, no database.

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

> **Never commit `downbox.conf` to git** — it may contain tunnel tokens and secrets. The file is gitignored by default.

Config file is searched in order:
1. `./downbox.conf`
2. `~/.config/downbox/downbox.conf`
3. `/etc/downbox/downbox.conf`

```
port: 8080
download-dir: ~/Downloads
tunnel: bore
bore-server: bore.pub
bore-secret: your-secret
```

Or with Cloudflare Tunnel:

```
tunnel: cloudflared
cloudflared-token: eyJ...
cloudflared-hostname: dl.example.com
```

Download options:

```
dns-servers: 1.1.1.1,8.8.8.8
interface: tun0
exclude-trackers: *
```

- **dns-servers** — Custom DNS for downloads (comma-separated)
- **interface** — Bind downloads to a network interface (VPN: `tun0`, `wg0`)
- **exclude-trackers** — Block BitTorrent trackers (`*` = all, or specific URIs)

CLI flags override config file values.

## Build from source

```bash
git clone https://github.com/meumeu-dev/downbox.git
cd downbox
make build          # local binary
make build-all      # linux/amd64 + i386 + arm64 + armv7
```

Requires Go 1.22+.

## Architecture

```
┌──────────────────────────────┐
│       Go binary (DownBox)    │
│  ┌────────┐ ┌──────────┐    │
│  │embed FS│ │ HTTP Srv  │    │
│  │(WebUI) │ │ net/http  │    │
│  └────────┘ └────┬─────┘    │
│    ┌─────────────┼────────┐  │
│  aria2 RPC    File API  Tunnel│
│  client       (os ops)  Mgr  │
└────┼─────────────┼────────┼──┘
  aria2c       filesystem  cloudflared/bore
```

- **0 external Go dependencies** — stdlib only
- **Frontend** — Alpine.js, embedded in binary
- **Tunnel** — Cloudflare Tunnel (token) or Bore (free/self-hosted)

## License

MIT
