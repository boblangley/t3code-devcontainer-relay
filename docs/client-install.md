# Client Install

This page explains how to download and configure the forked T3Code desktop app, how to get past
OS security warnings for unsigned builds, and how to use the zero-install web client instead.

---

## Background

T3Code is an AI coding assistant. The version you are using is a fork maintained at
[github.com/boblangley/t3code](https://github.com/boblangley/t3code) — it replaces the upstream
cloud authentication with a simple bearer token, so it can connect to your self-hosted relay
instead of a managed service.

There are two ways to use the client:

- **Desktop app** — a native application for macOS, Linux, and Windows. Download it once per
  device, enter your relay URL and token, done.
- **Web app** — a browser-based version served by the relay itself at
  `https://web.t3.example.com`. No download or install required; open it in any browser on any
  device that can reach your relay.

---

## Option A — Desktop app

### Step 1 — Find the right build

Go to the releases page:

**[https://github.com/boblangley/t3code-devcontainer-relay/releases](https://github.com/boblangley/t3code-devcontainer-relay/releases)**

Open the latest `t3code-desktop-*` release, or the `t3code-desktop-latest` alias, then find the
**Assets** section. Download the file for your OS and processor:

| OS | Processor | File to download |
|---|---|---|
| macOS | Apple Silicon (M1/M2/M3/M4) | `T3-Code-<version>-arm64.dmg` or `.zip` |
| macOS | Intel | `T3-Code-<version>-x64.dmg` or `.zip` |
| Linux | 64-bit (most computers) | `T3-Code-<version>-x64.AppImage` or `T3-Code-<version>-x86_64.AppImage` |
| Windows | 64-bit | `T3-Code-<version>-x64.exe` |
| Windows | ARM 64-bit | `T3-Code-<version>-arm64.exe` |

If you are unsure which macOS processor you have: click the Apple menu (top-left) → **About This
Mac**. Under "Chip" or "Processor" you will see either "Apple M..." (Apple Silicon = arm64) or
"Intel" (Intel = x64).

If you are unsure which Windows CPU architecture you have: press Windows key + Pause/Break, or
open **Settings** → **System** → **About** → **Device specifications** → **System type**. It
will say "64-bit operating system, x64-based processor."

### Step 2 — First-run on macOS (Gatekeeper warning)

The builds are unsigned — they are not submitted to Apple for notarization. macOS Gatekeeper will
block the app the first time you try to open it.

**Method 1 (command line — recommended):**

After copying the app to `/Applications`, run:

```bash
xattr -dr com.apple.quarantine /Applications/T3Code.app
```

This removes the quarantine flag that triggers Gatekeeper. Then open the app normally.

**Method 2 (right-click):**

Right-click the app icon (or two-finger tap on a trackpad) → **Open**. A dialog will appear with
a button to open it anyway. Click **Open**. macOS remembers this choice for future launches.

You only need to do this once per installation.

### Step 3 — First-run on Windows (SmartScreen warning)

Windows SmartScreen may show a blue dialog: "Windows protected your PC." The builds are unsigned
and do not yet have a reputation in Microsoft's database.

1. Click **More info** (small link under the warning text).
2. A **Run anyway** button appears. Click it.

The installer or app will open. You only need to do this once.

### Step 4 — First-run on Linux (AppImage)

Make the AppImage executable, then run it:

```bash
chmod +x T3-Code-<version>-x64.AppImage
./T3-Code-<version>-x64.AppImage
```

Some file managers let you right-click → **Properties** → **Permissions** → tick "Allow
executing file as program" instead.

### Step 5 — Configure the relay connection

When the app starts for the first time, it will ask for:

- **Relay URL**: enter `https://relay.t3.example.com`
  (substitute your domain; include `https://`, no trailing slash).
- **Bearer token**: enter the token you generated for this device.
  See [secrets-and-tokens.md](secrets-and-tokens.md) for how to generate tokens and why each
  device gets its own.

The token is stored locally in the app's settings, not in any committed file.

**Verify:** after entering the relay URL and token, the app should show your list of running
devcontainers. If the list is empty, your devcontainers may not be running yet — see
[add-a-devcontainer.md](add-a-devcontainer.md).

If the app shows an error, check:

- The relay is running: `docker compose ps` — `caddy` and `web` should be `Up`.
- The relay URL is reachable from this machine: `curl -s https://relay.t3.example.com/health`
  should return `{"ok":true,"service":"relay"}`.
- Local DNS is configured: see [local-dns.md](local-dns.md). If you are connecting over
  Tailscale from a different device, see [tailscale.md](tailscale.md).

---

## Option B — Web app (zero-install)

The relay runs a browser-based version of T3Code at:

```
https://web.t3.example.com
```

Open that URL in any browser on any device that can reach the relay (locally via dnsmasq, or
over Tailscale). When prompted, enter your relay URL and bearer token — same values as the
desktop app.

The web app is identical to the desktop app in functionality for coding sessions. Use it when:

- You are on a device where you cannot or do not want to install software.
- You are connecting from a phone or tablet.
- You want to test that the relay is working before setting up the desktop app.

---

## Troubleshooting

**macOS: "T3Code.app is damaged and can't be opened"**

This is still a Gatekeeper issue, just worded differently. Run the `xattr` command from Step 2
Method 1 above.

**macOS: app opens but immediately crashes**

Check that you downloaded the correct architecture build. Apple Silicon Macs can run Intel (`x64`)
builds via Rosetta, but arm64 builds are faster. Intel Macs cannot run arm64 builds at all.

**Windows SmartScreen: "More info" link does not appear**

Your organisation may have SmartScreen configured to block all unsigned executables. Contact your
IT department, or use the web client at `https://web.t3.example.com` instead.

**Linux: "fuse: device not found" when running the AppImage**

AppImages require FUSE to run. Install it:

- Ubuntu/Debian: `sudo apt install -y fuse libfuse2`
- Fedora: `sudo dnf install -y fuse fuse-libs`
- Arch: `sudo pacman -S fuse2`

**App shows "Network error" or "Failed to connect"**

- Check that `https://relay.t3.example.com/health` responds correctly in your browser. If it
  does not, fix the relay first (see [setup-guide.md](setup-guide.md)).
- Confirm the relay URL in the app settings does not have a trailing slash:
  `https://relay.t3.example.com` not `https://relay.t3.example.com/`.

**App shows 401 Unauthorized**

Your bearer token does not match any value in `RELAY_TOKENS` in `.env`. Double-check the token
you entered in the app. Tokens are long hex strings (64 characters) — it is easy to accidentally
copy an extra space or miss a character. Re-paste from the original `openssl rand -hex 32` output.

**Web app at `web.t3.example.com` shows a blank page or cannot load**

- Confirm the `web` service is running: `docker compose ps`.
- Check its logs: `docker compose logs web`.
- If you are accessing from a tailnet device, confirm split DNS is configured —
  see [tailscale.md](tailscale.md).
