# t3code-server devcontainer feature

Installs and supervises the forked T3Code server (bearer-auth branch) inside a
devcontainer. The server listens on `0.0.0.0:<port>` and validates inbound relay
requests via a shared-secret file that is bind-mounted from the host. Tailscale
is enabled by default so clients can connect directly to the devcontainer over
the tailnet when a Tailscale auth key file is mounted.

## Supported base image

**Ubuntu 24.04 "noble" ONLY** (`mcr.microsoft.com/devcontainers/base:noble`).

`install.sh` assumes glibc, apt, and bash and will fail fast with a clear error
on any other distribution.  No Alpine / musl / distro-detection code paths are
provided — this is an intentional operator constraint, not an oversight.

## Dependencies

The forked T3Code server is a Node.js application.  This feature declares:

```json
"dependsOn": { "ghcr.io/devcontainers/features/node:1": {} }
```

Consumers do **not** need to add the Node feature explicitly — it arrives
transitively.  `install.sh` will fail fast if `node` is not on `PATH` after the
dependency has been resolved, which should never happen in normal usage.

## Options

| Option       | Type   | Default                      | Description |
|---|---|---|---|
| `version`    | string | `latest`                     | Artifact release tag to install. `latest` pulls the most recent published release. Pin to a specific tag (e.g. `t3code-server-v1.2.3`) for reproducibility. |
| `port`       | string | `3773`                       | Port the server binds on. Caddy reaches the server on the shared Docker network at this port. |
| `host`       | string | `0.0.0.0`                    | Host/interface the server binds to. The default lets the relay probe it from the shared Docker network. |
| `secretPath` | string | `/run/t3code/relay-secret`   | Path **inside the container** where the shared relay-secret file is bind-mounted from the host. Must match the `target` of the `mounts` entry in your `devcontainer.json`. |
| `baseDir` | string | empty | Explicit T3 server state directory (`T3CODE_HOME`). Takes precedence over `stateParentDir` and any existing `T3CODE_HOME`. |
| `stateParentDir` | string | empty | Durable parent directory for T3 server state. When set, the feature uses `<stateParentDir>/<DEVCONTAINER_ID-or-HOSTNAME>` as `T3CODE_HOME`. |
| `workspaceHome` | string | empty | Workspace cwd passed to the server. Leave empty to use the `WORKSPACE_HOME` container environment variable when present. |
| `runAsUser` | string | `vscode` | Linux user that runs the T3 server process. Set to empty to run as the entrypoint user. |
| `sshAuthSock` | string | `/tmp/vscode-ssh-agent.sock` | Stable SSH agent socket path exported to the T3 server. The supervisor keeps it linked to VS Code's forwarded socket under `/tmp`. |
| `tailscale` | boolean | `true` | Install and start `tailscaled` in userspace networking mode. Set to `false` to opt out. |
| `tailscaleAuthKeyPath` | string | `/run/t3code/tailscale-authkey` | Path inside the container where a Tailscale auth key file is mounted. The key is read from this file at startup, never from an env var. |
| `tailscaleHostname` | string | empty | Optional Tailscale machine hostname. Leave empty to derive one from the container hostname. |
| `tailscaleStateDir` | string | empty | Optional `tailscaled` state directory. Leave empty to use `/var/lib/tailscale`. |
| `tailscaleServe` | boolean | `true` | Enables the T3 server's Tailscale Serve integration. |
| `tailscaleServePort` | string | `443` | HTTPS port passed to Tailscale Serve. |
| `tailnetDnsName` | string | empty | Optional explicit MagicDNS name for the server to advertise. Leave empty to resolve from `tailscale status --json`. |

## Usage

Minimal `devcontainer.json` (no options overrides needed for standard use):

```jsonc
{
  "features": {
    "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
      "stateParentDir": "/mnt/t3code-state"
    }
  },
  "containerEnv": {
    "DEVCONTAINER_ID": "${devcontainerId}",
    "WORKSPACE_HOME": "${containerWorkspaceFolder}",
    "T3RELAY_URL": "https://relay.t3.example.com"
  },
  "runArgs": [
    "--network=dev-ingress",
    "-l", "devcontainer.id=${devcontainerId}",
    "-h", "<myrepo>",
    "--name", "<myrepo>"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly",
    "source=${localEnv:HOME}/.config/t3relay/tailscale-authkey,target=/run/t3code/tailscale-authkey,type=bind,readonly",
    "source=${localEnv:HOME}/.local/share/t3code-devcontainers,target=/mnt/t3code-state,type=bind"
  ]
}
```

### Required `runArgs` explained

| Flag | Purpose |
|---|---|
| `--network=dev-ingress` | Attach to the shared bridge network so Caddy can reach the server. The network must be created once on the host: `docker network create dev-ingress`. |
| `-l devcontainer.id=...` | Label the container so the `t3code-relay` Caddy module can discover it via the Docker API. |
| `-h <myrepo>` | Set the container hostname. The feature uses this as the default Tailscale machine hostname when `tailscaleHostname` is empty. |
| `--name <myrepo>` | Name the container; the relay uses this as the routing hostname prefix (`<myrepo>.t3.<domain>`). Names must be unique across running containers. |

