import type { PluginCtx } from "../../shell/ctx";
import { getInstance, updateState } from "./instanceStore";

/** Reuse the manager's paired viewer; the first file creates it beside the
 * manager. The viewer adapter also recreates a previously pruned record. */
export async function openInPairedViewer(ctx: PluginCtx, managerId: string, path: string): Promise<void> {
  const viewerId = getInstance(managerId)?.state.viewerId ?? undefined;
  const opened = await ctx.shell.open({
    paneType: "file-viewer",
    instanceId: viewerId,
    params: { path },
    newPane: true,
  });
  updateState(managerId, { viewerId: opened.instanceId });
}
