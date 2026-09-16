# Komari — 项目指南

自托管服务器监控工具。本仓库是 **Go 后端**；前端是独立仓库。

## 仓库拓扑（重要）

| 仓库 | 内容 | 本地路径 |
|---|---|---|
| `komari`（本仓库） | Go 后端、API、Agent 协议、数据库 | `C:\Users\chen\komari` |
| `komari-web` | React + Vite 前端（默认主题） | `C:\Users\chen\komari-web` |

本仓库的 `web/` **不是前端源码**，而是 Go 的 HTTP 层（`web/api`、`web/router`、`web/rpc`、`web/agent` 等）。

前端在 CI 构建时由 `.github/actions/build-frontend/action.yml` 克隆 `komari-web`、`npm run build`，
产物打包成 `web/public/defaultTheme/dist.tar.zst`，通过 `web/public/public.go` 的 `//go:embed` 嵌入二进制。

## 编译期硬性前提

`web/public/public.go` 有两条 embed 指令：

```go
//go:embed defaultTheme/komari-theme.json
//go:embed defaultTheme/dist.tar.zst
```

这两个文件**不在版本库里**（CI 现生成）。缺失时 `go build` 会直接编译失败，
报 `pattern defaultTheme/dist.tar.zst: no matching files found`。
所以首次编译前必须先跑一遍前端构建（见下）。

## 本地开发工作流

### 日常开发（推荐：前后端分离）

`komari-web/vite.config.ts` 在 development 模式下配置了代理，
把 `/api` 和 `/themes` 转发到 `VITE_API_TARGET`（默认 `http://127.0.0.1:25774`）。

```bash
# 终端 1：后端
cd C:\Users\chen\komari
go run . server            # 监听 0.0.0.0:25774

# 终端 2：前端（热更新）
cd C:\Users\chen\komari-web
npm run dev
```

访问 vite 给出的地址，改前端代码即时生效，无需重新编译 Go。

### 首次构建 / 出单文件二进制

前端产物需要打包成 tar + zstd 放到 `web/public/defaultTheme/`。
**不需要安装 zstd 命令行工具** —— Node 18+ 自带 zstd（`zlib.zstdCompressSync`），
Go 侧用 `klauspost/compress/zstd` 的 `DecodeAll` 解单帧，格式完全兼容。

```bash
# 1. 构建前端
cd /c/Users/chen/komari-web && npm run build

# 2. 打包嵌入产物（注意 tar 的 -f 必须用 /c/... POSIX 路径，
#    传 C:/... 会被 GNU tar 当成远程主机名而失败）
cd /c/Users/chen/komari
mkdir -p web/public/defaultTheme
tar -cf /tmp/dist.tar -C /c/Users/chen/komari-web/dist .
node -e "const z=require('zlib'),f=require('fs');f.writeFileSync(process.argv[2],z.zstdCompressSync(f.readFileSync(process.argv[1]),{params:{[z.constants.ZSTD_c_compressionLevel]:19}}))"   /tmp/dist.tar web/public/defaultTheme/dist.tar.zst
rm -f /tmp/dist.tar
cp /c/Users/chen/komari-web/komari-theme.json web/public/defaultTheme/

# 3. 编译
go build -ldflags="-s -w" -o komari.exe .   # -s -w 必须加，见下节
```

`web/public/defaultTheme/` 是生成物，已由 `web/public/.gitignore`（`defaultTheme/*`）忽略，不会污染 `git status`。

## dev 模式 403 陷阱：CORS Origin 校验 vs vite 代理

**症状**：通过 `localhost:5173` 访问时，页面能打开、GET 请求正常，但所有 POST
（登录、`/api/rpc2` 等）返回 403，前端显示 "Network error"。直连 `127.0.0.1:25774` 则一切正常。

**原因**：`web/security/cors.go` 的中间件对 `/api/*` 做 Origin 校验：

```go
if origin != "" && allowOrigin == "" {
    c.AbortWithStatus(http.StatusForbidden)
}
```

