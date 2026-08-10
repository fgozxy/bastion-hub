#!/usr/bin/env bash
set -Eeuo pipefail

BASE_DIR=${CONTAINER_RESTIC_BASE:-/root/container-restic}
GLOBAL_ENV="$BASE_DIR/restic.env"
SOURCE_DEPLOY=${CONTAINER_RESTIC_DEPLOY:-/root/docker/nodepanel/deploy/container-restic}

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

load_env() {
  [[ -f "$GLOBAL_ENV" ]] || die "missing $GLOBAL_ENV"
  set -a
  # shellcheck disable=SC1090
  source "$GLOBAL_ENV"
  set +a
  [[ -n "${GITHUB_OWNER:-}" ]] || die "GITHUB_OWNER is empty"
  [[ -n "${GITHUB_REPO:-}" ]] || die "GITHUB_REPO is empty"
  [[ -n "${GITHUB_TOKEN:-}" ]] || die "GITHUB_TOKEN is empty"
  GITHUB_BRANCH=${GITHUB_BRANCH:-main}
  GITHUB_PREFIX=${GITHUB_PREFIX:-container-restic}
}

sanitize_env() {
  local src=$1 dst=$2
  sed -E \
    -e 's/^(RESTIC_PASSWORD=).*/\1[redacted]/' \
    -e 's/^(RESTIC_PASSWORD_FILE=).*/\1[local-only]/' \
    -e 's/^(SECONDARY_RESTIC_PASSWORD_FILE=).*/\1[local-only]/' \
    -e 's/^(AWS_ACCESS_KEY_ID=).*/\1[redacted]/' \
    -e 's/^(AWS_SECRET_ACCESS_KEY=).*/\1[redacted]/' \
    -e 's/^(GITHUB_TOKEN=).*/\1[redacted]/' \
    "$src" >"$dst"
}

copy_tree() {
  local src=$1 dst=$2
  [[ -e "$src" ]] || return 0
  mkdir -p "$dst"
  cp -a "$src"/. "$dst"/
}

prepare_payload() {
  local payload=$1
  mkdir -p "$payload"
  copy_tree "$SOURCE_DEPLOY" "$payload/deploy/container-restic"
  if [[ -d "$BASE_DIR/containers.d" ]]; then
    copy_tree "$BASE_DIR/containers.d" "$payload/runtime/containers.d"
  fi
  if [[ -f "$GLOBAL_ENV" ]]; then
    mkdir -p "$payload/runtime"
    sanitize_env "$GLOBAL_ENV" "$payload/runtime/restic.env.sanitized"
  fi
  cat >"$payload/README.restore.md" <<EOF2
# Container Restic Runtime Config Backup

Generated at: $(date -Is)
Node: ${NODE_NAME:-$(hostname -s 2>/dev/null || hostname || echo unknown)}

This GitHub backup intentionally contains scripts and sanitized config only.
It does not contain restic repository data, restic passwords, OneDrive tokens,
or container volume data.
EOF2
}

curl_cfg() {
  local file=$1
  cat >"$file" <<EOF2
header = "Authorization: Bearer ${GITHUB_TOKEN}"
header = "Accept: application/vnd.github+json"
header = "X-GitHub-Api-Version: 2022-11-28"
EOF2
  chmod 0600 "$file"
}

api_get_sha() {
  local cfg=$1 path=$2 url code body
  url="https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/contents/${path}?ref=${GITHUB_BRANCH}"
  body=$(mktemp)
  code=$(curl -sS -L --config "$cfg" -o "$body" -w '%{http_code}' "$url")
  if [[ "$code" == "200" ]]; then
    jq -r '.sha // empty' "$body"
  else
    printf ''
  fi
  rm -f "$body"
}

api_put_file() {
  local cfg=$1 local_file=$2 remote_path=$3 msg=$4 sha content body resp code url
  sha=$(api_get_sha "$cfg" "$remote_path")
  content=$(base64 -w0 "$local_file")
  body=$(mktemp)
  resp=$(mktemp)
  if [[ -n "$sha" ]]; then
    jq -n --arg message "$msg" --arg content "$content" --arg branch "$GITHUB_BRANCH" --arg sha "$sha" \
      '{message:$message,content:$content,branch:$branch,sha:$sha}' >"$body"
  else
    jq -n --arg message "$msg" --arg content "$content" --arg branch "$GITHUB_BRANCH" \
      '{message:$message,content:$content,branch:$branch}' >"$body"
  fi
  url="https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/contents/${remote_path}"
  code=$(curl -sS -L --config "$cfg" -X PUT -H 'Content-Type: application/json' -d @"$body" -o "$resp" -w '%{http_code}' "$url")
  if [[ "$code" != "200" && "$code" != "201" ]]; then
    sed -E 's/"token":"[^"]+"/"token":"[redacted]"/g' "$resp" >&2
    rm -f "$body" "$resp"
    die "github upload failed for $remote_path (http $code)"
  fi
  rm -f "$body" "$resp"
}

push_payload() {
  load_env
  need_cmd curl
  need_cmd jq
  need_cmd tar
  need_cmd base64
  local work payload cfg archive manifest remote_base
  work=$(mktemp -d)
  trap "rm -rf "$work"" EXIT
  payload="$work/payload"
  cfg="$work/curl.conf"
  archive="$work/container-restic-config.tar.gz"
  manifest="$work/manifest.json"
  remote_base=${GITHUB_PREFIX%/}

  prepare_payload "$payload"
  tar -C "$payload" -czf "$archive" .
  jq -n --arg generated_at "$(date -Is)" \
    --arg node "${NODE_NAME:-$(hostname -s 2>/dev/null || hostname || echo unknown)}" \
    --arg archive "${remote_base}/container-restic-config.tar.gz" \
    '{generated_at:$generated_at,node:$node,archive:$archive,contains:"scripts and sanitized config only"}' >"$manifest"
  curl_cfg "$cfg"
  log "uploading sanitized config package to ${GITHUB_OWNER}/${GITHUB_REPO}:${GITHUB_BRANCH}/${remote_base}"
  api_put_file "$cfg" "$archive" "${remote_base}/container-restic-config.tar.gz" "container restic config package $(date -Is)"
  api_put_file "$cfg" "$manifest" "${remote_base}/manifest.json" "container restic config manifest $(date -Is)"
}

case "${1:-backup}" in
  backup) push_payload ;;
  *)
    printf 'Usage: github-config-backup.sh [backup]\n' >&2
    exit 1
    ;;
esac
