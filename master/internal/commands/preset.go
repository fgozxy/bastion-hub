package commands

// Built-in saved commands. These are seeded into saved_commands on master start
// and re-appear if deleted (builtin=1 protects them from API deletion).

// securityScript is the "安全" preset. It is a single POSIX-sh script run via the
// agent's `sh -c`. It:
//  1. installs + enables fail2ban (with a sshd jail on the new port),
//  2. generates a dedicated root ed25519 keypair and adds it to authorized_keys,
//  3. hardens sshd: Port 22022, key-only auth, root only via key,
//  4. enables a firewall that defaults to denying inbound traffic and only allows
//     the new SSH port plus whatever TCP ports are currently listening (so running
//     services keep working),
//  5. prints the private key (save it to log in on port 22022).
//
// The agent auto-collects the new /root/.ssh/nodepanel_root.pub into Credentials
// bound to this node (snapshot→diff over ~/.ssh/*.pub), so the public key lands in
// the Credentials section automatically. The agent's own connection is an outbound
// WSS dial, so an inbound default-deny firewall does not disconnect it.
const securityScript = `# ===== NodePanel 安全加固 =====
NEW_PORT=22022
KEY=/root/.ssh/nodepanel_root
echo "=== NodePanel 安全加固开始 (SSH 端口将改为 $NEW_PORT) ==="

# 包管理器探测
if command -v apt-get >/dev/null 2>&1; then PM=apt-get
elif command -v dnf >/dev/null 2>&1; then PM=dnf
elif command -v yum >/dev/null 2>&1; then PM=yum
else PM=none; fi

# [1] 安装 fail2ban
echo "[1/5] 安装 fail2ban ..."
if [ "$PM" = apt-get ]; then
  export DEBIAN_FRONTEND=noninteractive
  (apt-get update -qq && apt-get install -y -qq fail2ban) || echo "warn: fail2ban 安装失败 (可稍后手动安装)"
elif [ "$PM" = dnf ]; then dnf install -y -q fail2ban || echo "warn: fail2ban 安装失败"
elif [ "$PM" = yum ]; then yum install -y -q fail2ban || echo "warn: fail2ban 安装失败"
else echo "warn: 未识别包管理器，跳过 fail2ban 安装"; fi

# [2] 生成 root 密钥并加入 authorized_keys
echo "[2/5] 配置 root SSH 密钥 ..."
mkdir -p /root/.ssh && chmod 700 /root/.ssh
if [ ! -f "$KEY" ]; then ssh-keygen -t ed25519 -f "$KEY" -N "" -C "nodepanel-root" >/dev/null 2>&1; fi
touch /root/.ssh/authorized_keys
# 部分商家镜像的 authorized_keys 末行没有换行符，直接追加会把新公钥粘到上一行，
# 变成一条非法记录（旧 key 的 comment 吃掉新 key），导致新私钥无法登录。
if [ -s /root/.ssh/authorized_keys ] && [ -n "$(tail -c 1 /root/.ssh/authorized_keys)" ]; then
  echo >> /root/.ssh/authorized_keys
fi
if [ -f "$KEY.pub" ]; then
  grep -qF "$(cat "$KEY.pub")" /root/.ssh/authorized_keys 2>/dev/null || cat "$KEY.pub" >> /root/.ssh/authorized_keys
fi
chmod 600 /root/.ssh/authorized_keys "$KEY" 2>/dev/null

# [3] 加固 sshd：端口 22022 + 仅密钥 + root 仅密钥
echo "[3/5] 加固 sshd ..."
mkdir -p /etc/ssh/sshd_config.d
cp -n /etc/ssh/sshd_config /etc/ssh/sshd_config.nodepanel.bak 2>/dev/null || true
cat > /etc/ssh/sshd_config.d/99-nodepanel-hardening.conf <<EOF
Port $NEW_PORT
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
EOF
# 确保主配置包含 drop-in 目录
grep -qE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config.d/\*\.conf' /etc/ssh/sshd_config 2>/dev/null \
  || echo 'Include /etc/ssh/sshd_config.d/*.conf' >> /etc/ssh/sshd_config
if command -v sshd >/dev/null 2>&1; then sshd -t 2>/dev/null && echo "sshd 配置语法 OK"; fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl restart sshd 2>/dev/null || systemctl restart ssh 2>/dev/null || true
elif command -v service >/dev/null 2>&1; then
  service sshd restart 2>/dev/null || service ssh restart 2>/dev/null || true
fi

# [4] 防火墙：默认拒绝入站，仅放行当前监听端口 + 新 SSH 端口
echo "[4/5] 配置防火墙 (默认拒绝入站，放行必要端口) ..."
# 收集当前监听 TCP 端口
RAW_PORTS="$( (ss -tlnH 2>/dev/null || netstat -tln 2>/dev/null) | sed -n 's/.*[:[]\([0-9][0-9]*\)[\] ].*/\1/p' )"
NEEDED="$( (printf '%s\n%s\n' "$NEW_PORT" "$RAW_PORTS") | grep -E '^[0-9]+$' | sort -un )"
if command -v ufw >/dev/null 2>&1; then
  printf '%s\n' "$NEEDED" | while read -r p; do [ -n "$p" ] && ufw allow "$p/tcp" >/dev/null 2>&1; done
  ufw --force enable >/dev/null 2>&1 && echo "ufw 已启用 (默认拒绝入站)"
elif command -v firewall-cmd >/dev/null 2>&1; then
  systemctl enable --now firewalld >/dev/null 2>&1 || true
  printf '%s\n' "$NEEDED" | while read -r p; do [ -n "$p" ] && firewall-cmd --permanent --add-port="$p/tcp" >/dev/null 2>&1; done
  firewall-cmd --reload >/dev/null 2>&1 && echo "firewalld 已配置 (默认区域)"
elif command -v iptables >/dev/null 2>&1; then
  iptables -P INPUT ACCEPT
  iptables -F
  iptables -A INPUT -i lo -j ACCEPT
  iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  printf '%s\n' "$NEEDED" | while read -r p; do [ -n "$p" ] && iptables -A INPUT -p tcp --dport "$p" -j ACCEPT; done
  iptables -A INPUT -j DROP
  iptables -P INPUT DROP
  command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save 2>/dev/null
  command -v iptables-save >/dev/null 2>&1 && { iptables-save >/etc/iptables/rules.v4 2>/dev/null || iptables-save >/etc/sysconfig/iptables 2>/dev/null || true; }
  echo "iptables 已配置 (默认拒绝入站)"
else
  echo "warn: 未找到 ufw/firewalld/iptables，跳过防火墙"
fi

# [5] 配置 fail2ban sshd 监狱（指向新端口）
echo "[5/5] 配置 fail2ban ..."
if [ -d /etc/fail2ban ]; then
  cat > /etc/fail2ban/jail.local <<EOF
[DEFAULT]
bantime = 1h
findtime = 10m
maxretry = 5
backend = systemd

[sshd]
enabled = true
port = $NEW_PORT
EOF
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now fail2ban >/dev/null 2>&1 || true
    systemctl restart fail2ban 2>/dev/null || true
  fi
fi

echo ""
echo "=== 完成 ==="
echo "SSH: ssh -i <私钥> -p $NEW_PORT root@<节点IP>"
echo "提示：原 22 端口已不再监听；Agent 走出站 WSS，不受防火墙影响，仍可在面板管理。"
echo ""
echo "----- 新 root 公钥（已自动收录到「凭证」并绑定本节点）-----"
[ -f "$KEY.pub" ] && cat "$KEY.pub"
echo ""
echo "----- 新 root 私钥（请立即复制保存，仅此一次完整显示）-----"
[ -f "$KEY" ] && cat "$KEY"
echo "----- END -----"
echo "=== NodePanel 安全加固结束 ==="`

// builtinSavedCommands returns the built-in saved commands seeded into the store.
func builtinSavedCommands() []struct{ ID, Name, Script string } {
	return []struct{ ID, Name, Script string }{
		{"preset-security", "安全", securityScript},
	}
}
