---
title: Claude T3Code resume cursor uses T3 thread id plus Claude resume session id
tags:
  - t3code
  - claude
  - session-import
  - sqlite
lifecycle: permanent
createdAt: '2026-06-13T16:13:27.252Z'
updatedAt: '2026-06-13T16:13:27.252Z'
role: summary
alwaysLoad: false
project: github-com-boblangley-t3code-devcontainer-relay
projectName: t3code-devcontainer-relay
memoryVersion: 1
---
Claude provider session imports for T3Code must not put the Claude native session UUID in `resume_cursor_json.threadId`.

T3Code's Claude adapter (`vendor-t3code/apps/server/src/provider/Layers/ClaudeAdapter.ts`) reads resume cursors as `{ threadId?, resume?, sessionId?, resumeSessionAt?, turnCount? }`. On `startSession`, it uses `resume` as the Claude SDK resume/session id, while the active T3Code aggregate thread id remains the T3 thread id. A correct imported Claude runtime binding uses `provider_name = claudeAgent`, `provider_instance_id = claudeAgent`, and a cursor like:

```json
{"threadId":"<t3-thread-id>","resume":"<claude-session-uuid>","resumeSessionAt":"<last-assistant-uuid>","turnCount":1}
```

Local Claude native transcripts were observed under `/home/vscode/.claude/projects/**/<session-id>.jsonl`; real records include `sessionId`, `uuid`, `type`, `message.role`, `message.content`, `cwd`, and `timestamp`. Tool calls appear as assistant `tool_use` content blocks and tool results appear as user messages with `tool_result` content plus `toolUseResult`.
