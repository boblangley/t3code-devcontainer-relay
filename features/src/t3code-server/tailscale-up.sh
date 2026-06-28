#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="/usr/local/etc/t3code-server.env"
if [ -r "${ENV_FILE}" ]; then
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

T3CODE_TAILSCALE_ENABLED="${T3CODE_TAILSCALE_ENABLED:-true}"
T3CODE_TAILSCALE_AUTHKEY_PATH="${T3CODE_TAILSCALE_AUTHKEY_PATH:-/run/t3code/tailscale-authkey}"
T3CODE_TAILSCALE_HOSTNAME="${T3CODE_TAILSCALE_HOSTNAME:-}"

sanitize_hostname() {
    printf '%s' "${1:-}" \
        | tr '[:upper:]' '[:lower:]' \
        | tr -cs 'a-z0-9-' '-' \
        | sed -e 's/^-//' -e 's/-$//'
}

looks_like_docker_id() {
    printf '%s' "${1:-}" | grep -Eq '^[0-9a-f]{12}([0-9a-f]{52})?$'
}

if [ "${T3CODE_TAILSCALE_ENABLED}" != "true" ]; then
    echo "[tailscale-up] disabled by feature config"
    exit 0
fi

if [ ! -r "${T3CODE_TAILSCALE_AUTHKEY_PATH}" ]; then
    echo "[tailscale-up] auth key file not readable at ${T3CODE_TAILSCALE_AUTHKEY_PATH}; tailscaled will stay logged out"
    exit 0
fi

for _ in $(seq 1 30); do
    if tailscale status --json >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

hostname_arg=()
if [ -n "${T3CODE_TAILSCALE_HOSTNAME}" ]; then
    hostname_arg=(--hostname="${T3CODE_TAILSCALE_HOSTNAME}")
elif [ -n "${HOSTNAME:-}" ] && ! looks_like_docker_id "${HOSTNAME}"; then
    safe_name="$(sanitize_hostname "${HOSTNAME}")"
    if [ -n "${safe_name}" ]; then
        hostname_arg=(--hostname="${safe_name}")
    fi
fi

tailscale up \
    --auth-key="file:${T3CODE_TAILSCALE_AUTHKEY_PATH}" \
    --accept-dns=false \
    --ssh=false \
    "${hostname_arg[@]}"
