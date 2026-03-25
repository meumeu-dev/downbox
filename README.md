# DownBox

Lightweight self-hosted download station with web UI. Single binary, runs on a Raspberry Pi.

![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8)
![Size](https://img.shields.io/badge/binary-~7MB-green)
![Arch](https://img.shields.io/badge/arch-amd64%20|%20arm64%20|%20armv7-blue)

## Install

```bash
curl -sL meumeu.dev/downbox/install | bash
```

Then open `http://localhost:8080` to configure.

## Features

- **aria2 engine** — HTTP, FTP, BitTorrent, magnet links. 16 connections per download.
- **File browser** — Browse, preview, download, delete. Share files with a direct link.
- **Remote access** — Cloudflare Tunnel or Bore. Access your downloads from anywhere.
- **Setup wizard** — First-run web wizard to configure everything.
- **Tiny footprint** — ~7MB binary, ~25MB RAM. No Docker, no database.

## Usage

```bash
downbox start              # Start as daemon
downbox stop               # Stop
downbox restart             # Restart
downbox status              # Show status
downbox init                # Generate config file
downbox help                # Show help
```

## Config

Config file is searched in order:
1. `./downbox.conf`
2. `~/.config/downbox/downbox.conf`
3. `/etc/downbox/downbox.conf`

```
port: 8080
download-dir: ~/Downloads
tunnel: cloudflared
cloudflared-token: eyJ...
cloudflared-hostname: dl.example.com
```

CLI flags override config file values.

## Build from source

```bash
git clone https://github.com/meumeu-dev/downbox.git
cd downbox
make build          # local binary
make build-all      # linux/amd64 + arm64 + armv7
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
- **Frontend** — Alpine.js + PicoCSS, embedded in binary
- **Tunnel** — Cloudflare Tunnel (token) or Bore (free)

## License

MIT
