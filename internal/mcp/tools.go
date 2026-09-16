package mcp

// tools.go
// 把高频监控与管理场景封装成语义化 MCP 工具。
//
// 这一层是为了让 agent 不必先内省再拼方法名就能完成常见任务。
// 长尾需求交给 server.go 里的 komari_call 处理。
//
// 每个工具对应的底层 RPC 方法在注释中标注，便于与上游路由定义
// （web/router/router.go）对照维护。

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------- 监控查询 ----------

type listServersInput struct {
	UUID string `json:"uuid,omitempty" jsonschema:"只查询指定 uuid 的节点；留空返回全部节点"`
}

type serversStatusInput struct {
	UUIDs []string `json:"uuids,omitempty" jsonschema:"只查询这些 uuid 的最新状态；留空返回全部节点"`
}

type metricsHistoryInput struct {
	UUID     string `json:"uuid" jsonschema:"目标节点的 uuid，可由 list_servers 获得"`
	LoadType string `json:"load_type,omitempty" jsonschema:"指标类型，如 cpu / ram / disk / net；留空由服务端返回默认集合"`
	Hours    int    `json:"hours,omitempty" jsonschema:"回溯小时数，默认 1"`
}

type recentRecordsInput struct {
	UUID string `json:"uuid" jsonschema:"目标节点的 uuid"`
}

type pingRecordsInput struct {
	UUID   string `json:"uuid,omitempty" jsonschema:"目标节点的 uuid"`
	TaskID string `json:"task_id,omitempty" jsonschema:"ping 任务 ID"`
	Hours  int    `json:"hours,omitempty" jsonschema:"回溯小时数，默认 1"`
}

func registerMonitoringTools(s *mcp.Server, p Policy) {
	// common:getNodes —— 节点静态信息（名称、地区、系统、硬件规格等）
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_servers",
		Description: "列出所有被监控的服务器节点及其基础信息（名称、地区、操作系统、CPU/内存/磁盘规格）。" +
			"返回结果里的 uuid 是后续所有按节点查询的入口，通常先调这个。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listServersInput) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if in.UUID != "" {
			params["uuid"] = in.UUID
		}
		raw, err := callRPC(ctx, p, "common:getNodes", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// common:getNodesLatestStatus —— 最新一次上报（在线与否、当前负载）
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_servers_status",
		Description: "获取节点最新一次上报的实时状态：在线/离线、CPU 与内存占用、磁盘使用、网络速率、负载、进程数等。" +
			"排查「哪台机器现在有问题」时用这个；看趋势请用 get_server_metrics_history。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in serversStatusInput) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		switch len(in.UUIDs) {
		case 0:
			// 不传参数即返回全部
		case 1:
			params["uuid"] = in.UUIDs[0]
		default:
			params["uuids"] = in.UUIDs
		}
		raw, err := callRPC(ctx, p, "common:getNodesLatestStatus", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// public:getRecordsByUUID —— 历史负载曲线
	// 参数取自 web/router/router.go 的 WithQuery("uuid", "load_type", "hours") 绑定。
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_server_metrics_history",
		Description: "查询单个节点的历史负载记录，用于分析趋势、定位异常时间点、判断是突发还是持续劣化。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in metricsHistoryInput) (*mcp.CallToolResult, any, error) {
		if in.UUID == "" {
			return errResult(fmt.Errorf("uuid 不能为空，可先调用 list_servers 获取"))
		}
		params := map[string]any{"uuid": in.UUID}
		if in.LoadType != "" {
			params["load_type"] = in.LoadType
		}
		if in.Hours > 0 {
			params["hours"] = in.Hours
		}
		raw, err := callRPC(ctx, p, "public:getRecordsByUUID", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// public:getClientRecentRecords —— 最近一段时间的原始上报
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_server_recent_records",
		Description: "获取单个节点最近的原始上报记录（比 get_server_metrics_history 粒度更细），适合排查刚刚发生的抖动。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentRecordsInput) (*mcp.CallToolResult, any, error) {
		if in.UUID == "" {
			return errResult(fmt.Errorf("uuid 不能为空，可先调用 list_servers 获取"))
		}
		raw, err := callRPC(ctx, p, "public:getClientRecentRecords", map[string]any{"uuid": in.UUID})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// public:getPingRecords —— 网络延迟/连通性
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_ping_records",
		Description: "查询 ping 探测历史（延迟与丢包），用于判断网络连通性问题。可先用 komari_call 调 public:getPublicPingTasks 查看有哪些探测任务。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pingRecordsInput) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if in.UUID != "" {
			params["uuid"] = in.UUID
		}
		if in.TaskID != "" {
			params["task_id"] = in.TaskID
		}
		if in.Hours > 0 {
			params["hours"] = in.Hours
		}
		raw, err := callRPC(ctx, p, "public:getPingRecords", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// 告警规则总览：合并负载告警与离线告警两类
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_alert_rules",
		Description: "列出所有已配置的告警规则，包含负载类告警（CPU/内存等阈值）与离线告警。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		load, err := callRPC(ctx, p, "admin:getAllLoadNotifications", map[string]any{})
		if err != nil {
			return errResult(err)
		}
		offline, err := callRPC(ctx, p, "admin:listOfflineNotifications", map[string]any{})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(map[string]any{
			"load_notifications":    load,
			"offline_notifications": offline,
		})
	})
}

