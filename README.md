# NodePanel — 多服务器管理面板

<!-- NODEPANEL:AUTO:BEGIN -->
> 📅 **最近更新**:2026-07-26 03:29:51 UTC  ·  🏷️ **Agent 版本**:2.4.6  ·  📝 **本次变更**:40 files changed, 3245 insertions(+), 953 deletions(-)

<details><summary>📦 最近提交</summary>

```text
b3e495a NodePanel project upload @ 2026-07-15 01:21:58
0b7c547 fix(gitpush): preserve FEATURES markers so README auto-syncs
11ac3b6 NodePanel project upload @ 2026-07-15 01:14:55
bbe6fdc docs: rewrite README + FEATURES to match current feature set
3d5d1d8 NodePanel project upload @ 2026-07-15 01:00:32
```

</details>
<!-- NODEPANEL:AUTO:END -->




一个用 Go 写的自托管多服务器管理面板：主控机一行命令把云服务器作为「节点」拉入（自动装轻量 Agent，反向 WSS 穿 NAT），之后在一个界面里管理容器、备份 / 恢复 / 迁移、域名 / 隧道 / DNS、健康监控、防火墙、SSH 凭据、命令执行与定时任务——全程出站连接，节点无需开放入站端口。

## ✨ 功能

<!-- NODEPANEL:FEATURES:BEGIN -->
<!-- 功能板块：单一数据源为 docs/FEATURES.md；改功能请编辑该文件，推送时 README 的本段自动同步。 -->

- **仪表盘** — 节点在线/离线、地理分布、CPU 实时负载、备份历史、最近命令，一屏全图表。
- **节点管理** — 一条命令加入（自动装 Agent，反向 WSS 穿 NAT，节点零入站端口）；国旗 + 可编辑名 + IPv4/IPv6（复制钮，IPv6 省略中段）+ 在线状态 + Agent 版本；重生成安装命令；单节点 / 批量一键升级 Agent；设主域名与入站类型（CF Tunnel / NPM 外部）。
- **容器管理** — 跨节点实时采集容器与镜像；每个容器自动标注更新类型（🟢 latest/tag 可自动 / 🟡 build 源码构建 / 🔴 local 本地 / 🔴 pinned 固定摘要）；一键「更新所有可自动」批量拉取重建（compose 优先 `docker compose up --pull always --build`，非 compose 走 pull，结果如实回报不再假成功）；源码构建支持「源码更新」（节点 `git pull` + `compose build`）；固定/本地支持「换镜像」（改写 compose `image:` 并重建）；只读「扫描更新」对比远端 manifest digest，本地构建镜像自动跳过。
- **备份** — 容器卷+配置 或 任意目录打包（tar.gz，分块上传绕过 CF 体积上限，单 S3 目标支持 agent 直传免中转）；目标：GitHub 私有库 / OneDrive / VPS(SFTP) / S3·MinIO；按容器保留份数 + 按备份保留天数；备份记录持久化稳定容器名，恢复视图按名关联、扛容器重建换 id；restic 增量备份（每容器一快照）；僵尸 running 记录自动回收。
- **恢复** — 任意快照恢复到任一节点（跨节点 DR）；从归档 `container.json` 重建容器（自动拉镜像 / 端口重映射 / 网络兜底）；恢复前预检（端口 / 路径 / 镜像 / 磁盘，端口冲突硬阻断、路径冲突告警）；流式进度 + 持久化历史；按容器查全部历史快照做时间点恢复。
- **容器迁移** — 跨节点无损迁移（数据 + 端口 + 域名）：备份源 → 预检目标 → 目标重建（端口自动重映射）→ 域名从源隧道改指目标隧道（支持改名 `a.a.com→a.b.com` 或保留主机名改指）→ 可选删源；迁移前域名计划预演 + 冲突检测；全程流式 + 持久化。
- **Cloudflare 隧道** — 任意隧道（含手建）的创建 / 启停 / 重命名 / 删除；运行时分流（panel=systemd / 手建=Docker，按 token 解析 tunnel id 匹配）；删除同步清 DNS CNAME。
- **域名（隧道 ingress）** — 聚合各隧道 hostname→源站 ingress + 实时 DNS CNAME 匹配；增删改规则、跨隧道移动 hostname（安全 add→改 DNS→删源 + 回滚）。
- **DNS 记录** — 通用 Cloudflare DNS 编辑器：任意 zone、任意记录类型（A/AAAA/CNAME/MX/TXT/SRV/CAA/NS…），proxied/TTL/priority 按类型自动约束；区别于只管隧道 ingress 的隧道/域名板块。
- **健康监控** — 一键装 / 卸 Netdata（loopback 绑定、不经 Netdata Cloud，释放低配节点内存）；按模板采集 CPU / 负载 / 内存 / swap / 磁盘空间·IO / 网络 / iowait / 进程；可编辑告警阈值（负载按核数×2 缩放），超阈 Telegram 推送（带冷却）；装 / 卸 / 图表 / 告警全面板管理。
- **命令执行** — 多选节点广播执行，实时输出流（浏览器 WebSocket）；命令生成的 SSH 公钥自动收录到凭据；常用命令保存复用；内置「安全加固」预设（fail2ban + root ed25519 + sshd 改 22022 仅密钥 + 默认拒绝防火墙放行当前端口）。
- **凭据与 SSH** — 上传 / 绑定节点 / 真·SSH 登录测试（agent 拨本机 sshd 验密钥）；多节点扫描已装公钥与本机密钥对并导入；命令后自动收录新公钥。
- **节点防火墙** — 单节点弹窗：检测 ufw/firewalld、列出公网监听端口、端口多选开 / 关、整墙总开关；展开 ufw 应用配置（如 Nginx Full→80/443）。
- **通知与异常监控** — Telegram 机器人推送（备份报告全角对齐表格、恢复、迁移、健康告警、容器异常、僵尸回收）；容器异常监控按间隔扫描，进入 exited(非0)/dead/restarting 才推（主动 exited(0) 不报），每容器恢复前只报一次。
- **定时任务** — 北京时间（UTC+8，不受容器时区影响）cron；两类：① 备份（多容器 / 多路径 / 多节点 / 多目标，离线节点 2h 内重试，按节点×目标 🟢/🟡/🔴 报告）；② 容器更新（在「设置 → 容器更新」或计划任务里勾选容器板块中的 Compose Registry 容器，定时只读扫描后仅更新所选且确认有新版本的运行中容器，自动跳过 build/local/pinned，需 agent ≥ 2.4.0）。
- **探针 Komari** — 把在线节点一键加入 Komari 监控（自动装 komari-agent），按 IPv4/名称去重匹配。
- **设置与项目同步** — 账户、Telegram、Cloudflare token（校验 Tunnel:Edit+DNS:Edit）、备份保留 / 排除、容器异常监控、Komari、公网域名；**GitHub 项目同步**：把面板源码推到仓库，README 顶部版本 / 提交信息自动刷新。
<!-- NODEPANEL:FEATURES:END -->

