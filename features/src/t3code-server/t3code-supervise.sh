#!/usr/bin/env bash
# t3code-supervise.sh — devcontainer entrypoint + restart-loop supervisor
#
# Role: devcontainer entrypoint (declared in devcontainer-feature.json as
# "entrypoint").  The devcontainer CLI launches this script with the
# container's original CMD as positional arguments ("$@").  The standard
# pattern for a feature entrypoint is:
#   1. Start any background processes.
#   2. exec "$@" to hand off to the container's own command (or sleep infinity
#      if no command was provided), so the container stays alive.
#
# NOTE: s6-overlay was evaluated for supervision and rejected as overkill for
# supervising a single process.  If a second supervised process is ever added
# (e.g. a metrics exporter), revisit this decision then.
#
# Supervision strategy: a simple while-true restart loop with exponential
# backoff (capped), logging to /tmp/t3code-server.log, guarded by a PID file
# so a container restart (which re-runs the entrypoint) does not spawn a
# duplicate server if one is already running from a previous entrypoint
# invocation in the same container instance.

# Do NOT use set -e here: this is an entrypoint / supervisor script where we
# intentionally continue past non-zero exit codes (process restarts, PID checks).
# Errors are handled explicitly throughout.

# ---------------------------------------------------------------------------
# Source the env file written by install.sh
# ---------------------------------------------------------------------------

ENV_FILE="/usr/local/etc/t3code-server.env"

if [ ! -f "${ENV_FILE}" ]; then
    echo "ERROR [t3code-supervise]: env file not found at ${ENV_FILE}. Was the feature installed correctly?" >&2
    # Do not exit here — still exec "$@" so the container isn't bricked.
else
    # shellcheck source=/dev/null
    . "${ENV_FILE}"
fi

# Fall back to defaults if the env file was missing or incomplete.
T3CODE_INSTALL_DIR="${T3CODE_INSTALL_DIR:-/usr/local/lib/t3code-server}"
T3CODE_PORT="${T3CODE_PORT:-3773}"
T3CODE_SECRETPATH="${T3CODE_SECRETPATH:-/run/t3code/relay-secret}"
T3CODE_BASEDIR="${T3CODE_BASEDIR:-}"
T3CODE_STATEPARENTDIR="${T3CODE_STATEPARENTDIR:-}"
T3CODE_WORKSPACEHOME="${T3CODE_WORKSPACEHOME:-}"
T3CODE_RUNASUSER="${T3CODE_RUNASUSER:-vscode}"
T3CODE_SSHAUTHSOCK="${T3CODE_SSHAUTHSOCK:-/tmp/vscode-ssh-agent.sock}"

# ---------------------------------------------------------------------------
# Env vars passed to the t3code server process
#
# IMPORTANT COORDINATION POINT: The exact env var names below must match what
# the forked t3code server (vendor-t3code, branch bearer-auth) reads.  Those
# names are defined by the server's auth module — if they change there, update
# them here too.  They are collected in one place (this block) to make that
# easy.
#
#   PORT                     — TCP port the server listens on (0.0.0.0:PORT).
#   T3CODE_RELAY_SECRET_FILE — Path to the shared-secret file.  The server
#                              reads this file on startup and requires an
#                              X-Relay-Secret header on inbound requests whose
#                              value matches the file contents.
#   T3CODE_HOME              — Optional base directory for server state. When
#                              stateParentDir is configured, the supervisor
#                              derives this at container-start time from
#                              DEVCONTAINER_ID or HOSTNAME so multiple
#                              devcontainers can share one durable parent
#                              mount without sharing one SQLite DB.
#   T3CODE_RUNASUSER         — Optional Linux user for the server process. The
#                              supervisor runs as the entrypoint user, then
#                              drops privileges before starting Node when this
#                              is set and the entrypoint user is root.
#   SSH_AUTH_SOCK            — Stable in-container socket path for the forwarded
#                              SSH agent. VS Code injects the real socket under
#                              /tmp after container boot, so the supervisor keeps
#                              this path symlinked to the newest injected socket.
# ---------------------------------------------------------------------------

SERVER_PORT="${T3CODE_PORT}"
SERVER_SECRET_FILE="${T3CODE_SECRETPATH}"
SERVER_NODE_BIN="$(command -v node 2>/dev/null || true)"

