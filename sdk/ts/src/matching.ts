/**
 * Channel pattern matching (protocol spec section 4.2).
 *
 * Duplicated from the kernel on purpose: the SDK must stay extractable as a
 * standalone package without importing kernel internals. The kernel remains
 * the single validation authority — this is the local dispatch mirror only.
 */
export function channelMatches(pattern: string, channel: string): boolean {
  if (pattern === ">") return true;
  const patternFields = pattern.split(":");
  const channelFields = channel.split(":");
  if (patternFields.length > channelFields.length) return false;
  return patternFields.every(
    (field, index) => field === "*" || field === channelFields[index],
  );
}
