import { describe, expect, it } from "vitest";
import { formatBytes } from "./formatBytes";

describe("formatBytes", () => {
  it("formats zero and byte boundaries", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1 KB");
    expect(formatBytes(1024 ** 2)).toBe("1 MB");
  });
  it("keeps useful fractions and rounds larger values", () => {
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(10 * 1024)).toBe("10 KB");
    expect(formatBytes(5 * 1024 ** 3 + 512 * 1024 ** 2)).toBe("5.5 GB");
  });
  it("supports large values and rejects invalid input", () => {
    expect(formatBytes(1024 ** 5)).toBe("1 PB");
    expect(formatBytes(1024 ** 6)).toBe("1024 PB");
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(Number.NaN)).toBe("0 B");
  });
});
