import { afterEach, describe, expect, it, vi } from "vitest";
import { snapshotInputSession, beginQueuedInputEdit, setInputSessionRaw, type InputSession } from "../../stores/inputSessionState";
import { chatInputRPC } from "./inputDispatch";
import { reportFrontendLog } from "../../shell/frontendLog";

vi.mock("../../shell/frontendLog", () => ({ reportFrontendLog: vi.fn() }));

function session(): InputSession {
  return {
    id: "chat:view-two", pluginId: "chat", paneType: "chat", instanceId: "view-two", params: { chatId: "c" }, label: "Chat",
    text: "hello", branchId: "branch-a", mode: "dispatch", queuedDispatchId: "", revision: 0,
    selectedRoleIds: ["r1"], forceNewSession: false, parallel: false, raw: false, rawByBranch: {}, pinned: true,
    phase: "idle", error: "", updatedAt: 0,
  };
}

afterEach(() => { vi.restoreAllMocks(); });

describe("pane-independent chat input RPC", () => {
  it("retains structured chat, view and raw intent after the pane is gone", () => {
    const value = session();
    setInputSessionRaw(value, "branch-a", true);
    const request = chatInputRPC(snapshotInputSession(value));
    expect(request.channel).toBe("chat:_:dispatch");
    expect(request.payload).toMatchObject({
      chat_id: "c", input_instance_id: "view-two", branch_id: "branch-a", raw: true,
    });
  });

  it("drops hidden non-member role ids before dispatch and logs the anomaly", () => {
    const warning = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const value = session();
    value.selectedRoleIds = ["removed-role", "r1"];
    const request = chatInputRPC(snapshotInputSession(value), ["r1", "r2"]);
    expect(request.payload.role_ids).toEqual(["r1"]);
    expect(warning).toHaveBeenLastCalledWith("[chat] dispatch dropped non-member role ids", {
      chatId: "c",
      droppedRoleIds: ["removed-role"],
      selectedRoleIds: ["removed-role", "r1"],
      memberRoleIds: ["r1", "r2"],
    });
    expect(reportFrontendLog).toHaveBeenLastCalledWith("warn", "chat", "[chat] dispatch dropped non-member role ids", {
      chatId: "c",
      droppedRoleIds: ["removed-role"],
      selectedRoleIds: ["removed-role", "r1"],
      memberRoleIds: ["r1", "r2"],
    });

    value.selectedRoleIds = ["removed-role"];
    const automatic = chatInputRPC(snapshotInputSession(value), ["r1", "r2"]);
    expect(automatic.payload).not.toHaveProperty("role_ids");
    expect(warning).toHaveBeenCalledTimes(2);
    expect(reportFrontendLog).toHaveBeenCalledTimes(2);
  });

  it("keeps a closed pane's queued edit as an update, not a new dispatch", () => {
    const value = session();
    beginQueuedInputEdit(value, "branch-a", "dispatch-1", "edited");
    const request = chatInputRPC(snapshotInputSession(value));
    expect(request.channel).toBe("chat:_:queued-update");
    expect(request.payload).toEqual({ chat_id: "c", dispatch_id: "dispatch-1", message: "edited" });
    expect(request.payload).not.toHaveProperty("branch_id");
  });
});
