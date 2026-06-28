#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="/usr/local/etc/t3code-server.env"
if [ -r "${ENV_FILE}" ]; then
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

T3CODE_INSTALL_DIR="${T3CODE_INSTALL_DIR:-/usr/local/lib/t3code-server}"
T3CODE_PORT="${T3CODE_PORT:-3773}"
T3CODE_HOST="${T3CODE_HOST:-0.0.0.0}"
T3CODE_SECRETPATH="${T3CODE_SECRETPATH:-/run/t3code/relay-secret}"
T3CODE_BASEDIR="${T3CODE_BASEDIR:-}"
T3CODE_STATEPARENTDIR="${T3CODE_STATEPARENTDIR:-}"
T3CODE_WORKSPACEHOME="${T3CODE_WORKSPACEHOME:-}"
T3CODE_RUNASUSER="${T3CODE_RUNASUSER:-vscode}"
T3CODE_SSHAUTHSOCK="${T3CODE_SSHAUTHSOCK:-/tmp/vscode-ssh-agent.sock}"
T3CODE_TAILSCALE_ENABLED="${T3CODE_TAILSCALE_ENABLED:-true}"
T3CODE_TAILSCALE_AUTHKEY_PATH="${T3CODE_TAILSCALE_AUTHKEY_PATH:-/run/t3code/tailscale-authkey}"
T3CODE_TAILSCALE_SERVE_ENABLED="${T3CODE_TAILSCALE_SERVE_ENABLED:-true}"
T3CODE_TAILSCALE_SERVE_PORT="${T3CODE_TAILSCALE_SERVE_PORT:-443}"

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

resolve_t3code_home() {
    if is_non_empty "${T3CODE_BASEDIR}"; then
        printf '%s' "${T3CODE_BASEDIR}"
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
        printf '%s/%s' "${parent}" "${state_id}"
        return 0
    fi

    if is_non_empty "${T3CODE_HOME:-}"; then
        printf '%s' "${T3CODE_HOME}"
    fi
}

resolve_workspace_home() {
    if is_non_empty "${T3CODE_WORKSPACEHOME}"; then
        printf '%s' "${T3CODE_WORKSPACEHOME}"
        return 0
    fi

    if is_non_empty "${WORKSPACE_HOME:-}"; then
        printf '%s' "${WORKSPACE_HOME}"
    fi
}

run_as_user_exists() {
    is_non_empty "${T3CODE_RUNASUSER}" && id "${T3CODE_RUNASUSER}" >/dev/null 2>&1
}

resolve_tailscale_dns_name() {
    if is_non_empty "${T3CODE_TAILNET_DNS_NAME:-}"; then
        printf '%s' "${T3CODE_TAILNET_DNS_NAME}"
        return 0
    fi

    if [ "${T3CODE_TAILSCALE_ENABLED}" != "true" ] || ! command -v tailscale >/dev/null 2>&1; then
        return 0
    fi

    local status_json
    status_json="$(tailscale status --json 2>/dev/null || true)"
    if ! is_non_empty "${status_json}"; then
        return 0
    fi

    node -e '
const raw = process.argv[1] || "";
try {
  const parsed = JSON.parse(raw);
  const dnsName = String(parsed?.Self?.DNSName || "").trim().replace(/\.$/u, "");
  if (dnsName) process.stdout.write(dnsName);
} catch {}
' "${status_json}"
}

wait_for_tailscale_dns_name() {
    local dns_name
    local attempts=30
    while [ "${attempts}" -gt 0 ]; do
        dns_name="$(resolve_tailscale_dns_name)"
        if is_non_empty "${dns_name}"; then
            printf '%s' "${dns_name}"
            return 0
        fi
        attempts=$((attempts - 1))
        sleep 1
    done
}

