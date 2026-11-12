import type { TicketSource } from "./ticket";

// The full saved configuration for one connection, exactly as the server
// stores it. Fields outside the connection's source stay empty.
export interface TicketPolicy {
  tag: string;
  priorities?: string[];
  field_id?: number;
  field_value?: string;
  auto_questions: boolean;
  max_questions: number;
  match_condition?: string;
  auto_run_condition?: string;
  auto_runs?: boolean;
}

export interface ConnectionConfig {
  source?: TicketSource;
  repository?: string;
  token_env?: string;
  workspace?: string;
  team?: string;
  origin?: string;
  email?: string;
  subdomain?: string;
  client_id?: string;
  redirect_uri?: string;
  poll_seconds: number;
  internal_notes: boolean;
  investigation_comments: boolean;
  max_auto_runs_per_day?: number;
  policy: TicketPolicy;
}

export interface ConnectionCredential {
  mode: "stored" | "env" | "none";
  env?: string;
}

// The admin projection of a saved connection. The credential value never
// leaves the server.
export interface ConnectionStatus {
  id: string;
  label?: string;
  source: TicketSource;
  account: string;
  running: boolean;
  connected: boolean;
  credential: ConnectionCredential;
  config: ConnectionConfig;
  oauth?: { account: string; workspace_url: string; connected: boolean; scopes: string[] };
}

export interface ConnectionInput {
  label?: string;
  config: ConnectionConfig;
  token?: string;
  confirm_reset?: boolean;
}

export interface ConnectionTestResult {
  ok: boolean;
  detail?: string;
}
