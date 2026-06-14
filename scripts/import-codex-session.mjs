#!/usr/bin/env node
// ---
// relationships:
//   references:
//     - AGENTS.md
//     - vendor-t3code/apps/server/src/orchestration/Layers/ProjectionPipeline.ts
//     - vendor-t3code/packages/contracts/src/orchestration.ts
// ---

import { DatabaseSync } from "node:sqlite";
import { createHash, randomUUID } from "node:crypto";
import { copyFileSync, existsSync, mkdirSync, readFileSync, statSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";

const DEFAULT_CODEX_SESSIONS_ROOT = "/home/vscode/.codex/sessions";
const DEFAULT_CLAUDE_PROJECTS_ROOT = "/home/vscode/.claude/projects";
const DEFAULT_BACKUP_ROOT = "/home/vscode/.t3/recovery-backups";
const PROJECTOR_NAMES = [
  "projection.checkpoints",
  "projection.pending-approvals",
  "projection.projects",
  "projection.thread-activities",
  "projection.thread-messages",
  "projection.thread-proposed-plans",
  "projection.thread-sessions",
  "projection.thread-turns",
  "projection.threads",
];

function usage(exitCode = 0) {
  const text = `
Usage:
  scripts/import-codex-session.mjs inspect --provider <codex|claude> --provider-thread-id <provider-session-id> [--session-file <path>]
  scripts/import-codex-session.mjs import --db <state.sqlite> --provider <codex|claude> --provider-thread-id <provider-session-id> --project-root <path> --title <title> [options]

Options:
  --session-file <path>       Provider JSONL file. If omitted, searches the provider home.
  --model <model>             Defaults to model inferred from JSONL, then provider default.
  --reasoning-effort <value>  Codex: reasoningEffort option. Claude: effort option.
  --thread-id <uuid>          T3Code thread id. Defaults to deterministic UUID from provider thread id.
  --dry-run                   Parse, inspect DB, and print planned changes without writing.
  --force                     Replace an existing import for this deterministic thread id/provider cursor.
  --unsafe-live               Allow writing while T3Code server runtime appears live.
  --codex-sessions-root <dir> Override Codex sessions root.
  --claude-projects-root <dir> Override Claude projects root.
  --backup-root <dir>         Override backup root. Defaults to /home/vscode/.t3/recovery-backups.
`;
  console.log(text.trim());
  process.exit(exitCode);
}

function fail(message) {
  console.error(`error: ${message}`);
  process.exit(1);
}

function parseArgs(argv) {
  const [command, ...rest] = argv;
  if (!command || command === "--help" || command === "-h") usage(0);
  const args = { command };
  for (let index = 0; index < rest.length; index += 1) {
    const token = rest[index];
    if (!token.startsWith("--")) fail(`unexpected positional argument '${token}'`);
    const key = token.slice(2).replace(/-([a-z])/g, (_, char) => char.toUpperCase());
    if (["dryRun", "force", "unsafeLive"].includes(key)) {
      args[key] = true;
      continue;
    }
    const value = rest[index + 1];
    if (value === undefined || value.startsWith("--")) {
      fail(`missing value for ${token}`);
    }
    args[key] = value;
    index += 1;
  }
  return args;
}

function requireArg(args, key) {
  if (!args[key]) fail(`missing required --${key.replace(/[A-Z]/g, (char) => `-${char.toLowerCase()}`)}`);
  return args[key];
}

function timestampForPath(date = new Date()) {
  return date.toISOString().replace(/[:.]/g, "-");
}

function toIso(value, fallback = new Date().toISOString()) {
  if (typeof value === "string") {
    const date = new Date(value);
    if (!Number.isNaN(date.valueOf())) return date.toISOString();
  }
  if (typeof value === "number") {
    const millis = value > 10_000_000_000 ? value : value * 1000;
    const date = new Date(millis);
    if (!Number.isNaN(date.valueOf())) return date.toISOString();
  }
  return fallback;
}

function stableUuid(input) {
  const hex = createHash("sha256").update(input).digest("hex").slice(0, 32).split("");
  hex[12] = "5";
  hex[16] = ((parseInt(hex[16], 16) & 0x3) | 0x8).toString(16);
  return `${hex.slice(0, 8).join("")}-${hex.slice(8, 12).join("")}-${hex.slice(12, 16).join("")}-${hex.slice(16, 20).join("")}-${hex.slice(20).join("")}`;
}

function stableId(prefix, ...parts) {
  return `${prefix}-${createHash("sha256").update(parts.join("\0")).digest("hex").slice(0, 24)}`;
}

function json(value) {
  return JSON.stringify(value);
}

function textFromContent(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => {
      if (typeof part?.text === "string") return part.text;
      if (typeof part?.input_text === "string") return part.input_text;
      if (typeof part?.output_text === "string") return part.output_text;
      return "";
    })
    .filter(Boolean)
    .join("\n\n");
}

