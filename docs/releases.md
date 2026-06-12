---
relationships:
  references:
    - ../vendor/t3code.yml
    - ../vendor-t3code
---

# Releases

This repository is the release control plane for the self-hosted relay distribution. The T3Code application code comes from the `vendor-t3code` fork submodule; relay infrastructure code lives in this repository.

## Fork Handoff

The fork repository owns upstream synchronization work:

- detect an upstream stable T3Code release
- create a fork milestone named for the fork release, for example `t3code-0.0.28-wyrd.1`
- sync fork `main` with upstream `main`
- rebase the fork branch, currently `bearer-auth`
- merge the fork PR
- tag the fork release

After the fork release tag exists, the fork automation should open a PR here that only:

- updates `vendor-t3code` to the fork release commit
- updates `vendor/t3code.yml`
- assigns the matching relay-repo milestone

## Vendor Manifest

`vendor/t3code.yml` is the source of truth for the fork revision consumed here.

The intended convention is:

```text
fork tag:       t3code-<upstreamVersion>-wyrd.<revision>
releaseVersion: <upstreamVersion>-wyrd.<revision>
milestone:      t3code-<upstreamVersion>-wyrd.<revision>
```

The initial manifest may reference legacy fork tags while still using a downstream `releaseVersion` that starts with the upstream package version.

## Downstream Artifacts

Fork-derived artifacts use the manifest `releaseVersion`:

- `t3code-server-<releaseVersion>` GitHub Release containing server tarballs
- `ghcr.io/boblangley/t3code-relay-web:<releaseVersion>`
- future desktop artifacts should use the same release version

Relay infrastructure artifacts keep their own component version clocks:

- `relay-caddy-<version>` for `ghcr.io/boblangley/t3code-relay-caddy`
- `feature-t3code-server-<version>` for the devcontainer feature

Avoid naked repo-wide `v*` tags for new releases. They do not identify which component is being released.
