# viewer-next

Bus SDKs and the prototype frontend for the Viewer plugin framework. The
kernel and all core plugins have been rewritten in Go and live in `../next-go/`
(single-binary `viewerd`); this directory keeps only the language SDKs and the
bus-native prototype UI.

**Authorities (read before writing code):**

- `../docs/plugin-framework.md` — architecture decisions
- `../docs/plugin-protocol.md` — wire-level protocol spec
- `../next-go/README.md` — Go implementation (mainline)

## Layout

- `sdk/` — Python plugin SDK (`viewer-plugin-sdk`): BusClient + Plugin base.
  Also used by the Go stack's black-box smoke suite (`../next-go/scripts/`).
- `ts-sdk/` — TS bus SDK (`@viewer/bus-sdk`): browser/Node>=22 BusClient.
- `frontend/` — Vue 3 + Pinia prototype display layer for the bus world:
  Dock + splittable workspace, in-repo plugins at `src/plugins/<id>/`
  (bus-inspector pane, terminal panel + xterm pane).

## Smoke tests

The Python stack here no longer ships a kernel; run the Go black-box suite
instead (it exercises the protocol against the Go binaries using `sdk/`):

Run `cd ts-sdk && npm test` as the TS SDK vitest gate against the Go kernel.

```bash
../next-go/scripts/smoke_all.sh
```
