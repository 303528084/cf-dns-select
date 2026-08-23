// cf-dns-select —— Cloudflare 最优节点自动优选 + 多实例 AdGuard Home DNS 重写。
//
// 在原「Cloudflare 最优节点测速」能力之上，增加了：
//   - 内置 Web Dashboard（登录鉴权 + 来源 CIDR 白名单）
//   - 按设定频率自动测速，并将最快 IP 同步写入多个 AdGuard Home 实例的 DNS 重写表
//   - 支持为任意使用 Cloudflare 的域名（如 openrouter.ai）写入同一条最优 IP 重写
//
// 安全要求（缺一不可启动）：
//   - DSA_ALLOWED_CIDRS：允许访问 Dashboard 的来源网段（逗号分隔，如 192.168.0.0/23）
//   - Dashboard 登录密码：环境变量 DSA_PASSWORD 指定；未指定时首次启动自动生成并打印到日志
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neon9809/cf-dns-select/config"
	"github.com/neon9809/cf-dns-select/task"
	"github.com/neon9809/cf-dns-select/web"
)

var version = "dev"

func main() {
	// 同步版本号到 web 包（/api/health 使用），ldflags 只注入 main.version
	web.SetVersion(version)
	var (
		listen   string
		cfgFile  string
		printVer bool
		runOnce  bool
	)
	flag.StringVar(&listen, "listen", "0.0.0.0:8080", "Dashboard 监听地址")
	flag.StringVar(&cfgFile, "config", "", "配置文件路径（默认 ./config.json，可用环境变量 DSA_CONFIG 覆盖）")
	flag.BoolVar(&runOnce, "once", false, "只执行一次测速并写入后退出（供 cron 使用）")
	flag.BoolVar(&printVer, "v", false, "打印版本")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cf-dns-select %s —— Cloudflare 最优节点自动写入 AdGuard Home DNS 重写\n\n", version)
		fmt.Fprintln(os.Stderr, "参数：")
		flag.PrintDefaults()
	}
	flag.Parse()

	if printVer {
		fmt.Println("cf-dns-select " + version)
		return
	}

	if cfgFile != "" {
		os.Setenv("DSA_CONFIG", cfgFile)
	}

	task.InitRandSeed()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	config.SetGlobal(cfg)

	// ------------------------------------------------------------------
	// 安全门禁 1：必须显式配置允许的来源 CIDR 网段，否则拒绝启动
	// ------------------------------------------------------------------
	cidrs, err := web.ParseCIDRs(os.Getenv("DSA_ALLOWED_CIDRS"))
	if err != nil {
		log.Fatalf("[安全门禁] %v", err)
	}

	// ------------------------------------------------------------------
	// 安全门禁 2：Dashboard 登录密码。
	//   - 环境变量 DSA_PASSWORD 显式指定；
	//   - 未指定时首次启动生成随机密码并打印到容器日志。
	// ------------------------------------------------------------------
	password := os.Getenv("DSA_PASSWORD")
	if password == "" {
		password, err = web.RandomPassword()
		if err != nil {
			log.Fatalf("生成随机密码失败: %v", err)
		}
		fmt.Println("==================================================")
		fmt.Println("  未设置 DSA_PASSWORD 环境变量。")
		fmt.Printf("  本次启动已自动生成随机 Dashboard 登录密码：\n\n      %s\n\n", password)
		fmt.Println("  请立即记录！该密码仅在本次进程生命周期内有效，")
		fmt.Println("  重启后若仍未设置 DSA_PASSWORD 会重新生成。")
		fmt.Println("==================================================")
	} else if len(password) < 8 {
		// P2-13：显式弱密码告警（不拒绝启动，保持兼容）
		fmt.Println("[警告] DSA_PASSWORD 长度不足 8 位，存在被暴力破解的风险，建议使用更强的密码。")
	}

	srv, err := web.New(cfg, password, cidrs)
	if err != nil {
		log.Fatalf("初始化 Dashboard 失败: %v", err)
	}

	addr := listen
	// M-4：命令行显式传参优先于环境变量
	if !flagExplicit("listen") {
		if p := os.Getenv("DSA_LISTEN"); p != "" {
			addr = p
		}
	}

	if runOnce {
		srv.RunOnceAndWait()
		return
	}

	srv.StartScheduler()

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Printf("\n==============================================\n")
	fmt.Printf("  cf-dns-select %s\n", version)
	fmt.Printf("  Dashboard: http://%s/\n", addr)
	fmt.Printf("  允许来源网段: %s\n", os.Getenv("DSA_ALLOWED_CIDRS"))
	fmt.Printf("==============================================\n\n")

	// H-5：优雅退出 —— SIGTERM/SIGINT 后停止接收新连接，等待在途请求完成；
	// P0-2：再等待在途测速任务完成（避免写入阶段被杀导致实例间不一致）
	done := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		fmt.Println("\n收到退出信号，正在优雅关闭...")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		if srv.WaitRunners(30 * time.Second) {
			fmt.Println("在途测速任务已完成。")
		} else {
			fmt.Println("[警告] 等待测速任务超时（30 秒），强制退出；下次测速会自动收敛。")
		}
		close(done)
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Dashboard 服务启动失败: %v", err)
	}
	<-done
	fmt.Println("已退出。")
}

// flagExplicit 判断某个 flag 是否被显式传入（区别于默认值）。
func flagExplicit(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
