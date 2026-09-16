# Komari (MCP fork)

[English](./README.md) | [简体中文](./README_zh-cn.md)

自托管的服务器监控工具，内置 **MCP（Model Context Protocol）端点**，
让 AI agent 可以直接查询监控数据、管理探针。

本仓库 fork 自 [komari-monitor/komari](https://github.com/komari-monitor/komari)，
上游已于 2026-09-16 归档。本 fork 从 `1.5.0-fix1`（commit `0ca87aa`，上游最终状态）
继续，并新增 MCP 支持。

> [!WARNING]
> Komari 是一款自托管的监控/控制程序，仅应部署在你拥有或已获得授权管理的系统上。
> 在未获授权的情况下部署、访问、持久化、执行命令及从事其他滥用行为，
> 用户需要自行承担部署和使用 Komari 的责任。开发者不对未经授权或滥用行为及其后果承担责任。

## 与上游的区别

上游的功能全部保留，额外在 `/api/mcp` 提供 MCP 端点。
上游代码基本未动 —— 对既有文件的修改只有 `web/router/router.go` 里注册路由的一行。

现有的探针（`komari-agent`）和主题**无需任何改动**即可继续使用：
上报协议与前端契约和 `1.5.0-fix1` 完全一致。

## 特性

- **实时监控**：秒级实时数据展示
- **轻量高效**：低资源占用，适合各种规模的服务器
- **自托管**：完全掌控数据隐私，部署简单
- **Web 界面**：直观的监控仪表盘
- **可扩展**：支持自定义主题和插件
- **MCP 端点**：让 AI agent 读取监控数据、管理探针

## 快速开始

### 使用安装脚本（Linux，推荐）

```bash
curl -fsSL -o install-komari.sh https://raw.githubusercontent.com/CHENJINWEN33/komari/main/install-komari.sh
sudo bash install-komari.sh
```

交互式安装器，会自动识别架构、配置 systemd 服务，并且**升级、卸载、查看状态、看日志、
重启**都在同一个菜单里。以后想升级，再跑一次即可。

> **为什么要分两步下载再执行，而不是一行搞定？**
>
> 安装器需要 root（要写 `/opt`、`/etc/systemd/system`，并操作系统级 systemd 单元）。
> 而 `sudo bash <(curl ...)` 会失败：`<(...)` 进程替换产生的 `/dev/fd/63`，
> 会被 sudo 的 `closefrom` 机制关掉（它默认关闭编号 ≥ 3 的所有文件描述符），
> 于是报 `No such file or directory`。
>
> 改用 `curl ... | sudo bash` 能装，但脚本用裸 `read` 从 stdin 读取输入，
> 而 stdin 已被脚本自身占用，交互菜单会失效并全部落到默认值。
>
> 先下载再执行既保留了交互，也让你有机会在以 root 运行前先审阅脚本内容。

### 使用二进制文件

或者从 [Releases](https://github.com/CHENJINWEN33/komari/releases) 下载对应平台的文件并运行：

```bash
./komari server            # 监听 0.0.0.0:25774
```

浏览器打开 `http://<你的地址>:25774`，按首次安装引导完成初始化。

然后在每台需要监控的机器上安装
[komari-agent](https://github.com/komari-monitor/komari-agent)。
上游的探针可直接用于本 fork。

### 从源码构建

前端在**独立仓库**里，构建时才嵌入二进制。所以在全新 clone 的仓库里直接 `go build`
会失败，报 `pattern defaultTheme/dist.tar.zst: no matching files found`，
必须先生成该文件：

```bash
# 1. 构建前端
git clone https://github.com/komari-monitor/komari-web
cd komari-web && npm install && npm run build && cd ..

# 2. 打包成后端要嵌入的归档
cd komari
mkdir -p web/public/defaultTheme
tar -cf /tmp/dist.tar -C ../komari-web/dist .
zstd -19 -T0 -q -f /tmp/dist.tar -o web/public/defaultTheme/dist.tar.zst
cp ../komari-web/komari-theme.json web/public/defaultTheme/

# 3. 编译
go build -o komari .
```

`zstd` 命令行工具不是必须的 —— Node 18+ 自带 zstd 实现
（`zlib.zstdCompressSync`），产出的单帧归档格式完全兼容。

环境要求：**Go 1.25** 和可用的 C 工具链（cgo 是强制的，
`internal/sqlitetune` 直接 import 了 `mattn/go-sqlite3`）。
上游 CI 用 `zig cc` 交叉编译；在 Windows 上，较旧的 MinGW-w64 工具链
可能产出调试节偏移不满足 `FileAlignment` 的 PE 文件，导致 Windows 拒绝加载 ——
遇到这种情况加上 `-ldflags="-s -w"` 即可。

## 一键配置 MCP 的 HTTPS 访问

如果你只是想让 AI 客户端能连上 MCP，不想手动折腾反代配置，用这个脚本：

```bash
curl -fsSL -o setup-mcp-proxy.sh https://raw.githubusercontent.com/CHENJINWEN33/komari/main/setup-mcp-proxy.sh
sudo bash setup-mcp-proxy.sh
```

它会自动完成：

1. 检测你用的是 nginx 还是 Caddy，**新增站点配置，不覆盖任何现有文件**
2. 处理证书（nginx 走 certbot，Caddy 自动申请）
3. 开一个带随机密钥的 MCP 入口，由反代**代为注入 `Authorization` 头**
4. 把 Komari 改为只监听 `127.0.0.1`，并设好 `KOMARI_MCP_TRUST_PROXY_HOST=true`
5. 验证握手，打印可直接粘贴到 AI 客户端的 URL

> **为什么要「代为注入请求头」？**
>
> claude.ai 的连接器对话框没有填写请求头的地方，客户端无法自带
> `Authorization`。所以脚本把密钥放进 URL 路径，反代校验后剥掉密钥段、
> 补上 `Bearer` 头再转发。这样连接器只需填一个 URL。
>
> 代价是这个 URL 本身等同凭据，不要公开分享。

脚本会先做语法校验再重载，配置写错不会影响你现有的站点。
想了解每一步细节，或需要手动配置，见下一节。

## 生产部署（HTTPS 反向代理）

安装器只负责装 Komari 本体，不配置反向代理。装完后默认监听 `0.0.0.0:25774`，
也就是**以 HTTP 明文直接暴露在公网**。

这不是可选项：Komari 自带 Web SSH 与远程命令执行功能，一旦管理员密码或 API Key
被中间链路嗅探，攻击者获得的是你**所有被监控主机**的 shell。上公网前请务必配好 HTTPS。

以下三种情况任选其一。

### 情况一：服务器上还没有反向代理

推荐 Caddy，证书自动申请、自动续期，配置只有两行：

```bash
sudo apt-get update
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key'   | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt'   | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo apt-get update && sudo apt-get install -y caddy

sudo tee /etc/caddy/Caddyfile >/dev/null <<'EOF'
你的域名.com {
    reverse_proxy 127.0.0.1:25774
}
EOF
sudo systemctl restart caddy
```

### 情况二：已经在用 Caddy

**不要覆盖现有的 Caddyfile**，追加一个站点块即可：

```bash
sudo cp /etc/caddy/Caddyfile /etc/caddy/Caddyfile.bak.$(date +%s)
printf '
%s {
    reverse_proxy 127.0.0.1:25774
}
' "你的域名.com"   | sudo tee -a /etc/caddy/Caddyfile
sudo caddy validate --config /etc/caddy/Caddyfile && sudo systemctl reload caddy
```

`tee -a` 的 `-a` 是追加；先备份、再 `validate` 验证语法，避免写错把现有站点搞挂。

### 情况三：已经在用 Nginx

**必须转发 WebSocket 升级头**。探针上报走 `/api/clients/v2/rpc`（WebSocket），
网页终端同样如此。漏配的话面板能正常打开、探针却永远连不上，而且错误信息很难定位。

先在 `http {}` 块里加（通常放在 `nginx.conf`）：

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

再配置站点：

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name 你的域名.com;

    ssl_certificate     /etc/letsencrypt/live/你的域名.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/你的域名.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:25774;
        proxy_http_version 1.1;

        # WebSocket 必需
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 探针连接是长连接，超时设短了会被反复断开
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

### 配好反代之后：把 Komari 锁回本机

反代配好后，`25774` 仍然直接监听在公网上，等于给 HTTPS 留了个绕过通道。
用 systemd drop-in 覆盖，让它只监听回环地址：

```bash
sudo mkdir -p /etc/systemd/system/komari.service.d
sudo tee /etc/systemd/system/komari.service.d/override.conf >/dev/null <<'EOF'
[Service]
ExecStart=
ExecStart=/opt/komari/komari server -l 127.0.0.1:25774

# 反代场景下 MCP 必需，原因见下
Environment=KOMARI_MCP_TRUST_PROXY_HOST=true
Environment=GIN_MODE=release
EOF

sudo systemctl daemon-reload
sudo systemctl restart komari
ss -tlnp | grep 25774      # 应只剩 127.0.0.1
```

用 drop-in 而不是直接改 `/etc/systemd/system/komari.service`，是因为安装器在升级时
会重新生成那个文件，直接改会被覆盖；drop-in 在独立目录里，升级后依然生效。

> **反代后 MCP 必须设 `KOMARI_MCP_TRUST_PROXY_HOST=true`**
>
> MCP SDK 默认启用 DNS rebinding 防护：当服务监听在回环地址、而请求的 `Host` 头
> 不是回环地址时返回 403。反向代理场景正好命中——Nginx/Caddy 连的是
> `127.0.0.1:25774`，转发过来的 `Host` 却是对外域名。不设这个变量，
> MCP 端点会一直返回 403。

### 最后：先配好 HTTPS，再做首次安装引导

首次安装引导要设置管理员密码。请用 `https://你的域名.com` 打开面板完成引导，
不要用 `http://服务器IP:25774` —— 后者的密码是明文传输的。

## MCP 端点

通过官方的 Streamable HTTP 传输把 Komari 暴露为 MCP 工具，
这样你可以直接问 AI「**哪台机器内存快满了**」，它会自己去查监控数据。

### 配置

1. 在后台设置里生成 API Key（配置键 `api_key`，长度至少 12 位）
2. 在 agent 的 MCP 配置中添加：

```json
{
  "mcpServers": {
    "komari": {
      "type": "http",
      "url": "http://127.0.0.1:25774/api/mcp",
      "headers": { "Authorization": "Bearer 你的APIKey" }
    }
  }
}
```

### 工具清单

| 分组 | 工具 |
|---|---|
| 监控查询 | `list_servers`、`get_servers_status`、`get_server_metrics_history`、`get_server_recent_records`、`get_ping_records`、`list_alert_rules` |
| 探针管理 | `list_servers_admin`、`get_server_detail`、`add_server`、`edit_server` |
| 内省 | `komari_list_methods`、`komari_method_help`、`komari_call` |

内省这组基于 Komari 自带的运行时 RPC 元数据（`rpc.methods`、`rpc.help`），
因此服务端暴露的每个 RPC 方法都能触达 —— 包括**日后新增的方法**，
MCP 层无需改动即可跟上。

### 风险分档

每个 RPC 方法都会被归入三档之一：

| 档位 | 含义 | 默认 |
|---|---|---|
| `read` | 只读查询，不改变任何状态 | 开放 |
| `manage` | 可逆的业务配置变更 | 开放 |
| `dangerous` | 远程命令执行、裸 SQL、文件写、凭据读取、安全设置变更、不可逆删除 | **关闭** |

已注册的 90 个方法中，68 个放行，22 个默认不暴露。

### 环境变量

| 变量 | 默认 | 作用 |
|---|---|---|
| `KOMARI_MCP_ENABLED` | `true` | 设为 `false` 可整体关闭 MCP 端点 |
| `KOMARI_MCP_ALLOW_DANGEROUS` | `false` | 设为 `true` 放开高危档工具 |
| `KOMARI_MCP_TRUST_PROXY_HOST` | `false` | 部署在反向代理后且 MCP 返回 403 时设为 `true` |

> [!CAUTION]
> MCP 端点复用 Komari 既有的管理员鉴权，而 Komari 的 API Key 拥有管理员角色
> 且**豁免 2FA**（`VerifySensitive2FACore` 对 API Key 主体直接放行）。
> 因此持有该 Key 的 agent 具备完整管理员能力。若开启
> `KOMARI_MCP_ALLOW_DANGEROUS=true`，它将能在**所有被监控主机**上执行任意命令。
> 请按此前提管理这个 Key。

实现细节见 [`internal/mcp/README.md`](./internal/mcp/README.md)。

## 截图

| 页面 | 截图 |
| ------------ | ------------------------------------------------------------ |
| 主页仪表盘 | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E4%B8%BB%E9%A1%B5%E4%BB%AA%E8%A1%A8%E7%9B%98.webp" width="800" alt="主页仪表盘"> |
| 后台仪表盘 | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E5%90%8E%E5%8F%B0%E4%BB%AA%E8%A1%A8%E7%9B%98.webp" width="800" alt="后台仪表盘"> |
| 历史图表 | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E5%8E%86%E5%8F%B2%E5%9B%BE%E8%A1%A8.webp" width="800" alt="历史图表"> |
| 网页终端 | <img src="https://b2.akz.moe/awesome-pictures/komari-screenshot/%E7%BD%91%E9%A1%B5%E7%BB%88%E7%AB%AF.webp" width="800" alt="网页终端"> |

## 致谢

Komari 由 [Akizon77](https://github.com/Akizon77) 与
[Komari 的贡献者们](https://github.com/komari-monitor/komari/graphs/contributors) 创建。
没有他们的工作就没有这个 fork。上游文档仍在 [komari.wiki](https://www.komari.wiki/)。

本项目基于 MIT 许可证，见 [LICENSE](./LICENSE)。原始版权声明已原样保留。
