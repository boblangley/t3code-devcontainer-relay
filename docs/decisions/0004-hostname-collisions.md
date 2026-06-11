# 0004 — Hostname collision policy (D4)

- Status: accepted
- Date: 2026-06-11

## Context and Problem Statement

Open discovery item D4: two containers may sanitize to the same hostname (e.g.
two checkouts both named `myrepo`, or names differing only by characters removed
during sanitization). The relay maps `<name>.t3.<domain>` → one container; a
collision would make routing ambiguous.

## Decision

The host is derived from the container `--name` (or the `t3relay.host` label
override), lowercased and sanitized to `[a-z0-9-]`. On a collision (the
sanitized host already maps to a *different* `container_id`):

1. Suffix the loser with a short container-ID fragment: `<name>-<id[:6]>`.
2. Emit a `WARN` log naming both containers and the assigned hostnames.
3. The first-seen container keeps the bare `<name>`; ordering is by discovery
   time so a restart of the same container is stable (same `container_id` keeps
   its host).

The override label `t3relay.host=<name>` lets an operator resolve collisions
deterministically.

## Consequences

- No silent mis-routing; collisions are visible in logs and still reachable via
  the suffixed host.
- Stable hostnames across container restarts (keyed on `container_id`).
- Two-way door: the suffix length / strategy is a localized change in the
  discovery layer.
