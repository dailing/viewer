import { describe, expect, it } from "vitest";
import { snapshotInputSession, beginQueuedInputEdit, type InputSession } from "../../stores/inputSessionState";
import { chatInputRPC } from "./inputDispatch";

function session(): InputSession {
  return {
    id: "chat:c#two", pluginId: "chat", paneType: "chat", instanceId: "c#two", label: "Chat",
    text: "hello", branchId: "branch-a", mode: "dispatch", queuedDispatchId: "", revision: 0,
    selectedRoleIds: ["r1"], forceNewSession: false, parallel: false, pinned: true,
    phase: "idle", error: "", updatedAt: 0,
  };
}

describe("pane-independent chat input RPC", () => {
  it("retains branch and raw mode after the pane is gone", () => {
    const request = chatInputRPC(snapshotInputSession(session()), true);
    expect(request.channel).toBe("chat:_:dispatch");
    expect(request.payload).toMatchObject({ chat_id: "c", branch_id: "branch-a", raw: true });
  });

  it("keeps a closed pane's queued edit as an update, not a new dispatch", () => {
    const value = session();
    beginQueuedInputEdit(value, "branch-a", "dispatch-1", "edited");
    const request = chatInputRPC(snapshotInputSession(value), false);
    expect(request.channel).toBe("chat:_:queued-update");
    expect(request.payload).toEqual({ chat_id: "c", dispatch_id: "dispatch-1", message: "edited" });
    expect(request.payload).not.toHaveProperty("branch_id");
  });
});
