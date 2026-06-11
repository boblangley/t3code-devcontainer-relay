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
# ---------------------------------------------------------------------------

SERVER_PORT="${T3CODE_PORT}"
SERVER_SECRET_FILE="${T3CODE_SECRETPATH}"

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

    echo "[t3code-supervise] Supervisor started (PID ${BASHPID}, port=${SERVER_PORT}, secretPath=${SERVER_SECRET_FILE})" >> "${LOG_FILE}" 2>&1

    while true; do
        if [ -z "${SERVER_ENTRYPOINT}" ]; then
            echo "[t3code-supervise] $(date -u +%FT%TZ) Server entrypoint not found in ${T3CODE_INSTALL_DIR}. Retrying in ${backoff}s..." >> "${LOG_FILE}" 2>&1
        else
            echo "[t3code-supervise] $(date -u +%FT%TZ) Starting server: node ${SERVER_ENTRYPOINT}" >> "${LOG_FILE}" 2>&1

            # Run the server; errors are logged; the loop continues regardless.
            PORT="${SERVER_PORT}" \
            T3CODE_RELAY_SECRET_FILE="${SERVER_SECRET_FILE}" \
            node "${SERVER_ENTRYPOINT}" >> "${LOG_FILE}" 2>&1 || true

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
# Main: start supervisor in background, then exec the container command
# ---------------------------------------------------------------------------

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
