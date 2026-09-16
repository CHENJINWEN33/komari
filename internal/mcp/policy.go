package mcp

// policy.go
// MCP 暴露策略：决定 agent 可以通过 MCP 调用哪些 Komari RPC 方法。
//
// 分三档风险，默认只开放 read + manage。dangerous 档必须由运维显式打开
// （环境变量 KOMARI_MCP_ALLOW_DANGEROUS=true），因为这一档包含在被监控主机上
// 执行任意命令、跑裸 SQL、改动安全设置等能造成不可逆后果的方法。

import (
	"os"
	"strings"
)

// Tier 是单个 RPC 方法的风险档位。
type Tier int

const (
	// TierRead 只读：查询节点、指标、历史记录、配置快照，不改变任何状态。
	TierRead Tier = iota
	// TierManage 管理：增删改探针与告警等业务配置，影响可控且可人工回滚。
	TierManage
	// TierDangerous 高危：远程命令执行、裸 SQL、文件写、凭据读取、安全设置变更。
	TierDangerous
)

func (t Tier) String() string {
	switch t {
	case TierRead:
		return "read"
	case TierManage:
		return "manage"
	case TierDangerous:
		return "dangerous"
	}
	return "unknown"
}

// dangerousMethods 显式高危名单。
//
// 收录标准是「一次调用即可造成不可逆后果，或可直接扩大调用者自身权限」：
//   - 远程代码执行与文件写：exec、file* 的写操作
//   - 裸 SQL：dbExec 可写；dbQuery 虽只读，但能绕过业务层读出 token 等敏感列
//   - 凭据泄露：getClientToken 返回探针接入凭据
//   - 权限自提升：editSettings 可改 API Key / 关闭 CORS 校验等安全开关
//   - 不可逆删除：removeClient、clearRecords、deletePlugin、vacuumDatabase 等
var dangerousMethods = map[string]bool{
	// 远程执行
	"admin:exec": true,

	// 裸 SQL
	"admin:dbExec":   true,
	"admin:dbQuery":  true,
	"admin:dbTables": true,

	// 文件写操作（fileList / fileStat / fileSearch / fileListRoots 是只读，不在此列）
	"admin:fileDelete": true,
	"admin:fileMove":   true,
	"admin:fileCopy":   true,
	"admin:fileChmod":  true,
	"admin:fileChown":  true,
	"admin:fileMkdir":  true,

	// 凭据读取
	"admin:getClientToken": true,

	// 安全设置变更（可关闭 CORS 校验、轮换 API Key、改动 OIDC / 终端配置）
	"admin:editSettings":             true,
	"admin:setOidcProvider":          true,
	"admin:setMessageSenderProvider": true,
	"admin:setXtermjsSettings":       true,

	// 插件：可加载任意代码
	"admin:setPluginEnabled":       true,
	"admin:setPluginConfiguration": true,
	"admin:deletePlugin":           true,

	// 不可逆删除 / 数据破坏
	"admin:removeClient":      true,
	"admin:clearRecords":      true,
	"admin:clearAllRecords":   true,
	"admin:vacuumDatabase":    true,
	"admin:deleteSession":     true,
	"admin:deleteAllSessions": true,

	// 数据迁移：长时间占用数据库，中断可能留下半迁移状态
	"admin:startMetricMigration":  true,
	"admin:cancelMetricMigration": true,
}

// readOnlyAdminPrefixes 用于把 admin: 命名空间里的查询类方法归入 read 档。
// 命名约定来自上游：查询方法一律以这些动词开头。
var readOnlyAdminPrefixes = []string{"get", "list", "test"}

// ClassifyMethod 返回方法的风险档位。
//
// 判定顺序（先命中先返回）：
//  1. 显式高危名单
//  2. public: / common: 命名空间 —— 面向前端的读接口，归 read
//  3. admin: 且方法名以查询动词开头 —— 归 read
//  4. 其余 —— 归 manage
//
// 未知方法（上游新增而本策略未覆盖）会落到 manage 而非 read，保证新增方法
// 不会因为疏漏而被当成安全的只读接口暴露出去。
func ClassifyMethod(method string) Tier {
	if dangerousMethods[method] {
		return TierDangerous
	}

	ns, name, ok := strings.Cut(method, ":")
	if !ok {
		// rpc.methods / rpc.help / rpc.ping 等内省方法，只读。
		if strings.HasPrefix(method, "rpc.") {
			return TierRead
		}
		return TierManage
	}

	switch ns {
	case "public", "common":
		return TierRead
	case "admin":
		for _, p := range readOnlyAdminPrefixes {
			if strings.HasPrefix(name, p) {
				return TierRead
			}
		}
	}
	return TierManage
}

// Policy 是一次 MCP 服务实例的暴露策略快照。
type Policy struct {
	// AllowDangerous 为 true 时才放行 TierDangerous 的方法。
	AllowDangerous bool
}

// LoadPolicy 从环境变量读取策略。
//
//	KOMARI_MCP_ALLOW_DANGEROUS=true  开启高危档（默认关闭）
func LoadPolicy() Policy {
	return Policy{
		AllowDangerous: envBool("KOMARI_MCP_ALLOW_DANGEROUS", false),
	}
}

// Allows 判断该方法在当前策略下是否可被 MCP 调用。
func (p Policy) Allows(method string) bool {
	if ClassifyMethod(method) == TierDangerous {
		return p.AllowDangerous
	}
	return true
}

// Enabled 返回 MCP 端点是否启用。
//
//	KOMARI_MCP_ENABLED=false 可整体关闭（默认启用）
//
// 端点本身受管理员鉴权保护，与 /api/admin/* 同级，所以默认启用；
// 真正需要谨慎的是高危档，那一档单独开关且默认关闭。
func Enabled() bool {
	return envBool("KOMARI_MCP_ENABLED", true)
}

// TrustProxyHost 返回是否信任反向代理转发过来的 Host 头。
//
//	KOMARI_MCP_TRUST_PROXY_HOST=true  关闭 SDK 的 DNS rebinding 防护（默认关闭此开关，即保持防护）
//
// 仅在 Komari 部署于反向代理之后、且 MCP 端点被 403 拦截时才需要打开。
func TrustProxyHost() bool {
	return envBool("KOMARI_MCP_TRUST_PROXY_HOST", false)
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
