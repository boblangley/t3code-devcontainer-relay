# Local DNS Setup

This page explains why you need a local DNS server, how to install and configure dnsmasq on
macOS and Linux, and how to verify that `*.t3.example.com` resolves to your local machine.

---

## Why you need local DNS

**DNS resolution** is the process of turning a hostname like `relay.t3.example.com` into an IP
address your computer can connect to. Normally your computer asks your router (or your ISP's
servers) to do this.

The relay runs locally on your machine. There are no public DNS records pointing at it — Cloudflare
is used only for the TLS certificate challenge, not to publish addresses (see
[cloudflare.md](cloudflare.md)). So when your browser or the desktop client asks "what IP is
`relay.t3.example.com`?" the public internet has no answer.

The solution is **dnsmasq**, a small DNS server that runs on your machine. You configure it with
one rule: for any name under `t3.example.com`, return `127.0.0.1` (the loopback address — your
own machine). Docker is listening on port 443 at `127.0.0.1`, so the connection arrives at Caddy.

> **Note:** this page covers DNS for the machine running Docker. For devices on your tailnet
> (phone, second laptop), split DNS is configured in the Tailscale admin console instead —
> see [tailscale.md](tailscale.md).

---

## macOS

### Install dnsmasq via Homebrew

[Homebrew](https://brew.sh) is a package manager for macOS. If you do not have it, install it
first by following the instructions at brew.sh.

Install dnsmasq:

```bash
brew install dnsmasq
```

This downloads and installs dnsmasq. Successful output ends with something like
`Summary: 🍺 /opt/homebrew/Cellar/dnsmasq/2.x/`.

### Configure the wildcard rule

Add the wildcard DNS rule to dnsmasq's config file:

```bash
echo "address=/t3.example.com/127.0.0.1" >> $(brew --prefix)/etc/dnsmasq.conf
```

This appends one line to the config file telling dnsmasq: "resolve any name ending in
`t3.example.com` to `127.0.0.1`." The `$(brew --prefix)` part expands to the Homebrew prefix
directory (usually `/opt/homebrew` on Apple Silicon or `/usr/local` on Intel).

> **Substitute your domain:** replace `t3.example.com` with `t3.yourdomain.com`.

### Start dnsmasq

```bash
sudo brew services start dnsmasq
```

This starts dnsmasq as a background service and registers it to run on login. You should see
`Successfully started dnsmasq`.

### Tell macOS to use dnsmasq for this domain

macOS has a feature where you can put a file in `/etc/resolver/` naming a domain, and macOS will
send DNS queries for that domain to a different server. Create the file:

```bash
sudo mkdir -p /etc/resolver
echo "nameserver 127.0.0.1" | sudo tee /etc/resolver/t3.example.com
```

The first command creates the `/etc/resolver/` directory if it does not exist. The second writes
a file named `t3.example.com` that tells macOS to query `127.0.0.1` (your local dnsmasq) for
anything under that domain.

### Verify

```bash
dig relay.t3.example.com
```

In the output, look for the `ANSWER SECTION`. You should see:

```
;; ANSWER SECTION:
relay.t3.example.com.  0  IN  A  127.0.0.1
```

Also verify with ping:

```bash
ping -c 1 anything.t3.example.com
```

Expected: `PING anything.t3.example.com (127.0.0.1): ...` — the IP in parentheses should be
`127.0.0.1`.

---

## Linux

Linux distributions vary in how they handle DNS. The most common complication is
**systemd-resolved**, which manages DNS on modern Ubuntu, Debian, Fedora, and Arch systems.
systemd-resolved listens on `127.0.0.53:53` and, on many systems, its stub listener also
occupies `127.0.0.1:53`. If dnsmasq tries to bind to port 53 and finds it taken, it will fail
to start. This section handles that.

### Step 1 — Install dnsmasq

**Ubuntu/Debian:**

```bash
sudo apt update && sudo apt install -y dnsmasq
```

**Fedora/RHEL:**

```bash
sudo dnf install -y dnsmasq
```

**Arch Linux:**

```bash
sudo pacman -S dnsmasq
```

### Step 2 — Disable systemd-resolved's stub listener

Edit `/etc/systemd/resolved.conf`:

```bash
sudo nano /etc/systemd/resolved.conf
```

Find the line `#DNSStubListener=yes` (it may be commented out). Change it to:

```
DNSStubListener=no
```

Save and exit (Ctrl-O, Enter, Ctrl-X in nano).

Restart systemd-resolved:

```bash
sudo systemctl restart systemd-resolved
```

This stops the stub listener on `127.0.0.1:53` so dnsmasq can bind there. systemd-resolved
continues to work but moves its listener to `127.0.0.53:53`.

> **Why this matters:** if you skip this step and dnsmasq fails to start, `journalctl -u dnsmasq`
> will show `failed to create listening socket for port 53: Address already in use`.

### Step 3 — Configure the wildcard rule

Create a dnsmasq config file for the relay:

```bash
echo "address=/t3.example.com/127.0.0.1" | sudo tee /etc/dnsmasq.d/t3relay.conf
```

> **Substitute your domain:** replace `t3.example.com` with `t3.yourdomain.com`.

### Step 4 — Start dnsmasq

```bash
sudo systemctl enable --now dnsmasq
```

`enable` registers it to start on boot; `--now` starts it immediately. Check that it started:

```bash
sudo systemctl status dnsmasq
```

You should see `active (running)`.

### Step 5 — Point the system at dnsmasq

You need to tell the system to use `127.0.0.1` for DNS. If you are using systemd-resolved (which
is common), edit `/etc/systemd/resolved.conf` again and add a DNS line so resolved forwards
queries to dnsmasq:

```bash
sudo nano /etc/systemd/resolved.conf
```

In the `[Resolve]` section, set:

```
DNS=127.0.0.1
```

Restart systemd-resolved once more:

```bash
sudo systemctl restart systemd-resolved
```

Alternatively, if your system uses `/etc/resolv.conf` directly (no systemd-resolved, or you have
unlinked it), add `nameserver 127.0.0.1` as the **first** line in `/etc/resolv.conf`. Note that
some tools overwrite `resolv.conf` on reconnect; if that happens, configure your network manager
(NetworkManager, netplan, etc.) to set `127.0.0.1` as the primary DNS.

### Verify

```bash
dig relay.t3.example.com
```

Look for `127.0.0.1` in the `ANSWER SECTION`:

```
;; ANSWER SECTION:
relay.t3.example.com.  0  IN  A  127.0.0.1
```

Also:

```bash
ping -c 1 web.t3.example.com
```

Expected: the hostname resolves to `127.0.0.1`.

---

## Using a LAN IP instead of `127.0.0.1`

If you run the relay on a dedicated server on your local network (not the machine you browse
from), replace `127.0.0.1` in the dnsmasq rule with the server's LAN IP address (e.g.
`192.168.1.10`):

