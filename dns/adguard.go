package dns

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

	"github.com/neon9809/cf-dns-select/config"
)

// APIError 类型化的 AdGuard API 错误，携带 HTTP 状态码。
// 用于重试逻辑精确区分 4xx（不重试）与网络错误/5xx（重试）。
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

// IsClientError 判断 err 是否为 4xx 客户端错误（凭据错误、参数错误等，重试无意义）。
func IsClientError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// adguardClient AdGuard Home 的 Provider 实现（在线管理型，HTTP API CRUD）。
type adguardClient struct {
	url      string
	username string
	password string
	http     *http.Client
}

// NewAdGuard 创建 AdGuard Home provider。
func NewAdGuard(inst config.AdguardInstance) Provider {
	return &adguardClient{
		url:      strings.TrimRight(inst.URL, "/"),
		username: inst.Username,
		password: inst.Password,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *adguardClient) Name() string { return "adguard" }

// do 发送一个带 Basic Auth 的请求到 AdGuard Home 控制接口。
func (c *adguardClient) do(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.url+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
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
func (c *adguardClient) doWithRetry(method, path string, body any, attempts int) ([]byte, error) {
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
		if IsClientError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("重试 %d 次后仍失败: %w", attempts, lastErr)
}

// agRewrite 对应 AdGuard Home 的一条 DNS 重写规则。
type agRewrite struct {
	Domain  string `json:"domain"`
	Answer  string `json:"answer"`
	Enabled bool   `json:"enabled"`
}

func (c *adguardClient) TestConnection() error {
	_, err := c.list()
	return err
}

func (c *adguardClient) list() ([]agRewrite, error) {
	data, err := c.doWithRetry(http.MethodGet, "/control/rewrite/list", nil, 3)
	if err != nil {
		return nil, err
	}
	var list []agRewrite
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (c *adguardClient) ListRewrites() ([]RewriteEntry, error) {
	list, err := c.list()
	if err != nil {
		return nil, err
	}
	out := make([]RewriteEntry, 0, len(list))
	for _, e := range list {
		out = append(out, RewriteEntry{Domain: e.Domain, Answer: e.Answer, Enabled: e.Enabled})
	}
	return out, nil
}

func (c *adguardClient) addRewrite(domain, answer string) error {
	if _, err := c.doWithRetry(http.MethodPost, "/control/rewrite/add", agRewrite{Domain: domain, Answer: answer, Enabled: true}, 3); err != nil {
		return fmt.Errorf("添加重写规则失败 (%s -> %s): %w", domain, answer, err)
	}
	return nil
}

func (c *adguardClient) removeRewrite(e agRewrite) error {
	if _, err := c.doWithRetry(http.MethodPost, "/control/rewrite/delete", e, 3); err != nil {
		return fmt.Errorf("删除重写规则失败 (%s -> %s): %w", e.Domain, e.Answer, err)
	}
	return nil
}

// ReplaceRewrites 把实例的重写表完全重置为期望集合 want：
// 先清空全部现有重写记录，再逐条写入期望集合。
// 注意：清空的是整张重写表，实例上非本工具管理的重写规则也会被删除。
func (c *adguardClient) ReplaceRewrites(want map[string]map[string]bool) (SyncStats, error) {
	var stats SyncStats

	list, err := c.list()
	if err != nil {
		return stats, fmt.Errorf("获取重写列表失败: %w", err)
	}

	// 1. 清空现有重写表
	for _, ent := range list {
		if err := c.removeRewrite(ent); err != nil {
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
