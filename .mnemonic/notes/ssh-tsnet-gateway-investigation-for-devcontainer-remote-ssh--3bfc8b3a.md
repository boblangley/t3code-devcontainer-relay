---
title: SSH tsnet gateway investigation for devcontainer Remote-SSH routing
tags:
  - ssh
  - tailscale
  - devcontainer
  - architecture
  - relay
lifecycle: permanent
createdAt: '2026-06-13T07:19:16.462Z'
updatedAt: '2026-06-13T07:19:16.462Z'
role: research
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
SSH tsnet gateway investigation on 2026-06-13 found the design is plausible as a complementary Remote-SSH path, but the proposed shape needs corrections.

The gateway should support OpenSSH `direct-tcpip` channels used by `ProxyJump`/`ssh -W`, not rely only on parsing a session command like `nc target:22`. A command fallback can be useful, but VS Code Remote-SSH delegates to OpenSSH config and normal jump-host behavior is TCP channel forwarding.

Tailnet membership and ACLs restrict who can reach a `tsnet` listener, but a plain `tsnet.Server.Listen` service still needs an application-level authorization story if per-user decisions matter. The gateway can use `LocalClient().WhoIs(remoteAddr)` for caller identity, or use Tailscale SSH/`ListenSSH` if that model fits the channel requirements.

The gateway must whitelist discovered devcontainers and port 22 only. Forwarding arbitrary requested host:port would turn the relay into a tailnet-accessible pivot across the Docker network.

Agent forwarding should not be enabled for the jump host by default. It may be useful for the final devcontainer session, but OpenSSH agent forwarding exposes signing capability to the remote side and is not needed for the jump TCP stream itself.

A practical spike should start as a separate `ssh-gateway` service on `dev-ingress` with its own persisted tsnet state and host key, then decide whether to merge into the existing Caddy/tsnet module after behavior is proven.
