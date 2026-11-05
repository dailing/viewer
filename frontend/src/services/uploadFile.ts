import { RpcError } from "@viewer/bus-sdk";

interface UploadBus {
  request(channel: string, value: unknown): Promise<unknown>;
}
interface UploadTicketReply { url: string }
export interface UploadResult { path: string; size: number }
export class UploadConflictError extends Error {
  constructor(path: string) {
    super(`目标已存在：${path}`);
    this.name = "UploadConflictError";
  }
}

/** Upload the original File bytes through the ticketed HTTP data plane. */
export async function uploadFile(
  bus: UploadBus,
  dir: string,
  file: File,
  overwrite = false,
  fetchImpl: typeof fetch = fetch,
): Promise<UploadResult> {
  let ticket: UploadTicketReply;
  try {
    ticket = await bus.request("file:_:upload:ticket", { dir, name: file.name, overwrite }) as UploadTicketReply;
  } catch (cause) {
    if (cause instanceof RpcError && cause.code === "already_exists") throw new UploadConflictError(file.name);
    throw cause;
  }
  const response = await fetchImpl(ticket.url, {
    method: "POST",
    headers: { "Content-Type": "application/octet-stream" },
    body: file,
  });
  if (response.status === 409) throw new UploadConflictError(file.name);
  if (!response.ok) throw new Error(`上传失败（HTTP ${response.status}）：${await response.text()}`);
  return await response.json() as UploadResult;
}
