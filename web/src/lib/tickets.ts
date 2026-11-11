import { request } from "./api-client";
import type { ProcedureVersion, TicketAction, TicketCase, TicketProcedure, TicketSource, TicketWorkspace } from "./types/ticket";

export function orderedProcedures(version: ProcedureVersion): TicketProcedure[] {
  return [...(version.additional_procedures ?? []), version.procedure];
}

// A Bondren issue can be referenced by its ID, its issue URL, or its number;
// a pasted URL reduces to the issue ID segment. A Jira issue can be
// referenced by its key, its URL, or its numeric id; a pasted URL reduces to
// the key segment. Other sources use numbers.
export function ticketReference(source: TicketSource | undefined, value: string): string {
  const trimmed = value.trim();
  if (source === "bondren" && trimmed.includes("/work/support/")) {
    return (trimmed.split("/work/support/")[1] ?? "").slice(0, 36);
  }
  if (source === "jira" && trimmed.includes("/browse/")) {
    return (trimmed.split("/browse/")[1] ?? "").split(/[?#]/)[0].slice(0, 32);
  }
  return trimmed;
}

export const readTicketWorkspace = () => request<TicketWorkspace>("/api/tickets");

const connectionPath = (connection: string, suffix = "") =>
  `/api/tickets/c/${encodeURIComponent(connection)}${suffix}`;

export const takeTicketAction = ({ connection, id, action, revision, plan, agent }: TicketAction) =>
  request<TicketCase>(`${connectionPath(connection)}/${encodeURIComponent(id)}/${action}`, {
    method: "POST",
    body: JSON.stringify(action === "route" ? { revision, agent } : { revision, plan }),
  });

export const beginZendeskOAuth = (connection: string) =>
  request<{ url: string }>(connectionPath(connection, "/oauth/start"), { method: "POST" });

export const saveProcedure = (connection: string, version: ProcedureVersion) =>
  request<ProcedureVersion>(connectionPath(connection, "/procedure"), { method: "POST", body: JSON.stringify(version) });

export const previewProcedure = (connection: string, id: string, procedure: TicketProcedure) =>
  request<TicketCase>(`${connectionPath(connection)}/${encodeURIComponent(id)}/procedure-preview`, { method: "POST", body: JSON.stringify(procedure) });

export interface ConditionDiagnostic {
  message: string;
  line?: number;
  column?: number;
}

export const validateTicketCondition = (connection: string, expression: string, signal: AbortSignal) =>
  request<ConditionDiagnostic[]>(connectionPath(connection, "/procedure/validate-condition"), {
    method: "POST", body: JSON.stringify({ expression }), signal,
  });
