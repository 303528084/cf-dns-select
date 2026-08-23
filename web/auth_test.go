package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neon9809/cf-dns-select/adguard"
)

func TestRandomPasswordNoBias(t *testing.T) {
	seen := make(map[byte]int)
	for i := 0; i < 200; i++ {
		p, err := RandomPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 20 {
			t.Fatalf("密码长度应为 20，实际 %d", len(p))
		}
		for j := range p {
			seen[p[j]]++
		}
	}
	if len(seen) < 40 { // charset 有 56 个字符，200 次采样应覆盖大部分
		t.Errorf("字符分布过窄，仅出现 %d 种字符", len(seen))
	}
}

func TestSessionStoreMultiSession(t *testing.T) {
	s := newSessionStore()
	tok1, err := s.newSession()
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := s.newSession()
	if err != nil {
		t.Fatal(err)
	}
	if tok1 == tok2 {
		t.Fatal("两个会话的 token 不应相同")
	}
	// 多会话并存：登录第二个不应使第一个失效（P1-1 回归）
	if !s.valid(tok1) || !s.valid(tok2) {
		t.Error("多会话应同时有效")
	}
	// 登出只吊销自己的 token
	if !s.revoke(tok1) {
		t.Error("revoke 应返回 true")
	}
	if s.valid(tok1) {
		t.Error("已吊销的 token 应失效")
	}
	if !s.valid(tok2) {
		t.Error("其他会话不应受登出影响")
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	s := newSessionStore()
	tok, err := s.newSession()
	if err != nil {
		t.Fatal(err)
	}
	// 手动把过期时间改到过去
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if s.valid(tok) {
		t.Error("过期 token 应无效")
	}
}

func TestLoginLimiterLockout(t *testing.T) {
	l := newLoginLimiter()
	ip := "192.168.1.50"
	for i := 0; i < loginLockoutThreshold; i++ {
		if !l.allowed(ip) {
			t.Fatalf("第 %d 次尝试前不应被锁定", i+1)
		}
		l.recordFailure(ip)
	}
	if l.allowed(ip) {
		t.Error("达到失败阈值后应被锁定")
	}
	// 其他 IP 不受影响
	if !l.allowed("10.0.0.1") {
		t.Error("其他 IP 不应被连带锁定")
	}
	// 成功登录后重置
	l.reset(ip)
	if !l.allowed(ip) {
		t.Error("reset 后应允许再次尝试")
	}
}

func TestExtractTokenNoQuery(t *testing.T) {
	// P2-8 回归：不再接受 URL query 传 token
	r := httptest.NewRequest("GET", "/api/status?token=leaked", nil)
	if got := extractToken(r); got != "" {
		t.Errorf("query token 应被忽略，实际得到 %q", got)
	}
	r.Header.Set("Authorization", "Bearer abc")
	if got := extractToken(r); got != "abc" {
		t.Errorf("Bearer token 提取失败: %q", got)
	}
}

func TestParseCIDRsSingleIP(t *testing.T) {
	cl, err := ParseCIDRs("192.168.1.5,10.0.0.0/24,::1")
	if err != nil {
		t.Fatal(err)
	}
	if !cl.contains("192.168.1.5:12345") || !cl.contains("10.0.0.99:80") || !cl.contains("[::1]:443") {
		t.Error("白名单包含判断失败")
	}
	if cl.contains("192.168.1.6:1") {
		t.Error("不在白名单内的 IP 不应通过")
	}
}

func TestAPIErrorClassification(t *testing.T) {
	e401 := &adguard.APIError{Method: "GET", Path: "/x", StatusCode: 401, Body: "unauthorized"}
	e500 := &adguard.APIError{Method: "GET", Path: "/x", StatusCode: 500, Body: "oops"}
	netErr := error(&mockNetErr{})
	if !adguard.IsClientError(e401) {
		t.Error("401 应判定为客户端错误")
	}
	if adguard.IsClientError(e500) {
		t.Error("500 不应判定为客户端错误（应重试）")
	}
	if adguard.IsClientError(netErr) {
		t.Error("网络错误不应判定为客户端错误（应重试）")
	}
	if !strings.Contains(e401.Error(), "HTTP 401") {
		t.Errorf("错误信息格式异常: %q", e401.Error())
	}
}

type mockNetErr struct{}

func (*mockNetErr) Error() string { return "connection refused" }
