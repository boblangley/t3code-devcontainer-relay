# 0002 — Mount the Docker socket read-only (D1)

- Status: accepted
- Date: 2026-06-11

## Context and Problem Statement

Open discovery item D1: confirm whether the relay needs write access to the
Docker socket. An earlier design considered minting per-session credentials via
`docker exec` into each devcontainer, which would require a writable socket.

## Decision

Mount `/var/run/docker.sock` **read-only** (`:ro`). Under the shared-secret auth
model (§5.2) the relay never execs into containers: the relay→server trust is a
file-based `X-Relay-Secret` header bind-mounted into both sides, so no runtime
credential minting is needed. The module only needs Docker **read** operations:
list/inspect containers and stream events.

## Consequences

- Smaller blast radius: a compromised Caddy cannot start/stop/exec containers.
- The discovery loop uses only `ContainerList`, `ContainerInspect`, and
  `Events` — all available over a read-only socket.
- Two-way door: if a future feature genuinely needs exec, revisit by dropping
  `:ro`; documented here so the constraint is explicit.
