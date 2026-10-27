export interface DraftRequestGuard {
  branchId: string;
  generation: number;
  revision: number;
}

/** A late draft RPC may update bookkeeping/UI only while the exact line and
 * local edit generation that issued it are still current. */
export function draftRequestCurrent(request: DraftRequestGuard, current: DraftRequestGuard): boolean {
  return request.branchId === current.branchId
    && request.generation === current.generation
    && request.revision === current.revision;
}