function normalizeProvider(provider) {
  if (provider === undefined || provider === "codex") {
    return {
      input: "codex",
      providerName: "codex",
      providerInstanceId: "codex",
      adapterKey: "codex",
      defaultModel: "gpt-5.5",
    };
  }
  if (provider === "claude" || provider === "claudeAgent") {
    return {
      input: "claude",
      providerName: "claudeAgent",
      providerInstanceId: "claudeAgent",
      adapterKey: "claudeAgent",
      defaultModel: "claude-opus-4-8",
    };
  }
  fail(`unsupported --provider ${provider}`);
}

function findSessionFile(providerThreadId, root, label) {
  const result = spawnSync(
    "find",
    [root, "-type", "f", "-name", `*${providerThreadId}.jsonl`, "-print"],
    { encoding: "utf8" },
  );
  if (result.status !== 0) {
    fail(`failed to search ${label} sessions under ${root}: ${result.stderr.trim()}`);
  }
  const matches = result.stdout
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .sort();
  if (matches.length === 0) fail(`no ${label} session JSONL found for ${providerThreadId}`);
  if (matches.length > 1) {
    fail(`multiple ${label} session JSONL files found for ${providerThreadId}; pass --session-file`);
  }
  return matches[0];
}

function readJsonl(file) {
  return readFileSync(file, "utf8")
    .split("\n")
    .map((line, index) => ({ line: line.trim(), lineNumber: index + 1 }))
    .filter((entry) => entry.line.length > 0)
    .map((entry) => {
      try {
        return { lineNumber: entry.lineNumber, value: JSON.parse(entry.line) };
      } catch (error) {
        throw new Error(`invalid JSON on line ${entry.lineNumber}: ${error.message}`);
      }
    });
}

function parseCodexSession(file, providerThreadId) {
  const entries = readJsonl(file);
  const meta = entries.find((entry) => entry.value.type === "session_meta")?.value.payload ?? {};
  if (meta.id && meta.id !== providerThreadId) {
    fail(`session file id ${meta.id} does not match requested provider thread id ${providerThreadId}`);
  }

  let currentTurnId = null;
  let model = typeof meta.model === "string" ? meta.model : null;
  let reasoningEffort = null;
  const messages = [];
  const activities = [];
  const turnTimes = new Map();
  const counts = new Map();

  for (const { lineNumber, value } of entries) {
    const timestamp = toIso(value.timestamp, toIso(meta.timestamp));
    const typeKey = `${value.type}:${value.payload?.type ?? ""}`;
    counts.set(typeKey, (counts.get(typeKey) ?? 0) + 1);

    if (value.type === "event_msg" && value.payload?.type === "task_started") {
      currentTurnId = value.payload.turn_id ?? currentTurnId;
      if (currentTurnId) {
        turnTimes.set(currentTurnId, {
          requestedAt: toIso(value.payload.started_at, timestamp),
          startedAt: toIso(value.payload.started_at, timestamp),
          completedAt: null,
        });
      }
      continue;
    }

    if (value.type === "turn_context") {
      currentTurnId = value.payload?.turn_id ?? currentTurnId;
      if (typeof value.payload?.model === "string") model = value.payload.model;
      if (typeof value.payload?.effort === "string") reasoningEffort = value.payload.effort;
      if (typeof value.payload?.collaboration_mode?.settings?.reasoning_effort === "string") {
        reasoningEffort = value.payload.collaboration_mode.settings.reasoning_effort;
      }
      continue;
    }

    if (value.type === "event_msg" && value.payload?.type === "task_complete") {
      const turnId = value.payload.turn_id ?? currentTurnId;
      if (turnId) {
        const existing = turnTimes.get(turnId) ?? {
          requestedAt: timestamp,
          startedAt: timestamp,
          completedAt: null,
        };
        turnTimes.set(turnId, { ...existing, completedAt: toIso(value.payload.completed_at, timestamp) });
      }
      continue;
    }

    if (value.type === "event_msg" && value.payload?.type === "user_message") {
      const text = typeof value.payload.message === "string" ? value.payload.message : "";
      if (text.length > 0) {
        messages.push({
          providerItemId: `jsonl:${lineNumber}`,
          role: "user",
          text,
          turnId: currentTurnId,
          createdAt: timestamp,
          updatedAt: timestamp,
        });
      }
      continue;
    }

    if (value.type === "event_msg" && value.payload?.type === "agent_message") {
      const text = typeof value.payload.message === "string" ? value.payload.message : "";
      if (text.length > 0) {
        messages.push({
          providerItemId: `jsonl:${lineNumber}`,
          role: "assistant",
          text,
          turnId: currentTurnId,
          createdAt: timestamp,
          updatedAt: timestamp,
        });
      }
      continue;
    }

    if (value.type === "response_item" && value.payload?.type === "message") {
      const role = value.payload.role;
      if (role === "assistant" && !value.payload.phase) {
        const text = textFromContent(value.payload.content);
        if (text.length > 0) {
          messages.push({
            providerItemId: `jsonl:${lineNumber}`,
            role: "assistant",
            text,
            turnId: currentTurnId,
            createdAt: timestamp,
            updatedAt: timestamp,
          });
        }
      }
      continue;
    }

    if (value.type === "response_item" && value.payload?.type === "function_call") {
      activities.push({
        providerItemId: `jsonl:${lineNumber}`,
        turnId: currentTurnId,
        tone: "tool",
        kind: "provider.tool.call",
        summary: `${value.payload.name ?? "tool"} call`,
        payload: {
          name: value.payload.name ?? null,
          callId: value.payload.call_id ?? null,
          lineNumber,
        },
        createdAt: timestamp,
      });
      continue;
    }

    if (value.type === "response_item" && value.payload?.type === "function_call_output") {
      activities.push({
        providerItemId: `jsonl:${lineNumber}`,
        turnId: currentTurnId,
        tone: "tool",
        kind: "provider.tool.result",
        summary: `tool result ${value.payload.call_id ?? ""}`.trim(),
        payload: {
          callId: value.payload.call_id ?? null,
          lineNumber,
        },
        createdAt: timestamp,
      });
    }
  }

  const createdAt = messages[0]?.createdAt ?? toIso(meta.timestamp);
  const updatedAt = messages.at(-1)?.updatedAt ?? entries.at(-1)?.value?.timestamp ?? createdAt;
  return {
    file,
    provider: "codex",
    meta,
    counts: Object.fromEntries([...counts.entries()].sort(([left], [right]) => left.localeCompare(right))),
    createdAt,
    updatedAt: toIso(updatedAt, createdAt),
    cwd: typeof meta.cwd === "string" ? meta.cwd : null,
    model,
    reasoningEffort,
    lastAssistantProviderItemId: messages.filter((message) => message.role === "assistant").at(-1)
      ?.providerItemId,
    messages,
    activities,
    turnTimes,
  };
}

