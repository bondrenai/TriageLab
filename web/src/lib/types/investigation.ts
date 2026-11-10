export interface Settings {
  agent: string;
  sandbox_template?: string;
  namespace: string;
  manual_agents: string[];
  resource_writes?: boolean;
  max_description_bytes: number;
  max_file_bytes: number;
  max_total_bytes: number;
  max_files: number;
  max_experiments: number;
  timeout_seconds: number;
}

export interface RunSummary {
  id: string;
  description: string;
  status: string;
  outcome: string;
  started_at: string;
  finished_at?: string;
  experiment_count: number;
  active: boolean;
}

export interface Experiment {
  process_id: string;
  command: string[];
  cwd?: string;
  state?: string;
  exit_code?: number | null;
  stdout?: string;
  stderr?: string;
  outputs_complete?: boolean;
  truncated?: boolean;
  timed_out?: boolean;
}

export interface Hypothesis {
  statement: string;
  status: string;
  evidence_process_ids: string[];
}

export interface TicketNote {
  source: "zendesk" | "github" | "linear";
  visibility: "internal" | "repository" | "workspace";
  ticket_id: string;
  ticket_url: string;
  status: "ready" | "posted" | "unconfirmed" | "blocked";
  body?: string;
  plan?: string;
  comment_id?: string;
  can_post: boolean;
  findings?: string[];
}

export interface InvestigationReport {
  agent?: { namespace: string; name: string };
  sandbox_template?: string;
  run_id: string;
  status: string;
  outcome: string;
  started_at: string;
  finished_at?: string;
  active?: boolean;
  cleanup_running?: boolean;
  cleanup_error?: string;
  progress?: string[];
  ticket_note?: TicketNote;
  case: {
    description: string;
    attachments?: { path: string; size_bytes: number; sha256: string }[];
    environment?: { repository_name?: string };
  };
  experiments?: Experiment[];
  analysis?: {
    summary?: string;
    observations?: string[];
    hypotheses?: Hypothesis[];
    reproduced_process_ids?: string[];
    root_cause?: { statement: string; evidence_process_ids: string[] };
    reproduction_steps?: string[];
    next_actions?: string[];
    missing_information?: string[];
  };
  warnings?: string[];
  errors?: string[];
  cleanup?: { sandbox_deleted?: boolean; session_deleted?: boolean; errors?: string[] };
}
