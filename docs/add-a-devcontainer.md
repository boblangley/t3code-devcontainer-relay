# Add a Devcontainer

This page explains how to make any project's devcontainer automatically appear in the relay, what
each configuration line does, and how to verify it is working.

---

## Background

A **devcontainer** is a Docker container that your editor (VS Code, Cursor, etc.) uses as a
development environment — your code, tools, and terminal all run inside it. The relay discovers
devcontainers automatically, but only if they carry the right Docker labels and are on the right
Docker network.

A **reverse proxy** is software that sits in front of other services and forwards requests to
them. Caddy is the reverse proxy here: it receives HTTPS requests for
`myrepo.t3.example.com` and forwards them to the T3Code server inside your devcontainer.

You do not need to touch Caddy or the relay stack. You only need to add a few lines to your
project's `devcontainer.json`.

---

## Prerequisites

- The relay stack is running (`docker compose ps` shows `caddy` and `web` `Up`).
- You have already created the `dev-ingress` network and the shared-secret file as described in
  [setup-guide.md](setup-guide.md) Stages 3 and 5. If not, run:

  ```bash
  docker network create dev-ingress
  mkdir -p ~/.config/t3relay
  openssl rand -hex 32 > ~/.config/t3relay/secret
  chmod 600 ~/.config/t3relay/secret
  ```

  The `dev-ingress` network must be created **once** on the host. All devcontainers and the
  relay stack share it. You do not recreate it for each project.

---

## The devcontainer.json snippet

Open (or create) `.devcontainer/devcontainer.json` in your project. Add the following, merging
it with any configuration you already have:

```jsonc
{
  "features": {
    "ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
      "stateParentDir": "/mnt/t3code-state"
    },
    "ghcr.io/devcontainers/features/sshd:1": {}
  },
  "containerEnv": {
    "DEVCONTAINER_ID": "${devcontainerId}",
    "WORKSPACE_HOME": "${containerWorkspaceFolder}",
    "T3RELAY_URL": "https://relay.t3.example.com"
  },
  "runArgs": [
    "--network=dev-ingress",
    "-l", "devcontainer.id=${devcontainerId}",
    "-h", "myrepo",
    "--name", "myrepo"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly",
    "source=${localEnv:HOME}/.local/share/t3code-devcontainers,target=/mnt/t3code-state,type=bind"
  ]
}
```

Replace `myrepo` with a short, lowercase name for your project. This name becomes the
subdomain: `myrepo.t3.example.com`.

---

## Line-by-line explanation

### `features` block

```jsonc
"ghcr.io/boblangley/t3code-devcontainer-relay/t3code-server:1": {
  "stateParentDir": "/mnt/t3code-state"
}
```

This installs the `t3code-server` feature inside the devcontainer. The feature downloads the
forked T3Code server binary and sets up a process that keeps it running. The server listens on
port 3773 inside the container and waits for connections from the relay.

The `:1` at the end pins the major version so you get updates within the same major version
(bug fixes, security patches) but not breaking changes.

Node.js (required by the server) is installed automatically as a dependency of this feature —
you do not need to add a Node feature separately.

`stateParentDir` points at a durable bind mount. At container startup, the feature stores server
state under `/mnt/t3code-state/<DEVCONTAINER_ID>` so each devcontainer keeps a stable environment
identity and SQLite database without sharing state with other devcontainers.

```jsonc
"ghcr.io/devcontainers/features/sshd:1": {}
```

Installs and starts an SSH server inside the devcontainer. The relay accepts VS Code Remote-SSH
connections on tailnet port 22 and forwards them to the feature's internal SSH port.

### `containerEnv`

```jsonc
"DEVCONTAINER_ID": "${devcontainerId}"
```

Exposes the stable devcontainer ID inside the container. The feature uses this as the state
subdirectory name when `stateParentDir` is set.

```jsonc
"WORKSPACE_HOME": "${containerWorkspaceFolder}"
```