function claudeContentToText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .map((part) => {
      if (typeof part?.text === "string") return part.text;
      if (typeof part?.content === "string" && part.type !== "tool_result") return part.content;
      return "";
    })
    .filter(Boolean)
    .join("\n\n");
}

function isUsableProviderModel(value) {
  return typeof value === "string" && value.length > 0 && !value.startsWith("<");
}

function summarizeClaudeToolUse(part) {
  const name = typeof part?.name === "string" ? part.name : "tool";
  return `${name} call`;
}

function parseClaudeSession(file, providerThreadId) {
  const entries = readJsonl(file);
  const messages = [];
  const activities = [];
  const turnTimes = new Map();
  const counts = new Map();
  let cwd = null;
  let model = null;
  let currentTurnId = null;
  let lastAssistantProviderItemId = null;

  const setTurn = (sourceId, timestamp) => {
    currentTurnId = stableId("turn", providerThreadId, sourceId);
    if (!turnTimes.has(currentTurnId)) {
      turnTimes.set(currentTurnId, {
        requestedAt: timestamp,
        startedAt: timestamp,
        completedAt: null,
      });
    }
    return currentTurnId;
  };

  for (const { lineNumber, value } of entries) {
    const timestamp = toIso(value.timestamp);
    const typeKey = `${value.type}:${value.message?.role ?? ""}`;
    counts.set(typeKey, (counts.get(typeKey) ?? 0) + 1);
    if (typeof value.cwd === "string") cwd = value.cwd;
    if (isUsableProviderModel(value.message?.model)) model = value.message.model;
    if (value.sessionId && value.sessionId !== providerThreadId) {
      fail(`session file id ${value.sessionId} does not match requested provider thread id ${providerThreadId}`);
    }

    if (value.type === "user" && value.message?.role === "user") {
      const isToolResult =
        Array.isArray(value.message.content) &&
        value.message.content.some((part) => part?.type === "tool_result");
      const turnId = isToolResult
        ? currentTurnId
        : setTurn(value.promptId ?? value.uuid ?? `line:${lineNumber}`, timestamp);
      const text = claudeContentToText(value.message.content);
      if (text.length > 0 && !isToolResult) {
        messages.push({
          providerItemId: value.uuid ?? `jsonl:${lineNumber}`,
          role: "user",
          text,
          turnId,
          createdAt: timestamp,
          updatedAt: timestamp,
        });
      }
      if (isToolResult) {
        const contentText = claudeContentToText(value.message.content);
        activities.push({
          providerItemId: value.uuid ?? `jsonl:${lineNumber}`,
          turnId,
          tone: "tool",
          kind: "provider.tool.result",
          summary: "tool result",
          payload: {
            uuid: value.uuid ?? null,
            lineNumber,
            toolUseResult: value.toolUseResult ?? null,
            text: contentText.slice(0, 4000),
          },
          createdAt: timestamp,
        });
      }
      continue;
    }

    if (value.type === "assistant" && value.message?.role === "assistant") {
      const turnId = currentTurnId ?? setTurn(value.parentUuid ?? value.uuid ?? `line:${lineNumber}`, timestamp);
      if (isUsableProviderModel(value.message.model)) model = value.message.model;
      const textParts = [];
      for (const part of Array.isArray(value.message.content) ? value.message.content : []) {
        if (part?.type === "text" && typeof part.text === "string") {
          textParts.push(part.text);
        } else if (part?.type === "tool_use") {
          activities.push({
            providerItemId: part.id ?? value.uuid ?? `jsonl:${lineNumber}`,
            turnId,
            tone: "tool",
            kind: "provider.tool.call",
            summary: summarizeClaudeToolUse(part),
            payload: {
              uuid: value.uuid ?? null,
              toolUseId: part.id ?? null,
              name: part.name ?? null,
              input: part.input ?? null,
              lineNumber,
            },
            createdAt: timestamp,
          });
        }
      }
      const text = textParts.join("\n\n");
      if (text.length > 0) {
        lastAssistantProviderItemId = value.uuid ?? value.message.id ?? `jsonl:${lineNumber}`;
        messages.push({
          providerItemId: lastAssistantProviderItemId,
          role: "assistant",
          text,
          turnId,
          createdAt: timestamp,
          updatedAt: timestamp,
        });
        const existing = turnTimes.get(turnId);
        if (existing) {
          turnTimes.set(turnId, { ...existing, completedAt: timestamp });
        }
      }
      continue;
    }

    if (value.type === "attachment" && value.attachment) {
      activities.push({
        providerItemId: value.uuid ?? `jsonl:${lineNumber}`,
        turnId: currentTurnId,
        tone: "info",
        kind: `claude.attachment.${value.attachment.type ?? "unknown"}`,
        summary: value.attachment.command ?? value.attachment.hookName ?? "Claude attachment",
        payload: {
          uuid: value.uuid ?? null,
          attachment: value.attachment,
          lineNumber,
        },
        createdAt: timestamp,
      });
    }
  }

  const createdAt = messages[0]?.createdAt ?? entries.find((entry) => entry.value.timestamp)?.value.timestamp;
  const updatedAt = messages.at(-1)?.updatedAt ?? entries.at(-1)?.value?.timestamp ?? createdAt;
  return {
    file,
    provider: "claude",
    meta: { id: providerThreadId },
    counts: Object.fromEntries([...counts.entries()].sort(([left], [right]) => left.localeCompare(right))),
    createdAt: toIso(createdAt),
    updatedAt: toIso(updatedAt, toIso(createdAt)),
    cwd,
    model,
    reasoningEffort: null,
    lastAssistantProviderItemId,
    messages,
    activities,
    turnTimes,
  };
}

