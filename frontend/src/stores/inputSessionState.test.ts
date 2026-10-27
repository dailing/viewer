import { describe, expect, it } from "vitest";
import {
  beginQueuedInputEdit,
  cancelQueuedInputEdit,
  completeInputSessionSend,
  inputSessionIntent,
  releaseQueuedInputEdit,
  setInputSessionBranch,
  setInputSessionText,
  snapshotInputSession,
  type InputSession,
} from "./inputSessionState";

function session(): InputSession {
  return {
    id: "chat:c", pluginId: "chat", paneType: "chat", instanceId: "c", label: "Chat",
    text: "ordinary", branchId: "a", mode: "dispatch", queuedDispatchId: "", revision: 0,
    selectedRoleIds: ["r1"], forceNewSession: false, parallel: false, pinned: true,
    phase: "idle", error: "", updatedAt: 0,
  };
}

describe("input session send intent", () => {
  it("keeps an await-bound branch and never clears a newly selected line", () => {
    const value = session();
    const intent = inputSessionIntent(value);
    const sent = snapshotInputSession(value, "from A", intent);
    setInputSessionBranch(value, "b");
    setInputSessionText(value, "draft on B");
    completeInputSessionSend(value, sent);
    expect(sent.branchId).toBe("a");
    expect(value.branchId).toBe("b");
    expect(value.text).toBe("draft on B");
    expect(value.phase).toBe("idle");
  });

  it("clears only the unchanged sent snapshot, not text typed while sending", () => {
    const value = session();
    setInputSessionText(value, "first");
    const sent = snapshotInputSession(value);
    setInputSessionText(value, "second");
    completeInputSessionSend(value, sent);
    expect(value.text).toBe("second");

    const second = snapshotInputSession(value);
    completeInputSessionSend(value, second);
    expect(value.text).toBe("");
    expect(value.branchId).toBe("a");
  });

  it("accepts the backend empty-draft acknowledgement without re-arming one-shot options", () => {
    const value = session();
    value.forceNewSession = true;
    value.parallel = true;
    const sent = snapshotInputSession(value);
    setInputSessionText(value, ""); // remote draft:sync from the accepted dispatch
    completeInputSessionSend(value, sent);
    expect(value.text).toBe("");
    expect(value.forceNewSession).toBe(false);
    expect(value.parallel).toBe(false);
  });

  it("captures role and one-shot options with the intent", () => {
    const value = session();
    value.forceNewSession = true;
    value.parallel = true;
    const sent = snapshotInputSession(value);
    value.selectedRoleIds.push("r2");
    value.forceNewSession = false;
    value.parallel = false;
    expect(sent.selectedRoleIds).toEqual(["r1"]);
    expect(sent.forceNewSession).toBe(true);
    expect(sent.parallel).toBe(true);
  });
});

describe("queued edit lifecycle", () => {
  it("keeps queued text separate from and restores the ordinary draft", () => {
    const value = session();
    beginQueuedInputEdit(value, "a", "d1", "queued original");
    setInputSessionText(value, "queued edited");
    expect(value.draftBeforeQueueEdit).toBe("ordinary");
    const sent = snapshotInputSession(value);
    completeInputSessionSend(value, sent);
    expect(value.mode).toBe("dispatch");
    expect(value.queuedDispatchId).toBe("");
    expect(value.text).toBe("ordinary");
  });

  it("cancel and branch switch restore rather than flush the queued text", () => {
    const value = session();
    beginQueuedInputEdit(value, "a", "d1", "queued");
    cancelQueuedInputEdit(value);
    expect(value.text).toBe("ordinary");

    beginQueuedInputEdit(value, "a", "d2", "queued 2");
    setInputSessionBranch(value, "b");
    expect(value.mode).toBe("dispatch");
    expect(value.branchId).toBe("b");
    expect(value.text).toBe("ordinary");
  });

  it("preserves the edited text if the queue entry disappears", () => {
    const value = session();
    beginQueuedInputEdit(value, "a", "d1", "queued");
    setInputSessionText(value, "too late but keep me");
    releaseQueuedInputEdit(value);
    expect(value.mode).toBe("dispatch");
    expect(value.text).toBe("too late but keep me");
    expect(value.branchId).toBe("a");
  });
});
