package mcp

// server_test.go
// 通过内存传输跑完整的 MCP 握手，验证工具清单符合预期。
//
// tools/list 不触达数据库，因此这些用例无需 DB 夹具即可运行。

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTestClient 启动一个 MCP server 并用内存传输接上客户端，返回已完成初始化的会话。
func connectTestClient(t *testing.T) *mcp.ClientSession {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := NewServer()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

func listToolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

// 握手能完成，且暴露出预期的语义化工具与内省工具。
func TestServerExposesExpectedTools(t *testing.T) {
	t.Setenv("KOMARI_MCP_ALLOW_DANGEROUS", "false")

	cs := connectTestClient(t)
	names := listToolNames(t, cs)

	want := []string{
		// 内省
		"komari_list_methods",
		"komari_method_help",
		"komari_call",
		// 监控
		"list_servers",
		"get_servers_status",
		"get_server_metrics_history",
		"get_server_recent_records",
		"get_ping_records",
		"list_alert_rules",
		// 管理
		"list_servers_admin",
		"get_server_detail",
		"add_server",
		"edit_server",
	}
	for _, n := range want {
		if !names[n] {
			t.Errorf("缺少工具 %q，实际清单: %v", n, keysOf(names))
		}
	}
}

// 每个工具都必须带非空描述和输入 schema，否则模型无法正确选用。
func TestAllToolsHaveDescriptionAndSchema(t *testing.T) {
	cs := connectTestClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("工具清单为空")
	}
	for _, tool := range res.Tools {
		if tool.Description == "" {
			t.Errorf("工具 %q 缺少 description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("工具 %q 缺少 inputSchema", tool.Name)
		}
	}
}

// 默认策略下，通用入口 komari_call 必须拒绝高危方法。
func TestCallToolRejectsDangerousMethodByDefault(t *testing.T) {
	t.Setenv("KOMARI_MCP_ALLOW_DANGEROUS", "false")

	cs := connectTestClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "komari_call",
		Arguments: map[string]any{"method": "admin:exec"},
	})
	if err != nil {
		t.Fatalf("CallTool 传输层报错: %v", err)
	}
	if !res.IsError {
		t.Fatal("调用 admin:exec 应当被策略拒绝，但返回了成功")
	}
}

// 工具名必须稳定：agent 侧的提示词与用户习惯都依赖它，改名属于破坏性变更。
func TestToolNamesAreStable(t *testing.T) {
	cs := connectTestClient(t)
	names := listToolNames(t, cs)

	if len(names) < 13 {
		t.Errorf("工具数量 %d 少于预期的 13，可能有工具注册失败: %v", len(names), keysOf(names))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