### Required secret bind mount

The relay module and the in-container server share a secret to authenticate
relay-initiated requests.  The secret lives in a file on the host — never in
environment variables or committed config — and is bind-mounted read-only into
each devcontainer.

The `mounts` entry above mounts `~/.config/t3relay/secret` to the default
`secretPath` of `/run/t3code/relay-secret`.  Create it on the host before
opening the devcontainer:

```bash
mkdir -p ~/.config/t3relay
openssl rand -hex 32 > ~/.config/t3relay/secret
chmod 600 ~/.config/t3relay/secret
```

The same file must be bind-mounted into the `caddy` compose service (see the
top-level `docker-compose.yml`).

If you override `secretPath` in the feature options, update the mount `target`
to match.

### Tailscale direct endpoints

Tailscale is on by default. To join the devcontainer to your tailnet, mount a
Tailscale auth key file at the default `tailscaleAuthKeyPath`:

```jsonc
"mounts": [
  "source=${localEnv:HOME}/.config/t3relay/tailscale-authkey,target=/run/t3code/tailscale-authkey,type=bind,readonly"
]
```

If the file is not present or not readable, `tailscaled` still starts but stays
logged out, and the relay falls back to the existing relay-proxied endpoint.

When Tailscale is joined, the feature starts `tailscaled` in userspace
networking mode and the T3 server enables Tailscale Serve for its local server
port. The server advertises its Tailscale HTTPS endpoint in
`/.well-known/t3/environment`; the relay records that descriptor and returns
the MagicDNS endpoint from `/v1/environments` and `/connect`.

Set `tailnetDnsName` only when automatic MagicDNS discovery is not appropriate:

```jsonc
"features": {
  "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
    "tailnetDnsName": "myrepo.example-tailnet.ts.net"
  }
}
```

Set `tailscaleHostname` only when you want the Tailscale machine name shown in
the admin console and MagicDNS to differ from the container hostname.

```jsonc
"features": {
  "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
    "tailscaleHostname": "myrepo"
  }
}
```

Set `tailscale` to `false` to opt out:

```jsonc
"features": {
  "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
    "tailscale": false
  }
}
```

### On-demand port exposure helper

The feature installs `/usr/local/bin/t3relay`, a small helper for exposing
agent-started web servers through the relay without editing Caddy config.

Set `T3RELAY_URL` in `containerEnv` to your relay API URL:

```jsonc
"containerEnv": {
  "DEVCONTAINER_ID": "${devcontainerId}",
  "T3RELAY_URL": "https://relay.t3.example.com"
}
```

Then, from inside the devcontainer:

```bash
t3relay expose 5173 --name vite
```

The helper registers the port with the relay using the mounted shared-secret
file and prints the public URL:

```text
https://myrepo--vite.t3.example.com
```

Exposure hostnames use the single DNS label `<environment>--<exposure>` so the
relay's existing `*.t3.<domain>` wildcard certificate remains valid. Exposures
expire automatically; the default TTL is one hour and the maximum is one day.

Useful commands:

```bash
t3relay expose 3000
t3relay exposures
t3relay unexpose vite
```

### Persistent server state

The server stores its environment identity, projects, threads, auth sessions,
settings, logs, worktrees, and SQLite state under `T3CODE_HOME`. By default the
server chooses `$HOME/.t3`, which is inside the container and may not survive a
rebuild.

For persistent state, bind-mount a common host directory and set
`stateParentDir` to the mount target:

```jsonc
{
  "features": {
    "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
      "stateParentDir": "/mnt/t3code-state"
    }
  },
  "containerEnv": {
    "DEVCONTAINER_ID": "${devcontainerId}",
    "WORKSPACE_HOME": "${containerWorkspaceFolder}"
  },
  "mounts": [
    "source=${localEnv:HOME}/.local/share/t3code-devcontainers,target=/mnt/t3code-state,type=bind"
  ]
}
```

At container startup the feature resolves:

```text
T3CODE_HOME=/mnt/t3code-state/<DEVCONTAINER_ID>
```

If `DEVCONTAINER_ID` is not set, the supervisor falls back to `HOSTNAME`, then
`unknown`. This keeps distinct devcontainers from sharing one `state.sqlite`
while still allowing one durable parent mount.

Use `baseDir` only when you want to provide the full state directory yourself.
If `baseDir` is set, it wins over `stateParentDir`.

### Runtime user

The devcontainer entrypoint may start as `root`, but the T3 server process
does not need to stay root. By default the supervisor starts the server with:

```text
runAsUser=vscode
```

When the supervisor itself is running as root and `runAsUser` names an existing
Linux user, it prepares the resolved `T3CODE_HOME` directory and then launches
Node through `runuser`. If the user does not exist, the supervisor logs a
warning and falls back to the entrypoint user.

