# Setup Guide

This guide walks you through setting up t3code-devcontainer-relay from scratch on a fresh machine.
By the end you will have a T3Code client on your desktop or phone listing a running devcontainer,
accessible over your local network and over the internet through Tailscale.

**What you need before you start:**

- A domain name managed on Cloudflare (e.g. `example.com`). You do not need to host a website —
  you just need the domain registered and its DNS controlled by Cloudflare.
- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (macOS or Windows) or Docker
  Engine + Docker Compose (Linux) installed and running.
- A [Tailscale](https://tailscale.com) account (free tier is fine).
- Git installed.
- About 1 hour end-to-end, plus a few minutes of waiting while certificates issue.

Substitute `example.com` for your own domain in every command and config snippet below.

---

## Stage 1 — Clone the repository

Open a terminal and clone the monorepo to your machine:

```bash
git clone --recurse-submodules https://github.com/boblangley/t3code-devcontainer-relay.git
cd t3code-devcontainer-relay
```

`--recurse-submodules` also checks out the forked T3Code source inside `vendor-t3code/`.
You should see a directory listing including `docker-compose.yml`, `.env.example`, and `docs/`.

---

## Stage 2 — Cloudflare: create a scoped API token

The relay needs to prove to the certificate authority that you own `*.t3.example.com`. It does
this via a mechanism called DNS-01 — Cloudflare temporarily adds a DNS record on your behalf, the
certificate authority checks for it, and the record is deleted. **No public DNS record ever points
at your machine.**

Full details: [cloudflare.md](cloudflare.md)

**Short version:**

1. Log in to the [Cloudflare dashboard](https://dash.cloudflare.com).
2. Go to **My Profile** (top-right avatar) → **API Tokens** → **Create Token**.
3. Choose **Edit zone DNS** template.
4. Under **Zone Resources** → restrict to your specific zone (e.g. `example.com`).
5. Click **Continue to summary** → **Create Token**.
6. Copy the token. You will paste it into `.env` in Stage 4.

Verification: see [cloudflare.md](cloudflare.md) for how to confirm the cert issues after
Stage 5.

---

## Stage 3 — Secrets and tokens

Three separate credentials power the stack. They have different jobs and live in different places.
Full details: [secrets-and-tokens.md](secrets-and-tokens.md)

**Create the shared-secret file** (one-time host setup):

```bash
mkdir -p ~/.config/t3relay
openssl rand -hex 32 > ~/.config/t3relay/secret
chmod 600 ~/.config/t3relay/secret
```

This creates a random 64-character hex string readable only by you. The relay uses it to prove to
each devcontainer that requests arriving on the internal Docker network are genuinely from the
relay, not from somewhere else.

**Generate relay bearer tokens** — one per person or device:

```bash
openssl rand -hex 32
```

Run this once for each person or device that will connect. Copy each output; you will paste them
into `.env` in Stage 4.

---

## Stage 4 — Fill in `.env`

Copy the example file and open it:

```bash
cp .env.example .env
```

Open `.env` in any text editor. Fill in each value:

```dotenv
CF_API_TOKEN=<paste your Cloudflare token here>
T3_DOMAIN=example.com
RELAY_TOKENS=<paste one or more tokens here, comma-separated>
RELAY_SECRET_FILE=~/.config/t3relay/secret
TS_AUTHKEY=<leave blank for now — filled in Stage 6>
TAILSCALE_HOSTNAME=t3code-relay
```

**Never commit `.env`.** It is listed in `.gitignore`. The secrets in it are yours alone.
See [secrets-and-tokens.md](secrets-and-tokens.md) for why each credential lives where it does.

---

## Stage 5 — Create the host network and start the stack

The relay stack and all devcontainers share a Docker network called `dev-ingress`. Docker networks
are isolated communication channels between containers — creating this network once lets every
future devcontainer talk to the relay without any extra configuration.

Create the network:

```bash
docker network create dev-ingress
```

You should see a long hex string (the network ID) printed and no error.

Now start the compose stack:

```bash
docker compose up -d
```

This pulls the images (the first run takes a few minutes) and starts two services: `caddy`
(the TLS proxy, relay, embedded tailnet node, and tailnet DNS server) and `web`
(the browser client).

Optional: expose local files through the relay mount browser by placing bind mounts or named
volumes under `/mnt/t3relay` in a local compose override. For example:

```yaml
services:
  caddy:
    volumes:
      - ~/notes:/mnt/t3relay/notes:ro
```

After restart, open `https://relay.t3.example.com/mounts` and enter one of your relay bearer
tokens. The browser can render `.html`, `.markdown`, and image files, and can show source for
non-binary files.

Check that both services are running:

```bash
docker compose ps
```

Both services should show `running` or `Up`. Check caddy's logs to watch the wildcard
certificate issue:

```bash
docker compose logs -f caddy
```

Look for lines containing `certificate obtained successfully` or `tls: obtained certificate`.
This takes up to 2 minutes on first start. If you see errors, see
[cloudflare.md — Troubleshooting](cloudflare.md#troubleshooting).

---

## Stage 6 — Tailscale: join the tailnet and configure split DNS

A tailnet is your own private network overlay across your devices — traffic between tailnet members
is encrypted end-to-end, even over the public internet. The relay embeds a Tailscale node inside
the `caddy` container so that your phone or laptop on the road can reach your devcontainers.

Full details: [tailscale.md](tailscale.md)

**Short version:**

1. Log in to the [Tailscale admin console](https://login.tailscale.com/admin).
2. Go to **Settings** → **Keys** → **Generate auth key**.
   Check **Reusable** and **Ephemeral** → **Generate key**. Copy the key.
3. Paste it into `.env` as `TS_AUTHKEY=tskey-auth-...`.
4. Optionally set `TAILSCALE_HOSTNAME=` in `.env` if you want a different machine name.
5. Restart the stack: `docker compose up -d`.
6. In the admin console → **Machines**, confirm `t3code-relay` appears.
7. Configure split DNS: **DNS** → **Add nameserver** → custom, enter the tailnet IP of
   `t3code-relay`, restrict to domain `t3.example.com`.

Verification: install Tailscale on a phone or second laptop, connect to your tailnet, and open
`https://relay.t3.example.com/health` — you should see `{"ok":true,"service":"relay"}`.

---

## Stage 7 — Local DNS: make `*.t3.example.com` resolve on this machine

When your browser or the desktop client on this machine tries to reach
`relay.t3.example.com`, it needs to know what IP address that name points to. Your router and
public DNS do not know about it. dnsmasq is a small local DNS server that handles this by mapping
every name under `*.t3.example.com` to `127.0.0.1` (localhost), where Docker is listening on
port 443.

Full details: [local-dns.md](local-dns.md)

**Short version (macOS):**

```bash
brew install dnsmasq
echo "address=/t3.example.com/127.0.0.1" >> $(brew --prefix)/etc/dnsmasq.conf
sudo brew services start dnsmasq
sudo mkdir -p /etc/resolver
echo "nameserver 127.0.0.1" | sudo tee /etc/resolver/t3.example.com
```

**Short version (Linux with systemd-resolved):**

```bash
sudo apt install dnsmasq
echo "address=/t3.example.com/127.0.0.1" | sudo tee /etc/dnsmasq.d/t3relay.conf
# Disable systemd-resolved's stub listener first — see local-dns.md
sudo systemctl restart dnsmasq
```

Verification:

```bash
dig relay.t3.example.com
```

You should see `127.0.0.1` in the `ANSWER SECTION`. If not, see
[local-dns.md — Troubleshooting](local-dns.md#troubleshooting).

---

## Stage 8 — Install the T3Code client

Download the desktop app for your OS from this repo's GitHub Releases page and configure it to
point at your relay.

Full details: [client-install.md](client-install.md)

**Short version:**

1. Go to [github.com/boblangley/t3code-devcontainer-relay/releases](https://github.com/boblangley/t3code-devcontainer-relay/releases).
2. Open the latest `t3code-desktop-*` release, or `t3code-desktop-latest`, and download the build for your OS and architecture.
3. On macOS: if macOS blocks the app, run
   `xattr -dr com.apple.quarantine /Applications/T3Code.app`.
   On Windows: if SmartScreen appears, click **More info** → **Run anyway**.
4. Launch the app. When prompted, enter:
   - Relay URL: `https://relay.t3.example.com`
   - Bearer token: one of the tokens you generated in Stage 3.

Alternatively, open `https://web.t3.example.com` in your browser — same interface, no install
needed.

---

## Stage 9 — Add your first devcontainer

With the stack running and the client configured, you can make any devcontainer appear in the
relay automatically by adding the feature, relay run arguments, and the shared mounts to its
`devcontainer.json`.

Full details: [add-a-devcontainer.md](add-a-devcontainer.md)

**Short version:** in your project's `.devcontainer/devcontainer.json`, add:

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
    "WORKSPACE_HOME": "${containerWorkspaceFolder}"
  },
  "runArgs": [
    "--network=dev-ingress",
    "-l", "devcontainer.id=${devcontainerId}",
    "-h", "${devcontainerId}",
    "--name", "myrepo"
  ],
  "mounts": [
    "source=${localEnv:HOME}/.config/t3relay/secret,target=/run/t3code/relay-secret,type=bind,readonly",
    "source=${localEnv:HOME}/.local/share/t3code-devcontainers,target=/mnt/t3code-state,type=bind"
  ]
}
```

Replace `myrepo` with your repository name. Open (or reopen) the container in VS Code or your
editor. Within a minute, it will appear in the relay's environment list and will be accessible at
`https://myrepo.t3.example.com`.

For VS Code Remote-SSH, add the SSH config from
[add-a-devcontainer.md](add-a-devcontainer.md#connecting-with-vs-code-remote-ssh) and connect to
`myrepo.t3.example.com`.

---

## What you have now

| Address | What it is |
|---|---|
| `https://relay.t3.example.com` | The relay API your client connects to |
| `https://web.t3.example.com` | The zero-install browser client |
| `https://myrepo.t3.example.com` | Direct route to your devcontainer's T3Code server |
| `ssh vscode@myrepo.t3.example.com` | Remote-SSH route to your devcontainer through the relay |

If something is not working, check the per-topic pages for detailed troubleshooting:
[cloudflare.md](cloudflare.md) · [local-dns.md](local-dns.md) · [tailscale.md](tailscale.md) ·
[secrets-and-tokens.md](secrets-and-tokens.md) · [client-install.md](client-install.md) ·
[add-a-devcontainer.md](add-a-devcontainer.md)
