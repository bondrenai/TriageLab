import { request } from "./api-client";
import type { ProcedureVersion, TicketAction, TicketCase, TicketProcedure, TicketWorkspace } from "./types/ticket";

export function orderedProcedures(version: ProcedureVersion): TicketProcedure[] {
  return [...(version.additional_procedures ?? []), version.procedure];
}

export const readTicketWorkspace = () => request<TicketWorkspace>("/api/tickets");

export const takeTicketAction = ({ id, action, revision, plan, agent }: TicketAction) =>
  request<TicketCase>(`/api/tickets/${encodeURIComponent(id)}/${action}`, {
    method: "POST",
    body: JSON.stringify(action === "route" ? { revision, agent } : { revision, plan }),
  });

export const beginZendeskOAuth = () => request<{ url: string }>("/api/tickets/oauth/start", { method: "POST" });

export const saveProcedure = (version: ProcedureVersion) =>
  request<ProcedureVersion>("/api/tickets/procedure", { method: "POST", body: JSON.stringify(version) });

export const previewProcedure = (id: string, procedure: TicketProcedure) =>
  request<TicketCase>(`/api/tickets/${encodeURIComponent(id)}/procedure-preview`, { method: "POST", body: JSON.stringify(procedure) });
