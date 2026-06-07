#!/usr/bin/env bash
set -euo pipefail

PANEL_BASE_URL="${PANEL_BASE_URL:?required}"
TOKEN_FILE="${TOKEN_FILE:?required}"
STATE_DIR="/var/lib/bastion-hub"

if [[ ! -f "$TOKEN_FILE" ]]; then
    echo "[policy] Token file not found"
    exit 1
fi

NODE_TOKEN=$(cat "$TOKEN_FILE")
POLICY_REV_FILE="$STATE_DIR/applied_policy_revision"

APPLIED_POLICY_REVISION=0
if [[ -f "$POLICY_REV_FILE" ]]; then
    APPLIED_POLICY_REVISION=$(cat "$POLICY_REV_FILE")
fi

# Fetch policy from panel
POLICY=$(curl -fsSL -H "Authorization: Bearer $NODE_TOKEN" "$PANEL_BASE_URL/api/agent/policy" 2>/dev/null || true)
if [[ -z "$POLICY" ]]; then
    echo "[policy] No policy returned"
    exit 0
fi

REVISION=$(echo "$POLICY" | jq -r '.revision // 0')
MODE=$(echo "$POLICY" | jq -r '.mode // "report"')

# Self-update from agent_config before applying policy
AGENT_CONFIG=$(echo "$POLICY" | jq -r '.agent_config // {}')
AUTO_UPDATE=$(echo "$AGENT_CONFIG" | jq -r '.auto_update // false')
if [[ "$AUTO_UPDATE" == "true" ]]; then
    SCRIPTS=$(echo "$AGENT_CONFIG" | jq -r '.scripts // {}')
    if [[ "$SCRIPTS" != "{}" && "$SCRIPTS" != "null" ]]; then
        SCRIPT_DIR=$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")")
        while IFS= read -r filename; do
            [[ -z "$filename" ]] && continue
            expected=$(echo "$SCRIPTS" | jq -r --arg f "$filename" '.[$f] // empty')
            [[ -z "$expected" ]] && continue
            if [[ "$expected" == sha256:* ]]; then
                expected="${expected#sha256:}"
            fi
            target_file="$SCRIPT_DIR/$filename"
            current_script=""
            if [[ "$filename" == "policy-apply.sh" ]]; then
                current_script=$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")
            fi
            local_sha=""
            if [[ -f "$target_file" ]]; then
                local_sha=$(sha256sum "$target_file" 2>/dev/null | awk '{print $1}')
            fi
            if [[ "$local_sha" == "$expected" ]]; then
                continue
            fi
            tmp_file="${target_file}.tmp.$$"
            if curl -fsSL -H "Authorization: Bearer $NODE_TOKEN" \
                "$PANEL_BASE_URL/api/agent/assets/$filename" -o "$tmp_file" 2>/dev/null; then
                download_sha=$(sha256sum "$tmp_file" 2>/dev/null | awk '{print $1}')
                if [[ "$download_sha" == "$expected" ]]; then
                    chmod +x "$tmp_file"
                    mv "$tmp_file" "$target_file"
                    echo "[policy] Updated $filename ($local_sha -> $download_sha)"
                    if [[ -n "$current_script" && "$current_script" != "$target_file" && -f "$current_script" ]]; then
                        cp "$target_file" "$current_script"
                        echo "[policy] Also updated running script: $current_script"
                    fi
                else
                    echo "[policy] Checksum mismatch for $filename: expected $expected, got $download_sha"
                    rm -f "$tmp_file"
                fi
            else
                echo "[policy] Failed to download $filename"
                rm -f "$tmp_file"
            fi
        done < <(echo "$SCRIPTS" | jq -r 'keys[]')
    fi
fi

if [[ "$MODE" == "report" ]]; then
    echo "[policy] Report mode: no changes applied"
    curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/policy-result" \
        -H "Authorization: Bearer $NODE_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"applied_revision\":$REVISION,\"mode\":\"report\"}" || true
    exit 0
fi

