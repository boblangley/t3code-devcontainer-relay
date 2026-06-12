# T3 Code Relay API Contract

This document specifies the HTTP/WS API contract that the `t3code-relay` Caddy module must implement.
It is derived from the upstream T3 Code source (`pingdotgg/t3code`) by tracing actual code paths.

Where the shape is uncertain or not fully traceable, the entry is marked **ASSUMPTION**.

The rest of this document maps the **full upstream** surface for reference. The
section immediately below is the **authoritative, reduced contract** the module
actually implements and that the fork's client/server patches must conform to.

---

## Self-Hosted Implementation Scope (authoritative)

In the self-hosted relay we drop Clerk, DPoP, OAuth token-exchange, signed-JWT
mint proofs, and managed Cloudflare tunnels. Two secrets replace all of it:

- **Client → Relay:** `Authorization: Bearer <token>` where `<token>` ∈ `RELAY_TOKENS`.
- **Relay → Server:** `X-Relay-Secret: <secret>` (from `shared_secret_file`),
  injected by the relay on every proxied request. The forked server treats a
  valid `X-Relay-Secret` as an authenticated admin session
  (`environmentAuthenticatedAuthLayer`, `apps/server/src/auth/http.ts`).

The module exposes two surfaces on one Caddy instance (one listener, :443):

### A. Relay control-plane API — host `relay.t3.<domain>`

| Method | Path | Auth | Behaviour |
|---|---|---|---|
| GET | `/health` | none | `{ "ok": true, "service": "relay" }` |
| GET | `/v1/environments` | Bearer | List environments from SQLite. Each record matches contracts `RelayClientEnvironmentRecord` exactly: `{ environmentId, label, endpoint: { httpBaseUrl, wsBaseUrl, providerKind: "t3_relay" }, linkedAt }` — `environmentId` is the T3 server descriptor id from `/.well-known/t3/environment` when available, while the relay may keep a separate internal devcontainer row key. `label` and `linkedAt` are non-empty (the client decodes with Effect Schema). `httpBaseUrl`/`wsBaseUrl` point at `https://<name>.t3.<domain>` (surface B). Per-environment status/platform are read from `/status`, not this record. |
| POST | `/v1/environments/:id/status` | Bearer | Probe the container (`GET /.well-known/t3/environment` with `X-Relay-Secret`); return `{ environmentId, endpoint, status: online\|offline, checkedAt, descriptor }`. `environmentId` must match the listed environment id and, when present, `descriptor.environmentId`. |
| POST | `/v1/environments/:id/connect` | Bearer | Return `{ environmentId, endpoint, credential, expiresAt }`. `environmentId` must match the listed environment id. Since surface B already injects `X-Relay-Secret`, `credential` is a non-secret marker (the server trusts the relay-injected header); the client uses `endpoint` to open its session over surface B. |
| DELETE | `/v1/environments/:id` | Bearer | Forget an environment row from SQLite. This is intended for stopped environments that will never return. If the matching devcontainer is still running and discoverable, the next discovery pass will recreate the row. Returns `204` when deleted and `404` for an unknown environment. |
| OPTIONS | `*` | none | CORS preflight (headers per the CORS section below). |

`POST /v1/client/dpop-token`, the OAuth metadata endpoints, link-challenge /
link / unlink, mobile registration, and agent-activity are **out of scope** —
the fork's client must not call them. If the client still requests OAuth
metadata, the relay MAY return a minimal stub (see ASSUMPTION notes below).

### B. Per-environment proxy — host `*.t3.<domain>` (one wildcard route)

A single request-time handler (no per-container route generation; see
`docs/decisions/0003-route-merge-mechanism.md`):

1. Validate `Authorization: Bearer <token>` against `RELAY_TOKENS` (401 if absent/invalid).
2. Resolve `Host` (`<name>.t3.<domain>`) → environment row in SQLite → container IP.
3. Reverse-proxy (HTTP **and** WebSocket upgrade) to `<ip>:<port>` (default 3773),
   **removing** the client `Authorization` header and **setting** `X-Relay-Secret`.
