# Tailscale Setup

This page explains what Tailscale is, how to add the relay's embedded tailnet node to your
tailnet, and how to configure split DNS so devices on your tailnet can reach
`*.t3.example.com`.

---

## What Tailscale does here

A **tailnet** is your own private, encrypted network overlay that connects your devices across the
internet.

The relay stack embeds a Tailscale node directly inside the `caddy` container using
[`tsnet`](https://tailscale.com/docs/features/tsnet). That embedded node joins your tailnet as a
machine such as `t3code-relay`.

When a Tailscale device looks up `relay.t3.example.com` or any `*.t3.example.com` address:

1. Tailscale sends the DNS query to the relay node because of your split-DNS rule.
2. The relay answers with its own tailnet IP.
3. The device connects to that tailnet IP on TCP 443.
4. The relay forwards that connection to the local Caddy listener, which terminates TLS and routes
   to the correct devcontainer.

**Important:** your local machine does NOT join the tailnet. Local access still uses dnsmasq on
the host — see [local-dns.md](local-dns.md).

---

## Prerequisites

- A Tailscale account. Sign up free at [tailscale.com](https://tailscale.com).
- The relay stack cloned and `.env` partially filled in (see [setup-guide.md](setup-guide.md)
  Stages 1–4).

---

## Step 1 — Create an auth key

An auth key lets the embedded relay node join your tailnet without a browser login.

1. Log in to the [Tailscale admin console](https://login.tailscale.com/admin).
2. Click **Settings** → **Keys**.
3. Click **Generate auth key**.
4. Set a description, e.g. `t3code-relay embedded node`.
5. Check **Reusable**.
6. Check **Ephemeral**.
7. Click **Generate key**.
8. Copy the key (`tskey-auth-...`).

---

## Step 2 — Add the key to `.env`

Open `.env` and set:

```dotenv
TS_AUTHKEY=tskey-auth-abc123EXAMPLE
TAILSCALE_HOSTNAME=t3code-relay
```

Change `TAILSCALE_HOSTNAME` if you want a different machine name in the tailnet.

---

## Step 3 — Start or restart the stack

```bash
docker compose up -d
```

The `caddy` container will start the embedded `tsnet` node and join your tailnet within a few
seconds.

---

## Step 4 — Verify the node appears

1. Go to the [Tailscale admin console](https://login.tailscale.com/admin) → **Machines**.
2. You should see a machine named `t3code-relay` or whatever you set in `TAILSCALE_HOSTNAME`.
3. Note its tailnet IP (`100.x.x.x`). You will use it in Step 5.

If the node does not appear after 30 seconds, check:

```bash
docker compose logs caddy
```

Look for tsnet startup messages or a login URL.

---

## Step 5 — Configure split DNS

Without split DNS, tailnet devices cannot resolve `relay.t3.example.com` because there are no
public DNS records for these hosts.

In the Tailscale admin console:

1. Click **DNS**.
2. Under **Nameservers**, click **Add nameserver** → **Custom**.
3. Enter the tailnet IP of the relay node from Step 4.
4. Check **Restrict to domain** and enter `t3.example.com`.
5. Click **Save**.

Repeat the same process for each additional `t3.<domain>` zone you choose to serve.

> **Substitute your domain:** replace `t3.example.com` with `t3.yourdomain.com`.

---

## Step 6 — Verify from a tailnet device

From a second tailnet device, open:

```text
https://relay.t3.example.com/health
```

Expected:

```json
{"ok":true,"service":"relay"}
```

---

## Troubleshooting

**Node does not appear in the Machines list**

- Check `docker compose logs caddy`.
- Confirm `TS_AUTHKEY` is set in `.env`.
- Recreate the relay container with `docker compose up -d --force-recreate caddy`.

**Split DNS is not working**

- Confirm the nameserver IP exactly matches the relay node's tailnet IP.
- Confirm the restricted domain matches the zone you intend to serve, e.g. `t3.example.com`.
- Make sure the Tailscale client on the device is connected and allowed to use Tailscale DNS.

**TLS error over Tailscale**

- Check `docker compose logs caddy` and wait for the wildcard certificate to finish issuing.

**Ephemeral node disappears**

- This is expected when the container stops. Use a non-ephemeral auth key if you want the node to
  remain listed while offline.