function printInspection(parsed) {
  const userMessages = parsed.messages.filter((message) => message.role === "user").length;
  const assistantMessages = parsed.messages.filter((message) => message.role === "assistant").length;
  console.log(JSON.stringify({
    provider: parsed.provider,
    sessionFile: parsed.file,
    providerThreadId: parsed.meta.id ?? null,
    createdAt: parsed.createdAt,
    updatedAt: parsed.updatedAt,
    cwd: parsed.cwd,
    model: parsed.model,
    reasoningEffort: parsed.reasoningEffort,
    userMessages,
    assistantMessages,
    activities: parsed.activities.length,
    turns: parsed.turnTimes.size,
    recordTypes: parsed.counts,
  }, null, 2));
}

function openDb(dbPath) {
  if (!existsSync(dbPath)) fail(`database does not exist: ${dbPath}`);
  return new DatabaseSync(dbPath);
}

function getOne(db, sql, params = {}) {
  return db.prepare(sql).get(params);
}

function getAll(db, sql, params = {}) {
  return db.prepare(sql).all(params);
}

function run(db, sql, params = {}) {
  return db.prepare(sql).run(params);
}

function integrityCheck(db) {
  return getOne(db, "PRAGMA integrity_check").integrity_check;
}

function detectLiveServer(dbPath, unsafeLive) {
  if (unsafeLive) return null;
  const runtimePath = join(dirname(dbPath), "server-runtime.json");
  if (!existsSync(runtimePath)) return null;
  let runtime;
  try {
    runtime = JSON.parse(readFileSync(runtimePath, "utf8"));
  } catch {
    fail(`cannot parse ${runtimePath}; stop T3Code server or pass --unsafe-live`);
  }
  const pid = runtime.pid;
  if (!Number.isInteger(pid) || pid <= 0) return null;
  try {
    statSync(`/proc/${pid}`);
  } catch {
    return null;
  }
  fail(`T3Code server appears live at pid ${pid}. Stop that exact process before import, or pass --unsafe-live.`);
}

function backupDb(dbPath, backupRoot) {
  const stamp = timestampForPath();
  const dir = join(backupRoot, stamp);
  mkdirSync(dir, { recursive: true });
  const backupPath = join(dir, `${basename(dbPath, ".sqlite")}.state.sqlite`);
  copyFileSync(dbPath, backupPath);
  for (const suffix of ["-wal", "-shm"]) {
    const sidecar = `${dbPath}${suffix}`;
    if (existsSync(sidecar)) copyFileSync(sidecar, `${backupPath}${suffix}`);
  }
  return backupPath;
}

function projectTitleFromRoot(projectRoot) {
  return basename(projectRoot) || "workspaces";
}

