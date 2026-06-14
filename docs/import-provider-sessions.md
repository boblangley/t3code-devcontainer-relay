---
relationships:
  references:
    - ../scripts/import-codex-session.mjs
    - ../scripts/import-provider-session.mjs
    - ../vendor-t3code/apps/server/src/orchestration/Layers/ProjectionPipeline.ts
    - ../vendor-t3code/apps/server/src/provider/Layers/ClaudeAdapter.ts
    - ../vendor-t3code/packages/contracts/src/orchestration.ts
---

# Provider session importer

`scripts/import-provider-session.mjs` imports provider-native JSONL sessions
into a T3Code SQLite state database. `scripts/import-codex-session.mjs` remains
as a backward-compatible entry point.

The importer is intentionally operational and conservative. It creates
canonical T3Code orchestration events, writes the matching projection rows
directly, upserts `provider_session_runtime` with a provider resume cursor, and
advances all known projector rows in `projection_state`. This direct projection
write is an importer shortcut: it mirrors the current `ProjectionPipeline`
behavior without booting the full server Effect layer.

Supported providers:

- `codex`: reads `/home/vscode/.codex/sessions/YYYY/MM/DD/*.jsonl` and writes
  resume cursor `{"threadId":"<codex-thread-id>"}`.
- `claude` / `claudeAgent`: reads
  `/home/vscode/.claude/projects/**/<claude-session-id>.jsonl` and writes
  `provider_name = claudeAgent`. The resume cursor is shaped for T3Code's
  Claude adapter:
  `{"threadId":"<t3-thread-id>","resume":"<claude-session-id>","resumeSessionAt":"<assistant-uuid>","turnCount":N}`.

## Inspect

```bash
scripts/import-provider-session.mjs inspect \
  --provider codex \
  --provider-thread-id 019eab02-6b37-7783-bdbe-8b364333cef1
```

```bash
scripts/import-provider-session.mjs inspect \
  --provider claude \
  --provider-thread-id f823f267-917d-49a8-a579-086b4c9d31c0
```

The inspector locates the JSONL file, parses provider-native records, and
reports detected timestamps, cwd, model, reasoning effort where applicable,
message counts, activity counts, turn count, and record types.

## Import

Stop the T3Code server before importing into a live state database. The script
checks `<T3CODE_HOME>/userdata/server-runtime.json` and refuses to write if the
recorded pid is still alive unless `--unsafe-live` is passed.

```bash
scripts/import-provider-session.mjs import \
  --db /home/vscode/.t3/devcontainers/<id>/userdata/state.sqlite \
  --provider codex \
  --provider-thread-id <codex-thread-id> \
  --project-root /workspaces/example \
  --title "Recovered session"
```

```bash
scripts/import-provider-session.mjs import \
  --db /home/vscode/.t3/devcontainers/<id>/userdata/state.sqlite \
  --provider claude \
  --provider-thread-id <claude-session-id> \
  --project-root /workspaces/example \
  --title "Recovered Claude session"
```

Before writing, the script runs `PRAGMA integrity_check` and creates a
timestamped backup under `/home/vscode/.t3/recovery-backups/<timestamp>/`.
The backup copies `state.sqlite` and any adjacent `state.sqlite-wal` and
`state.sqlite-shm` sidecars. It does not read or modify `environment-id`,
`auth_sessions`, `auth_pairing_links`, or secrets.

Use `--dry-run` to print the planned project/thread/runtime changes without
writing. Duplicate imports are refused when the provider resume cursor already
exists. `--force` only replaces the deterministic imported T3 thread; it still
refuses if the provider thread is already bound to a different T3 thread.
