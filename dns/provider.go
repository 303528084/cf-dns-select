// Package dns 定义 DNS 实例的统一 Provider 抽象。
//
// 不同 DNS 产品的记录管理能力差异极大（调研结论）：
//   - AdGuard Home / Technitium：在线管理型，HTTP API 完整 CRUD；
//   - dnsmasq / SmartDNS / MosDNS：文件型，无运行时记录 CRUD，
//     通过写入受管规则文件 + reload/重启（或自动热读入）生效。
//
// 上层（web.Server）只面向本接口编程，不感知具体产品差异。
package dns

import "github.com/neon9809/cf-dns-select/config"

// RewriteEntry 一条重写记录（域名 → 解析目标）。
type RewriteEntry struct {
	Domain  string `json:"domain"`
	Answer  string `json:"answer"`
	Enabled bool   `json:"enabled"`
}

// SyncStats 记录一次 ReplaceRewrites 的操作统计。
type SyncStats struct {
	Written int // 新增或更新的条目数
	Removed int // 清理的条目数
}

// Provider 是一个 DNS 实例的统一操作接口。
// want 的语义为「期望的完整重写集合」：domain → 允许的 answer 集合
// （空集合表示该域名不应存在任何记录）。
type Provider interface {
	// Name 返回 provider 类型标识（adguard / technitium / dnsmasq / smartdns / mosdns）。
	Name() string
	// TestConnection 测试实例可达性/可用性。
	TestConnection() error
	// ListRewrites 列出当前生效的重写记录（文件型为受管文件内容）。
	ListRewrites() ([]RewriteEntry, error)
	// ReplaceRewrites 把重写状态收敛到期望集合（API 型为差集收敛或清空重发，
	// 文件型为重写受管文件并触发加载）。
	ReplaceRewrites(want map[string]map[string]bool) (SyncStats, error)
}

// New 按 provider 类型构造对应客户端。inst.Type 决定具体实现。
func New(inst config.AdguardInstance) (Provider, error) {
	switch inst.Type {
	case "", "adguard":
		return NewAdGuard(inst), nil
	case "technitium":
		return NewTechnitium(inst), nil
	case "dnsmasq":
		return NewFileBased(inst, "dnsmasq", fileFormatHosts), nil
	case "smartdns":
		return NewFileBased(inst, "smartdns", fileFormatSmartDNS), nil
	case "mosdns":
		return NewFileBased(inst, "mosdns", fileFormatMosDNSHosts), nil
	default:
		return nil, ErrUnknownProvider
	}
}
