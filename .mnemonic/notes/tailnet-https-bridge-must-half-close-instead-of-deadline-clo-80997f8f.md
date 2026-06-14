---
title: >-
  Tailnet HTTPS bridge must half-close instead of deadline-closing both
  directions
tags:
  - t3code
  - tailscale
  - relay
  - websocket
lifecycle: permanent
createdAt: '2026-06-13T12:57:47.326Z'
updatedAt: '2026-06-13T12:57:47.326Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
Tailnet HTTPS bridge WebSocket disconnects on 2026-06-13 were traced to the raw TCP bridge from `tsnet` TCP 443 to local Caddy `127.0.0.1:443`.

The old bridge ran `io.Copy` in both directions and, as soon as either direction returned, set an immediate deadline on both sockets. That can terminate the response direction even though the request direction has finished normally. The observed symptom was that user query frames reached the devcontainer server over the tailnet path, but response frames did not make it back and the browser reported socket disconnects.

The relay module fix is to use TCP half-close semantics: after each copy direction completes, call `CloseWrite` on that direction's destination when supported, and only fall back to an immediate deadline for connection types without write-half-close support. A regression test named `TestProxyBidirectional_AllowsResponseAfterClientHalfClose` pins the case where the client request side half-closes before the backend sends its response.
