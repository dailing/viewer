# Local Live File Viewer

Local Live File Viewer is a private-network workspace for coordinating Codex, Hermes, and OpenCode roles while browsing and editing local files. A Go microkernel (`viewerd`) exposes a WebSocket bus, runs all core plugins in-process, and serves the built Vue application on the same HTTP port; the normal frontend is a single Super Workspace shell with chats, roles, files, Git changes, terminals, Settings, and recursively split panes.

The implementation is Go-first: kernel and core plugins live under `cmd/` and `internal/`, frontend under `frontend/`. Language SDKs live under `sdk/` (`sdk/go`, `sdk/python`, `sdk/ts`). Python exists only as development tooling under `scripts/` (smoke tests, mocks, migrations) — there is no Python product code. Design decisions: `docs/plugin-framework.md` (authoritative) and `docs/plugin-protocol.md` (wire-level protocol spec).

The application assumes a trusted machine and trusted LAN. Terminal, Git, file editing, and Agent processes can modify local files.

## Layout

```
cmd/                 single-binary entry (kernel + core plugin set + embedded frontend)
internal/            kernel and core plugins (broker/kernel/pluginapi/plugins/...)
sdk/
  go/                Go bus SDK: protocol frame types + BusClient (RPC, subscribe, reconnect)
  python/            Python bus SDK: BusClient + Plugin base
  ts/                TS bus SDK (@viewer/bus-sdk, browser/Node>=22)
frontend/            Vue 3 + Pinia frontend; in-repo plugins at src/plugins/<id>/
web/                 go:embed mount point for the frontend dist + build-release.sh
scripts/             black-box smoke suites, mock services, migration tools
examples/pingpong/   two-way RPC example against the kernel
docs/                architecture decisions (plugin-framework.md) + wire protocol (plugin-protocol.md)
```

## SDKs

Each language SDK implements the same bus duties over the kernel WebSocket
(`ws://<host>:<kernel-port>/ws`): connect + hello, RPC requests, event
subscriptions, retained-state mailbox reads, and automatic reconnect.

- `sdk/go` — part of the root Go module (`module viewer`); import
  `viewer/sdk/go/busclient` and `viewer/sdk/go/protocol`. External projects
  can copy the directory as its own module (busclient depends only on
  protocol, no other kernel packages).
- `sdk/python` — standalone package (`viewer-plugin-sdk`): `BusClient` +
  `Plugin`. Also used by the smoke suites.
- `sdk/ts` — npm package `@viewer/bus-sdk`; vitest suite in `sdk/ts/tests`
  runs against the Go kernel binary (`VIEWER_KERNEL_BIN` override supported).

## Features

- Create direct or group chats and assign member roles.
- Route a message through an OpenAI-compatible dispatcher or target roles explicitly.
- Run Codex (App Server), Hermes (ACP), and OpenCode (ACP) roles in the background and stream their persisted output into Chat panes.
- Retain visible Super Workspace chat messages to an optional chat-scoped Hindsight memory bank.
- Stop a running role with a two-click confirmation.
- Give each role a dispatcher-facing description and a separate Agent-facing prompt.
- Decouple roles from Agents/providers/models with reusable routing profiles, per-chat overrides, capability filters, and ordered automatic failover.
- Reuse role sessions per chat or start a new session for each run.
- Cite earlier messages with `@msg-...` references.
- Browse, upload, delete, edit, and live-refresh files under the current Chat Root.
- Preview Markdown, HTML, images, PDFs, CSV, text, source code, and Git diffs.
- Render Markdown with KaTeX, Mermaid, Highlight.js, tables, task lists, footnotes, and local links.
- Stage, revert, commit, and push changes from Git diff panes.
- Open reconnectable PTY terminals through WebSockets.
- Use optional voice dictation and LLM refinement (voice plugin relays to a configurable upstream service; `service_ws`/`model`/`language` are configurable).
- Arrange chats, files, diffs, and terminals in persisted recursive split panes.
- Configure appearance, Markdown themes, routing profiles, dispatcher profiles, voice, and server controls.

## Super Workspace

The application has one Super Workspace, persisted under the fixed owner `dailing:default`. It contains user-created chats and globally available roles.

Every chat requires a `root`, relative to the server's filesystem boundary. Files, Git, terminals, and Agents use that Chat Root. A role may optionally add a subdirectory beneath it; there is no profile or global-working-directory fallback.

A role has two distinct instruction fields:

- `description`: routing metadata for the dispatcher. It describes when the role should be selected, its capabilities, and its dispatch constraints.
- `prompt`: operating instructions delivered directly to the selected Agent. It defines workflow, standards, style, and execution rules.

The dispatcher receives descriptions but not prompts. A role Agent receives its prompt but not its description. A Role selects a reusable routing profile; a Chat may override it for one Role. Viewer treats each driver's model selection value as opaque and never modifies Agent credentials or Agent-owned configuration.

Normal message delivery is asynchronous: the chat plugin persists the user query and dispatch tasks; agent service plugins create or resume ACP/App Server sessions for the selected targets; events stream back over the bus with an echoed `turn_id`; the Chat pane incrementally reloads the changed run.

Codex uses its native App Server protocol; Hermes and OpenCode use ACP. Hermes runs through the user-selected Hermes Profile, preserving Hermes-owned configuration, memory, history, channels, and credentials.

## Requirements

- Go 1.26+
- Node.js and npm (frontend build)
- `codex` on `PATH` with App Server support for Codex roles
- `hermes` on `PATH` with the ACP optional dependency for Hermes roles
- `opencode` on `PATH` for OpenCode roles
- An OpenAI-compatible chat-completions endpoint for automatic routing

## Build

Build the frontend and the single-binary release (kernel + core plugins + embedded UI):

```bash
./web/build-release.sh
```

Or build only the Go binaries:

```bash
go build ./...
```

## Run

```bash
dist/viewerd \
  --host 127.0.0.1 --port 18730 \
  --kernel-host 127.0.0.1 --kernel-port 8765 \
  --data-dir ~/.local/share/viewer
```

- Browser and SDK connect to `ws://127.0.0.1:18730/ws` (gateway); HTTP static assets on the same port.
- External plugins connect to `ws://127.0.0.1:8765/ws` (kernel). The kernel defaults to and should stay loopback; `--kernel-host` changes it only when explicitly set.
- `--data-dir` defaults to `$XDG_DATA_HOME/viewer` (or `~/.local/share/viewer`): `config.json`, `instance.json`, external-plugin registry, and logs.
- Development: `--static frontend/dist` overrides the embedded UI. Frontend dev manual: `frontend/README.md`.
- `--plugins` selects core plugins: `"all"`, `"none"`, or a comma-separated list.

SIGINT/SIGTERM make the kernel broadcast close 4009 to all connections, then shut plugins down in reverse order and close PTYs, bounded by a 10-second deadline.

## Standard checks

Do not start the frontend development server for routine verification.

```bash
cd frontend && npx vue-tsc --noEmit && npm run build
gofmt -l . && go build ./... && go test ./...
bash scripts/smoke_all.sh   # black-box suites; uses .venv python
cd sdk/ts && npm test       # TS SDK vitest against the Go kernel
```

Single-binary black-box acceptance (after `./web/build-release.sh`):

```bash
.venv/bin/python scripts/smoke_single_binary.py --viewerd-bin dist/viewerd
```

For the detailed module map, data flow, wire protocol, and fault locations, see [`architecture.md`](architecture.md), `docs/plugin-framework.md`, and `docs/plugin-protocol.md`.
