#!/usr/bin/env bash
# install.sh — devcontainer feature installer for t3code-server
#
# Called by the devcontainer CLI during container build.  The feature options
# are injected as environment variables by the CLI (uppercased option IDs):
#   VERSION        — artifact tag, default "latest"
#   PORT           — listen port, default "3773"
#   SECRETPATH     — secret file path, default "/run/t3code/relay-secret"
#   BASEDIR        — explicit T3CODE_HOME override, default ""
#   STATEPARENTDIR — durable parent for per-devcontainer T3CODE_HOME, default ""
#   WORKSPACEHOME  — explicit server cwd override, default ""
#   RUNASUSER      — runtime user for the server process, default "vscode"
#   SSHAUTHSOCK    — stable SSH agent socket path exposed to the server process
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

# Preserve feature options before sourcing /etc/os-release below. Ubuntu's
# os-release defines VERSION="24.04.x LTS (...)", which would otherwise
# overwrite the devcontainer feature's `version` option.
FEATURE_VERSION="${VERSION:-latest}"
FEATURE_PORT="${PORT:-3773}"
FEATURE_SECRETPATH="${SECRETPATH:-/run/t3code/relay-secret}"
FEATURE_BASEDIR="${BASEDIR:-}"
FEATURE_STATEPARENTDIR="${STATEPARENTDIR:-}"
FEATURE_WORKSPACEHOME="${WORKSPACEHOME:-}"
FEATURE_RUNASUSER="${RUNASUSER:-vscode}"
FEATURE_SSHAUTHSOCK="${SSHAUTHSOCK:-/tmp/vscode-ssh-agent.sock}"

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

VERSION="${FEATURE_VERSION}"
PORT="${FEATURE_PORT}"
SECRETPATH="${FEATURE_SECRETPATH}"
BASEDIR="${FEATURE_BASEDIR}"
STATEPARENTDIR="${FEATURE_STATEPARENTDIR}"
WORKSPACEHOME="${FEATURE_WORKSPACEHOME}"
RUNASUSER="${FEATURE_RUNASUSER}"
SSHAUTHSOCK="${FEATURE_SSHAUTHSOCK}"

info "Installing t3code-server version='${VERSION}' port='${PORT}' secretPath='${SECRETPATH}'"
if [ -n "${BASEDIR}" ]; then
    info "Using explicit baseDir='${BASEDIR}'"
elif [ -n "${STATEPARENTDIR}" ]; then
    info "Using stateParentDir='${STATEPARENTDIR}'"
fi
if [ -n "${WORKSPACEHOME}" ]; then
    info "Using workspaceHome='${WORKSPACEHOME}'"
fi
if [ -n "${RUNASUSER}" ]; then
    info "Server process will run as user '${RUNASUSER}' when possible"
fi
if [ -n "${SSHAUTHSOCK}" ]; then
    info "Server process will use stable SSH_AUTH_SOCK='${SSHAUTHSOCK}'"
fi

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
# When VERSION == "latest" we use the explicit t3code-server-latest release
# alias. Do not use GitHub's /releases/latest path: this repo also publishes
# devcontainer feature releases, and those may become the repository's latest
# release even though they do not contain server tarballs.
#
# IMPORTANT: The asset filename below must exactly match what
# build-t3code-artifacts.yaml attaches to the release.  That workflow is
# maintained separately; if the naming changes there, update this URL.
# ---------------------------------------------------------------------------

REPO_OWNER="boblangley"
REPO_NAME="t3code-devcontainer-relay"
ASSET_NAME="t3code-server-linux-${ARCH}.tar.gz"

if [ "${VERSION}" = "latest" ]; then
    DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/t3code-server-latest/${ASSET_NAME}"
else
    case "${VERSION}" in
        t3code-server-*) RELEASE_TAG="${VERSION}" ;;
        *)              RELEASE_TAG="t3code-server-${VERSION}" ;;
    esac
    DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${RELEASE_TAG}/${ASSET_NAME}"
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
    err "Failed to download artifact from ${DOWNLOAD_URL}. Ensure the server artifact release exists and the asset '${ASSET_NAME}' is attached to it. The build-t3code-artifacts.yaml workflow in this repo produces these assets."
fi

info "Extracting to ${INSTALL_DIR} ..."
mkdir -p "${INSTALL_DIR}"
tar -xzf "${TMP_DIR}/${ASSET_NAME}" -C "${INSTALL_DIR}" --strip-components=1

if [ -z "$(ls -A "${INSTALL_DIR}" 2>/dev/null)" ]; then
    err "Install directory ${INSTALL_DIR} is empty after extraction. The artifact may be malformed."
fi

if [ ! -f "${INSTALL_DIR}/dist/bin.mjs" ]; then
    err "Server entrypoint not found at ${INSTALL_DIR}/dist/bin.mjs. The artifact may be malformed or built with an unexpected output layout."
fi

for package_name in "effect" "@effect/platform-node"; do
    if ! node -e "require.resolve(process.argv[2], { paths: [process.argv[1]] })" "${INSTALL_DIR}" "${package_name}" >/dev/null 2>&1; then
        err "Server artifact is missing production dependencies; could not resolve '${package_name}' from ${INSTALL_DIR}. Rebuild the server artifact with node_modules included."
    fi
done

info "Server installed to ${INSTALL_DIR}"

# ---------------------------------------------------------------------------
# Install the supervise script
# ---------------------------------------------------------------------------

SUPERVISE_SRC="${FEATURE_DIR}/t3code-supervise.sh"
SUPERVISE_DEST="/usr/local/share/t3code-supervise.sh"
HELPER_SRC="${FEATURE_DIR}/t3relay"
HELPER_DEST="/usr/local/bin/t3relay"

if [ ! -f "${SUPERVISE_SRC}" ]; then
    err "t3code-supervise.sh not found at expected path '${SUPERVISE_SRC}'. This is a feature packaging error."
fi

cp "${SUPERVISE_SRC}" "${SUPERVISE_DEST}"
chmod +x "${SUPERVISE_DEST}"
info "Supervise script installed to ${SUPERVISE_DEST}"

if [ ! -f "${HELPER_SRC}" ]; then
    err "t3relay helper not found at expected path '${HELPER_SRC}'. This is a feature packaging error."
fi

cp "${HELPER_SRC}" "${HELPER_DEST}"
chmod +x "${HELPER_DEST}"
info "t3relay helper installed to ${HELPER_DEST}"

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
T3CODE_BASEDIR="${BASEDIR}"
T3CODE_STATEPARENTDIR="${STATEPARENTDIR}"
T3CODE_WORKSPACEHOME="${WORKSPACEHOME}"
T3CODE_RUNASUSER="${RUNASUSER}"
T3CODE_SSHAUTHSOCK="${SSHAUTHSOCK}"
EOF

chmod 644 /usr/local/etc/t3code-server.env
info "Env file written to /usr/local/etc/t3code-server.env"

info "t3code-server feature installation complete."
