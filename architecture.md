# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how the Go kernel, core plugins, frontend, terminal handling, file service, live update flow, and runtime configuration fit together.

Implementation is Go-first: the kernel and all core plugins live under `` (Go), the frontend under `frontend/` (Vue/TS). Python exists only as development tooling under `scripts/` (smoke tests, mocks, migrations); there is no Python product code. The authoritative design documents are `docs/plugin-framework.md` (plugin framework decisions) and `docs/plugin-protocol.md` (wire-level protocol spec); `README.md` is the Go-line build/run manual.

## Purpose

Local Live File Viewer is a private-network workspace for coordinating Codex, Hermes, and OpenCode roles while browsing and editing local files. A Go microkernel (`viewerd`) exposes a WebSocket bus, runs the core plugin set in-process, and serves the built Vue frontend on the same HTTP port. The normal frontend is a single Super Workspace shell with chats, roles, files, Git changes, terminals, Settings, and recursively split panes.

The application assumes a trusted machine and trusted LAN. Terminal, Git, file editing, and Agent processes can modify local files.

## Runtime Flow

1. `viewerd` (single static binary) starts the kernel on loopback `--kernel-host`/`--kernel-port` (defaults `127.0.0.1:8765`) and the gateway on `--host`/`--port` (default `127.0.0.1:18730`). All core plugins are assembled in-process and connect to the kernel over loopback WebSockets through the Go bus SDK — there is no second in-process transport. External plugins connect to the kernel `/ws` directly, and are language-independent.
2. The browser connects to `ws://<gateway>/ws`; the gateway plugin gives each browser connection a dedicated kernel connection whose hello reuses the browser UUID, then relays every later frame byte-for-byte. The same HTTP port serves the embedded frontend (`web/` embeds `web/dist`, built from `frontend`), with `viewerd --static` as the development override.
3. The frontend starts in `frontend/src/main.ts`: creates the Vue app and Pinia, mounts the shell, and lets the in-repo plugin loader register pane plugins (files, chat, voice, chat-manager). Panes, sidebar, drafts, and scroll positions persist browser-locally; `utils/storage.ts` migrates legacy `.dailing` keys on first access.
4. Chat flow: `ComposerBox.vue` sends the draft through the chat plugin RPC; chat publishes prompt/turn frames over the bus to the selected agent plugin; agent plugins (`viewer.agent-hermes`, `viewer.agent-codex`, `viewer.agent-opencode`) own ACP/App Server stdio subprocesses and publish ordered raw-plus-parsed event frames plus turn-ended events (carrying stop reason and, on prompt failure, the error text); chat persists turns/blocks in `chat.sqlite3` and the frontend incrementally reloads changed runs. A turn whose final stop reason is neither `end_turn` nor `cancelled` is surfaced as a persisted `error` message block (stop reason plus per-attempt error details) rendered as a red single-line row in the timeline. Events are correlated by echoed `turn_id`, never by session-to-turn mapping.
5. SIGINT/SIGTERM make the kernel broadcast close 4009 to all connections, then shut plugins down in reverse order and close PTYs, bounded by a 10-second deadline.

## Go Kernel Structure

``

- `internal/protocol/` — five wire-frame shapes, strong hello/envelope validation, UUIDv4 and manifest validation, dotted plugin-id-capable channel/pattern grammar, prefix/`*`/`>` matcher. Payload values are arbitrary JSON.
- `internal/broker/` — serialized subscription table, publish fanout, ordered retained mailbox replacement/replay, atomic subscribe handoff, bounded drop-new outbound queues (corr-tagged WARN on dropped RPC frames), protocol-error delivery, depth guard, no_route fast-fail for subscriber-less RPC requests. `registry.go` owns the retained `plugins:_:list` snapshot and lifecycle events.
- `internal/kernel/` — serves only `/ws`, hello identity, delivery time/origin stamps, oversize-frame rejection, one outbound writer plus ping/pong heartbeat per connection, close 4009 on shutdown. Core assembly and supervisor startup are deliberately outside the kernel.
- `sdk/go/busclient/` — concurrency-safe Go bus SDK shared by in-process and external Go plugins: transport isolation, hello + registry barrier, retained/live handler workers, protocol-error callbacks, inbox RPC/timeout/cancel mapping, exponential reconnect, state callbacks, panic recovery.
- `internal/pluginapi/` — in-process plugin contract, compile-time registry, inspector-first startup, reverse shutdown, loopback kernel-WS wiring, startup deadlines, plugin-level panic isolation.
- `cmd/viewerd/main.go` — assembled single binary: kernel + all core plugins + embedded frontend. Flags: `--host`, `--port`, `--kernel-host`, `--kernel-port`, `--data-dir`, `--static`, `--plugins`. Standalone forms: `cmd/viewer-kernel`, `cmd/viewer-gateway`, `cmd/viewer-terminal`, `cmd/viewer-supervisor`, `cmd/viewer-inspector`, `cmd/viewer-configstore`, `cmd/viewer-instancestore`, `cmd/viewer-fileservice`, `cmd/viewer-voice`.

