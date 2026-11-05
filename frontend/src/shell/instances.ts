import { useLayoutStore } from "../stores/layout";
import type { InstanceOpenRequest, OpenedInstance } from "./definePlugin";
import { componentRegistry, instanceRegistry } from "./registries";

/** Tiled and floating panes both keep an unpinned instance alive. */
export function hostedInstanceIds(paneType: string): Set<string> {
  const layout = useLayoutStore();
  const ids = new Set<string>();
  for (const pane of layout.panes) {
    if (pane.content?.paneType === paneType) ids.add(pane.content.instanceId);
  }
  for (const pane of layout.floating) {
    if (pane.content.paneType === paneType) ids.add(pane.content.instanceId);
  }
  return ids;
}

/**
 * Browser-local instance opening. The plugin adapter resolves/creates the
 * instance first; only then does Shell mutate pane placement.
 */
export async function openShellInstance<P = unknown>(
  request: InstanceOpenRequest<P>,
): Promise<OpenedInstance> {
  if (!componentRegistry.has(request.paneType)) {
    throw new Error(`unknown pane type: ${request.paneType}`);
  }
  const adapter = instanceRegistry.get(request.paneType);
  let instanceId: string;
  if (adapter === undefined) {
    if (request.params !== undefined) {
      throw new Error(`pane type ${request.paneType} requires an instance adapter for params`);
    }
    instanceId = request.instanceId ?? "main";
  } else {
    if (adapter.cardinality === "singleton") {
      if (request.instanceId !== undefined && request.instanceId !== "main") {
        throw new Error(`singleton pane type ${request.paneType} only opens main`);
      }
      request = { ...request, instanceId: "main" };
    }
    instanceId = await adapter.open(request as InstanceOpenRequest);
    if (instanceId === "") throw new Error(`instance adapter ${request.paneType} returned an empty id`);
    if (adapter.cardinality === "singleton" && instanceId !== "main") {
      throw new Error(`singleton adapter ${request.paneType} must return main`);
    }
  }
  const paneId = useLayoutStore().openInstance(request.paneType, instanceId, {
    newPane: request.newPane,
  });
  return { paneId, instanceId };
}