// ---------- 探针管理 ----------

type addServerInput struct {
	Name string `json:"name,omitempty" jsonschema:"新节点的显示名称；留空则由服务端生成默认名"`
}

type editServerInput struct {
	UUID   string         `json:"uuid" jsonschema:"要修改的节点 uuid"`
	Fields map[string]any `json:"fields" jsonschema:"要更新的字段键值对，例如 {\"name\":\"新名称\",\"region\":\"HK\"}；仅传需要改的字段"`
}

type getServerInput struct {
	UUID string `json:"uuid" jsonschema:"目标节点的 uuid"`
}

func registerManagementTools(s *mcp.Server, p Policy) {
	// admin:listClients —— 管理视角的节点清单
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_servers_admin",
		Description: "以管理视角列出所有节点（含 list_servers 看不到的管理字段，如排序权重、分组、隐藏状态）。" +
			"日常查询用 list_servers 即可，需要管理字段时才用这个。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		raw, err := callRPC(ctx, p, "admin:listClients", map[string]any{})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// admin:getClient
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_server_detail",
		Description: "获取单个节点的完整配置详情（管理视角）。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getServerInput) (*mcp.CallToolResult, any, error) {
		if in.UUID == "" {
			return errResult(fmt.Errorf("uuid 不能为空"))
		}
		raw, err := callRPC(ctx, p, "admin:getClient", map[string]any{"uuid": in.UUID})
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// admin:addClient —— 注意：返回体含 token，是探针接入凭据
	mcp.AddTool(s, &mcp.Tool{
		Name: "add_server",
		Description: "新增一个被监控节点，返回该节点的 uuid 与接入 token。" +
			"token 需要填入目标主机上的探针（komari-agent）配置才能完成接入。" +
			"注意：返回值包含凭据，请勿转发到不可信位置。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addServerInput) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if in.Name != "" {
			params["name"] = in.Name
		}
		raw, err := callRPC(ctx, p, "admin:addClient", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(raw)
	})

	// admin:editClient —— 局部更新
	mcp.AddTool(s, &mcp.Tool{
		Name: "edit_server",
		Description: "修改节点配置（局部更新，只需传要改的字段）。" +
			"可用字段名请先用 get_server_detail 查看当前节点结构。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editServerInput) (*mcp.CallToolResult, any, error) {
		if in.UUID == "" {
			return errResult(fmt.Errorf("uuid 不能为空"))
		}
		if len(in.Fields) == 0 {
			return errResult(fmt.Errorf("fields 不能为空，至少指定一个要修改的字段"))
		}
		params := map[string]any{"uuid": in.UUID}
		for k, v := range in.Fields {
			if k == "uuid" {
				continue // 不允许借 fields 改写目标 uuid
			}
			params[k] = v
		}
		raw, err := callRPC(ctx, p, "admin:editClient", params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(map[string]any{"ok": true, "uuid": in.UUID, "result": raw})
	})
}