4. 404 if the host is unknown, 502/504 if the container is unreachable.

This handler carries the stock server session flow transparently: the client's
calls to `<httpBaseUrl>/oauth/token`, `/api/auth/websocket-ticket`, and the
WebSocket `/api/rpc?wsTicket=...` all traverse surface B and succeed because the
injected `X-Relay-Secret` short-circuits server auth.

`relay.t3.<domain>` is matched more specifically than `*.t3.<domain>`, and
`web.t3.<domain>` (caddy-docker-proxy label route) likewise — so the wildcard
only ever catches devcontainer hosts. Non-devcontainer label services are
unaffected.

---

## Architecture Summary

```
Client (web/desktop)
    |
    | Authorization: Bearer <relay-secret>  (our replacement for Clerk)
    v
t3code-relay (Caddy module)   <-- implements this API
    |
    | Authorization: Bearer <environment-credential>  or  shared-secret probe
    v
t3code server (apps/server)  running inside devcontainer, port 3773
```

The relay acts as a control plane. Actual application traffic (WebSocket RPC) flows **directly** between the client and the environment server after the client has obtained an environment credential from the relay.

---

## Relay Authentication

### Client → Relay

All relay endpoints (except `/health` and OAuth metadata) require:

```
Authorization: Bearer <relay-bearer-token>
```

In the stock T3 Code cloud, this is a Clerk session or DPoP-bound access token. In our self-hosted relay, we replace this with a simple shared bearer token configured on the relay.

For DPoP-protected operations (connect, status, mobile), the stock client also sends:
```
DPoP: <dpop-proof-jwt>
```

**ASSUMPTION**: For the initial self-hosted implementation we will accept a plain `Authorization: Bearer <secret>` without DPoP requirements. The client fork must be patched to remove DPoP for these endpoints, or the relay must bypass DPoP verification.

### Relay → Environment Server

The relay calls the environment server using the `environmentCredential` it issued when the environment was linked. This is a bearer token:
```
Authorization: Bearer <environment-credential>
```

For probe calls (health/mint), the relay sends a JWT `proof` field in the JSON body signed with the relay's `cloudMintPrivateKey` (Ed25519). In our self-hosted relay, we replace this with a simpler `X-Relay-Secret` shared header:

```
X-Relay-Secret: <configured-secret>
```

The server must be patched at `apps/server/src/auth/http.ts:162` (`environmentAuthenticatedAuthLayer`) to accept this header as equivalent to a valid admin session.

---

## Probe Endpoint (Environment Server)

The relay uses this to discover and health-check environment servers.

### `GET /.well-known/t3/environment`

**Target**: Environment server (apps/server) directly  
**Auth**: None required  
**Port**: 3773 (default, `DEFAULT_PORT` in `apps/server/src/config.ts:17`)

**Response: 200 OK**
```json
{
  "environmentId": "<opaque-string>",
  "label": "<human-readable-label>",
  "platform": {
    "os": "linux",
    "arch": "x64"
  },
  "serverVersion": "0.0.27",
  "capabilities": {
    "repositoryIdentity": true
  }
}
```

Schema source: `packages/contracts/src/environment.ts:28-35` (`ExecutionEnvironmentDescriptor`)

Use this endpoint to:
1. Confirm a server is alive at the expected address
2. Read `environmentId` (stable identity key for the environment)
3. Read `label` (display name)
4. Read `serverVersion` (for compatibility checks)

---

## Relay API Endpoints

Base URL: `https://<relay-host>`

### Health

#### `GET /health`

Auth: None  
Response: 200
```json
{ "ok": true, "service": "relay" }
```

---

### OAuth Metadata

#### `GET /.well-known/oauth-authorization-server`

Auth: None  
Response: 200
```json
{
  "issuer": "https://<relay-host>",
  "token_endpoint": "https://<relay-host>/v1/client/dpop-token",
  "grant_types_supported": ["urn:ietf:params:oauth:grant-type:token-exchange"],
  "token_endpoint_auth_methods_supported": ["none"],
  "dpop_signing_alg_values_supported": ["ES256"],
  "scopes_supported": ["environment:connect", "environment:status", "mobile:registration"]
}
```

