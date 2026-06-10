# T3 Code Devcontainer Relay Stack

This document sketches a personal remote-access stack for T3 Code devcontainers.

The goal is:

- run `t3 serve` automatically inside each devcontainer
- expose each devcontainer through a stable HTTPS hostname
- make those hostnames reachable both locally and over a tailnet
- optionally add a small personal relay so configured environments can be listed from one place

## Architecture

```text
Local browser / T3 desktop
        |
        | https://<name>.t3.<personal-domain>
        v
  localhost:443
        |
        v
  Caddy Docker Proxy  <---- Docker labels on devcontainers
        |
        v
  devcontainer:t3 serve


Tailnet client
        |
        | https://<name>.t3.<personal-domain>
        v
  Tailscale sidecar
        |
        v
  Caddy Docker Proxy
        |
        v
  devcontainer:t3 serve
```

Caddy is the single ingress/router. Devcontainers only need to join the Caddy Docker network and expose their T3 server on an internal port.

The Tailscale sidecar is a private network adapter into Caddy. It does not need to know about each devcontainer.

## Docker Stack

Create a shared Docker network:

```sh
docker network create dev-ingress
```

A Compose-style baseline:

```yaml
networks:
  dev-ingress:
    external: true

services:
  caddy:
    build: ./caddy
    restart: unless-stopped
    environment:
      CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN}
    ports:
      - "127.0.0.1:80:80"
      - "127.0.0.1:443:443"
    networks:
      - dev-ingress
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy-data:/data
      - caddy-config:/config

  tailscale-caddy:
    image: tailscale/tailscale:latest
    hostname: t3-dev-ingress
    restart: unless-stopped
    environment:
      TS_AUTHKEY: ${TS_AUTHKEY}
      TS_STATE_DIR: /var/lib/tailscale
      TS_USERSPACE: "true"
      TS_SERVE_CONFIG: /config/serve.json
    volumes:
      - tailscale-state:/var/lib/tailscale
      - ./tailscale/serve.json:/config/serve.json:ro
    networks:
      - dev-ingress

volumes:
  caddy-data:
  caddy-config:
  tailscale-state:

networks:
  dev-ingress:
```

Caddy binds to the host loopback interface for local access and also joins `dev-ingress` so it can reach devcontainers.

The Tailscale sidecar also joins `dev-ingress` and proxies tailnet traffic to Caddy.

## Custom Caddy Build

This stack needs a custom Caddy binary because the desired Caddy image combines:

- Caddy Docker Proxy for Docker-label based routing
- Cloudflare DNS provider for DNS-01 wildcard certificates
- a personal T3 relay/registry module

Example `./caddy/Dockerfile`:

```Dockerfile
FROM caddy:builder AS builder

RUN xcaddy build \
  --with github.com/lucaslorentz/caddy-docker-proxy/v2 \
  --with github.com/caddy-dns/cloudflare
  --with github.com/boblangley/caddy-t3code-relay

FROM caddy:alpine

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
```

Use a scoped Cloudflare API token rather than a global API key. The token should be limited to the `<personal-domain>` zone and include the permissions needed for DNS-01 challenge management, typically zone read plus DNS edit.

With Cloudflare DNS-01, Caddy can obtain a wildcard certificate for:

```text
*.t3.<personal-domain>
```

That lets local and tailnet clients use the same HTTPS names without exposing devcontainers publicly.

## Tailscale Serve

The sidecar should expose Caddy over the tailnet. The exact `TS_SERVE_CONFIG` JSON should be verified against the Tailscale image version in use, but conceptually it should proxy tailnet HTTPS to Caddy:

```json
{
  "TCP": {
    "443": {
      "HTTPS": true
    }
  },
  "Web": {
    "${TS_CERT_DOMAIN}:443": {
      "Handlers": {
        "/": {
          "Proxy": "http://caddy:80"
        }
      }
    }
  }
}
```

If this is too fiddly, run Tailscale on the host first and come back to the sidecar after the Caddy/devcontainer routing is stable.

## DNS

Use a private subdomain for T3 environments:

```text
*.t3.<personal-domain>
```

Recommended split DNS:

- Local machine: `*.t3.<personal-domain>` resolves to `127.0.0.1`.
- Tailnet clients: `*.t3.<personal-domain>` resolves to the Tailscale sidecar node.
- Public DNS: either no record, or a harmless placeholder.

Caddy routes by `Host` header, so wildcard DNS is enough. Per-container DNS records are not required for the first version.

For HTTPS, use a wildcard certificate for `*.t3.<personal-domain>`, preferably through DNS-01. That keeps local and tailnet URLs identical and avoids browser mixed-content problems.

## Devcontainer Config

Each devcontainer should:

- join `dev-ingress`
- run `t3 serve` on `0.0.0.0:3773`
- carry Caddy labels for its hostname
- use a persistent `T3CODE_HOME`

Example `.devcontainer/devcontainer.json` additions:

