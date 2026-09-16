package mcp

// server.go
// 把 Komari 的 JSON-RPC 方法暴露为 MCP（Model Context Protocol）工具，
// 使 AI agent 能直接查询监控数据、管理探针。
//
// 设计要点：
//   - 进程内调用。走 jsonrpc.OnInternalRequest 直接进分发器，不绕 HTTP，
//     因此不需要 API Key、不额外占用连接，也不会和主进程争抢 SQLite。
//   - 工具分两层。常用监控场景封装成语义化工具（见 tools.go）；
//     其余 80+ 方法通过 komari_list_methods / komari_method_help / komari_call
//     这组内省工具按需访问，Komari 新增 RPC 方法时 MCP 无需改代码即可跟上。
//   - 统一过策略。所有调用（含语义化工具）都经过 policy.go 的档位校验。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/komari-monitor/komari/utils"
	jsonrpc "github.com/komari-monitor/komari/web/rpc/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "komari"

// callRPC 以管理员身份在进程内调用一个 Komari RPC 方法。
//
// 调用前强制过策略校验：即使语义化工具写错了方法名，也不会越过 dangerous 档。
func callRPC(ctx context.Context, p Policy, method string, params any) (any, error) {
	if !p.Allows(method) {
		return nil, fmt.Errorf(
			"方法 %s 属于 %s 档，当前未启用。如确需使用，请设置环境变量 KOMARI_MCP_ALLOW_DANGEROUS=true 并重启 Komari",
			method, ClassifyMethod(method),
		)
	}

	resp := jsonrpc.OnInternalRequest(ctx, rpc.RoleAdmin, method, params)
	if resp == nil {
		return nil, fmt.Errorf("调用 %s 未返回响应", method)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("调用 %s 失败: %s", method, resp.Error.Message)
	}
	return resp.Result, nil
}

// jsonResult 把任意结果序列化成 MCP 的文本内容。
//
// 输出统一走 text content 而非 structured content：Komari 各方法的返回结构差异很大，
// 逐个声明 output schema 收益低且容易与上游脱节，交给模型读 JSON 更实际。
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("序列化结果失败: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

// errResult 把错误作为工具调用失败返回给 agent（而非协议层错误），
// 这样模型能读到原因并自行调整，而不是整个会话中断。
func errResult(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}, nil, nil
}

// NewServer 构造一个配置完整的 MCP server 实例。
func NewServer() *mcp.Server {
	p := LoadPolicy()

	instructions := "Komari 服务器监控系统。可查询被监控主机（节点/探针）的在线状态、" +
		"CPU/内存/磁盘/网络指标与历史记录，并管理探针与告警规则。\n\n" +
		"先用 list_servers 获取节点及其 uuid，再用 uuid 查询具体指标。\n" +
		"若封装好的工具不满足需求，用 komari_list_methods 查看全部可用 RPC 方法，" +
		"komari_method_help 查看某方法的参数说明，再用 komari_call 调用。"
	if !p.AllowDangerous {
		instructions += "\n\n注意：远程命令执行、裸 SQL、文件写入等高危操作当前已禁用。"
	}

	s := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Title:   "Komari Monitor",
		Version: utils.CurrentVersion,
	}, &mcp.ServerOptions{
		Instructions: instructions,
	})

	registerIntrospectionTools(s, p)
	registerMonitoringTools(s, p)
	registerManagementTools(s, p)

	return s
}

// ---------- 内省工具：让 agent 能访问未被语义化封装的方法 ----------

type listMethodsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"只列出该命名空间的方法，可选 public/common/admin；留空返回全部"`
}

type methodHelpInput struct {
	Method string `json:"method" jsonschema:"完整方法名，形如 common:getNodes 或 admin:listClients"`
}

type callInput struct {
	Method string         `json:"method" jsonschema:"完整方法名，形如 common:getNodes"`
	Params map[string]any `json:"params,omitempty" jsonschema:"方法参数键值对，无参数时可省略"`
}

func registerIntrospectionTools(s *mcp.Server, p Policy) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "komari_list_methods",
		Description: "列出当前 MCP 策略下可调用的全部 Komari RPC 方法，并标注每个方法的风险档位" +
			"（read 只读 / manage 管理 / dangerous 高危）。当封装好的工具不够用时，先调这个。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMethodsInput) (*mcp.CallToolResult, any, error) {
		raw, err := callRPC(ctx, p, "rpc.methods", map[string]any{})
		if err != nil {
			return errResult(err)
		}
		names, ok := raw.([]string)
		if !ok {
			// rpc.methods 正常返回 []string；此处兜底处理经 JSON 往返后的 []any。
			if anys, ok2 := raw.([]any); ok2 {
				names = make([]string, 0, len(anys))
				for _, v := range anys {
					if s, ok3 := v.(string); ok3 {
						names = append(names, s)
					}
				}
			} else {
				return errResult(fmt.Errorf("rpc.methods 返回了非预期类型 %T", raw))
			}
		}

		type methodInfo struct {
			Method string `json:"method"`
			Tier   string `json:"tier"`
		}
		out := make([]methodInfo, 0, len(names))
		for _, name := range names {
			if in.Namespace != "" && !hasNamespace(name, in.Namespace) {
				continue
			}
			if !p.Allows(name) {
				continue // 未启用的高危方法不出现在清单里，避免模型反复尝试
			}
			out = append(out, methodInfo{Method: name, Tier: ClassifyMethod(name).String()})
		}
		return jsonResult(out)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "komari_method_help",
		Description: "查看某个 Komari RPC 方法的元数据：用途说明、参数列表（名称/类型/是否必填）与返回类型。" +
			"在用 komari_call 调用不熟悉的方法前先查这个。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in methodHelpInput) (*mcp.CallToolResult, any, error) {
		if in.Method == "" {
			return errResult(fmt.Errorf("method 不能为空"))
		}
		if !p.Allows(in.Method) {
			return errResult(fmt.Errorf("方法 %s 属于 %s 档，当前未启用", in.Method, ClassifyMethod(in.Method)))
		}
		raw, err := callRPC(ctx, p, "rpc.help", map[string]any{"method": in.Method})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(map[string]any{
			"tier": ClassifyMethod(in.Method).String(),
			"meta": raw,
		})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "komari_call",
		Description: "直接调用任意一个被策略放行的 Komari RPC 方法。这是通用入口，" +
			"用于封装工具覆盖不到的场景。调用前建议先用 komari_method_help 确认参数。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in callInput) (*mcp.CallToolResult, any, error) {
		if in.Method == "" {
			return errResult(fmt.Errorf("method 不能为空"))
		}
		params := in.Params
		if params == nil {
			params = map[string]any{}
		}
		raw, err := callRPC(ctx, p, in.Method, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})
}

func hasNamespace(method, ns string) bool {
	return len(method) > len(ns) && method[:len(ns)] == ns && method[len(ns)] == ':'
}
