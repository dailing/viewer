import { describe, expect, it, vi } from "vitest";
import type { BusClient, BusFrame } from "@viewer/bus-sdk";
import type { PluginShellApi } from "./ctx";
import { createProgrammaticShellHandler, syncProgrammaticShell } from "./programmatic";

function fixture() {
  const api: PluginShellApi = {
    open: vi.fn(async () => ({ paneId: "pane-1", instanceId: "task-view" })),
    panes: {
      list: vi.fn(() => [{ paneId: "pane-1", paneType: "chat", instanceId: "task-view", location: "tiled" as const, active: true }]),
      close: vi.fn(), focus: vi.fn(), move: vi.fn(),
    },
  };
  const publish = vi.fn(async (_channel: string, _value: unknown) => undefined);
  return { api, publish, handler: createProgrammaticShellHandler(api, publish) };
}
async function dispatched(publish: ReturnType<typeof vi.fn>): Promise<void> { await vi.waitFor(() => expect(publish).toHaveBeenCalled()); }

describe("programmatic Shell RPC", () => {
  it("opens a Chat/Task through the existing instance adapter", async () => {
    const { api, publish, handler } = fixture();
    handler({ channel: "shell:conn-a:open", value: { pane_type: "chat", params: { chatId: "c", initialTaskId: "t" }, _reply_to: "_inbox:x", _corr: "1" } } as BusFrame);
    await dispatched(publish);
    expect(api.open).toHaveBeenCalledWith({ paneType: "chat", instanceId: undefined, params: { chatId: "c", initialTaskId: "t" }, newPane: false });
    expect(publish).toHaveBeenCalledWith("_inbox:x", { _corr: "1", ok: true, result: { paneId: "pane-1", instanceId: "task-view" } });
  });

  it("uses the existing pane helpers for list, focus, move and close", async () => {
    const { api, publish, handler } = fixture();
    for (const [suffix, payload] of [["panes:list", {}], ["panes:focus", { pane_id: "pane-1" }], ["panes:move", { pane_id: "pane-1", delta: -1 }], ["panes:close", { pane_id: "pane-1" }]] as const) {
      handler({ channel: `shell:conn-a:${suffix}`, value: { ...payload, _reply_to: "_inbox:x", _corr: suffix } } as BusFrame);
    }
    await vi.waitFor(() => expect(publish).toHaveBeenCalledTimes(4));
    expect(api.panes.list).toHaveBeenCalledOnce();
    expect(api.panes.focus).toHaveBeenCalledWith("pane-1");
    expect(api.panes.move).toHaveBeenCalledWith("pane-1", -1);
    expect(api.panes.close).toHaveBeenCalledWith("pane-1");
  });

  it("returns a protocol error for malformed operations", async () => {
    const { api, publish, handler } = fixture();
    handler({ channel: "shell:conn-a:panes:move", value: { pane_id: "pane-1", delta: 2, _reply_to: "_inbox:x", _corr: "bad" } } as BusFrame);
    await dispatched(publish);
    expect(api.panes.move).not.toHaveBeenCalled();
    expect(publish.mock.calls[0]?.[1]).toMatchObject({ ok: false, error: { code: "shell_error" } });
  });

  it("unsubscribes the old browser connection before replaying a new one", () => {
    const subscribed: string[] = [];
    const unsubscribed: string[] = [];
    const client = {
      subscribe: vi.fn(async (channel: string) => { subscribed.push(channel); }),
      unsubscribe: vi.fn(async (channel: string) => { unsubscribed.push(channel); }),
      publish: vi.fn(async () => undefined),
    } as unknown as BusClient;
    syncProgrammaticShell(client, "conn-test-a");
    syncProgrammaticShell(client, "conn-test-b");
    syncProgrammaticShell(client, null);
    expect(subscribed).toContain("shell:conn-test-a:open");
    expect(subscribed).toContain("shell:conn-test-b:open");
    expect(unsubscribed).toContain("shell:conn-test-a:open");
    expect(unsubscribed).toContain("shell:conn-test-b:open");
  });
});
