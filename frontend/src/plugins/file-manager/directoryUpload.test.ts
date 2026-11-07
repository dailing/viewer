import { describe, expect, it, vi } from "vitest";
import { RpcError } from "@viewer/bus-sdk";
import { ensureDirectory, joinUploadPath, scanDroppedItems } from "./directoryUpload";

function fileEntry(name: string, contents = name): FileSystemFileEntry {
  return {
    isFile: true,
    isDirectory: false,
    name,
    file: (resolve: (file: File) => void) => resolve(new File([contents], name)),
  } as FileSystemFileEntry;
}

function directoryEntry(name: string, batches: FileSystemEntry[][]): FileSystemDirectoryEntry {
  const remaining = [...batches, []];
  const reader = {
    readEntries: (resolve: (entries: FileSystemEntry[]) => void) => resolve(remaining.shift() ?? []),
  };
  return {
    isFile: false,
    isDirectory: true,
    name,
    createReader: () => reader,
  } as FileSystemDirectoryEntry;
}

describe("directoryUpload", () => {
  it("rebuilds nested paths, reads every directory batch and keeps empty directories", async () => {
    const root = directoryEntry("root", [
      [fileEntry("first.txt"), directoryEntry("empty", [])],
      [directoryEntry("nested", [[fileEntry("second.txt")]])],
    ]);
    const result = await scanDroppedItems([root]);
    expect(result.map((entry) => `${entry.kind}:${entry.path}`)).toEqual([
      "directory:root",
      "file:root/first.txt",
      "directory:root/empty",
      "directory:root/nested",
      "file:root/nested/second.txt",
    ]);
    const second = result[4];
    expect(second.kind).toBe("file");
    if (second.kind === "file") expect(await second.file.text()).toBe("second.txt");
  });

  it("accepts ordinary files alongside directory entries", async () => {
    const plain = new File(["plain"], "plain.txt");
    const result = await scanDroppedItems([plain, fileEntry("entry.txt")]);
    expect(result.map((entry) => entry.path)).toEqual(["plain.txt", "entry.txt"]);
    expect(result.every((entry) => entry.kind === "file")).toBe(true);
  });

  it("creates directories and merges only into an existing directory", async () => {
    const request = vi.fn(async (channel: string) => {
      if (channel === "file:_:mkdir") throw new RpcError("already_exists", "exists");
      return { entries: [] };
    });
    await ensureDirectory({ request }, "/tmp", "root");
    expect(request.mock.calls).toEqual([
      ["file:_:mkdir", { dir: "/tmp", name: "root" }],
      ["file:_:list", { path: "/tmp/root" }],
    ]);
  });

  it("does not hide mkdir failures other than collisions", async () => {
    const request = vi.fn(async () => { throw new RpcError("write_error", "denied"); });
    await expect(ensureDirectory({ request }, "", "root")).rejects.toThrow("denied");
    expect(request).toHaveBeenCalledTimes(1);
    expect(joinUploadPath("", "root")).toBe("root");
    expect(joinUploadPath("/", "root")).toBe("/root");
  });
});
