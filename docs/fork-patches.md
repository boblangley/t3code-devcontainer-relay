# Fork patches — `boblangley/t3code` @ `bearer-auth`

This document specifies the patches to apply to the **forked** T3 Code repo on
the `bearer-auth` branch. The monorepo consumes that branch via the
`vendor-t3code` submodule (currently pinned to the branch's tip; re-pin to the
patched SHA after these land).

> Scope note: this repo (`t3code-devcontainer-relay`) and the fork are **separate
> repositories**. These patches must be authored and pushed in the fork. Keep the
> patch surface minimal — ideally one auth-strategy module per app — so rebases
> against upstream stay cheap (SPEC §5.4). File:line references are against the
> upstream commit the branch currently points at; verify they still apply.

The authoritative wire contract both sides implement is
[`module/API.md` → "Self-Hosted Implementation Scope"](../module/API.md).

## 1. Server — replace pairing/session auth with `X-Relay-Secret`

**Where:** `apps/server/src/auth/http.ts` — `environmentAuthenticatedAuthLayer`
(≈ line 162), the single middleware that gates all authenticated endpoints.
(Alternative narrower point: `authenticateRequest()` in
`apps/server/src/auth/EnvironmentAuth.ts` ≈ line 298, before the cookie/bearer
check.)

**Change:** before the normal session/cookie/bearer checks, read
`request.headers["x-relay-secret"]`. Read the expected value from the file at
`T3CODE_RELAY_SECRET_FILE` (env var; the feature sets this to the bind-mounted
secret path, default `/run/t3code/relay-secret`). If the header is present and
matches (constant-time compare), short-circuit to a synthetic authenticated
admin `AuthenticatedSession` and skip pairing/session/Clerk entirely. If absent,
fall through to stock behaviour (so local dev still works).

**Bind address / port:** ensure the server binds `0.0.0.0:${PORT}` (default
3773) — it is only reachable on `dev-ingress`. `PORT` is already supported via
`config.ts` (`DEFAULT_PORT = 3773`).

**Probe endpoint:** keep `GET /.well-known/t3/environment` unauthenticated and
returning `{ environmentId, label, platform, serverVersion, capabilities }` — the
relay uses it for discovery/health. (Stock already exposes this.)

**Env var contract (must match the feature's supervise script):**
- `T3CODE_RELAY_SECRET_FILE` — path to the shared-secret file.
- `PORT` — listen port (3773).

## 2. Desktop & web clients — Clerk → relay URL + bearer token

**Web (`apps/web`):**
- `apps/web/src/cloud/publicConfig.ts` (≈ line 21): the relay URL comes from
  `VITE_T3CODE_RELAY_URL` at build time. Make it runtime-configurable (read from
  a settings field / `window.__T3_RELAY__` / a `/config.json` fetched at boot) so
  one static build works against any relay host. The web image
  (`web/Dockerfile`) serves the static SPA; the relay URL is `relay.t3.<domain>`.
- Replace the Clerk `getToken()` path (`managedAuth.tsx`) with a static bearer
  token entered in settings; send `Authorization: Bearer <token>` on relay calls.
- Drop DPoP: do **not** call `POST /v1/client/dpop-token`; send the plain bearer.

**Desktop (`apps/desktop`):**
- Relay URL is stored per-environment at `relayManaged.relayUrl` in
  `savedEnvironments.json` (`apps/desktop/src/settings/DesktopSavedEnvironments.ts`).
  Add a settings field for relay URL + bearer token; replace the Clerk flow
  (`apps/desktop/src/ipc/methods/cloudAuth.ts`) with the static-token strategy.
- **Disable/remove the auto-updater** — it would pull stock upstream builds that
  expect Clerk.

**Client call surface** must be limited to the authoritative subset in
`module/API.md`: `GET /v1/environments`, `POST /v1/environments/:id/status`,
`POST /v1/environments/:id/connect`, then the direct session over
`<endpoint.httpBaseUrl>` (which is `https://<name>.t3.<domain>`, proxied by the
relay). No link-challenge / OAuth-metadata / mobile calls.

## 3. Desktop distribution (CI in this repo)

`.github/workflows/build-t3code-desktop.yaml` builds desktop artifacts from the
`vendor-t3code` submodule pinned by this repo:

- macOS `.dmg`/`.zip` arm64 + x64, **unsigned** (`CSC_IDENTITY_AUTO_DISCOVERY=false`,
  no notarization)
- Linux AppImage x64
- Windows NSIS arm64 + x64

Artifacts attach to GitHub Releases in this repo using
`t3code-desktop-<releaseVersion>`, plus the floating alias
`t3code-desktop-latest`. This repo's `docs/client-install.md` covers the
unsigned first-run steps.

## 4. After patching

1. Push the patches to `bearer-auth`.
2. In this repo: `git -C vendor-t3code fetch && git -C vendor-t3code checkout <patched-sha>`,
   then commit the submodule bump.
3. Run `build-t3code-artifacts.yaml` to publish the server tarballs the feature
   installs.
