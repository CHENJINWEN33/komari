package mcp

// redact_test.go
// 凭据剥离：非高危方法的返回体里夹带的凭据，未开启高危档时不能交给 agent。
//
// 端到端用例走内存 SQLite 跑真实的 RPC 方法，覆盖「语义化工具 → callRPC → 上游 handler」整条链路。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/internal/metricstore"
	"github.com/komari-monitor/komari/pkg/rpc"
	jsonrpc "github.com/komari-monitor/komari/web/rpc/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:internal_mcp_test?mode=memory&cache=shared"

	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}

	os.Exit(m.Run())
}

func TestRedactStripsNestedFields(t *testing.T) {
	in := map[string]any{
		"current": "",
		"data": []models.Session{
			{UUID: "u1", Session: "cookie-1", Ip: "1.1.1.1"},
			{UUID: "u2", Session: "cookie-2", Ip: "2.2.2.2"},
		},
	}
	out, err := redact(in, []string{"session"})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "cookie-") {
		t.Fatalf("会话凭据未被剔除: %s", b)
	}
	if !strings.Contains(string(b), "2.2.2.2") {
		t.Fatalf("非敏感字段不应被剔除: %s", b)
	}
}

// 大整数经剥离后必须保持原值，不能被 float64 截断。
func TestRedactPreservesLargeIntegers(t *testing.T) {
	const big int64 = 1<<62 + 1
	out, err := redact(map[string]any{"traffic_limit": big, "token": "x"}, []string{"token"})
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	b, _ := json.Marshal(out)
	if want := `{"traffic_limit":4611686018427387905}`; string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}

func TestPolicyRedactionOnlyWhenLocked(t *testing.T) {
	locked := Policy{AllowDangerous: false}
	open := Policy{AllowDangerous: true}

	for _, m := range []string{"common:getNodes", "admin:listClients", "admin:getClient", "admin:getSessions", "admin:getSettings"} {
		if len(locked.RedactedFields(m)) == 0 {
			t.Errorf("默认策略下 %s 应剔除凭据字段", m)
		}
		if len(open.RedactedFields(m)) != 0 {
			t.Errorf("开启高危档后 %s 不应再剔除字段", m)
		}
	}
	if got := locked.RedactedFields("common:getNodesLatestStatus"); len(got) != 0 {
		t.Errorf("common:getNodesLatestStatus 不应剔除字段，got %v", got)
	}

	params := map[string]any{"uuid": "x", "token": "y"}
	if locked.ForbiddenParam("admin:editClient", params) != "token" {
		t.Error("默认策略应拒绝通过 editClient 改写 token")
	}
	if open.ForbiddenParam("admin:editClient", params) != "" {
		t.Error("开启高危档后应允许改写 token")
	}
	if locked.ForbiddenParam("admin:editClient", map[string]any{"uuid": "x", "name": "n"}) != "" {
		t.Error("普通字段不应被拒绝")
	}
	// 名字里带 token 但不是 token 列的字段不能误伤
	if locked.ForbiddenParam("admin:editClient", map[string]any{"uuid": "x", "token_note": "n"}) != "" {
		t.Error("token_note 不是 token 列，不应被拒绝")
	}
}

// tokenParamVariants 是同样会落到 clients.token 列的参数名写法：
// GORM 认 Go 字段名（Token），认不出的键直接当列名，而列名不区分大小写。
var tokenParamVariants = []string{"Token", "TOKEN", "ToKeN", "clients.token", "`token`", `"token"`}

func TestForbiddenParamMatchesColumnNotSpelling(t *testing.T) {
	locked := Policy{AllowDangerous: false}
	for _, name := range tokenParamVariants {
		if got := locked.ForbiddenParam("admin:editClient", map[string]any{"uuid": "x", name: "y"}); got != "token" {
			t.Errorf("参数名 %q 应被识别为 token，got %q", name, got)
		}
	}
}

