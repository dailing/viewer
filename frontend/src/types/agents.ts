export type AgentProvider = string;

export type AgentProviderInfo = {
  id: AgentProvider;
  name: string;
  icon: string;
  context_recycle_percent?: number;
  context_recycle_tokens?: number | null;
};