function buildImportPlan(args, parsed, db) {
  const providerThreadId = requireArg(args, "providerThreadId");
  const providerConfig = normalizeProvider(args.provider);
  const projectRoot = resolve(requireArg(args, "projectRoot"));
  const title = requireArg(args, "title");
  const model = args.model ?? parsed.model ?? providerConfig.defaultModel;
  const reasoningEffort =
    args.reasoningEffort ?? parsed.reasoningEffort ?? (providerConfig.input === "codex" ? "high" : undefined);
  const modelOptions =
    reasoningEffort === undefined
      ? []
      : [
          {
            id: providerConfig.input === "codex" ? "reasoningEffort" : "effort",
            value: reasoningEffort,
          },
        ];
  const modelSelection = {
    instanceId: providerConfig.providerInstanceId,
    model,
    ...(modelOptions.length > 0 ? { options: modelOptions } : {}),
  };
  const threadId =
    args.threadId ?? stableUuid(`t3code-import:${providerConfig.providerName}:${providerThreadId}`);
  const now = new Date().toISOString();
  const createdAt = parsed.createdAt;
  const updatedAt = parsed.updatedAt;

  const existingRuntimeImports = getAll(
    db,
    "SELECT thread_id, resume_cursor_json FROM provider_session_runtime WHERE provider_name = $providerName AND resume_cursor_json LIKE $needle",
    { $providerName: providerConfig.providerName, $needle: `%${providerThreadId}%` },
  );
  const existingAnyProviderRuntimeImports = getAll(
    db,
    "SELECT thread_id, provider_name, resume_cursor_json FROM provider_session_runtime WHERE resume_cursor_json LIKE $needle",
    { $needle: `%${providerThreadId}%` },
  );
  const existingThread = getOne(
    db,
    "SELECT thread_id, title FROM projection_threads WHERE thread_id = $threadId",
    { $threadId: threadId },
  );
  if (!args.force) {
    if (existingRuntimeImports.length > 0) {
      fail(`provider thread id already imported by T3 thread ${existingRuntimeImports.map((row) => row.thread_id).join(", ")}; pass --force to replace this deterministic import`);
    }
    const otherProviderImports = existingAnyProviderRuntimeImports.filter(
      (row) => row.provider_name !== providerConfig.providerName,
    );
    if (otherProviderImports.length > 0) {
      fail(
        `provider thread id appears in runtime binding(s) for another provider: ${otherProviderImports
          .map((row) => `${row.provider_name}/${row.thread_id}`)
          .join(", ")}`,
      );
    }
    if (existingThread) {
      fail(`T3 thread id already exists: ${threadId}; pass --thread-id or --force`);
    }
  } else if (existingRuntimeImports.some((row) => row.thread_id !== threadId)) {
    fail(
      `provider thread id is already imported by different T3 thread(s): ${existingRuntimeImports
        .map((row) => row.thread_id)
        .join(", ")}; refusing to create a duplicate resume binding`,
    );
  }

  let project = getOne(
    db,
    "SELECT project_id, title, workspace_root FROM projection_projects WHERE workspace_root = $projectRoot AND deleted_at IS NULL ORDER BY updated_at DESC LIMIT 1",
    { $projectRoot: projectRoot },
  );
  let createProject = false;
  if (!project) {
    project = {
      project_id: stableUuid(`t3code-import:project:${projectRoot}`),
      title: projectTitleFromRoot(projectRoot),
      workspace_root: projectRoot,
    };
    createProject = true;
  }

  const events = [];
  const addEvent = ({ aggregateKind, streamId, type, occurredAt, payload, metadata = {}, actorKind = "server" }) => {
    events.push({
      eventId: stableId("evt", providerThreadId, type, events.length.toString(), streamId),
      aggregateKind,
      streamId,
      type,
      occurredAt,
      payload,
      metadata,
      actorKind,
    });
  };

  if (createProject) {
    addEvent({
      aggregateKind: "project",
      streamId: project.project_id,
      type: "project.created",
      occurredAt: createdAt,
      payload: {
        projectId: project.project_id,
        title: project.title,
        workspaceRoot: project.workspace_root,
        defaultModelSelection: modelSelection,
        scripts: [],
        createdAt,
        updatedAt: createdAt,
      },
    });
  }

  addEvent({
    aggregateKind: "thread",
    streamId: threadId,
    type: "thread.created",
    occurredAt: createdAt,
    payload: {
      threadId,
      projectId: project.project_id,
      title,
      modelSelection,
      runtimeMode: "full-access",
      interactionMode: "default",
      branch: null,
      worktreePath: projectRoot,
      createdAt,
      updatedAt: createdAt,
    },
  });

  const messages = parsed.messages.map((message, index) => ({
    ...message,
    messageId: stableId("msg", providerThreadId, message.providerItemId, message.role, String(index)),
  }));
  for (const message of messages) {
    addEvent({
      aggregateKind: "thread",
      streamId: threadId,
      type: "thread.message-sent",
      occurredAt: message.updatedAt,
      actorKind: message.role === "assistant" ? "provider" : "client",
      payload: {
        threadId,
        messageId: message.messageId,
        role: message.role,
        text: message.text,
        turnId: message.turnId,
        streaming: false,
        createdAt: message.createdAt,
        updatedAt: message.updatedAt,
      },
      metadata: {
        adapterKey: providerConfig.adapterKey,
        providerItemId: message.providerItemId,
        ...(message.turnId ? { providerTurnId: message.turnId } : {}),
        ingestedAt: now,
      },
    });
  }

  const activities = parsed.activities.map((activity, index) => ({
    ...activity,
    activityId: stableId("act", providerThreadId, activity.providerItemId, activity.kind, String(index)),
    sequence: index,
  }));
  for (const activity of activities) {
    addEvent({
      aggregateKind: "thread",
      streamId: threadId,
      type: "thread.activity-appended",
      occurredAt: activity.createdAt,
      actorKind: "provider",
      payload: {
        threadId,
        activity: {
          id: activity.activityId,
          tone: activity.tone,
          kind: activity.kind,
          summary: activity.summary,
          payload: activity.payload,
          turnId: activity.turnId,
          sequence: activity.sequence,
          createdAt: activity.createdAt,
        },
      },
      metadata: {
        adapterKey: providerConfig.adapterKey,
        providerItemId: activity.providerItemId,
        ...(activity.turnId ? { providerTurnId: activity.turnId } : {}),
        ingestedAt: now,
      },
    });
  }

  const session = {
    threadId,
    providerName: providerConfig.providerName,
    providerInstanceId: providerConfig.providerInstanceId,
    providerSessionId: null,
    providerThreadId: null,
    activeTurnId: null,
    runtimeMode: "full-access",
    status: "stopped",
    lastError: null,
    updatedAt,
  };
  addEvent({
    aggregateKind: "thread",
    streamId: threadId,
    type: "thread.session-set",
    occurredAt: updatedAt,
    payload: { threadId, session },
    metadata: { adapterKey: providerConfig.adapterKey, ingestedAt: now },
  });

  const turns = [...new Set(messages.map((message) => message.turnId).filter(Boolean))].map((turnId) => {
    const byTurn = messages.filter((message) => message.turnId === turnId);
    const first = byTurn[0];
    const last = byTurn.at(-1);
    const times = parsed.turnTimes.get(turnId) ?? {};
    const assistant = [...byTurn].reverse().find((message) => message.role === "assistant");
    const user = byTurn.find((message) => message.role === "user");
    return {
      turnId,
      threadId,
      pendingMessageId: user?.messageId ?? null,
      assistantMessageId: assistant?.messageId ?? null,
      state: "completed",
      requestedAt: times.requestedAt ?? first?.createdAt ?? createdAt,
      startedAt: times.startedAt ?? first?.createdAt ?? createdAt,
      completedAt: times.completedAt ?? last?.updatedAt ?? updatedAt,
      checkpointTurnCount: null,
      checkpointRef: null,
      checkpointStatus: null,
      checkpointFiles: [],
      sourceProposedPlanThreadId: null,
      sourceProposedPlanId: null,
    };
  });

  const resumeCursor =
    providerConfig.input === "claude"
      ? {
          threadId,
          resume: providerThreadId,
          ...(parsed.lastAssistantProviderItemId
            ? { resumeSessionAt: parsed.lastAssistantProviderItemId }
            : {}),
          turnCount: turns.length,
        }
      : { threadId: providerThreadId };

  return {
    provider: providerConfig.input,
    providerConfig,
    providerThreadId,
    threadId,
    project,
    createProject,
    title,
    modelSelection,
    createdAt,
    updatedAt,
    now,
    events,
    messages,
    activities,
    turns,
    session,
    runtime: {
      threadId,
      providerName: providerConfig.providerName,
      providerInstanceId: providerConfig.providerInstanceId,
      adapterKey: providerConfig.adapterKey,
      runtimeMode: "full-access",
      status: "stopped",
      lastSeenAt: updatedAt,
      resumeCursor,
      runtimePayload: {
        cwd: project.workspace_root,
        model,
        activeTurnId: null,
        lastError: null,
        modelSelection,
        lastRuntimeEvent: `import.${providerConfig.input}-session`,
        lastRuntimeEventAt: now,
        importedFrom: parsed.file,
      },
    },
    duplicates: { existingRuntimeImports, existingThread },
  };
}

