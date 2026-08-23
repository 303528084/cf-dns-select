package dns

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/neon9809/cf-dns-select/config"
)

// technitiumClient Technitium DNS Server 的 Provider 实现。
// 在线管理型：HTTP API 完整 CRUD，变更动态生效无需重启。
// 实现方式：在主 zone（默认取实例 URL 的 host 作为 zone 名，或用户以
// URL query/配置指定）下维护 A 记录；域名 → 记录名映射为
// 「把 domain 的点替换为连字符」的扁平名，避免子域授权问题。
type technitiumClient struct {
	url   string
	token string
	zone  string // 承载记录的主 zone（如 "lan"）
	http  *http.Client
}

// NewTechnitium 创建 Technitium DNS provider。
// inst.URL 形如 http://192.168.1.2:5380（可选 ?zone=xxx 指定承载 zone）；
// inst.Password 存放 API Token。
func NewTechnitium(inst config.AdguardInstance) Provider {
	u := strings.TrimRight(inst.URL, "/")
	zone := ""
	if i := strings.Index(u, "?zone="); i >= 0 {
		zone = strings.TrimSpace(u[i+len("?zone="):])
		u = u[:i]
	}
	if zone == "" {
		// 默认用主机名的第一段作为 zone（如 http://dns.lan:5380 → lan）
		host := u
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if i := strings.Index(host, ":"); i >= 0 {
			host = host[:i]
		}
		parts := strings.Split(host, ".")
		if len(parts) >= 2 {
			zone = strings.Join(parts[len(parts)-2:], ".")
		} else {
			zone = "lan"
		}
	}
	return &technitiumClient{
		url:   u,
		token: inst.Password,
		zone:  zone,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *technitiumClient) Name() string { return "technitium" }

// recordName 把重写域名转换为扁平记录名（dots → dashes），避免创建子 zone。
func recordName(domain string) string {
	return strings.ReplaceAll(strings.TrimSuffix(domain, "."), ".", "-")
}

// api 调用 Technitium HTTP API（全部为 GET + query 参数）。
func (c *technitiumClient) api(path string, params map[string]string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, c.url+path, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("token", c.token)
	for k, v := range params {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("technitium 响应解析失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	if status, _ := out["status"].(string); status != "ok" && status != "" {
		return out, &APIError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: fmt.Sprint(out["errorMessage"])}
	}
	return out, nil
}

func (c *technitiumClient) TestConnection() error {
	_, err := c.api("/api/zones/list", nil)
	return err
}

// tRecord Technitium zone 内的一条 A 记录。
type tRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"A"` // A 记录的 IP 在 "A" 字段
}

func (c *technitiumClient) ListRewrites() ([]RewriteEntry, error) {
	out, err := c.api("/api/zones/records/get", map[string]string{
		"zone":     c.zone,
		"listZone": "true",
	})
	if err != nil {
		return nil, err
	}
	var records []tRecord
	if raw, ok := out["response"]; ok {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &records)
		}
	}
	entries := make([]RewriteEntry, 0, len(records))
	for _, r := range records {
		if r.Type != "A" || r.Value == "" {
			continue
		}
		entries = append(entries, RewriteEntry{Domain: r.Name, Answer: r.Value, Enabled: true})
	}
	return entries, nil
}

func (c *technitiumClient) addRecord(name, ip string) error {
	_, err := c.api("/api/zones/records/add", map[string]string{
		"zone": c.zone, "name": name, "type": "A", "ipAddress": ip, "ttl": "60",
	})
	return err
}

func (c *technitiumClient) updateRecord(name, oldIP, newIP string) error {
	_, err := c.api("/api/zones/records/update", map[string]string{
		"zone": c.zone, "name": name, "originalType": "A", "originalIPAddress": oldIP,
		"type": "A", "ipAddress": newIP, "ttl": "60",
	})
	return err
}

func (c *technitiumClient) deleteRecord(name, ip string) error {
	_, err := c.api("/api/zones/records/delete", map[string]string{
		"zone": c.zone, "domain": name, "type": "A", "ipAddress": ip,
	})
	return err
}

// ReplaceRewrites 差集收敛到期望集合。want 中每个 domain 对应一条 A 记录
// （多 answer 时取排序后的第一个——Technitium 单记录单值）。
func (c *technitiumClient) ReplaceRewrites(want map[string]map[string]bool) (SyncStats, error) {
	var stats SyncStats

	existing, err := c.ListRewrites()
	if err != nil {
		return stats, fmt.Errorf("获取现有记录失败: %w", err)
	}
	byName := make(map[string]string, len(existing))
	for _, e := range existing {
		byName[e.Domain] = e.Answer
	}

	// 期望集合：每域名一个答案（排序后第一个）
	desired := make(map[string]string, len(want))
	for d, answers := range want {
		if len(answers) == 0 {
			continue // 空集表示该域名不应有记录
		}
		list := make([]string, 0, len(answers))
		for a := range answers {
			list = append(list, a)
		}
		sort.Strings(list)
		desired[d] = list[0]
	}

	// 删除/更新不在期望中的现有记录
	for name, ip := range byName {
		newIP, ok := desired[name]
		switch {
		case !ok:
			if err := c.deleteRecord(name, ip); err != nil {
				return stats, fmt.Errorf("删除记录失败 (%s): %w", name, err)
			}
			stats.Removed++
		case newIP != ip:
			if err := c.updateRecord(name, ip, newIP); err != nil {
				return stats, fmt.Errorf("更新记录失败 (%s): %w", name, err)
			}
			stats.Written++
		}
	}
	// 新增缺失的记录
	for name, ip := range desired {
		if _, ok := byName[name]; !ok {
			if err := c.addRecord(name, ip); err != nil {
				return stats, fmt.Errorf("新增记录失败 (%s -> %s): %w", name, ip, err)
			}
			stats.Written++
		}
	}
	return stats, nil
}
