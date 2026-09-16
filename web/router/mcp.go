package router

// mcp.go
// 把 MCP（Model Context Protocol）端点挂到 /api/mcp，供 AI agent 接入。
//
// 传输层用 MCP 官方的 Streamable HTTP：
//   POST   /api/mcp  发送 JSON-RPC 请求
//   GET    /api/mcp  打开 SSE 流接收服务端推送
//   DELETE /api/mcp  结束会话
//
// 鉴权复用既有的管理员校验（api.RequireRole(api.RoleAdmin)），
// 即 Authorization: Bearer <API Key> 或已登录的会话 Cookie，
// 与 /api/admin/* 同一套，不新增凭据体系。

import (
	"net/http"

	"github.com/gin-gonic/gin"
	appmcp "github.com/komari-monitor/komari/internal/mcp"
	logger "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/web/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerMCPRoutes 注册 MCP 端点。KOMARI_MCP_ENABLED=false 时整体跳过。
func registerMCPRoutes(r *gin.Engine) {
	if !appmcp.Enabled() {
		logger.Infof("mcp", "MCP endpoint disabled (KOMARI_MCP_ENABLED=false)")
		return
	}

	// SDK 默认开启 DNS rebinding 防护：当服务监听在回环地址、而请求的 Host 头
	// 不是回环地址时直接 403。这在反向代理场景下会误伤——nginx 连的是
	// 127.0.0.1:25774，转发过来的 Host 却是对外域名，于是被判定为重绑定攻击。
	//
	// 因此提供开关：部署在反代后面时设 KOMARI_MCP_TRUST_PROXY_HOST=true。
	// 默认保持防护开启，因为直连 localhost 才是更常见的初始形态，
	// 且该防护是抵御浏览器发起的 DNS 重绑定攻击的最后一道门。
	opts := &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: appmcp.TrustProxyHost(),
	}

	// server 实例在进程内复用：工具注册是一次性的，无需按请求重建。
	// handler 自身维护会话表，同样必须单例。
	server := appmcp.NewServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, opts)

	// handler 按 HTTP 方法分发，不解析路径，所以直接交给它即可，无需 StripPrefix。
	g := r.Group("/api/mcp", api.RequireRole(api.RoleAdmin))
	g.Any("", gin.WrapH(handler))

	policy := appmcp.LoadPolicy()
	logger.Infof("mcp", "MCP endpoint ready at /api/mcp (dangerous=%v, trust_proxy_host=%v)",
		policy.AllowDangerous, opts.DisableLocalhostProtection)
}
