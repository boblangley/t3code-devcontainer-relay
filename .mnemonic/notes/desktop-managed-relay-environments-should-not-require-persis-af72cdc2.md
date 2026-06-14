---
title: >-
  Desktop managed relay environments should not require persisted local
  credentials
tags:
  - desktop
  - managed-relay
  - saved-environments
  - t3code
lifecycle: permanent
createdAt: '2026-06-12T23:58:39.511Z'
updatedAt: '2026-06-12T23:58:39.511Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
Desktop relay-managed environments should not depend on local secret persistence to connect.

On 2026-06-12, the desktop app surfaced `Unable to persist managed environment credentials.` while connecting T3 Connect environments that worked in the web app. The local desktop persistence adapter returns `false` when Electron safe storage is unavailable, so the web runtime treated managed-environment connection as a hard failure even though the relay session can mint a fresh environment access token on demand.

The fix is to treat secret persistence as optional for relay-managed environments:

- keep persisting the saved-environment metadata record
- connect immediately with the freshly minted credential held in memory
- when reconnecting and no saved credential exists, mint a new managed credential from the signed-in relay session instead of forcing the user to re-pair

Separately, stale remote entries shown in the `Remote environments` list come from the relay's linked-environment listing, not the local saved-environment registry. Removing those entries requires calling the relay unlink endpoint, not the local saved-environment remove path.
