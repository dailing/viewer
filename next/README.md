# viewer-next

Plugin-framework rewrite of Viewer (Phase 0+). This is a near-greenfield project
developed alongside the legacy app in the same worktree; legacy code is
reference-only and will be replaced piece by piece.

**Authorities (read before writing code):**

- `../docs/plugin-framework.md` — architecture decisions (v0.19)
- `../docs/plugin-protocol.md` — wire-level protocol spec (v0.1, frozen before impl)

## Layout

- `kernel/` — the microkernel: WebSocket endpoint + broker (publish routing +
  retained mailbox) + connection registry + the single autostart hook.
- `plugins/` — core and feature plugins (later phases).
- `tests/` — incremental tests; every module lands with its tests in the same change.

## Run

```bash
uv sync
uv run python -m kernel --port 8765
uv run pytest -v
```
