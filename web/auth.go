package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// auth 中间件：会话 Token 鉴权 + 来源 CIDR 白名单。

// sessionTTL 会话有效期。
const sessionTTL = 24 * time.Hour

// maxSessions 同时存在的会话上限（防止内存无限增长）。
const maxSessions = 64

// loginLockoutThreshold / loginLockoutWindow 登录防爆破参数：
// 同一来源 IP 在窗口内连续失败达到阈值后，锁定至窗口结束。
const (
	loginLockoutThreshold = 5
	loginLockoutWindow    = 15 * time.Minute
)

// secureCookies 控制 cookie 的 Secure 标志。
// 默认 false（内网 HTTP 直连场景）；置于 TLS 反代之后时，
// 可通过构建时 -ldflags "-X web.secureCookies=true" 或环境变量 DSA_SECURE_COOKIES 开启。
var secureCookies = false

// sessionStore 支持多会话并存的会话存储。
// 每次登录签发独立 Token，互不挤占；过期会话惰性清理。
type sessionStore struct {
	mu        sync.Mutex
	sessions  map[string]time.Time // token -> 过期时间
	generator func() (string, error)
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions:  make(map[string]time.Time),
		generator: randomToken,
	}
}

// RandomPassword 生成 20 位随机密码（字母数字，去除易混淆字符）。
// 使用拒绝采样消除模偏差：只接受落在 charset 整数倍范围内的随机值。
func RandomPassword() (string, error) {
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// 计算 unbiased 上限：256 中能被 charset 均分的最大范围
	maxValid := 256 - (256 % len(charset))
	out := make([]byte, 0, 20)
	buf := make([]byte, 32)
	for len(out) < 20 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= maxValid {
				continue // 拒绝采样：丢弃会导致偏差的值
			}
			out = append(out, charset[int(b)%len(charset)])
			if len(out) == 20 {
				break
			}
		}
	}
	return string(out), nil
}

// randomToken 生成 256 位随机 Token。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newSession 创建一个新会话。超过并发上限时优先淘汰最早过期的会话。
func (s *sessionStore) newSession() (string, error) {
	tok, err := s.generator()
	if err != nil {
		return "", err
	}
	now := time.Now()
	exp := now.Add(sessionTTL)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 惰性清理过期会话
	for t, e := range s.sessions {
		if now.After(e) {
			delete(s.sessions, t)
		}
	}
	// 超上限时淘汰最早过期的会话
	if len(s.sessions) >= maxSessions {
		var oldestTok string
		var oldestExp time.Time
		first := true
		for t, e := range s.sessions {
			if first || e.Before(oldestExp) {
				oldestTok, oldestExp = t, e
				first = false
			}
		}
		delete(s.sessions, oldestTok)
	}

	s.sessions[tok] = exp
	return tok, nil
}

// valid 检查 Token 是否有效（存在且未过期）。
func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

// revoke 吊销指定 Token（登出用）。
func (s *sessionStore) revoke(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[tok]
	delete(s.sessions, tok)
	return ok
}

// loginLimiter 按来源 IP 的登录失败计数与锁定。
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*loginFailState
}

type loginFailState struct {
	count       int
	windowEnd   time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: make(map[string]*loginFailState)}
}

// allowed 判断该来源 IP 当前是否允许尝试登录。
func (l *loginLimiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.fails[ip]
	if !ok {
		return true
	}
	now := time.Now()
	if now.After(st.lockedUntil) && now.After(st.windowEnd) {
		delete(l.fails, ip) // 窗口已过且未在锁定期，重置
		return true
	}
	return now.After(st.lockedUntil)
}

// recordFailure 记录一次失败；达到阈值则锁定至窗口结束。返回剩余可尝试次数。
func (l *loginLimiter) recordFailure(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// P1-4：顺带全量清扫过期条目。公网暴露时攻击者可用海量伪造源 IP
	// 使 map 无限增长；登录频率低，全量遍历无性能压力。
	for k, st := range l.fails {
		if now.After(st.windowEnd) && now.After(st.lockedUntil) {
			delete(l.fails, k)
		}
	}
	st, ok := l.fails[ip]
	if !ok || now.After(st.windowEnd) {
		st = &loginFailState{windowEnd: now.Add(loginLockoutWindow)}
		l.fails[ip] = st
	}
	st.count++
	if st.count >= loginLockoutThreshold {
		st.lockedUntil = st.windowEnd // 锁定到本统计窗口结束
	}
	return loginLockoutThreshold - st.count
}

