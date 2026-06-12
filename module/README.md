# t3code-devcontainer-relay module

A Caddy v2 module that discovers devcontainers via Docker labels, stores them in SQLite, and provides:

- **Proxy handler** (`http.handlers.t3code_relay_proxy`): authenticates Bearer tokens, resolves `Host` to a container IP via the store, and reverse-proxies the request.
- **API handler** (`http.handlers.t3code_relay_api`): provides `/health`, `/v1/environments`, per-environment `/status` and `/connect`, and `DELETE /v1/environments/:id` for forgetting stale rows.

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
  }
}
```

## Running tests

```
go test ./...
```
