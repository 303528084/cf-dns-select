package task

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/neon9809/cf-dns-select/utils"
)

const (
	tcpConnectTimeout = time.Second * 1
	maxRoutine        = 1000
	defaultRoutines   = 200
	defaultPort       = 443
	defaultPingTimes  = 4
)

var (
	Routines      = defaultRoutines
	TCPPort   int = defaultPort
	PingTimes int = defaultPingTimes
)

type Ping struct {
	wg      *sync.WaitGroup
	m       *sync.Mutex
	ips     []*net.IPAddr
	csv     utils.PingDelaySet
	control chan bool
	bar     *utils.Bar
}

func checkPingDefault() {
	if Routines <= 0 {
		Routines = defaultRoutines
	}
	if Routines > maxRoutine {
		// P2-2：并发上限此前声明未使用，误配超大值会开海量 goroutine 导致 OOM
		utils.Yellow.Printf("[警告] 延迟测速线程数 %d 超过安全上限 %d，已按上限执行\n", Routines, maxRoutine)
		Routines = maxRoutine
	}
	if TCPPort <= 0 || TCPPort >= 65535 {
		TCPPort = defaultPort
	}
	if PingTimes <= 0 {
		PingTimes = defaultPingTimes
	}
}

func NewPing() (*Ping, error) {
	checkPingDefault()
	ips, err := loadIPRanges()
	if err != nil {
		return nil, err
	}
	// 地区过滤：配置了目标地区时，严格按地区库过滤候选 IP（IPv4/IPv6 一致）。
	// HTTPing 模式：未知地区 IP 放行（测完学习入库）；已知 IP 按库过滤。
	// TCPing 模式：只测库中已确认属于目标地区的 IP；未知 IP 跳过（无法学习）。
	if HttpingCFColo != "" {
		before := len(ips)
		filtered := make([]*net.IPAddr, 0, before)
		for _, ip := range ips {
			if coloAllowed(ip.IP) {
				filtered = append(filtered, ip)
			}
		}
		mode := "TCPing"
		if Httping {
			mode = "HTTPing"
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("地区库中没有目标地区 %s 的 IP 记录（%s 模式），无候选可测。请先以 HTTPing 模式测速学习地区库，或清空地区过滤", HttpingCFColo, mode)
		}
		ips = filtered
		utils.Green.Printf("地区过滤（%s）：跳过 %d 个，实际测试 %d 个（地区库 %d 条映射）\n",
			mode, before-len(ips), len(ips), ColoIPDB.Len())
	}
	return &Ping{
		wg:      &sync.WaitGroup{},
		m:       &sync.Mutex{},
		ips:     ips,
		csv:     make(utils.PingDelaySet, 0),
		control: make(chan bool, Routines),
		bar:     utils.NewBarWithPhase(len(ips), "可用:", "", "ping"),
	}, nil
}

func (p *Ping) Run() utils.PingDelaySet {
	if len(p.ips) == 0 {
		return p.csv
	}
	if Httping {
		utils.Cyan.Printf("开始延迟测速（模式：HTTP, 端口：%d, 范围：%v ~ %v ms, 丢包：%.2f)\n", TCPPort, utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	} else {
		utils.Cyan.Printf("开始延迟测速（模式：TCP, 端口：%d, 范围：%v ~ %v ms, 丢包：%.2f)\n", TCPPort, utils.InputMinDelay.Milliseconds(), utils.InputMaxDelay.Milliseconds(), utils.InputMaxLossRate)
	}
	for _, ip := range p.ips {
		p.wg.Add(1)
		p.control <- false
		go p.start(ip)
	}
	p.wg.Wait()
	p.bar.Done()
	sort.Sort(p.csv)
	return p.csv
}

func (p *Ping) start(ip *net.IPAddr) {
	defer p.wg.Done()
	p.tcpingHandler(ip)
	<-p.control
}

// bool connectionSucceed float32 time
func (p *Ping) tcping(ip *net.IPAddr) (bool, time.Duration) {
	startTime := time.Now()
	var fullAddress string
	if isIPv4(ip.String()) {
		fullAddress = fmt.Sprintf("%s:%d", ip.String(), TCPPort)
	} else {
		fullAddress = fmt.Sprintf("[%s]:%d", ip.String(), TCPPort)
	}
	conn, err := net.DialTimeout("tcp", fullAddress, tcpConnectTimeout)
	if err != nil {
		return false, 0
	}
	defer conn.Close()
	duration := time.Since(startTime)
	return true, duration
}

// pingReceived pingTotalTime
func (p *Ping) checkConnection(ip *net.IPAddr) (recv int, totalDelay time.Duration, colo string) {
	if Httping {
		recv, totalDelay, colo = p.httping(ip)
		return
	}
	colo = "" // TCPing 不获取 colo
	for i := 0; i < PingTimes; i++ {
		if ok, delay := p.tcping(ip); ok {
			recv++
			totalDelay += delay
		}
	}
	return
}

func (p *Ping) appendIPData(data *utils.PingData) {
	p.m.Lock()
	defer p.m.Unlock()
	p.csv = append(p.csv, utils.CloudflareIPData{
		PingData: data,
	})
}

// handle tcping
func (p *Ping) tcpingHandler(ip *net.IPAddr) {
	recv, totalDlay, colo := p.checkConnection(ip)
	// 在锁内读取 len(p.csv)，避免与其他 goroutine 的 append 并发竞争（上游原版存在此数据竞争）
	p.m.Lock()
	nowAble := len(p.csv)
	if recv != 0 {
		nowAble++
	}
	p.m.Unlock()
	// detail 携带当前正在测试的地址，供 dashboard 实时展示
	p.bar.Grow(1, ip.String())
	if recv == 0 {
		return
	}
	data := &utils.PingData{
		IP:       ip,
		Sended:   PingTimes,
		Received: recv,
		Delay:    totalDlay / time.Duration(recv),
		Colo:     colo,
	}
	p.appendIPData(data)
}
