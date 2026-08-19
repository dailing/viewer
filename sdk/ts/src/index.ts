export { channelMatches } from "./matching.js";
export {
  BusClient,
  BusConnectionError,
  PROTOCOL_VERSION,
  RpcError,
  RpcTimeoutError,
} from "./client.js";
export { definePlugin } from "./plugin.js";
export type {
  BusClientOptions,
  BusErrorEntry,
  BusFrame,
  ErrorHandler,
  FrameHandler,
  Origin,
  PluginManifest,
  StateHandler,
} from "./client.js";
export type {
  DockInstance,
  DockProvider,
  PaneChrome,
  PaneChromeAction,
  PaneChromeControl,
  PluginComponent,
  PluginCtx,
  PluginModule,
  PluginPaneCtx,
} from "./plugin.js";
