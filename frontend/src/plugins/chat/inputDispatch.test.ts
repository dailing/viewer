import { afterEach, describe, expect, it, vi } from "vitest";
import { snapshotInputSession, beginQueuedInputEdit, setInputSessionRaw, type InputSession } from "../../stores/inputSessionState";
import { chatInputRPC } from "./inputDispatch";
import { reportFrontendLog } from "../../shell/frontendLog";

vi.mock("../../shell/frontendLog", () => ({ reportFrontendLog: vi.fn() }));

function session(): InputSession {
  return {
    id: "chat:view-two", pluginId: "chat", paneType: "chat", instanceId: "view-two", params: { chatId: "c" }, label: "Chat",
    text: "hello", taskId: "task-a", continueTurnId: "", mode: "dispatch", queuedDispatchId: "", revision: 0,
    selectedRoleIds: ["r1"], forceNewSession: false, parallel: false, raw: false, rawByTask: {}, pinned: true,
    phase: "idle", error: "", updatedAt: 0,
  };
}

afterEach(() => { vi.restoreAllMocks(); });

describe("pane-independent Task input RPC", () => {
  it("retains the view, Task and raw intent after the pane is gone", () => {
    const value = session();
    setInputSessionRaw(value, "task-a", true);
    const request = chatInputRPC(snapshotInputSession(value));
    expect(request.channel).toBe("chat:_:send-message");
    expect(request.payload).toMatchObject({
      chat_id: "c", input_instance_id: "view-two", task_id: "task-a", text: "hello", raw: true, reopen_ended: true,
    });
    expect(request.payload).not.toHaveProperty("message");
  });

  it("drops hidden non-member role ids before dispatch and logs the anomaly", () => {
    const warning = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const value = session();
    value.selectedRoleIds = ["removed-role", "r1"];
    const request = chatInputRPC(snapshotInputSession(value), ["r1", "r2"]);
    expect(request.payload.role_ids).toEqual(["r1"]);
    expect(warning).toHaveBeenCalledTimes(1);
    expect(reportFrontendLog).toHaveBeenCalledTimes(1);
  });

  it("keeps a closed pane's queued edit as an update, not a new dispatch", () => {
    const value = session();
    beginQueuedInputEdit(value, "task-a", "dispatch-1", "edited");
    const request = chatInputRPC(snapshotInputSession(value));
    expect(request.channel).toBe("chat:_:queued-update");
    expect(request.payload).toEqual({ chat_id: "c", dispatch_id: "dispatch-1", message: "edited" });
    expect(request.payload).not.toHaveProperty("task_id");
  });
});
