#!/usr/bin/env bash
# Starts long-running feature services without requiring s6-overlay to be PID 1.
# Devcontainer feature entrypoints are composed into a shell chain, so this
# script must return after launching background work.
set -u

ENV_FILE="/usr/local/etc/t3code-server.env"
if [ -r "${ENV_FILE}" ]; then
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

T3CODE_TAILSCALE_ENABLED="${T3CODE_TAILSCALE_ENABLED:-true}"

is_running() {
    local pattern="${1:-}"
    [ -n "${pattern}" ] && pgrep -f "${pattern}" >/dev/null 2>&1
}

start_tailscaled() {
    if [ "${T3CODE_TAILSCALE_ENABLED}" != "true" ]; then
        echo "[t3code-entrypoint] tailscaled disabled by feature config" >&2
        return 0
    fi

    if is_running '[/]usr/sbin/tailscaled|[/]usr/bin/tailscaled|tailscaled --tun=userspace-networking'; then
        echo "[t3code-entrypoint] tailscaled already running" >&2
        return 0
    fi

    echo "[t3code-entrypoint] starting tailscaled" >&2
    nohup /usr/local/share/tailscaled-run.sh >/tmp/tailscaled.log 2>&1 &
}

start_tailscaled

if is_running '[/]usr/local/share/t3code-supervise.sh|[/]usr/local/lib/t3code-server/dist/bin.mjs'; then
    echo "[t3code-entrypoint] t3code supervisor/server already running" >&2
else
    echo "[t3code-entrypoint] starting t3code supervisor" >&2
    nohup /usr/local/share/t3code-supervise.sh true >/tmp/t3code-entrypoint.log 2>&1 &
fi

exit 0
