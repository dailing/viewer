import type { DirectoryListing, FileMeta, TextLineWindow, UserProfile, ViewerConfig } from "../types/files";
import type { TerminalInfo, TerminalSnapshot } from "../types/terminals";
import type { CodexCliStatus, CodexModelOptions } from "../types/codex";
import type {
  SuperChatCreate,
  SuperChatList,
  SuperChatPatch,
  SuperDisplayItemsPage,
  SuperDispatchResponse,
  SuperHistoryRun,
  SuperHistoryRunCreate,
  SuperRoleCreate,
  SuperRolePatch,
  SuperRoleStatuses,
  SuperWorkspaceData,
  SuperWorkspaceList,
  SuperWorkspacePatch,
} from "../types/superWorkspace";
import type { AgentLoopDefinition, AgentLoopInfo, AgentLoopRunRecord } from "../types/agentLoops";
import type { AgentTask, AgentTaskContext, AgentTaskCreate, AgentTaskDependencyPatch, AgentTaskFile, AgentTaskGroup, AgentTaskListResponse, AgentTaskManagerRequest, AgentTaskPatch, AgentTaskPlan, AgentTaskResetAction, AgentTaskResetResponse, AgentTaskSettings } from "../types/agentTasks";
import type { AgentProvider, AgentProviderInfo } from "../types/agents";
import type { GitDiffText, GitStatus } from "../types/git";
import { currentUserId } from "../utils/userProfile";

async function request<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(withUser(url), options);
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || response.statusText);
  }
  return response.json() as Promise<T>;
}

function socketUrl(path: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}${withUser(path)}`;
}

function withUser(url: string): string {
  const user = currentUserId();
  if (!user || !url.startsWith("/api/")) return url;
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}user=${encodeURIComponent(user)}`;
}

export function rawUrl(path: string, contentHash?: string, base?: string): string {
  const hashQuery = contentHash ? `&h=${encodeURIComponent(contentHash)}` : "";
  const baseQuery = base !== undefined ? `&base=${encodeURIComponent(base)}` : "";
  return withUser(`/api/file/raw?path=${encodeURIComponent(path)}${hashQuery}${baseQuery}`);
}

function encodePathSegments(path: string): string {
  return path
    .split("/")
    .filter(Boolean)
    .map((part) => encodeURIComponent(part))
    .join("/");
}

export function siteUrl(path: string, contentHash?: string): string {
  if (path.startsWith("/")) {
    const params = new URLSearchParams({ path });
    if (contentHash) params.set("h", contentHash);
    return withUser(`/api/file/site?${params.toString()}`);
  }
  const hashQuery = contentHash ? `?h=${encodeURIComponent(contentHash)}` : "";
  return withUser(`/api/file/site/${encodePathSegments(path)}${hashQuery}`);
}

export async function listUsers(): Promise<UserProfile[]> {
  return request<UserProfile[]>("/api/users");
}

export async function getTree(path = ""): Promise<DirectoryListing> {
  return request<DirectoryListing>(`/api/tree?path=${encodeURIComponent(path)}`);
}

export async function getMeta(path: string): Promise<FileMeta> {
  return request<FileMeta>(`/api/file/meta?path=${encodeURIComponent(path)}`);
}

export async function getText(path: string): Promise<string> {
  const response = await fetch(withUser(`/api/file/content?path=${encodeURIComponent(path)}`));
  if (!response.ok) throw new Error(await response.text());
  return response.text();
}

export async function getTextLines(path: string, start: number, count: number): Promise<TextLineWindow> {
  return request<TextLineWindow>(
    `/api/file/text-lines?path=${encodeURIComponent(path)}&start=${encodeURIComponent(start)}&count=${encodeURIComponent(count)}`,
  );
}

export async function putText(path: string, content: string): Promise<FileMeta> {
  const response = await fetch(withUser(`/api/file/content?path=${encodeURIComponent(path)}`), {
    method: "PUT",
    headers: { "Content-Type": "text/plain; charset=utf-8" },
    body: content,
  });
  if (!response.ok) throw new Error(await response.text());
  return response.json() as Promise<FileMeta>;
}

export async function uploadFile(directory: string, file: File): Promise<void> {
  const query = `directory=${encodeURIComponent(directory)}&filename=${encodeURIComponent(file.name)}`;
  const response = await fetch(withUser(`/api/file/upload?${query}`), {
    method: "POST",
    headers: { "Content-Type": file.type || "application/octet-stream" },
    body: file,
  });
  if (!response.ok) throw new Error(await response.text());
}

