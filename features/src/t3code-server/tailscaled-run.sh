#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="/usr/local/etc/t3code-server.env"
if [ -r "${ENV_FILE}" ]; then
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

T3CODE_TAILSCALE_ENABLED="${T3CODE_TAILSCALE_ENABLED:-true}"
T3CODE_TAILSCALE_STATE_DIR="${T3CODE_TAILSCALE_STATE_DIR:-}"
T3CODE_BASEDIR="${T3CODE_BASEDIR:-}"
T3CODE_STATEPARENTDIR="${T3CODE_STATEPARENTDIR:-}"

is_non_empty() {
    [ -n "${1:-}" ]
}

trim_trailing_slashes() {
    local value="${1:-}"
    while [ "${value}" != "/" ] && [ "${value%/}" != "${value}" ]; do
        value="${value%/}"
    done
    printf '%s' "${value}"
}

sanitize_path_segment() {
    printf '%s' "${1:-}" | tr '/:' '__'
}

resolve_tailscale_state_dir() {
    if is_non_empty "${T3CODE_TAILSCALE_STATE_DIR}"; then
        printf '%s' "${T3CODE_TAILSCALE_STATE_DIR}"
        return 0
    fi

    if is_non_empty "${T3CODE_BASEDIR}"; then
        printf '%s/tailscale' "$(trim_trailing_slashes "${T3CODE_BASEDIR}")"
        return 0
    fi

    if is_non_empty "${T3CODE_STATEPARENTDIR}"; then
        local parent
        local raw_id
        local state_id
        parent="$(trim_trailing_slashes "${T3CODE_STATEPARENTDIR}")"
        raw_id="${DEVCONTAINER_ID:-${HOSTNAME:-unknown}}"
        state_id="$(sanitize_path_segment "${raw_id}")"
        if ! is_non_empty "${state_id}"; then
            state_id="unknown"
        fi
        printf '%s/%s/tailscale' "${parent}" "${state_id}"
        return 0
    fi

    printf '%s' "/var/lib/tailscale"
}

if [ "${T3CODE_TAILSCALE_ENABLED}" != "true" ]; then
    echo "[tailscaled] disabled by feature config"
    exec sleep infinity
fi

TAILSCALE_STATE_DIR="$(resolve_tailscale_state_dir)"
mkdir -p /var/run/tailscale "${TAILSCALE_STATE_DIR}"

(
    /usr/local/share/tailscale-up.sh || true
) &

exec tailscaled \
    --tun=userspace-networking \
    --socket=/var/run/tailscale/tailscaled.sock \
    --statedir="${TAILSCALE_STATE_DIR}"
