---
title: >-
  Caddy relay tailnet startup needs patched image and concrete tsnet UDP listen
  IP
tags:
  - t3code
  - tailscale
  - caddy
  - relay
lifecycle: permanent
createdAt: '2026-06-13T06:15:17.240Z'
updatedAt: '2026-06-13T06:15:17.240Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
Caddy relay tailnet investigation on 2026-06-13 found three interacting causes.

The live `caddy-relay` container was using `ghcr.io/boblangley/t3code-relay-caddy:latest` from image version `0.1.9` / revision `091ab7b`, while the repo had `relay-caddy-0.2.1` / `e079492` with `tailscale_auth_key` wired into `caddy/Caddyfile`. The stale image loaded a `t3code_relay` block without `tailscale_hostname`, `tailscale_auth_key`, or `tailscale_state_dir`, so no embedded tsnet node started even though `TS_AUTHKEY` and `TAILSCALE_HOSTNAME` were set in the container environment.

After moving to `0.2.1`, Caddy loaded tailnet config and tsnet authenticated as `t3code-relay` with tailnet IP `100.64.39.97`, but config load failed with `tsnet.ListenPacket("udp", ":53"): address must be a valid IP`. Tailscale `tsnet.Server.ListenPacket` requires a concrete Tailscale IP address for UDP listeners; the relay code must bind DNS UDP to `netip.AddrPortFrom(ip4, 53)` or IPv6 fallback rather than `:53`.

The live host stack also had a stale `caddy_1.email` label. Caddy-docker-proxy treated that as an invalid `email` directive inside the wildcard site block. Older images masked this because their base Caddyfile declared the wildcard site directly, but newer label-driven images require the stale label to be removed.

A local patched image `0.2.1-local-tailnet-fix` was built and the `caddy-relay` container was manually recreated without the stale `caddy_1.email` label. Logs showed `tailscale node ready`, `t3code_relay started`, and Caddy `load complete`; same-network health check returned `{ "ok": true, "service": "relay" }`. The host compose file lives at `/home/tinomen/Docker/docker-compose.yml` on the Docker host and was not mounted into the devcontainer, so the host compose source still needs to be aligned separately.
