# t3code-devcontainer-relay module

A Caddy v2 module that discovers devcontainers via Docker labels, stores them in SQLite, and provides:

- **Proxy handler** (`http.handlers.t3code_relay_proxy`): authenticates Bearer tokens, resolves `Host` to a container IP via the store, and reverse-proxies the request.
- **API handler** (`http.handlers.t3code_relay_api`): provides `/health`, `/v1/environments`, per-environment `/status` and `/connect`, `DELETE /v1/environments/:id` for forgetting stale rows, and the relay mount browser at `/mounts`.

## Caddyfile global option

```caddyfile
{
  t3code_relay {
    domain_suffix       t3.example.com
    relay_host          relay.t3.example.com
    db_path             /var/lib/t3relay/relay.db
    docker_host         unix:///var/run/docker.sock
    probe_port          3773
    tokens              tok1,tok2
    shared_secret_file  /run/secrets/relay_secret
    mounts_root         /mnt/t3relay
  }
}
```

## Mounted file browser

The relay serves a browser UI from `https://relay.t3.<domain>/mounts`. The UI is public HTML, but file tree and file content endpoints require a configured relay bearer token.

- `GET /v1/mounts/tree` returns the file tree under `mounts_root`.
- `GET /v1/mounts/file/<path>?mode=render|source` returns rendered content or source.
- `.html` files render as sandboxed HTML, `.markdown` files render to HTML, and common image types render as images.
- Non-binary files can be viewed as source with line numbers and client-side syntax highlighting.

## Running tests

```
go test ./...
```
