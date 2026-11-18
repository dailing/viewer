export class HumanInputController<EventT> {
  private controlRequest: Promise<boolean> | undefined;
  private queue: Promise<void> = Promise.resolve();

  constructor(
    private readonly acquire: () => Promise<boolean>,
    private readonly deliver: (event: EventT) => Promise<void>,
  ) {}

  ensureControl(acquire: () => Promise<boolean> = this.acquire): Promise<boolean> {
    if (this.controlRequest) return this.controlRequest;
    const request = acquire();
    this.controlRequest = request;
    const clear = () => {
      if (this.controlRequest === request) this.controlRequest = undefined;
    };
    void request.then(clear, clear);
    return request;
  }

  send(event: EventT): Promise<boolean> {
    const result = this.queue.then(async () => {
      if (!(await this.ensureControl())) return false;
      await this.deliver(event);
      return true;
    });
    this.queue = result.then(() => undefined, () => undefined);
    return result;
  }
}
