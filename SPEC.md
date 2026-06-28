# t3code-devcontainer-relay — Specification

A self-hosted, devcontainer-native replacement for the T3Code managed relay. A custom Caddy build discovers running devcontainers via the Docker API, probes the T3Code server inside each, records environments in SQLite, and exposes a relay-compatible API so forked T3Code clients can connect to any devcontainer through a single endpoint — locally and over Tailscale — at `*.t3.<personal-domain>`.

This is for a single-operator, trusted/secure environment. Auth is deliberately simplified to bearer tokens. Do not harden beyond what is specified.

---

## 1. Goals

1. Zero per-repo relay configuration: adding the devcontainer feature + minimal `runArgs` makes a devcontainer appear in the relay automatically.
2. One ingress: Caddy terminates TLS for `*.t3.<personal-domain>` with a Cloudflare DNS-01 wildcard cert and routes to devcontainers and to the relay surface itself.
3. Reachable from the local machine (via dnsmasq) and from the tailnet (via an embedded `tsnet` node with split DNS), without the local machine joining the tailnet.
4. Everything reproducible from one monorepo; images and the feature published to GHCR.

## 2. Non-goals

- Multi-user auth, Clerk compatibility, or any real identity provider. The relay accepts a configured bearer token (or tokens) and nothing else.
- Supporting unmodified upstream T3Code clients. We maintain forks (desktop, web, server) with a bearer-token auth strategy.
- Public internet exposure. Ingress is loopback/LAN + tailnet only. Cloudflare is used solely for DNS-01 challenges; no proxied DNS records pointing at this host.

## 3. Architecture

```
                          ┌─────────────────────────────────────────────┐
                          │ docker host                                 │
   tailnet                │                                             │
  ┌────────┐   tcp 443    │  ┌───────────┐ network: ts-ingress          │
  │ phone/ ├──────────────┼─►│ tailscale │──┐                           │
  │ laptop │              │  └───────────┘  │ tcp forward 443           │
  └────────┘              │                 ▼                           │
                          │  ┌─────────────────────────────┐            │
   local machine          │  │ caddy (custom build)        │            │
  ┌────────┐  tcp 443     │  │  - caddy-docker-proxy       │            │
  │ dnsmasq├──────────────┼─►│  - cloudflare DNS module    │            │
  │ *.t3.* │ (published   │  │  - t3code-relay module      │            │
  └────────┘  host port)  │  │      SQLite + relay API     │            │
                          │  └──────┬──────────────────────┘            │
                          │         │ network: dev-ingress              │
                          │         │ (also: docker.sock, ro)           │
                          │   ┌─────┴──────┐   ┌────────────┐           │
                          │   │ devcont. A │   │ devcont. B │  ...      │
                          │   │ t3 server  │   │ t3 server  │           │
                          │   │ :3773      │   │ :3773      │           │
                          │   └────────────┘   └────────────┘           │
                          └─────────────────────────────────────────────┘
```

Request flow, client perspective:
1. Forked T3Code client is pointed at `relay.t3.<domain>` with a bearer token.
2. Relay API lists environments (from SQLite, kept fresh by Docker discovery + probing).
3. Client opens a session to an environment; relay routes/proxies to `<container>:3773` on `dev-ingress`.
4. Direct per-repo addresses `<myrepo>.t3.<domain>` also route straight to that container's server (route emitted by the t3code-relay module, not by labels).

## 4. Repository layout (monorepo)

```
t3code-devcontainer-relay/
├── README.md
├── SPEC.md                          # this file
├── .devcontainer/
│   └── devcontainer.json            # dogfood: this repo developed in a target-shaped devcontainer (§5.8)
├── .env.example
├── docker-compose.yml
├── web/
│   └── Dockerfile                   # t3code web app image, built from the submodule
├── docs/
│   ├── setup-guide.md               # full beginner walkthrough (§9)
│   ├── cloudflare.md
│   ├── tailscale.md
│   ├── local-dns.md
│   ├── secrets-and-tokens.md
│   ├── client-install.md
│   └── add-a-devcontainer.md
├── caddy/
│   ├── Dockerfile                   # xcaddy build
│   └── Caddyfile                    # base config; global options, relay app, snippet for label-based services
├── module/                          # Go source for the t3code-relay Caddy module
│   ├── go.mod
│   └── ...
├── features/
│   └── src/
│       └── t3code-server/
│           ├── devcontainer-feature.json
│           ├── install.sh
│           └── t3code-supervise.sh  # restart-loop wrapper
├── vendor-t3code/                   # git submodule → github.com/boblangley/t3code @ bearer-auth (pinned SHA)
├── test/                            # feature tests (feature-starter convention)
└── .github/workflows/
    ├── release-feature.yaml         # adapted from devcontainers/feature-starter
    ├── release-caddy-image.yaml
    ├── release-web-image.yaml
    ├── build-t3code-artifacts.yaml  # builds forked server (and optionally clients) from the submodule pin
    └── test.yaml
```

