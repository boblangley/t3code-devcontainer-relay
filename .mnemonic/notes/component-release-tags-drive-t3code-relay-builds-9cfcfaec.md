---
title: Component release tags drive t3code relay builds
tags:
  - release-process
  - github-actions
  - t3code
lifecycle: permanent
createdAt: '2026-06-12T01:57:25.253Z'
updatedAt: '2026-06-12T19:52:05.902Z'
role: decision
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
Component release tags drive t3code relay builds.

The relay repository avoids new naked `v*` tags. Component releases use explicit tag prefixes so the tag identifies the artifact being released: `relay-caddy-<version>` for the Caddy image, `feature-t3code-server-<version>` for the devcontainer feature, `t3code-server-<releaseVersion>` for server release artifacts, and image tags such as `t3code-web-<releaseVersion>` for web builds.

The devcontainer feature still keeps its package-format source of truth in `features/src/t3code-server/devcontainer-feature.json`. The release workflow treats `feature-t3code-server-<version>` as the operator command: if the tag version differs from the JSON version, the workflow commits the JSON bump to `main`, moves the tag to that commit, verifies alignment, and publishes from that aligned workspace.

The workflow publishes in the same run after alignment because pushes performed with the default GitHub Actions token do not reliably trigger follow-up workflow runs.

Devcontainers/action repo tagging is disabled in `.github/workflows/release-feature.yaml` via `disable-repo-tagging: "true"`. Without that input, `devcontainers/action@v1` creates additional repository tags such as `feature_t3code-server_0.1.6` using underscores, and it uses the original workflow context SHA rather than the feature workflow's post-bump tag target.
