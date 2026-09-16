package mcp

import "testing"

func TestClassifyMethod(t *testing.T) {
	cases := []struct {
		method string
		want   Tier
	}{
		// 只读：前端公开接口
		{"public:getNodesInformation", TierRead},
		{"common:getNodes", TierRead},
		{"common:getNodesLatestStatus", TierRead},
		{"public:getRecordsByUUID", TierRead},

		// 只读：admin 查询类（按动词前缀归档）
		{"admin:listClients", TierRead},
		{"admin:getClient", TierRead},
		{"admin:getSettings", TierRead},
		{"admin:getTasks", TierRead},

		// 只读：内省
		{"rpc.methods", TierRead},
		{"rpc.help", TierRead},

		// 管理：可逆的业务变更
		{"admin:addClient", TierManage},
		{"admin:editClient", TierManage},
		{"admin:addPingTask", TierManage},
		{"admin:addLoadNotification", TierManage},
		{"admin:sendNotification", TierManage},

		// 高危：远程执行与裸 SQL
		{"admin:exec", TierDangerous},
		{"admin:dbExec", TierDangerous},
		{"admin:dbQuery", TierDangerous},

		// 高危：文件写
		{"admin:fileDelete", TierDangerous},
		{"admin:fileChmod", TierDangerous},

		// 高危：凭据与安全设置
		{"admin:getClientToken", TierDangerous},
		{"admin:editSettings", TierDangerous},

		// 高危：不可逆删除
		{"admin:removeClient", TierDangerous},
		{"admin:clearAllRecords", TierDangerous},
	}

	for _, c := range cases {
		if got := ClassifyMethod(c.method); got != c.want {
			t.Errorf("ClassifyMethod(%q) = %v, want %v", c.method, got, c.want)
		}
	}
}

// 文件类的只读操作不应被误判为高危，否则 agent 连目录都列不了。
func TestFileReadOpsAreNotDangerous(t *testing.T) {
	for _, m := range []string{
		"admin:fileList",
		"admin:fileStat",
		"admin:fileSearch",
		"admin:fileListRoots",
	} {
		if got := ClassifyMethod(m); got == TierDangerous {
			t.Errorf("ClassifyMethod(%q) = dangerous，只读文件操作不应进高危档", m)
		}
	}
}

// 未知方法必须落到 manage 而非 read：
// 上游新增方法时，宁可让 agent 把它当有副作用的操作对待，也不要默认当安全只读接口。
func TestUnknownAdminMethodDefaultsToManage(t *testing.T) {
	if got := ClassifyMethod("admin:somethingNobodyHasWrittenYet"); got != TierManage {
		t.Errorf("未知 admin 方法 = %v, want manage", got)
	}
}

func TestPolicyGating(t *testing.T) {
	locked := Policy{AllowDangerous: false}
	open := Policy{AllowDangerous: true}

	if locked.Allows("admin:exec") {
		t.Error("默认策略不应放行 admin:exec")
	}
	if !open.Allows("admin:exec") {
		t.Error("AllowDangerous=true 时应放行 admin:exec")
	}

	// 非高危方法两种策略下都必须放行
	for _, m := range []string{"common:getNodes", "admin:listClients", "admin:addClient"} {
		if !locked.Allows(m) {
			t.Errorf("默认策略误拦了 %s", m)
		}
		if !open.Allows(m) {
			t.Errorf("开放策略误拦了 %s", m)
		}
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		in   string
		def  bool
		want bool
	}{
		{"", true, true},   // 未设置 -> 取默认
		{"", false, false}, // 未设置 -> 取默认
		{"true", false, true},
		{"1", false, true},
		{"on", false, true},
		{"YES", false, true}, // 大小写不敏感
		{"false", true, false},
		{"0", true, false},
		{"off", true, false},
		{"随便写的", true, true}, // 无法识别 -> 退回默认，不擅自开启
	}
	for _, c := range cases {
		t.Setenv("KOMARI_MCP_TEST_FLAG", c.in)
		if got := envBool("KOMARI_MCP_TEST_FLAG", c.def); got != c.want {
			t.Errorf("envBool(%q, def=%v) = %v, want %v", c.in, c.def, got, c.want)
		}
	}
}
