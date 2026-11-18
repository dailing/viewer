export function formatCompact(value: number | null | undefined): string {
  const number = Number(value ?? 0);
  if (!Number.isFinite(number)) return "0";
  const absolute = Math.abs(number);
  if (absolute >= 1_000_000_000) return `${(number / 1_000_000_000).toFixed(absolute >= 10_000_000_000 ? 0 : 1)}B`;
  if (absolute >= 1_000_000) return `${(number / 1_000_000).toFixed(absolute >= 10_000_000 ? 0 : 1)}M`;
  if (absolute >= 1_000) return `${(number / 1_000).toFixed(absolute >= 10_000 ? 0 : 1)}K`;
  return Math.round(number).toLocaleString();
}

export function formatDuration(milliseconds: number | null | undefined): string {
  const value = Math.max(0, Number(milliseconds ?? 0));
  if (value < 1000) return `${Math.round(value)}ms`;
  if (value < 60_000) return `${(value / 1000).toFixed(1)}s`;
  if (value < 3_600_000) return `${Math.floor(value / 60_000)}m ${Math.round(value % 60_000 / 1000)}s`;
  return `${Math.floor(value / 3_600_000)}h ${Math.round(value % 3_600_000 / 60_000)}m`;
}

export function localDateKey(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function dateRange(days: number, now = new Date()): { from: string; to: string } {
  const to = localDateKey(now);
  const start = new Date(now);
  start.setDate(start.getDate() - (days - 1));
  return { from: localDateKey(start), to };
}