The fork itself lives at `github.com/boblangley/t3code` (fork of `pingdotgg/t3code`); patches are commits on the `bearer-auth` branch. The monorepo consumes it only via the submodule pinned to a SHA on that branch. Keep the patch surface minimal — ideally one auth-strategy module per app — to keep rebases against upstream cheap.

## 5. Components

### 5.1 Custom Caddy image (`ghcr.io/boblangley/t3code-relay-caddy`)

- Built with `xcaddy` from a pinned Caddy version. Plugins:
  - `github.com/lucaslorentz/caddy-docker-proxy/v2` — unchanged; continues to serve label-configured services on the same host (non-devcontainer services keep using `caddy=` labels).
  - `github.com/caddy-dns/cloudflare` — DNS-01 for the `*.t3.<domain>` wildcard. `CF_API_TOKEN` via env.
  - `./module` (local replace directive or module path) — the t3code-relay module.
- Mounts `/var/run/docker.sock` (read-only is sufficient if the credential-exec path is dropped per §5.2 auth model; see open item D1).
- Persists `/data` (certs) and `/var/lib/t3code-relay/relay.db` (SQLite) via named volumes.
- Joins `dev-ingress` to reach devcontainers. Publishes 443 (and 80 for redirects) on the host for local access. Tailnet connectivity and split-DNS service are provided by an embedded `tsnet` node inside the same container.

### 5.2 `t3code-relay` Caddy module

Caddy module namespace: `t3code-relay`. It plays two roles:

**a) Dynamic environment discovery + tailnet services.**
- Watches the Docker API (event stream + periodic reconcile, e.g. every 30s) for containers carrying the `devcontainer.id` label.
- For each discovered container: resolve its name and IP on `dev-ingress`, probe the T3Code server API on port 3773 (configurable; see open item D2 for the probe endpoint/shape), and upsert an `environments` row.
- Serves a tailnet DNS listener on TCP+UDP `:53` via `tsnet`, answering wildcard `*.t3.<domain>` queries for the domains declared on the relay container's own Caddy labels.
- Accepts tailnet TCP `:443` via `tsnet` and forwards it to the local Caddy HTTPS listener.
- Public HTTP hostnames are not generated by the module. Instead, wildcard/exact relay hosts are declared on the relay container's own `caddy_<n>` labels, and the module routes requests at runtime by parsing `<container-name>` from the incoming host.

**b) Relay API surface.**
- Replicates the subset of the upstream relay HTTP/WS API that our forked clients use (environment listing, session brokering, tunnel/proxy semantics). The exact surface is defined by what the forked clients call — derive it from the fork, not from upstream docs (open item D2).
- Served at `relay.t3.<domain>` (configurable).
- **Auth:** static bearer token(s) from env (`RELAY_TOKENS`, comma-separated). Every relay API request requires `Authorization: Bearer <token>`. No Clerk emulation. The forked server trusts requests arriving from the relay over `dev-ingress` carrying a shared secret header (`X-Relay-Secret`); the secret value lives in a **file on the host, bind-mounted into both the caddy container and every devcontainer** (never in env vars or devcontainer.json). The module reads it from `shared_secret_file`. This removes any need to `docker exec` credential minting.
- **SQLite** (`modernc.org/sqlite` or `mattn/go-sqlite3`; prefer modernc for CGO-free builds) — schema sketch:

```sql
CREATE TABLE environments (
  id            TEXT PRIMARY KEY,   -- devcontainer.id label value
  container_id  TEXT NOT NULL,
  name          TEXT NOT NULL,      -- sanitized container name / t3relay.host
  hostname      TEXT NOT NULL,      -- <name>.t3.<domain>
  ip            TEXT NOT NULL,
  port          INTEGER NOT NULL DEFAULT 3773,
  status        TEXT NOT NULL,      -- running | unreachable | stopped
  probe_json    TEXT,               -- raw server info from probe
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
);
```

Rows for stopped containers are retained with `status='stopped'` (history is cheap; clients filter on status).

**Caddyfile config block (target shape):**

```caddyfile
{
  t3code_relay {
    domain_suffix t3.{$T3_DOMAIN}
    relay_host    relay.t3.{$T3_DOMAIN}
    db_path       /var/lib/t3code-relay/relay.db
    docker_host   unix:///var/run/docker.sock
    probe_port    3773
    tokens        {$RELAY_TOKENS}
    shared_secret_file /run/secrets/t3relay-secret
  }
}
```

