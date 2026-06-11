# caddy/Dockerfile
#
# Build context: repo root (build with: docker build -f caddy/Dockerfile .)
#
# Produces a custom Caddy binary with three plugins:
#   1. caddy-docker-proxy  — label-based dynamic reverse proxy (non-devcontainer services)
#   2. caddy-dns/cloudflare — DNS-01 ACME provider for wildcard cert (*.t3.<domain>)
#   3. t3code-relay module  — Docker discovery, SQLite, relay API + request-time
#                              wildcard proxy (no route emission; see decision 0003)
#
# The final image runs `caddy docker-proxy` so caddy-docker-proxy drives label discovery,
# and merges our base Caddyfile (CADDY_DOCKER_CADDYFILE_PATH) at startup for the relay app
# and global ACME configuration.

ARG CADDY_VERSION=2.8.4

# ---------------------------------------------------------------------------
# Stage 1 — builder
#   Uses the official xcaddy-based builder image to compile our custom binary.
#   The local t3code-relay module is copied in from the repo root so xcaddy can
#   resolve it via a local replace directive without a network round-trip.
# ---------------------------------------------------------------------------
FROM caddy:${CADDY_VERSION}-builder AS builder

# Copy the local Go module source into the build context.
# The Docker build context is the repo root, so module/ maps to ./module here.
COPY module/ /build/module/

# The module's go.mod requires a newer Go than the caddy:2.8.4-builder ships.
# GOTOOLCHAIN=auto (Go's default) makes the build fetch the required toolchain
# automatically during `xcaddy build`. Set explicitly for clarity/robustness.
ENV GOTOOLCHAIN=auto

# Build Caddy with all required plugins.
#   --with github.com/lucaslorentz/caddy-docker-proxy/v2
#       Label-driven reverse proxy; same host can keep serving non-devcontainer services.
#   --with github.com/caddy-dns/cloudflare
#       Cloudflare DNS-01 ACME solver for the *.t3.<domain> wildcard certificate.
#   --with github.com/boblangley/t3code-devcontainer-relay/module=/build/module
#       Our local relay module (local replace directive; the path equals the COPY dest above).
RUN xcaddy build \
    --with github.com/lucaslorentz/caddy-docker-proxy/v2 \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/boblangley/t3code-devcontainer-relay/module=/build/module

# ---------------------------------------------------------------------------
# Stage 2 — final image
#   Starts from the stock Caddy image (same version) and swaps in the custom
#   binary.  Stock entrypoint, health-check, and non-root user are preserved.
# ---------------------------------------------------------------------------
FROM caddy:${CADDY_VERSION}

# Replace the stock binary with our plugin-enriched build.
COPY --from=builder /usr/bin/caddy /usr/bin/caddy

# Install the base Caddyfile.
# caddy-docker-proxy merges this at startup when CADDY_DOCKER_CADDYFILE_PATH is set.
# It carries the global options (ACME DNS-01, t3code_relay app config) and the
# relay.t3.* / *.t3.* site blocks that bind the module's request-time handlers.
COPY caddy/Caddyfile /etc/caddy/Caddyfile

# Standard HTTP / HTTPS ports.
EXPOSE 80 443

# Run caddy-docker-proxy so label-based route discovery is active.
# CADDY_DOCKER_CADDYFILE_PATH tells caddy-docker-proxy to merge the base Caddyfile
# (global options, t3code_relay config) into the final generated config.
ENV CADDY_DOCKER_CADDYFILE_PATH=/etc/caddy/Caddyfile

CMD ["caddy", "docker-proxy"]
