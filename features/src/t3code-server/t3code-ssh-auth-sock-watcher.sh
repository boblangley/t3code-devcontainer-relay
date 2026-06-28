#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="/usr/local/etc/t3code-server.env"
if [ -r "${ENV_FILE}" ]; then
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

T3CODE_SSHAUTHSOCK="${T3CODE_SSHAUTHSOCK:-/tmp/vscode-ssh-agent.sock}"
if [ -z "${T3CODE_SSHAUTHSOCK}" ]; then
    exec sleep infinity
fi

find_vscode_ssh_auth_sock() {
    local candidate
    local newest=""
    for candidate in /tmp/vscode-ssh-auth-*.sock; do
        if [ "${candidate}" = "/tmp/vscode-ssh-auth-*.sock" ] || [ ! -S "${candidate}" ]; then
            continue
        fi
        if [ -z "${newest}" ] || [ "${candidate}" -nt "${newest}" ]; then
            newest="${candidate}"
        fi
    done
    printf '%s' "${newest}"
}

while true; do
    target="$(find_vscode_ssh_auth_sock)"
    if [ -n "${target}" ]; then
        parent_dir="$(dirname "${T3CODE_SSHAUTHSOCK}")"
        mkdir -p "${parent_dir}" 2>/dev/null || true
        if [ -L "${T3CODE_SSHAUTHSOCK}" ] || [ ! -e "${T3CODE_SSHAUTHSOCK}" ]; then
            ln -sfn "${target}" "${T3CODE_SSHAUTHSOCK}" 2>/dev/null || true
        else
            echo "[t3code-ssh-auth-sock-watcher] stable path exists and is not a symlink: ${T3CODE_SSHAUTHSOCK}" >&2
        fi
    fi
    sleep 2
done
