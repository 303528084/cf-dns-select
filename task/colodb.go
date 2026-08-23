package task

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ----------------------------------------------------------------------------
// Cloudflare IP → 地区（IATA 机场码）映射库
//
// 背景：Cloudflare 官方只公布全量 IP 段，不公布 IP 与节点的对应关系；
// anycast 地址的 GeoIP 数据也不可靠。本库采用「学习 + 持久化」策略：
//   1. HTTPing 测速时从响应头 cf-ray 学到 IP→地区码 的真实映射，写入内存库并落盘；
//   2. 后续任何测速（含 TCPing）先查库：命中指定地区的 IP 才参与测试，
//      未命中的直接跳过——大幅节省时间；
//   3. 库文件随使用不断丰富，越用越准。
//
// 库文件默认与 IP 段文件同目录（colo_ip_db.json），可用 DSA_COLO_IP_DB 覆盖路径。
// ----------------------------------------------------------------------------

// coloEntry 单条映射记录。
type coloEntry struct {
	Colo string `json:"colo"` // IATA 机场码，如 HKG
}

// coloDB IP→地区 映射库。key 为 IP 的字符串形式（JSON 安全）。
type coloDB struct {
	mu       sync.RWMutex
	entries  map[string]coloEntry
	filePath string
	dirty    bool
}

var ColoIPDB = newColoDB()

func newColoDB() *coloDB {
	return &coloDB{entries: make(map[string]coloEntry)}
}

// ipKey 把 IP 转为 map key（按网段聚合：IPv4 取前 3 字节 /24，IPv6 取前 8 字节 /64）。
// 以十六进制字符串存储——JSON 安全；此前直接存二进制字符串会被 UTF-8 编码替换
// 非法字节，导致映射加载后永久丢失。
func ipKey(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return hex.EncodeToString(v4[:3])
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	return hex.EncodeToString(v6[:8])
}

// SetColo 记录一条映射；返回是否为新条目。
func (db *coloDB) SetColo(ip net.IP, colo string) bool {
	if ip == nil || colo == "" {
		return false
	}
	key := ipKey(ip)
	db.mu.Lock()
	defer db.mu.Unlock()
	if old, ok := db.entries[key]; ok && old.Colo == colo {
		return false
	}
	db.entries[key] = coloEntry{Colo: colo}
	db.dirty = true
	return true
}

// GetColo 查询 IP 的地区码；未知返回 ""。
func (db *coloDB) GetColo(ip net.IP) string {
	if ip == nil {
		return ""
	}
	key := ipKey(ip)
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.entries[key].Colo
}

// Len 返回库中条目数。
func (db *coloDB) Len() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.entries)
}

// Save 把库持久化到 JSON 文件（原子写：tmp + rename）。
func (db *coloDB) Save() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.dirty || db.filePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(db.entries, "", " ")
	if err != nil {
		return err
	}
	tmp := db.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, db.filePath); err != nil {
		os.Remove(tmp)
		return err
	}
	db.dirty = false
	return nil
}

// Load 从文件加载库；文件不存在不算错误。
func (db *coloDB) Load(path string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if path == "" {
		path = defaultColoDBPath()
	}
	db.filePath = path
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	entries := make(map[string]coloEntry)
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&entries); err != nil {
		return err // 文件损坏时从空库开始，不影响测速
	}
	db.entries = entries
	return nil
}

// defaultColoDBPath 库文件默认路径：跟随 IP 段文件所在目录。
func defaultColoDBPath() string {
	dir := filepath.Dir(IPFile)
	if dir == "." || dir == "" {
		return "colo_ip_db.json"
	}
	return filepath.Join(dir, "colo_ip_db.json")
}

// InitColoIPDB 初始化地区库：优先环境变量 DSA_COLO_IP_DB，否则用默认路径。
func InitColoIPDB() {
	path := os.Getenv("DSA_COLO_IP_DB")
	if err := ColoIPDB.Load(path); err != nil {
		log.Printf("[警告] 地区库加载失败（将从空库开始）: %v", err)
	} else if ColoIPDB.Len() > 0 {
		log.Printf("地区库已加载：%d 条 IP→地区 映射（%s）", ColoIPDB.Len(), ColoIPDB.filePath)
	}
}

// coloAllowed 判断 IP 是否符合当前地区过滤：
//   - 未配置过滤（HttpingCFColo 为空）→ 全部允许；
//   - 已配置过滤：
//     · HTTPing 模式：未知地区 IP 放行（测完会学习入库）；已知 IP 按库过滤。
//     · TCPing 模式：只测库中已确认属于目标地区的 IP；未知 IP 跳过（无法学习，白测浪费）。
//
// 返回值 allowed 表示是否应继续测试该 IP。
func coloAllowed(ip net.IP) bool {
	if HttpingCFColo == "" {
		return true
	}
	known := ColoIPDB.GetColo(ip)
	if Httping {
		// HTTPing：未知 IP 放行以学习；已知 IP 按库过滤
		if known == "" {
			return true
		}
	} else {
		// TCPing：未知 IP 无法学习地区，跳过；只测已知目标地区 IP
		if known == "" {
			return false
		}
	}
	want := strings.Split(strings.ToUpper(HttpingCFColo), ",")
	for _, c := range want {
		if strings.TrimSpace(c) == known {
			return true
		}
	}
	return false
}