function maxSequence(db) {
  return getOne(db, "SELECT COALESCE(MAX(sequence), 0) AS max_sequence FROM orchestration_events").max_sequence;
}

function maxProjectionStateSequence(db) {
  return getOne(
    db,
    "SELECT COALESCE(MAX(last_applied_sequence), 0) AS max_sequence FROM projection_state",
  ).max_sequence;
}

function insertEvent(db, event) {
  run(
    db,
    `INSERT INTO orchestration_events (
      event_id, aggregate_kind, stream_id, stream_version, event_type, occurred_at,
      command_id, causation_event_id, correlation_id, actor_kind, payload_json, metadata_json
    )
    VALUES (
      $eventId, $aggregateKind, $streamId,
      COALESCE((SELECT stream_version + 1 FROM orchestration_events WHERE aggregate_kind = $aggregateKind AND stream_id = $streamId ORDER BY stream_version DESC LIMIT 1), 0),
      $eventType, $occurredAt, NULL, NULL, NULL, $actorKind, $payloadJson, $metadataJson
    )`,
    {
      $eventId: event.eventId,
      $aggregateKind: event.aggregateKind,
      $streamId: event.streamId,
      $eventType: event.type,
      $occurredAt: event.occurredAt,
      $actorKind: event.actorKind,
      $payloadJson: json(event.payload),
      $metadataJson: json(event.metadata),
    },
  );
}

