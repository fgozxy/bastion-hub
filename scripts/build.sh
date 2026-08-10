#!/usr/bin/env bash
# Build the frontend, embed it into the master, and cross-compile the agent
# for the architectures served at /dl/.
set -e
cd "$(dirname "$0")/.."

echo "[1/4] building frontend"
( cd web && npm install --no-audit --no-fund >/dev/null && npm run build )

echo "[2/4] embedding frontend into master"
rm -rf master/internal/webassets/dist
cp -r web/dist master/internal/webassets/dist

echo "[3/4] building master"
mkdir -p build
go build -trimpath -ldflags "-s -w" -o build/nodepanel-master ./master/cmd/master

echo "[4/4] cross-compiling agents"
for arch in amd64 arm64 386; do
  GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" -o build/nodepanel-agent-linux-$arch ./agent/cmd/agent
done
GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w" -o build/nodepanel-agent-linux-arm-7 ./agent/cmd/agent

echo "✓ build complete:"
ls -la build/
