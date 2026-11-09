import { request } from "./api-client";
import type { Settings, RunSummary, InvestigationReport, ZendeskNote } from "./types";
import exampleJSON from "../../../docs/verification/agent-demo.json";

export const exampleReport: InvestigationReport = exampleJSON;
export const getSettings = () => request<Settings>("/api/settings");
export const listRuns = () => request<{ runs: RunSummary[] }>("/api/runs");
export const getRun = (id: string) => request<InvestigationReport>(`/api/runs/${encodeURIComponent(id)}`);
export const cancelRun = (id: string) => request<{ id?: string }>(`/api/runs/${encodeURIComponent(id)}/cancel`, { method: "POST" });

export const postZendeskNote = (id: string, plan: string) =>
  request<ZendeskNote>(`/api/runs/${encodeURIComponent(id)}/zendesk-note`, { method: "POST", body: JSON.stringify({ plan }) });

export function createRun(input: { description: string; files: File[] }) {
  const body = new FormData();
  body.append("description", input.description);
  for (const file of input.files) body.append("files", file, file.webkitRelativePath || file.name);
  return request<{ id: string }>("/api/runs", { method: "POST", body });
}

export function reportURL(id: string, format: "json" | "md") {
  return `/api/runs/${encodeURIComponent(id)}/report.${format}`;
}

export function caseTitle(description: string) {
  return description.split("\n").find(line => line.trim())?.replace(/^#+\s*/, "").slice(0, 160) || "Untitled investigation";
}

export function formatDate(value?: string) {
  if (!value) return "Not recorded";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "Not recorded" : new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}

export function elapsed(start: string, end?: string) {
  if (!end) return "In progress";
  const seconds = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000));
  return Number.isFinite(seconds) ? (seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`) : "Not recorded";
}

export function fileSize(size: number) {
  return size < 1024 ? `${size} B` : size < 1024 * 1024 ? `${(size / 1024).toFixed(1)} KB` : `${(size / 1024 / 1024).toFixed(1)} MB`;
}
