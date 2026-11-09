export interface TicketCase {
  ticket_id: string;
  account: string;
  source?: "zendesk" | "github";
  url?: string;
  title: string;
  requester?: { id: string; name?: string; email?: string; url?: string };
  state: "excluded" | "review" | "ask" | "waiting" | "approval" | "launched";
  reason: string;
  facts?: { label: string; value: string; comment_id: string; url?: string }[];
  files?: { path: string; sha256: string; size_bytes: number }[];
  draft?: string;
  questions?: { body: string; status: string; at: string }[];
  plan?: string;
  plan_expires: string;
  checked_at: string;
  run_id?: string;
  paused: boolean;
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
  };
  poll_seconds: number;
  max_experiments: number;
  timeout_seconds: number;
  template: string;
}

export type TicketWorkspace =
  | { enabled: false }
  | {
      enabled: true;
      source: "zendesk" | "github";
      account: string;
      connected: boolean;
      repository?: string;
      settings: TicketSettings;
      cases: TicketCase[];
      oauth?: { connected: boolean; account: string; workspace_url: string; scopes: string[] };
    };

export interface TicketAction {
  id: string;
  action: "preview" | "track" | "refresh" | "clarify" | "approve" | "pause" | "resume";
  revision?: string;
  plan?: string;
}
