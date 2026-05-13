import type { DirectoryListing, FileMeta, ViewerConfig, WorkspaceConfig } from "../types/files";
import type { TerminalInfo, TerminalSnapshot } from "../types/terminals";
import type { CodexCliStatus, CodexModelOptions, CodexSessionInfo, CodexSessionSnapshot } from "../types/codex";
import type { HermesSessionInfo, HermesSessionSnapshot } from "../types/hermes";
import type { WorkspaceData, WorkspaceSnapshot } from "../types/workspaces";
import type { AgentLoopDefinition, AgentLoopInfo, AgentLoopRunRecord } from "../types/agentLoops";
import type { AgentProvider, AgentProviderInfo } from "../types/agents";
import type { GitDiffText, GitStatus } from "../types/git";

async function request<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(url, options);
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || response.statusText);
  }
  return response.json() as Promise<T>;
}

function socketUrl(path: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}${path}`;
}

export function rawUrl(path: string, contentHash?: string, base?: string): string {
  const hashQuery = contentHash ? `&h=${encodeURIComponent(contentHash)}` : "";
  const baseQuery = base !== undefined ? `&base=${encodeURIComponent(base)}` : "";
  return `/api/file/raw?path=${encodeURIComponent(path)}${hashQuery}${baseQuery}`;
}

function encodePathSegments(path: string): string {
  return path
    .split("/")
    .filter(Boolean)
    .map((part) => encodeURIComponent(part))
    .join("/");
}

export function siteUrl(path: string, contentHash?: string): string {
  const hashQuery = contentHash ? `?h=${encodeURIComponent(contentHash)}` : "";
  return `/api/file/site/${encodePathSegments(path)}${hashQuery}`;
}

export async function getTree(path = ""): Promise<DirectoryListing> {
  return request<DirectoryListing>(`/api/tree?path=${encodeURIComponent(path)}`);
}

export async function getMeta(path: string): Promise<FileMeta> {
  return request<FileMeta>(`/api/file/meta?path=${encodeURIComponent(path)}`);
}

export async function getText(path: string): Promise<string> {
  const response = await fetch(`/api/file/content?path=${encodeURIComponent(path)}`);
  if (!response.ok) throw new Error(await response.text());
  return response.text();
}

export async function resolveMarkdownLink(base: string, target: string): Promise<{ path: string }> {
  return request<{ path: string }>(
    `/api/file/resolve-link?base=${encodeURIComponent(base)}&target=${encodeURIComponent(target)}`,
  );
}

export async function resolveDirectoryLink(base: string, target: string): Promise<{ path: string }> {
  return request<{ path: string }>(
    `/api/file/resolve-directory-link?base=${encodeURIComponent(base)}&target=${encodeURIComponent(target)}`,
  );
}

export async function getGitStatus(scope?: string): Promise<GitStatus> {
  const query = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return request<GitStatus>(`/api/git/status${query}`);
}

export async function getGitDiff(path: string): Promise<GitDiffText> {
  return request<GitDiffText>(`/api/git/diff?path=${encodeURIComponent(path)}`);
}

export async function stageGitPath(path?: string, scope?: string): Promise<GitStatus> {
  return request<GitStatus>("/api/git/stage", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, scope }),
  });
}

export async function revertGitPath(path: string): Promise<GitStatus> {
  return request<GitStatus>("/api/git/revert", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  });
}

export async function commitGit(message: string, scope?: string): Promise<GitStatus> {
  return request<GitStatus>("/api/git/commit", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, scope }),
  });
}

export async function pushGit(scope?: string): Promise<{ status: string; output: string }> {
  const query = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return request<{ status: string; output: string }>(`/api/git/push${query}`, { method: "POST" });
}

export async function getConfig(): Promise<ViewerConfig> {
  return request<ViewerConfig>("/api/config");
}

export async function putConfig(config: ViewerConfig): Promise<ViewerConfig> {
  return request<ViewerConfig>("/api/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
}

export async function getWorkspaces(): Promise<WorkspaceData> {
  return request<WorkspaceData>("/api/workspaces");
}

export async function getWorkspaceConfig(): Promise<WorkspaceConfig> {
  return request<WorkspaceConfig>("/api/workspaces/config");
}

export async function putWorkspaceConfig(config: WorkspaceConfig): Promise<WorkspaceData> {
  return request<WorkspaceData>("/api/workspaces/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
}

export async function putWorkspace(id: string, snapshot: WorkspaceSnapshot): Promise<WorkspaceData> {
  return request<WorkspaceData>(`/api/workspaces/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(snapshot),
  });
}

