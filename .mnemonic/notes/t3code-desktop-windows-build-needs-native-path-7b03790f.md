---
title: T3Code desktop Windows build needs native PATH
tags:
  - github-actions
  - desktop-build
  - t3code
lifecycle: permanent
createdAt: '2026-06-12T03:07:44.219Z'
updatedAt: '2026-06-12T03:07:44.219Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
T3Code desktop Windows build needs native PATH for vendor binaries.

On 2026-06-12, the desktop artifact workflow failed on Windows because the build script launches child commands through `cmd` with `shell: true` so `.cmd` shims resolve. A Git Bash step could run `vp --version`, but the nested `cmd` process could not find `vp` when the workflow only exposed a POSIX-flavored `vendor-t3code/node_modules/.bin` path. The fix was to use PowerShell on Windows, append the native `vendor-t3code\node_modules\.bin` path to `GITHUB_PATH`, and run the Windows artifact command from PowerShell.

The successful fixing commit was `98c951e` (`ci(desktop): use native Windows path for vendor binaries`). Run `27391625401` then built linux x64, macOS arm64, macOS x64, and Windows x64 desktop artifacts and published `t3code-desktop-0.0.27-boblangley.1` plus `t3code-desktop-latest`.
