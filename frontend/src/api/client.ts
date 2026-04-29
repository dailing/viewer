import type { DirectoryListing, FileMeta, ViewerConfig } from "../types/files";
import type { TerminalInfo, TerminalSnapshot } from "../types/terminals";
import type { CodexSessionInfo, CodexSessionSnapshot } from "../types/codex";

async function request<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(url, options);
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || response.statusText);
  }
  return response.json() as Promise<T>;
}

export function rawUrl(path: string, contentHash?: string): string {
  const hashQuery = contentHash ? `&h=${encodeURIComponent(contentHash)}` : "";
  return `/api/file/raw?path=${encodeURIComponent(path)}${hashQuery}`;
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
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/terminals/${encodeURIComponent(id)}/ws`;
}

export async function listCodexSessions(): Promise<CodexSessionInfo[]> {
  return request<CodexSessionInfo[]>("/api/codex/sessions");
}

export async function createCodexSession(prompt: string, cwd = ""): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>("/api/codex/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt, cwd }),
  });
}

export async function getCodexSession(id: string): Promise<CodexSessionSnapshot> {
  return request<CodexSessionSnapshot>(`/api/codex/sessions/${encodeURIComponent(id)}`);
}

export async function sendCodexMessage(id: string, prompt: string): Promise<CodexSessionInfo> {
  return request<CodexSessionInfo>(`/api/codex/sessions/${encodeURIComponent(id)}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ prompt }),
  });
}

export async function deleteCodexSession(id: string): Promise<void> {
  await request<{ status: string }>(`/api/codex/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export function codexSessionSocketUrl(id: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/codex/sessions/${encodeURIComponent(id)}/ws`;
}

export function voiceSocketUrl(): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}/api/voice/ws`;
}