### Core plugins (resident in `viewerd`)

- `internal/plugins/gateway/` — C4 HTTP gateway: browser `/ws` relay + static `fs.FS` serving with resolved-path/symlink containment.
- `internal/plugins/terminal/` — PTY sessions, incremental UTF-8 decoding, 30 ms/128 KiB output coalescing, bounded snapshot ring, retained status replay, 30-second post-exit history, resize/write/RPC, process-group termination.
- `internal/plugins/supervisor/` — C0 process supervisor: registry loading, managed external-plugin process groups, append-only logs, lifecycle state publication, serialized manual restart RPCs, exponential backoff, 60-second crash-loop breaker, TERM-to-KILL tree shutdown.
- `internal/plugins/inspector/` — bus inspector: open `>` capture with self-origin rejection, bounded ring, compound filters, pause/resume/clear, stats, newest-first 800KB-budgeted cursor snapshots.
- `internal/plugins/configstore/`, `instancestore/`, `fileservice/` — C1–C3 core services: atomic JSON config, atomic per-instance state with retained tombstones, file resolve/read/hash + unpaged directory listing (`file:_:list` filters hidden names, sorts directories first case-insensitively). `internal/plugins/pluginrpc/` supplies inbox response helpers; `internal/plugins/storefile/` owns sibling-temp-file JSON replacement.
- `internal/agentdriver/` + `internal/acp/` + `internal/plugins/agenthermes/` — provider-neutral headless-agent bus types, bounded NDJSON JSON-RPC ACP stdio client (`ParseBlock`), and the single-instance `viewer.agent-hermes` service plugin (session pool, strict non-empty state validation for Hermes session loads with fresh-session fallback, immediate prompt ack, ordered event frames, retained catalog under `viewer.agent-hermes:_:catalog`).
- `internal/codexserver/` + `internal/plugins/agentcodex/` — bounded Codex App Server JSONL stdio client and single-instance `viewer.agent-codex` (thread control, best-effort `model/list`, `openai-subscription` catalog).
- `internal/plugins/agentopencode/` — second ACP tenant, single-instance `viewer.agent-opencode`; unknown ACP update kinds retained as `other` blocks.
- `internal/plugins/chat/` — provider-agnostic Super Workspace orchestrator. Plugin config `plugins.viewer-chat` retains workspace metadata, `agents` mapping, LLM router, summaries/Hindsight, and word/byte context budgets; raw recent history is capped separately and the combined context bridge has a final UTF-8 byte cap that preserves the newest content. Roles and routing policies are domain rows in GORM/modernc `chat.sqlite3`. Every agent start/prompt/cancel and event/turn-ended exchange crosses the bus; chat has no ACP/App Server import, parser, process, or in-process agent interface. Chats/messages/turns/blocks persist under the data directory with the compound `(chat_id, occurred_at)` index on `message_blocks`. Routing policy resolution per dispatch is layered: the chat's `role_routing_policy_overrides[role_id]` (column `role_routing_policy_overrides`, edited per chat in ChatsPanel) beats the role's `routing_policy_id`, which beats the workspace `default_routing_policy_id`. Incoming event blocks are coalesced before persistence: consecutive same-kind `agent_text`/`thinking` deltas append into one open block per segment, and `tool_call` blocks carrying a `tool_call_id` (emitted by the ACP and Codex block parsers) merge lifecycle status updates into the single block opened by the initial call, republishing with the same block id so the frontend updates the row in place. A reused Hermes session that ends with `refusal` before emitting any event is treated as stale and retried once with a fresh session; visible refusals, fresh-session refusals, and other agents are never auto-retried. `chat:_:blocks:list` replies are byte-budgeted (`blocksReplyBudget`, under the kernel's 1 MiB frame limit — oversized publishes are rejected asynchronously and would otherwise strand the caller until timeout): an over-budget reply stops early with `truncated: true` + inclusive `next_after` cursor, and the frontend pages forward, deduplicating boundary blocks by id. The chat plugin logs bus protocol errors (e.g. `frame_too_large`) via the client error callback.
- `internal/plugins/voice/` — external voice-service relay: each `voice:_:start` reads `plugins.viewer-voice.service_ws/model/language` from config-store, opens one upstream WebSocket, forwards base64 bus chunks as binary frames and stop as JSON, normalizes service text messages into `voice:{rec}:event`, owns concurrent session cancellation plus the ten-minute hard limit. The standalone form is `cmd/viewer-voice`.

### External plugins and tooling

- External plugins connect to the kernel `/ws` directly (`ws://<kernel-host>:<kernel-port>/ws`); kernel defaults to loopback. `examples/pingpong/` demonstrates two Go SDK clients making RPC calls in both directions.
- `scripts/` holds the Python black-box tooling (test-only): `smoke_*.py` suites, `mock_*.py` deterministic Agent fixtures, `migrate_*.py` data migrations. The Python SDK itself lives in `sdk/python/`. `smoke_all.sh` orchestrates the full suite with `.venv/bin/python`. There is no Python product code.

## Frontend Structure

`frontend/src/`

- `main.ts` — Vue app bootstrap, Pinia install, shell mount.
- Shell — split-pane shell with pane chrome (title bar, actions), sidebar, Settings; browser-local layout persistence; no global navbar.
- Plugin loader — in-repo plugin loader that registers pane plugins and headless plugins per backend plugin availability.
- `plugins/files/` — Files pane: root and directory listings exclusively through `file:_:list`, lazy child directories, no preview/mutation actions.
- `plugins/chat/` — per-chat module: `ChatPane.vue` (dispatch semantics, incremental run reload, lazy per-segment markdown rendering with `renderedHtmlFor()`), `ComposerBox.vue` (local draft state, 300 ms debounced auto-grow, voice input, explicit/automatic Role selection, send/stop). Send immediately renders an optimistic user box (发送中 marker, then a `Role → agent/provider/model` routing annotation once dispatch returns; failure marks the box 发送失败 with a dismiss button, and the incoming real user message supersedes the placeholder and inherits its routing label). Dispatch immediately renders one optimistic response box per role (shimmer placeholder until the turn's first live event lands); the running indicator shows only on each role's latest box, never on its historical turns. When enabled (`stores/chatSettings.ts`, default on), the thread renders one viewport of virtual space after the final message; initial loads and newly sent queries scroll to the message-end anchor rather than the absolute scroll-container end, while streaming updates never auto-scroll. Chat-manager mutations emit the frontend-local `viewer:chats-changed` event.
- `plugins/voice/` — headless plugin: `voiceStore.ts` owns per-composer recording/processing/ready state, one globally active MediaRecorder, 250 ms chunk encoding and bus delivery, cancellation cleanup; `VoiceInputButton.vue` binds state to a draft through `defineModel`.
- `plugins/chat-manager/` — chat list / Dock management; ChatsPanel edits per-chat settings including per-role routing policy overrides (`role_routing_policy_overrides`).
- `plugins/settings/` — unified full-page Settings pane (`settings:main`, opened from the Dock gear): layout open mode (`stores/layout.ts` `openMode`), chat reading virtual space (`stores/chatSettings.ts`), Dock hover-expand delay (`stores/dockSettings.ts`), markdown theme overrides (`stores/markdownStyle.ts`), and backend restart / build-restart via the gateway admin API. All toggles are browser-local (localStorage) and apply immediately.
- `frontend/README.md` — short manual loop for serving `frontend/dist` through `viewerd --static`, using the embedded UI, or running Vite with its `/ws` gateway proxy.

## Root And Build Files

`` — Go 1.26 implementation line for the Viewer microkernel. The assembled `cmd/viewerd` includes twelve resident plugins, including chat, voice, and the headless agent services, plus the embedded frontend. See "Go Kernel Structure" above for per-package responsibilities, and `scripts/` for the smoke/mock/migration tooling (test-only, Python).

`frontend/package.json` — frontend metadata, scripts, dependencies. Scripts: `dev`, `build` (`vue-tsc --noEmit && vite build`), `preview`. Main libraries: Vue, Vite, Pinia, Bootstrap, Bootstrap Icons, xterm, markdown-it plugins, KaTeX, Mermaid, Highlight.js.

`frontend/package-lock.json` / `tsconfig.json` / `vite.config.ts` / `index.html` — locked dependency graph (do not hand-edit), TS config, Vite config with Vue plugin, minimal HTML entry.

`web/` — `go:embed` mount point for the built frontend (`web/dist`, generated by `web/build-release.sh`, which builds `frontend`, syncs its dist into the embed tree, then builds the release binary to `dist/viewerd`; `scripts/build-release.sh` remains the compatibility entry point).

`docs/plugin-framework.md` — authoritative plugin framework design (iteration rules: version bumps per ratified section). `docs/plugin-protocol.md` — wire-level protocol spec.

## Data Contracts

- Wire protocol: five frame types (hello/event/rpc/…), three-segment channels, inbox RPC conventions, retained mailbox semantics — see `docs/plugin-protocol.md` and `internal/protocol/protocol.go`.
- Channel grammar: dotted plugin-id-capable patterns with prefix/`*`/`>` matching; payload values are arbitrary JSON.
- Frontend types in `frontend/src/plugins/*/types.ts` mirror backend bus payloads; if a payload field changes, update the matching frontend type and all consumers.

## Persistence

- `--data-dir` (default `$XDG_DATA_HOME/viewer`, else `~/.local/share/viewer`): `config.json`, `instance.json`, external-plugin registry, and `viewerd.log`. The assembled server appends both structured `slog` records and standard-library plugin logs to `viewerd.log` while retaining stderr/journal output. `config.json` holds appearance, voice, Markdown, and chat/LLM router settings; Agent credentials and provider/model configuration stay in each Agent's own files.
- `chat.sqlite3` under the data directory: Super Workspace chats, roles, routing policies, messages, turns, blocks (via GORM/modernc). Legacy C1 roles/routing are imported exactly once when both DB tables are empty.
- Browser-local state: pane layout, sidebar state, pins, drafts, scroll positions (non-namespaced localStorage, `.dailing` keys migrated).

## Common Fault Locations

- Bus not connecting / registry empty: check kernel `--kernel-host`/`--kernel-port` reachability, gateway relay hello, `internal/kernel/` and `internal/broker/registry.go`.
- Plugin missing from `plugins:_:list`: check `internal/pluginapi/` assembly, plugin manifest/hello validation in `internal/protocol/`.
- Terminal output glitches: `internal/plugins/terminal/` coalescing/snapshot logic and `TerminalViewer.vue`.
- Chat dispatch fails: `internal/plugins/chat/` routing policies, agent catalog mailboxes, agent plugin logs, `chat.sqlite3`; check mock fixtures (`scripts/mock_*.py`) for deterministic reproduction.
- Voice not recording: `plugins.viewer-voice.service_ws/model/language` config, upstream service reachability, `voiceStore.ts` recording state.
- File listing wrong/security issues: `internal/plugins/fileservice/` resolve/containment and gateway static `fs.FS` containment.
- Frontend runtime errors: browser console; kernel/plugin logs under the data directory.

## Maintenance Rules

- Keep this file synchronized with code when responsibilities move or files are added/removed.
- Keep bus payloads and frontend TypeScript interfaces aligned.
- Do not hand-edit generated artifacts (`frontend/package-lock.json`, `frontend/dist/`, `web/dist/`, `frontend/node_modules/`).
- Standard checks: frontend `cd frontend && npx vue-tsc --noEmit && npm run build`; backend `gofmt -l . && go build ./... && go test ./...`; black-box `bash scripts/smoke_all.sh`.
- The app is read-only for served files except terminal/Agent/loop processes, which can modify files because they run real commands in the served root. Viewer-owned config/state/log files live under the configured data directory.
