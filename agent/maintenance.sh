#!/usr/bin/env bash
set -euo pipefail

PANEL_BASE_URL="${PANEL_BASE_URL:?required}"
TOKEN_FILE="${TOKEN_FILE:?required}"

if [[ ! -f "$TOKEN_FILE" ]]; then
    echo "[maintenance] Token file not found"
    exit 1
fi

NODE_TOKEN=$(cat "$TOKEN_FILE")
STATE_DIR="/var/lib/bastion-hub"
mkdir -p "$STATE_DIR"

REPORT="{}"
WARNINGS=()

# Helper: add check result to report
add_check() {
    local key="$1"
    local value="$2"
    REPORT=$(echo "$REPORT" | jq --arg k "$key" --argjson v "$value" '. + {($k): $v}')
}

# Helper: add warning
warn() {
    WARNINGS+=("$1")
    echo "[maintenance] WARNING: $1"
}

# --- 1. Disk space (all mount points) ---
DISK_CHECKS="[]"
while read -r filesystem size used avail use_percent mount; do
    [[ "$filesystem" == "Filesystem" ]] && continue
    USE_NUM=$(echo "$use_percent" | tr -d '%')
    DISK_CHECKS=$(echo "$DISK_CHECKS" | jq --arg fs "$filesystem" --arg mp "$mount" --argjson used "$USE_NUM" --argjson avail "$(echo "$avail" | sed 's/G//;s/M//;s/K//;s/T//')" '. + [{filesystem: $fs, mount: $mp, used_percent: $used, avail: $avail}]')
    if [[ "$USE_NUM" -gt 90 ]]; then
        warn "Disk usage on $mount is ${USE_NUM}%"
    fi
done < <(df -h 2>/dev/null | grep -v tmpfs | grep -v devtmpfs)
add_check "disk" "$DISK_CHECKS"

# --- 2. Memory usage ---
MEM_INFO="{}"
if command -v free &>/dev/null; then
    MEM_TOTAL=$(free -m | awk '/^Mem:/{print $2}')
    MEM_USED=$(free -m | awk '/^Mem:/{print $3}')
    MEM_AVAIL=$(free -m | awk '/^Mem:/{print $7}')
    if [[ "$MEM_TOTAL" -gt 0 ]]; then
        MEM_PERCENT=$((MEM_USED * 100 / MEM_TOTAL))
        MEM_INFO=$(jq -n --argjson total "$MEM_TOTAL" --argjson used "$MEM_USED" --argjson avail "$MEM_AVAIL" --argjson pct "$MEM_PERCENT" '{total_mb: $total, used_mb: $used, avail_mb: $avail, used_percent: $pct}')
        if [[ "$MEM_PERCENT" -gt 90 ]]; then
            warn "Memory usage is ${MEM_PERCENT}%"
        fi
    fi
fi
add_check "memory" "$MEM_INFO"

# --- 3. System load ---
LOAD_INFO="{}"
if [[ -f /proc/loadavg ]]; then
    LOAD1=$(awk '{print $1}' /proc/loadavg)
    LOAD5=$(awk '{print $2}' /proc/loadavg)
    LOAD15=$(awk '{print $3}' /proc/loadavg)
    LOAD_INFO=$(jq -n --arg l1 "$LOAD1" --arg l5 "$LOAD5" --arg l15 "$LOAD15" '{load_1min: $l1, load_5min: $l5, load_15min: $l15}')
fi
add_check "load" "$LOAD_INFO"

# --- 4. Zombie processes ---
ZOMBIE_COUNT=$(ps aux | awk '$8 ~ /^Z/ {count++} END {print count+0}')
add_check "zombie_processes" "$ZOMBIE_COUNT"
if [[ "$ZOMBIE_COUNT" -gt 10 ]]; then
    warn "Zombie processes: $ZOMBIE_COUNT"
fi

# --- 5. System updates available ---
UPDATES_AVAILABLE=0
UPDATE_COMMAND=""
if command -v apt &>/dev/null; then
    # Debian/Ubuntu
    apt list --upgradable 2>/dev/null | grep -c "upgradable" >/dev/null 2>&1 && true
    UPDATES_AVAILABLE=$(apt list --upgradable 2>/dev/null | grep -v "Listing" | wc -l)
    UPDATE_COMMAND="apt list --upgradable"
elif command -v dnf &>/dev/null; then
    # RHEL/CentOS 8+
    UPDATES_AVAILABLE=$(dnf check-update --quiet 2>/dev/null | grep -v "^$" | wc -l)
    UPDATE_COMMAND="dnf check-update"
elif command -v yum &>/dev/null; then
    # RHEL/CentOS 7
    UPDATES_AVAILABLE=$(yum check-update --quiet 2>/dev/null | grep -v "^$" | wc -l)
    UPDATE_COMMAND="yum check-update"
