---
title: On-demand devcontainer port exposure uses single-label wildcard hostnames
tags:
  - relay
  - devcontainer
  - caddy
  - architecture
lifecycle: permanent
createdAt: '2026-06-14T14:56:08.701Z'
updatedAt: '2026-06-14T14:56:08.701Z'
role: decision
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
On-demand devcontainer port exposure uses relay-owned SQLite registration and single-label wildcard hostnames.

The chosen hostname form is `<environment>--<exposure>.t3.<domain>`, for example `myrepo--vite.t3.example.com`. This keeps every exposure at the same DNS depth as `<environment>.t3.<domain>`, so the existing `*.t3.<domain>` wildcard certificate remains valid.

Caddy config is not mutated for each exposure. The existing wildcard route remains the only Caddy route, and the relay handler resolves request hostnames at request time from SQLite. This follows the repo's accepted route-merge decision and avoids contention with caddy-docker-proxy reloads.

The in-container helper registers exposures through the relay API using the mounted relay shared-secret file, so agents can expose arbitrary local ports without receiving a personal relay bearer token.
