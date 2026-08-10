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