#### `GET /.well-known/oauth-protected-resource`

Auth: None  
Response: 200
```json
{
  "resource": "https://<relay-host>",
  "authorization_servers": ["https://<relay-host>"],
  "scopes_supported": ["environment:connect", "environment:status", "mobile:registration"],
  "dpop_bound_access_tokens_required": true,
  "dpop_signing_alg_values_supported": ["ES256"]
}
```

**ASSUMPTION**: For self-hosted relay without DPoP, `dpop_bound_access_tokens_required` should be `false`.

---

### Token Exchange (Client Auth Bootstrap)

#### `POST /v1/client/dpop-token`

In stock T3 Code: exchanges a Clerk bearer token for a DPoP-bound access token.  
In self-hosted relay: **ASSUMPTION** — this endpoint may be bypassed entirely if the client is patched to use a plain bearer token. If implemented, the relay should issue its own access token.

Headers:
```
DPoP: <dpop-proof-jwt>
Content-Type: application/x-www-form-urlencoded
```

Body (form-urlencoded):
```
grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange
&subject_token=<clerk-or-relay-bearer-token>
&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Ajwt
&requested_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aaccess_token
&resource=https%3A%2F%2F<relay-host>
&scope=environment%3Aconnect+environment%3Astatus
&client_id=t3-web
```

Response: 200
```json
{
  "access_token": "<dpop-bound-token>",
  "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
  "token_type": "DPoP",
  "expires_in": 1800,
  "scope": "environment:connect environment:status"
}
```

Schema source: `packages/contracts/src/relay.ts:633-659` (`RelayDpopAccessTokenRequest`, `RelayDpopAccessTokenResponse`)

---

### Environment Listing

#### `GET /v1/environments`

Auth: `Authorization: Bearer <clerk-token>` (or relay bearer in self-hosted)

Response: 200
```json
{
  "environments": [
    {
      "environmentId": "<opaque-string>",
      "label": "<human-readable>",
      "endpoint": {
        "httpBaseUrl": "https://<tunnel-or-direct-url>",
        "wsBaseUrl": "wss://<tunnel-or-direct-url>",
        "providerKind": "cloudflare_tunnel"
      },
      "linkedAt": "2026-01-01T00:00:00.000Z"
    }
  ]
}
```

Schema source: `packages/contracts/src/relay.ts:582-593` (`RelayClientEnvironmentRecord`, `RelayListEnvironmentsResponse`)

**Self-hosted relay implementation**: Enumerate discovered devcontainers. For each running t3code server, include it in the list. The `endpoint.httpBaseUrl` and `wsBaseUrl` should be the relay's proxy URL for that container (e.g. `https://<relay>/<container-id>/`). The `providerKind` can be `t3_relay` (a valid value in the upstream schema) or `manual`.

---

### Environment Linking

Used by the server CLI to register with the relay. In self-hosted mode, this flow is replaced by automatic discovery. If implemented:

#### `POST /v1/client/environment-link-challenges`

Issues a challenge for the environment to sign.

Auth: Bearer  
Body:
```json
{
  "notificationsEnabled": true,
  "liveActivitiesEnabled": false,
  "managedTunnelsEnabled": true
}
```
Response: 200
```json
{
  "challenge": "<random-string>",
  "expiresAt": "2026-01-01T00:05:00.000Z"
}
```

#### `POST /v1/client/environment-links`

Auth: Bearer  
Body:
```json
{
  "proof": "<environment-signed-JWT>",
  "notificationsEnabled": true,
  "liveActivitiesEnabled": false,
  "managedTunnelsEnabled": true
}
```
Response: 200
```json
{
  "ok": true,
  "cloudUserId": "<user-id>",
  "environmentId": "<env-id>",
  "endpoint": {
    "httpBaseUrl": "https://...",
    "wsBaseUrl": "wss://...",
    "providerKind": "cloudflare_tunnel"
  },
  "endpointRuntime": {
    "providerKind": "cloudflare_tunnel",
    "connectorToken": "...",
    "tunnelId": "...",
    "tunnelName": "..."
  },
  "relayIssuer": "https://<relay-host>",
  "environmentCredential": "<bearer-token-for-env-to-use>",
  "cloudMintPublicKey": "<ed25519-pem-public-key>"
}
```

