import { afterEach, describe, expect, it, vi } from "vitest";
import { base64ToBlob, BlobUrlOwner, fetchFileText, fileTicketUrl, readFile, readPdfPage } from "./fileData";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("fileData", () => {
  it("decodes base64 with the supplied MIME type", async () => {
    const blob = base64ToBlob("aGVsbG8=", "text/plain");
    expect(blob.type).toBe("text/plain");
    expect(await blob.text()).toBe("hello");
  });

  it("uses a ticket and the raw HTTP data plane for full text", async () => {
    const request = vi.fn(async () => ({ url: "/raw?ticket=abc" }));
    vi.stubGlobal("fetch", vi.fn(async () => new Response("notebook text")));
    await expect(fetchFileText({ request }, "/tmp/a.ipynb")).resolves.toBe("notebook text");
    expect(request).toHaveBeenCalledWith("file:_:ticket", { path: "/tmp/a.ipynb" });
    await expect(fileTicketUrl({ request }, "/tmp/a.ipynb")).resolves.toBe("/raw?ticket=abc");
  });

  it("surfaces raw HTTP failures", async () => {
    const request = vi.fn(async () => ({ url: "/raw" }));
    vi.stubGlobal("fetch", vi.fn(async () => new Response("no", { status: 404 })));
    await expect(fetchFileText({ request }, "/missing")).rejects.toThrow("HTTP 404");
  });

  it("centralizes read and PDF-page RPC payload shapes", async () => {
    const request = vi.fn(async (channel: string) => channel === "file:_:read"
      ? { path: "/paper/main.tex", size: 4, encoding: "utf-8", content: "body" }
      : { path: "/paper/main.pdf", page: 2, pages: 3, mtime: 1, page_width: 10, page_height: 20, image_width: 10, image_height: 20, dpi: 72, mime: "image/webp", encoding: "base64", content: "eA==" });
    const bus = { request };
    await expect(readFile(bus, "/paper/main.tex", 1024)).resolves.toMatchObject({ content: "body" });
    await expect(readPdfPage(bus, { path: "/paper/main.pdf", page: 2, scale: 2, trim_x: 100, trim_y: 0 })).resolves.toMatchObject({ pages: 3 });
    expect(request).toHaveBeenNthCalledWith(1, "file:_:read", { path: "/paper/main.tex", max_bytes: 1024 });
    expect(request).toHaveBeenNthCalledWith(2, "file:_:pdfpage", { path: "/paper/main.pdf", page: 2, scale: 2, trim_x: 100, trim_y: 0 });
  });

  it("revokes every object URL owned by a render scope", () => {
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:test");
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    const owner = new BlobUrlOwner();
    expect(owner.mint(new Blob(["x"]))).toBe("blob:test");
    owner.revokeAll();
    owner.revokeAll();
    expect(create).toHaveBeenCalledOnce();
    expect(revoke).toHaveBeenCalledOnce();
    expect(revoke).toHaveBeenCalledWith("blob:test");
  });
});