// 回归：只拦小写 token 时，传 "Token" 就能把探针凭据改成任意值。
// 这里直接读库确认凭据没变，而不是只看工具有没有报错。
func TestClientTokenNotRewritableViaParamSpelling(t *testing.T) {
	t.Setenv(envAllowDangerous, "false")
	cs := connectTestClient(t)

	text, isErr := callToolJSON(t, cs, "add_server", map[string]any{"name": "spelling-test"})
	if isErr {
		t.Fatalf("add_server 失败: %s", text)
	}
	var added struct {
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(text), &added); err != nil || added.UUID == "" || added.Token == "" {
		t.Fatalf("add_server 返回结构不符合预期: %s", text)
	}

	for i, name := range tokenParamVariants {
		// 每次用不同的值：token 列有唯一约束，重复值会被数据库挡下，掩盖真正的结论。
		value := fmt.Sprintf("attacker-chosen-%d", i)
		for _, call := range []struct {
			tool string
			args map[string]any
		}{
			{"edit_server", map[string]any{"uuid": added.UUID, "fields": map[string]any{name: value}}},
			{"komari_call", map[string]any{"method": "admin:editClient", "params": map[string]any{"uuid": added.UUID, name: value}}},
		} {
			if text, isErr := callToolJSON(t, cs, call.tool, call.args); !isErr {
				t.Errorf("%s 不应接受参数名 %q: %s", call.tool, name, text)
			}
			if now, err := clients.GetClientTokenByUUID(added.UUID); err != nil {
				t.Fatalf("读取 token 失败: %v", err)
			} else if now != added.Token {
				t.Fatalf("%s 用参数名 %q 改写了探针 token", call.tool, name)
			}
		}
	}
}

func callToolJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestClientTokenNotExposedByDefault(t *testing.T) {
	t.Setenv(envAllowDangerous, "false")
	cs := connectTestClient(t)

	// add_server 按设计返回 token（接入新探针必需）
	text, isErr := callToolJSON(t, cs, "add_server", map[string]any{"name": "redact-test"})
	if isErr {
		t.Fatalf("add_server 失败: %s", text)
	}
	var added struct {
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(text), &added); err != nil || added.UUID == "" || added.Token == "" {
		t.Fatalf("add_server 返回结构不符合预期: %s", text)
	}

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_servers", nil},
		{"list_servers", map[string]any{"uuid": added.UUID}},
		{"list_servers_admin", nil},
		{"get_server_detail", map[string]any{"uuid": added.UUID}},
		{"komari_call", map[string]any{"method": "admin:listClients"}},
		{"komari_call", map[string]any{"method": "admin:getClient", "params": map[string]any{"uuid": added.UUID}}},
	} {
		text, isErr := callToolJSON(t, cs, tc.tool, tc.args)
		if isErr {
			t.Fatalf("%s 失败: %s", tc.tool, text)
		}
		if !strings.Contains(text, added.UUID) {
			t.Fatalf("%s 结果里找不到新节点: %s", tc.tool, text)
		}
		if strings.Contains(text, added.Token) || strings.Contains(text, `"token"`) {
			t.Errorf("%s 泄露了探针 token: %s", tc.tool, text)
		}
	}

	// 改写 token 必须被拒绝，普通字段照常可改
	if text, isErr := callToolJSON(t, cs, "edit_server", map[string]any{
		"uuid": added.UUID, "fields": map[string]any{"token": "attacker-chosen"},
	}); !isErr {
		t.Errorf("edit_server 不应允许改写 token: %s", text)
	}
	if text, isErr := callToolJSON(t, cs, "komari_call", map[string]any{
		"method": "admin:editClient", "params": map[string]any{"uuid": added.UUID, "token": "attacker-chosen"},
	}); !isErr {
		t.Errorf("komari_call admin:editClient 不应允许改写 token: %s", text)
	}
	if text, isErr := callToolJSON(t, cs, "edit_server", map[string]any{
		"uuid": added.UUID, "fields": map[string]any{"name": "renamed"},
	}); isErr {
		t.Errorf("edit_server 修改普通字段失败: %s", text)
	}
}

