export interface Sender { from: "user" | "role"; role_id?: string; role_name?: string }
export interface ChatMessage { id: string; chat_id: string; turn_id: string; role: "user" | "assistant"; text: string; created_at: number; sender: Sender }
export interface Role { id: string; name: string; description: string; prompt: string; provider: "hermes" | "codex-app-server"; cwd: string; model: string | null; routing_policy_id: string; capability_requirements: Record<string, unknown>; session_policy: string; context_recycle_percent: number | null; context_recycle_tokens: number | null; created_at: number; updated_at: number }
export interface Chat { id: string; name: string; type: string; pinned: boolean; root: string; common_prompt: string; member_role_ids: string[]; role_routing_policy_overrides: Record<string, string>; created_at: number; updated_at: number }
export interface ChatList { chats: Chat[]; active_chat_id: string; messages?: ChatMessage[] }
export interface Workspace { id: string; name: string; common_prompt: string; roles: Role[]; routing_policies: unknown[]; default_routing_policy_id: string }
export function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error) }
