# Docker 部署

整个控制面（master）以一个 Docker 容器运行，一行 `docker compose up -d` 即可部署。
Agent 仍是部署到各被管节点上的轻量二进制（通过面板安装命令加入），无需容器化。

## 快速开始

```bash
git clone <你的仓库> nodepanel && cd nodepanel
cp .env.example .env            # 编辑：DOMAIN / ADMIN_USER / ADMIN_PASS
docker compose up -d --build
```

容器监听 `127.0.0.1:8088`（仅本机）。公网 HTTPS 由你的反代（Nginx Proxy Manager /
Cloudflare 等）转发到 `127.0.0.1:8088`。

- `.env` 的 `ADMIN_PASS` 只在**空数据库**首次启动时用于创建管理员；已有数据库保留原账号。
- 数据（SQLite、Agent 二进制、暂存备份）持久化在卷 `/var/lib/nodepanel`
  （compose 默认挂载宿主 `/var/lib/nodepanel`，可改）。

## 架构

```
多阶段 Dockerfile:
  node:20-alpine   -> 构建前端 (npm run build)
  golang:1.21      -> 嵌入前端 + 编译 master + 交叉编译 4 架构 Agent
  alpine:3.20      -> 运行时（master + 内置 Agent 二进制 + entrypoint）
```

`deploy/docker-entrypoint.sh` 首次/升级时把镜像内自带的 4 个架构 Agent 二进制
种子化进数据卷的 `assets/` 目录（节点通过面板 `/dl/` 拉取，即「更新 Agent」）。

## 常用命令

```bash
docker compose logs -f          # 查看日志
docker compose restart          # 重启
docker compose up -d --build    # 代码更新后重新构建并启动
docker compose down             # 停止
```

镜像约 54 MB，纯 Go 静态二进制 + alpine，无外部依赖。

## 从旧版（systemd）迁移

1. 停止旧 master：`systemctl stop nodepanel-master && systemctl disable nodepanel-master`
2. `docker compose up -d --build`（挂载同一个 `/var/lib/nodepanel`，数据无缝保留）
3. 反代继续指向 `127.0.0.1:8088`，无需改动。

## 节点（Agent）

节点加入方式不变：面板「节点」页生成安装命令，在被管主机以 root 执行：

```bash
curl -fsSL "https://<域名>/install.sh?token=<TOKEN>" | bash
```

Agent 以 systemd 服务跑在节点上，通过出站 WSS 连面板（天然穿 NAT，无需节点开放入站端口）。