Schema source: `packages/contracts/src/relay.ts:241-265` (`RelayEnvironmentLinkResponse`)

**Self-hosted relay**: The `environmentCredential` is a token the relay issues that the environment server stores and uses to authenticate its outbound requests back to the relay. The `cloudMintPublicKey` is the relay's Ed25519 public key used to sign health/mint proofs.

---

### Environment Status Check

#### `POST /v1/environments/:environmentId/status`

The relay checks whether the environment server is online by calling `POST /api/t3-connect/health` on the environment server.

Auth: `Authorization: DPoP <access-token>` + `DPoP: <proof>`  
Path param: `environmentId` (string)  
Body: empty

Response: 200
```json
{
  "environmentId": "<env-id>",
  "endpoint": {
    "httpBaseUrl": "https://...",
    "wsBaseUrl": "wss://...",
    "providerKind": "cloudflare_tunnel"
  },
  "status": "online",
  "checkedAt": "2026-01-01T00:00:00.000Z",
  "descriptor": {
    "environmentId": "...",
    "label": "...",
    "platform": { "os": "linux", "arch": "x64" },
    "serverVersion": "0.0.27",
    "capabilities": { "repositoryIdentity": true }
  }
}
```

Response when offline: same shape but `"status": "offline"` and optional `"error"` field.

Schema source: `packages/contracts/src/relay.ts:707-715` (`RelayEnvironmentStatusResponse`)

**Self-hosted relay implementation**: Instead of the JWT-proof handshake, probe `GET /.well-known/t3/environment` on the container (using `X-Relay-Secret` header) to populate `descriptor` and confirm `status: "online"`.

---

### Environment Connect (Get Session Credential)

This is the core broker operation. The client requests a short-lived credential to authenticate directly with the environment server.

#### `POST /v1/environments/:environmentId/connect`

Auth: `Authorization: DPoP <access-token>` + `DPoP: <proof>`  
Path param: `environmentId`  
Body:
```json
{
  "clientProofKeyThumbprint": "<JWK-thumbprint>",
  "deviceId": "<optional-device-id>"
}
```

Relay internally calls `POST /api/t3-connect/mint-credential` on the environment server with a JWT proof. The environment server mints a 2-minute pairing credential and returns it.

Response: 200
```json
{
  "environmentId": "<env-id>",
  "endpoint": {
    "httpBaseUrl": "https://...",
    "wsBaseUrl": "wss://...",
    "providerKind": "cloudflare_tunnel"
  },
  "credential": "<12-char-pairing-token>",
  "expiresAt": "2026-01-01T00:02:00.000Z"
}
```

Schema source: `packages/contracts/src/relay.ts:696-702` (`RelayEnvironmentConnectResponse`)

**Self-hosted relay implementation**: Instead of the signed JWT mint flow, the relay can directly call `POST /oauth/token` on the environment server (using `X-Relay-Secret` for auth) to issue a session token, or call the server's `/api/auth/pairing-token` endpoint to generate a short-lived pairing credential. Return the credential to the client.

The client then exchanges the credential directly with the environment server:
```
POST <httpBaseUrl>/oauth/token
subject_token=<credential>
subject_token_type=urn:ietf:params:oauth:token-type:jwt
requested_token_type=urn:ietf:params:oauth:token-type:access_token
...
```

---

### Environment Unlink

#### `DELETE /v1/client/environment-links/:environmentId`

Auth: Bearer  
Response: 200 `{ "ok": true }`

---

### Environment Server Callback Endpoints

These are called by the relay **on the environment server**, not the other way around.

#### `POST /api/t3-connect/health`

Auth: JWT proof in body (relay-signed)  
Body: `{ "proof": "<relay-signed-JWT>" }`  
Response: `RelayEnvironmentHealthResponse`