Exposes the workspace path inside the container. The feature passes this as the T3 server cwd so
the initial project is the workspace instead of `/`.

```jsonc
"T3RELAY_URL": "https://relay.t3.example.com"
```

Lets the in-container `t3relay` helper know where to register on-demand port
exposures. Substitute your actual relay domain.

### `runArgs`

These are arguments passed to `docker run` when the devcontainer starts. Each one is a separate
string in the array.

```jsonc
"--network=dev-ingress"
```

Attaches the container to the `dev-ingress` Docker network. The relay's Caddy container is also
on this network. This is what allows Caddy to reach port 3773 inside your devcontainer — they
can talk to each other because they are on the same network.

```jsonc
"-l", "devcontainer.id=${devcontainerId}"
```

Sets a Docker label on the container. `${devcontainerId}` is a variable that the devcontainer
CLI replaces with a stable, unique identifier for this project. The relay's discovery process
watches for containers carrying this exact label — it is how the relay knows "this container is
a devcontainer, not some random Docker process."

```jsonc
"-h", "myrepo"
```

Sets the hostname inside the container. The T3Code server feature uses this as
the default Tailscale machine hostname.

```jsonc
"--name", "myrepo"
```

Sets the Docker container name. **This name becomes the subdomain:** a container named `myrepo`
is reachable at `myrepo.t3.example.com`. Choose a short, lowercase, hyphen-allowed name.

> **Naming rules:** the container name is sanitized (lowercased, non-alphanumeric characters
> replaced with hyphens). If two containers end up with the same sanitized name, a short suffix
> from the container ID is appended automatically. You can override the hostname explicitly with
> the Docker label `t3relay.host=mycustomname` in your `runArgs`.

### `mounts` block

```jsonc
"source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly"
```

Mounts the shared-secret file from your host machine into the container (read-only). The T3Code
server inside the container reads this file to verify that requests carrying `X-Relay-Secret`
are genuinely from the relay, not from an attacker on the container's network.

`${localEnv:HOME}` expands to your home directory on the host (e.g. `/home/yourname`). The
target path `/run/t3code/relay-secret` is where the server looks for the secret by default.

**The secret value itself never appears in `devcontainer.json` or any committed file** — only the
path to the file is here, and the file on disk is only readable by you (mode 600).

---

## Supported base image

The `t3code-server` feature requires Ubuntu 24.04 ("Noble"). The recommended base image is:

```jsonc
"image": "mcr.microsoft.com/devcontainers/base:noble"
```

If your `devcontainer.json` uses a different base image and you see an error like "unsupported
OS" during container build, switch to `base:noble` or add the feature only to Noble-based images.
Refer to the feature README on GHCR for the full list of supported bases.

---

## Opening the devcontainer

In VS Code (or Cursor, or another editor with devcontainer support):

1. Open the project folder.
2. Press `Ctrl+Shift+P` (or `Cmd+Shift+P` on macOS) → **Dev Containers: Reopen in Container**.
3. The editor builds or pulls the container image and starts it.

For VS Code: if the project was already open, use **Dev Containers: Rebuild and Reopen in
Container** after changing `devcontainer.json`.

The relay discovers the new container within 30 seconds (the relay reconciles Docker state on a
periodic schedule). You do not need to restart the relay stack.

---

## Connecting with VS Code Remote-SSH

The relay also listens on tailnet TCP port 22 and acts as an SSH jump server. It accepts only
SSH, only user `vscode`, and only forwarding requests to known devcontainer hostnames on port 22.

Add this to your client machine's `~/.ssh/config`, replacing `example.com`:

```sshconfig
Host t3-gateway
  HostName relay.t3.example.com
  User vscode
  Port 22
  ForwardAgent no

Host *.t3.example.com
  User vscode
  Port 22
  ProxyJump t3-gateway
  ForwardAgent yes
```

Then connect VS Code Remote-SSH to:

