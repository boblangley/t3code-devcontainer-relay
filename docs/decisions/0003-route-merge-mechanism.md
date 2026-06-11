# 0003 — Route-merge mechanism: single wildcard request-time handler (D3)

- Status: accepted
- Date: 2026-06-11

## Context and Problem Statement

Open discovery item D3: the t3code-relay module must emit routes for
devcontainers while coexisting with caddy-docker-proxy (CDP), which generates
the Caddy config from `caddy=` labels for non-devcontainer services. Only one
HTTP server can bind `:443`, so the module's routes and CDP's routes must live
in the same server. The SPEC offered two candidate mechanisms:

1. **CDP-sibling generator** — produce config the way CDP does and let both merge.
2. **Admin-API pushes** — an independent app PATCHing routes into a dedicated
   server block via Caddy's admin API.

## Considered Options

- **CDP-sibling generator.** CDP exposes no public "additional dynamic route
  provider" plugin point; this would mean patching/forking CDP. High coupling,
  fragile across CDP upgrades.
- **Admin-API pushes.** CDP periodically reloads the *entire* config on Docker
  events via `caddy load`, which would clobber any routes we PATCH in. Requires
  fighting CDP for config ownership.
- **Single wildcard request-time handler (chosen).** Put one static route
  `*.t3.<domain>` in the base Caddyfile, handled by a module HTTP handler that
  resolves `Host` → container at request time (from SQLite) and reverse-proxies.
  All dynamism lives inside the handler; no route generation, no config
  reloads, no contention with CDP.

## Decision

Implement the proxy as a **single wildcard route** (`*.t3.<domain>`) bound to a
module HTTP handler (`t3code_relay_proxy`) plus a relay-API handler
(`t3code_relay_api`) on `relay.t3.<domain>`. Both live in the base Caddyfile that
CDP merges via `CADDY_DOCKER_CADDYFILE_PATH`. A background Caddy **app**
(registered via the `t3code_relay` global option) runs Docker discovery and owns
the shared SQLite store the handlers read.

Caddy's most-specific-host-wins routing guarantees exact hostnames
(`relay.t3.<domain>`, `web.t3.<domain>` from a CDP label, any other labelled
service) win over the wildcard, so the wildcard only ever catches devcontainer
hosts. CDP's generated config is never touched by the module.

## Consequences

- Zero contention with CDP; label-based services keep working unchanged.
- No admin-API writes, no per-container route churn, no reload storms.
- Discovery still maintains SQLite (for the relay API + history); the proxy just
  reads it per request, with a tiny in-memory cache.
- Two-way door: if per-route features (per-host TLS policy, per-route headers)
  are ever needed, we can move to admin-API pushes without changing the feature
  or the fork.
- Validation still required against a live CDP instance (the D3 "spike"); the
  design is structured so that proof is a deployment check, not a code rewrite.
