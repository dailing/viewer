import type { PdfPageResult } from "./types";

/** Shared files-plugin data-plane helpers. No Vue component state. */
export interface FileReadResult {
  path: string;
  size: number;
  encoding: "utf-8" | "base64";
  content: string;
}

export interface TicketReply {
  url: string;
}

export interface FileRequestBus {
  request(channel: string, value: unknown): Promise<unknown>;
}

export async function fileTicketUrl(bus: FileRequestBus, path: string): Promise<string> {
  const reply = (await bus.request("file:_:ticket", { path })) as TicketReply;
  return reply.url;
}

export async function readFile(bus: FileRequestBus, path: string, maxBytes: number): Promise<FileReadResult> {
  return (await bus.request("file:_:read", { path, max_bytes: maxBytes })) as FileReadResult;
}

export interface PdfPageRequest {
  path: string;
  page: number;
  scale: number;
  trim_x?: number;
  trim_y?: number;
}

export async function readPdfPage(bus: FileRequestBus, request: PdfPageRequest): Promise<PdfPageResult> {
  return (await bus.request("file:_:pdfpage", request)) as PdfPageResult;
}

/** Stream an entire text file through the raw data plane, bypassing bus read
 * size limits. HTTP errors stay observable to the caller. */
export async function fetchFileText(bus: FileRequestBus, path: string, signal?: AbortSignal): Promise<string> {
  const url = await fileTicketUrl(bus, path);
  const response = await fetch(url, { signal });
  if (!response.ok) throw new Error(`读取失败（HTTP ${response.status}）`);
  return response.text();
}

export function base64ToBlob(content: string, mime: string): Blob {
  const binary = atob(content);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return new Blob([bytes], { type: mime });
}

/** Ownership scope for object URLs. Minted URLs are always revoked together,
 * including when their async producer completes after a re-render. */
export class BlobUrlOwner {
  private urls = new Set<string>();

  mint(blob: Blob): string {
    const url = URL.createObjectURL(blob);
    this.urls.add(url);
    return url;
  }

  revokeAll(): void {
    for (const url of this.urls) URL.revokeObjectURL(url);
    this.urls.clear();
  }
}
