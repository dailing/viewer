export interface OptimisticSendState {
  text: string;
  sending: boolean;
  failed: string;
  branchIds: string[];
}

export interface OptimisticTurnState {
  dispatchId: string;
  roleId: string;
  branchId: string;
}

/** Whether any optimistic line is part of the selected view. null = 全部. */
export function optimisticLinesVisible(branchIds: string[], selected: string[] | null): boolean {
  return selected === null || branchIds.some((id) => selected.includes(id));
}

function sharesLine(left: string[], right: string[] | undefined): boolean {
  return right === undefined || left.some((id) => right.includes(id));
}

/** Reconcile the real user echo with its optimistic box. While dispatch is
 * still in flight the server may assign auto-branches before the RPC reply,
 * so an exact sending text match is enough; afterwards line ownership must
 * overlap, preventing one branch's echo from clearing another branch's box. */
export function settleOptimisticSend<T extends OptimisticSendState>(
  pending: T[],
  message: { text: string; branchIds?: string[] },
): T[] {
  const text = message.text.trim();
  const index = pending.findIndex((item) => item.text === text && (item.sending || sharesLine(item.branchIds, message.branchIds)));
  if (index < 0) return pending;
  return [...pending.slice(0, index), ...pending.slice(index + 1)];
}

/** Resolve only the matching dispatch when known; legacy message/block
 * echoes without a dispatch id fall back to the exact role × branch pair. */
export function resolveOptimisticTurn<T extends OptimisticTurnState>(
  pending: Map<string, T>,
  roleId: string | undefined,
  branchId: string | undefined,
  dispatchId = "",
): Map<string, T> {
  if (!roleId || (dispatchId === "" && branchId === undefined)) return pending;
  let changed = false;
  const next = new Map<string, T>();
  for (const [key, value] of pending) {
    const matches = dispatchId !== ""
      ? value.dispatchId === dispatchId && value.roleId === roleId
      : value.roleId === roleId && value.branchId === branchId;
    if (matches) changed = true;
    else next.set(key, value);
  }
  return changed ? next : pending;
}
