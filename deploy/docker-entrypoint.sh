#!/bin/sh
set -e

DATA="${NODEPANEL_DATA:-/var/lib/nodepanel}"
mkdir -p "$DATA/assets" "$DATA/backups"

# Seed the bundled agent binaries into the data volume's assets dir (served at
# /dl/) whenever the image ships a newer copy. Nodes pull these via the panel.
if [ -d /app/agents ]; then
  for f in /app/agents/nodepanel-agent-linux-*; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    dest="$DATA/assets/$name"
    if [ ! -f "$dest" ] || ! cmp -s "$f" "$dest"; then
      cp -f "$f" "$dest"
      echo "[entrypoint] seeded $name"
    fi
  done
fi

# Default to dev mode on 0.0.0.0:8088 (so the published port / reverse proxy
# reaches it) unless the caller passes explicit flags.
if [ "$#" -eq 0 ]; then
  set -- --dev --dev-addr=0.0.0.0:8088 --data-dir="$DATA"
fi

exec /app/nodepanel-master "$@"
