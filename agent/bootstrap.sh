#!/usr/bin/env bash
set -euo pipefail

: "${BOOTSTRAP_PANEL_BASE_URL:?required}"
: "${BOOTSTRAP_ENROLL_TOKEN:?required}"
: "${BOOTSTRAP_HOSTNAME:?required}"
: "${BOOTSTRAP_ROLE:=worker}"
: "${BOOTSTRAP_ENV:=prod}"
: "${BOOTSTRAP_PROFILE:=minimal,docker}"
: "${BOOTSTRAP_SSH_PORT:=22}"

PANEL_BASE_URL="${BOOTSTRAP_PANEL_BASE_URL%/}"
LOG_FILE="/var/log/bastion-bootstrap.log"
AGENT_DIR="/opt/bastion-agent"
TOKEN_FILE="/var/lib/bastion-hub/node.token"

echo "[bootstrap] Starting bootstrap at $(date -Iseconds)" | tee -a "$LOG_FILE"

# 1. Set hostname
hostnamectl set-hostname "$BOOTSTRAP_HOSTNAME"
if ! grep -q "$BOOTSTRAP_HOSTNAME" /etc/hosts; then
    echo "127.0.1.1 $BOOTSTRAP_HOSTNAME" >> /etc/hosts
fi
echo "[bootstrap] Hostname set to $BOOTSTRAP_HOSTNAME" | tee -a "$LOG_FILE"

# 2. Install minimal packages
echo "[bootstrap] Installing base packages..." | tee -a "$LOG_FILE"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    curl ca-certificates jq openssh-client openssh-server iproute2 python3 python3-pip \
    git rsync net-tools 2>&1 | tee -a "$LOG_FILE"

# 3. Install Docker if requested
if [[ "$BOOTSTRAP_PROFILE" == *"docker"* ]]; then
    echo "[bootstrap] Installing Docker..." | tee -a "$LOG_FILE"
    if ! command -v docker &>/dev/null; then
        curl -fsSL https://get.docker.com | bash 2>&1 | tee -a "$LOG_FILE"
        systemctl enable docker || true
        systemctl start docker || true
    fi
fi

# 4. Create directories
mkdir -p "$AGENT_DIR" /var/lib/bastion-hub /etc/bastion-hub/ssh /root/.ssh
chmod 700 /root/.ssh

# 4.1 Install bastion-hub SSH public key for remote management
PUBKEY=$(curl -fsSL "$PANEL_BASE_URL/assets/bastion-hub.pub" 2>/dev/null || true)
if [[ -n "$PUBKEY" ]]; then
    if ! grep -qF "$PUBKEY" /root/.ssh/authorized_keys 2>/dev/null; then
        echo "$PUBKEY" >> /root/.ssh/authorized_keys
        chmod 600 /root/.ssh/authorized_keys
        echo "[bootstrap] Installed bastion-hub SSH key" | tee -a "$LOG_FILE"
    fi
fi

# 5. Download agent scripts
for script in agent.sh policy-apply.sh maintenance.sh; do
    curl -fsSL "$PANEL_BASE_URL/assets/$script" -o "$AGENT_DIR/$script"
    chmod +x "$AGENT_DIR/$script"
    echo "[bootstrap] Downloaded $script" | tee -a "$LOG_FILE"
done

