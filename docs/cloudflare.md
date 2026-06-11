# Cloudflare Setup

This page explains what Cloudflare does for the relay, how to create the API token it needs,
and how to confirm the wildcard certificate issued successfully.

---

## What Cloudflare is doing here

A TLS certificate (the thing that makes `https://` work and shows the padlock in your browser)
requires you to prove you own the domain. The relay uses a method called **DNS-01 challenge**:
instead of serving a file over HTTP, it asks Cloudflare to temporarily create a special DNS record
on your behalf. The certificate authority (Let's Encrypt) reads that record, confirms ownership,
and issues the certificate. Cloudflare then deletes the temporary record.

**Important:** this process creates no permanent DNS records. No public DNS record ever points at
your machine. Your home IP address is never exposed. Cloudflare is used *only* for the DNS-01
challenge.

The certificate that gets issued covers `*.t3.example.com` — a wildcard certificate. A wildcard
certificate is one certificate that is valid for every subdomain: `relay.t3.example.com`,
`web.t3.example.com`, `myrepo.t3.example.com`, and so on. Caddy (the TLS proxy inside the relay
stack) handles obtaining and renewing this certificate automatically.

---

## Prerequisites

- Your domain (e.g. `example.com`) must be added to Cloudflare and its nameservers must point at
  Cloudflare. If you registered the domain elsewhere, follow Cloudflare's guide to
  [change nameservers](https://developers.cloudflare.com/dns/zone-setups/full-setup/).

---

## Step 1 — Create a scoped API token

The token you create gives Caddy exactly one permission: edit DNS records on your zone. It cannot
touch anything else in your Cloudflare account.

1. Log in to the [Cloudflare dashboard](https://dash.cloudflare.com).
2. Click your avatar (top-right corner) → **My Profile**.
3. Click **API Tokens** in the left sidebar.
4. Click **Create Token**.
5. Find the **Edit zone DNS** template and click **Use template**.
6. Under **Permissions**, verify the row reads: Zone · DNS · Edit.
7. Under **Zone Resources**, change the first dropdown from "All zones" to
   **Specific zone**, then select your domain (e.g. `example.com`) from the third dropdown.
8. Leave everything else as-is and click **Continue to summary**.
9. Review the summary and click **Create Token**.
10. Copy the token string that appears. **This is the only time Cloudflare will show it.**

The token looks something like `abc123XYZ_example_longstring`. Store it somewhere safe; you will
paste it into `.env` in the next step.

---

## Step 2 — Add the token to `.env`

Open `.env` in the repo root (copy it from `.env.example` first if you have not done so):

```bash
cp .env.example .env
```

Find the line:

```dotenv
CF_API_TOKEN=
```

Paste your token immediately after the `=` sign, with no spaces:

```dotenv
CF_API_TOKEN=abc123XYZ_example_longstring
```

**Never commit `.env`.** The file is listed in `.gitignore`, but double-check with
`git status` — `.env` should never appear as a staged file. The token lives only in `.env` on
your machine. See [secrets-and-tokens.md](secrets-and-tokens.md) for why.

---

## Step 3 — Verify the certificate issues

After you run `docker compose up -d` (Stage 5 of [setup-guide.md](setup-guide.md)), Caddy
contacts Let's Encrypt and uses your Cloudflare token to complete the DNS-01 challenge.

Watch the logs:

```bash
docker compose logs -f caddy
```

Successful output looks like:

```
caddy  | {"level":"info","msg":"certificate obtained successfully","identifiers":["*.t3.example.com"]}
```

This normally takes 30–90 seconds on the first start. Once you see it, press Ctrl-C to stop
following the logs.

You can also verify with curl:

```bash
curl -s https://relay.t3.example.com/health
```

Expected response: `{"ok":true,"service":"relay"}`

If you see a TLS error instead, the certificate has not yet issued — wait another minute and
retry.

---

## Troubleshooting

**"Error getting certificate" or ACME challenge failed**

The most common cause is wrong token permissions. In the Cloudflare dashboard, go to
**My Profile** → **API Tokens**, click the token name, and confirm it has Zone · DNS · Edit on
your specific zone. If you accidentally selected "All zones" with read-only permission, delete the
token and create a new one.

Check that the token value in `.env` is correct and has no trailing spaces or newlines:

```bash
grep CF_API_TOKEN .env
```

**"Network ts-ingress not found" or compose error on start**

This is a compose networking issue, not a Cloudflare issue. Make sure you ran
`docker network create dev-ingress` before `docker compose up -d`.

**Certificate issued but `curl` still gives a TLS error on first try**

Caddy caches the certificate on disk (`caddy-data` volume). If you deleted volumes and restarted,
it re-issues automatically — just wait another 30–90 seconds.

**"Too many certificates" error from Let's Encrypt**

Let's Encrypt rate-limits certificate issuance per domain. If you have restarted Caddy many times
in testing, you may hit this limit. Wait a few hours, or check the Caddy docs on using the
Let's Encrypt staging endpoint for initial testing.

**Cloudflare "Authentication error" (code 10000)**

Your API token may have expired or been deleted. Generate a new one following Step 1 above,
update `.env`, and restart: `docker compose up -d`.
