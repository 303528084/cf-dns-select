# cf-dns-select

> ⚡ Cloudflare 最优节点自动优选 + 多实例 AdGuard Home DNS 重写
>
> 定时测速找出当前最快的 Cloudflare IP，自动同步写入局域网内**多个 AdGuard Home 实例**的 DNS 重写表，
> 让全屋设备无需任何配置即可享受最优的 Cloudflare 加速线路。

## 这是什么

国外大量网站（OpenAI、openrouter.ai、Workers 应用等）托管在 Cloudflare CDN 上，
但默认分配的节点对部分网络环境并不友好。本项目：

1. **测速**：复用久经考验的 [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) 测速内核
   （TCPing / HTTPing / 下载测速），从 Cloudflare 全部 IP 段中找出当前最快的节点；
2. **写入**：通过 AdGuard Home 官方 HTTP API，把最快 IP 写入 DNS 重写规则——
   支持通配符域名（如 `*.example.com`）和任意自定义域名列表；
3. **守护**：内置可达性守护协程，每分钟探测当前生效 IP；不可达时自动轮换上次测速的
   #2~#5 备选，全部失效则触发重新测速（指数退避）；
4. **同步**：一次结果同时写入你的**全部 AdGuard Home 实例**（主备互备），
   单实例故障不影响其他实例。

与原项目的关系：本项目以 CloudflareSpeedTest 的 `task`/`utils` 包作为测速内核库，
在其之上构建了全新的守护进程形态产品（Web Dashboard、多实例同步、安全门禁、容器化部署），
并非其 CLI 工具的分支替代。

## 功能一览

- 🔄 **自动调度**：按设定间隔（默认 60 分钟）自动测速；支持「启动时立即测一次」与手动触发
- 💓 **可达性守护**：每分钟探测当前生效 IP；不可达时自动轮换备选（上次测速 #2~#5），
  全部失效则重新测速并按指数退避（1→2→4→…→60 分钟）重试
- 🎯 **单 IP 写入**：每域名只写最快 1 条重写（AdGuard rewrite 不支持同域名多条记录）；
  启用 IPv6 测速时与 IPv4 合并排名取最快
- 🏠 **多实例互备**：所有 AdGuard Home 实例同步写入，连通性并发探测
- ⚛️ **原子更新**：优先使用 `PUT /control/rewrite/update` 无空窗更新，自动清理同域名重复条目
- 🌐 **灵活域名**：主重写域名（支持通配符）+ 任意数量的附加域名（如 `openrouter.ai`）
- 🔐 **安全内建**：
  - 来源 CIDR 白名单门禁——未显式配置允许网段时**拒绝启动**
  - Dashboard 登录鉴权（会话 Token、恒定时间比较、防爆破延迟）
  - 未配置密码时自动生成随机密码并打印到启动日志
  - API 回显凭据脱敏；前端全量转义防 XSS
- 🖥️ **中文 Dashboard**：状态总览、实例管理、结果排行、写入记录、参数配置
- 🐳 **容器化**：多阶段构建、非 root 运行、healthcheck、配置持久化

## 快速开始

```bash
git clone https://github.com/neon9809/cf-dns-select.git
cd cf-dns-select

# 1. 编辑 docker-compose.yml：把 CFS_ALLOWED_CIDRS 改成你局域网的实际网段
# 2. 准备可写的数据目录（容器内用户 uid=1000）
mkdir -p data && sudo chown -R 1000:1000 data

docker compose up -d

# 若未设置 DSA_PASSWORD，查看随机生成的登录密码：
docker logs cf-dns-select | grep -A 3 "随机"
```

打开 `http://<宿主机IP>:8080/` 登录后：

1. 在「AdGuard Home 实例」中添加你的实例（如 主 / 备），系统会自动检测连通性
2. 配置主重写域名（支持通配符），并按需添加其他域名
3. 保存配置 → 点击「立即测速并写入全部实例」

## 环境变量 / 参数

| 项 | 默认 | 说明 |
|---|---|---|
| `-listen` | `0.0.0.0:8080` | Dashboard 监听地址 |
| `-config` | `./config.json` | 配置文件路径 |
| `-once` | 关 | 只测速一次并写入后退出（适合 cron） |
| `DSA_ALLOWED_CIDRS` | **无（必须配置）** | 允许访问 Dashboard 的来源网段，逗号分隔；未配置时进程拒绝启动 |
| `DSA_PASSWORD` | 随机生成 | Dashboard 登录密码；未设置时每次启动生成新随机密码并打印到日志 |
| `DSA_CONFIG` | — | 同 `-config` |
| `DSA_LISTEN` | — | 同 `-listen`（命令行显式传参时优先） |
| `DSA_COLO_IP_DB` | 跟随 IP 段文件目录 | IP→地区 学习库路径；Docker 部署建议设为 `/app/data/colo_ip_db.json` 以随数据卷持久化 |
| `DSA_SECURE_COOKIES` | 关 | 置 `1` 后会话 cookie 携带 `Secure` 标志；**置于 HTTPS 反向代理之后时必须开启**，否则 cookie 明文传输 |