```json
{
  "runArgs": [
    "--network=dev-ingress",
    "--label=caddy=myrepo.t3.<personal-domain>",
    "--label=caddy.reverse_proxy={{upstreams 3773}}",
    "--label=t3.relay.enabled=true",
    "--label=t3.relay.name=myrepo",
    "--label=t3.relay.url=https://myrepo.t3.<personal-domain>",
    "--label=t3.relay.base-dir=/home/vscode/.t3"
  ],
  "forwardPorts": [3773],
  "postStartCommand": "bash -lc 'export T3CODE_HOME=$HOME/.t3; pgrep -f \"t3 serve.*--port 3773\" >/dev/null || nohup npx t3 serve --host 0.0.0.0 --port 3773 --base-dir \"$T3CODE_HOME\" > /tmp/t3-serve.log 2>&1 &'"
}
```

Use a stable hostname per repo/devcontainer. For example:

```text
https://t3code.t3.<personal-domain>
https://agent-playground.t3.<personal-domain>
```

To mint a pairing link manually:

```sh
T3CODE_HOME="$HOME/.t3" npx t3 auth pairing create \
  --base-dir "$T3CODE_HOME" \
  --base-url "https://myrepo.t3.<personal-domain>" \
  --ttl 1h
```

The pairing command and `t3 serve` must use the same `--base-dir`.

## T3Code Personal Relay

T3Code relay is a full T3 Connect control plane. It includes account auth, managed endpoints, Cloudflare tunnel provisioning, mobile notification plumbing, DPoP, environment link proofs, persistence, and observability.

For personal use, a much smaller relay can be useful:

- one place to list configured devcontainer environments
- one place to mint fresh connect credentials
- Docker auto-discovery from labels

### Relay As A Caddy Module

Since this stack already needs a custom Caddy build, the personal relay can be implemented as another Caddy module.

That is a good fit because Caddy already sits at the intersection of:

- Docker socket access
- Docker labels
- route/hostname configuration
- TLS certificates
- local and tailnet ingress

The custom Caddy binary can become the single personal ingress and registry process:

```text
custom-caddy
  modules:
    - caddy-docker-proxy
    - caddy-dns/cloudflare
    - caddy-t3-relay
```

The relay module should be framed as a personal T3 environment registry, not a clone of the hosted T3 Connect relay.

Responsibilities:

- watch Docker events or periodically scan Docker containers
- look for `devcontainer.id` labels
- probe each environment descriptor
- expose a minimal relay HTTP API
- mint short-lived T3 pairing credentials using the docker exec API

```text
Docker events / labels
        |
        v
personal-t3-relay
        |
        | probes /.well-known/t3/environment
        v
SQLite registry
```

The relay can watch Docker events, find devcontainers, probe:

```text
https://myrepo.t3.<personal-domain>/.well-known/t3/environment
```

and store:

```json
{
  "environmentId": "env_...",
  "label": "myrepo",
  "endpoint": {
    "httpBaseUrl": "https://myrepo.t3.<personal-domain>/",
    "wsBaseUrl": "wss://myrepo.t3.<personal-domain>/",
    "providerKind": "manual"
  },
  "linkedAt": "2026-06-10T00:00:00.000Z"
}
```

### T3Code-compatible Relay Contract


```http
GET /health
GET /v1/environments
POST /v1/environments/:environmentId/connect
POST /oauth/token
POST /v1/environments/:environmentId/status
POST /v1/client/environment-link-challenges
POST /v1/client/environment-links
DELETE /v1/client/environment-links/:environmentId
```

`GET /v1/environments`:

```json
{
  "environments": [
    {
      "environmentId": "env_...",
      "label": "myrepo",
      "endpoint": {
        "httpBaseUrl": "https://myrepo.t3.<personal-domain>/",
        "wsBaseUrl": "wss://myrepo.t3.<personal-domain>/",
        "providerKind": "manual"
      },
      "linkedAt": "2026-06-10T00:00:00.000Z"
    }
  ]
}
```

`POST /v1/environments/:environmentId/connect`:

```json
{
  "environmentId": "env_...",
  "endpoint": {
    "httpBaseUrl": "https://myrepo.t3.<personal-domain>/",
    "wsBaseUrl": "wss://myrepo.t3.<personal-domain>/",
    "providerKind": "manual"
  },
  "credential": "short-lived-environment-bootstrap-token",
  "expiresAt": "2026-06-10T01:00:00.000Z"
}
```

The current client-side managed relay code expects Clerk and DPoP behavior. As a result the relay must emulate enough of Clerk/DPoP to satisfy the existing `RelayApi` client.

### Credential Minting With Docker Exec

For a Caddy-module relay, the most direct credential-minting path is Docker exec.

Flow:

1. Client calls `POST /v1/environments/:environmentId/connect`.
2. Relay finds the registered Docker container for the environment.
3. Relay runs the configured mint command through the Docker Engine exec API.
4. Relay parses the JSON output from `t3 auth pairing create`.
5. Relay returns the short-lived credential and the already-known endpoint.

The equivalent shell command is:

```sh
docker exec myrepo-devcontainer \
  npx t3 auth pairing create \
    --base-dir /home/vscode/.t3 \
    --ttl 5m \
    --json
```

Important constraints:

- The mint command must use the same `--base-dir` as the running `t3 serve`.
- The Docker socket grants broad host control; only run this on a trusted personal machine.
- Restrict Docker exec to containers explicitly labeled for relay use.
- Prefer a fixed allowlisted command over arbitrary label-provided shell.
- Keep generated credentials short-lived.
- Log credential issuance metadata, but never log the credential itself.