export async function activateWorkspace(id: string): Promise<WorkspaceData> {
  return request<WorkspaceData>(`/api/workspaces/${encodeURIComponent(id)}/activate`, { method: "POST" });
}

export async function restartServer(): Promise<{ status: string; pid: number }> {
  return request<{ status: string; pid: number }>("/api/admin/restart", { method: "POST" });
}

export async function stopServer(): Promise<{ status: string; pid: number }> {
  return request<{ status: string; pid: number }>("/api/admin/stop", { method: "POST" });
}

export async function listTerminals(): Promise<TerminalInfo[]> {
  return request<TerminalInfo[]>("/api/terminals");
}

export async function createTerminal(cwd = ""): Promise<TerminalInfo> {
  return request<TerminalInfo>("/api/terminals", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ cwd }),
  });
}

export async function getTerminal(id: string): Promise<TerminalSnapshot> {
  return request<TerminalSnapshot>(`/api/terminals/${encodeURIComponent(id)}`);
}

export async function terminateTerminal(id: string): Promise<TerminalInfo> {
  return request<TerminalInfo>(`/api/terminals/${encodeURIComponent(id)}/terminate`, { method: "POST" });
}

export async function deleteTerminal(id: string): Promise<void> {
  await request<{ status: string }>(`/api/terminals/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function terminalSocketUrl(id: string): string {
  return socketUrl(`/api/terminals/${encodeURIComponent(id)}/ws`);
}

export async function listAgentProviders(): Promise<AgentProviderInfo[]> {
  return request<AgentProviderInfo[]>("/api/agents/providers");
}

export async function listAgentSessions(provider: AgentProvider): Promise<unknown[]> {
  return request<unknown[]>(`/api/agents/sessions?provider=${encodeURIComponent(provider)}`);
}

export async function createAgentSession(provider: AgentProvider, prompt: string, cwd = "", model?: string): Promise<unknown> {
  return request<unknown>("/api/agents/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider, prompt, cwd, model }),
  });
}

export async function getAgentSession(provider: AgentProvider, id: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}?provider=${encodeURIComponent(provider)}`);
}

export async function sendAgentMessage(provider: AgentProvider, id: string, prompt: string, model?: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider, prompt, model }),
  });
}

export async function queueAgentMessage(provider: AgentProvider, id: string, prompt: string, model?: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/queue`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider, prompt, model }),
  });
}

export async function updateAgentQueuedMessage(provider: AgentProvider, id: string, itemId: string, prompt: string, model?: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider, prompt, model }),
  });
}

export async function deleteAgentQueuedMessage(provider: AgentProvider, id: string, itemId: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}?provider=${encodeURIComponent(provider)}`, { method: "DELETE" });
}

export async function terminateAgentSession(provider: AgentProvider, id: string): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/terminate`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider }),
  });
}

export async function resolveAgentApproval(provider: AgentProvider, id: string, approvalId: string, choice: string, all = false): Promise<unknown> {
  return request<unknown>(`/api/agents/sessions/${encodeURIComponent(id)}/approvals/${encodeURIComponent(approvalId)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider, choice, all }),
  });
}

export function agentSessionSocketUrl(provider: AgentProvider, id: string): string {
  return socketUrl(`/api/agents/sessions/${encodeURIComponent(id)}/ws?provider=${encodeURIComponent(provider)}`);
}

export async function listCodexSessions(): Promise<CodexSessionInfo[]> {
  return request<CodexSessionInfo[]>("/api/agents/sessions?provider=codex");
}

export async function getCodexStatus(): Promise<CodexCliStatus> {
  return request<CodexCliStatus>("/api/codex/status");
}

export async function getCodexModels(): Promise<CodexModelOptions> {
  return request<CodexModelOptions>("/api/codex/models");
}

export async function createCodexSession(prompt: string, cwd = "", model?: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>("/api/agents/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "codex", prompt, cwd, model }),
  });
}

