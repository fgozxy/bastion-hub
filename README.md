# Bastion Hub

集中式 SSH 堡垒机与节点管理平台。通过 Web 界面管理多台云服务器的 SSH 策略、访问凭证、Docker Compose 项目，并支持 Agent 自更新与策略收敛。

---

## 功能特性

- **节点管理**：注册、心跳监控、IP 地址自动采集
- **SSH 策略化**：支持 enforce / report 两种模式，统一管理 sshd_config、防火墙(ufw)、Docker(Watchtower)、Agent 自更新
- **凭证管理**：SSH 密钥对生成、自动下发到节点 authorized_keys
- **Docker 管理**：采集 Compose 项目、批量更新镜像
- **Agent 自更新**：策略控制 Agent 脚本版本，节点自动从主控拉取并校验 sha256
- **维护巡检**：磁盘、内存、负载、僵尸进程、系统更新、权限检查
- **Cloudflare IP List 同步**（可选）
- **GitHub 备份**：批量备份节点配置到 GitHub 仓库

---

## 快速部署

### 1. 克隆仓库

```bash
git clone https://github.com/fgozxy/bastion-hub.git
cd bastion-hub
```

### 2. 创建环境配置文件

```bash
cp .env.example .env.bastion
```

编辑 `.env.bastion`：

```env
APP_ENV=production
DATABASE_URL=/data/bastion.db
ADMIN_USERNAME=admin
ADMIN_PASSWORD_HASH=$2b$12$...  # bcrypt 哈希后的密码
PANEL_BASE_URL=https://your-domain.com
```

> 生成 bcrypt 密码哈希：
> ```bash
> python3 -c "from passlib.hash import bcrypt; print(bcrypt.hash('你的密码'))"
> ```

### 3. 启动服务

```bash
docker compose up -d
```

服务默认监听 `127.0.0.1:8080`，建议配合 **Nginx Proxy Manager** 或 Nginx 反向代理暴露到公网。

### 4. 初始化 SSH 密钥

```bash
mkdir -p data/ssh
ssh-keygen -t ed25519 -f data/ssh/bastion-hub -N ""
```

---

## 目录结构

```
bastion-hub/
├── app/                    # FastAPI 主应用
│   ├── api/               # API 路由
│   ├── core/              # 配置、数据库、安全
│   ├── models/            # 数据仓库
│   ├── services/          # 业务逻辑
│   ├── templates/         # Jinja2 前端模板
│   └── workers/           # 后台任务
├── agent/                 # 节点端脚本
│   ├── bootstrap.sh       # 节点初始化
│   ├── agent.sh           # 主 Agent（心跳、策略、自更新）
│   ├── policy-apply.sh    # 策略独立应用
│   └── maintenance.sh     # 维护巡检
├── data/                  # 数据持久化（SQLite、SSH 密钥、Token）
├── docker-compose.yml
├── Dockerfile
└── requirements.txt
```

---

## 初始化节点

在目标节点上执行：

```bash
curl -fsSL https://your-domain.com/assets/bootstrap.sh | \
  BOOTSTRAP_PANEL_BASE_URL=https://your-domain.com \
  BOOTSTRAP_ENROLL_TOKEN=<注册令牌> \
  BOOTSTRAP_HOSTNAME=node-01 \
  bash
```

注册令牌在 Web 界面的 **初始化节点** 页面生成。

---

## 反向代理配置示例

如果使用 Nginx Proxy Manager：

- **Scheme**: `http`
- **Forward Hostname / IP**: `bastion-hub-web`
- **Forward Port**: `8080`
- 开启 **WebSockets Support**
- 开启 **Block Common Exploits**

如果使用纯 Nginx：

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

---

## 环境变量说明

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `APP_ENV` | 运行环境 | `production` |
| `DATABASE_URL` | SQLite 数据库路径 | `/data/bastion.db` |
| `ADMIN_USERNAME` | 管理员用户名 | `admin` |
| `ADMIN_PASSWORD_HASH` | 管理员密码 bcrypt 哈希 | （必填） |
| `PANEL_BASE_URL` | 面板公网地址 | `http://localhost:8080` |
| `SSH_KEY_PATH` | SSH 私钥路径 | `/data/ssh/bastion-hub` |

---

## 技术栈

- **Backend**: FastAPI 0.111 + Uvicorn + SQLite
- **Frontend**: Jinja2 模板 + 原生 JavaScript
- **Agent**: Bash + jq + curl
- **Container**: Docker + Docker Compose

---

## License

MIT
