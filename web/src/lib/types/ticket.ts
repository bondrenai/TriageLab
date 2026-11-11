export type TicketSource = "zendesk" | "github" | "linear" | "bondren" | "jira";

export interface TicketCase {
  ticket_id: string;
  account: string;
  source?: TicketSource;
  url?: string;
  title: string;
  requester?: { id: string; name?: string; email?: string; url?: string };
  state: "excluded" | "review" | "ask" | "waiting" | "approval" | "launched";
  reason: string;
  facts?: { label: string; value: string; comment_id: string; url?: string }[];
  files?: { path: string; sha256: string; size_bytes: number; source_url?: string }[];
  draft?: string;
  questions?: { body: string; status: string; at: string }[];
  plan?: string;
  plan_expires: string;
  checked_at: string;
  run_id?: string;
  auto_run?: boolean;
  unattended?: boolean;
  paused: boolean;
  revision: string;
  procedure_revision?: string;
  agent?: string;
  sandbox?: string;
  matches?: RouteRule[];
  agent_choices?: string[];
  matched_tag?: string;
  missing?: string[];
}

export interface RouteRule {
  tag: string;
  agent: string;
}

export interface EvidenceRule {
  kind: "field" | "attachment";
  match: string;
  required: boolean;
  required_when?: string;
  pattern?: string;
  question?: string;
}

export interface TicketProcedure {
  policy: TicketSettings["policy"];
  evidence: EvidenceRule[];
  clarification: string;
  default_agent: string;
  sandbox: string;
  routes: RouteRule[];
}

export interface ProcedureVersion {
  procedure: TicketProcedure;
  additional_procedures?: TicketProcedure[];
  revision: string;
}

export interface TicketSettings {
  policy: {
    tag: string;
    priorities?: string[];
    field_id?: number;
    field_value?: string;
    auto_questions: boolean;
    max_questions: number;
    auto_runs?: boolean;
    auto_run_condition?: string;
    match_condition?: string;
  };
  internal_notes: boolean;
  investigation_comments: boolean;
  poll_seconds: number;
  max_experiments: number;
  timeout_seconds: number;
  template: string;
  clarification_limit: number;
  auto_runs_allowed: boolean;
}

export type TicketWorkspace =
  | { enabled: false }
  | {
      enabled: true;
      source: TicketSource;
      account: string;
      connected: boolean;
      repository?: string;
      settings: TicketSettings;
      procedure: ProcedureVersion;
      cases: TicketCase[];
      oauth?: { connected: boolean; account: string; workspace_url: string; scopes: string[] };
    };

export interface TicketAction {
  id: string;
  action: "preview" | "track" | "refresh" | "clarify" | "approve" | "pause" | "resume" | "route";
  revision?: string;
  plan?: string;
  agent?: string;
}
