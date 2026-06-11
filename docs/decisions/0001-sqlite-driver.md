# 0001 — CGO-free SQLite driver (modernc.org/sqlite)

- Status: accepted
- Date: 2026-06-11

## Context and Problem Statement

The t3code-relay module persists discovered environments in SQLite (§5.2). The
Caddy image is built with `xcaddy` and shipped multi-arch (amd64 + arm64). A
CGO-dependent driver (`mattn/go-sqlite3`) complicates xcaddy/buildx
cross-compilation and requires a C toolchain in the build image.

## Decision

Use `modernc.org/sqlite` — a pure-Go (CGO-free) SQLite. `xcaddy build` stays a
plain `go build` with `CGO_ENABLED=0`, and multi-arch builds need no
cross-compiler. The spec already states this preference (§5.2).

## Consequences

- Simple, reproducible `xcaddy` builds; no C toolchain in the Dockerfile.
- Slightly slower than the C driver, which is irrelevant for this workload
  (a handful of upserts every 30s, single operator).
- Two-way door: the driver is isolated behind the module's small store layer, so
  swapping to `mattn/go-sqlite3` later is a localized change.
