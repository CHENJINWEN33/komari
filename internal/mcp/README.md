# MCP 端点

把 Komari 的监控与管理能力暴露为 [MCP](https://modelcontextprotocol.io/)
工具，供 AI agent（Claude Code、Claude Desktop 等）直接接入。

## 端点

```
POST   /api/mcp   发送 JSON-RPC 请求
GET    /api/mcp   打开 SSE 流
DELETE /api/mcp   结束会话
```

传输层为 MCP 官方的 Streamable HTTP（`github.com/modelcontextprotocol/go-sdk`）。

鉴权复用既有的管理员校验，与 `/api/admin/*` 同一套：
`Authorization: Bearer <API Key>` 或已登录的会话 Cookie。不新增凭据体系。

## 接入方式

1. 在后台「设置」里生成 API Key（配置键 `api_key`，长度需 ≥ 12）。
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

## 工具

### 内省（覆盖长尾需求）

| 工具 | 说明 |
|---|---|
| `komari_list_methods` | 列出当前策略放行的全部 RPC 方法及其风险档位 |
| `komari_method_help` | 查看某方法的参数与返回类型 |
| `komari_call` | 调用任意被放行的 RPC 方法 |

这三个工具基于 `pkg/rpc` 的运行时元数据（`rpc.methods` / `rpc.help`），
因此上游新增 RPC 方法时 MCP 无需改代码即可访问。

### 监控查询

`list_servers`、`get_servers_status`、`get_server_metrics_history`、
`get_server_recent_records`、`get_ping_records`、`list_alert_rules`

### 探针管理

`list_servers_admin`、`get_server_detail`、`add_server`、`edit_server`

## 风险分档

`policy.go` 把每个 RPC 方法归入三档：

| 档位 | 含义 | 默认 |
|---|---|---|
| `read` | 只读查询，不改变状态 | 开放 |
| `manage` | 可逆的业务配置变更 | 开放 |
| `dangerous` | 远程命令执行、裸 SQL、文件写、凭据读取、安全设置变更、不可逆删除 | **关闭** |

判定顺序：显式高危名单 → `public:`/`common:` 命名空间归 read →
`admin:` 下以 `get`/`list`/`test` 开头归 read → 其余归 manage。

**未知方法落 manage 而非 read**，保证上游新增方法不会因策略疏漏被当成安全只读接口。

高危名单收录标准是「一次调用即可造成不可逆后果，或可直接扩大调用者自身权限」，
其中 `admin:editSettings` 被列为高危的原因是它能改 API Key、关闭 CORS 校验等安全开关。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `KOMARI_MCP_ENABLED` | `true` | 设为 `false` 可整体关闭 MCP 端点 |
| `KOMARI_MCP_ALLOW_DANGEROUS` | `false` | 设为 `true` 放开高危档工具 |
| `KOMARI_MCP_TRUST_PROXY_HOST` | `false` | 部署在反向代理后且 MCP 被 403 时设为 `true` |

关于 `KOMARI_MCP_TRUST_PROXY_HOST`：SDK 默认开启 DNS rebinding 防护 ——
服务监听回环地址、但请求 `Host` 头不是回环地址时返回 403。
反向代理场景下 nginx 连的是 `127.0.0.1:25774`、转发的 `Host` 却是对外域名，
会被误判。此时打开该开关。

## 安全须知

Komari 的 API Key 权限等同管理员，且 `web/api/AuthSensitive.go` 中
`VerifySensitive2FACore` 对 API Key **直接豁免 2FA**。
因此持有 API Key 的 agent 拥有完整管理员能力。若开启 `KOMARI_MCP_ALLOW_DANGEROUS`，
agent 将能在所有被监控主机上执行任意命令。请按此前提管理该 Key。

## 实现要点

调用走 `jsonrpc.OnInternalRequest`，即**进程内**进入 RPC 分发器，
不绕 HTTP、不额外占连接，也不会与主进程争抢 SQLite 文件。

所有调用（含语义化工具）统一经过 `Policy.Allows` 校验，
即使语义化工具写错方法名也不会越过高危档。