## 安全模型说明

- **CIDR 白名单只保护 API 接口**：静态页面与 `/api/health`（仅返回存活状态）对白名单外来源开放。这是有意设计——健康检查供容器/负载均衡探测使用；静态页不含任何数据（数据全部经鉴权 API 加载）。若需完全封锁，请在防火墙层面限制端口。
- **不支持 `X-Forwarded-For`**：来源 IP 判定始终基于 TCP 对端地址（`RemoteAddr`），不信任任何请求头，避免伪造头绕过白名单。
- 显式设置的 `DSA_PASSWORD` 长度不足 8 位时启动会打印警告（不拒绝启动）。

## 反向代理部署

支持置于 Nginx / Caddy / Traefik 等反代之后，但需注意以下事项：

1. **CIDR 白名单填代理的网段**：由于不信任 XFF 头，Dashboard 看到的来源 IP 是代理的地址。例如 Nginx 与本服务同机部署时填 `127.0.0.1/32`。
2. **必须设置 `DSA_SECURE_COOKIES=1`**：确保会话 cookie 仅经 HTTPS 传输。
3. **登录限流按代理 IP 共享**：所有经同一代理访问的用户被视为同一来源 IP，一人多次输错密码触发锁定会影响所有人。建议仅在可信网络环境（如家庭内网 + 反代到公网）下这样部署。
4. **不要在不可信网络直接暴露端口**：本服务设计为内网工具，公网直连风险自担。

```nginx
# Nginx 示例
server {
    listen 443 ssl;
    server_name dns.example.com;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        # 注意：不要依赖 X-Forwarded-For 做访问控制，本服务不读取该头
    }
}
```

## HTTP API

除 `/api/login`、`/api/health` 与静态页面外，全部接口需要登录（Bearer Token 或 HttpOnly Cookie），
且来源 IP 必须在白名单网段内：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查（公开） |
| POST | `/api/login` | 登录，返回 Token |
| GET | `/api/status` | 运行状态 + 最近测速结果 + 写入记录 |
| GET/POST | `/api/config` | 读取（凭据脱敏）/ 更新配置 |
| POST | `/api/run` | 触发一次测速+写入全部实例 |
| GET/POST/DELETE | `/api/domains[/{name}]` | 管理额外域名列表 |
| GET/POST/DELETE | `/api/instances` | 管理 AdGuard Home 实例（含连通性检测） |
| POST | `/api/test-instance` | 测试单个实例连接（不保存） |
| GET | `/api/rewrites?url=` | 列出指定实例当前的重写规则 |
| POST/GET | `/api/instances/sync[?url=]` | 实例同步（异步）：POST 启动后台任务返回 202；GET 轮询任务状态与结果 |

## 项目结构

```
main.go        守护进程入口（安全门禁、优雅退出）
config/        配置模型与持久化（原子写入、脱敏、更新语义）
adguard/       AdGuard Home 客户端（多实例、重试、原子更新）
speedtest/     测速内核封装（包级互斥，非并发安全契约显式化）
web/           Dashboard API + 内嵌前端（go:embed static）
web/ui/        React 前端源码（Vite，构建产物输出到 web/static）
task/ utils/   CloudflareSpeedTest 测速内核（上游代码，含少量健壮性修复）
ip.txt         Cloudflare IPv4 段数据
ipv6.txt       Cloudflare IPv6 段数据
```

## 前端开发

前端为 React（Vite 构建），源码在 `web/ui/`，构建产物输出到 `web/static/` 并随 Go 二进制打包：

```bash
cd web/ui
npm install
npm run dev     # 开发模式：http://localhost:5173，/api 自动代理到 http://127.0.0.1:8080
npm run build   # 构建到 web/static/，随后 go build 即打包最新前端
```

## 数据持久化

全部配置保存在 `/app/data/config.json`。宿主目录属主需与容器用户（uid 1000）匹配：

```bash
sudo chown -R 1000:1000 ./data
```

## 从 cfs-adns 升级

环境变量已更名：`CFS_*` → `DSA_*`（如 `CFS_PASSWORD` → `DSA_PASSWORD`）。
`config.json` 格式兼容，直接复用挂载卷即可。

## 致谢

- [XIU2/CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest) —— 测速内核
- [AdguardTeam/AdGuardHome](https://github.com/AdguardTeam/AdGuardHome) —— DNS 重写能力

## License

GPL-3.0（继承自上游测速内核）