**Self-hosted relay**: Replace with `X-Relay-Secret` header check in the server middleware. Skip JWT proof verification.

#### `POST /api/t3-connect/mint-credential`

Auth: JWT proof in body (relay-signed)  
Body: `{ "proof": "<relay-signed-JWT>" }`  
Response:
```json
{
  "credential": "<12-char-pairing-token>",
  "expiresAt": "2026-01-01T00:02:00.000Z",
  "proof": "<env-signed-JWT>"
}
```

**Self-hosted relay**: Replace with `X-Relay-Secret` header; skip JWT proof verification and response proof.

---

### Agent Activity Publishing (Server → Relay)

The environment server calls this to push agent activity state (for mobile push notifications).

#### `POST /v1/environments/:environmentId/threads/:threadId/agent-activity`

Auth: `Authorization: Bearer <environment-credential>`  
Body:
```json
{
  "state": {
    "environmentId": "...",
    "threadId": "...",
    "projectTitle": "...",
    "threadTitle": "...",
    "phase": "running",
    "headline": "...",
    "modelTitle": "...",
    "updatedAt": "...",
    "deepLink": "..."
  },
  "proof": "<environment-signed-JWT>"
}
```
State may be `null` to clear a previously published state.

Response: 200
```json
{
  "ok": true,
  "deliveries": []
}
```

**Self-hosted relay**: Can be stubbed to return `{ "ok": true, "deliveries": [] }` if mobile push is not needed.

---

### Mobile Registration (Optional)

These endpoints are for iOS mobile push notifications and are not required for the core relay functionality.

- `POST /v1/mobile/devices` — Register device
- `POST /v1/mobile/live-activities` — Register live activity
- `DELETE /v1/mobile/devices/:deviceId` — Unregister device

---

## Client-Side Connection Flow (Full Sequence)

```
1. Client → Relay: GET /v1/environments
   ← list of environments with endpoint URLs

2. Client → Relay: POST /v1/environments/:environmentId/status  (optional)
   ← online/offline status + descriptor

3. Client → Relay: POST /v1/environments/:environmentId/connect
   body: { clientProofKeyThumbprint: "..." }
   ← { credential: "ABCDEF123456", endpoint: { httpBaseUrl, wsBaseUrl }, expiresAt }

4. Client → Environment Server: POST <httpBaseUrl>/oauth/token
   body: subject_token=ABCDEF123456&...
   ← { access_token: "...", token_type: "Bearer", expires_in: 2592000, scope: "..." }

5. Client → Environment Server: POST <httpBaseUrl>/api/auth/websocket-ticket
   Authorization: Bearer <access_token>
   ← { ticket: "...", expiresAt: "..." }

6. Client → Environment Server: WS <wsBaseUrl>/api/rpc?wsTicket=<ticket>
   (all further application traffic is over this WebSocket)
```

---

## CORS

The relay must respond with:
```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, DELETE, OPTIONS
Access-Control-Allow-Headers: authorization, b3, traceparent, content-type, dpop
Access-Control-Expose-Headers: traceparent, www-authenticate
Access-Control-Max-Age: 86400
```

---

## Error Shapes

All errors follow a tagged error schema:

```json
{
  "_tag": "RelayAuthInvalidError",
  "code": "auth_invalid",
  "reason": "invalid_bearer",
  "traceId": "<trace-id>"
}
```

HTTP status codes:
- 401: `RelayAuthInvalidError`, `RelayEnvironmentLinkProofExpiredError`, `RelayAgentActivityPublishProofExpiredError`
- 400: `RelayEnvironmentLinkProofInvalidError`
- 403: `RelayEnvironmentConnectNotAuthorizedError`
- 500: `RelayInternalError`, `RelayEnvironmentLinkFailedError`
- 502: `RelayEnvironmentEndpointUnavailableError`
- 503: `RelayEnvironmentLinkUnavailableError`
- 504: `RelayEnvironmentEndpointTimedOutError`

Schema source: `packages/contracts/src/relay.ts:324-487`
