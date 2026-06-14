# t3code-devcontainer-relay

A self-hosted, devcontainer-native replacement for the [T3 Code](https://github.com/pingdotgg/t3code)
managed relay. A custom [Caddy](https://caddyserver.com) build discovers running
devcontainers via the Docker API, probes the T3 Code server inside each, records
them in SQLite, and exposes a relay-compatible API so forked T3 Code clients can
reach any devcontainer through a single endpoint — locally and over Tailscale —
at `*.t3.<your-domain>`. The relay embeds its own Tailscale node and serves
tailnet DNS directly; local-machine DNS still uses dnsmasq.

This is for a **single operator** in a trusted environment. Auth is deliberately
simple: bearer tokens for people, a shared-secret file between the relay and the
servers. See [`SPEC.md`](SPEC.md) for the full design.

> **New to Docker, DNS, Cloudflare, or Tailscale?** Skip this quick-start and
> follow the step-by-step **[setup guide](docs/setup-guide.md)** instead — it
> assumes no prior knowledge and takes about an hour.

## How it works

```
 phone/laptop ──(tailnet 53/443)──┐
                               ▼
 local machine ──(LAN 443)──► caddy (custom build) ──► devcontainer A : t3 server :3773
   dnsmasq *.t3.<domain>        ├─ caddy-docker-proxy   └─► devcontainer B : t3 server :3773
                                ├─ cloudflare DNS-01 (wildcard cert)
                                └─ t3code-relay module
                                   ├─ SQLite + relay API
                                   ├─ embedded tsnet node
                                   └─ tailnet DNS for wildcard t3 zones
```

- One ingress: Caddy terminates TLS for `*.t3.<domain>` with a Cloudflare
  DNS-01 wildcard certificate (no public DNS records point at your machine).
- Zero per-repo relay config: adding the `t3code-server` feature + a few
  `runArgs` makes a devcontainer show up in the relay automatically.
- Reachable from the local machine (via dnsmasq) and from your tailnet (via the
  relay's embedded Tailscale node and split DNS).
- Supported wildcard domains are declared on the `caddy` service's own
  `caddy_<n>` labels. Add more wildcard/relay-host label blocks there to serve
  more base domains.

Surfaces once running:

| Host | What it is |
|---|---|
| `relay.t3.<domain>` | Relay API — point your T3 Code client here with a bearer token |
| `web.t3.<domain>` | The web client (zero-install alternative) |
| `<repo>.t3.<domain>` | Direct route to one devcontainer's server |
| `<repo>--<name>.t3.<domain>` | On-demand route to an arbitrary port exposed from that devcontainer |

## Quick-start (you already know the tooling)

```bash
# 1. One-time host setup
docker network create dev-ingress
mkdir -p ~/.config/t3relay && openssl rand -hex 32 > ~/.config/t3relay/secret && chmod 600 ~/.config/t3relay/secret

# 2. Configure
cp .env.example .env
#   edit .env: CF_API_TOKEN, T3_DOMAIN, RELAY_TOKENS (one per person), TS_AUTHKEY

# 3. Bring up the stack
docker compose up -d
docker compose logs -f caddy        # watch the wildcard cert issue

# 4. Point a client at https://relay.t3.<domain> with one of your RELAY_TOKENS
```

Then add a devcontainer to any repo — see [`docs/add-a-devcontainer.md`](docs/add-a-devcontainer.md).

## Repository layout

| Path | Purpose |
|---|---|
| `caddy/` | Custom xcaddy image: caddy-docker-proxy + cloudflare DNS + the relay module |
| `module/` | The `t3code-relay` Caddy module (Go): Docker discovery, SQLite, relay API. Contract in [`module/API.md`](module/API.md) |
| `features/src/t3code-server/` | Devcontainer feature that installs + supervises the forked server |
| `web/` | Static-SPA image for the web client |
| `docker-compose.yml` | The `caddy` + `web` stack; `caddy` embeds the Tailscale node and defines served wildcard domains via labels |
| `vendor-t3code/` | Submodule → the T3 Code fork (`bearer-auth`) |
| `docs/` | Beginner setup manual; `docs/decisions/` holds the MADRs; `docs/fork-patches.md` specs the fork changes |
| `.github/workflows/` | GHCR publishing for the feature, images, and server artifacts |

## Documentation

Start with the **[setup guide](docs/setup-guide.md)**. Per-topic deep dives:
[Cloudflare](docs/cloudflare.md) ·
[secrets & tokens](docs/secrets-and-tokens.md) ·
[local DNS](docs/local-dns.md) ·
[Tailscale](docs/tailscale.md) ·
[client install](docs/client-install.md) ·
[add a devcontainer](docs/add-a-devcontainer.md).

## Status

Early. The `bearer-auth` fork patches (server shared-secret check; clients
bearer token) are tracked in [`docs/fork-patches.md`](docs/fork-patches.md) and
land in the separate [`boblangley/t3code`](https://github.com/boblangley/t3code)
repo; re-pin the `vendor-t3code` submodule once they merge.
