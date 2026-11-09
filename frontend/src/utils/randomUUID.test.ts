import { afterEach, describe, expect, it, vi } from "vitest";
import { randomUUID } from "./randomUUID";

afterEach(() => vi.unstubAllGlobals());

describe("randomUUID", () => {
  it("uses the native API when available", () => {
    vi.stubGlobal("crypto", { randomUUID: () => "native-id" });
    expect(randomUUID()).toBe("native-id");
  });

  it("builds an RFC 4122 UUID without randomUUID on plain HTTP", () => {
    vi.stubGlobal("crypto", {
      getRandomValues: (bytes: Uint8Array) => { bytes.fill(0); return bytes; },
    });
    expect(randomUUID()).toBe("00000000-0000-4000-8000-000000000000");
  });

  it("retains a UUID-shaped fallback when crypto is unavailable", () => {
    vi.stubGlobal("crypto", undefined);
    expect(randomUUID()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });
});