_is_non_empty() {
    [ -n "${1:-}" ]
}

_trim_trailing_slashes() {
    local value="${1:-}"
    while [ "${value}" != "/" ] && [ "${value%/}" != "${value}" ]; do
        value="${value%/}"
    done
    printf '%s' "${value}"
}

_sanitize_path_segment() {
    printf '%s' "${1:-}" | tr '/:' '__'
}

_resolve_t3code_home() {
    if _is_non_empty "${T3CODE_BASEDIR}"; then
        printf '%s' "${T3CODE_BASEDIR}"
        return 0
    fi

    if _is_non_empty "${T3CODE_STATEPARENTDIR}"; then
        local parent
        local raw_id
        local state_id
        parent="$(_trim_trailing_slashes "${T3CODE_STATEPARENTDIR}")"
        raw_id="${DEVCONTAINER_ID:-${HOSTNAME:-unknown}}"
        state_id="$(_sanitize_path_segment "${raw_id}")"
        if ! _is_non_empty "${state_id}"; then
            state_id="unknown"
        fi
        printf '%s/%s' "${parent}" "${state_id}"
        return 0
    fi

    if _is_non_empty "${T3CODE_HOME:-}"; then
        printf '%s' "${T3CODE_HOME}"
    fi
}

_resolve_workspace_home() {
    if _is_non_empty "${T3CODE_WORKSPACEHOME}"; then
        printf '%s' "${T3CODE_WORKSPACEHOME}"
        return 0
    fi

    if _is_non_empty "${WORKSPACE_HOME:-}"; then
        printf '%s' "${WORKSPACE_HOME}"
    fi
}

SERVER_T3CODE_HOME="$(_resolve_t3code_home)"
SERVER_WORKSPACE_HOME="$(_resolve_workspace_home)"
SERVER_RUN_AS_USER="${T3CODE_RUNASUSER}"
SERVER_SSH_AUTH_SOCK="${T3CODE_SSHAUTHSOCK}"

_run_as_user_exists() {
    _is_non_empty "${SERVER_RUN_AS_USER}" && id "${SERVER_RUN_AS_USER}" >/dev/null 2>&1
}

_prepare_server_state_dir() {
    if ! _is_non_empty "${SERVER_T3CODE_HOME}"; then
        return 0
    fi

    mkdir -p "${SERVER_T3CODE_HOME}" 2>/dev/null || true

    if [ "$(id -u)" -eq 0 ] && _run_as_user_exists; then
        local run_as_group
        run_as_group="$(id -gn "${SERVER_RUN_AS_USER}" 2>/dev/null || printf '%s' "${SERVER_RUN_AS_USER}")"
        chown "${SERVER_RUN_AS_USER}:${run_as_group}" "${SERVER_T3CODE_HOME}" 2>/dev/null || true
    fi
}

_find_vscode_ssh_auth_sock() {
    local candidate
    local newest=""

    for candidate in /tmp/vscode-ssh-auth-*.sock; do
        if [ "${candidate}" = "/tmp/vscode-ssh-auth-*.sock" ]; then
            continue
        fi

        if [ ! -S "${candidate}" ]; then
            continue
        fi

        if [ -z "${newest}" ] || [ "${candidate}" -nt "${newest}" ]; then
            newest="${candidate}"
        fi
    done

    printf '%s' "${newest}"
}

_repair_ssh_auth_sock_symlink() {
    if ! _is_non_empty "${SERVER_SSH_AUTH_SOCK}"; then
        return 1
    fi

    local target
    target="$(_find_vscode_ssh_auth_sock)"
    if ! _is_non_empty "${target}"; then
        return 1
    fi

    local parent_dir
    parent_dir="$(dirname "${SERVER_SSH_AUTH_SOCK}")"
    mkdir -p "${parent_dir}" 2>/dev/null || true

    if [ -L "${SERVER_SSH_AUTH_SOCK}" ]; then
        local current_target
        current_target="$(readlink "${SERVER_SSH_AUTH_SOCK}" 2>/dev/null || true)"
        if [ "${current_target}" = "${target}" ]; then
            return 0
        fi
    elif [ -e "${SERVER_SSH_AUTH_SOCK}" ]; then
        echo "[t3code-supervise] $(date -u +%FT%TZ) SSH_AUTH_SOCK stable path exists and is not a symlink: ${SERVER_SSH_AUTH_SOCK}" >> "${LOG_FILE}" 2>&1
        return 1
    fi

    ln -sfn "${target}" "${SERVER_SSH_AUTH_SOCK}" 2>/dev/null || return 1
    echo "[t3code-supervise] $(date -u +%FT%TZ) Linked SSH_AUTH_SOCK ${SERVER_SSH_AUTH_SOCK} -> ${target}" >> "${LOG_FILE}" 2>&1
}