### 5.3 Devcontainer feature `t3code-server` (`ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server`)

- Repo structure, test harness, and publish workflow adapted from `devcontainers/feature-starter`.
- **Supported base image: `mcr.microsoft.com/devcontainers/base:noble` only** (operator convention). `install.sh` may assume Ubuntu 24.04 — glibc, apt, bash — and must not carry distro-detection or musl/Alpine code paths. Feature tests run against `base:noble` only (amd64 + arm64). Document the constraint in the feature README and fail fast with a clear error if `/etc/os-release` isn't Ubuntu noble.
- **Runtime dependency: Node.js.** The forked server is a Node app, so `devcontainer-feature.json` declares `"dependsOn": { "ghcr.io/devcontainers/features/node:1": {} }` (with `installsAfter` listing the same), so consumers get Node automatically without adding it themselves. `install.sh` fails fast if `node` is not on PATH. Pin/verify the minimum Node major required by the t3code fork (check the fork's `package.json` engines field during implementation) and pass it as the node feature's `version` if upstream requires a specific major.
- `install.sh` downloads the forked server build (GHCR release artifact produced by `build-t3code-artifacts.yaml`) for the target arch (linux-amd64/arm64, glibc), installs to `/usr/local/lib/t3code-server`, installs `t3code-supervise.sh`.
- Feature options:
  - `version` (default `latest`) — artifact tag.
  - `port` (default `3773`).
- Supervision: the feature contributes an `entrypoint` that launches `t3code-supervise.sh` — a restart loop (`while true; run server; sleep backoff`) writing logs to `/tmp/t3code-server.log`, PID-file guarded so container restarts don't double-start. (s6-overlay was considered and rejected as overkill for one process; revisit only if a second supervised process appears.)
- The server reads the shared secret from a file (default `/run/t3code/relay-secret`, overridable via feature option `secretPath`), bind-mounted from the host (e.g. `~/.config/t3relay/secret`, mode 0600), and requires the matching `X-Relay-Secret` header on inbound requests, binding 0.0.0.0:3773 (it is only reachable on `dev-ingress`).

**Consumer devcontainer.json (target — note: no caddy labels needed anymore):**

```jsonc
{
  "features": {
    "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {}
  },
  "runArgs": [
    "--network=dev-ingress",
    "-l", "devcontainer.id=${devcontainerId}",
    "-h", "<myrepo>",
    "--name", "<myrepo>"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly"
  ]
}
```

### 5.4 T3Code fork (submodule `vendor-t3code`)

Branch `bearer-auth`, patches limited to:
- **server:** replace pairing/session auth with the `X-Relay-Secret` shared-secret check; keep everything else stock.
- **desktop & web apps:** replace the Clerk flow with a settings field for relay URL + bearer token; auth strategy sends `Authorization: Bearer <token>` on relay calls.
- **desktop:** disable/remove the auto-updater (it would otherwise pull stock upstream builds that expect Clerk).

CI in the monorepo builds the server artifact (linux amd64+arm64), desktop artifacts, and web image on submodule pin bumps.

**Desktop distribution (CI in this repo):** `build-t3code-desktop.yaml` builds from the pinned `vendor-t3code` submodule, with a matrix building:
- macOS: `.dmg`/`.zip`, arm64 + x64, unsigned — set `CSC_IDENTITY_AUTO_DISCOVERY=false` and disable notarization.
- Linux: AppImage x64 (a PKGBUILD for Arch/CachyOS is optional later if desktop integration is wanted).
- Windows: NSIS installer arm64 + x64.

Artifacts attach to GitHub Releases on this repo using `t3code-desktop-<releaseVersion>`, plus the floating `t3code-desktop-latest` alias. The docs link to this repo's releases page and document first-run steps for unsigned builds: macOS `xattr -dr com.apple.quarantine` (or right-click → Open); Windows SmartScreen "More info → Run anyway". Each user/device gets its own token from `RELAY_TOKENS` so tokens can be revoked independently.

**Web app image (`ghcr.io/boblangley/t3code-relay-web`):** built from the fork submodule by a monorepo workflow and run as a compose service (§5.5). Discovery sub-item of D2: determine whether the web build is a static SPA (serve via a minimal static-file image; relay URL entered in the client UI or provided as runtime config) or requires a Node server process — this determines the Dockerfile.

### 5.5 Compose stack

`docker-compose.yml` at repo root. Requirements:

- Services: `caddy`, `web`.
- `web` service: the `t3code-relay-web` image (§5.4); joins `dev-ingress`; routed by caddy-docker-proxy via labels `caddy=web.t3.{$T3_DOMAIN}` / `caddy.reverse_proxy={{upstreams <port>}}` — no module involvement.
- Networks: `dev-ingress` (external — created once by the operator, shared with devcontainers).
- `caddy` service: the GHCR image from §5.1; `ports: 443:443, 80:80` on the host; volumes for docker.sock (ro), caddy data, relay sqlite, plus a read-only bind mount of `${RELAY_SECRET_FILE}` → `/run/secrets/t3relay-secret`; env from `.env`; and `caddy_<n>` labels defining the exact relay host(s) plus wildcard `*.t3.<domain>` host(s).
- Tailnet connectivity is provided by an embedded `tsnet` node inside the `caddy` container, using `TS_AUTHKEY` and `TAILSCALE_HOSTNAME`.
- Volumes: `caddy-data`, `relay-db`.

### 5.6 `.env.example`

```dotenv
# Cloudflare API token with Zone.DNS edit on the zone for <personal-domain>
CF_API_TOKEN=

# Base domain; relay surface lives under t3.<T3_DOMAIN>
T3_DOMAIN=example.com

# Comma-separated bearer tokens accepted by the relay API
RELAY_TOKENS=

# Host path to the shared-secret file (bind-mounted into caddy and all devcontainers)
RELAY_SECRET_FILE=~/.config/t3relay/secret

# Tailscale auth key for the embedded relay node
TS_AUTHKEY=

# Tailnet hostname presented by the embedded relay node
TAILSCALE_HOSTNAME=t3code-relay
```

### 5.7 DNS (documentation, not code)

Covered in detail by `docs/local-dns.md`, `docs/tailscale.md`, and `docs/cloudflare.md` (see §9). In brief:
- dnsmasq on the local machine: `address=/t3.<domain>/127.0.0.1` (or the docker host's LAN IP).
- Tailscale admin: split DNS for each served `t3.<domain>` zone → the relay node's tailnet IP.
- Cloudflare: no public records needed for `*.t3.<domain>`; only the API token for DNS-01.

### 5.8 Dogfooding devcontainer

The repo itself is developed inside a devcontainer that follows the exact consumer convention from §5.3, so the stack is exercised by its own development:

```jsonc
{
  "name": "t3code-devcontainer-relay",
  "image": "mcr.microsoft.com/devcontainers/base:noble",
  "features": {
    "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {},
    "ghcr.io/devcontainers/features/go:1": {},
    "ghcr.io/devcontainers/features/docker-outside-of-docker:1": {}
  },
  "runArgs": [
    "--network=dev-ingress",
    "-l", "devcontainer.id=${devcontainerId}",
    "-h", "t3code-devcontainer-relay",
    "--name", "t3code-devcontainer-relay"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly"
  ]
}
```

Notes:
- `go` feature for module development; `docker-outside-of-docker` so the devcontainer CLI / compose / image builds can run against the host daemon from inside (the node feature arrives transitively via the t3code-server feature's `dependsOn`).
- Bootstrap caveat for the README: the published `t3code-server` feature reference is circular before the first feature release. Initial development happens with the feature block commented out (or pointing at a local `./features/src/t3code-server` path, which the devcontainer CLI supports); switch to the GHCR reference after v0.1.0 is published.
- The `dev-ingress` network and the secret file must exist on the host before first open; README's setup section covers both.

## 6. CI / publishing (GHCR)

- `release-feature.yaml`: feature-starter's publish workflow, scoped to `features/src`, publishes `t3code-server` as an OCI artifact.
- `release-caddy-image.yaml`: buildx multi-arch (amd64, arm64) of `caddy/Dockerfile`, tags `latest` + semver, pushes to GHCR.
- `build-t3code-artifacts.yaml`: checks out the submodule pin, builds the forked server, attaches binaries to a GitHub release consumed by the feature's `install.sh`.
- `test.yaml`: Go tests for the module; `devcontainer features test` for the feature.

## 7. Open discovery items (do these first)

- **D1 — Docker socket scope.** Confirm read-only socket suffices (discovery + inspect only, no exec needed under the shared-secret model). If yes, mount `:ro` and document.
- **D2 — Relay API + probe surface.** From the fork's client code, enumerate the exact endpoints/WS messages the desktop and web clients use against a relay, and the server's info/health endpoint shape for probing. This defines the module's API contract. Output: a short `module/API.md`.
- **D3 — Route-merge spike.** Prove caddy-docker-proxy generated config and module-generated routes coexist in one Caddy instance (label routes for non-devcontainer services must keep working). Pick the mechanism (CDP-sibling generator vs. admin-API pushes) based on the spike.
- **D4 — Hostname collisions.** Define behavior when two containers sanitize to the same name (suffix with short container ID; log a warning).

## 8. Milestones

1. Repo scaffold from feature-starter; compose + .env.example; caddy image building with CDP + cloudflare only (no module). Label-based proxying and wildcard cert working end to end locally.
2. D2 + D3 spikes; module skeleton registering with Caddy, Docker discovery → SQLite, route emission for devcontainers.
3. Fork patches (server shared-secret; clients bearer token); artifact build workflow; feature installing + supervising the server.
4. Relay API surface in the module; end-to-end: forked desktop client → relay → devcontainer session.
5. Embedded `tsnet` + split DNS docs; polish, README, docs/ per §9, tag v0.1.0.

## 9. Documentation requirements (`docs/`)

The README stays short: what the project is, a quick-start for someone who already knows the tooling, and a link to `docs/setup-guide.md`. The detailed manual-configuration documentation lives in `docs/` and is written for a **beginner audience** — a smart adult who is new to development, DNS, Cloudflare, Tailscale, and Docker. Concretely, that means every doc must:

- Assume no prior vocabulary: briefly define each concept the first time it appears (what DNS resolution is, what a reverse proxy does, what a tailnet is) in one or two sentences, then move on — explain enough to follow the steps confidently, not a textbook.
- Give exact click-paths for web dashboards ("Cloudflare dashboard → your domain → *My Profile* → *API Tokens* → *Create Token*") and exact copy-pasteable shell commands, with a sentence saying what each command does and what successful output looks like.
- Include a verification step after every stage ("run `dig test.t3.example.com` — you should see `127.0.0.1`") so a reader knows immediately whether to proceed or troubleshoot.
- Include a short troubleshooting list per page for the predictable failure modes (wrong API token permissions, `dev-ingress` network missing, cert not issuing, split DNS not applying, SmartScreen/Gatekeeper warnings).
- Never instruct the reader to paste secrets into files that get committed; each doc that touches a credential repeats where it lives and why.

Page-by-page scope:

- **`setup-guide.md`** — the spine. Numbered end-to-end walkthrough from "you have a computer and a domain" to "your T3Code client lists your devcontainer." Orders the stages (prerequisites → Cloudflare → secrets/tokens → compose up → local DNS → Tailscale → client install → first devcontainer) and links into the per-topic pages for detail. States up front what the reader needs: a domain on Cloudflare, Docker installed, a Tailscale account, ~1 hour.
- **`cloudflare.md`** — what Cloudflare is doing for us (DNS-01 challenge for a wildcard certificate; explicitly: *no* public DNS records point at your machine). Creating a scoped API token (Zone → DNS → Edit on the one zone), putting it in `.env`, verifying the cert issues (what to look for in `docker compose logs caddy`).
- **`tailscale.md`** — what a tailnet is, creating an account, generating the auth key for the embedded relay node (and the reusable/ephemeral tradeoff), confirming the node appears in the admin console, configuring split DNS for `t3.<domain>` → the relay node's tailnet IP, installing Tailscale on a phone/laptop and verifying reachability.
- **`local-dns.md`** — why the local machine needs dnsmasq (wildcard `*.t3.<domain>` → localhost), install + config per OS (macOS via Homebrew with the resolver-file approach; Linux including systemd-resolved coexistence, which is the usual trap), verification with `dig`/`ping`.
- **`secrets-and-tokens.md`** — the three credentials and their distinct jobs (CF token: lets Caddy prove domain ownership; relay bearer tokens: let a *client/person* in, one per person; shared secret file: lets the *relay* talk to servers). Generating strong values (`openssl rand -hex 32`), creating `~/.config/t3relay/secret` with `chmod 600`, filling `.env` from `.env.example`, how to revoke a person's token.
- **`client-install.md`** — downloading the right desktop build per OS/arch from this repo's `t3code-desktop-*` releases, the unsigned-build first-run steps (Gatekeeper, SmartScreen), entering the relay URL and bearer token, plus the web app at `web.t3.<domain>` as the zero-install alternative.
- **`add-a-devcontainer.md`** — the per-repo recipe: the §5.3 devcontainer.json snippet annotated line-by-line (what each runArg does and why), creating `dev-ingress` once (`docker network create dev-ingress`), naming rules/collisions, and verifying the repo appears in the client.

Acceptance test for this documentation: a newcomer can complete `setup-guide.md` start-to-finish on a fresh machine without asking for help or consulting external docs.