export async function deleteFile(path: string): Promise<void> {
  const response = await fetch(withUser(`/api/file?path=${encodeURIComponent(path)}`), { method: "DELETE" });
  if (!response.ok) throw new Error(await response.text());
}

export async function resolveMarkdownLink(base: string, target: string): Promise<{ path: string; content_hash?: string }> {
  return request<{ path: string; content_hash?: string }>(
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

export async function getSuperWorkspace(): Promise<SuperWorkspaceData> {
  return request<SuperWorkspaceData>("/api/super-workspace");
}

export async function updateSuperWorkspace(patch: SuperWorkspacePatch): Promise<SuperWorkspaceData> {
  return request<SuperWorkspaceData>("/api/super-workspace", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function listSuperWorkspaces(): Promise<SuperWorkspaceList> {
  return request<SuperWorkspaceList>("/api/super-workspace/workspaces");
}

export async function activateSuperWorkspace(workspaceId: string): Promise<SuperWorkspaceList> {
  return request<SuperWorkspaceList>(`/api/super-workspace/active-workspace/${workspaceId}`, { method: "POST" });
}

export async function listSuperChats(): Promise<SuperChatList> {
  return request<SuperChatList>("/api/super-workspace/chats");
}

export async function createSuperChat(chat: SuperChatCreate): Promise<SuperChatList> {
  return request<SuperChatList>("/api/super-workspace/chats", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(chat),
  });
}

export async function updateSuperChat(id: string, patch: SuperChatPatch): Promise<SuperChatList> {
  return request<SuperChatList>(`/api/super-workspace/chats/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function deleteSuperChat(id: string): Promise<SuperChatList> {
  return request<SuperChatList>(`/api/super-workspace/chats/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function activateSuperChat(id: string): Promise<SuperChatList> {
  return request<SuperChatList>(`/api/super-workspace/active-chat/${encodeURIComponent(id)}`, { method: "POST" });
}

export async function getSuperRoleStatuses(workspaceId: string): Promise<SuperRoleStatuses> {
  return request<SuperRoleStatuses>(`/api/super-workspace/role-statuses/${workspaceId}`);
}

export async function createSuperRole(role: SuperRoleCreate): Promise<SuperWorkspaceData> {
  return request<SuperWorkspaceData>("/api/super-workspace/roles", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(role),
  });
}

export async function updateSuperRole(id: string, patch: SuperRolePatch): Promise<SuperWorkspaceData> {
  return request<SuperWorkspaceData>(`/api/super-workspace/roles/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function deleteSuperRole(id: string): Promise<SuperWorkspaceData> {
  return request<SuperWorkspaceData>(`/api/super-workspace/roles/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function listSuperWorkspaceRuns(limit = 30, before?: number, after?: number, chatId?: string | null): Promise<SuperDisplayItemsPage> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (before !== undefined) params.set("before", String(before));
  if (after !== undefined) params.set("after", String(after));
  if (chatId) params.set("chat_id", chatId);
  return request<SuperDisplayItemsPage>(`/api/super-workspace/runs?${params.toString()}`);
}

export async function createSuperWorkspaceRun(payload: SuperHistoryRunCreate): Promise<SuperHistoryRun> {
  return request<SuperHistoryRun>("/api/super-workspace/runs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
}

export async function dispatchSuperWorkspace(message: string, roleIds?: string[]): Promise<SuperDispatchResponse> {
  return request<SuperDispatchResponse>("/api/super-workspace/dispatch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, role_ids: roleIds }),
  });
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

export async function getAgentSession(provider: AgentProvider, id: string, detail: "focus" | "full" = "focus"): Promise<unknown> {
  return request<unknown>(
    `/api/agents/sessions/${encodeURIComponent(id)}?provider=${encodeURIComponent(provider)}&detail=${encodeURIComponent(detail)}`,
  );
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

export function agentSessionSocketUrl(provider: AgentProvider, id: string, detail: "focus" | "full" = "focus"): string {
  return socketUrl(`/api/agents/sessions/${encodeURIComponent(id)}/ws?provider=${encodeURIComponent(provider)}&detail=${encodeURIComponent(detail)}`);
}

export async function getCodexStatus(): Promise<CodexCliStatus> {
  return request<CodexCliStatus>("/api/codex/status");
}

export async function getCodexModels(): Promise<CodexModelOptions> {
  return request<CodexModelOptions>("/api/codex/models");
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

export async function listAgentTasks(groupId?: string, status?: string): Promise<AgentTaskListResponse> {
  const params = new URLSearchParams();
  if (groupId) params.set("group_id", groupId);
  if (status) params.set("status", status);
  const query = params.toString();
  return request<AgentTaskListResponse>(`/api/agent-tasks${query ? `?${query}` : ""}`);
}

export async function listAgentTaskGroups(): Promise<AgentTaskGroup[]> {
  return request<AgentTaskGroup[]>("/api/agent-tasks/groups");
}

export async function getAgentTask(id: string): Promise<AgentTask> {
  return request<AgentTask>(`/api/agent-tasks/${encodeURIComponent(id)}`);
}

export async function getAgentTaskContext(id: string): Promise<AgentTaskContext> {
  return request<AgentTaskContext>(`/api/agent-tasks/${encodeURIComponent(id)}/context`);
}

export async function listAgentTaskFiles(id: string): Promise<AgentTaskFile[]> {
  return request<AgentTaskFile[]>(`/api/agent-tasks/${encodeURIComponent(id)}/files`);
}

export async function createAgentTask(task: AgentTaskCreate): Promise<AgentTask> {
  return request<AgentTask>("/api/agent-tasks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(task),
  });
}

export async function patchAgentTask(id: string, patch: AgentTaskPatch): Promise<AgentTask> {
  return request<AgentTask>(`/api/agent-tasks/${encodeURIComponent(id)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function deleteAgentTask(id: string): Promise<void> {
  await request<{ status: string; task_id: string }>(`/api/agent-tasks/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function resetAgentTask(id: string, action: AgentTaskResetAction, reason: string): Promise<AgentTaskResetResponse> {
  return request<AgentTaskResetResponse>(`/api/agent-tasks/${encodeURIComponent(id)}/reset`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ action, reason }),
  });
}

export async function patchAgentTaskDependencies(id: string, patch: AgentTaskDependencyPatch): Promise<AgentTask> {
  return request<AgentTask>(`/api/agent-tasks/${encodeURIComponent(id)}/dependencies`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function dispatchAgentTask(id: string, force = false): Promise<AgentTask> {
  return request<AgentTask>(`/api/agent-tasks/${encodeURIComponent(id)}/dispatch`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ force }),
  });
}

export async function dispatchReadyAgentTasks(groupId?: string, force = false): Promise<{ dispatched: AgentTask[] }> {
  const params = new URLSearchParams();
  if (groupId) params.set("group_id", groupId);
  if (force) params.set("force", "true");
  const query = params.toString();
  return request<{ dispatched: AgentTask[] }>(`/api/agent-tasks/dispatch-ready${query ? `?${query}` : ""}`, { method: "POST" });
}

export async function getAgentTaskSettings(groupId = "default"): Promise<AgentTaskSettings> {
  return request<AgentTaskSettings>(`/api/agent-tasks/settings?group_id=${encodeURIComponent(groupId)}`);
}

export async function updateAgentTaskSettings(settings: Partial<AgentTaskSettings> & { default_group_id?: string; project_root?: string }): Promise<AgentTaskSettings> {
  return request<AgentTaskSettings>("/api/agent-tasks/settings", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
}

export async function getAgentTaskPlan(groupId = "default"): Promise<AgentTaskPlan> {
  return request<AgentTaskPlan>(`/api/agent-tasks/plan?group_id=${encodeURIComponent(groupId)}`);
}

export async function updateAgentTaskPlan(plan: Pick<AgentTaskPlan, "group_id" | "goal" | "plan" | "context" | "constraints"> & { reason?: string }): Promise<AgentTaskPlan> {
  return request<AgentTaskPlan>("/api/agent-tasks/plan", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(plan),
  });
}

export async function requestAgentTaskManager(requestBody: AgentTaskManagerRequest): Promise<{ manager_session_id: string; session: unknown }> {
  return request<{ manager_session_id: string; session: unknown }>("/api/agent-tasks/manager", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(requestBody),
  });
}

export function voiceSocketUrl(): string {
  return socketUrl("/api/voice/ws");
}