if [[ "$MODE" != "enforce" || "$REVISION" -le "$APPLIED_POLICY_REVISION" ]]; then
    echo "[policy] Already applied revision $APPLIED_POLICY_REVISION (desired $REVISION)"
    exit 0
fi

echo "[policy] Desired revision $REVISION mode=$MODE"

POLICY_ERROR=""
NEW_POLICY_REVISION="$APPLIED_POLICY_REVISION"

# === SSHD ===
if [[ -f /etc/ssh/sshd_config && ! -f /etc/ssh/sshd_config.bastion-backup ]]; then
    cp /etc/ssh/sshd_config /etc/ssh/sshd_config.bastion-backup
fi

BEFORE_HASH=$(md5sum /etc/ssh/sshd_config 2>/dev/null | awk '{print $1}' || echo "")

# Remove old managed block if exists
if grep -q "^# === Bastion Hub managed block ===" /etc/ssh/sshd_config 2>/dev/null; then
    sed -i '/^# === Bastion Hub managed block ===/,/^# === End Bastion Hub managed block ===/d' /etc/ssh/sshd_config
fi

# Build managed block with base enforce config
{
    echo ""
    echo "# === Bastion Hub managed block ==="
    echo "# DO NOT EDIT - managed by bastion-hub policy"
    echo "PasswordAuthentication no"
    echo "KbdInteractiveAuthentication no"
    echo "PubkeyAuthentication yes"
    echo "PermitRootLogin prohibit-password"
    echo "AuthenticationMethods publickey"

    # SSH CA
    CA_KEY=$(echo "$POLICY" | jq -r '.trusted_user_ca_public_key // empty')
    if [[ -n "$CA_KEY" ]]; then
        mkdir -p /etc/bastion-hub/ssh
        printf '%s\n' "$CA_KEY" > /etc/bastion-hub/ssh/user_ca.pub
        chmod 644 /etc/bastion-hub/ssh/user_ca.pub
        echo "TrustedUserCAKeys /etc/bastion-hub/ssh/user_ca.pub"
    fi

    # Extra sshd config from policy
    SSHD_CONFIG=$(echo "$POLICY" | jq -r '.sshd_config // {}')
    if [[ "$SSHD_CONFIG" != "{}" && "$SSHD_CONFIG" != "null" ]]; then
        echo "$SSHD_CONFIG" | jq -r 'to_entries[] | "\(.key) \(.value)"' 2>/dev/null || true
    fi

    echo "# === End Bastion Hub managed block ==="
} >> /etc/ssh/sshd_config

AFTER_HASH=$(md5sum /etc/ssh/sshd_config 2>/dev/null | awk '{print $1}' || echo "")

if [[ "$BEFORE_HASH" != "$AFTER_HASH" ]]; then
    if sshd -t >/dev/null 2>&1; then
        systemctl restart sshd >/dev/null 2>&1 || service ssh restart >/dev/null 2>&1 || true
        date +%s > "$STATE_DIR/ssh_policy_applied_at"
        echo "[policy] sshd reloaded"
    else
        POLICY_ERROR="sshd_config syntax invalid after applying policy"
        sed -i '/^# === Bastion Hub managed block ===/,/^# === End Bastion Hub managed block ===/d' /etc/ssh/sshd_config
        if [[ -f /etc/ssh/sshd_config.bastion-backup ]]; then
            cp /etc/ssh/sshd_config.bastion-backup /etc/ssh/sshd_config
        fi
        echo "[policy] ERROR: $POLICY_ERROR"
    fi
fi

