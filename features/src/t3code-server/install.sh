#!/usr/bin/env bash
# install.sh — devcontainer feature installer for t3code-server
#
# Called by the devcontainer CLI during container build.  The feature options
# are injected as environment variables by the CLI (uppercased option IDs):
#   VERSION     — artifact tag, default "latest"
#   PORT        — listen port, default "3773"
#   SECRETPATH  — secret file path, default "/run/t3code/relay-secret"
#
# Supported base image: mcr.microsoft.com/devcontainers/base:noble ONLY.
# This script assumes Ubuntu 24.04 (glibc, apt, bash) and will fail fast
# on any other distro.

set -e

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

err() { echo "ERROR [t3code-server feature]: $*" >&2; exit 1; }
info() { echo "INFO  [t3code-server feature]: $*"; }

# ---------------------------------------------------------------------------
# Guard 1: Ubuntu noble (24.04) only
# ---------------------------------------------------------------------------

if [ ! -f /etc/os-release ]; then
    err "/etc/os-release not found. This feature only supports mcr.microsoft.com/devcontainers/base:noble (Ubuntu 24.04)."
fi

# shellcheck source=/dev/null
. /etc/os-release

if [ "${ID:-}" != "ubuntu" ] || [ "${VERSION_CODENAME:-}" != "noble" ]; then
    err "Unsupported base image: ${PRETTY_NAME:-unknown}. This feature requires Ubuntu 24.04 noble (mcr.microsoft.com/devcontainers/base:noble). Detected: ID=${ID:-?} VERSION_CODENAME=${VERSION_CODENAME:-?}"
fi

info "Base image check passed: Ubuntu noble (24.04)"

# ---------------------------------------------------------------------------
# Guard 2: node must be on PATH (provided by dependsOn node feature)
# ---------------------------------------------------------------------------

if ! command -v node >/dev/null 2>&1; then
    err "'node' is not on PATH. The t3code-server feature declares 'dependsOn' on ghcr.io/devcontainers/features/node:1, which should install Node automatically. If you are using this feature standalone, add the node feature to your devcontainer.json features block first."
fi

NODE_VERSION="$(node --version)"
info "Node check passed: ${NODE_VERSION}"

# ---------------------------------------------------------------------------
# Resolve options (devcontainer CLI uppercases option ids)
# ---------------------------------------------------------------------------

VERSION="${VERSION:-latest}"
PORT="${PORT:-3773}"
SECRETPATH="${SECRETPATH:-/run/t3code/relay-secret}"

info "Installing t3code-server version='${VERSION}' port='${PORT}' secretPath='${SECRETPATH}'"

# ---------------------------------------------------------------------------
# Determine target architecture
# ---------------------------------------------------------------------------

UNAME_M="$(uname -m)"
case "${UNAME_M}" in
    x86_64)   ARCH="amd64" ;;
    aarch64)  ARCH="arm64" ;;
    *)
        err "Unsupported architecture: ${UNAME_M}. Only x86_64 (amd64) and aarch64 (arm64) are supported."
        ;;
esac

info "Target architecture: ${ARCH}"

# ---------------------------------------------------------------------------
# Construct the download URL
#
# Artifact convention (must match build-t3code-artifacts.yaml naming):
#   Release tag:   t3code-server-<semver>   (e.g. t3code-server-v1.2.3)
#                  or the floating alias "latest"
#   Asset name:    t3code-server-linux-<arch>.tar.gz
#
# When VERSION == "latest" we use the /releases/latest/download/ alias path
# so we always pull the most recent published release without needing to
# resolve the tag first.
#
# IMPORTANT: The asset filename below must exactly match what
# build-t3code-artifacts.yaml attaches to the release.  That workflow is
# maintained separately; if the naming changes there, update this URL.
# ---------------------------------------------------------------------------

REPO_OWNER="boblangley"
REPO_NAME="t3code-devcontainer-relay"
ASSET_NAME="t3code-server-linux-${ARCH}.tar.gz"

if [ "${VERSION}" = "latest" ]; then
    DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/${ASSET_NAME}"
else
    DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${VERSION}/${ASSET_NAME}"
fi

info "Download URL: ${DOWNLOAD_URL}"

# ---------------------------------------------------------------------------
# Download and install the server artifact
# ---------------------------------------------------------------------------

INSTALL_DIR="/usr/local/lib/t3code-server"
FEATURE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP_DIR="$(mktemp -d)"

# Ensure cleanup of temp dir on exit (success or failure)
trap 'rm -rf "${TMP_DIR}"' EXIT

info "Downloading artifact..."
if ! curl -fsSL "${DOWNLOAD_URL}" -o "${TMP_DIR}/${ASSET_NAME}"; then
    err "Failed to download artifact from ${DOWNLOAD_URL}. Ensure the release '${VERSION}' exists and the asset '${ASSET_NAME}' is attached to it. The build-t3code-artifacts.yaml workflow in this repo produces these assets."
fi

info "Extracting to ${INSTALL_DIR} ..."
mkdir -p "${INSTALL_DIR}"
tar -xzf "${TMP_DIR}/${ASSET_NAME}" -C "${INSTALL_DIR}" --strip-components=1

# Verify extraction produced some content.
# The tar archive is expected to contain an index.js or similar Node entrypoint.
# Adjust this check if the build output layout changes.
if [ -z "$(ls -A "${INSTALL_DIR}" 2>/dev/null)" ]; then
    err "Install directory ${INSTALL_DIR} is empty after extraction. The artifact may be malformed."
fi

info "Server installed to ${INSTALL_DIR}"

# ---------------------------------------------------------------------------
# Install the supervise script
# ---------------------------------------------------------------------------

SUPERVISE_SRC="${FEATURE_DIR}/t3code-supervise.sh"
SUPERVISE_DEST="/usr/local/share/t3code-supervise.sh"

if [ ! -f "${SUPERVISE_SRC}" ]; then
    err "t3code-supervise.sh not found at expected path '${SUPERVISE_SRC}'. This is a feature packaging error."
fi

cp "${SUPERVISE_SRC}" "${SUPERVISE_DEST}"
chmod +x "${SUPERVISE_DEST}"
info "Supervise script installed to ${SUPERVISE_DEST}"

# ---------------------------------------------------------------------------
# Write the env file (sourced by the supervise script at container start)
#
# This persists the resolved feature options so the entrypoint (which runs
# after the install phase) can read them without re-parsing devcontainer.json.
# ---------------------------------------------------------------------------

mkdir -p /usr/local/etc

cat > /usr/local/etc/t3code-server.env <<EOF
# Generated by t3code-server feature install.sh — do not edit manually.
# Re-running the container will re-source this file via t3code-supervise.sh.

T3CODE_INSTALL_DIR="${INSTALL_DIR}"
T3CODE_PORT="${PORT}"
T3CODE_SECRETPATH="${SECRETPATH}"
T3CODE_VERSION="${VERSION}"
EOF

chmod 644 /usr/local/etc/t3code-server.env
info "Env file written to /usr/local/etc/t3code-server.env"

info "t3code-server feature installation complete."
