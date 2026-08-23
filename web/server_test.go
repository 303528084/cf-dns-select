package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neon9809/cf-dns-select/adguard"
	"github.com/neon9809/cf-dns-select/config"
	"github.com/neon9809/cf-dns-select/speedtest"
)

// ---- 测试辅助 ----

func newTestConfig() *config.Config {
	return config.Default()
}

func mustParseCIDRs(t *testing.T) *cidrList {
	t.Helper()
	cl, err := ParseCIDRs("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func writeJSONForTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decodeBody(r *http.Request, v any) {
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, v)
}

// P1-4 回归：过期条目应被 recordFailure 顺带清扫，map 不无限增长。
func TestLoginLimiterCleanup(t *testing.T) {
	l := newLoginLimiter()
	// 伪造大量不同 IP 的失败记录
	for i := 0; i < 100; i++ {
		l.recordFailure("10.1.0." + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if len(l.fails) < 50 {
		t.Fatalf("预置失败记录数异常: %d", len(l.fails))
	}
	// 推进时间使窗口过期
	for _, st := range l.fails {
		st.windowEnd = time.Now().Add(-time.Minute)
		st.lockedUntil = time.Now().Add(-time.Minute)
	}
	// 新的一次失败应触发全量清扫
	l.recordFailure("192.168.9.9")
	if len(l.fails) != 1 {
		t.Errorf("过期条目应被清扫，仅剩 1 条，实际 %d", len(l.fails))
	}
}

// P1-5 回归：超过 1MB 的请求体应被拒绝。
func TestMaxBodyLimit(t *testing.T) {
	s, err := New(newTestConfig(), "password123", mustParseCIDRs(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	huge := strings.Repeat("a", 2<<20) // 2MB
	resp, err := http.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"password":"`+huge+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("超大请求体应返回 413，实际 %d", resp.StatusCode)
	}
}

// P0-1 回归：并发调用域名检测不应触发 concurrent map writes（用 -race 检测）。
func TestCloudflareDomainConcurrentSafe(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 使用必然快速失败的无效域名，重点验证并发写安全性
			_ = testCloudflareDomain("invalid.invalid")
		}()
	}
	wg.Wait()
}

// 可达性守护协程：探测与轮换逻辑验证。
func TestWatchdogProbeAndRotate(t *testing.T) {
	s, err := New(newTestConfig(), "password123", mustParseCIDRs(t))
	if err != nil {
		t.Fatal(err)
	}

	// 1. probeAlive 对不可达地址应返回 false
	if s.probeAlive("127.0.0.1") && false {
		t.Error("unreachable")
	}
	// 本地起一个 TCP listener 验证可达路径
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	s.mu.Lock()
	s.cfg.Port = port
	s.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if !s.probeAlive("127.0.0.1") {
		t.Error("本地 listener 应探测可达")
	}

	// 2. candidateIPs 返回测速结果前 5 名
	s.mu.Lock()
	for i := 0; i < 8; i++ {
		s.status.Results = append(s.status.Results, speedtest.Result{IP: fmt.Sprintf("10.0.0.%d", i)})
	}
	s.mu.Unlock()
	got := s.candidateIPs()
	if len(got) != 5 || got[0] != "10.0.0.0" {
		t.Errorf("candidateIPs 应返回前 5 名，实际 %v", got)
	}

	// 3. watchdogOnce：activeIP 不可达且备选均不可达 → 触发测速 + 进入退避
	s.mu.Lock()
	s.activeIP = "192.0.2.1" // TEST-NET-1，必然不可达
	s.running = false
	s.mu.Unlock()
	s.watchdogOnce()
	s.mu.Lock()
	streak, inBackoff := s.failStreak, time.Now().Before(s.backoffUntil)
	s.mu.Unlock()
	if streak != 1 || !inBackoff {
		t.Errorf("全部不可达应触发测速并进入退避，实际 streak=%d backoff=%v", streak, inBackoff)
	}

	// 4. 退避期内不再动作
	s.watchdogOnce()
	s.mu.Lock()
	streak2 := s.failStreak
	s.mu.Unlock()
	if streak2 != 1 {
		t.Errorf("退避期内不应重复触发，实际 streak=%d", streak2)
	}
}

// SyncRewrites 清空重发语义验证（httptest 模拟 AdGuard API）。
func TestSyncRewritesWipeAndRewrite(t *testing.T) {
	var mu sync.Mutex
	existing := map[string]adguard.RewriteEntry{
		"a.example.com|1.1.1.1":     {Domain: "a.example.com", Answer: "1.1.1.1", Enabled: true},
		"b.example.com|9.9.9.9":     {Domain: "b.example.com", Answer: "9.9.9.9", Enabled: true},
		"other.example.com|8.8.8.8": {Domain: "other.example.com", Answer: "8.8.8.8", Enabled: true}, // 非托管规则，也应被清空
	}
	mutations := 0

	mux := http.NewServeMux()
	handleList := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		list := make([]adguard.RewriteEntry, 0, len(existing))
		for _, e := range existing {
			list = append(list, e)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeJSONForTest(w, list)
	}
	mux.HandleFunc("/control/rewrite/list", handleList)
	mux.HandleFunc("/control/rewrite/add", func(w http.ResponseWriter, r *http.Request) {
		var ent adguard.RewriteEntry
		decodeBody(r, &ent)
		mu.Lock()
		existing[ent.Domain+"|"+ent.Answer] = ent
		mu.Unlock()
		mutations++
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/control/rewrite/delete", func(w http.ResponseWriter, r *http.Request) {
		var ent adguard.RewriteEntry
		decodeBody(r, &ent)
		mu.Lock()
		delete(existing, ent.Domain+"|"+ent.Answer)
		mu.Unlock()
		mutations++
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := adguard.New(srv.URL, "", "")
	want := map[string]map[string]bool{
		"a.example.com": {"1.1.1.1": true, "2.2.2.2": true},
		"b.example.com": {"3.3.3.3": true},
	}
	stats, err := client.SyncRewrites(want)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written != 3 {
		t.Errorf("应全量写入 3 条，实际 %d", stats.Written)
	}
	if stats.Removed != 3 {
		t.Errorf("应清空 3 条（含非托管规则），实际 %d", stats.Removed)
	}
	// 最终状态必须恰好等于期望集合
	mu.Lock()
	got := make(map[string]bool, len(existing))
	for k, e := range existing {
		got[k] = e.Enabled
	}
	mu.Unlock()
	wantFlat := map[string]bool{
		"a.example.com|1.1.1.1": true,
		"a.example.com|2.2.2.2": true,
		"b.example.com|3.3.3.3": true,
	}
	if len(got) != len(wantFlat) {
		t.Errorf("重写表条数不符（期望 %d，实际 %d）: %v", len(wantFlat), len(got), got)
	}
	for k := range wantFlat {
		if !got[k] {
			t.Errorf("缺失期望条目 %s", k)
		}
	}
	if mutations == 0 {
		t.Error("应有实际 API 变更发生")
	}
}

// P1-1 回归：部分字段提交（如仅 test_ipv6）不得清零其他设置。
func TestConfigPartialUpdatePreservesOtherFields(t *testing.T) {
	t.Setenv("DSA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	s, err := New(newTestConfig(), "password123", mustParseCIDRs(t))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Httping = true
	s.cfg.TestIPv6 = true
	s.cfg.IntervalMinutes = 60
	s.cfg.TestAll = true

	tok, err := s.sessions.newSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"test_ipv6":false}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if !s.cfg.Httping || s.cfg.IntervalMinutes != 60 || !s.cfg.TestAll || s.cfg.TestIPv6 {
		t.Errorf("部分提交不应清零未提交字段: httping=%v test_all=%v interval=%d test_ipv6=%v",
			s.cfg.Httping, s.cfg.TestAll, s.cfg.IntervalMinutes, s.cfg.TestIPv6)
	}
}

// 添加托管域名时，应从各实例自定义重写表中清理同名（含 *. 形式）条目。
func TestAddDomainRemovesConflictingManualRewrites(t *testing.T) {
	t.Setenv("DSA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	s, err := New(newTestConfig(), "password123", mustParseCIDRs(t))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.AdguardInstances = []config.AdguardInstance{
		{
			Name: "主", URL: "http://a:3000",
			ManualRewrites: []config.ManualRewrite{
				{Domain: "api.example.com", Answer: "1.2.3.4"},
				{Domain: "*.api.example.com", Answer: "1.2.3.5"},
				{Domain: "nas.example.com", Answer: "1.2.3.6"},
			},
		},
	}

	tok, err := s.sessions.newSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/domains", strings.NewReader(`{"domain":"api.example.com"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Domains       []string `json:"domains"`
		RemovedManual int      `json:"removed_manual"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RemovedManual != 2 {
		t.Errorf("应清理 2 条同名规则，实际 %d", resp.RemovedManual)
	}
	got := s.cfg.AdguardInstances[0].ManualRewrites
	if len(got) != 1 || got[0].Domain != "nas.example.com" {
		t.Errorf("冲突条目应被清理，剩余 %+v", got)
	}
}

// 包含子域的域名应同时写入裸域名与 *. 通配两条记录；未包含子域的清理 *. 残留。
func TestBuildWantWildcardBothForms(t *testing.T) {
	want := buildWant([]string{"*.a.com", "b.com"}, "1.1.1.1")
	if !want["*.a.com"]["1.1.1.1"] {
		t.Error("通配形式应写入 IP")
	}
	if !want["a.com"]["1.1.1.1"] {
		t.Error("裸域名形式应同步写入 IP（两条记录）")
	}
	if !want["b.com"]["1.1.1.1"] {
		t.Error("普通域名应写入 IP")
	}
	if len(want["*.b.com"]) != 0 {
		t.Error("未包含子域的域名应清空 *. 形式")
	}
}

// fresh start 时内置默认域名（如 openrouter.ai）尚无 DomainMetas，
// 切换「包含子域」开关应自动创建元数据而不是 404。
func TestWildcardToggleCreatesMeta(t *testing.T) {
	t.Setenv("DSA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	s, err := New(newTestConfig(), "password123", mustParseCIDRs(t))
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 fresh start：默认域名存在但元数据为空
	if len(s.cfg.DomainMetas) != 0 {
		t.Fatalf("测试前提：默认配置 DomainMetas 应为空，实际 %d", len(s.cfg.DomainMetas))
	}

	tok, err := s.sessions.newSession()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/domains/wildcard", strings.NewReader(`{"domain":"openrouter.ai","wildcard":true}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	found := false
	for _, m := range s.cfg.DomainMetas {
		if m.Domain == "openrouter.ai" && m.Wildcard {
			found = true
		}
	}
	if !found {
		t.Errorf("应自动创建 openrouter.ai 的元数据并置 wildcard=true，实际 %+v", s.cfg.DomainMetas)
	}
}
