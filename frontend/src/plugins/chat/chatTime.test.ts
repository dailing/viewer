import { describe, expect, it } from "vitest";
import { formatChatTime } from "./chatTime";

const timeOptions: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit" };
const dateOptions: Intl.DateTimeFormatOptions = { year: "numeric", month: "2-digit", day: "2-digit" };

describe("formatChatTime", () => {
  const now = new Date(2026, 9, 27, 12, 0);

  it("shows only the time for a message from today", () => {
    const date = new Date(2026, 9, 27, 8, 5);
    expect(formatChatTime(date.getTime(), now)).toBe(date.toLocaleTimeString([], timeOptions));
  });

  it("shows the full date and time for yesterday and older messages", () => {
    for (const date of [new Date(2026, 9, 26, 23, 59), new Date(2025, 0, 2, 8, 5)]) {
      const expected = `${date.toLocaleDateString([], dateOptions)} ${date.toLocaleTimeString([], timeOptions)}`;
      expect(formatChatTime(date.getTime(), now)).toBe(expected);
    }
  });
});
