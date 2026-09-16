# Komari (MCP fork)

[English](./README.md) | [简体中文](./README_zh-cn.md)

A self-hosted server monitoring solution, with a built-in **MCP (Model Context Protocol)
endpoint** so AI agents can query metrics and manage probes directly.

This is a fork of [komari-monitor/komari](https://github.com/komari-monitor/komari),
which was archived on 2026-09-16. This fork continues from tag `1.5.0-fix1`
(commit `0ca87aa`, the final upstream state) and adds MCP support.

> [!WARNING]
> Komari is a self-hosted monitoring and control application. Deploy it only on systems
> you own or are authorized to manage. You are solely responsible for how you deploy and
> use Komari. The developers accept no liability for unauthorized access, persistence,
> command execution, other misuse, or any resulting consequences.

## What's different from upstream

Everything upstream did, plus an MCP endpoint at `/api/mcp`. Upstream code is otherwise
untouched — the only modification to an existing file is a single line in
`web/router/router.go` that registers the route.

Existing agents (`komari-agent`) and themes work unchanged: the reporting protocol
and the frontend contract are identical to `1.5.0-fix1`.

## Features

- **Real-time monitoring**: second-level live data.
- **Lightweight**: low resource usage, suitable for servers of any size.
- **Self-hosted**: full control over your data, simple to deploy.
- **Web interface**: an intuitive monitoring dashboard.
- **Extensible**: supports custom themes and plugins.
- **MCP endpoint**: let an AI agent read your monitoring data and manage probes.

## Quick start

### Install script (Linux, recommended)

```bash
curl -fsSL -o install-komari.sh https://raw.githubusercontent.com/CHENJINWEN33/komari/main/install-komari.sh
sudo bash install-komari.sh
```

An interactive installer that detects your architecture, sets up a systemd service, and
also handles upgrade / uninstall / status / logs from the same menu. Run it again to upgrade.

> **Why download first instead of a one-liner?**
>
> The installer needs root (it writes to `/opt` and `/etc/systemd/system`, and manages a
> system systemd unit). `sudo bash <(curl ...)` fails: the `/dev/fd/63` produced by process
> substitution is closed by sudo's `closefrom` behaviour, which closes every descriptor
> numbered 3 or above — you get `No such file or directory`.
>
> `curl ... | sudo bash` does install, but the script reads input with a bare `read` from
> stdin, and stdin is already the script itself, so the interactive menus silently fall
> through to defaults.
>
> Downloading first keeps the menus working and lets you read the script before running it
> as root.

### Binaries

Or grab the build for your platform from
[Releases](https://github.com/CHENJINWEN33/komari/releases) and run it:

```bash
./komari server            # listens on 0.0.0.0:25774
```

Open `http://<host>:25774` and follow the first-run install guide.

Then install [komari-agent](https://github.com/komari-monitor/komari-agent) on each machine
you want to monitor. The upstream agent works as-is with this fork.

### Build from source

The frontend lives in a **separate repository** and is embedded into the binary at build
time, so `go build` on a fresh clone fails with
`pattern defaultTheme/dist.tar.zst: no matching files found` until you produce it:

```bash
# 1. Build the frontend
git clone https://github.com/komari-monitor/komari-web
cd komari-web && npm install && npm run build && cd ..

# 2. Pack it into the archive the backend embeds
cd komari
mkdir -p web/public/defaultTheme
tar -cf /tmp/dist.tar -C ../komari-web/dist .
zstd -19 -T0 -q -f /tmp/dist.tar -o web/public/defaultTheme/dist.tar.zst
cp ../komari-web/komari-theme.json web/public/defaultTheme/

# 3. Build
go build -o komari .
```

`zstd` is not strictly required — Node 18+ ships a zstd implementation
(`zlib.zstdCompressSync`) that produces a compatible single-frame archive.

Requires **Go 1.25** and a working C toolchain (cgo is mandatory: `internal/sqlitetune`
imports `mattn/go-sqlite3` directly). Upstream CI cross-compiles with `zig cc`; on Windows,
an old MinGW-w64 toolchain can emit a PE whose debug sections violate `FileAlignment`,
producing a binary Windows refuses to load — add `-ldflags="-s -w"` if you hit that.

## One-command HTTPS setup for MCP

If you just want an AI client to reach the MCP endpoint and would rather not
hand-write proxy config:

```bash
curl -fsSL -o setup-mcp-proxy.sh https://raw.githubusercontent.com/CHENJINWEN33/komari/main/setup-mcp-proxy.sh
sudo bash setup-mcp-proxy.sh
```

It will:

1. Detect whether you run nginx or Caddy and **add a site file without
   overwriting anything that already exists**
2. Handle certificates (certbot for nginx, automatic for Caddy)
3. Expose a secret-bearing MCP path where the proxy **injects the
   `Authorization` header for you**
4. Bind Komari to `127.0.0.1` and set `KOMARI_MCP_TRUST_PROXY_HOST=true`
5. Verify the handshake and print a URL you can paste straight into a client

> **Why inject the header?**
>
> The claude.ai connector dialog has no field for request headers, so the client
> cannot send `Authorization` itself. The script puts the secret in the URL path
> instead; the proxy validates it, strips it, and adds the `Bearer` header before
> forwarding. The connector then only needs a URL.
>
> The trade-off: that URL *is* the credential. Don't share it.

The script validates config before reloading, so a mistake cannot take down your
existing sites. For the manual walkthrough, see the next section.

## Production deployment (HTTPS)

The installer sets up Komari itself but not a reverse proxy. Out of the box it listens on
`0.0.0.0:25774` — plain HTTP, directly exposed.

This matters: Komari ships web SSH and remote command execution, so an admin password or
API key sniffed in transit hands an attacker shells on **every monitored host**. Put HTTPS
in front of it before exposing it to the internet.

### No reverse proxy yet

Caddy is the shortest path — certificates are requested and renewed automatically:

```bash
sudo apt-get update
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key'   | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt'   | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo apt-get update && sudo apt-get install -y caddy

sudo tee /etc/caddy/Caddyfile >/dev/null <<'EOF'
your.domain.com {
    reverse_proxy 127.0.0.1:25774
}
EOF
sudo systemctl restart caddy
```

### Already running Caddy

**Do not overwrite the existing Caddyfile** — append a site block:

```bash
sudo cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.bak.$(date +%s)
printf '
%s {
    reverse_proxy 127.0.0.1:25774
}
' "your.domain.com"   | sudo tee -a /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile && sudo systemctl reload caddy
```

### Already running Nginx

**You must forward WebSocket upgrade headers.** Agents report over
`/api/clients/v2/rpc` (WebSocket), as does the web terminal. Without this the dashboard
loads fine but agents never connect, and the failure is hard to diagnose.

In your `http {}` block:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

Then the site:

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name your.domain.com;

    ssl_certificate     /etc/letsencrypt/live/your.domain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/your.domain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:25774;
        proxy_http_version 1.1;

        # Required for WebSocket
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Agent connections are long-lived
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

### Then bind Komari back to loopback

With a proxy in front, port `25774` is still listening publicly — a way around your HTTPS.
Override it with a systemd drop-in:

```bash
sudo mkdir -p /etc/systemd/system/komari.service.d
sudo tee /etc/systemd/system/komari.service.d/override.conf >/dev/null <<'EOF'
[Service]
ExecStart=
ExecStart=/opt/komari/komari server -l 127.0.0.1:25774

# Required for MCP behind a proxy, see below
Environment=KOMARI_MCP_TRUST_PROXY_HOST=true
Environment=GIN_MODE=release
EOF

sudo systemctl daemon-reload
sudo systemctl restart komari
ss -tlnp | grep 25774      # should show 127.0.0.1 only
```

A drop-in rather than editing `/etc/systemd/system/komari.service` directly: the installer
regenerates that file on upgrade, which would discard your edits. Drop-ins live in a
separate directory and survive.

> **Behind a proxy, MCP needs `KOMARI_MCP_TRUST_PROXY_HOST=true`**
>
> The MCP SDK enables DNS rebinding protection by default: it returns 403 when the server
> listens on loopback but the request's `Host` header is not a loopback address. A reverse
> proxy hits exactly that — Nginx/Caddy connects to `127.0.0.1:25774` while forwarding your
> public hostname. Without this variable the MCP endpoint returns 403 every time.

### Run the first-run install guide over HTTPS

The first-run guide is where you set the administrator password. Open
`https://your.domain.com` for it — not `http://server-ip:25774`, where that password
travels in clear text.

## MCP endpoint

Exposes Komari as MCP tools over the official Streamable HTTP transport, so an AI agent
can answer questions like *"which host is running out of memory?"* by querying your
monitoring data itself.

### Setup

1. Generate an API Key in the admin settings (`api_key`, at least 12 characters).
2. Point your agent at the endpoint:

```json
{
  "mcpServers": {
    "komari": {
      "type": "http",
      "url": "http://127.0.0.1:25774/api/mcp",
      "headers": { "Authorization": "Bearer YOUR_API_KEY" }
    }
  }
}
```

### Tools

| Group | Tools |
|---|---|
| Monitoring | `list_servers`, `get_servers_status`, `get_server_metrics_history`, `get_server_recent_records`, `get_ping_records`, `list_alert_rules` |
| Probe management | `list_servers_admin`, `get_server_detail`, `add_server`, `edit_server` |
| Introspection | `komari_list_methods`, `komari_method_help`, `komari_call` |

The introspection tools are built on Komari's own runtime RPC metadata (`rpc.methods`,
`rpc.help`), so every RPC method the server exposes is reachable — including methods added
later, with no change to the MCP layer.

### Risk tiers

Every RPC method is classified into one of three tiers:

| Tier | Meaning | Default |
|---|---|---|
| `read` | Queries that change nothing | exposed |
| `manage` | Reversible business configuration changes | exposed |
| `dangerous` | Remote command execution, raw SQL, file writes, credential reads, security-setting changes, irreversible deletes | **blocked** |

Of the 90 registered methods, 68 are exposed and 22 are withheld by default.

### Environment variables

| Variable | Default | Effect |
|---|---|---|
| `KOMARI_MCP_ENABLED` | `true` | Set `false` to disable the endpoint entirely |
| `KOMARI_MCP_ALLOW_DANGEROUS` | `false` | Set `true` to expose the `dangerous` tier |
| `KOMARI_MCP_TRUST_PROXY_HOST` | `false` | Set `true` when running behind a reverse proxy and MCP returns 403 |

> [!CAUTION]
> The MCP endpoint authenticates with Komari's existing admin check, and Komari's API Key
> carries the admin role while being **exempt from 2FA**
> (`VerifySensitive2FACore` returns early for API-key principals).
> An agent holding that key therefore has full administrator capability. With
> `KOMARI_MCP_ALLOW_DANGEROUS=true` it can execute arbitrary commands on every monitored
> host. Treat the key accordingly.

See [`internal/mcp/README.md`](./internal/mcp/README.md) for implementation details.

## Screenshots

| Page | Screenshot |
| ------------ | ------------------------------------------------------------ |
| Dashboard | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E4%B8%BB%E9%A1%B5%E4%BB%AA%E8%A1%A8%E7%9B%98.webp" width="800" alt="Dashboard"> |
| Admin dashboard | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E5%90%8E%E5%8F%B0%E4%BB%AA%E8%A1%A8%E7%9B%98.webp" width="800" alt="Admin dashboard"> |
| History charts | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E5%8E%86%E5%8F%B2%E5%9B%BE%E8%A1%A8.webp" width="800" alt="History charts"> |
| Web terminal | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E7%BD%91%E9%A1%B5%E7%BB%88%E7%AB%AF.webp" width="800" alt="Web terminal"> |

## Credits

Komari was created by [Akizon77](https://github.com/Akizon77) and the
[Komari contributors](https://github.com/komari-monitor/komari/graphs/contributors).
This fork exists only because of their work. Upstream documentation remains at
[komari.wiki](https://www.komari.wiki/).

Licensed under the MIT License — see [LICENSE](./LICENSE). The original copyright notice
is retained unchanged.
