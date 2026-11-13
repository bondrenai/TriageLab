import { request } from "./api-client";
import type { Settings, RunSummary, InvestigationReport, TicketNote } from "./types";
import exampleJSON from "../../../docs/verification/missing-orders-demo.json";

export const exampleReport: InvestigationReport = exampleJSON;
export const getSettings = () => request<Settings>("/api/settings");
export const listRuns = () => request<{ runs: RunSummary[] }>("/api/runs");
export const getRun = (id: string) => request<InvestigationReport>(`/api/runs/${encodeURIComponent(id)}`);
export const cancelRun = (id: string) => request<{ id?: string }>(`/api/runs/${encodeURIComponent(id)}/cancel`, { method: "POST" });
export const cleanupRun = (id: string) => request<{ id?: string }>(`/api/runs/${encodeURIComponent(id)}/cleanup`, { method: "POST" });

export const pauseRun = (id: string) => request<{ id: string }>(`/api/runs/${encodeURIComponent(id)}/pause`, { method: "POST" });

export interface ResumeInput {
  description: string;
  files: File[];
  round: number;
  settings: string;
}

function lifecycleHeaders(round: number, settings: string) {
  return { "X-TriageLab-Resume-Round": String(round), "X-TriageLab-Resume-Settings": settings };
}

export function resumeRun(id: string, input: ResumeInput) {
  const body = new FormData();
  body.append("description", input.description);
  for (const file of input.files) body.append("files", file, file.webkitRelativePath || file.name);
  return request<{ id: string }>(`/api/runs/${encodeURIComponent(id)}/resume`, { method: "POST", body, headers: lifecycleHeaders(input.round, input.settings) });
}

export function steerRun(id: string, input: ResumeInput) {
  const body = new FormData();
  body.append("description", input.description);
  for (const file of input.files) body.append("files", file, file.webkitRelativePath || file.name);
  return request<{ id: string }>(`/api/runs/${encodeURIComponent(id)}/steer`, { method: "POST", body, headers: lifecycleHeaders(input.round, input.settings) });
}

export const closeRun = (id: string, input: { round: number; settings: string }) =>
  request<{ id: string }>(`/api/runs/${encodeURIComponent(id)}/close`, { method: "POST", headers: lifecycleHeaders(input.round, input.settings) });

export const postTicketNote = (id: string, plan: string) =>
  request<TicketNote>(`/api/runs/${encodeURIComponent(id)}/ticket-note`, { method: "POST", body: JSON.stringify({ plan }) });

export function createRun(input: { description: string; files: File[]; agent?: string; sandbox?: string; namespace?: string }) {
  const body = new FormData();
  body.append("description", input.description);
  for (const file of input.files) body.append("files", file, file.webkitRelativePath || file.name);
  const headers: Record<string, string> = {};
  if (input.agent) headers["X-TriageLab-Agent"] = input.agent;
  if (input.sandbox) headers["X-TriageLab-Sandbox"] = input.sandbox;
  if (input.namespace) headers["X-TriageLab-Namespace"] = input.namespace;
  return request<{ id: string }>("/api/runs", { method: "POST", body, headers });
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
