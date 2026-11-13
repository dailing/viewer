export interface Sender { from: "user" | "role"; role_id?: string; role_name?: string }
export interface ChatMessage { id: string; chat_id: string; task_id: string; turn_id: string; role: "user" | "assistant"; text: string; created_at: number; sender: Sender; source_plugin?: string; causation_id?: string; deleted?: boolean }
export interface ChatBlock { id: string; chat_id: string; task_id: string; turn_id: string; kind: string; text: string; payload: string; occurred_at: number; role_id?: string; role_name?: string }
export interface TurnTarget { dispatch_id?: string; role_id?: string; role_name?: string; agent?: string; provider?: string; model?: string }
export interface TurnTargetEntry { dispatchId: string; roleId: string; roleName: string; label: string }
export interface TurnSession { session_id: string; dispatch_id?: string; role_id?: string; role_name?: string; started_at?: number; task_id: string }
export interface QueuedMessage { message_id: string; dispatch_id: string; chat_id: string; task_id: string; role_id: string; role_ids?: string[]; role_name?: string; text: string; enqueued_at: number; position?: number }
export interface RunningTurn { turn_id: string; task_id: string; role_id: string; role_name?: string }
export interface Task {
  id: string; chat_id: string; chat_name?: string; title: string; title_source: "default" | "generated" | "manual";
  title_revision: number; title_query_message_id?: string; created_by: string; causation_id?: string; correlation_id?: string;
  pinned: boolean; running?: boolean; created_at: number; updated_at: number; ended_at?: number | null; status: "open" | "ended";
  summary_status?: "missing" | "pending" | "failed" | "completed"; summary_final?: boolean; summary_preview?: string;
  summary_revision?: number; pending_decisions?: number;
}
export interface TaskSummary { task_id: string; chat_id: string; text: string; source_turn_ids: string[]; summary_revision: number; source_revision: number; provider: string; model: string; status: string; final: boolean; error: string; updated_at: number }
export interface DecisionOption { id: string; label: string; description?: string; action?: "end_current_task" }
export interface Decision {
  id: string; chat_id: string; task_id: string; source_turn_id: string; source_message_id: string; role_id: string;
  chat_name?: string; task_title?: string; role_name?: string;
  version: number; kind: string; question: string; options: DecisionOption[]; display: string; tone: string; parse_warning: string;
  status: "pending" | "dispatching" | "resolved" | "cancelled" | "invalid"; response: string; resolved_dispatch_id: string;
  marker_start: number; marker_end: number; created_at: number; updated_at: number;
}
export interface DecisionList { decisions: Decision[]; has_more?: boolean; next_created_at?: number; next_id?: string; pending_count?: number }
export interface TaskList { tasks: Task[]; has_more?: boolean; next_updated_at?: number; next_id?: string }
export interface ChatBlockList { blocks: ChatBlock[]; truncated?: boolean; next_after?: number; turn_targets?: Record<string, TurnTarget>; turn_sessions?: Record<string, TurnSession> }
export interface ChatHistory { chat: Chat; running_turns?: RunningTurn[]; background_task_ids?: string[]; turn_targets?: Record<string, TurnTarget>; turn_sessions?: Record<string, TurnSession>; queued_messages?: QueuedMessage[]; messages?: ChatMessage[]; has_more?: boolean; has_more_after?: boolean }
export interface ChatList { chats: Chat[]; active_chat_id: string; running_chat_ids?: string[] }
export interface Role { id: string; name: string; description: string; prompt: string; cwd: string; routing_policy_id: string; session_policy: string; created_at: number; updated_at: number }
export interface Chat { id: string; name: string; type: string; pinned: boolean; position: number; root: string; common_prompt: string; member_role_ids: string[]; role_routing_policy_overrides: Record<string, string>; created_at: number; updated_at: number }
export interface Workspace { id: string; name: string; common_prompt: string; roles: Role[]; routing_policies: RoutingPolicy[]; default_routing_policy_id: string }
export interface AgentProviderCatalog { provider: string; models: string[]; parameter_schema?: Record<string, unknown> }
export interface AgentCatalog { agent: string; plugin_id: string; online: boolean; providers: AgentProviderCatalog[] }
export interface RoutingCandidate { id: string; name: string; agent_id: string; provider_id: string; model_id: string; enabled: boolean; parameters: Record<string, unknown> }
export interface RoutingPolicy { id: string; name: string; enabled: boolean; auto_failover: boolean; max_attempts: number; candidates: RoutingCandidate[] }
export interface RoutingConfig { default_routing_policy_id: string; routing_policies: RoutingPolicy[] }
export function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error) }
