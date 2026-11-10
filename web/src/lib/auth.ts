import { request } from "./api-client";

export type Role = "viewer" | "user" | "admin";

const roleRank: Record<Role, number> = { viewer: 1, user: 2, admin: 3 };

/** True when `role` grants every permission of `want` (viewer < user < admin). */
export const roleAtLeast = (role: Role | undefined, want: Role): boolean =>
  role !== undefined && roleRank[role] >= roleRank[want];

export interface SessionInfo {
  authenticated: boolean;
  username?: string;
  role?: Role;
  via?: string;
  oidc_enabled: boolean;
}

export interface Account {
  username: string;
  role: Role;
  disabled: boolean;
  created_at: string;
  updated_at: string;
  has_password: boolean;
}

export const getSession = () => request<SessionInfo>("/api/auth/session");

export const signIn = (username: string, password: string) =>
  request<SessionInfo>("/api/auth/login", { method: "POST", body: JSON.stringify({ username, password }) });

export const signOut = () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" });

export const listAccounts = () => request<{ users: Account[] }>("/api/auth/admin/users");

export const createAccount = (input: { username: string; password: string; role: Role }) =>
  request<{ users: Account[] }>("/api/auth/admin/users", { method: "POST", body: JSON.stringify(input) });

export const updateAccount = (username: string, input: { role?: Role; disabled?: boolean }) =>
  request<{ users: Account[] }>(`/api/auth/admin/users/${encodeURIComponent(username)}`, {
    method: "PUT", body: JSON.stringify(input),
  });

export const setAccountPassword = (username: string, password: string) =>
  request<{ users: Account[] }>(`/api/auth/admin/users/${encodeURIComponent(username)}/password`, {
    method: "POST", body: JSON.stringify({ password }),
  });

export const removeAccount = (username: string) =>
  request<{ users: Account[] }>(`/api/auth/admin/users/${encodeURIComponent(username)}`, { method: "DELETE" });

/** One OAuth permission an API client can be granted. */
export type Scope = "investigations:run" | "investigations:read";

export interface APIClient {
  id: string;
  scopes: Scope[];
  disabled: boolean;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export interface ClientListing {
  clients: APIClient[];
  scopes: Scope[];
}

/** A listing plus the one-time secret of the client that was created or rotated. */
export interface IssuedSecret extends ClientListing {
  client_id: string;
  client_secret: string;
}

export const listClients = () => request<ClientListing>("/api/auth/admin/clients");

export const createClient = (input: { id: string; scopes: Scope[] }) =>
  request<IssuedSecret>("/api/auth/admin/clients", { method: "POST", body: JSON.stringify(input) });

export const updateClient = (id: string, input: { scopes?: Scope[]; disabled?: boolean }) =>
  request<ClientListing>(`/api/auth/admin/clients/${encodeURIComponent(id)}`, {
    method: "PUT", body: JSON.stringify(input),
  });

export const rotateClientSecret = (id: string) =>
  request<IssuedSecret>(`/api/auth/admin/clients/${encodeURIComponent(id)}/secret`, { method: "POST" });

export const removeClient = (id: string) =>
  request<ClientListing>(`/api/auth/admin/clients/${encodeURIComponent(id)}`, { method: "DELETE" });
