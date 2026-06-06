import type { AgentProvider } from "./agents";

export type SuperRole = {
  id: string;
  name: string;
  description: string;
  provider: AgentProvider;
  cwd: string;
  model?: string | null;
  session_ref: string;
  created_at: number;
  updated_at: number;
};

export type SuperWorkspaceData = {
  id: string;
  name: string;
  common_prompt: string;
  roles: SuperRole[];
};

export type SuperWorkspacePatch = {
  common_prompt?: string;
};

export type SuperRoleCreate = {
  name: string;
  description?: string;
  provider?: AgentProvider;
  cwd?: string;
  model?: string | null;
};

export type SuperRolePatch = Partial<Omit<SuperRole, "id" | "created_at" | "updated_at">>;

export type SuperDispatchResponse = {
  role_ids: string[];
  rationale: string;
  raw?: Record<string, unknown> | null;
};

export type AgentHistoryMessage = {
  id: string;
  provider: AgentProvider;
  viewer_session_id?: string | null;
  provider_session_id?: string | null;
  index: number;
  received_at: number;
  role: string;
  event_type: string;
  text: string;
  query?: string | null;
  status?: string | null;
  rationale?: string;
  error?: string;
  requested_role_ids?: string[];
  selected_role_ids?: string[];
  file_changes: {
    path: string;
    change_type: string;
    diff?: string | null;
  }[];
  patch_text?: string | null;
  raw: Record<string, unknown>;
  occurred_at: number;
  query_id?: string | null;
  query_message_id?: string | null;
  driver_run_id?: string | null;
  super_run_id?: string | null;
  super_target_id?: string | null;
  parent_message_id?: string | null;
  sender_role_id?: string | null;
  recipient_role_id?: string | null;
};

export type SuperHistoryTarget = {
  id: string;
  run_id: string;
  role_id: string;
  role_name: string;
  provider: AgentProvider;
  viewer_session_id: string;
  session_ref: string;
  agent_prompt: string;
  status: string;
  created_at: number;
  updated_at: number;
  messages: AgentHistoryMessage[];
};

export type SuperHistoryRun = {
  id: string;
  user_id: string;
  message: string;
  query: string;
  message_id: string;
  role_ids: string[];
  citation_ids?: string[];
  status: "selecting" | "queued" | "running" | "dispatched" | "completed" | "failed";
  rationale: string;
  error: string;
  parent_message_id?: string | null;
  sender_role_id?: string | null;
  created_at: number;
  updated_at: number;
  targets: SuperHistoryTarget[];
};

export type SuperHistoryRunsPage = {
  runs: SuperHistoryRun[];
  has_more: boolean;
  next_before?: number | null;
  next_after?: number | null;
};

export type SuperDisplayTarget = {
  id: string;
  role_id: string;
  role_name: string;
  provider: AgentProvider;
  session_ref: string;
  status: string;
};

export type SuperDisplayItem = {
  id: string;
  kind: "query" | "message";
  user_id: string;
  text: string;
  role: string;
  event_type: string;
  provider: AgentProvider;
  created_at: number;
  updated_at: number;
  message_id: string;
  query_message_id?: string | null;
  driver_run_id?: string | null;
  parent_message_id?: string | null;
  sender_role_id?: string | null;
  recipient_role_id?: string | null;
  role_id?: string | null;
  role_name: string;
  session_ref: string;
  target_status: string;
  run_status: string;
  error: string;
  citation_ids?: string[];
  dispatch_targets: SuperDisplayTarget[];
  raw: Record<string, unknown>;
};

export type SuperDisplayItemsPage = {
  items: SuperDisplayItem[];
  has_more: boolean;
  next_before?: number | null;
  next_after?: number | null;
};

export type SuperHistoryRunCreate = {
  message: string;
  parent_message_id?: string | null;
  sender_role_id?: string | null;
};
