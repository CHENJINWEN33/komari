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

### 使用二进制文件

从 [Releases](https://github.com/CHENJINWEN33/komari/releases) 下载对应平台的文件并运行：

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