elif command -v apk &>/dev/null; then
    # Alpine
    UPDATES_AVAILABLE=$(apk list --upgradable 2>/dev/null | wc -l)
    UPDATE_COMMAND="apk list --upgradable"
fi
add_check "updates_available" "$UPDATES_AVAILABLE"
if [[ "$UPDATES_AVAILABLE" -gt 0 ]]; then
    warn "System updates available: $UPDATES_AVAILABLE"
fi

# --- 6. Permission checks ---
PERM_CHECKS="[]"

# /etc/ssh permissions
if [[ -d /etc/ssh ]]; then
    SSH_PERM=$(stat -c "%a" /etc/ssh 2>/dev/null || echo "unknown")
    PERM_CHECKS=$(echo "$PERM_CHECKS" | jq --arg path "/etc/ssh" --arg perm "$SSH_PERM" '. + [{path: $path, mode: $perm, ok: ($perm == "755" or $perm == "700")}]')
    if [[ "$SSH_PERM" != "755" && "$SSH_PERM" != "700" ]]; then
        warn "/etc/ssh permissions are $SSH_PERM (expected 755 or 700)"
    fi
fi

# /root/.ssh permissions
if [[ -d /root/.ssh ]]; then
    ROOTSSH_PERM=$(stat -c "%a" /root/.ssh 2>/dev/null || echo "unknown")
    PERM_CHECKS=$(echo "$PERM_CHECKS" | jq --arg path "/root/.ssh" --arg perm "$ROOTSSH_PERM" '. + [{path: $path, mode: $perm, ok: ($perm == "700")}]')
    if [[ "$ROOTSSH_PERM" != "700" ]]; then
        warn "/root/.ssh permissions are $ROOTSSH_PERM (expected 700)"
    fi
fi

# sudoers file
if [[ -f /etc/sudoers ]]; then
    SUDOERS_PERM=$(stat -c "%a" /etc/sudoers 2>/dev/null || echo "unknown")
    PERM_CHECKS=$(echo "$PERM_CHECKS" | jq --arg path "/etc/sudoers" --arg perm "$SUDOERS_PERM" '. + [{path: $path, mode: $perm, ok: ($perm == "440")}]')
    if [[ "$SUDOERS_PERM" != "440" ]]; then
        warn "/etc/sudoers permissions are $SUDOERS_PERM (expected 440)"
    fi
fi

# shadow file
if [[ -f /etc/shadow ]]; then
    SHADOW_PERM=$(stat -c "%a" /etc/shadow 2>/dev/null || echo "unknown")
    PERM_CHECKS=$(echo "$PERM_CHECKS" | jq --arg path "/etc/shadow" --arg perm "$SHADOW_PERM" '. + [{path: $path, mode: $perm, ok: ($perm == "640" or $perm == "000")}]')
fi

add_check "permissions" "$PERM_CHECKS"

# --- 7. SSH key expiration check ---
SSH_KEY_CHECKS="[]"
if [[ -d /root/.ssh ]]; then
    for keyfile in /root/.ssh/id_* /root/.ssh/authorized_keys; do
        [[ -f "$keyfile" ]] || continue
        KEY_AGE_DAYS=$(( ($(date +%s) - $(stat -c %Y "$keyfile" 2>/dev/null || echo 0)) / 86400 ))
        SSH_KEY_CHECKS=$(echo "$SSH_KEY_CHECKS" | jq --arg path "$keyfile" --argjson age "$KEY_AGE_DAYS" '. + [{path: $path, age_days: $age}]')
        if [[ "$KEY_AGE_DAYS" -gt 365 ]]; then
            warn "SSH key $keyfile is older than 1 year (${KEY_AGE_DAYS} days)"
        fi
    done
fi
add_check "ssh_keys" "$SSH_KEY_CHECKS"

# --- 8. Agent script checksum (simplified) ---
AGENT_SCRIPT="/usr/local/bin/bastion-agent.sh"
if [[ -f "$AGENT_SCRIPT" ]]; then
    AGENT_SIZE=$(stat -c "%s" "$AGENT_SCRIPT" 2>/dev/null || echo 0)
    add_check "agent_script_size" "$AGENT_SIZE"
fi

# Build final payload
WARNINGS_JSON=$(printf '%s\n' "${WARNINGS[@]}" | jq -R . | jq -s .)
PAYLOAD=$(jq -n \
    --argjson report "$REPORT" \
    --argjson warnings "$WARNINGS_JSON" \
    --arg ts "$(date -Iseconds)" \
    '{report: $report, warnings: $warnings, checked_at: $ts}')

echo "[maintenance] Checks complete. Warnings: ${#WARNINGS[@]}"

# Report to panel
curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/maintenance" \
    -H "Authorization: Bearer $NODE_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" || {
    echo "[maintenance] Failed to report to panel"
    exit 0
}

echo "[maintenance] Reported to panel at $(date -Iseconds)"
