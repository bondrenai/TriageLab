import { request } from "./api-client";
import type { TicketAction, TicketCase, TicketWorkspace } from "./types/ticket";

export const readTicketWorkspace = () => request<TicketWorkspace>("/api/tickets");

export const takeTicketAction = ({ id, action, revision, plan }: TicketAction) =>
  request<TicketCase>(`/api/tickets/${encodeURIComponent(id)}/${action}`, {
    method: "POST",
    body: JSON.stringify({ revision, plan }),
  });