// reset 清除该 IP 的失败记录（登录成功后调用）。
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// cidrList 来源 CIDR 白名单。
type cidrList struct {
	nets []*net.IPNet
}

// ParseCIDRs 解析逗号分隔的 CIDR 列表；为空时返回错误（必须显式配置）。
func ParseCIDRs(s string) (*cidrList, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("必须通过环境变量 DSA_ALLOWED_CIDRS 显式配置允许访问 Dashboard 的来源网段（例如 192.168.0.0/23）")
	}
	cl := &cidrList{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			// 单个 IP 视为 /32 或 /128
			if ip := net.ParseIP(part); ip != nil {
				if ip.To4() != nil {
					part += "/32"
				} else {
					part += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("非法的 CIDR %q: %w", part, err)
		}
		cl.nets = append(cl.nets, n)
	}
	if len(cl.nets) == 0 {
		return nil, fmt.Errorf("DSA_ALLOWED_CIDRS 未包含任何有效网段")
	}
	return cl, nil
}

// contains 检查 IP 是否在白名单内。
func (cl *cidrList) contains(ipStr string) bool {
	ip, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		ip = ipStr
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range cl.nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// extractToken 从请求中提取 Token：仅支持 Authorization: Bearer 与 HttpOnly cookie。
// （不再接受 URL query 传 token，避免 token 泄漏进访问日志/代理日志。）
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("dsa_token"); err == nil {
		return c.Value
	}
	return ""
}

// clientIP 提取请求来源 IP（不含端口），用于登录限流。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authMiddleware 包装需要鉴权的 handler。
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 来源 CIDR 白名单
		if s.allowedCIDRs != nil && !s.allowedCIDRs.contains(r.RemoteAddr) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "来源 IP 不在允许的网段内"})
			return
		}
		// 2. 会话 Token
		if !s.sessions.valid(extractToken(r)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已过期"})
			return
		}
		next(w, r)
	}
}

// handleLogin 登录：校验密码，签发会话 Token（写入 HttpOnly cookie 并返回 body）。
// 内置按来源 IP 的防爆破锁定：窗口内连续失败达阈值后锁定至窗口结束。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.allowedCIDRs != nil && !s.allowedCIDRs.contains(r.RemoteAddr) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "来源 IP 不在允许的网段内"})
		return
	}
	ip := clientIP(r)

	// 防爆破：锁定期内直接拒绝（不消耗密码比较时间）
	if !s.loginLimit.allowed(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "失败次数过多，请稍后再试"})
		return
	}

	var in struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		// P1-5：请求体超过 MaxBytesReader 上限时返回 413
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "请求体过大或格式错误"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Password), []byte(s.password)) != 1 {
		remaining := s.loginLimit.recordFailure(ip)
		// 短暂等待增加在线爆破成本（配合锁定机制）
		time.Sleep(500 * time.Millisecond)
		msg := fmt.Sprintf("密码错误（剩余尝试次数：%d）", remaining)
		if remaining <= 0 {
			msg = "失败次数过多，已临时锁定，请稍后再试"
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msg})
		return
	}
	s.loginLimit.reset(ip)

	tok, err := s.sessions.newSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "生成会话失败"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "dsa_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secureCookies, // TLS 反代场景可通过 DSA_SECURE_COOKIES=1 开启
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// handleLogout 登出：吊销当前会话 Token 并清除 cookie。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok := extractToken(r)
	s.sessions.revoke(tok)
	http.SetCookie(w, &http.Cookie{
		Name:     "dsa_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secureCookies,
		MaxAge:   -1, // 立即删除 cookie
	})
	writeJSON(w, http.StatusOK, map[string]string{"message": "已退出登录"})
}
