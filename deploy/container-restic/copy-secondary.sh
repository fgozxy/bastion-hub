#!/usr/bin/env bash
set -Eeuo pipefail

BASE_DIR=${CONTAINER_RESTIC_BASE:-/root/container-restic}
GLOBAL_ENV="$BASE_DIR/restic.env"

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

load_env() {
  [[ -f "$GLOBAL_ENV" ]] || die "missing $GLOBAL_ENV"
  set -a
  # shellcheck disable=SC1090
  source "$GLOBAL_ENV"
  set +a
  [[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is empty"
  [[ -n "${RESTIC_PASSWORD_FILE:-}${RESTIC_PASSWORD:-}" ]] || die "set RESTIC_PASSWORD_FILE or RESTIC_PASSWORD"
  [[ -n "${SECONDARY_RESTIC_REPOSITORY:-}" ]] || die "SECONDARY_RESTIC_REPOSITORY is empty"
  [[ -n "${SECONDARY_RESTIC_PASSWORD_FILE:-}" ]] || SECONDARY_RESTIC_PASSWORD_FILE=${RESTIC_PASSWORD_FILE:-}
  export RESTIC_REPOSITORY RESTIC_PASSWORD RESTIC_PASSWORD_FILE SECONDARY_RESTIC_REPOSITORY SECONDARY_RESTIC_PASSWORD_FILE RCLONE_CONFIG
}

node_name() {
  printf '%s' "${NODE_NAME:-$(hostname -s 2>/dev/null || hostname || echo unknown)}"
}

init_secondary() {
  load_env
  need_cmd restic
  log "initializing secondary repository: $SECONDARY_RESTIC_REPOSITORY"
  restic --repo "$SECONDARY_RESTIC_REPOSITORY" --password-file "$SECONDARY_RESTIC_PASSWORD_FILE" init
}

copy_secondary() {
  load_env
  need_cmd restic
  log "copying docker-container snapshots for node $(node_name) to secondary"
  restic \
    --repo "$SECONDARY_RESTIC_REPOSITORY" \
    --password-file "$SECONDARY_RESTIC_PASSWORD_FILE" \
    copy \
    --from-repo "$RESTIC_REPOSITORY" \
    --from-password-file "$RESTIC_PASSWORD_FILE" \
    --tag "type:docker-container" \
    --tag "node:$(node_name)"
}

case "${1:-copy-secondary}" in
  init-secondary) init_secondary ;;
  copy-secondary|copy) copy_secondary ;;
  *)
    printf 'Usage: copy-secondary.sh [init-secondary|copy-secondary]\n' >&2
    exit 1
    ;;
esac
