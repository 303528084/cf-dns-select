package adguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Client 用于通过 HTTP API 向一个 AdGuard Home 实例写入 DNS 重写规则。
type Client struct {
	URL      string // 例如 http://192.168.1.2:3000
	Username string
	Password string

	http *http.Client
}

// New 创建一个 AdGuard Home 客户端。
func New(url, username, password string) *Client {
	return &Client{
		URL:      strings.TrimRight(url, "/"),
		Username: username,
		Password: password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// RewriteEntry 对应 AdGuard Home 的一条 DNS 重写规则。
type RewriteEntry struct {
	Domain  string `json:"domain"`
	Answer  string `json:"answer"`
	Enabled bool   `json:"enabled"`
}

// APIError 类型化的 AdGuard API 错误，携带 HTTP 状态码。
// 用于重试逻辑精确区分 4xx（不重试）与网络错误/5xx（重试）。
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("adguard api %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

// IsClientError 判断 err 是否为 4xx 客户端错误（凭据错误、参数错误等，重试无意义）。
func IsClientError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// do 发送一个带 Basic Auth 的请求到 AdGuard Home 控制接口。
func (c *Client) do(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.URL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Username != "" || c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

// doWithRetry 带重试的请求：最多 attempts 次，指数退避 + 抖动。
// 仅对网络错误与 5xx 重试；4xx（如凭据错误）不重试。
func (c *Client) doWithRetry(method, path string, body any, attempts int) ([]byte, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * time.Second
			backoff += time.Duration(rand.Int63n(int64(500 * time.Millisecond))) // 抖动
			time.Sleep(backoff)
		}
		data, err := c.do(method, path, body)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// 4xx 客户端错误不重试（凭据错误、参数错误重试无意义）
		if IsClientError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("重试 %d 次后仍失败: %w", attempts, lastErr)
}

// ListRewrites 获取当前所有 DNS 重写规则。
func (c *Client) ListRewrites() ([]RewriteEntry, error) {
	data, err := c.doWithRetry(http.MethodGet, "/control/rewrite/list", nil, 3)
	if err != nil {
		return nil, err
	}
	var list []RewriteEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SyncStats 记录一次 SyncRewrites 的操作统计。
type SyncStats struct {
	Written int // 新增或更新的条目数
	Removed int // 清理的过期条目数
}

// SyncRewrites 把实例的重写表完全重置为期望集合 want（域名 → 允许的答案 IP 集合）：
// 先清空全部现有重写记录，再逐条写入期望集合。
// 注意：清空的是整张重写表，实例上非本工具管理的重写规则也会被删除。
func (c *Client) SyncRewrites(want map[string]map[string]bool) (SyncStats, error) {
	var stats SyncStats

	list, err := c.ListRewrites()
	if err != nil {
		return stats, fmt.Errorf("获取重写列表失败: %w", err)
	}

	// 1. 清空现有重写表
	for _, ent := range list {
		if err := c.RemoveRewriteEntry(ent); err != nil {
			return stats, fmt.Errorf("清空重写规则失败 (%s -> %s): %w", ent.Domain, ent.Answer, err)
		}
		stats.Removed++
	}

	// 2. 全量写入期望集合（排序保证顺序确定）
	domains := make([]string, 0, len(want))
	for d := range want {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	for _, d := range domains {
		answers := make([]string, 0, len(want[d]))
		for a := range want[d] {
			answers = append(answers, a)
		}
		sort.Strings(answers)
		for _, a := range answers {
			if err := c.addRewrite(d, a); err != nil {
				return stats, err
			}
			stats.Written++
		}
	}
	return stats, nil
}

// addRewrite 添加一条重写规则。
func (c *Client) addRewrite(domain, answer string) error {
	if _, err := c.doWithRetry(http.MethodPost, "/control/rewrite/add", RewriteEntry{Domain: domain, Answer: answer, Enabled: true}, 3); err != nil {
		return fmt.Errorf("添加重写规则失败 (%s -> %s): %w", domain, answer, err)
	}
	return nil
}

// RemoveRewriteEntry 删除一条指定的重写规则。
func (c *Client) RemoveRewriteEntry(ent RewriteEntry) error {
	if _, err := c.doWithRetry(http.MethodPost, "/control/rewrite/delete", ent, 3); err != nil {
		return fmt.Errorf("删除重写规则失败 (%s -> %s): %w", ent.Domain, ent.Answer, err)
	}
	return nil
}

// RemoveRewrite 删除指定域名的全部重写规则（忽略不存在的情况）。
func (c *Client) RemoveRewrite(domain string) error {
	list, err := c.ListRewrites()
	if err != nil {
		return err
	}
	for _, ent := range list {
		if ent.Domain == domain {
			if err := c.RemoveRewriteEntry(ent); err != nil {
				return err
			}
		}
	}
	return nil
}
