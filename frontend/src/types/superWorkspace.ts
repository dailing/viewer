import type { AgentProvider } from "./agents";

export type SuperRole = {
  id: string;
  name: string;
  description: string;
  provider: AgentProvider;
  cwd: string;
  model?: string | null;
  session_policy: "reuse" | "new_each_run";
  created_at: number;
  updated_at: number;
};

export type SuperWorkspaceData = {
  id: string;
  name: string;
  common_prompt: string;
  roles: SuperRole[];
};

export type SuperWorkspaceSummary = {
  id: string;
  name: string;
  created_at: number;
  updated_at: number;
};

export type SuperWorkspaceList = {
  active_workspace_id: string;
  workspaces: SuperWorkspaceSummary[];
};

export type SuperChatSummary = {
  id: string;
  workspace_id: string;
  name: string;
  type: "group" | "direct";
  pinned: boolean;
  cwd: string;
  common_prompt: string;
  member_role_ids: string[];
  created_at: number;
  updated_at: number;
};

export type SuperChatList = {
  active_chat_id: string;
  chats: SuperChatSummary[];
};

export type SuperChatCreate = {
  name?: string;
  type?: "group" | "direct";
  pinned?: boolean;
  cwd?: string;
  common_prompt?: string;
  member_role_ids?: string[];
};

export type SuperChatPatch = Partial<SuperChatCreate>;

export type SuperRoleCreate = {
  name: string;
  description?: string;
  provider?: AgentProvider;
  cwd?: string;
  model?: string | null;
  session_policy?: "reuse" | "new_each_run";
};

export type SuperRolePatch = Partial<Omit<SuperRole, "id" | "created_at" | "updated_at">>;

export type AgentHistoryMessage = {
  id: string;
  workspace_id?: string | null;
  chat_id?: string | null;
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
  workspace_id: string;
  chat_id: string;
  run_id: string;
  role_id: string;
  role_name: string;
  provider: AgentProvider;
  viewer_session_id: string;
  provider_session_id?: string | null;
  session_ref: string;
  agent_prompt: string;
  status: string;
  model_context_window?: number | null;
  total_tokens?: number | null;
  context_used_percent?: number | null;
  created_at: number;
  updated_at: number;
  messages: AgentHistoryMessage[];
};

export type SuperHistoryRun = {
  id: string;
  workspace_id: string;
  chat_id: string;
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
  workspace_id: string;
  chat_id: string;
  role_id: string;
  role_name: string;
  provider: AgentProvider;
  viewer_session_id: string;
  provider_session_id?: string | null;
  session_ref: string;
  status: string;
  model_context_window?: number | null;
  total_tokens?: number | null;
  context_used_percent?: number | null;
};

export type SuperDisplayItem = {
  id: string;
  workspace_id: string;
  chat_id: string;
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
  viewer_session_id: string;
  provider_session_id?: string | null;
  session_ref: string;
  model_context_window?: number | null;
  total_tokens?: number | null;
  context_used_percent?: number | null;
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
  chat_id?: string | null;
  role_ids?: string[] | null;
  parent_message_id?: string | null;
  sender_role_id?: string | null;
};
