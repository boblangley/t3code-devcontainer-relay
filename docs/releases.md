---
relationships:
  references:
    - ../vendor/t3code.yml
    - ../vendor-t3code
---

# Releases

This repository is the release control plane for the self-hosted relay distribution. The T3Code application code comes from the `vendor-t3code` fork submodule; relay infrastructure code lives in this repository.

## Fork workflows

The fork has two different workflows. Keep them separate.

### Fork maintenance

Use this when upstream `t3dotgg/t3-code` shipped a new stable release and the
fork needs to be synced or rebased.

The fork repository owns this upstream-synchronization work:

- detect an upstream stable T3Code release
- create a fork milestone named for the fork release, for example `t3code-0.0.28-boblangley.1`
- sync the fork default branch, currently `bearer-auth`, with upstream `main`
- rebase the fork patch stack when needed
- merge the fork PR into the default branch
- tag the fork release

### Fork revision

Use this when upstream did not change, but the fork itself needs another
release revision on top of the currently active fork tag.

For example, if the currently consumed tag is
`t3code-0.0.28-boblangley.1`, the next fork-only revision should be
`t3code-0.0.28-boblangley.2`.

The fork repository owns this fork-revision work:

- read the current active fork tag from `vendor/t3code.yml`
- increment only the fork revision suffix
- create a dedicated submodule branch or worktree in `vendor-t3code` from the commit referenced by the current active fork tag
- implement and validate the fork changes in that submodule branch or worktree
- push the branch to the fork repository
- create the matching fork milestone, for example `t3code-0.0.28-boblangley.2`
- open a fork PR targeting `bearer-auth`
- merge the fork PR into the default branch
- tag the new fork release
- clean up the local submodule branch or worktree once the fork change is landed

When using this repo's workspace to author fork changes inside the
`vendor-t3code` submodule, resetting the submodule checkout to the current
active fork tag is a local convenience step only. The authoritative release
workflow still happens in the fork repository.

## Fork handoff to this repo

After the fork release tag exists, the fork automation should open a PR here that only:

- updates `vendor-t3code` to the fork release commit
- updates `vendor/t3code.yml`
- assigns the matching relay-repo milestone

## Vendor Manifest

`vendor/t3code.yml` is the source of truth for the fork revision consumed here.

The intended convention is:

```text
fork tag:       t3code-<upstreamVersion>-boblangley.<revision>
releaseVersion: <upstreamVersion>-boblangley.<revision>
milestone:      t3code-<upstreamVersion>-boblangley.<revision>
```

Legacy fork tags may exist, but new releases should use the intended convention.

## Downstream Artifacts

Fork-derived artifacts use the manifest `releaseVersion`:

- `t3code-server-<releaseVersion>` GitHub Release containing server tarballs
- `t3code-web-<releaseVersion>` tag publishing `ghcr.io/boblangley/t3code-relay-web:<releaseVersion>`
- `t3code-desktop-<releaseVersion>` GitHub Release containing unsigned desktop installers

Floating GitHub Release aliases are scoped by artifact type:

- `t3code-server-latest`
- `t3code-desktop-latest`

Relay infrastructure artifacts keep their own component version clocks:

- `relay-caddy-<version>` for `ghcr.io/boblangley/t3code-relay-caddy`
- `feature-t3code-server-<version>` for the devcontainer feature

Avoid naked repo-wide `v*` tags for new releases. They do not identify which component is being released.

## Feature Version Alignment

The devcontainer feature keeps its version in `features/src/t3code-server/devcontainer-feature.json`, as required by the devcontainer feature packaging format. Releases are still tag-driven:

1. Push `feature-t3code-server-<version>`.
2. If the JSON version already matches `<version>`, the workflow publishes the feature.
3. If the JSON version does not match, the workflow commits the JSON version bump to `main`, moves the tag to that commit, pushes both, verifies the workspace is aligned, and publishes.

This makes the tag the operator-facing release command while keeping the devcontainer manifest as the package-format source of truth. The workflow publishes in the same run after alignment because pushes made with the default GitHub Actions token do not reliably trigger follow-up workflow runs.
