# SDKs

Each language SDK implements the same bus duties over the kernel WebSocket
(`ws://<host>:<kernel-port>/ws`): connect + hello, RPC requests, event
subscriptions, retained-state mailbox reads, and automatic reconnect. The
wire protocol is specified in `../docs/plugin-protocol.md`; bus semantics in
`../docs/plugin-framework.md`.

## sdk/go

Part of the root Go module (`module viewer`); import directly:

```go
import (
    "viewer/sdk/go/busclient"
    "viewer/sdk/go/protocol"
)
```

`busclient` depends only on `protocol` (no other kernel packages), so the
directory can be lifted out as its own module for external consumers.

## sdk/python

Standalone Python package (`viewer-plugin-sdk`): `BusClient` + `Plugin`.
Requires `websockets`. Used by the repo's black-box smoke suites (see
`../scripts/`); also usable standalone against any kernel:

```python
from sdk import BusClient  # with sdk/python on sys.path
```

## sdk/ts

npm package `@viewer/bus-sdk` for browser and Node >= 22. Vitest suite in
`tests/` builds/runs the Go kernel binary directly (`VIEWER_KERNEL_BIN`
override supported):

```bash
cd sdk/ts && npm test
```