```
address=/t3.example.com/192.168.1.10
```

Other machines on the same LAN can then use that dnsmasq instance as their DNS server and reach
the relay at the LAN IP. This is an advanced configuration; for single-machine setups the
loopback `127.0.0.1` is correct.

---

## Troubleshooting

**`dig` returns `SERVFAIL` or no answer**

- Check that dnsmasq is running: `sudo systemctl status dnsmasq` (Linux) or
  `brew services list | grep dnsmasq` (macOS).
- Check dnsmasq logs for errors: `journalctl -u dnsmasq -n 50` (Linux) or
  `sudo brew services log dnsmasq` (macOS).
- Confirm the config file contains the right rule:
  `cat /etc/dnsmasq.d/t3relay.conf` (Linux) or
  `grep t3 $(brew --prefix)/etc/dnsmasq.conf` (macOS).

**`Address already in use` — dnsmasq fails to bind port 53 (Linux)**

- You have not disabled systemd-resolved's stub listener. Follow Step 2 above.
- Verify no other process is using port 53: `sudo ss -tulpn | grep :53`.

**macOS does not use the resolver file**

- Confirm the file exists: `cat /etc/resolver/t3.example.com`. It should contain
  `nameserver 127.0.0.1`.
- macOS caches resolver configuration. Flush the DNS cache:
  `sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder`.
- The resolver filename must exactly match the domain in your dnsmasq rule. If your domain is
  `t3.mycompany.io`, the file should be `/etc/resolver/t3.mycompany.io`.

**`dig` works but the browser still cannot reach `relay.t3.example.com`**

- The relay may not be running. Check: `docker compose ps` — all three services should show
  `running`.
- The TLS certificate may not have issued yet. See
  [cloudflare.md — Verify the certificate issues](cloudflare.md#step-3--verify-the-certificate-issues).
- Some browsers (Chrome, Firefox) use their own DNS-over-HTTPS resolver and may bypass dnsmasq.
  Disable DNS-over-HTTPS in the browser's privacy/security settings, or test with `curl` first:
  `curl -v https://relay.t3.example.com/health`.

**Ping resolves correctly but HTTPS still fails with `connection refused`**

- Docker is not listening on port 443. Make sure the compose stack is running with
  `docker compose ps` and that the `caddy` service shows `Up` with `0.0.0.0:443->443/tcp`.
