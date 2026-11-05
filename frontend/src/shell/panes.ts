import { useLayoutStore } from "../stores/layout";

export interface ShellPaneInfo {
  paneId: string;
  paneType: string;
  instanceId: string;
  location: "tiled" | "floating";
  active: boolean;
}

/** Snapshot of both layout containers, never a live mutable layout reference. */
export function listShellPanes(): ShellPaneInfo[] {
  const layout = useLayoutStore();
  const tiled = layout.panes.flatMap((pane) => pane.content === null ? [] : [{
    paneId: pane.id,
    paneType: pane.content.paneType,
    instanceId: pane.content.instanceId,
    location: "tiled" as const,
    active: layout.activePaneId === pane.id,
  }]);
  const floating = layout.floating.map((pane) => ({
    paneId: pane.id,
    paneType: pane.content.paneType,
    instanceId: pane.content.instanceId,
    location: "floating" as const,
    active: false,
  }));
  return [...tiled, ...floating];
}

export function closeShellPane(paneId: string): void {
  const layout = useLayoutStore();
  if (layout.floating.some((pane) => pane.id === paneId)) layout.closeFloating(paneId);
  else layout.closePane(paneId);
}

export function focusShellPane(paneId: string): void {
  const layout = useLayoutStore();
  if (layout.floating.some((pane) => pane.id === paneId)) layout.raiseFloating(paneId);
  else if (layout.panes.some((pane) => pane.id === paneId)) layout.setActivePane(paneId);
}

export function moveShellPane(paneId: string, delta: -1 | 1): void {
  const layout = useLayoutStore();
  if (layout.panes.some((pane) => pane.id === paneId)) layout.movePane(paneId, delta);
}
