#!/usr/bin/env bash
set -euo pipefail

PANEL_BASE_URL="${PANEL_BASE_URL:?required}"
TOKEN_FILE="${TOKEN_FILE:?required}"

if [[ ! -f "$TOKEN_FILE" ]]; then
    echo "[agent] Token file not found: $TOKEN_FILE"
    exit 1
fi

NODE_TOKEN=$(cat "$TOKEN_FILE")
HOSTNAME=$(hostname -f 2>/dev/null || hostname)

# State persistence
STATE_DIR="/var/lib/bastion-hub"
mkdir -p "$STATE_DIR"
POLICY_REV_FILE="$STATE_DIR/applied_policy_revision"

APPLIED_POLICY_REVISION=0
if [[ -f "$POLICY_REV_FILE" ]]; then
    APPLIED_POLICY_REVISION=$(cat "$POLICY_REV_FILE")
fi

# Collect IPs
COLLECT_IPS() {
    local ips="[]"
    local pub4 pub6
    pub4=$(curl -fsSL -4 --max-time 10 https://icanhazip.com 2>/dev/null || true)
    if [[ -n "$pub4" ]]; then
        ips=$(echo "$ips" | jq ". + [{family:\"ipv4\",scope:\"public\",address:\"$pub4\",source:\"external_check\"}]")
    fi
    pub6=$(curl -fsSL -6 --max-time 10 https://icanhazip.com 2>/dev/null || true)
    if [[ -n "$pub6" ]]; then
        ips=$(echo "$ips" | jq ". + [{family:\"ipv6\",scope:\"public\",address:\"$pub6\",source:\"external_check\"}]")
    fi
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

# Heartbeat
HEARTBEAT_PAYLOAD=$(jq -n \
    --arg hostname "$HOSTNAME" \
    --argjson addresses "$IP_JSON" \
    --arg agent_version "0.2.0" \
    --argjson applied_policy_revision "$APPLIED_POLICY_REVISION" \
    '{hostname:$hostname, addresses:$addresses, agent_version:$agent_version, applied_policy_revision:$applied_policy_revision}')

HEARTBEAT_RESPONSE=$(curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/heartbeat" \
    -H "Authorization: Bearer $NODE_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$HEARTBEAT_PAYLOAD" 2>/dev/null) || {
    echo "[agent] Heartbeat failed"
    exit 1
}

# Self-update from policy before applying anything else
SELF_UPDATE() {
    local CONFIG="$1"
    local AUTO_UPDATE=$(echo "$CONFIG" | jq -r '.auto_update // false')
    if [[ "$AUTO_UPDATE" != "true" ]]; then
        return
    fi

    local SCRIPTS=$(echo "$CONFIG" | jq -r '.scripts // {}')
    if [[ "$SCRIPTS" == "{}" || "$SCRIPTS" == "null" ]]; then
        return
    fi

    local SCRIPT_DIR
    SCRIPT_DIR=$(dirname "$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")")
    local UPDATED=0

    while IFS= read -r filename; do
        [[ -z "$filename" ]] && continue
        local expected=$(echo "$SCRIPTS" | jq -r --arg f "$filename" '.[$f] // empty')
        [[ -z "$expected" ]] && continue

        if [[ "$expected" == sha256:* ]]; then
            expected="${expected#sha256:}"
        fi

        local target_file="$SCRIPT_DIR/$filename"
        local current_script=""
        if [[ "$filename" == "agent.sh" ]]; then
            current_script=$(readlink -f "${BASH_SOURCE[0]}" 2>/dev/null || echo "${BASH_SOURCE[0]}")
        fi

        local local_sha=""
        if [[ -f "$target_file" ]]; then
            local_sha=$(sha256sum "$target_file" 2>/dev/null | awk '{print $1}')
        fi

        if [[ "$local_sha" == "$expected" ]]; then
            continue
        fi

        local tmp_file="${target_file}.tmp.$$"
        if curl -fsSL -H "Authorization: Bearer $NODE_TOKEN" \
            "$PANEL_BASE_URL/api/agent/assets/$filename" -o "$tmp_file" 2>/dev/null; then

            local download_sha=$(sha256sum "$tmp_file" 2>/dev/null | awk '{print $1}')
            if [[ "$download_sha" == "$expected" ]]; then
                chmod +x "$tmp_file"
                mv "$tmp_file" "$target_file"
                echo "[agent] Updated $filename ($local_sha -> $download_sha)"
                UPDATED=1
                if [[ -n "$current_script" && "$current_script" != "$target_file" && -f "$current_script" ]]; then
                    cp "$target_file" "$current_script"
                    echo "[agent] Also updated running script: $current_script"
                fi
            else
                echo "[agent] Checksum mismatch for $filename: expected $expected, got $download_sha"
                rm -f "$tmp_file"
            fi
        else
            echo "[agent] Failed to download $filename"
            rm -f "$tmp_file"
        fi
    done < <(echo "$SCRIPTS" | jq -r 'keys[]')

    if [[ "$UPDATED" -eq 1 ]]; then
        echo "$(date -Iseconds)" > "$STATE_DIR/agent_last_updated"
    fi
}

AGENT_CONFIG="{}"
if [[ -n "$HEARTBEAT_RESPONSE" ]]; then
    AGENT_CONFIG=$(echo "$HEARTBEAT_RESPONSE" | jq -r '.agent_config // {}' 2>/dev/null || echo "{}")
fi
SELF_UPDATE "$AGENT_CONFIG"

# Policy fetch and apply
POLICY_RESPONSE=$(curl -fsSL -X GET "$PANEL_BASE_URL/api/agent/policy" \
    -H "Authorization: Bearer $NODE_TOKEN" \
    -H "Content-Type: application/json" 2>/dev/null || true)

POLICY_ERROR=""
NEW_POLICY_REVISION="$APPLIED_POLICY_REVISION"

if [[ -n "$POLICY_RESPONSE" ]]; then
    POLICY_REVISION=$(echo "$POLICY_RESPONSE" | jq -r '.revision // 0')
    POLICY_MODE=$(echo "$POLICY_RESPONSE" | jq -r '.mode // "report"')

    # If already converged, clear rollback marker
    if [[ "$POLICY_REVISION" -le "$APPLIED_POLICY_REVISION" ]]; then
        rm -f "$STATE_DIR/ssh_policy_applied_at"
    fi

    if [[ "$POLICY_MODE" == "enforce" && "$POLICY_REVISION" -gt "$APPLIED_POLICY_REVISION" ]]; then
        # === SSHD ===
        # Backup original sshd_config once
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
            CA_KEY=$(echo "$POLICY_RESPONSE" | jq -r '.trusted_user_ca_public_key // empty')
            if [[ -n "$CA_KEY" ]]; then
                mkdir -p /etc/bastion-hub/ssh
                printf '%s\n' "$CA_KEY" > /etc/bastion-hub/ssh/user_ca.pub
                chmod 644 /etc/bastion-hub/ssh/user_ca.pub
                echo "TrustedUserCAKeys /etc/bastion-hub/ssh/user_ca.pub"
            fi

            # Extra sshd config from policy
            SSHD_CONFIG=$(echo "$POLICY_RESPONSE" | jq -r '.sshd_config // {}')
            if [[ "$SSHD_CONFIG" != "{}" && "$SSHD_CONFIG" != "null" ]]; then
                echo "$SSHD_CONFIG" | jq -r 'to_entries[] | "\(.key) \(.value)"' 2>/dev/null || true
            fi

            echo "# === End Bastion Hub managed block ==="
        } >> /etc/ssh/sshd_config

        AFTER_HASH=$(md5sum /etc/ssh/sshd_config 2>/dev/null | awk '{print $1}' || echo "")

        if [[ "$BEFORE_HASH" != "$AFTER_HASH" ]]; then
            if sshd -t >/dev/null 2>&1; then
                systemctl restart sshd >/dev/null 2>&1 || service ssh restart >/dev/null 2>&1 || true
                # Mark for rollback check
                date +%s > "$STATE_DIR/ssh_policy_applied_at"
            else
                POLICY_ERROR="sshd_config syntax invalid after applying policy"
                # Remove managed block
                sed -i '/^# === Bastion Hub managed block ===/,/^# === End Bastion Hub managed block ===/d' /etc/ssh/sshd_config
                # Restore backup if needed
                if [[ -f /etc/ssh/sshd_config.bastion-backup ]]; then
                    cp /etc/ssh/sshd_config.bastion-backup /etc/ssh/sshd_config
                fi
            fi
        fi

        # Apply firewall_config
        if [[ -z "$POLICY_ERROR" ]]; then
            FIREWALL_CONFIG=$(echo "$POLICY_RESPONSE" | jq -r '.firewall_config // {}')
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

                    # Allow from allowed_source_cidrs
                    ALLOWED_CIDRS=$(echo "$POLICY_RESPONSE" | jq -r '.allowed_source_cidrs[]?' 2>/dev/null || true)
                    for CIDR in $ALLOWED_CIDRS; do
                        ufw allow from "$CIDR" to any port 22 proto tcp comment 'bastion-hub ssh' >/dev/null 2>&1 || true
                    done

                    ALLOW_PORTS=$(echo "$FIREWALL_CONFIG" | jq -r '.allow_ports[]?' 2>/dev/null || true)
                    for PORT_SPEC in $ALLOW_PORTS; do
                        ufw allow "$PORT_SPEC" >/dev/null 2>&1 || true
                    done

                    # Deny 22 from others if strict mode
                    STRICT_MODE=$(echo "$FIREWALL_CONFIG" | jq -r '.strict_mode // false')
                    if [[ "$STRICT_MODE" == "true" ]]; then
                        ufw deny 22/tcp comment 'bastion-hub deny others' >/dev/null 2>&1 || true
                    fi

                    ufw --force enable >/dev/null 2>&1 || true
                else
                    POLICY_ERROR="ufw installation failed"
                fi
            fi
        fi

        # Apply docker_config
        if [[ -z "$POLICY_ERROR" ]]; then
            DOCKER_CONFIG=$(echo "$POLICY_RESPONSE" | jq -r '.docker_config // {}')
            WATCHTOWER_ENABLED=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower // false')
            if [[ "$WATCHTOWER_ENABLED" == "true" ]]; then
                if command -v docker &>/dev/null; then
                    WATCHTOWER_IMAGE=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_image // "containrrr/watchtower"')
                    WATCHTOWER_INTERVAL=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_interval // 3600')
                    WATCHTOWER_CLEANUP=$(echo "$DOCKER_CONFIG" | jq -r '.watchtower_cleanup // true')
                    if docker ps -a --format '{{.Names}}' | grep -qxF "watchtower"; then
                        docker start watchtower >/dev/null 2>&1 || true
                    else
                        docker run -d --name watchtower --restart unless-stopped \
                            -v /var/run/docker.sock:/var/run/docker.sock \
                            "$WATCHTOWER_IMAGE" \
                            --interval "$WATCHTOWER_INTERVAL" \
                            $(if [[ "$WATCHTOWER_CLEANUP" == "true" ]]; then echo "--cleanup"; fi) \
                            >/dev/null 2>&1 || POLICY_ERROR="watchtower deploy failed"
                    fi
                else
                    POLICY_ERROR="docker not available for watchtower"
                fi
            else
                if command -v docker &>/dev/null && docker ps -a --format '{{.Names}}' | grep -qxF "watchtower"; then
                    docker stop watchtower >/dev/null 2>&1 || true
                fi
            fi
        fi

        if [[ -z "$POLICY_ERROR" ]]; then
            NEW_POLICY_REVISION="$POLICY_REVISION"
            echo "$NEW_POLICY_REVISION" > "$POLICY_REV_FILE"
        fi
    fi

    # Report policy result
    curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/policy-result" \
        -H "Authorization: Bearer $NODE_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$(jq -n --argjson rev "$NEW_POLICY_REVISION" --arg err "$POLICY_ERROR" '{applied_revision: $rev, error_msg: (if $err == "" then null else $err end)}')" || true
fi

# Credentials
CREDENTIALS_RESPONSE=$(curl -fsSL -X GET "$PANEL_BASE_URL/api/agent/credentials" \
    -H "Authorization: Bearer $NODE_TOKEN" \
    -H "Content-Type: application/json" 2>/dev/null || true)

if [[ -n "$CREDENTIALS_RESPONSE" ]]; then
    RESULTS="[]"
    USERS=$(echo "$CREDENTIALS_RESPONSE" | jq -r '.credentials_by_user | keys[]?' 2>/dev/null || true)
    for USER in $USERS; do
        USER_HOME=$(getent passwd "$USER" | cut -d: -f6)
        if [[ -z "$USER_HOME" ]]; then
            BINDING_IDS=$(echo "$CREDENTIALS_RESPONSE" | jq -r --arg u "$USER" '.credentials_by_user[$u][]? | .binding_id' 2>/dev/null || true)
            for BID in $BINDING_IDS; do
                RESULTS=$(echo "$RESULTS" | jq ". + [{binding_id: $BID, status: \"failed\", error_msg: \"User $USER not found\"}]")
            done
            continue
        fi

        SSH_DIR="$USER_HOME/.ssh"
        AUTH_KEYS="$SSH_DIR/authorized_keys"

        mkdir -p "$SSH_DIR"
        chmod 700 "$SSH_DIR"

        KEYS_JSON=$(echo "$CREDENTIALS_RESPONSE" | jq -r --arg u "$USER" '.credentials_by_user[$u] // []' 2>/dev/null || echo "[]")
        if [[ "$(echo "$KEYS_JSON" | jq 'length')" -eq 0 ]]; then
            continue
        fi

        # Backup original if it exists and has no bastion-hub marker
        if [[ -f "$AUTH_KEYS" ]] && [[ -s "$AUTH_KEYS" ]]; then
            if ! grep -q "=== BASTION-HUB-MANAGED ===" "$AUTH_KEYS"; then
                cp "$AUTH_KEYS" "$AUTH_KEYS.bak.$(date +%s)"
            fi
        fi

        # Extract unmanaged parts
        BEFORE=""
        AFTER=""
        if [[ -f "$AUTH_KEYS" ]]; then
            BEFORE=$(awk '/=== BASTION-HUB-MANAGED ===/{exit} {print}' "$AUTH_KEYS" 2>/dev/null || true)
            AFTER=$(awk 'BEGIN{found=0} /=== BASTION-HUB-END ===/{found=1; next} found{print}' "$AUTH_KEYS" 2>/dev/null || true)
        fi

        # Assemble final file
        {
            if [[ -n "$BEFORE" ]]; then
                echo "$BEFORE"
                echo ""
            fi
            echo "# === BASTION-HUB-MANAGED ==="
            echo "$KEYS_JSON" | jq -r '.[] | "# " + .name, .public_payload' 2>/dev/null || true
            echo "# === BASTION-HUB-END ==="
            if [[ -n "$AFTER" ]]; then
                echo ""
                echo "$AFTER"
            fi
        } > "$AUTH_KEYS"

        chmod 600 "$AUTH_KEYS"
        chown "$USER:$USER" "$AUTH_KEYS" 2>/dev/null || true

        # Mark bindings as applied
        BINDING_IDS=$(echo "$KEYS_JSON" | jq -r '.[] | .binding_id' 2>/dev/null || true)
        for BID in $BINDING_IDS; do
            RESULTS=$(echo "$RESULTS" | jq ". + [{binding_id: $BID, status: \"applied\"}]")
        done
    done

    # Report results
    if [[ "$(echo "$RESULTS" | jq 'length')" -gt 0 ]]; then
        curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/credentials-result" \
            -H "Authorization: Bearer $NODE_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$(jq -n --argjson results "$RESULTS" '{results: $results}')" || true
    fi
fi

# Docker snapshot if available
if command -v docker &>/dev/null; then
    DOCKER_AVAILABLE=true
    DOCKER_VERSION=$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)
    COMPOSE_VERSION=$(docker compose version --short 2>/dev/null || true)
    CONTAINERS_RUNNING=$(docker ps -q 2>/dev/null | wc -l)
    CONTAINERS_TOTAL=$(docker ps -aq 2>/dev/null | wc -l)
    IMAGES_TOTAL=$(docker images -q 2>/dev/null | wc -l)
    NETWORKS_TOTAL=$(docker network ls -q 2>/dev/null | wc -l)
    VOLUMES_TOTAL=$(docker volume ls -q 2>/dev/null | wc -l)

    # Compose projects
    RAW_COMPOSE=$(docker compose ls --format json 2>/dev/null || true)
    if [[ -z "$RAW_COMPOSE" ]]; then
        COMPOSE_PROJECTS="[]"
    else
        COMPOSE_PROJECTS=$(echo "$RAW_COMPOSE" | jq -c '.[]' 2>/dev/null | while read -r proj; do
            name=$(echo "$proj" | jq -r '.Name // .name')
            status=$(echo "$proj" | jq -r '.Status // .status // ""')
            config_files=$(echo "$proj" | jq -r '.ConfigFiles // .config_files // ""')
            proj_dir=$(echo "$config_files" | cut -d',' -f1 | sed 's|/[^/]*$||')
            git_rev=""
            git_branch=""
            if [[ -n "$proj_dir" && -d "$proj_dir/.git" ]]; then
                git_rev=$(cd "$proj_dir" && git rev-parse HEAD 2>/dev/null || true)
                git_branch=$(cd "$proj_dir" && git rev-parse --abbrev-ref HEAD 2>/dev/null || true)
            fi
            jq -n \
                --arg name "$name" \
                --arg status "$status" \
                --arg config_files "$config_files" \
                --arg git_rev "$git_rev" \
                --arg git_branch "$git_branch" \
                '{name: $name, status: $status, config_files: $config_files, git_url: "", git_branch: $git_branch, current_revision: $git_rev}'
        done | jq -s '.')
        if [[ -z "$COMPOSE_PROJECTS" ]]; then
            COMPOSE_PROJECTS="[]"
        fi
    fi

    DOCKER_PAYLOAD=$(jq -n \
        --arg docker_available "$DOCKER_AVAILABLE" \
        --arg docker_version "$DOCKER_VERSION" \
        --arg compose_version "$COMPOSE_VERSION" \
        --argjson containers_running "$CONTAINERS_RUNNING" \
        --argjson containers_total "$CONTAINERS_TOTAL" \
        --argjson images_total "$IMAGES_TOTAL" \
        --argjson networks_total "$NETWORKS_TOTAL" \
        --argjson volumes_total "$VOLUMES_TOTAL" \
        --argjson compose_projects "$COMPOSE_PROJECTS" \
        '{
            docker_available: ($docker_available == "true"),
            docker_version: $docker_version,
            compose_version: $compose_version,
            containers_running: $containers_running,
            containers_total: $containers_total,
            images_total: $images_total,
            networks_total: $networks_total,
            volumes_total: $volumes_total,
            compose_projects: $compose_projects
        }')

    curl -fsSL -X POST "$PANEL_BASE_URL/api/agent/docker-snapshot" \
        -H "Authorization: Bearer $NODE_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$DOCKER_PAYLOAD" || true
fi

# SSH policy rollback check
SSH_POLICY_MARKER="$STATE_DIR/ssh_policy_applied_at"
if [[ -f "$SSH_POLICY_MARKER" ]]; then
    MARKER_TIME=$(cat "$SSH_POLICY_MARKER")
    NOW=$(date +%s)
    ELAPSED=$((NOW - MARKER_TIME))
    if [[ "$ELAPSED" -gt 300 ]]; then
        # Not confirmed within 5 minutes, rollback
        if [[ -f /etc/ssh/sshd_config.bastion-backup ]]; then
            cp /etc/ssh/sshd_config.bastion-backup /etc/ssh/sshd_config
            if sshd -t >/dev/null 2>&1; then
                systemctl restart sshd >/dev/null 2>&1 || true
                echo "[agent] SSH policy rolled back after timeout"
            else
                echo "[agent] WARNING: rollback sshd config test failed"
            fi
        fi
        rm -f "$SSH_POLICY_MARKER"
        echo "0" > "$POLICY_REV_FILE"
    fi
fi

echo "[agent] Run completed at $(date -Iseconds) (policy_rev=$NEW_POLICY_REVISION)"
