package task

import (
	"net"
	"path/filepath"
	"testing"
)

// 聚合 key（IPv4 /24 为 3 字节 hex、IPv6 /64 为 8 字节 hex）保存加载后必须原样保留。
func TestColoDBRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.json")

	db := newColoDB()
	db.filePath = path
	ip6 := net.ParseIP("2606:4700:4700::1111")
	if ip6 == nil {
		t.Fatal("bad test ip")
	}
	if !db.SetColo(ip6, "HKG") {
		t.Fatal("SetColo 应写入新条目")
	}
	ip4 := net.ParseIP("104.16.144.240")
	db.SetColo(ip4, "SJC")
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := newColoDB()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if got := loaded.GetColo(ip6); got != "HKG" {
		t.Errorf("IPv6 映射加载后丢失，got %q", got)
	}
	if got := loaded.GetColo(ip4); got != "SJC" {
		t.Errorf("IPv4 映射加载后丢失，got %q", got)
	}
	// 同网段其他 IP 也能命中（按 /24、/64 聚合）
	if got := loaded.GetColo(net.ParseIP("104.16.144.99")); got != "SJC" {
		t.Errorf("同 /24 网段 IP 应命中聚合映射，got %q", got)
	}
	if got := loaded.GetColo(net.ParseIP("2606:4700:4700::1")); got != "HKG" {
		t.Errorf("同 /64 网段 IP 应命中聚合映射，got %q", got)
	}
}