```text
myrepo.t3.example.com
```

The first connection to the gateway asks you to accept the relay SSH host key. The key is generated
once and persisted in the relay data volume.

---

## Verifying it works

After the devcontainer starts, confirm the relay sees it:

**In the desktop or web client:** the environment list should show your new devcontainer.
Refresh the list if needed.

**In the browser:** open `https://myrepo.t3.example.com` (substitute your container name and
domain). You should reach the T3Code server. A 502 or 504 usually means the server inside the
container is still starting up — wait 30 seconds and try again.

**From the command line:**

```bash
curl -s https://relay.t3.example.com/v1/environments \
  -H "Authorization: Bearer YOUR_RELAY_TOKEN"
```

The response will be a JSON object with an `environments` array. Your devcontainer should appear
in the list with `"status": "running"`.

## Exposing agent-started web servers

When an agent starts a web server on an arbitrary port, register it from inside
the devcontainer:

```bash
t3relay expose 5173 --name vite
```

The helper prints a URL like:

```text
https://myrepo--vite.t3.example.com
```

The hostname stays one label under `t3.example.com`, so the relay's existing
`*.t3.example.com` wildcard certificate remains valid. If you omit `--name`,
the port number is used:

```bash
t3relay expose 3000
# https://myrepo--3000.t3.example.com
```

Exposures default to a one-hour TTL. Use `--ttl <seconds>` to change it, up to
one day. To inspect or remove exposures:

```bash
t3relay exposures
t3relay unexpose vite
```

---

## Troubleshooting

**Devcontainer does not appear in the relay or client**

- Confirm the container is running: `docker ps | grep myrepo`. If it is not running, the
  devcontainer failed to start — check your editor's devcontainer logs.
- Confirm the container is on `dev-ingress`:
  `docker inspect myrepo | grep -A5 Networks`. You should see `dev-ingress` in the output.
- Check the `devcontainer.id` label:
  `docker inspect myrepo | grep devcontainer.id`. If it is missing, the `-l devcontainer.id=...`
  runArg may have a typo.
- Check relay logs for discovery messages: `docker compose logs caddy | tail -50`.

**"dev-ingress network not found" error when opening the devcontainer**

The `dev-ingress` network does not exist on the host. Create it:

```bash
docker network create dev-ingress
```

This only needs to be done once per host machine.

**The container starts but `myrepo.t3.example.com` returns 502 or 504**

- The T3Code server inside the container may still be starting. Wait 30 seconds and try again.
- Check the server logs inside the container: open a terminal in the devcontainer and run
  `cat /tmp/t3code-server.log`. Look for startup errors.
- Confirm the secret file is mounted: inside the devcontainer, run
  `ls -la /run/t3code/relay-secret`. If the file is missing, the `mounts` entry may be
  misconfigured, or `~/.config/t3relay/secret` does not exist on the host.

**Two projects end up at the same subdomain**

If two containers have the same `--name`, the second one will get a suffix (e.g.
`myrepo-a1b2c3.t3.example.com`). To give a container an explicit hostname, add a label:

```jsonc
"-l", "t3relay.host=my-custom-name"
```

Use a name that is unique across all your devcontainers.

**Relay shows the environment as `stopped` even though the container is running**

- The relay marks environments `stopped` when it cannot probe the T3Code server on port 3773.
  Check that the feature installed correctly by running `cat /tmp/t3code-server.log` inside the
  container.
- Verify port 3773 is reachable from within the `dev-ingress` network:
  from another container on `dev-ingress`, run `curl http://myrepo:3773/.well-known/t3/environment`.

**Secret file permission error**

The secret file at `~/.config/t3relay/secret` must be readable by the user who owns the Docker
socket. Confirm with:

```bash
ls -la ~/.config/t3relay/secret
```

Expected: `-rw------- 1 yourname yourgroup`. If it is owned by root, fix it:

```bash
sudo chown $USER ~/.config/t3relay/secret
```