function applyPlan(db, plan, force) {
  db.exec("BEGIN IMMEDIATE");
  try {
    if (force) {
      run(db, "DELETE FROM provider_session_runtime WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM projection_thread_sessions WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM projection_thread_messages WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM projection_thread_activities WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM projection_turns WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM projection_threads WHERE thread_id = $threadId", { $threadId: plan.threadId });
      run(db, "DELETE FROM orchestration_events WHERE stream_id = $threadId", { $threadId: plan.threadId });
    }

    for (const event of plan.events) insertEvent(db, event);

    if (plan.createProject) {
      run(
        db,
        `INSERT INTO projection_projects (
          project_id, title, workspace_root, default_model_selection_json,
          scripts_json, created_at, updated_at, deleted_at
        )
        VALUES ($projectId, $title, $workspaceRoot, $modelSelectionJson, '[]', $createdAt, $updatedAt, NULL)
        ON CONFLICT (project_id) DO UPDATE SET
          title = excluded.title,
          workspace_root = excluded.workspace_root,
          default_model_selection_json = excluded.default_model_selection_json,
          scripts_json = excluded.scripts_json,
          updated_at = excluded.updated_at,
          deleted_at = excluded.deleted_at`,
        {
          $projectId: plan.project.project_id,
          $title: plan.project.title,
          $workspaceRoot: plan.project.workspace_root,
          $modelSelectionJson: json(plan.modelSelection),
          $createdAt: plan.createdAt,
          $updatedAt: plan.createdAt,
        },
      );
    }

    const latestUserMessageAt =
      plan.messages.filter((message) => message.role === "user").at(-1)?.createdAt ?? null;
    const latestTurnId = plan.turns.at(-1)?.turnId ?? null;
    run(
      db,
      `INSERT INTO projection_threads (
        thread_id, project_id, title, model_selection_json, runtime_mode, interaction_mode,
        branch, worktree_path, latest_turn_id, created_at, updated_at, archived_at,
        latest_user_message_at, pending_approval_count, pending_user_input_count,
        has_actionable_proposed_plan, deleted_at
      )
      VALUES (
        $threadId, $projectId, $title, $modelSelectionJson, 'full-access', 'default',
        NULL, $worktreePath, $latestTurnId, $createdAt, $updatedAt, NULL,
        $latestUserMessageAt, 0, 0, 0, NULL
      )`,
      {
        $threadId: plan.threadId,
        $projectId: plan.project.project_id,
        $title: plan.title,
        $modelSelectionJson: json(plan.modelSelection),
        $worktreePath: plan.project.workspace_root,
        $latestTurnId: latestTurnId,
        $createdAt: plan.createdAt,
        $updatedAt: plan.updatedAt,
        $latestUserMessageAt: latestUserMessageAt,
      },
    );

    for (const message of plan.messages) {
      run(
        db,
        `INSERT INTO projection_thread_messages (
          message_id, thread_id, turn_id, role, text, attachments_json,
          is_streaming, created_at, updated_at
        )
        VALUES ($messageId, $threadId, $turnId, $role, $text, NULL, 0, $createdAt, $updatedAt)`,
        {
          $messageId: message.messageId,
          $threadId: plan.threadId,
          $turnId: message.turnId,
          $role: message.role,
          $text: message.text,
          $createdAt: message.createdAt,
          $updatedAt: message.updatedAt,
        },
      );
    }

    for (const activity of plan.activities) {
      run(
        db,
        `INSERT INTO projection_thread_activities (
          activity_id, thread_id, turn_id, tone, kind, summary, payload_json, sequence, created_at
        )
        VALUES ($activityId, $threadId, $turnId, $tone, $kind, $summary, $payloadJson, $sequence, $createdAt)`,
        {
          $activityId: activity.activityId,
          $threadId: plan.threadId,
          $turnId: activity.turnId,
          $tone: activity.tone,
          $kind: activity.kind,
          $summary: activity.summary,
          $payloadJson: json(activity.payload),
          $sequence: activity.sequence,
          $createdAt: activity.createdAt,
        },
      );
    }

    for (const turn of plan.turns) {
      run(
        db,
        `INSERT INTO projection_turns (
          thread_id, turn_id, pending_message_id, source_proposed_plan_thread_id,
          source_proposed_plan_id, assistant_message_id, state, requested_at, started_at,
          completed_at, checkpoint_turn_count, checkpoint_ref, checkpoint_status, checkpoint_files_json
        )
        VALUES (
          $threadId, $turnId, $pendingMessageId, NULL, NULL, $assistantMessageId, $state,
          $requestedAt, $startedAt, $completedAt, NULL, NULL, NULL, '[]'
        )`,
        {
          $threadId: turn.threadId,
          $turnId: turn.turnId,
          $pendingMessageId: turn.pendingMessageId,
          $assistantMessageId: turn.assistantMessageId,
          $state: turn.state,
          $requestedAt: turn.requestedAt,
          $startedAt: turn.startedAt,
          $completedAt: turn.completedAt,
        },
      );
    }

    run(
      db,
      `INSERT INTO projection_thread_sessions (
        thread_id, status, provider_name, provider_session_id, provider_thread_id,
        active_turn_id, last_error, updated_at, runtime_mode, provider_instance_id
      )
      VALUES (
        $threadId, 'stopped', $providerName, NULL, NULL, NULL, NULL,
        $updatedAt, 'full-access', $providerInstanceId
      )`,
      {
        $threadId: plan.threadId,
        $providerName: plan.runtime.providerName,
        $providerInstanceId: plan.runtime.providerInstanceId,
        $updatedAt: plan.updatedAt,
      },
    );

    run(
      db,
      `INSERT INTO provider_session_runtime (
        thread_id, provider_name, provider_instance_id, adapter_key, runtime_mode, status,
        last_seen_at, resume_cursor_json, runtime_payload_json
      )
      VALUES (
        $threadId, $providerName, $providerInstanceId, $adapterKey, 'full-access', 'stopped',
        $lastSeenAt, $resumeCursorJson, $runtimePayloadJson
      )`,
      {
        $threadId: plan.threadId,
        $providerName: plan.runtime.providerName,
        $providerInstanceId: plan.runtime.providerInstanceId,
        $adapterKey: plan.runtime.adapterKey,
        $lastSeenAt: plan.runtime.lastSeenAt,
        $resumeCursorJson: json(plan.runtime.resumeCursor),
        $runtimePayloadJson: json(plan.runtime.runtimePayload),
      },
    );

    const appliedSequence = Math.max(maxSequence(db), maxProjectionStateSequence(db));
    for (const projector of PROJECTOR_NAMES) {
      run(
        db,
        `INSERT INTO projection_state (projector, last_applied_sequence, updated_at)
         VALUES ($projector, $sequence, $updatedAt)
         ON CONFLICT (projector) DO UPDATE SET
           last_applied_sequence = excluded.last_applied_sequence,
           updated_at = excluded.updated_at`,
        { $projector: projector, $sequence: appliedSequence, $updatedAt: plan.now },
      );
    }

    db.exec("COMMIT");
    return appliedSequence;
  } catch (error) {
    db.exec("ROLLBACK");
    throw error;
  }
}