放行条件之一是 `OriginMatchesHost(origin, c.Request.Host)`，而 `web/security/origin.go:25`
做的是**主机名全等比较**。

`komari-web/vite.config.ts` 的代理配了 `changeOrigin: true`，它把 `Host` 改写成目标
`127.0.0.1:25774`，但浏览器发来的 `Origin: http://localhost:5173` **原样转发**。
两者不相等 → 403。GET 请求浏览器不带 `Origin`，所以畅通无阻，症状才显得怪。

**当前状态：已采用方案 1** —— `http://localhost:5173` 已加入后台
「设置 → 站点 → API CORS 允许列表」，5173 的 POST 已验证通过（403 → 401）。

**解法（二选一）**：

1. **加白名单（推荐，无需改代码）**：先直连 `http://127.0.0.1:25774` 登录，
   进后台设置把 `http://localhost:5173` 加入 CORS 允许来源
   （配置键 `cors_allowed_origins`，见 `internal/config/settings.go:49`）。
   走的是产品自带机制，不与上游代码分叉。

2. **改 vite 代理**：在 `komari-web/vite.config.ts` 把 `/api` 的 `changeOrigin` 改为 `false`，
   这样 `Host` 保持 `localhost:5173`，与 `Origin` 相等即可通过。
   代价是本地前端仓库与上游产生分叉。

排查提示：403 与 401 的区别很关键 —— 401 说明已经走到密码校验（Origin 检查通过了），
403 说明在中间件就被拦下。用 `curl`（默认不带 `Origin` 头）测接口会得到 401，
看起来"接口正常"，从而误判。复现浏览器行为必须手动加 `-H "Origin: ..."`。

## Windows 构建陷阱：必须加 `-ldflags="-s -w"`

`internal/sqlitetune/connector.go` 直接 import 了 `github.com/mattn/go-sqlite3`（非测试代码），
所以 **cgo 是强制的**，`CGO_ENABLED=0` 编译不过。Go 会调用系统 C 编译器做外部链接。

本机的 `C:/Program Files/mingw64/bin/gcc`（MinGW-w64 gcc 8.1.0，2018 年的 binutils）
链接出的 PE 里，DWARF 调试节（`/4`、`/20`、`/36`）排在文件最前面且
`PointerToRawData` 不是 `FileAlignment`(512) 的整数倍 —— 违反 PE 规范。
产物看着正常（`file` 报 PE32+ x86-64），但 Windows 加载器直接拒绝：

- PowerShell：`The specified executable is not a valid application for this OS platform`
- Git Bash：`cannot execute binary file: Exec format error`

**解法**：`-ldflags="-s -w"` 剥掉调试信息，那几个越界的节随之消失，
节数从 23 降到 11 且全部对齐，二进制即可正常运行（体积也从 69MB 降到 35MB）：

```bash
go build -ldflags="-s -w" -o komari.exe .
```

注意 `go run . server` 不受影响（走临时构建路径）。只有产出 `.exe` 时需要这个 flag。

上游 CI 不踩这个坑，是因为 `.github/workflows/build.yml` 用 **zig** 当交叉编译器
（`zig_target_triple: x86_64-windows-gnu`）而不是 gcc。
若想带调试信息本地构建（比如用 delve 调试），需装 zig 或更新的 MinGW-w64，
然后 `CC="zig cc -target x86_64-windows-gnu" go build -o komari.exe .`。

## 版本号：本地构建显示 0.0.1 是正常的

`utils/version.go` 里 `CurrentVersion = "0.0.1"` / `VersionHash = "unknown"` 只是兜底值。
真实版本由 CI 在构建时用 ldflags 注入，**本地 `go build` 不带这些 flag 就会显示 0.0.1**。
这跟代码新旧无关，别据此判断需要升级——用 `git describe --tags` 才是准的。