# === Firewall ===
if [[ -z "$POLICY_ERROR" ]]; then
    FIREWALL_CONFIG=$(echo "$POLICY" | jq -r '.firewall_config // {}')
    PROVIDER=$(echo "$FIREWALL_CONFIG" | jq -r '.provider // "ufw"')
    if [[ "$PROVIDER" == "ufw" ]]; then
        if ! command -v ufw &>/dev/null; then
            apt-get update >/dev/null 2>&1 && apt-get install -y ufw >/dev/null 2>&1 || true
        fi
        if command -v ufw &>/dev/null; then
            DEFAULT_INCOMING=$(echo "$FIREWALL_CONFIG" | jq -r '.default_incoming // "deny"')
            if [[ "$DEFAULT_INCOMING" == "deny" ]]; then
                ufw default deny incoming >/dev/null 2>&1 || true
            elif [[ "$DEFAULT_INCOMING" == "allow" ]]; then
                ufw default allow incoming >/dev/null 2>&1 || true
            fi
            ufw default allow outgoing >/dev/null 2>&1 || true

            ALLOWED_CIDRS=$(echo "$POLICY" | jq -r '.allowed_source_cidrs[]?' 2>/dev/null || true)
            for CIDR in $ALLOWED_CIDRS; do
                ufw allow from "$CIDR" to any port 22 proto tcp comment 'bastion-hub ssh' >/dev/null 2>&1 || true
            done

            ALLOW_PORTS=$(echo "$FIREWALL_CONFIG" | jq -r '.allow_ports[]?' 2>/dev/null || true)
            for PORT_SPEC in $ALLOW_PORTS; do
                ufw allow "$PORT_SPEC" >/dev/null 2>&1 || true
            done

            STRICT_MODE=$(echo "$FIREWALL_CONFIG" | jq -r '.strict_mode // false')
            if [[ "$STRICT_MODE" == "true" ]]; then
                ufw deny 22/tcp comment 'bastion-hub deny others' >/dev/null 2>&1 || true
            fi

            ufw --force enable >/dev/null 2>&1 || true
            echo "[policy] firewall applied"
        else
            POLICY_ERROR="ufw installation failed"
        fi
    fi
fi

# === Docker ===
if [[ -z "$POLICY_ERROR" ]]; then
    DOCKER_CONFIG=$(echo "$POLICY" | jq -r '.docker_config // {}')
    WATCHTOWER_ENABLED=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower // false')
    if [[ "$WATCHTOWER_ENABLED" == "true" ]]; then
        if command -v docker &>/dev/null; then
            WATCHTOWER_IMAGE=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_image // "containrrr/watchtower"')
            WATCHTOWER_INTERVAL=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_interval // 3600')
            WATCHTOWER_CLEANUP=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_cleanup // true')
            if docker ps -a --format '{{.Names}}' | grep -qxF "watchtower"; then
                docker start watchtower >/dev/null 2>&1 || true
                echo "[policy] Watchtower started"
            else
                docker run -d --name watchtower --restart unless-stopped \
                    -v /var/run/docker.sock:/var/run/docker.sock \
                    "$WATCHTOWER_IMAGE" \
                    --interval "$WATCHTOWER_INTERVAL" \
                    $(if [[ "$WATCHTOWER_CLEANUP" == "true" ]]; then echo "--cleanup"; fi) \
                    >/dev/null 2>&1 || POLICY_ERROR="watchtower deploy failed"
                if [[ -z "$POLICY_ERROR" ]]; then
                    echo "[policy] Watchtower created"
                fi
            fi
        else
            POLICY_ERROR="docker not available for watchtower"
        fi
    else
        if command -v docker &>/dev/null && docker ps -a --format '{{.Names}}' | grep -qxF "watchtower"; then
            docker stop watchtower >/dev/null 2>&1 || true
            echo "[policy] Watchtower stopped"
        fi
    fi
fi

if [[ -z "$POLICY_ERROR" ]]; then
    NEW_POLICY_REVISION="$REVISION"
    echo "$NEW_POLICY_REVISION" > "$POLICY_REV_FILE"
    echo "[policy] Applied revision $REVISION"
else
    echo "[policy] Failed: $POLICY_ERROR"
fi

curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/policy-result" \
    -H "Authorization: Bearer $NODE_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$(jq -n --argjson rev "$NEW_POLICY_REVISION" --arg err "$POLICY_ERROR" '{applied_revision: $rev, error_msg: (if $err == "" then null else $err end)}')" || true