# 6. Collect IP addresses
COLLECT_IPS() {
    local ips="[]"
    # IPv4 public (via external check fallback)
    local pub4
    pub4=$(curl -fsSL -4 --max-time 10 https://icanhazip.com 2>/dev/null || true)
    if [[ -n "$pub4" ]]; then
        ips=$(echo "$ips" | jq ". + [{family:\"ipv4\",scope:\"public\",address:\"$pub4\",source:\"external_check\"}]")
    fi
    # IPv6 public
    local pub6
    pub6=$(curl -fsSL -6 --max-time 10 https://icanhazip.com 2>/dev/null || true)
    if [[ -n "$pub6" ]]; then
        ips=$(echo "$ips" | jq ". + [{family:\"ipv6\",scope:\"public\",address:\"$pub6\",source:\"external_check\"}]")
    fi
    # Local addresses from ip addr
    while read -r family scope addr iface; do
        [[ -z "$addr" ]] && continue
        # Skip virtual/Docker interfaces and link-local addresses
        [[ "$iface" =~ ^(lo|docker0|br-|veth) ]] && continue
        [[ "$scope" == "link_local" ]] && continue
        # Correct scope for RFC1918 / private addresses
        if [[ "$scope" == "public" ]]; then
            if [[ "$family" == "ipv4" && "$addr" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|192\.168\.|127\.|169\.254\.|100\.6[4-9]\.|100\.[7-9][0-9]\.|100\.1[0-1][0-9]\.|100\.12[0-7]\.) ]]; then
                scope="private"
            elif [[ "$family" == "ipv6" && "$addr" =~ ^(fc|fd|fe80:|::1|fe[c-f]) ]]; then
                scope="private"
            fi
        fi
        ips=$(echo "$ips" | jq ". + [{family:\"$family\",scope:\"$scope\",address:\"$addr\",source:\"agent\",interface:\"$iface\"}]")
    done < <(ip -json addr show 2>/dev/null | jq -r '
        [.[] | .ifname as $iface | .addr_info[] |
        select(.scope != "host") |
        {family: (if .family == "inet" then "ipv4" else "ipv6" end),
         scope: (if .scope == "global" then "public" elif .scope == "link" then "link_local" else "private" end),
         addr: .local,
         iface: $iface}] |
        .[] | "\(.family) \(.scope) \(.addr) \(.iface)"
    ')
    echo "$ips"
}

IP_JSON=$(COLLECT_IPS)
echo "[bootstrap] Collected IPs: $IP_JSON" | tee -a "$LOG_FILE"

# 7. Register with panel
REGISTER_PAYLOAD=$(jq -n \
    --arg token "$BOOTSTRAP_ENROLL_TOKEN" \
    --arg hostname "$BOOTSTRAP_HOSTNAME" \
    --arg role "$BOOTSTRAP_ROLE" \
    --arg env "$BOOTSTRAP_ENV" \
    --argjson addresses "$IP_JSON" \
    --argjson ssh_port "$BOOTSTRAP_SSH_PORT" \
    '{enrollment_token:$token, hostname:$hostname, role:$role, env:$env, addresses:$addresses, ssh_port:$ssh_port, agent_version:"0.1.0"}')

echo "[bootstrap] Registering..." | tee -a "$LOG_FILE"
REGISTER_RESP=$(curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/register" \
    -H "Content-Type: application/json" \
    -d "$REGISTER_PAYLOAD" 2>&1) || {
    echo "[bootstrap] Registration failed: $REGISTER_RESP" | tee -a "$LOG_FILE"
    exit 1
}

NODE_TOKEN=$(echo "$REGISTER_RESP" | jq -r '.node_token')
NODE_UUID=$(echo "$REGISTER_RESP" | jq -r '.node_uuid')
echo "$NODE_TOKEN" > "$TOKEN_FILE"
chmod 600 "$TOKEN_FILE"
echo "[bootstrap] Registered as $NODE_UUID" | tee -a "$LOG_FILE"

# 8. Install systemd units
cat > /etc/systemd/system/bastion-agent.service <<EOF
[Unit]
Description=Bastion Hub Agent
After=network.target

[Service]
Type=oneshot
ExecStart=$AGENT_DIR/agent.sh
Environment=PANEL_BASE_URL=$PANEL_BASE_URL
Environment=TOKEN_FILE=$TOKEN_FILE
EOF

cat > /etc/systemd/system/bastion-agent.timer <<EOF
[Unit]
Description=Bastion Hub Agent Timer

[Timer]
OnBootSec=1min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
EOF

cat > /etc/systemd/system/bastion-policy-sync.service <<EOF
[Unit]
Description=Bastion Hub Policy Sync
After=network.target

[Service]
Type=oneshot
ExecStart=$AGENT_DIR/policy-apply.sh
Environment=PANEL_BASE_URL=$PANEL_BASE_URL
Environment=TOKEN_FILE=$TOKEN_FILE
EOF

cat > /etc/systemd/system/bastion-policy-sync.timer <<EOF
[Unit]
Description=Bastion Hub Policy Sync Timer

[Timer]
OnBootSec=5min
OnUnitActiveSec=1h

[Install]
WantedBy=timers.target
EOF

cat > /etc/systemd/system/bastion-maintenance.service <<EOF
[Unit]
Description=Bastion Hub Maintenance
After=network.target

[Service]
Type=oneshot
ExecStart=$AGENT_DIR/maintenance.sh
Environment=PANEL_BASE_URL=$PANEL_BASE_URL
Environment=TOKEN_FILE=$TOKEN_FILE
EOF

cat > /etc/systemd/system/bastion-maintenance.timer <<EOF
[Unit]
Description=Bastion Hub Maintenance Timer

[Timer]
OnBootSec=10min
OnUnitActiveSec=6h

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable bastion-agent.timer
systemctl start bastion-agent.timer
systemctl enable bastion-policy-sync.timer
systemctl start bastion-policy-sync.timer
systemctl enable bastion-maintenance.timer
systemctl start bastion-maintenance.timer

echo "[bootstrap] Bootstrap complete at $(date -Iseconds)" | tee -a "$LOG_FILE"
