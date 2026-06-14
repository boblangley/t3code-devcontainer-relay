# t3code-server devcontainer feature

Installs and supervises the forked T3Code server (bearer-auth branch) inside a
devcontainer. The server listens on `0.0.0.0:<port>` and validates inbound relay
requests via a shared-secret file that is bind-mounted from the host.

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
| `port`       | string | `3773`                       | Port the server binds on (`0.0.0.0:<port>`). Caddy reaches the server on the `dev-ingress` network at this port. |
| `secretPath` | string | `/run/t3code/relay-secret`   | Path **inside the container** where the shared relay-secret file is bind-mounted from the host. Must match the `target` of the `mounts` entry in your `devcontainer.json`. |
| `baseDir` | string | empty | Explicit T3 server state directory (`T3CODE_HOME`). Takes precedence over `stateParentDir` and any existing `T3CODE_HOME`. |
| `stateParentDir` | string | empty | Durable parent directory for T3 server state. When set, the feature uses `<stateParentDir>/<DEVCONTAINER_ID-or-HOSTNAME>` as `T3CODE_HOME`. |
| `workspaceHome` | string | empty | Workspace cwd passed to the server. Leave empty to use the `WORKSPACE_HOME` container environment variable when present. |
| `runAsUser` | string | `vscode` | Linux user that runs the T3 server process. Set to empty to run as the entrypoint user. |

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
    "-h", "${devcontainerId}",
    "--name", "<myrepo>"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly",
    "source=${localEnv:HOME}/.local/share/t3code-devcontainers,target=/mnt/t3code-state,type=bind"
  ]
}
```

### Required `runArgs` explained

| Flag | Purpose |
|---|---|
| `--network=dev-ingress` | Attach to the shared bridge network so Caddy can reach the server. The network must be created once on the host: `docker network create dev-ingress`. |
| `-l devcontainer.id=...` | Label the container so the `t3code-relay` Caddy module can discover it via the Docker API. |
| `-h ${devcontainerId}` | Set the hostname to the devcontainer ID so Caddy can derive a stable address. |
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

## How it works

`install.sh` runs during the container image build:

1. Guards that the base image is Ubuntu noble and that `node` is on `PATH`.
2. Downloads the forked server tarball from a GitHub Release on this repo
   (`boblangley/t3code-devcontainer-relay`) for the detected arch
   (`linux-amd64` or `linux-arm64`, glibc).
3. Extracts it to `/usr/local/lib/t3code-server`.
4. Installs `t3code-supervise.sh` to `/usr/local/share/t3code-supervise.sh`.
5. Installs the `t3relay` helper to `/usr/local/bin/t3relay`.
6. Writes resolved feature options to `/usr/local/etc/t3code-server.env` so the
   entrypoint can source them without re-parsing `devcontainer.json`.

`t3code-supervise.sh` runs as the devcontainer `entrypoint` on every container
start:

1. Sources `/usr/local/etc/t3code-server.env`.
2. Checks a PID file at `/tmp/t3code-server.pid` — if the supervisor is already
   running (e.g. after a `docker restart` without a rebuild) it skips re-launch.
3. Resolves `T3CODE_HOME` from `baseDir`, `stateParentDir`, existing
   `T3CODE_HOME`, or the upstream server default.
4. Resolves the server cwd from `workspaceHome`, `WORKSPACE_HOME`, or the
   upstream server default.
5. Starts a restart-loop supervisor in a background subshell: runs the Node
   server as `runAsUser` when possible, logs to `/tmp/t3code-server.log`, backs
   off exponentially (1 → 2 → 4 … → 30 s cap) on repeated crashes.
6. `exec "$@"` to hand off to the container's own command (or `sleep infinity`
   if none is provided), keeping the container alive.

### Server environment variables

The supervise script passes these env vars to the Node process.  Their names
must match what the forked server reads (defined in `vendor-t3code`'s
`bearer-auth` branch — update here if the server names change):

| Variable | Purpose |
|---|---|
| `PORT` | TCP port (`0.0.0.0:PORT`) |
| `T3CODE_HOME` | Base directory for server state when configured or inherited |
| `T3CODE_RELAY_SECRET_FILE` | Filesystem path to the shared-secret file |

Logs are appended to `/tmp/t3code-server.log` inside the container.

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

s6-overlay was evaluated and rejected as overkill for a single supervised
process.  The simple while-loop supervisor is intentional.  If a second
supervised process is ever needed, reconsider s6-overlay at that point.
