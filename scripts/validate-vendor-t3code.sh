#!/usr/bin/env bash
# ---
# relationships:
#   validates: ../vendor/t3code.yml
#   references: ../vendor-t3code
# ---

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MANIFEST="${ROOT_DIR}/vendor/t3code.yml"
SUBMODULE="${ROOT_DIR}/vendor-t3code"

err() {
  echo "ERROR [vendor-t3code]: $*" >&2
  exit 1
}

info() {
  echo "INFO  [vendor-t3code]: $*"
}

manifest_value() {
  local key="$1"
  awk -F':[[:space:]]*' -v key="${key}" '
    $1 ~ "^[[:space:]]*" key "$" {
      value = $2
      sub(/[[:space:]]+#.*/, "", value)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      print value
      exit
    }
  ' "${MANIFEST}"
}

if [[ ! -f "${MANIFEST}" ]]; then
  err "manifest not found: ${MANIFEST}"
fi

if [[ ! -d "${SUBMODULE}/.git" && ! -f "${SUBMODULE}/.git" ]]; then
  err "vendor-t3code submodule is not initialized"
fi

upstream_version="$(manifest_value upstreamVersion)"
fork_repository="$(manifest_value forkRepository)"
fork_branch="$(manifest_value forkBranch)"
fork_tag="$(manifest_value forkTag)"
fork_commit="$(manifest_value forkCommit)"
release_version="$(manifest_value releaseVersion)"

[[ -n "${upstream_version}" ]] || err "upstreamVersion is empty"
[[ -n "${fork_repository}" ]] || err "forkRepository is empty"
[[ -n "${fork_branch}" ]] || err "forkBranch is empty"
[[ -n "${fork_tag}" ]] || err "forkTag is empty"
[[ -n "${fork_commit}" ]] || err "forkCommit is empty"
[[ -n "${release_version}" ]] || err "releaseVersion is empty"

actual_commit="$(git -C "${SUBMODULE}" rev-parse HEAD)"
if [[ "${actual_commit}" != "${fork_commit}" ]]; then
  err "manifest forkCommit ${fork_commit} does not match vendor-t3code HEAD ${actual_commit}"
fi

remote_url="$(git -C "${SUBMODULE}" remote get-url origin)"
case "${remote_url}" in
  *"${fork_repository}"* | *"${fork_repository}.git"*) ;;
  *) err "vendor-t3code origin ${remote_url} does not look like ${fork_repository}" ;;
esac

if ! git -C "${SUBMODULE}" rev-parse -q --verify "refs/tags/${fork_tag}" >/dev/null; then
  info "fetching fork tag ${fork_tag}"
  git -C "${SUBMODULE}" fetch --depth=1 origin "refs/tags/${fork_tag}:refs/tags/${fork_tag}"
fi

tag_commit="$(git -C "${SUBMODULE}" rev-parse "${fork_tag}^{commit}")"
if [[ "${tag_commit}" != "${fork_commit}" ]]; then
  err "manifest forkTag ${fork_tag} resolves to ${tag_commit}, not ${fork_commit}"
fi

for package_json in \
  "${SUBMODULE}/apps/server/package.json" \
  "${SUBMODULE}/apps/web/package.json" \
  "${SUBMODULE}/apps/desktop/package.json"
do
  package_version="$(
    node -e "const fs=require('fs'); console.log(JSON.parse(fs.readFileSync(process.argv[1], 'utf8')).version)" "${package_json}"
  )"
  if [[ "${package_version}" != "${upstream_version}" ]]; then
    err "${package_json#${ROOT_DIR}/} version ${package_version} does not match upstreamVersion ${upstream_version}"
  fi
done

case "${release_version}" in
  "${upstream_version}"*) ;;
  *) err "releaseVersion ${release_version} should start with upstreamVersion ${upstream_version}" ;;
esac

info "manifest ok: ${fork_tag} (${fork_commit}) -> release ${release_version}"
