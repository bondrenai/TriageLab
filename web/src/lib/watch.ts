import type { Experiment, ToolCall } from "./types/investigation.ts";

export type ActivityTone = "blue" | "green" | "amber" | "red" | "gray";

export interface WatchActivityItem {
  id: string;
  kind: "command" | "read_file" | "write_file" | "kill_process" | "tool" | "poll_group";
  label: string;
  detail: string;
  meta?: string;
  statusLabel: string;
  tone: ActivityTone;
  processId?: string;
  experimentNumber?: number;
  errorText?: string;
}

export function formatCommand(command: string[]): string {
  return command
    .map(argument => (/\s|["'\\]/.test(argument) || !argument ? JSON.stringify(argument) : argument))
    .join(" ");
}

export function formatSandboxPath(raw: unknown): string {
  if (typeof raw !== "string" || !raw.trim()) return "unknown path";
  const trimmed = raw.trim();
  if (trimmed === "/data/workspace") return ".";
  if (trimmed.startsWith("/data/workspace/")) return trimmed.slice("/data/workspace/".length);
  return trimmed;
}

export function formatDurationSeconds(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return "0s";
  const rounded = Math.round(totalSeconds);
  if (rounded < 60) return `${rounded}s`;
  const minutes = Math.floor(rounded / 60);
  const seconds = rounded % 60;
  if (minutes < 60) return `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

export function liveElapsed(start: string, end?: string, nowMs = Date.now()): string {
  const startMs = Date.parse(start);
  if (!Number.isFinite(startMs)) return "Not recorded";
  if (end) {
    const endMs = Date.parse(end);
    return Number.isFinite(endMs) ? formatDurationSeconds(Math.max(0, (endMs - startMs) / 1000)) : "Not recorded";
  }
  return formatDurationSeconds(Math.max(0, (nowMs - startMs) / 1000));
}

export function taskStateLabel(taskState?: string, status?: string, phase?: string): string {
  if (phase === "creating") return "Provisioning sandbox";
  if (phase === "invoking") return "Sending turn to agent";
  if (phase === "closing" || status === "closing") return "Closing workspace";
  if (status === "pausing" || phase === "pausing") return "Settling turn to pause";
  if (status === "resuming" || phase === "resuming") return "Restoring workspace";
  switch (taskState) {
    case "TASK_STATE_WORKING":
      return "Agent working";
    case "TASK_STATE_SUBMITTED":
      return "Turn queued";
    case "TASK_STATE_COMPLETED":
      return "Turn completed";
    case "TASK_STATE_FAILED":
      return "Turn failed";
    case "TASK_STATE_CANCELED":
      return "Turn canceled";
    case "TASK_STATE_REJECTED":
      return "Turn rejected";
    default:
      if (status === "queued") return "Queued";
      if (status === "running") return "Agent running";
      return "Status unavailable";
  }
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function toolOutput(response?: Record<string, unknown>): Record<string, unknown> | undefined {
  return asRecord(response?.output);
}

function toolError(response?: Record<string, unknown>): string {
  if (!response) return "";
  const direct = asString(response.error);
  if (direct) return direct;
  const output = toolOutput(response);
  return asString(output?.error);
}

function compactToolArgs(args?: Record<string, unknown>): string {
  if (!args) return "";
  const parts: string[] = [];
  for (const [key, value] of Object.entries(args)) {
    if (key === "sandbox_id" || key.endsWith("_base64")) continue;
    let formatted = "";
    if (typeof value === "string") {
      formatted = value.length > 80 ? `${value.slice(0, 77)}…` : value;
    } else if (typeof value === "number" || typeof value === "boolean") {
      formatted = String(value);
    } else if (Array.isArray(value)) {
      const items = value.map(item => (typeof item === "string" ? item : JSON.stringify(item))).join(", ");
      formatted = items.length > 80 ? `[${value.length} items]` : `[${items}]`;
    }
    if (formatted) parts.push(`${key}: ${formatted}`);
    if (parts.length >= 4) break;
  }
  return parts.join(" · ");
}

function experimentStatus(exp: Experiment): { statusLabel: string; tone: ActivityTone } {
  if (typeof exp.exit_code === "number") {
    return {
      statusLabel: `Exit ${exp.exit_code}`,
      tone: exp.exit_code === 0 ? "green" : "amber",
    };
  }
  if (exp.state === "PROCESS_STATE_RUNNING") {
    return { statusLabel: "Running", tone: "blue" };
  }
  if (exp.stopped_for_pause) {
    return { statusLabel: "Stopped", tone: "amber" };
  }
  return {
    statusLabel: exp.state?.replace(/^PROCESS_STATE_/, "").toLowerCase() || "Started",
    tone: "gray",
  };
}

export function buildWatchActivity(toolCalls: ToolCall[] = [], experiments: Experiment[] = []): WatchActivityItem[] {
  const expByCallId = new Map<string, { exp: Experiment; index: number }>();
  const expByPid = new Map<string, { exp: Experiment; index: number }>();
  experiments.forEach((exp, idx) => {
    const number = idx + 1;
    if (exp.call_id) expByCallId.set(exp.call_id, { exp, index: number });
    if (exp.process_id) expByPid.set(exp.process_id, { exp, index: number });
  });

  const items: WatchActivityItem[] = [];
  let pendingPolls: { count: number; pids: Set<string>; id: string } | null = null;

  const flushPolls = () => {
    if (!pendingPolls) return;
    const pidLabel = pendingPolls.pids.size === 1 ? ` · ${[...pendingPolls.pids][0]}` : pendingPolls.pids.size > 1 ? ` · ${pendingPolls.pids.size} processes` : "";
    items.push({
      id: pendingPolls.id,
      kind: "poll_group",
      label: "Process status checks",
      detail: `${pendingPolls.count} poll${pendingPolls.count === 1 ? "" : "s"}${pidLabel}`,
      statusLabel: "Polled",
      tone: "gray",
    });
    pendingPolls = null;
  };

  for (const call of toolCalls) {
    const err = toolError(call.response);
    const out = toolOutput(call.response);
    const args = call.args;

    if ((call.name === "get_sandbox_process" || call.name === "read_sandbox_outputs") && !err && !call.pending) {
      const pid = asString(args?.process_id);
      if (!pendingPolls) {
        pendingPolls = { count: 0, pids: new Set<string>(), id: `poll-${call.id}` };
      }
      pendingPolls.count++;
      if (pid) pendingPolls.pids.add(pid);
      continue;
    }

    flushPolls();

    switch (call.name) {
      case "start_sandbox_process": {
        const pid = asString(out?.process_id);
        const match = expByCallId.get(call.id) ?? (pid ? expByPid.get(pid) : undefined);
        const rawCommand = Array.isArray(args?.command) ? args.command.map(item => String(item)) : [];
        const command = match?.exp.command?.length ? formatCommand(match.exp.command) : rawCommand.length ? formatCommand(rawCommand) : "command unavailable";
        const cwd = match?.exp.cwd || asString(args?.cwd);
        const badge = err
          ? { statusLabel: "Failed", tone: "red" as ActivityTone }
          : call.pending
            ? { statusLabel: "Starting", tone: "blue" as ActivityTone }
            : match
              ? experimentStatus(match.exp)
              : { statusLabel: "Started", tone: "gray" as ActivityTone };
        items.push({
          id: call.id,
          kind: "command",
          label: match ? `Experiment ${String(match.index).padStart(2, "0")}` : "Command",
          detail: command,
          meta: cwd ? `cwd ${formatSandboxPath(cwd)}${match?.exp.process_id ? ` · ${match.exp.process_id}` : ""}` : match?.exp.process_id,
          statusLabel: badge.statusLabel,
          tone: badge.tone,
          processId: match?.exp.process_id || pid || undefined,
          experimentNumber: match?.index,
          errorText: err || undefined,
        });
        break;
      }
      case "read_sandbox_file": {
        // Never decode data_base64 here; only display redacted path/size metadata.
        const path = formatSandboxPath(args?.path);
        const size = asNumber(out?.size_bytes) ?? asNumber(out?.bytes_read);
        items.push({
          id: call.id,
          kind: "read_file",
          label: "Read file",
          detail: path,
          meta: size !== undefined ? `${size} B` : undefined,
          statusLabel: err ? "Failed" : call.pending ? "Pending" : "Read",
          tone: err ? "red" : call.pending ? "blue" : "gray",
          errorText: err || undefined,
        });
        break;
      }
      case "write_sandbox_file": {
        // Never decode data_base64 here; only display redacted path and bytes_written.
        const path = formatSandboxPath(args?.path);
        const written = asNumber(out?.bytes_written);
        items.push({
          id: call.id,
          kind: "write_file",
          label: "Write file",
          detail: path,
          meta: written !== undefined ? `${written} B` : undefined,
          statusLabel: err ? "Failed" : call.pending ? "Pending" : "Written",
          tone: err ? "red" : call.pending ? "blue" : "gray",
          errorText: err || undefined,
        });
        break;
      }
      case "kill_sandbox_process": {
        const pid = asString(args?.process_id);
        const match = pid ? expByPid.get(pid) : undefined;
        items.push({
          id: call.id,
          kind: "kill_process",
          label: "Stop process",
          detail: match ? `Experiment ${String(match.index).padStart(2, "0")} (${pid})` : pid || "unknown process",
          statusLabel: err ? "Failed" : call.pending ? "Stopping" : "Sent signal",
          tone: err ? "red" : "amber",
          processId: pid || undefined,
          experimentNumber: match?.index,
          errorText: err || undefined,
        });
        break;
      }
      default: {
        items.push({
          id: call.id,
          kind: "tool",
          label: call.name,
          detail: compactToolArgs(args) || "Tool call",
          statusLabel: err ? "Failed" : call.pending ? "Pending" : "Done",
          tone: err ? "red" : call.pending ? "blue" : "gray",
          errorText: err || undefined,
        });
        break;
      }
    }
  }

  flushPolls();
  return items;
}

export function resolveExpandedExperimentKeys(
  round: number,
  experiments: Experiment[],
  liveWatch: boolean,
  manualToggles: Record<string, boolean>,
): Set<string> {
  const open = new Set<string>();
  const lastIndex = experiments.length - 1;
  experiments.forEach((experiment, index) => {
    const key = `${round}:${experiment.process_id}`;
    const override = manualToggles[key];
    if (override !== undefined) {
      if (override) open.add(key);
      return;
    }
    if (liveWatch && (experiment.state === "PROCESS_STATE_RUNNING" || index === lastIndex)) {
      open.add(key);
    }
  });
  return open;
}
