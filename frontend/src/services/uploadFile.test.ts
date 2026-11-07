import { describe, expect, it, vi } from "vitest";
import { RpcError } from "@viewer/bus-sdk";
import { uploadFile, uploadFileWithProgress, UploadConflictError } from "./uploadFile";

describe("uploadFile", () => {
  it("requests a scoped ticket and posts the raw File body", async () => {
    const file = new File(["raw-bytes"], "a.bin", { type: "application/x-test" });
    const request = vi.fn(async () => ({ url: "/api/files/upload?ticket=signed" }));
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ path: "/tmp/a.bin", size: 9 }), { status: 201 }));
    await expect(uploadFile({ request }, "/tmp", file, false, fetchMock)).resolves.toEqual({ path: "/tmp/a.bin", size: 9 });
    expect(request).toHaveBeenCalledWith("file:_:upload:ticket", { dir: "/tmp", name: "a.bin", overwrite: false });
    const [, init] = fetchMock.mock.calls[0];
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe(file);
    expect(init?.headers).toEqual({ "Content-Type": "application/octet-stream" });
  });

  it("only requests an overwrite ticket when explicitly authorized", async () => {
    const file = new File(["new"], "a.txt");
    const request = vi.fn(async (_channel: string, value: any) => {
      if (!value.overwrite) throw new RpcError("already_exists", "exists");
      return { url: "/upload?overwrite" };
    });
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ path: "/a.txt", size: 3 }), { status: 201 }));
    await expect(uploadFile({ request }, "/", file, false, fetchMock)).rejects.toBeInstanceOf(UploadConflictError);
    expect(fetchMock).not.toHaveBeenCalled();
    await uploadFile({ request }, "/", file, true, fetchMock);
    expect(request).toHaveBeenLastCalledWith("file:_:upload:ticket", { dir: "/", name: "a.txt", overwrite: true });
  });

  it("maps HTTP collisions and surfaces failures", async () => {
    const file = new File(["x"], "a.txt");
    const bus = { request: vi.fn(async () => ({ url: "/upload" })) };
    await expect(uploadFile(bus, "/", file, false, vi.fn(async () => new Response("exists", { status: 409 })))).rejects.toBeInstanceOf(UploadConflictError);
    await expect(uploadFile(bus, "/", file, false, vi.fn(async () => new Response("denied", { status: 403 })))).rejects.toThrow("HTTP 403");
  });

  it("reports real XHR upload bytes while posting the original File", async () => {
    const file = new File(["123456789"], "a.bin");
    const bus = { request: vi.fn(async () => ({ url: "/upload?ticket=signed" })) };
    const xhr = {
      upload: { onprogress: null as ((event: ProgressEvent) => void) | null },
      open: vi.fn(),
      setRequestHeader: vi.fn(),
      send: vi.fn(() => {
        xhr.upload.onprogress?.({ loaded: 4, total: 9, lengthComputable: true } as ProgressEvent);
        xhr.status = 201;
        xhr.responseText = JSON.stringify({ path: "/a.bin", size: 9 });
        xhr.onload?.({ type: "load" } as ProgressEvent);
      }),
      onload: null as ((event: ProgressEvent) => void) | null,
      onerror: null,
      onabort: null,
      status: 0,
      responseText: "",
    };
    const onProgress = vi.fn();
    await expect(uploadFileWithProgress(
      bus,
      "/",
      file,
      false,
      onProgress,
      () => xhr as unknown as XMLHttpRequest,
    )).resolves.toEqual({ path: "/a.bin", size: 9 });
    expect(xhr.open).toHaveBeenCalledWith("POST", "/upload?ticket=signed");
    expect(xhr.send).toHaveBeenCalledWith(file);
    expect(onProgress).toHaveBeenCalledWith(4, 9);
  });
});
