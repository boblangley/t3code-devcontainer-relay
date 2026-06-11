# t3code-relay-web (§5.4) — builds the forked t3code web app from the
# vendor-t3code submodule and serves the static SPA on port 80.
#
# Build context MUST be the repo root (the build needs the vendor-t3code/
# submodule), with: docker build -f web/Dockerfile .
# In CI the submodule is checked out via actions/checkout submodules: recursive.
#
# D2 finding: apps/web is a Vite React SPA that builds to static files
# (apps/web/dist). We serve those with nginx. The relay URL is supplied at
# RUNTIME via /config.json (written from $T3_DOMAIN on container start) so one
# image works against any relay host — this matches the fork's client patch
# (docs/fork-patches.md §2: make the relay URL runtime-configurable).

# ---- Stage 1: build the SPA -------------------------------------------------
FROM node:24-bookworm-slim AS build
WORKDIR /src

# ca-certificates is required: the web build runs on vite-plus ("vp"), a Rust
# binary that initializes an HTTP client against the SYSTEM cert store at
# startup. The -slim image ships no CA bundle, so vp panics with
# "No CA certificates were loaded from the system" (SIGABRT / exit 134).
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Enable the pinned pnpm via corepack (root packageManager = pnpm@10.24.0).
RUN corepack enable

# Copy the whole submodule (it is a pnpm workspace; the web app depends on
# workspace packages, so the full monorepo must be present to install + build).
COPY vendor-t3code/ ./

# Install with the committed lockfile, scoped to the web app and its workspace
# dependency closure (the trailing "..." selects @t3tools/web + its deps).
# Why scoped instead of a full `pnpm install`: the monorepo's server/desktop
# apps depend on native modules listed in pnpm-workspace.yaml's
# onlyBuiltDependencies (e.g. node-pty, electron). A full install runs their
# build scripts, and node-pty's `node-gyp rebuild` fails on node:24-*-slim
# (no Python/compiler). The web app is a Vite SPA whose closure (web →
# client-runtime, contracts, shared) contains NO native-built deps, so scoping
# the install skips those builds entirely — and avoids compiling them under
# arm64 emulation in the multi-arch build.
# COORDINATION: the web package name is "@t3tools/web" (apps/web/package.json),
# built via `vp build` → apps/web/dist. Confirmed against bearer-auth.
RUN pnpm install --frozen-lockfile --filter "@t3tools/web..."
RUN VITE_HOSTED_APP_CHANNEL=latest pnpm --filter "@t3tools/web" build

# ---- Stage 2: serve ---------------------------------------------------------
FROM nginx:alpine AS serve

# Static SPA output.
COPY --from=build /src/apps/web/dist /usr/share/nginx/html

# SPA fallback: route unknown paths to index.html so client-side routing works.
RUN printf '%s\n' \
    'server {' \
    '  listen 80;' \
    '  root /usr/share/nginx/html;' \
    '  location /config.json { add_header Cache-Control "no-store"; try_files /config.json =404; }' \
    '  location / { try_files $uri $uri/ /index.html; }' \
    '}' > /etc/nginx/conf.d/default.conf

# The forked web client fetches /config.json at boot to learn the relay URL.
# nginx:alpine runs every executable in /docker-entrypoint.d before starting
# nginx, so we drop a generator there that writes config.json from $RELAY_URL
# (default derived from $T3_DOMAIN).
RUN printf '%s\n' \
    '#!/bin/sh' \
    'set -e' \
    ': "${T3_DOMAIN:=example.com}"' \
    ': "${RELAY_URL:=https://relay.t3.${T3_DOMAIN}}"' \
    'printf "{ \"relayUrl\": \"%s\" }\n" "$RELAY_URL" > /usr/share/nginx/html/config.json' \
    > /docker-entrypoint.d/99-t3-config.sh \
    && chmod +x /docker-entrypoint.d/99-t3-config.sh

EXPOSE 80
