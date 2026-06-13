---
relationships:
  references:
    - docs/releases.md
    - docs/fork-patches.md
    - vendor/t3code.yml
---

# AGENTS.md

Repo-local operating notes for `t3code-devcontainer-relay`.

This file complements the user/workspace-scoped agent instructions. When work
touches the `vendor-t3code` fork, follow the fork workflow documented here and
in [docs/releases.md](docs/releases.md).

## Fork workflow

There are two distinct workflows for the T3 Code fork. Do not conflate them.

### 1. Fork maintenance

Use this when upstream `t3dotgg/t3-code` shipped a new stable release and the
fork needs to be rebased or resynced.

High-level path:

1. Detect the upstream stable release.
2. Create the next fork milestone.
3. Sync `bearer-auth` with upstream `main`.
4. Rebase or repair the patch stack.
5. Open a fork PR targeting `bearer-auth`.
6. Merge the fork PR.
7. Tag the fork release.
8. Let this relay repo consume that tagged fork revision via a submodule bump.

### 2. Fork revision

Use this when upstream did not change, but the fork itself needs another
release revision, for example `t3code-0.0.28-boblangley.2` after
`t3code-0.0.28-boblangley.1`.

High-level path:

1. Read the current active fork tag from [vendor/t3code.yml](vendor/t3code.yml).
2. Increment only the fork revision suffix.
3. In the `vendor-t3code` submodule, create a dedicated branch or worktree from
   the commit referenced by that current fork tag.
4. Apply and validate the fork changes in that submodule branch/worktree.
5. Push the branch to the fork repository.
6. Create the new fork milestone matching the new revision.
7. Open a fork PR targeting `bearer-auth`.
8. Merge the fork PR.
9. Tag the new fork release.
10. Clean up the local submodule branch/worktree after the fork PR and tag are
    complete.
11. Let this relay repo consume that tagged fork revision via a submodule bump.

## Workspace use of `vendor-t3code`

Editing inside `vendor-t3code` is the normal way to author fork changes from
this workspace, but keep the protocol clear:

- changes inside `vendor-t3code` are fork changes
- submodule pointer changes in this repo are consumption changes
- do not treat a dirty submodule worktree as a completed relay-repo change

If durable process changes are made, update [docs/releases.md](docs/releases.md)
first and keep this file as a short reminder, not the only source of truth.
