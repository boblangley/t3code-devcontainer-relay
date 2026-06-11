# 0005 — Serve the web app as a static SPA with runtime config

- Status: accepted
- Date: 2026-06-11

## Context and Problem Statement

Discovery sub-item of D2: determine whether `apps/web` is a static SPA or needs
a Node server, which decides the `web/Dockerfile` shape and how the relay URL
reaches the client. Upstream bakes the relay URL at build time via
`VITE_T3CODE_RELAY_URL`, which would force a per-domain rebuild.

## Decision

`apps/web` is a Vite React SPA that builds to static files
(`apps/web/dist`). Serve it with `nginx:alpine` (no Node process at runtime).
Supply the relay URL at **runtime** via a `/config.json` written from
`$T3_DOMAIN`/`$RELAY_URL` by an `/docker-entrypoint.d` generator on container
start, and patch the fork's web client to fetch `/config.json` at boot
(docs/fork-patches.md §2). One image then works against any relay host.

## Consequences

- Tiny, fast image; no Node attack surface in the served container.
- No rebuild to retarget a relay; the operator just sets `T3_DOMAIN`.
- The build stage still needs the full pnpm workspace (the web app imports
  workspace packages), so the Docker build context is the repo root with the
  `vendor-t3code` submodule present.
- Two-way door: if the web app ever needs SSR/a Node runtime, swap stage 2 for a
  Node server image; the compose label (`{{upstreams 80}}`) stays the same.
