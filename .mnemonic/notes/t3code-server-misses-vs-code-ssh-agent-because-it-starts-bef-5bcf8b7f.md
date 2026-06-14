---
title: T3Code server misses VS Code SSH agent because it starts before env injection
tags:
  - t3code
  - ssh-agent
  - devcontainer
  - desktop
  - server
lifecycle: permanent
createdAt: '2026-06-13T04:13:09.622Z'
updatedAt: '2026-06-13T04:13:09.622Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
T3Code terminals in relay-managed devcontainers can miss VS Code SSH agent forwarding even when VS Code terminals inside the same container work.

Root cause found on 2026-06-13:

- the devcontainer feature starts the T3Code server from the container entrypoint at container boot time via `/usr/local/share/t3code-supervise.sh`
- that server process becomes the base environment for terminal PTY spawns
- VS Code injects its forwarded agent socket later into attached editor processes as `SSH_AUTH_SOCK=/tmp/vscode-ssh-auth-<id>.sock`
- the container does contain that socket, and `ssh-add -L` works when `SSH_AUTH_SOCK` is manually set to it
- the running T3Code server process did not inherit `SSH_AUTH_SOCK`, so server-spawned terminals also miss it

This means the relay feature is not failing to mount or forward the agent socket. The mismatch is that T3Code server-side terminal startup relies on inherited `process.env`, while the desktop app already has login-shell environment hydration logic for `SSH_AUTH_SOCK` and `PATH`.

Most likely fix location: T3Code server-side terminal environment preparation, or another server-side startup path that hydrates `SSH_AUTH_SOCK` from the container user/login shell before terminal PTY spawn. Relying on the desktop client cannot solve this because the valid socket path is container-local, not desktop-local.
