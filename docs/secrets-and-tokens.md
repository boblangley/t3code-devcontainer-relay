# Secrets and Tokens

This page describes the three credentials the relay uses, where each one lives, how to generate
strong values, and how to revoke access when needed.

---

## The three credentials and their jobs

The relay uses three completely separate credentials. Each one does a different job — understanding
the difference will help if something is misconfigured.

### 1. `CF_API_TOKEN` — lets Caddy prove domain ownership

When Caddy obtains the wildcard TLS certificate for `*.t3.example.com`, it must prove to Let's
Encrypt that you control `example.com`. It does this by temporarily creating a DNS record on
your behalf using this Cloudflare API token.

This token is only used at certificate issue/renewal time (roughly every 90 days). It is never
sent to clients or devcontainers. It lives in `.env` and is passed to the `caddy` container as
an environment variable.

See [cloudflare.md](cloudflare.md) for how to create this token with the exact minimum permissions.

### 2. `RELAY_TOKENS` — lets a client (person/device) in

When your desktop app or browser points at `relay.t3.example.com`, it must include a bearer
token in every request. The relay checks the incoming token against `RELAY_TOKENS`. If it matches,
the request is allowed; if not, the relay returns 401 Unauthorized.

You generate one token per person or device so you can revoke individual access without affecting
others. These tokens live in `.env` as a comma-separated list.

**This token is entered into the client app.** Keep it as private as you would a password. Anyone
who has it can connect to your relay.

### 3. Shared secret file — lets the relay talk to servers

Inside each devcontainer, a forked T3Code server runs on port 3773. It will only accept requests
that carry a matching secret in the `X-Relay-Secret` header. This proves the request arrived
from the relay (which is on the same internal Docker network), not from anywhere else.

The secret is stored as a file on your host machine at `~/.config/t3relay/secret`. It is
bind-mounted (read-only) into the Caddy container and into every devcontainer. It **never** goes
into `.env`, `devcontainer.json`, or any committed file — only the path to the file is in `.env`
(`RELAY_SECRET_FILE=~/.config/t3relay/secret`).

---

## Creating the shared secret file

Run these three commands once on your host machine. You only need to do this once; all future
devcontainers reuse the same file.

```bash
mkdir -p ~/.config/t3relay
openssl rand -hex 32 > ~/.config/t3relay/secret
chmod 600 ~/.config/t3relay/secret
```

What each command does:

- `mkdir -p ~/.config/t3relay` — creates the directory `~/.config/t3relay` (and any parents
  that do not exist). The `-p` flag means "no error if it already exists."
- `openssl rand -hex 32 > ~/.config/t3relay/secret` — generates 32 random bytes and writes them
  as 64 lowercase hex characters into the file. This is a cryptographically strong random value
  that would take an attacker longer than the age of the universe to guess.
- `chmod 600 ~/.config/t3relay/secret` — sets the file permissions to owner-read + owner-write
  only. No other user on the machine can read it.

Verify the file was created correctly:

```bash
ls -la ~/.config/t3relay/secret
```

Expected output:

```
-rw------- 1 yourname yourgroup 65 Jan  1 00:00 /home/yourname/.config/t3relay/secret
```

The permissions should be `-rw-------` (600). The file size should be 65 bytes (64 hex characters
plus a newline).

**Never paste the contents of this file anywhere.** The relay and devcontainers read it directly
from disk via a bind mount.

---

## Generating relay bearer tokens

Generate one token per person or device:

```bash
openssl rand -hex 32
```

This prints a 64-character hex string to your terminal. Copy it. Run the command again for each
additional person or device.

Example output:

```
a3f8b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1
```

These tokens are only ever entered into the client app (desktop or browser) and stored in `.env`.
They never go into `devcontainer.json` or any file that gets committed to source control.

---

## Filling in `.env`

Copy the example file if you have not done so:

```bash
cp .env.example .env
```

Open `.env` in a text editor and fill in each value:

```dotenv
# Cloudflare API token — from cloudflare.md Step 1
CF_API_TOKEN=paste-your-cloudflare-token-here

# Your domain (no leading dot, no protocol)
T3_DOMAIN=example.com

# One bearer token per person/device, comma-separated, no spaces
RELAY_TOKENS=token-for-alice,token-for-bob,token-for-phone

# Path to the shared secret file you created above
RELAY_SECRET_FILE=~/.config/t3relay/secret

# Tailscale auth key — from tailscale.md Step 1
TS_AUTHKEY=tskey-auth-paste-here
```

**Never commit `.env`.** It is listed in `.gitignore`. To be safe, confirm it is not tracked:

```bash
git status
```

`.env` should not appear in the output. If it does, run `git rm --cached .env` and commit the
removal.

---

## Revoking a token

If someone should no longer have access (they left the team, a device was lost, etc.):

1. Open `.env` and remove that person's token from `RELAY_TOKENS`. The remaining tokens stay
   on the same line, still comma-separated:

   ```dotenv
   # Before (alice, bob, phone)
   RELAY_TOKENS=aaaa...,bbbb...,cccc...

   # After removing bob
   RELAY_TOKENS=aaaa...,cccc...
   ```

2. Apply the change:

   ```bash
   docker compose up -d
   ```

   `docker compose up -d` recreates only containers whose configuration has changed. The `caddy`
   container restarts with the new environment and the removed token is immediately invalid. No
   restart of devcontainers is needed.

3. Tell the affected person their token no longer works and that they need a new one if they
   should retain access. Generate a new token with `openssl rand -hex 32`, add it to
   `RELAY_TOKENS`, and run `docker compose up -d` again.

---

## Troubleshooting

**Relay returns 401 Unauthorized when using the client**

- Confirm the token entered in the client exactly matches one of the values in `RELAY_TOKENS` in
  `.env`. Tokens are case-sensitive.
- Check for accidental whitespace: `grep RELAY_TOKENS .env` — there should be no spaces around
  the `=` and no spaces between tokens in the comma-separated list.
- After editing `.env`, you must restart the stack: `docker compose up -d`.

**"Permission denied" when the relay tries to read the secret file**

- Check the permissions: `ls -la ~/.config/t3relay/secret`. The file must exist and be readable
  by the user who started Docker. If you created the file as root, change ownership:
  `sudo chown $USER ~/.config/t3relay/secret`.
- Verify the path in `RELAY_SECRET_FILE` in `.env` matches the actual file location. The tilde
  (`~`) expands to your home directory — if you run Docker commands as root, `~` expands to
  `/root`, not your user home. Use an absolute path to be safe:
  `RELAY_SECRET_FILE=/home/yourname/.config/t3relay/secret`.

**Devcontainer logs show "invalid relay secret" or requests fail with 403**

- The secret file inside the devcontainer (`/run/t3code/relay-secret`) must match the one Caddy
  uses. Both are bind-mounted from the same host path (`RELAY_SECRET_FILE`). If you regenerated
  the secret file after devcontainers were already running, reopen (rebuild) the devcontainers so
  they get the new value.

**Lost the Cloudflare API token**

- Cloudflare does not show the token value again after creation. Go to
  **My Profile** → **API Tokens**, delete the old token, and create a new one following
  [cloudflare.md](cloudflare.md) Step 1. Update `CF_API_TOKEN` in `.env` and restart the stack.
  Caddy caches the certificate; you will not need to re-issue it unless the cert expires.
