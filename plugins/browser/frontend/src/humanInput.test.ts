import { describe, expect, it, vi } from "vitest";
import { HumanInputController } from "./humanInput";

interface TestEvent { type: string }

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe("HumanInputController", () => {
  it("holds the first click until control arrives and preserves press/release order", async () => {
    const control = deferred<boolean>();
    const acquire = vi.fn()
      .mockImplementationOnce(() => control.promise)
      .mockResolvedValue(true);
    const delivered: string[] = [];
    const controller = new HumanInputController<TestEvent>(acquire, async (event) => {
      delivered.push(event.type);
    });

    const pressed = controller.send({ type: "mousePressed" });
    const released = controller.send({ type: "mouseReleased" });
    await Promise.resolve();
    expect(delivered).toEqual([]);

    control.resolve(true);
    await expect(Promise.all([pressed, released])).resolves.toEqual([true, true]);
    expect(acquire).toHaveBeenCalledTimes(2);
    expect(delivered).toEqual(["mousePressed", "mouseReleased"]);
  });

  it("queues text and keys behind the same control request", async () => {
    const control = deferred<boolean>();
    const delivered: string[] = [];
    const controller = new HumanInputController<TestEvent>(() => control.promise, async (event) => {
      delivered.push(event.type);
    });

    const results = Promise.all([
      controller.send({ type: "insertText" }),
      controller.send({ type: "keyDown" }),
      controller.send({ type: "keyUp" }),
    ]);
    control.resolve(true);
    await expect(results).resolves.toEqual([true, true, true]);
    expect(delivered).toEqual(["insertText", "keyDown", "keyUp"]);
  });

  it("skips events when control is rejected and allows a later retry", async () => {
    const acquire = vi.fn()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true);
    const delivered: string[] = [];
    const controller = new HumanInputController<TestEvent>(acquire, async (event) => {
      delivered.push(event.type);
    });

    await expect(controller.send({ type: "mousePressed" })).resolves.toBe(false);
    await expect(controller.send({ type: "mouseReleased" })).resolves.toBe(true);
    expect(acquire).toHaveBeenCalledTimes(2);
    expect(delivered).toEqual(["mouseReleased"]);
  });

  it("does not overtake a delivery that is still in flight", async () => {
    const firstDelivery = deferred<void>();
    const delivered: string[] = [];
    const controller = new HumanInputController<TestEvent>(() => Promise.resolve(true), async (event) => {
      delivered.push(event.type);
      if (event.type === "mousePressed") await firstDelivery.promise;
    });

    const pressed = controller.send({ type: "mousePressed" });
    const released = controller.send({ type: "mouseReleased" });
    await vi.waitFor(() => expect(delivered).toEqual(["mousePressed"]));
    firstDelivery.resolve();
    await Promise.all([pressed, released]);
    expect(delivered).toEqual(["mousePressed", "mouseReleased"]);
  });
});
