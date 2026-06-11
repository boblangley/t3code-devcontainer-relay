# Tailscale Setup

This page explains what Tailscale is, how to add the relay's sidecar node to your tailnet, and
how to configure split DNS so devices on your tailnet can reach `*.t3.example.com`.

---

## What Tailscale does here

A **tailnet** is your own private, encrypted network overlay that connects your devices across the
internet — your phone in a coffee shop, your laptop at work, and your server at home can all
communicate as if they were on the same local network, without any port-forwarding or VPN server
to manage.

The relay stack includes a `tailscale` sidecar container. This container joins your tailnet as a
node named `t3code-relay`. When a Tailscale device (phone, laptop) connects to
`relay.t3.example.com` or any `*.t3.example.com` address, it reaches that node, which forwards
the traffic to Caddy (which terminates TLS and routes to your devcontainers).

**Important:** your local machine does NOT join the tailnet. Only the sidecar container does.
Local access (on the same machine running Docker) uses dnsmasq instead — see
[local-dns.md](local-dns.md).

---

## Prerequisites

- A Tailscale account. Sign up free at [tailscale.com](https://tailscale.com).
- The relay stack cloned and `.env` partially filled in (see [setup-guide.md](setup-guide.md)
  Stages 1–4). You will come back here for Stage 6.

---

## Step 1 — Create an auth key

An auth key lets a device join your tailnet without a browser login. The sidecar container uses
one on start-up.

1. Log in to the [Tailscale admin console](https://login.tailscale.com/admin).
2. Click **Settings** in the left sidebar.
3. Click **Keys**.
4. Click **Generate auth key**.
5. Set a description, e.g. `t3code-relay sidecar`.
6. Check **Reusable** — this lets the key work across container restarts without generating a
   new key each time.
7. Check **Ephemeral** — ephemeral nodes are automatically removed from your tailnet when they
   go offline, keeping the Machines list clean. The trade-off: if the container is down for
   maintenance, the node disappears from the list until it comes back up. This is the recommended
   setting.
8. Click **Generate key**.
9. Copy the key (it starts with `tskey-auth-`). **This is the only time Tailscale will show it.**

---

## Step 2 — Add the key to `.env`

Open `.env` in the repo root and find the line:

```dotenv
TS_AUTHKEY=
```

Paste your key immediately after the `=`, with no spaces:

```dotenv
TS_AUTHKEY=tskey-auth-abc123EXAMPLE
```

**Never commit `.env`.** The auth key lives only in `.env` on your machine.
See [secrets-and-tokens.md](secrets-and-tokens.md) for why credentials belong here.

---

## Step 3 — Start (or restart) the stack

If the stack is already running (from Stage 5 of [setup-guide.md](setup-guide.md)), restart it to
pick up the new key:

```bash
docker compose up -d
```

If you are running this step for the first time, this command starts all three services. The
`tailscale` container will authenticate and join your tailnet within a few seconds.

---

## Step 4 — Verify the node appears

1. Go to the [Tailscale admin console](https://login.tailscale.com/admin) → **Machines**.
2. You should see a machine named **t3code-relay** in the list with a green status dot.
3. Note the tailnet IP address shown for it (a `100.x.x.x` address). You will use it in Step 5.

If the node does not appear after 30 seconds, check the sidecar logs:

```bash
docker compose logs tailscale
```

Look for `Login URL:` (means the key was not accepted and the container is waiting for a browser
login — this should not happen with a valid auth key) or `authenticated` (success).

---

## Step 5 — Configure split DNS

**Split DNS** means "for names under a specific domain, ask this DNS server instead of the public
internet." Without this, devices on your tailnet cannot resolve `relay.t3.example.com` because
there are no public DNS records for it.

You will tell Tailscale: "for anything under `t3.example.com`, ask the sidecar node."

1. In the admin console, click **DNS** in the left sidebar.
2. Scroll to **Nameservers** and click **Add nameserver** → **Custom**.
3. In the **Nameserver** field, enter the tailnet IP of `t3code-relay` (the `100.x.x.x` address
   from Step 4).
4. Check **Restrict to domain** and enter `t3.example.com`.
5. Click **Save**.

Tailscale now routes DNS queries for `*.t3.example.com` from tailnet devices to the sidecar node,
which in turn resolves them to its own tailnet IP (because Caddy is listening on the sidecar's
forwarded port 443).

> **Substitute your domain:** replace `t3.example.com` with `t3.yourdomain.com` everywhere above.

---

## Step 6 — Verify from a tailnet device

Install Tailscale on a second device (phone or laptop). Sign in with the same Tailscale account.
Connect to your tailnet.

From that device, open a browser and go to:

```
https://relay.t3.example.com/health
```

Expected response:

```json
{"ok":true,"service":"relay"}
```

If you see a TLS error, the wildcard certificate may still be issuing — wait a minute and retry.
If you see a DNS failure ("site not found"), split DNS may not have propagated yet; wait 30
seconds and retry, or check that the nameserver IP in the Tailscale DNS settings matches the
`100.x.x.x` address of `t3code-relay` exactly.

---

## Troubleshooting

**Node does not appear in the Machines list**

- Check `docker compose logs tailscale` for error messages.
- Make sure `TS_AUTHKEY` in `.env` is set and has no trailing whitespace. Recreate the container
  with `docker compose up -d --force-recreate tailscale`.
- Auth keys expire. Go to **Settings** → **Keys** in the admin console and confirm the key is
  still valid. If it expired, generate a new one and update `.env`.

**Split DNS not working — tailnet devices cannot resolve `*.t3.example.com`**

- Confirm the nameserver IP in **DNS** → **Nameservers** exactly matches the tailnet IP of
  `t3code-relay`. Tailnet IPs are stable but double-check they match.
- On some platforms Tailscale needs the app to be open and connected for split DNS to take effect.
  Ensure Tailscale is connected (not just installed) on the device.
- Some corporate network configurations block Tailscale's DNS push. If you manage the device
  yourself, confirm "Use Tailscale DNS settings" is enabled in the Tailscale app preferences.

**TLS error when connecting over Tailscale (`certificate is not trusted` or `ERR_CERT_AUTHORITY_INVALID`)**

- This usually means Caddy has not yet issued the wildcard certificate. Check
  `docker compose logs caddy` for `certificate obtained successfully`. If the cert has issued and
  you still see this error, your browser may have cached an earlier TLS failure — clear the
  cache or try a private/incognito window.

**"Ephemeral" node keeps disappearing**

- This is expected behaviour: ephemeral nodes are removed when the container stops. The node will
  reappear when `docker compose up -d` brings the `tailscale` container back up and it
  re-authenticates. If you want the node to persist even when the container is down, uncheck
  **Ephemeral** when generating the auth key.

**Port 443 not reachable on the sidecar node**

- The sidecar is on the `ts-ingress` internal Docker network alongside `caddy`. It forwards raw
  TCP 443 to `caddy:443` using the inline `ts_serve` config in `docker-compose.yml`. Confirm
  both containers are running: `docker compose ps`.
