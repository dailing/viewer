import { RpcError } from "@viewer/bus-sdk";

export interface UploadBus {
  request(channel: string, value: unknown): Promise<unknown>;
}
interface UploadTicketReply { url: string }
export interface UploadResult { path: string; size: number }
export type UploadProgress = (loaded: number, total: number) => void;
export class UploadConflictError extends Error {
  constructor(path: string) {
    super(`目标已存在：${path}`);
    this.name = "UploadConflictError";
  }
}

async function requestUploadTicket(bus: UploadBus, dir: string, file: File, overwrite: boolean): Promise<UploadTicketReply> {
  try {
    return await bus.request("file:_:upload:ticket", { dir, name: file.name, overwrite }) as UploadTicketReply;
  } catch (cause) {
    if (cause instanceof RpcError && cause.code === "already_exists") throw new UploadConflictError(file.name);
    throw cause;
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
  const ticket = await requestUploadTicket(bus, dir, file, overwrite);
  const response = await fetchImpl(ticket.url, {
    method: "POST",
    headers: { "Content-Type": "application/octet-stream" },
    body: file,
  });
  if (response.status === 409) throw new UploadConflictError(file.name);
  if (!response.ok) throw new Error(`上传失败（HTTP ${response.status}）：${await response.text()}`);
  return await response.json() as UploadResult;
}

/** XHR exposes upload-byte progress; fetch does not consistently do so. */
export async function uploadFileWithProgress(
  bus: UploadBus,
  dir: string,
  file: File,
  overwrite: boolean,
  onProgress: UploadProgress,
  xhrFactory: () => XMLHttpRequest = () => new XMLHttpRequest(),
): Promise<UploadResult> {
  const ticket = await requestUploadTicket(bus, dir, file, overwrite);
  return await new Promise<UploadResult>((resolve, reject) => {
    const xhr = xhrFactory();
    xhr.open("POST", ticket.url);
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.upload.onprogress = (event) => {
      onProgress(event.loaded, event.lengthComputable ? event.total : file.size);
    };
    xhr.onerror = () => reject(new Error("上传连接中断"));
    xhr.onabort = () => reject(new Error("上传已中止"));
    xhr.onload = () => {
      if (xhr.status === 409) {
        reject(new UploadConflictError(file.name));
        return;
      }
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new Error(`上传失败（HTTP ${xhr.status}）：${xhr.responseText}`));
        return;
      }
      try {
        resolve(JSON.parse(xhr.responseText) as UploadResult);
      } catch {
        reject(new Error("上传响应格式无效"));
      }
    };
    xhr.send(file);
  });
}