func TestClientTokenVisibleWhenDangerousAllowed(t *testing.T) {
	t.Setenv(envAllowDangerous, "true")
	cs := connectTestClient(t)

	text, isErr := callToolJSON(t, cs, "add_server", map[string]any{"name": "redact-open"})
	if isErr {
		t.Fatalf("add_server 失败: %s", text)
	}
	var added struct {
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(text), &added)

	text, isErr = callToolJSON(t, cs, "get_server_detail", map[string]any{"uuid": added.UUID})
	if isErr {
		t.Fatalf("get_server_detail 失败: %s", text)
	}
	if !strings.Contains(text, added.Token) {
		t.Errorf("开启高危档后 get_server_detail 应返回 token: %s", text)
	}
}

func TestSettingsSecretsNotExposedByDefault(t *testing.T) {
	t.Setenv(envAllowDangerous, "false")
	secrets := map[string]any{
		config.ApiKeyKey:           "secret-api-key-value",
		config.AutoDiscoveryKeyKey: "secret-discovery-value",
		metricstore.MetricDBDSNKey: "postgres://u:secret-dsn-password@h/db",
		config.SitenameKey:         "redact-site",
	}
	for k, v := range secrets {
		if err := config.Set(k, v); err != nil {
			t.Fatalf("config.Set(%s): %v", k, err)
		}
	}
	cs := connectTestClient(t)

	text, isErr := callToolJSON(t, cs, "komari_call", map[string]any{"method": "admin:getSettings"})
	if isErr {
		t.Fatalf("admin:getSettings 失败: %s", text)
	}
	if !strings.Contains(text, "redact-site") {
		t.Fatalf("设置快照缺少常规字段: %s", text)
	}
	if strings.Contains(text, "secret-") {
		t.Errorf("admin:getSettings 泄露了凭据: %s", text)
	}
}

// 兜底扫描：逐个调用全部 read 档方法，确认没有任何一个把探针 token 带出来。
//
// 上游新增的查询方法只要返回 models.Client 就可能夹带 token，
// 这个用例能在 sensitiveResultFields 漏登记时直接报出方法名。
func TestNoReadMethodLeaksClientToken(t *testing.T) {
	ctx := context.Background()
	p := Policy{AllowDangerous: false}

	resp := jsonrpc.OnInternalRequest(ctx, rpc.RoleAdmin, "admin:addClient", map[string]any{"name": "leak-scan"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("admin:addClient 失败: %+v", resp)
	}
	b, _ := json.Marshal(resp.Result)
	var added struct {
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &added); err != nil || added.Token == "" {
		t.Fatalf("admin:addClient 返回结构不符合预期: %s", b)
	}

	methods := jsonrpc.OnInternalRequest(ctx, rpc.RoleAdmin, "rpc.methods", map[string]any{})
	names, _ := methods.Result.([]string)
	if len(names) == 0 {
		t.Fatal("rpc.methods 返回为空")
	}

	scanned := 0
	for _, name := range names {
		if ClassifyMethod(name) != TierRead {
			continue // 只扫 read 档：manage 档会改动状态，不能盲调
		}
		for _, params := range []map[string]any{
			{},
			{"uuid": added.UUID},
			{"uuids": []string{added.UUID}},
		} {
			res, err := callRPC(ctx, p, name, params)
			if err != nil {
				continue // 参数不全等报错与本用例无关
			}
			scanned++
			out, _ := json.Marshal(res)
			if strings.Contains(string(out), added.Token) {
				t.Errorf("%s(%v) 返回了探针 token，请在 sensitiveResultFields 中登记", name, params)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("没有任何 read 方法调用成功，用例前提不成立")
	}
}
