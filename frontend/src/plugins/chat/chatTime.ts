const timeOptions: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit" };
const dateOptions: Intl.DateTimeFormatOptions = { year: "numeric", month: "2-digit", day: "2-digit" };

/** Today's timeline rows stay compact; older rows need their calendar date. */
export function formatChatTime(ms: number, now = new Date()): string {
  const date = new Date(ms);
  const time = date.toLocaleTimeString([], timeOptions);
  const sameDay = date.getFullYear() === now.getFullYear()
    && date.getMonth() === now.getMonth()
    && date.getDate() === now.getDate();
  return sameDay ? time : `${date.toLocaleDateString([], dateOptions)} ${time}`;
}
