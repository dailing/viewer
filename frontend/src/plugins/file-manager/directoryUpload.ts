import { RpcError } from "@viewer/bus-sdk";

export type UploadTreeEntry =
  | { kind: "file"; path: string; file: File }
  | { kind: "directory"; path: string };

export type DroppedItem = FileSystemEntry | File;

interface DirectoryBus {
  request(channel: string, value: unknown): Promise<unknown>;
}

/** Capture entries synchronously: DataTransferItem objects expire after drop. */
export function captureDroppedItems(dataTransfer: DataTransfer): DroppedItem[] {
  const captured: DroppedItem[] = [];
  for (const item of Array.from(dataTransfer.items)) {
    if (item.kind !== "file") continue;
    const entry = item.webkitGetAsEntry();
    if (entry !== null) {
      captured.push(entry);
      continue;
    }
    const file = item.getAsFile();
    if (file !== null) captured.push(file);
  }
  if (captured.length > 0) return captured;
  return Array.from(dataTransfer.files);
}

export async function scanDroppedItems(items: DroppedItem[]): Promise<UploadTreeEntry[]> {
  const result: UploadTreeEntry[] = [];
  for (const item of items) {
    if (item instanceof File) {
      result.push({ kind: "file", path: item.name, file: item });
    } else {
      await visitEntry(item, "", result);
    }
  }
  return result;
}

async function visitEntry(entry: FileSystemEntry, parent: string, result: UploadTreeEntry[]): Promise<void> {
  const depth = parent === "" ? 0 : parent.split("/").length;
  if (depth >= 100) throw new Error(`目录层级过深，已停止遍历：${parent}`);
  const path = parent === "" ? entry.name : `${parent}/${entry.name}`;
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => {
      (entry as FileSystemFileEntry).file(resolve, reject);
    });
    result.push({ kind: "file", path, file });
    return;
  }
  if (!entry.isDirectory) throw new Error(`不支持的拖放条目：${path}`);
  // Include the directory itself so empty directories survive the upload.
  result.push({ kind: "directory", path });
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  // Chromium returns at most one implementation-defined batch per call. Keep
  // reading until an empty batch instead of silently dropping later children.
  for (;;) {
    const children = await new Promise<FileSystemEntry[]>((resolve, reject) => {
      reader.readEntries(resolve, reject);
    });
    if (children.length === 0) return;
    for (const child of children) await visitEntry(child, path, result);
  }
}

export function joinUploadPath(dir: string, name: string): string {
  if (dir === "") return name;
  return dir.endsWith("/") ? `${dir}${name}` : `${dir}/${name}`;
}

/** Create one directory, or merge into it when the existing path is a directory. */
export async function ensureDirectory(bus: DirectoryBus, dir: string, name: string): Promise<void> {
  try {
    await bus.request("file:_:mkdir", { dir, name });
  } catch (cause) {
    if (!(cause instanceof RpcError) || cause.code !== "already_exists") throw cause;
    // mkdir deliberately reports collisions. A list request verifies that the
    // collision is a directory; an existing file still fails as not_directory.
    await bus.request("file:_:list", { path: joinUploadPath(dir, name) });
  }
}
