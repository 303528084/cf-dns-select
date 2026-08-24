# cf-dns-select 飞牛应用中心提交说明（供审核参考）

## 1. 应用概述

cf-dns-select 定时测速找出当前网络响应最快的 Cloudflare 边缘节点，
自动写入局域网内 DNS 服务实例（AdGuard Home / Technitium / dnsmasq / SmartDNS / MosDNS）
的 DNS 重写表。内置中文 Web Dashboard 与登录鉴权。
开源地址：<https://github.com/neon9809/cf-dns-select>

## 2. 为什么需要 host 网络（默认模式）

上游项目的核心功能是测速优选：

- 需要对 Cloudflare 全部 IP 段发起 TCPing / HTTPing / 下载测速；
- IPv6 测速要求容器持有**真实的宿主 IPv6 路由**，桥接 + NAT 下 IPv6 基本不可用，
  这是上游 README 明确建议 host 部署的原因。

因此本包默认使用 `network_mode: host`。

**同时提供端口映射（bridge）模式**：安装向导「网络模式」页可切换，
表单中已明确提示"端口映射无 IPv6 测速"。实现方式为 compose profiles
（`COMPOSE_PROFILES` 由随包携带的 `.env` 选择），两种模式同一时刻只存在一个容器，
容器名统一为 `cf-dns-select`。

`.env` 默认值为 host 模式且随包携带——即使生命周期回调尚未执行、
应用中心在安装流程中提前拉起 docker-project，也会得到确定可启动的配置；
用户选择由回调同步后生效，模式不一致的旧容器会被自动清理重建。

## 3. 暴露面

| 模式 | 暴露 |
| --- | --- |
| host | 宿主机上仅监听**一个** TCP 端口 = 向导所选端口（默认 8080） |
| bridge | 宿主机上仅映射一个 TCP 端口（向导所选）→ 容器内 8080 |

没有数据库、管理后台等其他监听端口。出站方向仅为测速所需：
Cloudflare 各 IP 段的 TCP 443 探测与下载测速目标 URL、AdGuard Home 等
用户自己配置的内网 DNS 实例 API。

## 4. 访问控制（为何风险可控）

1. **CIDR 白名单门禁**：`DSA_ALLOWED_CIDRS` 未显式配置时进程拒绝启动；
   安装向导强制收集（默认三组 RFC1918 内网网段）。白名单外的 API 请求全部拒绝。
2. **登录鉴权**：会话 Token（恒定时间比较）、防爆破延迟；密码可由向导设置，
   未设置时自动生成随机密码打印到日志。
3. **不信任 X-Forwarded-For**：来源判定始终基于 TCP 对端地址，无法伪造头绕过白名单。
4. 白名单外可见的仅有静态登录页与 `/api/health`（只返回存活状态，不含任何数据）——
   这是为容器健康检查保留的有意设计；如需完全封锁可在防火墙层限制该端口。
5. 敏感操作均需登录；API 回显凭据脱敏，前端全量转义防 XSS。

综上：暴露单端口 + 强制内网白名单 + 登录鉴权 + 不信任代理头，
host 网络下应用的可达性边界清晰且默认收敛于局域网。

## 5. 权限模型

- 生命周期脚本以专用包用户上下文运行（`config/privilege`: run-as=package），
  并通过 `join-groups: ["docker"]` 加入 docker 用户组：这是本应用唯一需要的
  附加权限，用于在安装/应用设置/启动时重建自身 docker-project 的容器
  （改端口、切网络模式需要重建才能生效），除此之外无特权操作、无远程执行；
- 容器内以上游镜像的非 root 用户（uid 1000）运行；
- 未声明 `data-share`、`usr-local-linker`；数据全部位于应用沙箱目录
  （etc/var），卸载时随沙箱清理。

## 5.1 镜像版本策略4

compose 引用固定版本标签 `ghcr.io/neon9809/cf-dns-select:0.3.3`
（与 manifest.version 一致，不使用 latest）。理由：

- 同一 .fpk 在任何时间安装得到完全相同的镜像，审核可复现、用户可回滚；
- 上游发布新版本时通过发布新的 .fpk（新包版本 + 新镜像标签）升级，
  CI（GitHub Actions）会强制校验 git 标签、manifest.version、镜像标签三者一致。


## 6. 关于 service_port / checkport 的语义（v1.3.0 起）

实际监听端口由安装向导动态决定（`${wizard_port}`），不再是固定值。
为避免"manifest 声明固定端口 ≠ 实际端口"的歧义，本包**不再声明
service_port**，并将 `checkport=false`（关闭对名义端口的启动前探测，
否则系统会探测一个可能并不使用的端口而误判）。

入口打开的实际端口由 `app/ui/config` 中 `${wizard_port}` 动态解析，
两者始终一致。

## 7. os_min_version = 1.1.8 的依据

桌面入口 `app/ui/config` 使用了动态变量 `${wizard_port}` 解析访问端口。
按飞牛开放文档，入口环境变量支持自系统 **V1.1.8** 起提供，
故声明最低兼容版本 1.1.8。

## 8. 数据持久化与升级

- 运行参数主副本：`${TRIM_PKGETC}/deploy.conf`、`app.env`（600 权限）；
- 业务数据：`${TRIM_PKGVAR}/data`（config.json、colo_ip_db.json）;
- 升级时主副本自动恢复到新包内的引用路径；v1.0.x 旧配置位置自动迁移；
- 「应用设置」修改向导后会重建容器使新模式/新端口立即生效。

## 9. 已知限制

- 端口映射模式下 IPv6 测速不可用（NAT 限制，已在向导中提示）；
- 文件型 DNS 实例（dnsmasq/SmartDNS/MosDNS）的规则文件目录需用户自行挂载进容器
  （上游功能边界，见项目 README）。
