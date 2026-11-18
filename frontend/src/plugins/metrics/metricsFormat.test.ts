import { describe, expect, it } from "vitest";
import { dateRange, formatCompact, formatDuration, localDateKey } from "./metricsFormat";

describe("metrics formatting", () => {
  it("uses compact token counts without losing small values", () => {
    expect(formatCompact(999)).toBe("999");
    expect(formatCompact(1250)).toBe("1.3K");
    expect(formatCompact(1_250_000)).toBe("1.3M");
    expect(formatCompact(undefined)).toBe("0");
  });

  it("formats attempt and turn latencies", () => {
    expect(formatDuration(850)).toBe("850ms");
    expect(formatDuration(1500)).toBe("1.5s");
    expect(formatDuration(65_000)).toBe("1m 5s");
  });

  it("creates local inclusive date presets", () => {
    const now = new Date(2026, 10, 18, 12);
    expect(localDateKey(now)).toBe("2026-11-18");
    expect(dateRange(30, now)).toEqual({ from: "2026-10-20", to: "2026-11-18" });
  });
});
