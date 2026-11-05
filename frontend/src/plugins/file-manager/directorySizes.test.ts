import { describe, expect, it, vi } from "vitest";
import { DirectorySizes } from "./directorySizes";

const result = { path: "/a", size: 42, files: 2, directories: 1, errors: 0, complete: true };

describe("DirectorySizes", () => {
  it("calculates only on request and deduplicates concurrent clicks", async () => {
    const request = vi.fn(async () => result);
    const sizes = new DirectorySizes();
    expect(sizes.state("/a").status).toBe("idle");
    expect(request).not.toHaveBeenCalled();
    const first = sizes.calculate({ request }, "/a");
    const second = sizes.calculate({ request }, "/a");
    await Promise.all([first, second]);
    expect(request).toHaveBeenCalledOnce();
    expect(request).toHaveBeenCalledWith("file:_:du", { path: "/a" }, { timeout: 120_000 });
    expect(sizes.state("/a")).toEqual({ status: "ready", result });
  });

  it("keeps failures and partial results per directory", async () => {
    const sizes = new DirectorySizes();
    const partial = { ...result, errors: 2, complete: false };
    await sizes.calculate({ request: vi.fn(async () => partial) }, "/partial");
    await sizes.calculate({ request: vi.fn(async () => { throw new Error("denied"); }) }, "/error");
    expect(sizes.state("/partial").result?.complete).toBe(false);
    expect(sizes.state("/error")).toEqual({ status: "error", error: "denied" });
    expect(sizes.state("/other").status).toBe("idle");
  });
});