Override `runAsUser` if your image uses a different remote user. Set it to an
empty string only when you intentionally want the server process to run as the
entrypoint user.

### SSH agent forwarding

VS Code injects its forwarded SSH agent socket into the container after early
devcontainer feature entrypoints have already started. That socket usually
appears under `/tmp` as `vscode-ssh-auth-*.sock`, so the T3 server cannot rely
on inheriting the final `SSH_AUTH_SOCK` value from the entrypoint environment.

By default this feature exports:

```text
SSH_AUTH_SOCK=/tmp/vscode-ssh-agent.sock
```

to the T3 server process. A small watcher launched by the supervisor keeps that
stable path symlinked to the newest real VS Code socket when it appears. T3
server children, including terminals and MCP processes, inherit the stable path
before they start, while the symlink target can be repaired later.

Override `sshAuthSock` only if another process already owns the default path.

## How it works

`install.sh` runs during the container image build:

1. Guards that the base image is Ubuntu noble and that `node` is on `PATH`.
2. Downloads the forked server tarball from a GitHub Release on this repo
   (`boblangley/t3code-devcontainer-relay`) for the detected arch
   (`linux-amd64` or `linux-arm64`, glibc).
3. Extracts it to `/usr/local/lib/t3code-server`.
4. Installs the Tailscale CLI/daemon when enabled.
5. Installs the T3 server, Tailscale, and SSH socket watcher run scripts under
   `/usr/local/share`.
6. Installs the `t3relay` helper to `/usr/local/bin/t3relay`.
7. Writes resolved feature options to `/usr/local/etc/t3code-server.env` so the
   runtime scripts can source them without re-parsing `devcontainer.json`.

The devcontainer feature entrypoint is
`/usr/local/share/t3code-entrypoint.sh`. It starts:

| Service | Purpose |
|---|---|
| `tailscaled` | Starts `tailscaled` in userspace networking mode and runs `tailscale up` from the mounted auth key file when available. |
| `t3code-server` | Resolves state/workspace/Tailscale DNS context, then starts the Node server as `runAsUser` when possible. |
| `t3code-ssh-auth-sock-watcher` | Keeps the stable SSH agent socket path linked to VS Code's forwarded socket. Installed only when `sshAuthSock` is non-empty. |

### Server environment variables

The supervise script passes these env vars to the Node process.  Their names
must match what the forked server reads (defined in `vendor-t3code`'s
`bearer-auth` branch — update here if the server names change):

| Variable | Purpose |
|---|---|
| `PORT` | TCP port |
| `T3CODE_HOST` | Host/interface the server binds to |
| `T3CODE_HOME` | Base directory for server state when configured or inherited |
| `T3CODE_RELAY_SECRET_FILE` | Filesystem path to the shared-secret file |
| `T3CODE_TAILSCALE_SERVE` | Enables the server's Tailscale Serve integration |
| `T3CODE_TAILSCALE_SERVE_PORT` | HTTPS port for Tailscale Serve |
| `T3CODE_TAILNET_DNS_NAME` | Optional MagicDNS name advertised in the environment descriptor |

Service logs are written to `/tmp/t3code-server.log`,
`/tmp/tailscaled.log`, and `/tmp/t3code-ssh-auth-sock-watcher.log`.

## Artifact URL convention

The feature downloads server binaries from GitHub Releases on this repo.
Release tags produced by `build-t3code-artifacts.yaml` follow this convention:

```
Tag:   t3code-server-<semver>   (e.g.  t3code-server-v1.2.3)
Asset: t3code-server-linux-amd64.tar.gz
       t3code-server-linux-arm64.tar.gz
```

URL patterns used by `install.sh`:

```
# latest (floating alias):
https://github.com/boblangley/t3code-devcontainer-relay/releases/download/t3code-server-latest/t3code-server-linux-<arch>.tar.gz

# pinned version:
https://github.com/boblangley/t3code-devcontainer-relay/releases/download/t3code-server-<VERSION>/t3code-server-linux-<arch>.tar.gz
```

Pinned installs may use either the full release tag, such as
`t3code-server-v1.2.3`, or just the suffix, such as `v1.2.3`.

**Coordination point:** the asset filename in `install.sh` must exactly match
what `build-t3code-artifacts.yaml` attaches to the release.  If that workflow's
naming changes, update the `ASSET_NAME` variable in `install.sh` to match.

## Supervision notes

The feature uses a normal devcontainer feature entrypoint at
`/usr/local/share/t3code-entrypoint.sh`. Devcontainer entrypoints are composed
into a shell chain with entrypoints from other features, so this feature cannot
require `/init` to run as PID 1.

The entrypoint starts `tailscaled` and `/usr/local/share/t3code-supervise.sh`
in the background, then returns to the composed devcontainer entrypoint chain.
The supervisor runs the T3 server in a restart loop, starts the SSH agent socket
watcher, and logs to `/tmp/t3code-server.log`.
