package config

import (
	"testing"
)

func TestApplyUpdateFieldsPasswordPreservation(t *testing.T) {
	c := &Config{
		AdguardInstances: []AdguardInstance{
			{Name: "主", URL: "http://a:3000", Username: "admin", Password: "secret1"},
			{Name: "备", URL: "http://b:3000", Username: "admin", Password: "secret2"},
		},
		Port: 443,
	}

	in := Config{
		AdguardInstances: []AdguardInstance{
			{Name: "主", URL: "http://a:3000", Username: "admin", Password: "********"},
			{Name: "备", URL: "http://b:3000", Username: "admin", Password: ""},
			{Name: "新", URL: "http://c:3000", Username: "admin", Password: "newpass"},
		},
	}
	c.ApplyUpdateFields(in, map[string]bool{"adguard_instances": true})

	if c.AdguardInstances[0].Password != "secret1" {
		t.Errorf("掩码密码应保留旧值, got %q", c.AdguardInstances[0].Password)
	}
	if c.AdguardInstances[1].Password != "secret2" {
		t.Errorf("空密码应保留旧值, got %q", c.AdguardInstances[1].Password)
	}
	if c.AdguardInstances[2].Password != "newpass" {
		t.Errorf("新密码应被采用, got %q", c.AdguardInstances[2].Password)
	}
}

// TestApplyUpdateFieldsNilInstancesPreserved 回归测试（R-1）：
// Dashboard 保存常规配置时提交的 body 不含 adguard_instances（解码后为 nil），
// 此时必须保留旧实例列表，而不是把全部实例及凭据抹掉。
func TestApplyUpdateFieldsNilInstancesPreserved(t *testing.T) {
	c := &Config{
		AdguardInstances: []AdguardInstance{
			{Name: "主", URL: "http://a:3000", Username: "admin", Password: "secret1"},
			{Name: "备", URL: "http://b:3000", Username: "admin", Password: "secret2"},
		},
		Port:      443,
		TestCount: 10,
	}

	// 模拟前端保存测速参数的 body：不含实例字段
	in := Config{Port: 443, IntervalMinutes: 30}
	c.ApplyUpdateFields(in, map[string]bool{"port": true, "interval_minutes": true})

	if len(c.AdguardInstances) != 2 {
		t.Fatalf("nil 实例列表应保留旧值（期望 2 个实例），实际 %d 个", len(c.AdguardInstances))
	}
	if c.AdguardInstances[0].Password != "secret1" || c.AdguardInstances[1].Password != "secret2" {
		t.Errorf("保留的实例密码不应丢失: %+v", c.AdguardInstances)
	}
}

// TestApplyUpdateFieldsExplicitEmptyInstances 显式提交空数组表示清空全部实例。
func TestApplyUpdateFieldsExplicitEmptyInstances(t *testing.T) {
	c := &Config{
		AdguardInstances: []AdguardInstance{{Name: "主", URL: "http://a:3000", Password: "p"}},
	}
	in := Config{AdguardInstances: []AdguardInstance{}}
	c.ApplyUpdateFields(in, map[string]bool{"adguard_instances": true})
	if len(c.AdguardInstances) != 0 {
		t.Errorf("显式空数组应清空实例列表，实际 %d 个", len(c.AdguardInstances))
	}
}

// TestApplyUpdateFieldsPartial 回归：字段级合并时，只提交某字段不得重置其他
// 开关/参数（如保存测速参数时误关 IPv6 开关、重置端口）。
func TestApplyUpdateFieldsPartial(t *testing.T) {
	c := &Config{
		Port:      443,
		Httping:   true,
		TestIPv6:  true,
		CFColo:    "HKG",
		IPFile:    "ip.txt",
		TestCount: 10,
	}

	// 模拟「只保存节点区域」：只提交 cfcolo
	in := Config{CFColo: "HKG,NRT"}
	present := map[string]bool{"cfcolo": true}
	c.ApplyUpdateFields(in, present)

	if !c.TestIPv6 {
		t.Error("未提交 test_ipv6 时应保留旧值 true，实际被重置为 false")
	}
	if !c.Httping {
		t.Error("未提交 httping 时应保留旧值 true")
	}
	if c.Port != 443 {
		t.Errorf("未提交 port 时应保留旧值 443，实际 %d", c.Port)
	}
	if c.CFColo != "HKG,NRT" {
		t.Errorf("已提交 cfcolo 应被更新，实际 %q", c.CFColo)
	}

	// 显式提交 test_ipv6=false 时才应更新为 false
	c.ApplyUpdateFields(Config{TestIPv6: false}, map[string]bool{"test_ipv6": true})
	if c.TestIPv6 {
		t.Error("显式提交 test_ipv6=false 应被应用")
	}
}

func TestSanitized(t *testing.T) {
	c := &Config{
		AdguardInstances: []AdguardInstance{{Name: "主", URL: "http://a:3000", Password: "secret"}},
	}
	s := c.Sanitized()
	if s.AdguardInstances[0].Password != "********" {
		t.Errorf("脱敏失败: %q", s.AdguardInstances[0].Password)
	}
	if c.AdguardInstances[0].Password != "secret" {
		t.Errorf("原配置不应被修改")
	}
}

func TestCloneDeepCopy(t *testing.T) {
	c := &Config{
		CustomDomains:    []string{"a.com"},
		AdguardInstances: []AdguardInstance{{Name: "x"}},
	}
	cp := c.Clone()
	cp.CustomDomains[0] = "changed"
	cp.AdguardInstances[0].Name = "changed"
	if c.CustomDomains[0] != "a.com" || c.AdguardInstances[0].Name != "x" {
		t.Errorf("Clone 不是深拷贝")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DSA_CONFIG", dir+"/config.json")

	c := Default()
	c.AdguardInstances = []AdguardInstance{{Name: "主", URL: "http://192.168.1.2:3000", Password: "p"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.AdguardInstances) != 1 || loaded.AdguardInstances[0].URL != "http://192.168.1.2:3000" {
		t.Errorf("round trip 失败: %+v", loaded.AdguardInstances)
	}
}
