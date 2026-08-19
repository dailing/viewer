export interface Sender { from: "user" | "role"; role_id?: string; role_name?: string }
export interface ChatMessage { id: string; chat_id: string; turn_id: string; role: "user" | "assistant"; text: string; created_at: number; sender: Sender }
export interface ChatBlock { id: string; chat_id: string; turn_id: string; kind: string; text: string; payload: string; occurred_at: number; role_id?: string; role_name?: string }
export interface ChatBlockList { blocks: ChatBlock[]; truncated?: boolean; next_after?: number }
export interface Role { id: string; name: string; description: string; prompt: string; cwd: string; routing_policy_id: string; session_policy: string; context_recycle_percent: number | null; context_recycle_tokens: number | null; created_at: number; updated_at: number }
export interface Chat { id: string; name: string; type: string; pinned: boolean; root: string; common_prompt: string; member_role_ids: string[]; role_routing_policy_overrides: Record<string, string>; created_at: number; updated_at: number }
export interface RunningTurn { turn_id: string; role_id: string; role_name?: string }
export interface ChatList { chats: Chat[]; active_chat_id: string; running_chat_ids?: string[]; running_turns?: RunningTurn[]; messages?: ChatMessage[]; has_more?: boolean }
export interface Workspace { id: string; name: string; common_prompt: string; roles: Role[]; routing_policies: RoutingPolicy[]; default_routing_policy_id: string }
export interface AgentProviderCatalog { provider: string; models: string[]; parameter_schema?: Record<string, unknown> }
export interface AgentCatalog { agent: string; plugin_id: string; online: boolean; providers: AgentProviderCatalog[] }
export interface RoutingCandidate { id: string; name: string; agent_id: string; provider_id: string; model_id: string; enabled: boolean; parameters: Record<string, unknown> }
export interface RoutingPolicy { id: string; name: string; enabled: boolean; auto_failover: boolean; max_attempts: number; candidates: RoutingCandidate[] }
export interface RoutingConfig { default_routing_policy_id: string; routing_policies: RoutingPolicy[] }
export function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error) }
