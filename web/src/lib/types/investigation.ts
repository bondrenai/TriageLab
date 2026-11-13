import type { TicketSource } from "./ticket";

export interface Settings {
  agent: string;
  sandbox_template?: string;
  namespace: string;
  run_namespaces?: string[];
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
  round: number;
  resource_state: "active" | "retained" | "deleted" | "unconfirmed";
  attention?: { reason: string; action: string };
}

export interface ToolCall {
  id: string;
  name: string;
  args?: Record<string, unknown>;
  response?: Record<string, unknown>;
  pending?: boolean;
}

export interface Experiment {
  call_id?: string;
  process_id: string;
  command: string[];
  cwd?: string;
  env?: Record<string, string>;
  state?: string;
  exit_code?: number | null;
  stdout?: string;
  stderr?: string;
  outputs_complete?: boolean;
  truncated?: boolean;
  timed_out?: boolean;
  stopped_for_pause?: boolean;
}

export interface Hypothesis {
  statement: string;
  status: string;
  evidence_process_ids: string[];
  contradicting_process_ids?: string[];
  distinguishing_observation?: string;
}

export interface ExperimentAssessment {
  process_id: string;
  question: string;
  expected_observation: string;
  interpretation: string;
}

export interface TicketNote {
  source: TicketSource;
  visibility: "internal" | "repository" | "project" | "workspace";
  ticket_id: string;
  ticket_url: string;
  status: "ready" | "posted" | "unconfirmed" | "blocked";
  body?: string;
  plan?: string;
  comment_id?: string;
  can_post: boolean;
  automatic?: boolean;
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
  action_error?: string;
  task_state?: string;
  task_failure?: string;
  ticket_note?: TicketNote;
  round?: number;
  round_started_at?: string;
  rounds?: InvestigationReport[];
  lifecycle?: {
    phase: string;
    round: number;
    can_pause: boolean;
    can_steer?: boolean;
    can_resume: boolean;
    can_close: boolean;
    resume_settings: string;
    timeout_seconds: number;
    max_experiments: number;
    message?: string;
  };
  case: {
    description: string;
    attachments?: { path: string; size_bytes: number; sha256: string }[];
    environment?: { repository_name?: string };
  };
  continuation?: InvestigationReport["case"];
  experiments?: Experiment[];
  tool_calls?: ToolCall[];
  analysis?: {
    summary?: string;
    observations?: string[];
    hypotheses?: Hypothesis[];
    experiment_assessments?: ExperimentAssessment[];
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
