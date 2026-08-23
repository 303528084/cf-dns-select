package task

import (
	"bufio"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
)

const defaultInputFile = "ip.txt"

// maxIPPool 限制 IP 地址池的最大数量，防止误配大网段 + 测速全部 IP 时内存耗尽（OOM）。
const maxIPPool = 100000

var (
	// TestAll test all ip
	TestAll = false
	// IPFile is the filename of IP Rangs
	IPFile = defaultInputFile
)

// InitRandSeed 兼容保留：Go 1.20+ 全局 rand 已自动随机播种，无需手动 Seed。
func InitRandSeed() {}

func isIPv4(ip string) bool {
	return strings.Contains(ip, ".")
}

func randIPEndWith(num byte) byte {
	if num == 0 { // 对于 /32 这种单独的 IP
		return byte(0)
	}
	// P2-6：原实现 Intn(num) 覆盖 0~num-1，末段为 255 的 IP 永不被采样
	return byte(rand.Intn(int(num) + 1))
}

type IPRanges struct {
	ips     []*net.IPAddr
	mask    string
	firstIP net.IP
	ipNet   *net.IPNet
}

func newIPRanges() *IPRanges {
	return &IPRanges{
		ips: make([]*net.IPAddr, 0),
	}
}

// addIP 追加 IP 到地址池，超过上限时返回错误（防止 OOM）。
func (r *IPRanges) addIP(ip net.IP) error {
	if len(r.ips) >= maxIPPool {
		return fmt.Errorf("IP 地址池数量超过上限 %d（请检查 IP 段文件是否包含过大网段，或关闭「测速全部 IP」）", maxIPPool)
	}
	r.ips = append(r.ips, &net.IPAddr{IP: ip})
	return nil
}

// 如果是单独 IP 则加上子网掩码，反之则获取子网掩码(r.mask)
func (r *IPRanges) fixIP(ip string) (string, error) {
	// 如果不含有 '/' 则代表不是 IP 段，而是一个单独的 IP，因此需要加上 /32 /128 子网掩码
	if i := strings.IndexByte(ip, '/'); i < 0 {
		if isIPv4(ip) {
			r.mask = "/32"
		} else {
			r.mask = "/128"
		}
		ip += r.mask
	} else {
		r.mask = ip[i:]
	}
	return ip, nil
}

// parseCIDR 解析 IP 段，获得 IP、IP 范围、子网掩码；解析失败返回 error 而非直接退出。
func (r *IPRanges) parseCIDR(ip string) error {
	ip, err := r.fixIP(ip)
	if err != nil {
		return err
	}
	if r.firstIP, r.ipNet, err = net.ParseCIDR(ip); err != nil {
		return fmt.Errorf("非法的 IP 段 %q: %w", ip, err)
	}
	return nil
}

func (r *IPRanges) appendIPv4(d byte) error {
	return r.addIP(net.IPv4(r.firstIP[12], r.firstIP[13], r.firstIP[14], d))
}

// 返回第四段 ip 的最小值及可用数目
func (r *IPRanges) getIPRange() (minIP, hosts byte) {
	minIP = r.firstIP[15] & r.ipNet.Mask[3] // IP 第四段最小值

	// 根据子网掩码获取主机数量
	m := net.IPv4Mask(255, 255, 255, 255)
	for i, v := range r.ipNet.Mask {
		m[i] ^= v
	}
	total, _ := strconv.ParseInt(m.String(), 16, 32) // 总可用 IP 数
	if total > 255 {                                 // 矫正 第四段 可用 IP 数
		hosts = 255
		return
	}
	hosts = byte(total)
	return
}

func (r *IPRanges) chooseIPv4() error {
	if r.mask == "/32" { // 单个 IP 则无需随机，直接加入自身即可
		return r.addIP(r.firstIP)
	}
	minIP, hosts := r.getIPRange()    // 返回第四段 IP 的最小值及可用数目
	for r.ipNet.Contains(r.firstIP) { // 只要该 IP 没有超出 IP 网段范围，就继续循环随机
		if TestAll { // 如果是测速全部 IP
			for i := 0; i <= int(hosts); i++ { // 遍历 IP 最后一段最小值到最大值
				if err := r.appendIPv4(byte(i) + minIP); err != nil {
					return err
				}
			}
		} else { // 随机 IP 的最后一段 0.0.0.X
			if err := r.appendIPv4(minIP + randIPEndWith(hosts)); err != nil {
				return err
			}
		}
		r.firstIP[14]++ // 0.0.(X+1).X
		if r.firstIP[14] == 0 {
			r.firstIP[13]++ // 0.(X+1).X.X
			if r.firstIP[13] == 0 {
				r.firstIP[12]++ // (X+1).X.X.X
			}
		}
	}
	return nil
}

func (r *IPRanges) chooseIPv6() error {
	if r.mask == "/128" { // 单个 IP 则无需随机，直接加入自身即可
		return r.addIP(r.firstIP)
	}
	var tempIP uint8                  // 临时变量，用于记录前一位的值
	for r.ipNet.Contains(r.firstIP) { // 只要该 IP 没有超出 IP 网段范围，就继续循环随机
		r.firstIP[15] = randIPEndWith(255) // 随机 IP 的最后一段
		r.firstIP[14] = randIPEndWith(255) // 随机 IP 的最后一段

		targetIP := make([]byte, len(r.firstIP))
		copy(targetIP, r.firstIP)
		if err := r.addIP(targetIP); err != nil { // 加入 IP 地址池
			return err
		}

		for i := 13; i >= 0; i-- { // 从倒数第三位开始往前随机
			tempIP = r.firstIP[i]              // 保存前一位的值
			r.firstIP[i] += randIPEndWith(255) // 随机 0~255，加到当前位上
			if r.firstIP[i] >= tempIP {        // 如果当前位的值大于等于前一位的值，说明随机成功了，可以退出该循环
				break
			}
		}
	}
	return nil
}

// loadIPRanges 加载 IP 段并生成待测速地址池；任何错误（文件不存在、非法 CIDR、超上限）都会返回 error。
func loadIPRanges() ([]*net.IPAddr, error) {
	ranges := newIPRanges()
	var err error
	if IPFile == "" {
		IPFile = defaultInputFile
	}
	file, err := os.Open(IPFile)
	if err != nil {
		return nil, fmt.Errorf("打开 IP 段文件 %q 失败: %w", IPFile, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() { // 循环遍历文件每一行
		line := strings.TrimSpace(scanner.Text())       // 去除首尾的空白字符（空格、制表符、换行符等）
		if line == "" || strings.HasPrefix(line, "#") { // 跳过空行与注释行
			continue
		}
		if err = ranges.parseCIDR(line); err != nil { // 解析 IP 段，获得 IP、IP 范围、子网掩码
			return nil, err
		}
		if isIPv4(line) { // 生成要测速的所有 IPv4 / IPv6 地址（单个/随机/全部）
			err = ranges.chooseIPv4()
		} else {
			err = ranges.chooseIPv6()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 IP 段文件 %q 失败: %w", IPFile, err)
	}
	return ranges.ips, nil
}
