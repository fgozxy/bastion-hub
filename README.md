# NodePanel

自托管的多服务器管理面板。在一台主控上管理任意数量的云主机：容器、备份与恢复、健康监控、命令执行与定时任务——节点只需出站连接，无需开放入站端口。

## 功能

<!-- NODEPANEL:FEATURES:BEGIN -->
<!-- 功能板块：单一数据源为 docs/FEATURES.md；改功能请编辑该文件，推送时 README 的本段自动同步。 -->

- **仪表盘** — 节点在线 / 离线、地理分布、CPU 负载、备份历史、最近命令
- **节点** — 一条安装命令拉入 Agent（反向 WSS 穿 NAT，零入站端口）；改名、IPv4/IPv6、批量升级 Agent；主域名与入站类型（CF Tunnel / NPM）
- **容器** — 跨节点实时列表；自动标注更新类型（latest / build / local / pinned）；一键批量更新、源码 `git pull` + 重建、换镜像、扫描远端 digest
- **备份** — 容器卷 / 任意目录打包（tar.gz，分块上传）；目标：GitHub / OneDrive / SFTP / S3·MinIO；按份数 / 天数保留；restic 增量；僵尸任务自动回收
- **恢复** — 任意快照恢复到任一节点；从归档重建容器（端口重映射、网络兜底）；恢复前预检（端口 / 路径 / 镜像 / 磁盘）
- **健康** — 一键装卸 Netdata（loopback，不经 Cloud）；CPU / 内存 / 磁盘 / 网络等告警，Telegram 推送
- **命令与凭据** — 多节点广播执行、实时输出；SSH 密钥上传 / 扫描 / 登录测试；内置安全加固预设
- **防火墙** — ufw / firewalld 检测、端口开关、应用配置展开；SSH 跳板来源 IP/CIDR 白名单，多节点全选与在线热更新
- **定时任务** — 北京时间 cron：备份（多节点 × 多目标，离线重试）与容器自动更新
- **其它** — Telegram 通知、容器异常监控、GitHub 项目同步

<!-- NODEPANEL:FEATURES:END -->

## 架构

| 组件 | 说明 |
|------|------|
| **master** | 单 Go 二进制：HTTP API + WebSocket + SQLite + cron；前端 `embed` 进二进制 |
| **agent** | ~5–6 MB 静态二进制，systemd 运行；反向 WSS 连主控；支持 amd64 / arm64 / 386 / armv7 |
| **web** | React + TypeScript + Vite + Zustand，light / dark / white 主题 |

协议定义见 [`shared/proto/messages.go`](shared/proto/messages.go)。

---

## 部署

推荐用 **Docker Compose** 跑 master；被管节点上的 Agent 仍是轻量二进制（面板一键安装），无需容器化。

### 环境要求

| 项 | 要求 |
|----|------|
| 系统 | Linux（amd64 / arm64） |
| 软件 | Docker + Docker Compose v2 |
| 网络 | 域名解析到本机；反向代理（如 NPM）需支持 **WebSocket** |
| 磁盘 | 建议 ≥ 5 GB（备份暂存会占用空间） |

### 方式一：Docker Compose（推荐）

```bash
git clone https://github.com/fgozxy/bastion-hub.git nodepanel
cd nodepanel
cp .env.example .env
```

编辑 `.env`：

```bash
DOMAIN=panel.example.com   # 面板对外域名
ADMIN_USER=admin
ADMIN_PASS=change-me       # 仅空库首次启动生效；已有库保留原账号
```

启动：

```bash
docker compose up -d --build
```

| 项 | 说明 |
|----|------|
| 监听 | `127.0.0.1:8088`（仅本机，不直接暴露公网） |
| 数据 | `/var/lib/nodepanel`（SQLite、Agent 二进制、暂存备份） |
| 镜像 | 多阶段构建约 54 MB：`node` 构建前端 → `golang` 编译 → `alpine` 运行 |
| Agent | 容器启动时自动把 4 架构 Agent 种子化到数据卷 `assets/` |

常用命令：

```bash
docker compose logs -f          # 日志
docker compose restart          # 重启
docker compose up -d --build    # 拉代码后重建升级
docker compose down             # 停止
```

更多细节见 [`deploy/DOCKER.md`](deploy/DOCKER.md)。

### 方式二：二进制 + systemd

适合不想跑 Docker 的环境。

```bash
# 需要本机有 Go 1.21+、Node 20+
./scripts/build.sh
# 产出：build/nodepanel-master、build/nodepanel-agent-linux-*

sudo install -m 755 build/nodepanel-master /usr/local/bin/nodepanel-master
sudo mkdir -p /var/lib/nodepanel/assets
sudo cp build/nodepanel-agent-linux-* /var/lib/nodepanel/assets/

# 编辑域名后安装 unit
sudo sed -i 's/panel.example.com/你的域名/' deploy/nodepanel-master.service
sudo cp deploy/nodepanel-master.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nodepanel-master
```

master 默认监听本机；同样需要反代提供 HTTPS。首次可用命令行参数种子化管理员：

```text
--admin-user=admin --admin-pass=...
```

（仅空库生效。）

### 对外 HTTPS

容器 / 二进制只绑本机 `8088`，由反代接管 80/443。任选其一：

**Nginx Proxy Manager**

1. 新增 Proxy Host：域名 → `127.0.0.1:8088`
2. 打开 **Websockets Support**
3. SSL → Let's Encrypt（域名在 Cloudflare 时建议 DNS Challenge）

**裸 Nginx 示例**

```nginx
server {
    listen 443 ssl http2;
    server_name panel.example.com;

    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8088;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }
}
```

### 加入节点

1. 浏览器打开面板，用 `.env` 中的账号登录
2. 「节点 → 添加节点」复制安装命令
3. 在目标主机以 **root** 执行：

```bash
curl -fsSL "https://<你的域名>/install.sh?token=<TOKEN>" | bash
```

Agent 会下载对应架构二进制、写入 systemd unit，并以**出站 WSS** 上线（穿 NAT，节点无需开放入站端口）。

### 升级

```bash
cd nodepanel
git pull
docker compose up -d --build
```

entrypoint 会刷新 `assets/` 里的 Agent；各节点在面板点「更新 Agent」即可滚动升级。

从旧版 systemd master 迁到 Docker：先 `systemctl stop/disable nodepanel-master`，再 `docker compose up -d --build`（挂载同一 `/var/lib/nodepanel`，数据保留）。

### 本地开发构建

```bash
./scripts/build.sh   # 前端 → embed → master → 交叉编译 4 架构 Agent → build/
```

---

## 技术栈

Go · chi · gorilla/websocket · modernc.org/sqlite · robfig/cron · React · TypeScript · Vite · Zustand · Docker

## License

[MIT](LICENSE)