function printPlan(plan, dryRun) {
  console.log(JSON.stringify({
    dryRun,
    threadId: plan.threadId,
    projectId: plan.project.project_id,
    projectTitle: plan.project.title,
    projectRoot: plan.project.workspace_root,
    createProject: plan.createProject,
    title: plan.title,
    events: plan.events.length,
    messages: plan.messages.length,
    turns: plan.turns.length,
    activities: plan.activities.length,
    providerRuntime: {
      providerName: plan.runtime.providerName,
      status: plan.runtime.status,
      resumeCursor: plan.runtime.resumeCursor,
    },
  }, null, 2));
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const providerConfig = normalizeProvider(args.provider);
  const providerThreadId = requireArg(args, "providerThreadId");
  const sessionFile = args.sessionFile
    ? resolve(args.sessionFile)
    : providerConfig.input === "claude"
      ? findSessionFile(
          providerThreadId,
          args.claudeProjectsRoot ?? DEFAULT_CLAUDE_PROJECTS_ROOT,
          "Claude",
        )
      : findSessionFile(
          providerThreadId,
          args.codexSessionsRoot ?? DEFAULT_CODEX_SESSIONS_ROOT,
          "Codex",
        );
  const parsed =
    providerConfig.input === "claude"
      ? parseClaudeSession(sessionFile, providerThreadId)
      : parseCodexSession(sessionFile, providerThreadId);

  if (args.command === "inspect") {
    printInspection(parsed);
    return;
  }
  if (args.command !== "import") usage(1);

  const dbPath = resolve(requireArg(args, "db"));
  detectLiveServer(dbPath, args.unsafeLive === true);
  const db = openDb(dbPath);
  try {
    const beforeIntegrity = integrityCheck(db);
    if (beforeIntegrity !== "ok") fail(`pre-import integrity_check failed: ${beforeIntegrity}`);
    const plan = buildImportPlan(args, parsed, db);
    printPlan(plan, args.dryRun === true);
    if (args.dryRun) return;

    const backupPath = backupDb(dbPath, args.backupRoot ?? DEFAULT_BACKUP_ROOT);
    const appliedSequence = applyPlan(db, plan, args.force === true);
    const afterIntegrity = integrityCheck(db);
    if (afterIntegrity !== "ok") fail(`post-import integrity_check failed: ${afterIntegrity}`);
    console.log(JSON.stringify({
      imported: true,
      backupPath,
      threadId: plan.threadId,
      providerThreadId,
      appliedSequence,
      integrityCheck: afterIntegrity,
    }, null, 2));
  } finally {
    db.close();
  }
}

main();
