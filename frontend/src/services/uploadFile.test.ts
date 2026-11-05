import { describe, expect, it, vi } from "vitest";
import { RpcError } from "@viewer/bus-sdk";
import { uploadFile, UploadConflictError } from "./uploadFile";

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
});
