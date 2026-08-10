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