_run_ssh_auth_sock_watcher() {
    local interval=2
    echo "${BASHPID}" > "${WATCHER_PID_FILE}"
    echo "[t3code-supervise] SSH_AUTH_SOCK watcher started (PID ${BASHPID}, stablePath=${SERVER_SSH_AUTH_SOCK:-<disabled>})" >> "${LOG_FILE}" 2>&1

    while true; do
        _repair_ssh_auth_sock_symlink || true
        sleep "${interval}"
    done
}

_run_server_command() {
    local server_args=("$@")
    local env_args=(
        "PORT=${SERVER_PORT}"
        "T3CODE_RELAY_SECRET_FILE=${SERVER_SECRET_FILE}"
    )

    if _is_non_empty "${SERVER_SSH_AUTH_SOCK}"; then
        env_args+=("SSH_AUTH_SOCK=${SERVER_SSH_AUTH_SOCK}")
    fi

    if _is_non_empty "${SERVER_T3CODE_HOME}"; then
        env_args+=("T3CODE_HOME=${SERVER_T3CODE_HOME}")
    fi

    if [ "$(id -u)" -eq 0 ] && _run_as_user_exists; then
        runuser -u "${SERVER_RUN_AS_USER}" -- env "${env_args[@]}" "${SERVER_NODE_BIN}" "${SERVER_ENTRYPOINT}" "${server_args[@]}"
        return $?
    fi

    env "${env_args[@]}" "${SERVER_NODE_BIN}" "${SERVER_ENTRYPOINT}" "${server_args[@]}"
}

# ---------------------------------------------------------------------------
# Locate the server entrypoint inside the install dir
# The forked server is a Node app; the main entrypoint is expected at one
# of these paths (first match wins).  Adjust if the build output changes.
# ---------------------------------------------------------------------------

# Confirmed against vendor-t3code @ bearer-auth: package "t3" bin = dist/bin.mjs
# (`start` = `node dist/bin.mjs`). Other paths kept as fallbacks in case the
# build layout changes.
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

# ---------------------------------------------------------------------------
# PID-file guard
#
# If the container is restarted (but not rebuilt) the entrypoint re-runs while
# the supervisor loop from the previous run may still be alive in the same
# process namespace.  Check for that before starting another loop.
# ---------------------------------------------------------------------------

PID_FILE="/tmp/t3code-server.pid"
WATCHER_PID_FILE="/tmp/t3code-ssh-auth-sock-watcher.pid"
LOG_FILE="/tmp/t3code-server.log"

_supervisor_already_running() {
    if [ -f "${PID_FILE}" ]; then
        local stored_pid
        stored_pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
        if [ -n "${stored_pid}" ] && kill -0 "${stored_pid}" 2>/dev/null; then
            return 0  # still alive
        fi
    fi
    return 1
}

_watcher_already_running() {
    if [ -f "${WATCHER_PID_FILE}" ]; then
        local stored_pid
        stored_pid="$(cat "${WATCHER_PID_FILE}" 2>/dev/null || true)"
        if [ -n "${stored_pid}" ] && kill -0 "${stored_pid}" 2>/dev/null; then
            return 0
        fi
    fi
    return 1
}

# ---------------------------------------------------------------------------
# Restart-loop supervisor (runs in a background subshell)
# ---------------------------------------------------------------------------

