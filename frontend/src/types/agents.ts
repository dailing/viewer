export type AgentProvider = string;
export type AgentStatus = "idle" | "running" | "exited" | "failed";

export type AgentPrompt = {
  text: string;
  created_at: number;
};

export type AgentFileChange = {
  path: string;
  change_type: string;
  diff?: string | null;
};

export type AgentEvent = {
  index: number;
  received_at: number;
  event_type: string;
  text: string;
  file_changes: AgentFileChange[];
  patch_text?: string | null;
  raw_preview?: Record<string, unknown> | null;
};

export type AgentQueueItem = {
  id: string;
  prompt: string;
  created_at: number;
  updated_at: number;
  model?: string | null;
};

export type AgentApproval = {
  id: string;
  provider: AgentProvider;
  session_id: string;
  run_id?: string | null;
  title: string;
  description: string;
  command?: string | null;
  choices: string[];
  created_at: number;
  raw?: Record<string, unknown> | null;
};

export type AgentProviderInfo = {
  id: AgentProvider;
  name: string;
  icon: string;
};

export type AgentSessionInfo = {
  id: string;
  provider: AgentProvider;
  ref: string;
  provider_session_id?: string | null;
  title: string;
  cwd: string;
  cwd_relative?: string | null;
  model?: string | null;
  created_at: number;
  updated_at: number;
  status: AgentStatus;
  exit_code?: number | null;
  event_count: number;
  total_tokens?: number | null;
  queue: AgentQueueItem[];
  pending_approvals: AgentApproval[];
  raw: Record<string, unknown>;
};

export type AgentSessionSnapshot = AgentSessionInfo & {
  prompts: AgentPrompt[];
  events: AgentEvent[];
};

export type AgentSocketMessage =
  | { type: "snapshot"; session: AgentSessionSnapshot; source?: string }
  | { type: "event"; event: AgentEvent; session: AgentSessionInfo; source?: string }
  | { type: "status"; session: AgentSessionInfo; source?: string }
  | { type: "deleted"; source?: string };
