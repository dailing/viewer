import type { DirectoryListing, FileMeta, TextLineWindow, UserProfile, ViewerConfig } from "../types/files";
import type { TerminalInfo, TerminalSnapshot } from "../types/terminals";
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
  SuperWorkspaceData,
  SuperWorkspaceList,
  SuperWorkspacePatch,
} from "../types/superWorkspace";
import type { AgentProviderInfo } from "../types/agents";
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

export function voiceSocketUrl(): string {
  return socketUrl("/api/voice/ws");
}
