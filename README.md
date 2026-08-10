# NodePanel

自托管的多服务器管理面板。在一台主控上管理任意数量的云主机：容器、备份与迁移、Cloudflare 隧道 / DNS、健康监控、命令执行与定时任务——节点只需出站连接，无需开放入站端口。

```bash
git clone https://github.com/fgozxy/bastion-hub.git nodepanel
cd nodepanel
cp .env.example .env   # 编辑 DOMAIN / ADMIN_USER / ADMIN_PASS
docker compose up -d --build
```

面板默认监听 `127.0.0.1:8088`。用 Nginx / NPM / Cloudflare Tunnel 把 HTTPS 反代到该端口即可。

## 功能

<!-- NODEPANEL:FEATURES:BEGIN -->
<!-- 功能板块：单一数据源为 docs/FEATURES.md；改功能请编辑该文件，推送时 README 的本段自动同步。 -->

- **仪表盘** — 节点在线 / 离线、地理分布、CPU 负载、备份历史、最近命令
- **节点** — 一条安装命令拉入 Agent（反向 WSS 穿 NAT，零入站端口）；改名、IPv4/IPv6、批量升级 Agent；主域名与入站类型（CF Tunnel / NPM）
- **容器** — 跨节点实时列表；自动标注更新类型（latest / build / local / pinned）；一键批量更新、源码 `git pull` + 重建、换镜像、扫描远端 digest
- **备份** — 容器卷 / 任意目录打包（tar.gz，分块上传）；目标：GitHub / OneDrive / SFTP / S3·MinIO；按份数 / 天数保留；restic 增量；僵尸任务自动回收
- **恢复** — 任意快照恢复到任一节点；从归档重建容器（端口重映射、网络兜底）；恢复前预检（端口 / 路径 / 镜像 / 磁盘）
- **迁移** — 跨节点无损迁移（数据 + 端口 + 域名）：备份 → 预检 → 重建 → 隧道域名改指 → 可选删源
- **Cloudflare** — 隧道创建 / 启停 / 重命名 / 删除；hostname ingress 增删改与跨隧道移动；通用 DNS 编辑器（A/AAAA/CNAME/MX/TXT…）
- **健康** — 一键装卸 Netdata（loopback，不经 Cloud）；CPU / 内存 / 磁盘 / 网络等告警，Telegram 推送
- **命令与凭据** — 多节点广播执行、实时输出；SSH 密钥上传 / 扫描 / 登录测试；内置安全加固预设
- **防火墙** — ufw / firewalld 检测、端口开关、应用配置展开
- **定时任务** — 北京时间 cron：备份（多节点 × 多目标，离线重试）与容器自动更新
- **其它** — Telegram 通知、容器异常监控、Komari 探针一键接入、GitHub 项目同步

<!-- NODEPANEL:FEATURES:END -->

## 架构

| 组件 | 说明 |
|------|------|
| **master** | 单 Go 二进制：HTTP API + WebSocket + SQLite + cron；前端 `embed` 进二进制；Docker 部署 |
| **agent** | ~5–6 MB 静态二进制，systemd 运行；反向 WSS 连主控；支持 amd64 / arm64 / 386 / armv7 |
| **web** | React + TypeScript + Vite + Zustand，light / dark / white 主题 |

协议定义见 [`shared/proto/messages.go`](shared/proto/messages.go)。完整 Docker 说明见 [`deploy/DOCKER.md`](deploy/DOCKER.md)。

## 加入节点

面板「节点 → 添加节点」生成命令，在目标主机以 root 执行：

```bash
curl -fsSL "https://<你的域名>/install.sh?token=<TOKEN>" | bash
```

Agent 会下载对应架构二进制、写入 systemd unit，并以出站 WSS 上线。

## 本地构建

```bash
./scripts/build.sh   # 前端 → embed → master → 交叉编译 4 架构 Agent → build/
```

## 技术栈

Go · chi · gorilla/websocket · modernc.org/sqlite · robfig/cron · React · TypeScript · Vite · Zustand · Docker

## License

私人项目公开源码，供学习与自用。使用前请自行评估安全与合规风险。
