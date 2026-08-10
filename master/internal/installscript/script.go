// Package installscript renders the per-node installation shell script.
package installscript

import "strings"

// Render produces the install.sh payload for a given master base URL and
// enrollment token.
func Render(base, token string) string {
	base = strings.TrimRight(base, "/")
	s := tpl
	s = strings.ReplaceAll(s, "__BASE__", base)
	s = strings.ReplaceAll(s, "__TOKEN__", token)
	return s
}

const tpl = `#!/usr/bin/env bash
set -e
BASE="__BASE__"
TOKEN="__TOKEN__"

if [ "$(id -u)" != "0" ]; then
  echo "Please run this installer as root." >&2
  exit 1
fi

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64|armv8l) ARCH="arm64" ;;
  armv7l) ARCH="arm-7" ;;
  i386|i686) ARCH="386" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
OS="linux"

URL="$BASE/dl/nodepanel-agent-$OS-$ARCH"
echo "Downloading NodePanel agent from $URL ..."
install -m 0755 -o root -g root /dev/null /usr/local/bin/nodepanel-agent || true
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o /usr/local/bin/nodepanel-agent
else
  wget -qO /usr/local/bin/nodepanel-agent "$URL"
fi
chmod 0755 /usr/local/bin/nodepanel-agent

mkdir -p /etc/nodepanel-agent /var/lib/nodepanel-agent
cat > /etc/nodepanel-agent/config.conf <<'CONF'
server = "__BASE__"
token = "__TOKEN__"
CONF

cat > /etc/systemd/system/nodepanel-agent.service <<'UNIT'
[Unit]
Description=NodePanel Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nodepanel-agent -c /etc/nodepanel-agent/config.conf
Restart=always
RestartSec=5
Environment=NODEPANEL_STATE=/var/lib/nodepanel-agent/state.json
Environment=HOME=/root

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable nodepanel-agent.service
# Clear any cached identity so the agent re-enrolls with this token, and restart
# (enable --now alone won't restart an already-running agent with a new config).
rm -f /var/lib/nodepanel-agent/state.json
systemctl restart nodepanel-agent.service
echo "NodePanel agent installed and started. It will appear in the panel shortly."
`
