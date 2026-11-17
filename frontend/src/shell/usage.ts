export interface UsageWindow {
  id: string;
  used_percent: number;
  reset_at?: string;
  window_seconds: number;
}

export interface UsageAccount {
  id: string;
  name: string;
  provider: "kimi" | "codex" | string;
  model?: string;
  plan?: string;
  active?: boolean;
  fetched_at: string;
  error?: string;
  windows: UsageWindow[];
}

export interface KimiUsageReply {
  active_profile_id?: string;
  accounts?: UsageAccount[];
}

export type PaceTone = "calm" | "balanced" | "fast" | "exhausted";

export interface UsagePace {
  elapsedPercent: number;
  deltaPoints: number;
  tone: PaceTone;
}

export function accountWindow(account: UsageAccount | null | undefined, id: string): UsageWindow | null {
  return account?.windows.find((window) => window.id === id) ?? null;
}

/** Compare consumption with elapsed cycle time only when reset_at and the
 * declared duration describe a plausible current cycle. */
export function usagePace(window: UsageWindow | null | undefined, nowMs: number): UsagePace | null {
  if (window === null || window === undefined || !window.reset_at || window.window_seconds <= 0) return null;
  const resetMs = Date.parse(window.reset_at);
  if (!Number.isFinite(resetMs)) return null;
  const remainingSeconds = (resetMs - nowMs) / 1000;
  // A reset further away than one full declared window is not a reliable
  // fixed cycle boundary; show remaining quota without a pace marker.
  if (remainingSeconds > window.window_seconds + 60) return null;
  const elapsed = Math.min(1, Math.max(0, 1 - remainingSeconds / window.window_seconds));
  const used = Math.min(100, Math.max(0, window.used_percent));
  const deltaPoints = used - elapsed * 100;
  const tone: PaceTone = used >= 100 ? "exhausted" : deltaPoints < -10 ? "calm" : deltaPoints > 10 ? "fast" : "balanced";
  return { elapsedPercent: elapsed * 100, deltaPoints, tone };
}

export function paceLabel(pace: UsagePace | null): string {
  switch (pace?.tone) {
    case "calm": return "从容";
    case "fast": return "偏快";
    case "exhausted": return "已用尽";
    case "balanced": return "均衡";
    default: return "";
  }
}

export function selectKimiAccount(
  accounts: UsageAccount[],
  activeID: string,
  preference: string,
): UsageAccount | null {
  if (preference !== "active") {
    const pinned = accounts.find((account) => account.id === preference);
    if (pinned !== undefined) return pinned;
  }
  return accounts.find((account) => account.id === activeID) ?? accounts.find((account) => account.active) ?? accounts[0] ?? null;
}

export function shortAccountName(account: UsageAccount): string {
  return account.name.replace(/[（(]云端[)）]/g, "").trim();
}

export function remainingPercent(window: UsageWindow): number {
  return Math.round(Math.min(100, Math.max(0, 100 - window.used_percent)));
}

export function formatCountdown(resetAt: string | undefined, nowMs: number): string {
  if (resetAt === undefined || resetAt === "") return "重置时间未知";
  const resetMs = Date.parse(resetAt);
  if (!Number.isFinite(resetMs)) return "重置时间未知";
  const seconds = Math.max(0, Math.floor((resetMs - nowMs) / 1000));
  if (seconds === 0) return "已到重置时间";
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  if (days > 0) return `${days}天${hours % 24}小时后重置`;
  if (hours > 0) return `${hours}小时${minutes % 60}分后重置`;
  if (minutes > 0) return `${minutes}分钟后重置`;
  return "1分钟内重置";
}

export function paceExplanation(pace: UsagePace): string {
  const points = Math.round(Math.abs(pace.deltaPoints));
  if (pace.tone === "calm") return `余量比均匀使用多 ${points} 个百分点`;
  if (pace.tone === "fast") return `用量比时间进度超前 ${points} 个百分点`;
  if (pace.tone === "exhausted") return "当前周期额度已用尽";
  return "消耗与时间进度基本匹配";
}
