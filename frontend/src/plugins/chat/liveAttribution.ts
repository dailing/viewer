export type LiveVisibility = "visible" | "hidden" | "deferred";

/** Filtered views must wait for a turn's line instead of fail-open admitting
 * a frame into every branch. The unfiltered 全部 view remains immediate. */
export function liveTurnVisibility(
  lines: ReadonlyMap<string, string>,
  turnId: string,
  selectedBranchIds: string[] | null,
): LiveVisibility {
  if (selectedBranchIds === null) return "visible";
  const branchId = lines.get(turnId);
  if (branchId === undefined) return "deferred";
  return selectedBranchIds.includes(branchId) ? "visible" : "hidden";
}

export function drainDeferredTurn<T extends { turn_id: string }>(
  frames: Map<string, T>,
  turnId: string,
  admit: boolean,
): T[] {
  const accepted: T[] = [];
  for (const [id, value] of frames) {
    if (value.turn_id !== turnId) continue;
    frames.delete(id);
    if (admit) accepted.push(value);
  }
  return accepted;
}

/** Phases such as target carry no branch_id; they must not erase the
 * attribution learned from started/session/completed or a history seed. */
export function stampLiveTurnLine(
  lines: Map<string, string>,
  turnId: string | undefined,
  branchId: string | undefined,
): boolean {
  if (!turnId || branchId === undefined) return false;
  lines.set(turnId, branchId);
  return true;
}