_run_supervisor() {
    # Write the subshell's own PID to the PID file so the guard above can
    # detect a running supervisor across entrypoint re-invocations.
    # NOTE: $$ in a subshell still expands to the *parent* shell's PID in bash.
    # $BASHPID gives the actual subshell PID, which is what we need here.
    echo "${BASHPID}" > "${PID_FILE}"

    local backoff=1
    local max_backoff=30

    echo "[t3code-supervise] Supervisor started (PID ${BASHPID}, port=${SERVER_PORT}, secretPath=${SERVER_SECRET_FILE}, t3codeHome=${SERVER_T3CODE_HOME:-<server-default>}, workspaceHome=${SERVER_WORKSPACE_HOME:-<server-default>}, runAsUser=${SERVER_RUN_AS_USER:-<entrypoint-user>}, sshAuthSock=${SERVER_SSH_AUTH_SOCK:-<disabled>})" >> "${LOG_FILE}" 2>&1

    while true; do
        if [ -z "${SERVER_ENTRYPOINT}" ]; then
            echo "[t3code-supervise] $(date -u +%FT%TZ) Server entrypoint not found in ${T3CODE_INSTALL_DIR}. Retrying in ${backoff}s..." >> "${LOG_FILE}" 2>&1
        elif [ -z "${SERVER_NODE_BIN}" ]; then
            echo "[t3code-supervise] $(date -u +%FT%TZ) Node executable not found on PATH. Retrying in ${backoff}s..." >> "${LOG_FILE}" 2>&1
        else
            local server_args=()
            if _is_non_empty "${SERVER_WORKSPACE_HOME}"; then
                server_args+=("${SERVER_WORKSPACE_HOME}")
            fi

            if [ "$(id -u)" -eq 0 ] && _is_non_empty "${SERVER_RUN_AS_USER}" && ! _run_as_user_exists; then
                echo "[t3code-supervise] $(date -u +%FT%TZ) Configured runAsUser '${SERVER_RUN_AS_USER}' does not exist. Starting server as root." >> "${LOG_FILE}" 2>&1
            fi

            _prepare_server_state_dir

            echo "[t3code-supervise] $(date -u +%FT%TZ) Starting server as ${SERVER_RUN_AS_USER:-entrypoint user}: ${SERVER_NODE_BIN} ${SERVER_ENTRYPOINT}${SERVER_WORKSPACE_HOME:+ ${SERVER_WORKSPACE_HOME}}" >> "${LOG_FILE}" 2>&1

            # Run the server; errors are logged; the loop continues regardless.
            _run_server_command "${server_args[@]}" >> "${LOG_FILE}" 2>&1 || true

            echo "[t3code-supervise] $(date -u +%FT%TZ) Server exited (backoff ${backoff}s)" >> "${LOG_FILE}" 2>&1
        fi

        sleep "${backoff}"

        # Exponential backoff, capped at max_backoff seconds.
        backoff=$(( backoff * 2 ))
        if [ "${backoff}" -gt "${max_backoff}" ]; then
            backoff="${max_backoff}"
        fi
    done
}

# ---------------------------------------------------------------------------
# Main: start support loops in background, then exec the container command
# ---------------------------------------------------------------------------

if _is_non_empty "${SERVER_SSH_AUTH_SOCK}"; then
    if _watcher_already_running; then
        echo "[t3code-supervise] SSH_AUTH_SOCK watcher is already running (PID $(cat "${WATCHER_PID_FILE}" 2>/dev/null)). Skipping start." >&2
    else
        ( _run_ssh_auth_sock_watcher ) &
        echo "[t3code-supervise] SSH_AUTH_SOCK watcher launched in background." >&2
    fi
fi

if _supervisor_already_running; then
    echo "[t3code-supervise] Supervisor is already running (PID $(cat "${PID_FILE}" 2>/dev/null)). Skipping start." >&2
else
    # Launch the restart loop in a background subshell.  The subshell writes
    # its own PID to the PID file immediately inside _run_supervisor.
    ( _run_supervisor ) &

    echo "[t3code-supervise] Supervisor launched in background." >&2
fi

# Hand off to the container's own command.  If no command was provided (e.g.
# the container was started with no CMD), fall back to sleep infinity so the
# container stays alive and the supervisor loop keeps running.
if [ "$#" -eq 0 ]; then
    exec sleep infinity
else
    exec "$@"
fi