SERVER_T3CODE_HOME="$(resolve_t3code_home)"
SERVER_WORKSPACE_HOME="$(resolve_workspace_home)"
if is_non_empty "${T3CODE_TAILNET_DNS_NAME:-}" || { [ "${T3CODE_TAILSCALE_ENABLED}" = "true" ] && [ -r "${T3CODE_TAILSCALE_AUTHKEY_PATH}" ]; }; then
    SERVER_TAILNET_DNS_NAME="$(wait_for_tailscale_dns_name)"
else
    SERVER_TAILNET_DNS_NAME=""
fi
SERVER_NODE_BIN="$(command -v node 2>/dev/null || true)"

if [ -z "${SERVER_NODE_BIN}" ]; then
    echo "ERROR [t3code-server]: node is not on PATH" >&2
    exit 127
fi

CANDIDATE_ENTRYPOINTS=(
    "${T3CODE_INSTALL_DIR}/dist/bin.mjs"
    "${T3CODE_INSTALL_DIR}/bin.mjs"
    "${T3CODE_INSTALL_DIR}/dist/index.js"
    "${T3CODE_INSTALL_DIR}/index.js"
)

SERVER_ENTRYPOINT=""
for candidate in "${CANDIDATE_ENTRYPOINTS[@]}"; do
    if [ -f "${candidate}" ]; then
        SERVER_ENTRYPOINT="${candidate}"
        break
    fi
done

if [ -z "${SERVER_ENTRYPOINT}" ]; then
    echo "ERROR [t3code-server]: server entrypoint not found in ${T3CODE_INSTALL_DIR}" >&2
    exit 127
fi

if is_non_empty "${SERVER_T3CODE_HOME}"; then
    mkdir -p "${SERVER_T3CODE_HOME}" 2>/dev/null || true
    if [ "$(id -u)" -eq 0 ] && run_as_user_exists; then
        run_as_group="$(id -gn "${T3CODE_RUNASUSER}" 2>/dev/null || printf '%s' "${T3CODE_RUNASUSER}")"
        chown "${T3CODE_RUNASUSER}:${run_as_group}" "${SERVER_T3CODE_HOME}" 2>/dev/null || true
    fi
fi

env_args=(
    "PORT=${T3CODE_PORT}"
    "T3CODE_HOST=${T3CODE_HOST}"
    "T3CODE_RELAY_SECRET_FILE=${T3CODE_SECRETPATH}"
    "T3CODE_TAILSCALE_SERVE=${T3CODE_TAILSCALE_SERVE_ENABLED}"
    "T3CODE_TAILSCALE_SERVE_PORT=${T3CODE_TAILSCALE_SERVE_PORT}"
)

if is_non_empty "${T3CODE_SSHAUTHSOCK}"; then
    env_args+=("SSH_AUTH_SOCK=${T3CODE_SSHAUTHSOCK}")
fi
if is_non_empty "${SERVER_T3CODE_HOME}"; then
    env_args+=("T3CODE_HOME=${SERVER_T3CODE_HOME}")
fi
if is_non_empty "${SERVER_TAILNET_DNS_NAME}"; then
    env_args+=("T3CODE_TAILNET_DNS_NAME=${SERVER_TAILNET_DNS_NAME}")
fi

server_args=()
if is_non_empty "${SERVER_WORKSPACE_HOME}"; then
    server_args+=("${SERVER_WORKSPACE_HOME}")
fi

echo "[t3code-server] starting ${SERVER_NODE_BIN} ${SERVER_ENTRYPOINT}${SERVER_WORKSPACE_HOME:+ ${SERVER_WORKSPACE_HOME}}"
if [ "$(id -u)" -eq 0 ] && run_as_user_exists; then
    exec runuser -u "${T3CODE_RUNASUSER}" -- env "${env_args[@]}" "${SERVER_NODE_BIN}" "${SERVER_ENTRYPOINT}" "${server_args[@]}"
fi

exec env "${env_args[@]}" "${SERVER_NODE_BIN}" "${SERVER_ENTRYPOINT}" "${server_args[@]}"
