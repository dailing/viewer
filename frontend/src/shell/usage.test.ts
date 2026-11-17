import { describe, expect, it } from "vitest";

import {
  formatCountdown,
  remainingPercent,
  selectKimiAccount,
  usagePace,
  type UsageAccount,
  type UsageWindow,
} from "./usage";

const now = Date.parse("2026-11-16T12:00:00Z");

function window(used: number): UsageWindow {
  return { id: "7d", used_percent: used, window_seconds: 604800, reset_at: "2026-11-20T00:00:00Z" };
}

describe("usage pace", () => {
  it("compares used quota with elapsed cycle time", () => {
    expect(usagePace(window(38), now)).toEqual({ elapsedPercent: 50, deltaPoints: -12, tone: "calm" });
    expect(usagePace(window(65), now)?.tone).toBe("fast");
    expect(usagePace(window(55), now)?.tone).toBe("balanced");
    expect(usagePace(window(100), now)?.tone).toBe("exhausted");
  });

  it("does not invent a marker for missing or implausible cycle boundaries", () => {
    expect(usagePace({ ...window(20), reset_at: undefined }, now)).toBeNull();
    expect(usagePace({ ...window(20), reset_at: "not-a-date" }, now)).toBeNull();
    expect(usagePace({ ...window(20), reset_at: "2026-12-01T12:00:00Z" }, now)).toBeNull();
  });

  it("clamps elapsed progress around reset", () => {
    expect(usagePace({ ...window(20), reset_at: "2026-11-16T11:00:00Z" }, now)?.elapsedPercent).toBe(100);
    expect(remainingPercent(window(12.4))).toBe(88);
  });
});

describe("Kimi account selection", () => {
  const accounts = [
    { id: "kimi-dl", name: "Kimi DL", provider: "kimi", fetched_at: "", windows: [] },
    { id: "kimi-sy", name: "Kimi SY", provider: "kimi", fetched_at: "", windows: [] },
  ] satisfies UsageAccount[];

  it("follows active by default and supports a fixed account", () => {
    expect(selectKimiAccount(accounts, "kimi-sy", "active")?.id).toBe("kimi-sy");
    expect(selectKimiAccount(accounts, "kimi-sy", "kimi-dl")?.id).toBe("kimi-dl");
    expect(selectKimiAccount(accounts, "kimi-sy", "deleted")?.id).toBe("kimi-sy");
  });
});

describe("usage countdown", () => {
  it("uses compact Chinese durations", () => {
    expect(formatCountdown("2026-11-19T07:00:00Z", now)).toBe("2天19小时后重置");
    expect(formatCountdown("2026-11-16T13:40:00Z", now)).toBe("1小时40分后重置");
    expect(formatCountdown("2026-11-16T12:40:00Z", now)).toBe("40分钟后重置");
    expect(formatCountdown(undefined, now)).toBe("重置时间未知");
  });
});
