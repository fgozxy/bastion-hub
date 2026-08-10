package tunnels

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// safeNameRe restricts a tunnel name to characters safe for a systemd unit name
// and a filesystem path, preventing injection through the heredoc-written unit.
var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// safeName validates a tunnel name. Panel-created names must already match; this
// also guards odd names of hand-built tunnels when probing their (possibly
// differently-named) systemd unit.
func safeName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if !safeNameRe.MatchString(n) {
		return "", fmt.Errorf("隧道名只能包含字母、数字、下划线和连字符")
	}
	return n, nil
}

// installCmd installs cloudflared if absent (apt / dnf / yum / binary fallback)
// and prints its version. Idempotent.
func installCmd() string {
	return `set -e
if command -v cloudflared >/dev/null 2>&1; then cloudflared --version; exit 0; fi
if command -v apt-get >/dev/null 2>&1; then
  curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg
  chmod a+r /usr/share/keyrings/cloudflare-main.gpg
  codename=$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-bookworm}")
  echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared $codename main" > /etc/apt/sources.list.d/cloudflared.list
  apt-get update -qq && apt-get install -y cloudflared
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared.x86_64.rpm
elif command -v yum >/dev/null 2>&1; then
  yum install -y https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared.x86_64.rpm
else
  arch=$(uname -m); case "$arch" in x86_64) a=amd64;; aarch64|arm64) a=arm64;; *) echo "unsupported arch: $arch"; exit 1;; esac
  curl -fsSL -o /usr/local/bin/cloudflared "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$a"
  chmod +x /usr/local/bin/cloudflared
fi
cloudflared --version`
}

// unitCmd writes the 0600 env file (TUNNEL_TOKEN) + the systemd unit for the
// tunnel, then daemon-reloads and enables+starts it. safe/token are trusted
// (validated by caller; token is a base64url connector token). Uses a single-
// quoted heredoc so $TUNNEL_TOKEN and $(command -v …) are written verbatim and
// expanded by systemd/shell at run time.
func unitCmd(safe, token string) string {
	return fmt.Sprintf(`set -e
mkdir -p /etc/cloudflared
umask 077
cat > /etc/cloudflared/%[1]s.env <<'ENV'
TUNNEL_TOKEN=%[2]s
ENV
chmod 600 /etc/cloudflared/%[1]s.env
cat > /etc/systemd/system/cloudflared-%[1]s.service <<'UNIT'
[Unit]
Description=cloudflared tunnel %[1]s
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/cloudflared/%[1]s.env
ExecStart=/bin/sh -c 'exec "$(command -v cloudflared)" tunnel --no-autoupdate run --token "$TUNNEL_TOKEN"'
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now cloudflared-%[1]s.service`, safe, token)
}

// statusCmd emits two lines: PROC=<systemctl is-active> and VER=<cloudflared
// version>, or empty values when unavailable. Uses echo (no %) so it is safe to
// pass through fmt.
func statusCmd(safe string) string {
	return fmt.Sprintf(`echo "PROC=$(systemctl is-active cloudflared-%[1]s.service 2>/dev/null)"
echo "VER=$(cloudflared --version 2>/dev/null | head -1)"`, safe)
}

// startCmd / stopCmd control the tunnel's systemd service.
func startCmd(safe string) string { return fmt.Sprintf("systemctl start cloudflared-%s.service", safe) }
func stopCmd(safe string) string  { return fmt.Sprintf("systemctl stop cloudflared-%s.service", safe) }

// cleanupCmd disables & removes the tunnel's systemd unit + env file. Best-effort
// (the node may be offline when a tunnel is deleted).
func cleanupCmd(safe string) string {
	return fmt.Sprintf(`systemctl disable --now cloudflared-%[1]s.service 2>/dev/null || true
rm -f /etc/systemd/system/cloudflared-%[1]s.service /etc/cloudflared/%[1]s.env
systemctl daemon-reload 2>/dev/null || true`, safe)
}

