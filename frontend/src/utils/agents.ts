import type { AgentProvider } from "../types/agents";
import type { CodexSessionInfo, CodexSessionSnapshot } from "../types/codex";
import type { HermesSessionInfo, HermesSessionSnapshot } from "../types/hermes";

export function agentRef(provider: AgentProvider, id: string): string {
  return `${provider}:${id}`;
}

export function parseAgentRef(ref: string): { provider: AgentProvider; id: string } | null {
  const index = ref.indexOf(":");
  if (index <= 0 || index === ref.length - 1) return null;
  return { provider: ref.slice(0, index), id: ref.slice(index + 1) };
}

export function agentRefForPane(pane: { agentSession?: string }): string | undefined {
  return pane.agentSession;
}

export function providerSessionId(session: Record<string, unknown>, provider: AgentProvider): string | null {
  const providerKey = `${provider}_session_id`;
  const direct = session[providerKey];
  if (typeof direct === "string") return direct;
  if (provider === "codex" && typeof session.codex_session_id === "string") return session.codex_session_id;
  if (provider === "hermes" && typeof session.hermes_session_id === "string") return session.hermes_session_id;
  return null;
}

export function toAgentSessionInfo(session: CodexSessionInfo | HermesSessionInfo, provider: AgentProvider) {
  return {
    id: session.id,
    provider,
    ref: agentRef(provider, session.id),
    provider_session_id: providerSessionId(session as unknown as Record<string, unknown>, provider),
    title: session.title,
    cwd: session.cwd,
    cwd_relative: session.cwd_relative,
    model: session.model,
    created_at: session.created_at,
    updated_at: session.updated_at,
    status: session.status,
    exit_code: session.exit_code,
    event_count: session.event_count,
    total_tokens: session.total_tokens,
    queue: session.queue,
    pending_approvals: session.pending_approvals ?? [],
    raw: session as unknown as Record<string, unknown>,
  };
}

export function toAgentSessionSnapshot(session: CodexSessionSnapshot | HermesSessionSnapshot, provider: AgentProvider) {
  return {
    ...toAgentSessionInfo(session, provider),
    prompts: session.prompts,
    events: session.events,
  };
}