副作用：`main.go:12` 判断 `VersionHash == "unknown"` 时会把日志级别设为 **Debug**，
注入版本后自动变回 Info。CI 还会设 `GIN_MODE=release` 关掉 gin 的调试输出。

本地带版本信息构建：

```bash
VERSION=$(git describe --tags --always)      # 例如 1.5.0-fix1
VERSION_HASH=$(git rev-parse --short HEAD)   # 例如 0ca87aa
GIN_MODE=release go build -trimpath   -ldflags="-s -w -X github.com/komari-monitor/komari/utils.CurrentVersion=${VERSION} -X github.com/komari-monitor/komari/utils.VersionHash=${VERSION_HASH}"   -o komari.exe .
```

注意：Windows 上正在运行的 `.exe` 被文件锁占用，重新构建前必须先停掉服务。

## 与上游同步

```bash
git fetch upstream --tags
git log --oneline HEAD..upstream/main      # 看落后了什么
git rebase upstream/main                   # 在 feature 分支上做
```

## 主题覆盖机制（改 UI 时的捷径）

`web/public/public.go` 的 `getFileContent` 有回退链：先查 `./data/theme/<themeID>/<path>`，
命中则用本地文件，否则回退到嵌入的 default theme。

所以把自定义前端产物放进 `./data/theme/<你的主题ID>/dist/`，
在后台设置里把 theme 切到该 ID，就能替换界面而**不重新编译后端**。

注意：`StaticRestricted` 会强制走嵌入的默认主题，登录页和恢复页不受主题覆盖影响（安全设计，别绕过）。

## 二次开发：MCP 端点（本 fork 新增）

`internal/mcp/` + `web/router/mcp.go`，把 Komari 暴露为 MCP 工具供 AI agent 接入。
完整说明见 `internal/mcp/README.md`。

要点：
- 端点 `/api/mcp`，传输为 MCP 官方 Streamable HTTP，鉴权复用 `api.RequireRole(api.RoleAdmin)`
- 调用走 `jsonrpc.OnInternalRequest` 进程内分发，不绕 HTTP、不争抢 SQLite
- 工具分两层：语义化工具（常用监控/管理场景）+ 内省工具（`komari_list_methods` /
  `komari_method_help` / `komari_call`，基于 `rpc.methods` 和 `rpc.help` 动态发现，
  上游新增 RPC 方法时无需改 MCP 代码）
- 风险三档 read / manage / dangerous，高危档默认关闭

环境变量：`KOMARI_MCP_ENABLED`（默认 true）、`KOMARI_MCP_ALLOW_DANGEROUS`（默认 false）、
`KOMARI_MCP_TRUST_PROXY_HOST`（默认 false，反代部署被 403 时打开）。

改动 `internal/mcp/` 后务必跑 `go test ./internal/mcp/...` ——
其中 `server_test.go` 会通过内存传输跑一次真实 MCP 握手，能抓到工具注册失败。

## 关键入口

- `main.go` → `cmd/root.go` → `cmd/server.go`（`RunServer`）
- `internal/server` — 应用生命周期：Bootstrap → 安装引导 → 数据库迁移 → 运行
- `cmd/` 还有运维子命令：`chpasswd`、`disable2FA`、`permitPasswordLogin`
- 监听地址：`-l` / `--listen` 或环境变量 `KOMARI_LISTEN`，默认 `0.0.0.0:25774`
- 本地数据：`./data`、`komari.db`（均已 gitignore）

## Git 约定

- `origin` = fork（CHENJINWEN33/komari），`upstream` = 上游（komari-monitor/komari）
- 上游的 `.github/workflows/auto-merge-dev-to-main.yml` 说明上游用 dev → main 流程
- 二次开发请开 feature 分支，不要直接改 `main`，便于日后 `git rebase upstream/main` 同步

## 工具链要求

- Go **1.25.0**（见 `go.mod`）
- Node（CI 用 23，本机 24.19 可用）
- zstd：**不需要**，用 Node 内置的 zstd 代替（见上）
