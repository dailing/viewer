const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

/** Human-readable binary byte count; fractions are kept only below 10. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  if (unit === 0) return `${Math.round(value)} B`;
  const rendered = value < 10 ? (Math.round(value * 10) / 10).toFixed(1).replace(/\.0$/, "") : Math.round(value).toString();
  return `${rendered} ${UNITS[unit]}`;
}
