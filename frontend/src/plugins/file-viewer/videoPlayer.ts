export const VIDEO_SKIP_SECONDS = 10;

/** Clamp a requested playback position to the valid local-file timeline. */
export function clampVideoTime(value: number, duration: number): number {
  if (!Number.isFinite(value)) return 0;
  const upper = Number.isFinite(duration) && duration > 0 ? duration : Number.POSITIVE_INFINITY;
  return Math.min(Math.max(0, value), upper);
}

/** Compact elapsed/duration label; hours appear only when needed. */
export function formatVideoTime(value: number): string {
  if (!Number.isFinite(value) || value < 0) return "0:00";
  const total = Math.floor(value);
  const seconds = String(total % 60).padStart(2, "0");
  const minutes = Math.floor(total / 60) % 60;
  if (total < 3600) return `${Math.floor(total / 60)}:${seconds}`;
  return `${Math.floor(total / 3600)}:${String(minutes).padStart(2, "0")}:${seconds}`;
}

export function videoErrorMessage(code: number | undefined): string {
  switch (code) {
    case 1: // MEDIA_ERR_ABORTED
      return "视频加载已中止，请重试。";
    case 2: // MEDIA_ERR_NETWORK
      return "视频加载失败，网络中断或文件链接已过期，请重试。";
    case 3: // MEDIA_ERR_DECODE
      return "视频解码失败，文件可能损坏或使用了浏览器不支持的编码。";
    case 4: // MEDIA_ERR_SRC_NOT_SUPPORTED
      return "无法加载视频：链接可能已过期，或浏览器不支持此格式/编码。MP4 推荐使用 H.264/AAC 编码。";
    default:
      return "视频加载失败，请重试。";
  }
}