export async function getCodexSession(id: string): Promise<CodexSessionSnapshot> {
  return request<CodexSessionSnapshot>(`/api/agents/sessions/${encodeURIComponent(id)}?provider=codex`);
}

export async function sendCodexMessage(id: string, prompt: string, model?: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "codex", prompt, model }),
  });
}

export async function queueCodexMessage(id: string, prompt: string, model?: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "codex", prompt, model }),
  });
}

export async function updateCodexQueuedMessage(id: string, itemId: string, prompt: string, model?: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "codex", prompt, model }),
  });
}

export async function deleteCodexQueuedMessage(id: string, itemId: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}?provider=codex`, { method: "DELETE" });
}

export async function terminateCodexSession(id: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/terminate`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "codex" }),
  });
}

export function codexSessionSocketUrl(id: string): string {
  return agentSessionSocketUrl("codex", id);
}

export async function listHermesSessions(): Promise<HermesSessionInfo[]> {
  return request<HermesSessionInfo[]>("/api/agents/sessions?provider=hermes");
}

export async function createHermesSession(prompt: string, cwd = "", model?: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>("/api/agents/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "hermes", prompt, cwd, model }),
  });
}

export async function getHermesSession(id: string): Promise<HermesSessionSnapshot> {
  return request<HermesSessionSnapshot>(`/api/agents/sessions/${encodeURIComponent(id)}?provider=hermes`);
}

export async function sendHermesMessage(id: string, prompt: string, model?: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "hermes", prompt, model }),
  });
}

export async function queueHermesMessage(id: string, prompt: string, model?: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "hermes", prompt, model }),
  });
}

export async function updateHermesQueuedMessage(id: string, itemId: string, prompt: string, model?: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "hermes", prompt, model }),
  });
}

export async function deleteHermesQueuedMessage(id: string, itemId: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/queue/${encodeURIComponent(itemId)}?provider=hermes`, { method: "DELETE" });
}

export async function terminateHermesSession(id: string): Promise<HermesSessionInfo> {
  return request<HermesSessionInfo>(`/api/agents/sessions/${encodeURIComponent(id)}/terminate`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "hermes" }),
  });
}

export function hermesSessionSocketUrl(id: string): string {
  return agentSessionSocketUrl("hermes", id);
}

export async function listAgentLoops(): Promise<AgentLoopInfo[]> {
  return request<AgentLoopInfo[]>("/api/agent-loops");
}

export async function createAgentLoop(name: string): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>("/api/agent-loops", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
}

export async function reloadAgentLoops(): Promise<AgentLoopInfo[]> {
  return request<AgentLoopInfo[]>("/api/agent-loops/reload", { method: "POST" });
}

export async function updateAgentLoop(id: string, definition: AgentLoopDefinition): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>(`/api/agent-loops/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(definition),
  });
}

export async function deleteAgentLoop(id: string): Promise<void> {
  await request<{ status: string }>(`/api/agent-loops/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function runAgentLoop(id: string): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>(`/api/agent-loops/${encodeURIComponent(id)}/run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ trigger: "manual" }),
  });
}

export async function pauseAgentLoop(id: string): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>(`/api/agent-loops/${encodeURIComponent(id)}/pause`, { method: "POST" });
}

export async function resumeAgentLoop(id: string): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>(`/api/agent-loops/${encodeURIComponent(id)}/resume`, { method: "POST" });
}

export async function resetAgentLoopSession(id: string): Promise<AgentLoopInfo> {
  return request<AgentLoopInfo>(`/api/agent-loops/${encodeURIComponent(id)}/reset-session`, { method: "POST" });
}

export async function listAgentLoopRuns(id: string): Promise<AgentLoopRunRecord[]> {
  return request<AgentLoopRunRecord[]>(`/api/agent-loops/${encodeURIComponent(id)}/runs`);
}

export async function getAgentLoopRun(id: string, runId: string): Promise<AgentLoopRunRecord> {
  return request<AgentLoopRunRecord>(`/api/agent-loops/${encodeURIComponent(id)}/runs/${encodeURIComponent(runId)}`);
}

export function voiceSocketUrl(): string {
  return socketUrl("/api/voice/ws");
}
