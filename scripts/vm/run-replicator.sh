#!/usr/bin/env bash
# External launcher for stonkagents-replicator on a Linux guardian VM (not embedded in any .exe).
# Polls the tracker and enqueues downloads on local sync-daemon(s) until SIGTERM/SIGINT.
#
# Usage:
#   cp replicator.vm.env.example replicator.vm.env
#   edit replicator.vm.env
#   ./run-replicator.sh
#
# Or: REPLICATOR_ENV_FILE=/etc/stonkagents/replicator.env ./run-replicator.sh
#
# Environment file is optional if you export variables in systemd EnvironmentFile= instead.
#
# Required env vars (see replicator.vm.env.example):
#   REPLICATOR_TRACKER_URL     - tracker base URL, e.g. https://tracker.example.com
#   REPLICATOR_DAEMON_ENDPOINTS - comma-separated daemon URLs, e.g. http://127.0.0.1:7841
#
# Optional (replicator defaults apply if unset):
#   REPLICATOR_POLL_INTERVAL, REPLICATOR_REPLICA_TARGET, REPLICATOR_SOURCE_PEER_MAX_AGE,
#   REPLICATOR_RECENT_LIMIT, REPLICATOR_LOOKBACK, REPLICATOR_STATE_FILE,
#   REPLICATOR_STORAGE_SOFT_LIMIT_BYTES, REPLICATOR_STORAGE_HARD_LIMIT_BYTES,
#   REPLICATOR_DOWNLOAD_TIMEOUT, REPLICATOR_DOWNLOAD_POLL_INTERVAL,
#   REPLICATOR_MIN_REPLICAS_BEFORE_EVICTION
#
# Replicator logging (optional, consumed by stonkagents-replicator binary):
#   REPLICATOR_LOG_FORMAT=text   - human-readable lines (default: json)
#   REPLICATOR_LOG_LEVEL=info    - debug|info|warn|error (default: info)
#
# Launcher-only:
#   REPLICATOR_BIN        - path to stonkagents-replicator binary (default: search below)
#   REPLICATOR_ENV_FILE   - path to env file to source (default: alongside this script)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

ENV_FILE="${REPLICATOR_ENV_FILE:-${SCRIPT_DIR}/replicator.vm.env}"
if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  . "${ENV_FILE}"
  set +a
fi

if [[ -z "${REPLICATOR_TRACKER_URL:-}" ]]; then
  echo "run-replicator.sh: REPLICATOR_TRACKER_URL is required." >&2
  echo "Set it in ${ENV_FILE} or export it. See ${SCRIPT_DIR}/replicator.vm.env.example" >&2
  exit 1
fi

if [[ -z "${REPLICATOR_DAEMON_ENDPOINTS:-}" ]]; then
  echo "run-replicator.sh: REPLICATOR_DAEMON_ENDPOINTS is required." >&2
  echo "Set it in ${ENV_FILE} or export it. See ${SCRIPT_DIR}/replicator.vm.env.example" >&2
  exit 1
fi

resolve_bin() {
  if [[ -n "${REPLICATOR_BIN:-}" ]]; then
    echo "${REPLICATOR_BIN}"
    return
  fi
  for candidate in \
    "${REPO_ROOT}/bin/stonkagents-replicator" \
    "${SCRIPT_DIR}/stonkagents-replicator" \
    "$(command -v stonkagents-replicator 2>/dev/null || true)"; do
    if [[ -n "${candidate}" && -x "${candidate}" ]]; then
      echo "${candidate}"
      return
    fi
  done
  return 1
}

BIN="$(resolve_bin)" || {
  echo "run-replicator.sh: stonkagents-replicator binary not found." >&2
  echo "Build: (cd ${REPO_ROOT} && go build -o bin/stonkagents-replicator cmd/replicator/main.go)" >&2
  echo "Or set REPLICATOR_BIN to the full path." >&2
  exit 1
}

echo "run-replicator: launching stonkagents-replicator (PID will replace this shell on exec)" >&2
echo "  binary: ${BIN}" >&2
echo "  tracker: ${REPLICATOR_TRACKER_URL}" >&2
echo "  daemons: ${REPLICATOR_DAEMON_ENDPOINTS}" >&2
echo "  poll: ${REPLICATOR_POLL_INTERVAL:-30s} (set REPLICATOR_POLL_INTERVAL to change)" >&2
echo "  logs: REPLICATOR_LOG_FORMAT=${REPLICATOR_LOG_FORMAT:-json} REPLICATOR_LOG_LEVEL=${REPLICATOR_LOG_LEVEL:-info}" >&2
exec "${BIN}"
