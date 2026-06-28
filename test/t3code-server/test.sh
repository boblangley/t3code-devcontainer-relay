#!/bin/bash
# test.sh — devcontainer features test for t3code-server
#
# Run by `devcontainer features test` against a container built from the
# t3code-server feature on mcr.microsoft.com/devcontainers/base:noble.
#
# Test lib convention: source dev-container-features-test-lib, then call
# `check "<description>" <command>` for each assertion.  At the end call
# `reportResults` which exits 0 on all-pass or 1 on any failure.
#
# Usage (from repo root, with devcontainer CLI installed):
#   devcontainer features test \
#     --features t3code-server \
#     --base-image mcr.microsoft.com/devcontainers/base:noble \
#     .
#
# NOTE ON SKIPPED ASSERTIONS:
# Tests that require a successfully downloaded artifact (e.g. checking that
# the server binary exists in /usr/local/lib/t3code-server, or that the Node
# process actually starts) are SKIPPED here because no published release exists
# until build-t3code-artifacts.yaml has run and attached assets.
# Those tests are marked with comments indicating what they would assert.

set -e

# ---------------------------------------------------------------------------
# Load the shared test library
# (provided by the devcontainer features test harness at test run time)
# ---------------------------------------------------------------------------

# shellcheck source=/dev/null
source dev-container-features-test-lib

# ---------------------------------------------------------------------------
# 1. Base image guard: verify we are on Ubuntu noble
# ---------------------------------------------------------------------------

check "os-release exists" \
    test -f /etc/os-release

check "distro is ubuntu" \
    bash -c '. /etc/os-release && [ "${ID}" = "ubuntu" ]'

check "codename is noble" \
    bash -c '. /etc/os-release && [ "${VERSION_CODENAME}" = "noble" ]'

# ---------------------------------------------------------------------------
# 2. Node dependency (arrives via dependsOn → ghcr.io/devcontainers/features/node:1)
# ---------------------------------------------------------------------------

check "node is on PATH" \
    command -v node

check "node is executable" \
    node --version

# ---------------------------------------------------------------------------
# 3. Feature-installed files
# ---------------------------------------------------------------------------

check "supervise script is installed" \
    test -f /usr/local/share/t3code-supervise.sh

check "supervise script is executable" \
    test -x /usr/local/share/t3code-supervise.sh

check "entrypoint script is installed" \
    test -f /usr/local/share/t3code-entrypoint.sh

check "entrypoint script is executable" \
    test -x /usr/local/share/t3code-entrypoint.sh

check "tailscaled runner is installed" \
    test -f /usr/local/share/tailscaled-run.sh

check "tailscaled runner is executable" \
    test -x /usr/local/share/tailscaled-run.sh

check "t3relay helper is installed" \
    test -f /usr/local/bin/t3relay

check "t3relay helper is executable" \
    test -x /usr/local/bin/t3relay

check "env file is present" \
    test -f /usr/local/etc/t3code-server.env

check "env file is readable" \
    test -r /usr/local/etc/t3code-server.env

# Verify the env file contains the expected variable names (not their values,
# which depend on feature option overrides at test invocation time).
check "env file contains T3CODE_PORT" \
    grep -q "T3CODE_PORT" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_HOST" \
    grep -q "T3CODE_HOST" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_SECRETPATH" \
    grep -q "T3CODE_SECRETPATH" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_INSTALL_DIR" \
    grep -q "T3CODE_INSTALL_DIR" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_BASEDIR" \
    grep -q "T3CODE_BASEDIR" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_STATEPARENTDIR" \
    grep -q "T3CODE_STATEPARENTDIR" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_WORKSPACEHOME" \
    grep -q "T3CODE_WORKSPACEHOME" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_RUNASUSER" \
    grep -q "T3CODE_RUNASUSER" /usr/local/etc/t3code-server.env

check "env file contains T3CODE_SSHAUTHSOCK" \
    grep -q "T3CODE_SSHAUTHSOCK" /usr/local/etc/t3code-server.env

# ---------------------------------------------------------------------------
# 4. Install directory (populated only when the artifact was successfully
#    downloaded; skipped here because no release exists in CI until
#    build-t3code-artifacts.yaml has published one)
# ---------------------------------------------------------------------------
#
# SKIPPED: test -d /usr/local/lib/t3code-server
# SKIPPED: test -n "$(ls -A /usr/local/lib/t3code-server)"
# SKIPPED: node --check /usr/local/lib/t3code-server/index.js  (or dist/index.js)
#
# To enable these, ensure that a GitHub Release tagged t3code-server-<semver>
# exists on boblangley/t3code-devcontainer-relay with assets named:
#   t3code-server-linux-amd64.tar.gz
#   t3code-server-linux-arm64.tar.gz

# ---------------------------------------------------------------------------
# 5. Supervise script sanity: sourcing it without exec-ing the loop
#    (just check it is valid bash syntax)
# ---------------------------------------------------------------------------

check "supervise script has valid bash syntax" \
    bash -n /usr/local/share/t3code-supervise.sh

check "entrypoint script has valid bash syntax" \
    bash -n /usr/local/share/t3code-entrypoint.sh

check "entrypoint does not require s6 init" \
    bash -c '! grep -q "/init" /usr/local/share/t3code-entrypoint.sh'

check "supervise script exports SSH_AUTH_SOCK" \
    grep -q "SSH_AUTH_SOCK" /usr/local/share/t3code-supervise.sh

check "supervise script passes T3CODE_HOST" \
    grep -q "T3CODE_HOST" /usr/local/share/t3code-supervise.sh

check "supervise script watches VS Code SSH agent socket" \
    grep -q "vscode-ssh-auth-.*\\.sock" /usr/local/share/t3code-supervise.sh

check "t3relay helper has valid bash syntax" \
    bash -n /usr/local/bin/t3relay

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

reportResults
