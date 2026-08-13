# viewer-next

Plugin-framework rewrite of Viewer (Phase 0+). This is a near-greenfield project
developed alongside the legacy app in the same worktree; legacy code is
reference-only and will be replaced piece by piece.

**Authorities (read before writing code):**

- `../docs/plugin-framework.md` — architecture decisions (v0.19)
- `../docs/plugin-protocol.md` — wire-level protocol spec (v0.1, frozen before impl)

## Layout

- `kernel/` — the microkernel: WebSocket endpoint `/ws` + broker (publish routing +
  retained mailbox) + connection registry. **No autostart**: process bring-up is
  external (startup script / supervisor), the kernel is pure message system.
- `sdk/` — Python plugin SDK (`viewer-plugin-sdk`): BusClient + Plugin base.
- `ts-sdk/` — TS bus SDK (`@viewer/bus-sdk`): browser/Node>=22 BusClient, vitest
  suite against a real kernel.
- `plugins/` — core plugins: `supervisor` (C0), `configstore` (C1),
  `instancestore` (C2), `fileservice` (C3), `gateway` (C4 http-gateway +
  static assets), `inspector` (bus-inspector debug plugin), `terminal` (PTY
  sessions as bus instances, framework A.4).
- `frontend/` — Vue 3 + Pinia + Bootstrap display layer: thin bootstrap, shell
  (registries + ctx factory + PluginPaneHost), stage-A in-repo plugins at
  `src/plugins/<id>/` (bus-inspector pane, terminal panel + xterm pane).
- `tests/` — incremental tests; every module lands with its tests in the same change.

## Run

```
cd next && ./start.sh
```

`start.sh` launches the kernel and the C0 supervisor, which spawns every
enabled plugin in `registry.json` (each via its `backend/run` startup-ABI
entry). Ctrl-C stops the whole stack gracefully (supervisor SIGTERMs managed
plugins first, then the kernel closes with 4009). Ports: kernel `18765`,
gateway `18730` (serves `frontend/dist`; build it first with
`cd frontend && npm run build`). Env overrides: `VIEWER_HOST`, `VIEWER_PORT`,
`VIEWER_REGISTRY`, `VIEWER_HTTP_HOST`, `VIEWER_HTTP_PORT`, `VIEWER_STATIC`.

Tests: `uv run pytest -q`; TS SDK: `cd ts-sdk && npm install && npx vitest run`

Frontend: `cd frontend && npm install && npm run build` — serve `frontend/dist`
via the gateway plugin: `python -m plugins.gateway --kernel-ws ws://127.0.0.1:8765/ws --static frontend/dist --port 18730`
