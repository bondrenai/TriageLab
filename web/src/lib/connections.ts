import { request } from "./api-client";
import type { ConnectionInput, ConnectionStatus, ConnectionTestResult } from "./types/connection";

export const listConnections = () =>
  request<{ connections: ConnectionStatus[] }>("/api/tickets/connections");

export const createConnection = (input: ConnectionInput) =>
  request<{ id: string }>("/api/tickets/connections", { method: "POST", body: JSON.stringify(input) });

export const updateConnection = (id: string, input: ConnectionInput) =>
  request<{ id: string }>(`/api/tickets/connections/${id}`, { method: "PUT", body: JSON.stringify(input) });

export const deleteConnection = (id: string) =>
  request<void>(`/api/tickets/connections/${id}`, { method: "DELETE" });

// Test proposed settings before saving them. The token is optional when the
// caller wants to verify an environment-variable credential instead.
export const testConnection = (input: ConnectionInput) =>
  request<ConnectionTestResult>("/api/tickets/connections/test", { method: "POST", body: JSON.stringify(input) });

// Test a saved connection, keeping its stored credential when no replacement
// token is supplied.
export const testSavedConnection = (id: string, input: ConnectionInput) =>
  request<ConnectionTestResult>(`/api/tickets/connections/${id}/test`, { method: "POST", body: JSON.stringify(input) });

export const beginZendeskConnect = (id: string) =>
  request<{ url: string }>(`/api/tickets/c/${encodeURIComponent(id)}/oauth/start`, { method: "POST" });