## 架构

- **master**（`master/cmd/master`）— 单 Go 二进制：`chi` + `gorilla/websocket` + 纯 Go SQLite（`modernc.org/sqlite`，CGO 禁用）+ `robfig/cron` + `autocert`。前端经 `embed.FS` 打进二进制。Docker 部署，监听 `127.0.0.1:8088`，公网 HTTPS 由反代转发。
- **agent**（`agent/cmd/agent`）— 单静态二进制（~5–6MB，内存 ~10–25MB），以 systemd 服务跑在被管节点，**反向 WSS** 连主控（节点零入站端口）。`/proc` 直采指标，stdlib `tar/gzip`，交叉编译 **amd64 / arm64 / 386 / arm-7** 四架构。
- **web**（`web/`）— React + TypeScript + Vite + Zustand + chart.js，暖灰极简主题（light / dark / white），移动端适配。
- 协议定义见 [`shared/proto/messages.go`](shared/proto/messages.go)。

## 构建

```bash
./scripts/build.sh        # 前端 → embed → master → 交叉编译 4 架构 Agent（输出 build/）
```

## 部署（Docker Compose）

master 以单个 Docker 容器运行；Agent 仍是部署到各被管节点的轻量二进制（经面板安装命令加入，无需容器化）。

```bash
git clone <你的仓库> nodepanel && cd nodepanel
cp .env.example .env            # 编辑 DOMAIN / ADMIN_USER / ADMIN_PASS
docker compose up -d --build
```

- 容器监听 `127.0.0.1:8088`（仅本机）；公网 HTTPS 由反代（Nginx Proxy Manager / Cloudflare 等）转发到该端口
- `.env` 的 `ADMIN_PASS` 仅在**空库首次启动**时创建管理员；已有数据库保留原账号
- 数据（SQLite / Agent 二进制 / 暂存备份）持久化在卷 `/var/lib/nodepanel`
- 镜像约 54 MB，多阶段构建：`node:20` 构建前端 → `golang:1.21` 嵌入前端并编译 master + 4 架构 Agent → `alpine:3.20` 运行时；纯 Go 静态二进制，无外部依赖
- 升级：代码更新后 `docker compose up -d --build`；`entrypoint` 自动把新版 Agent 种子化进 `assets/`，各节点在面板点「更新 Agent」即可

```bash
docker compose logs -f          # 查看日志
docker compose restart          # 重启
docker compose down             # 停止
```

> 完整说明（含从 systemd 迁移）见 [`deploy/DOCKER.md`](deploy/DOCKER.md)。

## 对外 HTTPS

容器仅绑 `127.0.0.1:8088`，由本机反代接管 80/443。以 Nginx Proxy Manager 为例，加一条代理主机：域名 → `127.0.0.1:8088`，打开 **Websockets Support**，SSL 申请 Let's Encrypt（域名走 Cloudflare 时建议 DNS Challenge）。也可直接用 Cloudflare Tunnel 暴露面板自身。

## 加入节点

面板「节点 → 添加节点」生成命令，在目标主机以 root 执行：

```bash
curl -fsSL "https://<你的域名>/install.sh?token=<TOKEN>" | bash
```

Agent 自动下载对应架构二进制、写配置 + systemd unit 并启动，以反向 WSS 上线。

## 技术栈

Go · chi · gorilla/websocket · modernc.org/sqlite · robfig/cron · autocert · React · TypeScript · Vite · Zustand · chart.js · Docker
