import { describe, expect, it } from "vitest";

import { clampVideoTime, formatVideoTime, VIDEO_SKIP_SECONDS, videoErrorMessage } from "./videoPlayer";

describe("video player helpers", () => {
  it("uses a ten-second skip interval", () => {
    expect(VIDEO_SKIP_SECONDS).toBe(10);
  });

  it("clamps seeks to the beginning and end", () => {
    expect(clampVideoTime(-4, 120)).toBe(0);
    expect(clampVideoTime(12.5, 120)).toBe(12.5);
    expect(clampVideoTime(130, 120)).toBe(120);
    expect(clampVideoTime(Number.NaN, 120)).toBe(0);
    expect(clampVideoTime(130, Number.NaN)).toBe(130);
  });

  it("formats short and hour-long timelines", () => {
    expect(formatVideoTime(0)).toBe("0:00");
    expect(formatVideoTime(65.9)).toBe("1:05");
    expect(formatVideoTime(3599)).toBe("59:59");
    expect(formatVideoTime(3600)).toBe("1:00:00");
    expect(formatVideoTime(3661)).toBe("1:01:01");
    expect(formatVideoTime(Number.NaN)).toBe("0:00");
  });

  it("explains browser media failures without requiring a DOM", () => {
    expect(videoErrorMessage(2)).toContain("链接已过期");
    expect(videoErrorMessage(3)).toContain("解码失败");
    expect(videoErrorMessage(4)).toContain("H.264/AAC");
    expect(videoErrorMessage(undefined)).toBe("视频加载失败，请重试。");
  });
});
