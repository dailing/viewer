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
  static assets), `inspector` (bus-inspector debug plugin).
- `frontend/` — Vue 3 + Pinia + Bootstrap display layer: thin bootstrap, shell
  (registries + ctx factory + PluginPaneHost), stage-A in-repo plugins at
  `src/plugins/<id>/` (first: bus-inspector pane).
- `tests/` — incremental tests; every module lands with its tests in the same change.

## Run

```bash
uv sync
uv run python -m kernel --port 8765
uv run pytest -v
```

TS SDK: `cd ts-sdk && npm install && npx vitest run`

Frontend: `cd frontend && npm install && npm run build` — serve `frontend/dist`
via the gateway plugin: `python -m plugins.gateway --kernel-ws ws://127.0.0.1:8765/ws --static frontend/dist --port 18730`