// decodeTunnelToken mirrors agent/internal/agent decodeTunnelToken: it
// base64url-decodes a cloudflared connector token and returns the "t" (tunnel
// id) field of its JSON payload {"a","t","s"}. Used to identify which Docker
// container runs a given tunnel; unit-tested here.
func decodeTunnelToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	dec, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		// some tokens are standard base64 (with padding); try that as a fallback
		dec, err = base64.StdEncoding.DecodeString(token)
		if err != nil {
			return ""
		}
	}
	var p struct {
		T string `json:"t"`
	}
	if json.Unmarshal(dec, &p) != nil {
		return ""
	}
	return p.T
}

// dockerFindBlock is the shared shell fragment that resolves the cloudflared
// Docker container running tunnel %[1]s into $match. It tries an exact match
// (decode each cloudflared container's connector token and check it carries the
// tunnel id — the same logic as decodeTunnelToken, done in coreutils on the
// node), then falls back to a single container named cloudflared. $match is left
// empty when nothing matches. Uses echo (no %) so it is safe through fmt.
const dockerFindBlock = `target='%[1]s'
match=""
for c in $(docker ps -aq --filter ancestor=cloudflare/cloudflared); do
  tok=$(docker inspect -f '{{range .Config.Cmd}}{{println .}}{{end}}{{range .Config.Env}}{{println .}}{{end}}' "$c" 2>/dev/null | awk '
    /^--token$/ {getline; print; next}
    /^--token=/ {sub(/^--token=/,""); print; next}
    /^TUNNEL_TOKEN=/ {sub(/^TUNNEL_TOKEN=/,""); print; next}
  ' | head -1)
  [ -z "$tok" ] && continue
  if echo "$tok" | tr '_-' '/+' | base64 -di 2>/dev/null | grep -q -- "$target"; then
    match="$c"; break
  fi
done
if [ -z "$match" ]; then
  ids=$(docker ps -aq --filter name=cloudflared)
  [ "$(echo "$ids" | wc -w)" -eq 1 ] && match="$ids"
fi`

// dockerCtlCmd starts/stops/removes the cloudflared Docker container running
// tunnel %[1]s on a node. action is "start" | "stop" | "rm". For "rm" the
// container is stopped first; a missing container is a no-op (exit 0). For
// start/stop a missing container exits 3 so the caller can surface a clear
// error. Used for hand-built tunnels whose cloudflared runs in Docker rather
// than under a panel-provisioned systemd unit.
func dockerCtlCmd(tunnelID, action string) string {
	switch action {
	case "start", "stop", "rm":
	default:
		panic("dockerCtlCmd: bad action " + action)
	}
	return fmt.Sprintf(dockerFindBlock+`
if [ -z "$match" ]; then
  case '%[2]s' in
    rm) echo "no cloudflared container to clean"; exit 0 ;;
    *) echo "no matching cloudflared container"; exit 3 ;;
  esac
fi
case '%[2]s' in
  start) docker start "$match" ;;
  stop)  docker stop "$match" ;;
  rm)    docker stop "$match" 2>/dev/null || true; docker rm -f "$match" ;;
esac`, tunnelID, action)
}

// dockerStatusCmd emits PROC=<active|state> for the cloudflared Docker container
// running tunnel %[1]s, mirroring statusCmd's PROC= contract (active = running).
// VER is best-effort via docker exec. $match empty → PROC= (panel shows 未知).
func dockerStatusCmd(tunnelID string) string {
	return fmt.Sprintf(dockerFindBlock+`
if [ -z "$match" ]; then echo "PROC="; exit 0; fi
st=$(docker inspect -f '{{.State.Status}}' "$match" 2>/dev/null)
echo "PROC=$( [ "$st" = "running" ] && echo active || echo "$st" )"
echo "VER=$(docker exec "$match" cloudflared --version 2>/dev/null | head -1)"`, tunnelID)
}
