# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how frontend, backend, terminals, file watching, and preview rendering fit together.

Design docs for the plugin-framework migration (target architecture, not yet implemented): `docs/plugin-framework.md` (architecture decisions, authoritative) and `docs/plugin-protocol.md` (wire-level plugin protocol spec, Phase 0).

## Purpose

Local Live File Viewer is a private-network file browser and preview app. A FastAPI backend exposes file, Git, terminal, voice, and Super Workspace APIs and serves the built Vue frontend. `VIEWER_ROOT` is the fixed server-side filesystem boundary; every chat has a required relative Chat Root that supplies the actual working context for Files, Git, terminals, Codex, and Hermes. The application has one fixed persistence owner, `dailing`, and no user-profile selection layer. The Vue app provides chats, roles, recursive split panes, file viewers, live refresh, and browser-local layout persistence.

## Runtime Flow

1. In the systemd deployment, `scripts/supervise_viewer.py` is the stable service MainPID and launches `run.py` with the unit's canonical backend arguments. A normal `POST /api/admin/restart` sends SIGHUP to that supervisor: it gracefully stops only the backend generation and launches a replacement with the same arguments, leaving old workers in the same cgroup to drain in-flight turns while the replacement worker claims new work. An explicit `systemctl --user restart viewer.service` remains a hard whole-cgroup restart.
2. `run.py` loads project-local `.viewer.env` without overriding existing process variables, parses CLI flags, sets `VIEWER_*` environment variables including optional WhisperLiveKit voice settings, optionally builds the frontend, configures logging, and starts `uvicorn` on `app.main:app`; `--config-dir` and `--data-dir` isolate configuration from mutable databases/logs while preserving the legacy `~/.view` default. Active requests and connections receive a configurable graceful-shutdown window (`--graceful-shutdown-timeout`, default 5 seconds) before Uvicorn cancels them. `backend/app/main.py` creates the FastAPI app, installs middleware, starts `watch_root()` and a Super Workspace worker generation, registers routes, and mounts `frontend/dist` if it exists.
3. The frontend starts in `frontend/src/main.ts`, installs global client error logging, creates Pinia, and mounts `App.vue`. `App.vue` keeps the application height, width, and top/left position synchronized with the browser visual viewport (falling back to the window/dynamic viewport) so mobile virtual keyboards resize and reposition the application shell instead of exposing the layout viewport behind it when Safari pans a focused input.
4. `App.vue` loads config and terminal state directly, applies visual config as CSS variables, connects to `/api/events`, refreshes affected file listings, and dispatches every filesystem SSE as a `viewer:file-changed` browser event. Browser-local layout/sidebar/draft keys are no longer user-namespaced; `utils/storage.ts` migrates the former `.dailing` keys on first access. The normal page is the Super Workspace split-pane shell with no global navbar: every viewer pane owns a title bar, while Settings is launched from the sidebar activity bar.
5. `ViewerPane.vue` fetches file metadata and chooses the correct viewer component, including routing `.csv` text files to the CSV table viewer and oversized Markdown/CSV/text files to the virtualized large text viewer. Each pane renders `PaneTitleBar.vue`, which combines that pane's registered viewer controls with refresh/back/split/clear actions and dispatches `viewer:pane-refresh` for the concrete pane id; file panes clear visible content before refetching metadata and incrementing their pane `version`, diff panes clear and reload their Git diff directly, and terminal panes reconnect through their own viewer. Viewers fetch raw/text content and reload when their `version` prop changes; image and PDF panes include the pane version in raw-file URLs so manual refreshes can bypass browser cache even when the content hash is unchanged. Markdown panes render embedded images with browser lazy loading, track simple relative local image dependencies client-side, use a pane-level version cache key for raw-file URLs, reload/cache-bust embedded images when those image files change, and can switch into a split textarea/live-preview edit mode that saves through `/api/file/content` PUT. PDF panes configure the PDF.js worker explicitly, preload document metadata from `/api/file/raw` for the page count, and render individual pages lazily from the same cache-busted raw URL as page placeholders approach the scroll viewport. HTML panes load documents through an iframe-backed static-site route so relative scripts, stylesheets, images, and links resolve like a browser-served folder; inactive HTML panes render a transparent activation shield above the iframe so a first click can select the pane before iframe content consumes pointer events.
6. Terminal panes use REST for lifecycle operations and WebSocket `/api/terminals/{id}/ws` for interactive PTY input/output.
7. Codex App Server and ACP sessions are internal runtime primitives for Super Workspace roles. Codex uses its native JSONL App Server protocol; Hermes and OpenCode use the shared ACP runtime over worker-owned stdio subprocesses. Each driver exposes a read-only Agent/provider/model target catalog, while Viewer never edits Agent credentials or private configuration. Sessions use the required Chat Root as their real cwd. Provider output and chat history are written only into `super_workspace_messages` in `VIEWER_DATA_DIR/agent-history.sqlite3`; Viewer never reads a provider's private history database. The frontend does not expose low-level provider session panes or `/api/agents/sessions`.

## Backend Structure

`backend/app/main.py`

- FastAPI application and route table.
- `log_requests(request, call_next)`: logs failed and slow HTTP requests, while suppressing normal successful hot-path poll noise even in debug mode.
- `startup()`: ensures the fixed `VIEWER_ROOT` filesystem boundary exists, logs runtime config, and starts the filesystem watcher task.
- `shutdown()`: stops watcher task and terminates terminal sessions.
- `/api/health`: returns health and the current backend PID; the filesystem boundary is not exposed as workspace state.
- `/api/admin/restart`: requests one supervisor-controlled graceful generation replacement. There is no separate backend-only versus backend-plus-worker API mode: the replacement backend starts a new worker generation, while the old worker drains any in-flight turns and then exits through leadership handover.
- `/api/admin/stop`: launches the detached process manager to stop the current backend PID.
- `/api/tree`: calls `list_directory()` under the fixed filesystem boundary. Normal sidebar browsing starts at the current Chat Root and cannot navigate above it.
- `/api/file/upload`: streams one request body into a file under the requested active-user-root-relative directory. The directory must exist, filenames cannot contain path separators, and existing files are overwritten while directories are protected.
- `/api/file` DELETE: deletes an active-user-root-relative file only; directory deletion is intentionally rejected.
- `/api/file/meta`, `/api/file/content`, and `/api/file/text-lines`: read metadata/content under the fixed filesystem boundary; normal navigation supplies paths from the current Chat Root.
- `/api/file/content` PUT: saves UTF-8 text to an existing file under the fixed boundary.
- `/api/file/raw`: streams a file via inline `FileResponse` and emits `ETag` plus strong immutable browser cache headers. When called with `base`, resolves Markdown-local relative/absolute file links before serving.
- `/api/file/site/{path:path}` and `/api/file/site?path=...`: serve files as a static-site namespace for HTML preview iframes. The query form preserves absolute filesystem paths for outside-root files and symlink targets. HTML responses inject a `<base>` tag for relative assets and rewrite root-relative HTML/CSS asset URLs to the same `/api/file/site/` prefix; CSS responses rewrite root-relative `url(...)` and `@import` references; other files are returned through inline `FileResponse` so SVG/PDF/image assets render in-browser instead of downloading. Missing `generated/assets/...` requests fall back by searching upward for the nearest existing generated asset directory, which keeps static docs with page-relative generated-asset links working in the iframe preview. Cache headers are `no-cache` so local edits show after pane refreshes.
- `/api/file/resolve-link`: resolves a Markdown link target against a Markdown file path and returns a served-root-relative file path for viewer navigation, plus a stat-based `content_hash`/version when the target exists as a file. Markdown image rendering no longer calls this for every image during initial render; the frontend resolves simple relative image dependencies locally and lets `/api/file/raw?base=...` resolve actual image requests lazily.
- `/api/file/resolve-directory-link`: resolves a local link target against a served-root-relative directory, used by Codex session transcript links whose paths are relative to the session cwd.
- `/api/git/status`, `/api/git/diff`, `/api/git/stage`, `/api/git/revert`, `/api/git/commit`, and `/api/git/push`: expose Git operations; status and push require the current Chat Root scope.
- `/api/config` GET/PUT: reads and writes appearance, Markdown, voice, and Super Workspace dispatcher configuration. Agent credentials and Agent-owned model configuration are deliberately outside this schema.
- `/api/config/llm-provider-states` GET: returns the persisted per-LLM-provider freeze state (frozen flag, remaining seconds, last error, failure count) for the Settings UI. `/api/config/llm-provider-states/clear` POST clears one provider's freeze (or all when no `provider_id` is given).
- `/api/events`: streams Server-Sent Events from `hub.subscribe()`.
- `/api/terminals`: lists or creates terminal sessions for the fixed owner. POST requires a non-empty cwd supplied from the active Chat Root.
- `/api/terminals/{terminal_id}` routes: snapshot, terminate, delete, and WebSocket connect.
- `/api/agents/providers`: returns registered agent providers with frontend display metadata such as name and Bootstrap icon.
- `/api/agents/inference-targets`: returns the unified read-only Agent/provider/model catalog plus persisted health/cooldown state. Each target contains a stable Viewer `target_id` and a driver-owned opaque `selection_id`; `refresh=true` bypasses the five-minute discovery cache.
- `/api/super-workspace` routes: fixed-owner (`dailing`) Super Workspace role storage, persisted message/query history, chats, and LLM dispatch. Roles have separate dispatcher-facing `description` and Agent-facing `prompt` fields and select a routing policy instead of owning a fixed runtime/model. `GET/PUT /api/super-workspace/routing` exposes the workspace routing matrix. The independent worker resolves provider cwd as required `chat.root` plus optional `role.cwd`; there is no global-directory fallback. Running targets support two-click stop confirmation in the Chat UI. Dispatch uses the active OpenAI-compatible profile from the configured config directory and only receives role descriptions, while provider sessions receive role prompts.
- Super Workspace chats are persisted in `super_workspace_chats`; every chat requires `root`, membership, and a chat-level prompt. `ChatsPanel.vue` owns creation and settings, including a directory picker for the required root and optional per-Role routing-policy overrides. Direct chats have one role; group chats may have multiple roles. The current Chat Root drives Files, Changes, Terminals, and provider execution. The Files Pinia store keeps the active directory and back stack separately per chat id for the lifetime of the frontend; switching chats saves/restores that chat's navigation, and back navigation loads the popped path without pushing the directory being left back onto the same stack. Browser layout is stored in non-namespaced localStorage.
- Every persisted Super Workspace message has a non-null worker-owned `turn_id`. A provider dispatch uses its stable `driver_run_id` as the turn id, so assistant chunks and tool activity remain attached to the same semantic turn without relying on provider-specific message ids; legacy rows are migrated once with `turn_id = message id`. The chat frontend groups visible assistant messages by `turn_id`.
- `/api/voice/ws`: optional voice-input WebSocket endpoint. The browser streams encoded audio chunks while recording. `VIEWER_VOICE_SERVICE_WS` defaults to `ws://127.0.0.1:8765/v1/voice/ws`, so the backend acts as a gateway, sends browser `ready` after connecting upstream, forwards `start`, binary chunks, and `stop` to the standalone voice transaction service, then forwards service `processing` / `partial` / `committed` / `final` / `error` messages back to the browser. If the service URL is explicitly cleared, the backend saves the chunks and runs a single full-file `faster-whisper` transcription after `stop`; a configured upstream ASR WebSocket still bypasses the in-process path.
- Mounts built frontend static files from `settings.frontend_dist_resolved`.

`backend/app/config.py`

- Central Pydantic settings object using `VIEWER_` env prefix.
- Defines `PROJECT_ROOT`, `DEFAULT_FRONTEND_DIST`.
- `Settings.root_resolved`: expanded absolute served directory.
- `Settings.frontend_dist_resolved`: expanded absolute frontend build directory.
- Important settings: `root`, `host`, `port`, `frontend_dist`, `max_text_preview_bytes`, `show_hidden`, `poll_delay_ms`, `terminal_shell`, `debug`, `log_file`.
- Voice settings: `voice_enabled`, `voice_service_ws`, `voice_backend`, `voice_backend_policy`, `voice_direct_english_translation`, `voice_min_chunk_size`, `voice_stop_timeout_seconds`, `voice_model_idle_timeout_seconds`, `voice_offline_beam_size`, `voice_offline_vad_filter`, `voice_vac`, and `voice_vad` configure process-level voice input behavior. `~/.view/config.json` `voice` controls user-facing voice enablement, model choices, selected model, source language, translation enablement, and target language; defaults are `large-v3-turbo`, `auto`, and translation off. `voice_service_ws` defaults to `ws://127.0.0.1:8765/v1/voice/ws` and points `/api/voice/ws` at the standalone voice transaction service while keeping the browser connected to viewer; `voice_upstream_ws` bypasses the in-process offline path and proxies microphone audio to a separate streaming ASR WebSocket.

`backend/app/storage.py`

- Central viewer-local paths split between `VIEWER_CONFIG_DIR` and `VIEWER_DATA_DIR`; each falls back through `VIEWER_HOME` to `~/.view`. Configuration is read from the config directory, while SQLite state, logs, and provider session metadata live under the data directory. This permits isolated worktree/test instances without copying secrets into a database directory or touching the active instance.
- Defines `CONFIG_DIR`, `DATA_DIR`, `CONFIG_PATH`, `LOG_DIR`, `CODEX_APP_SERVER_LOG_DIR`, `HERMES_LOG_DIR`, `OPENCODE_LOG_DIR`, `TERMINAL_LOG_DIR`, `HERMES_RUN_DIR`, and `WEAVER_RUN_DIR`.
- `WEAVER_RUN_DIR` defaults to `/tmp/viewer_run/weaver` and can be overridden with `VIEWER_WEAVER_RUN_DIR`; it stores the active `worker.pid/json`, a per-worker-owner PID/state slot keyed by the DB `claimed_by` id, and provider driver process state. Worker startup uses graceful leadership handover: a new worker overwrites the active slot, while the old worker stops claiming and drains its in-flight Agent runs before exiting. The startup orphan sweep first checks whether each run's owning worker PID is alive; this protects in-process Hermes turns, which have no separate driver PID, from being marked interrupted while an old worker is legitimately draining.
- `HERMES_LOG_DIR` stores viewer-local Hermes-to-ACP session metadata only; message history lives in `~/.view/agent-history.sqlite3`. `HERMES_RUN_DIR` is reserved for Hermes detached-run state if the provider implementation later needs local runner files.
- `OPENCODE_LOG_DIR` stores viewer-local OpenCode-to-ACP session metadata only; OpenCode's own session storage remains provider-owned and is not read by Viewer.

`backend/app/identity.py`

- Defines the single persistence owner `dailing`. Legacy internal metadata columns retain this value, but it is not user-selectable and is not exposed as an API parameter.

`backend/app/files.py`

- File tree, path normalization, metadata, upload/delete helpers, content reading, and `~/.view/config.json` persistence.
- `normalize_relative(path)`: converts slashes, strips leading/trailing slashes, rejects `..` path segments.
- `served_root()`: returns the fixed `VIEWER_ROOT` filesystem boundary.
- `resolve_path(path)`: joins normalized paths to the fixed boundary and rejects absolute paths or symlink resolutions outside it.
- `resolve_markdown_link(base_path, target, user_id)`: resolves local Markdown image/link targets relative to the Markdown file, supports absolute/file URLs, strips common editor `:line[:column]` suffixes, and returns absolute paths when targets live outside the active user's root.
- `resolve_directory_link(base_dir, target, user_id)`: resolves local file links relative to a directory, supports absolute/file URLs, strips common editor `:line[:column]` suffixes, and returns absolute paths when targets live outside the active user's root.
- `resolve_served_directory(path, label)`: resolves a required working directory for Terminal/Codex/Hermes launches. Empty or missing directories are rejected; there is no fallback.
- `relative_for(path, user_id)`: returns path relative to the active user's root when possible; symlink targets or external paths may become absolute if outside root.
- `guess_mime(path)`: MIME type from filename.
- `preview_kind(path, mime, size)`: maps file extension/MIME to `image`, `markdown`, `html`, `pdf`, `text`, or `unsupported`; `.env`, `.env.*`, and `*.env` files are treated as text even when MIME guessing is inconclusive.
- `content_hash(path)`: computes SHA-256 for cache tagging.
- `metadata_version(path, stat)`: returns an mtime/size version token used instead of SHA-256 for oversized text-style previews so metadata reads do not scan very large files.
- `entry_for(path)`: builds `FileEntry` for a directory child.
- `list_directory(path)`: validates directory, filters hidden files when configured, sorts directories first.
- `upload_target(directory, filename)`: validates a target upload directory and basename-only filename, rejects directory overwrite, and returns the filesystem path that the route streams into.
- `delete_file(path, user_id)`: deletes a single file under the active user's root; directories are rejected to avoid recursive destructive actions from the sidebar.
- `get_meta(path)`: validates file and returns `FileMeta`, including preview type, text-size limit flag, and `content_hash`; oversized text-style previews use the mtime/size version token instead of a full-file hash.
- `read_text(path)`: reads UTF-8 with replacement fallback; rejects oversized text previews.
- `read_text_lines(path, start_line, count)`: validates text-style preview files, builds/caches newline byte offsets by path/mtime/size, and returns a bounded UTF-8 line window for frontend virtual scrolling.
- `config_path()`: returns `~/.view/config.json`.
- `read_config()` / `write_config(config)`: load and save appearance, voice, Markdown, and Super Workspace config; routing schema version 3 rewrites legacy routing targets and removes obsolete Viewer-owned Codex/provider-account settings.

`backend/app/events.py`

- Server-Sent Events fanout for filesystem changes.
- `EventHub.publish(event)`: pushes a `WatchEvent` to every subscriber queue and drops full queues.
- `EventHub.subscribe()`: async generator yielding `ready` then `file-change` SSE messages.
- `hub`: singleton used by `main.py` and `watcher.py`.

`backend/app/ws_clients.py`

- WebSocket client queue/fanout helpers for terminal sessions.
- `WebSocketClient`: websocket, outgoing queue, and writer task.
- `add_client()`, `enqueue()`, `broadcast()`, `remove_client()`, and `client_writer()`: JSON message queueing, stale-client cleanup, timeout-bounded writes, and socket close handling used by terminal sessions.

`backend/app/git_diff.py`

- Git working-tree integration for the Diff sidebar and viewer.
- Resolves the active Git context from the requested file/sidebar directory, matching the current Files directory model used by Terminal and Codex launches. Status queries are scoped to that directory, while returned paths are mapped back to served-root-relative paths for the frontend.
- `git_status()`: parses `git status --porcelain=v1 -z`, adds `git diff --numstat HEAD` counts, marks binary files, and returns relative paths for changed files.
- `git_diff(path)`: returns a unified text diff against `HEAD`; untracked text files are rendered as new-file diffs and binary files return `is_binary=true` with no diff text.
- `git_stage(path)`, `git_revert(path)`, `git_commit(request)`, and `git_push()`: implement the toolbar Git actions. Revert removes only untracked files, refusing untracked directories.

`backend/app/watcher.py`

- Watches the fixed `settings.root_resolved` boundary with non-recursive `watchfiles.watch` in a worker thread.
- `event_type(change)`: converts `watchfiles.Change` enum to API strings.
- `is_ignored_path(path)`: ignores high-churn `__outputs` directories while the `watchfiles` default filter ignores common development directories such as `.git`, `.venv`, and `node_modules`.
- `watch_root(stop_event)`: debounced watch loop; publishes `WatchEvent` with type, best-match root-relative path, directory flag, and mtime.

`backend/app/terminals.py`

- Interactive shell session manager using OS PTYs, async tasks, and WebSockets.
- `TerminalSession`: PTY process state, buffered output, per-session output log path, current PTY rows/cols, shared layout lock state, connected clients, locks, and tasks.
- Terminal metadata retains the fixed `dailing` owner. New terminals require an explicit Chat Root cwd.
- `TerminalSession.snapshot()`: full state fields; reconnect snapshots load output from the per-session on-disk log.
- `TerminalSession.summary()`: list-friendly state without output.
- `TerminalManager.list()`: sorted summaries.
- `TerminalManager.get(id)`: returns a session or 404.
- `TerminalManager.create(cwd)`: opens PTY, starts configured shell in the requested served-root-relative directory, falls back to root when unavailable, and launches reader/wait tasks.
- `terminate(id)`: asks process group to exit.
- `delete(id)`: terminates, cancels tasks, closes FD, removes session, broadcasts deletion.
- `connect(id, websocket)`: accepts WebSocket, sends snapshot, handles input/resize messages, removes client on disconnect.
- `write(session, data)`: writes encoded input to PTY with a lock.
- `resize(session, rows, cols, lock)`: updates PTY window size, records rows/cols, updates lock state, and broadcasts `layout` updates.
- `shutdown()`: deletes all sessions.
- `_read_output(session)`: reads PTY output, appends to disk log, caps in-memory buffer at `MAX_OUTPUT_CHARS`, broadcasts output with version.
- `_initialize_log()`, `_append_log()`, `_read_log()`, `_snapshot_for_client()`, `_remove_log()`: manage per-session terminal output log files used for reconnect replay.
- `_wait_for_exit(session)`: waits for process and broadcasts status.
- `_terminate_process(session)`: SIGHUP, then SIGTERM, then SIGKILL fallback.
- `_broadcast()`, `_remove_client()`: thin wrappers around `ws_clients.py` fanout and cleanup.
- `_set_size(fd, rows, cols)`: low-level `TIOCSWINSZ`.
- `terminal_manager`: singleton used by routes.

`backend/app/acp_runtime.py`

- Provider-neutral ACP client and stdio process runtime. `ACPProcessConfig` supplies provider id, executable, arguments, enabled state, profile, and YOLO metadata; adding another conforming local ACP agent requires a process adapter rather than another protocol implementation.
- ACP subprocess stdout uses a finite 16 MiB JSONL line limit, large enough for patch/edit permission payloads while remaining bounded; the runtime also treats an exited SDK receive task as unhealthy so the next operation discards and recreates the process. Compact event persistence and log-output limits are applied separately after protocol parsing.
- Performs initialize/capability negotiation and implements new/load/list/fork/resume/prompt/cancel/close/model/mode. It validates ACP ContentBlocks, capability-gates images/audio/resources, drains subprocess stderr, and deliberately declines client-hosted filesystem and terminal capabilities.
- Exposes one provider-overridable `set_model(session_id, model)` operation. The provider-neutral implementation uses the current ACP `session/set_config_option` model option and treats the selection value as opaque; provider adapters override only when their ACP implementation requires a legacy or dedicated method.
- The shared ACP client auto-approves Agent permission requests, preferring `allow_always` and falling back to `allow_once`; it cancels only when the Agent supplies no allow option. Provider-side explicit deny rules remain authoritative because they do not produce an approvable request.
- A missing `session/load` result—including the all-default response object produced when ACP SDK 0.9 deserializes a JSON-RPC null—is treated as missing and is never marked as bound.

`backend/app/acp_sessions.py`

- Provider-neutral Viewer session manager layered over `ACPRuntime`. It owns Viewer/provider session-id mapping, cwd/model validation, local metadata, prompt submission, list/fork/resume/cancel, status, usage, and structured ACP update normalization.
- ACP prompt/RPC failures are terminal provider failures: partial or error text remains visible and is finalized in history, while the Viewer session, driver target, and parent run leave `running` as `failed`. The Viewer ACP layer does not retry or replay provider prompts; retry policy remains owned by each Agent.
- ACP `session/update` notifications are the sole provider-event source. Agent/thought chunks are coalesced per message: chunks append to the turn's open stream event, and once a tool call or another stream follows, the stream is sealed and a fresh event starts, so consecutive messages interleave with tool activity in chronological order (each event keeps its first chunk's timestamp). Tool call/update pairs upsert one tool event with structured diff/file-change data, and plan/mode/config/usage/session/command updates refresh session metadata.
- Every live raw ACP update is archived before parsing, and every normalized update is written/upserted directly into `~/.view/agent-history.sqlite3` and announced through Super Workspace SSE. Provider-history replay during `session/load` is not re-archived. The manager never opens or synchronizes a provider-private database; historical chat reads use the Viewer history DB.

`backend/app/codex_app_server.py` and `backend/app/codex_app_server_sessions.py`

- Codex-native App Server provider over JSONL stdio (not ACP), and the only supported Codex driver. Each subprocess connection completes `initialize` and the `initialized` acknowledgement before thread methods, then uses `thread/start` / `thread/resume`, `turn/start` / `turn/interrupt`, and waits for `turn/completed` before leaving `running`. `model/list` supplies the live target catalog; the chosen opaque model id is passed to both thread creation and every turn so a thread can change model without Viewer maintaining a static model list.

`backend/app/inference.py` and `backend/app/driver_catalog.py`

- Define the shared read-only inference target contract and structured failure contract. The scheduler's minimum unit is `(agent_id, provider_id, model_id)` with a stable `target_id`; `selection_id` remains opaque outside its concrete driver.
- Hermes discovery reads the selected Hermes profile's own `config.yaml`, provider-key presence from `auth.json`, and Hermes model caches without reading credential values or modifying Agent state. OpenCode discovery calls its read-only `models` CLI. Codex App Server discovery uses its formal `model/list` RPC.
- Viewer does not create provider accounts, write Agent credentials, or rewrite Agent model/channel/memory configuration. Catalog discovery failures become warnings and do not mutate lower-layer state.
- `VIEWER_CODEX_APP_SERVER_YOLO` defaults to `true`. In that mode every `turn/start` explicitly sends `approvalPolicy: never` and the App Server `dangerFullAccess` sandbox policy, the protocol-native equivalent of bypassing approvals and sandboxing; setting the environment variable to false leaves permission policy to normal Codex configuration.
- Any JSON-RPC control-request timeout closes and discards the entire App Server subprocess because its request/connection state is no longer trustworthy; the next operation starts and initializes a fresh process instead of cascading the timeout into later requests.
- App Server stdio uses a finite 16 MiB JSONL line limit instead of asyncio's 64 KiB default because completion notifications can repeat aggregated command output. A stdout reader failure immediately fails pending RPC/turn waiters, marks the connection unhealthy, and terminates the subprocess; process cleanup still runs when a reader task has already failed. Viewer tool-event text is capped at 32 KiB and unknown-notification/debug logging records identifiers and payload size rather than recursively logging complete tool output.
- Normalizes the current slash-form Codex notifications (`item/agentMessage/delta`, reasoning/command deltas, aggregated `turn/diff/updated` patches, `thread/tokenUsage/updated`, and `turn/completed`) into Viewer AgentEvent IR while retaining the older item-level file-change notification for compatibility. Deltas with the same Codex `itemId` upsert one streaming event; repeated turn-diff snapshots replace the prior patch event and streaming events are finalized when the turn completes.
- Archives every complete Codex App Server notification, including unknown methods and payloads larger than the normalized-message preview limit, before attempting event normalization so later parsers can replay historical raw data from the Viewer DB.
- Viewer does not implement Codex App Server client-side approval or interactive-input requests; unsupported server requests receive an explicit JSON-RPC method error instead of hanging. Provider/model retry remains owned by Codex.

`backend/app/hermes_acp.py` and `backend/app/hermes_sessions.py`

- Thin Hermes registration layer over the shared ACP runtime/session manager. It supplies `hermes`, `-p <profile> [--yolo] acp`, the Hermes metadata directory, and legacy Viewer-metadata key migration.
- Overrides the shared model-selection operation to call Hermes' dedicated `session/set_model` method; Hermes accepts opaque `provider:model` selections there but does not apply its model through the generic ACP config option.
- ACP is enabled by default through `VIEWER_HERMES_ACP_ENABLED`; `VIEWER_HERMES_PROFILE` defaults to `default`, `VIEWER_HERMES_COMMAND` defaults to `hermes`, and `VIEWER_HERMES_YOLO` defaults to `true`. YOLO affects only the Viewer-owned subprocess and does not change Hermes gateway/profile configuration.
- `hermes_session_manager` remains the compatibility singleton used by Super Workspace. Hermes private `state.db` is owned solely by Hermes and is never read by Viewer.
- Hermes itself is not patched by Viewer. Because the current Hermes ACP adapter can encode terminal model failure as an `end_turn` message (or an empty `end_turn`) instead of a failed RPC/stop reason, the thin Viewer Hermes session adapter recognizes those terminal message shapes and marks the turn failed; retry remains entirely inside Hermes.
- Uses the official `agent-client-protocol` Python SDK pinned to the Hermes-compatible `0.9.x` line.

`backend/app/opencode_acp.py` and `backend/app/opencode_sessions.py`

- Thin OpenCode registration layer over the shared ACP runtime/session manager. It starts `opencode acp`, stores only Viewer session metadata under `OPENCODE_LOG_DIR`, and supports the same create/load/list/fork/resume/prompt/cancel flow negotiated from OpenCode's ACP capabilities.
- ACP is enabled by default through `VIEWER_OPENCODE_ACP_ENABLED`; `VIEWER_OPENCODE_COMMAND` defaults to `opencode`. OpenCode ACP does not expose a CLI auto-approval flag; its explicit deny rules remain governed by OpenCode configuration, while ask-style permission requests are auto-approved by Viewer's shared ACP client.

`backend/app/voice.py`

- Optional WebSocket bridge for voice input.
- `VIEWER_VOICE_SERVICE_WS` mode makes `connect_voice(websocket)` a gateway: it sends browser `ready` once the upstream connection is open, forwards browser `start`, binary audio chunks, and `stop` to the standalone voice transaction service, enriches `start` with the current viewer voice model/language/translation/offline settings, and forwards service `processing`, `partial`/`committed`, `final`, and `error` payloads back to the frontend. In this mode viewer does not transcribe or save a duplicate audio capture. Voice service `partial.text` and `final.text` are full transcript state, not deltas.
- `VoiceCapture`: saves the browser-sent audio chunks for each session under `~/.view/logs/voice/`, using a UTC finish-time filename plus a JSON sidecar with MIME type, size, chunk count, backend, and backend policy.
- Lazily loads a singleton offline `faster_whisper.WhisperModel` for the first in-process final transcription, keeps it warm across nearby dictations, switches models when the persisted voice model changes, and unloads it after `voice_model_idle_timeout_seconds` of inactivity to release GPU resources.
- `connect_voice(websocket)`: reads the current persisted voice config, accepts browser audio chunks from `/api/voice/ws`, and chooses the configured voice path. `voice_service_ws` takes precedence and proxies to the standalone voice transaction service; otherwise the local offline path saves chunks during recording, runs full-file transcription after `stop`, and returns `processing` then `final` transcript JSON to the frontend. When `voice_upstream_ws` is configured and no service URL is set, it proxies audio to the upstream ASR WebSocket and normalizes upstream messages.
- `_connect_whisperlivekit(websocket)`: creates one WhisperLiveKit `AudioProcessor` per browser voice session, forwards binary audio frames into it, saves the transmitted audio, and streams normalized result-state updates back to the client. On client stop, it flushes the processor and waits up to `voice_stop_timeout_seconds` for final model output before closing.
- `_connect_offline_voice(websocket)` / `_transcribe_offline(audio_path)`: save the streamed WebM/MP4 chunks, then transcribe the completed file with `faster-whisper` using `voice_offline_beam_size`, `voice_offline_vad_filter`, and the configured voice language; when translation is enabled with target `en`, the offline path uses Whisper's native `translate` task.
- `_normalize_upstream_message(message)` / `_normalize_payload(payload)`: accept common streaming ASR response shapes plus WhisperLiveKit `lines` / `buffer_transcription` / `buffer_translation` state and normalize final/partial text according to the active voice translation setting.
- `_whisper_kwargs()`: normalizes WhisperLiveKit options and disables target-language translation with a warning when `language=auto` and `voice_backend_policy` is not `simulstreaming`, because WhisperLiveKit rejects that translation configuration.

`backend/app/models.py`

- Pydantic API schemas: `FileEntry`, `DirectoryListing`, `FileMeta`, `ConfigData`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `WatchEvent`, `TerminalInfo`, `TerminalCreate`, and `TerminalSnapshot`.
- `ConfigData` stores appearance, Markdown, voice, and Super Workspace settings; it has no Agent credential/model configuration or user-profile fields. `SuperWorkspaceConfig.provider_context_limits` stores Agent-level context recycle percentage/token defaults, with optional Role settings taking precedence. `SuperWorkspaceConfig.chat_show_agent_activity` controls whether the chat requests compact tool/reasoning/edit activity rows in addition to user and final assistant messages. `SuperWorkspaceConfig.dispatch_profiles` is the ordered Viewer LLM provider chain consumed by `llm_client.chat_completion()` (first enabled non-frozen entry wins; each entry has id/name/api_url/model/api_key/enabled), and `llm_provider_freeze_seconds` sets the per-provider failure freeze window (default 3600). `SuperWorkspaceConfig.turn_summary_*` controls per-turn summarization (enable toggle, per-tool-call/file-change character budget, generation timeout), and `SuperWorkspaceConfig.context_bridge_*` controls summary/Hindsight recall injection on new sessions and role switches (enable toggles, max summaries, recall token cap). `active_dispatch_profile_id` is a deprecated legacy field kept only for config compatibility; chain order, not this id, decides priority.
- `AgentEventType` is the fixed backend intermediate-representation enum shared by provider parsers. Current values are `message:assistant`, `reasoning`, `tool_call`, `tool_result`, `custom_tool_call`, `exec_command_begin`, `exec_command_end`, `function_call`, `function_call_output`, `custom_tool_call_output`, `view_image_tool_call`, `patch_apply_end`, and fallback `operation` for logged-but-unclassified visible events. Provider drivers convert native logs into these values before writing Super Workspace history.
- These should stay aligned with TypeScript interfaces under `frontend/src/types/`.

`backend/app/super_workspace.py`

- Per-user Super Workspace role manager backed by `super_workspaces`, `super_workspace_user_state`, and `super_workspace_roles` rows in the configured data directory's `agent-history.sqlite3`. The single workspace is named `Default Super Workspace` and uses user-scoped id `{user}:default`; `super_workspace_user_state` records the active chat id for each user.
- Defines local API models for `SuperWorkspaceData`, `SuperRole`, routing config, role update payloads, and dispatch responses. Routing profiles live in Viewer config and contain ordered snapshots of discovered Agent/provider/model targets; no credential references are stored.
- `SuperWorkspaceManager.read()` / `update()` / create/update/delete role methods persist the common prompt, dispatcher-facing role descriptions, Agent-facing role prompts, profile selection, and capability requirements. Effective profile precedence is Chat-Role override, then Role profile, then workspace default. Candidate eligibility applies enabled state and declared tools/filesystem/context-window capabilities. Dispatcher role JSON/table rendering includes `description` but intentionally excludes `prompt`. Provider session refs are not role fields; current reusable sessions are owned by chat+role+Agent state rows in the history DB; changing a role prompt or route clears those mappings so the next dispatch creates a session with the new rules.
- `_ensure_role_routing_migration()` is the one-time legacy conversion: each distinct existing `(provider, model)` pair becomes a shared single-target policy and every Role receives a policy id. Runtime routing does not fall back to legacy Role provider/model fields after migration.
- `dispatch()` reads role descriptions, calls the Viewer LLM provider chain (`llm_client.chat_completion()`) with JSON response format, validates returned role ids against the candidate roles, and returns the selected ids/rationale. It intentionally only routes messages; the backend runtime owns creating/resuming the actual role agent sessions for normal Super Workspace dispatch.

`backend/app/super_workspace_runtime.py`

- DB-backed Super Workspace dispatcher and independent dispatch-task worker process.
- Defines `SuperWorkspaceMessageCreate` for new user/role-originated query messages. The payload carries optional structured `role_ids`, ACP-compatible `content_blocks`, plus optional `parent_message_id` and `sender_role_id` lineage fields. Structured blocks are persisted in the query message raw JSON and routed to Hermes without requiring Viewer-hosted filesystem or terminal methods.
- `SuperWorkspaceRuntime.submit()` parses leading `@Role ` query prefixes against all roles in the active Super Workspace, merges them with structured `role_ids`, persists the query message without the dispatch prefix, auto-dispatches through `SuperWorkspaceManager.dispatch()` using only the active chat's member roles when no explicit targets are supplied, marks the run queued, and writes one queued `super_workspace_driver_runs` dispatch-task row per concrete role id. Role mention keys are derived from role names using ASCII variable-name characters so frontend insertion and backend parsing agree.
- The worker resolves the effective routing profile when it claims each task, walks eligible targets in configured order, and preserves one worker-owned turn id across attempts. Normal tasks serialize per Chat+Role; an explicit `parallel_dispatch` task is claimable beside an active task only when it also forces a new session. That parallel session receives the normal bounded visible-chat bootstrap and becomes the canonical reusable Chat+Role session as soon as dispatch succeeds, so later normal queries continue from it. Older concurrent runs may finish and persist messages but cannot reclaim the session mapping. Drivers normalize failures into query/target/provider/Agent scope plus retry/failover safety. Credit, rate-limit, auth, and model-availability failures may advance only when safe; request/refusal and unknown possibly-side-effecting failures stop. Health/cooldowns are persisted in `super_workspace_target_health`; provider failures block every model under the same Agent/provider until expiry. Failover starts a new lower-layer session while retaining the Viewer turn id. The immutable target snapshot and every attempt outcome are persisted on `super_workspace_driver_runs` for audit/display.
- The backend runtime does not run the dispatch loop in-process. On startup it ensures a separate `super_workspace_worker.py` process is alive, with PID/state registered in `WEAVER_RUN_DIR`; startup refuses a live `worker.pid`, warning-logs and overwrites stale pid files, and direct worker invocation applies the same guard. The worker claims queued dispatch-task rows with a lease, skips concrete role ids that already have claimed/running tasks, and requeues tasks when that role id's current provider session is still running. Claimed tasks move through `claimed`, `running`, and terminal `completed`/`failed` states, and the parent query status is summarized from its target task statuses. The worker sends lightweight HTTP notifications to `/internal/super-workspace/notify` so the backend SSE stream can prompt frontend refreshes.
- `SuperAgentDriver` is the Agent-driver base. It exposes `list_targets()`, error normalization, and session dispatch. It checks the current chat+role+Agent backing session from `super_workspace_chat_role_sessions`, creates a clean lower-layer session when missing/stale/cwd-or-selection-mismatched or when context usage reaches the Role override / configured Agent percentage or token limit, and treats `selection_id` as opaque. Queueing is represented exclusively by DB task rows.
- `CodexAppServerSuperDriver`, `HermesSuperDriver`, and `OpenCodeSuperDriver` implement discovery and dispatch for their native protocols without modifying Agent configuration.
- `SuperWorkspaceEventHub` streams lightweight run-created/run-updated notifications through `/api/super-workspace/events`; the history DB remains the source for actual messages, and the frontend uses the display item `updated_at` cursor with `/api/super-workspace/runs?after=...` to fetch changed flat items instead of reloading the newest page after every event.

`backend/app/llm_client.py`

- The project's single OpenAI-compatible chat-completions interface. Internal consumers (the Super Workspace dispatcher today, the chat-context summarizer later) call `chat_completion()` instead of talking to one hard-coded endpoint.
- Providers come from `super_workspace.dispatch_profiles` in Viewer config; list order is priority. The first enabled, non-frozen provider handles each call. A provider that fails (HTTP error, connection error, or malformed/empty response) is frozen for `super_workspace.llm_provider_freeze_seconds` (default 3600) and the chain falls through to the next provider, mirroring the agent target health/cooldown semantics of `super_workspace_target_health`.
- Freeze state persists in `VIEWER_DATA_DIR/llm-provider-health.json` so restarts do not reset cooldowns; expired entries are pruned on write. `provider_states()` feeds the Settings UI, `clear_provider_state()` backs the clear endpoint.
- API key resolution order: profile `api_key`, then `VIEWER_SUPER_DISPATCH_API_KEY` / `VIEWER_OPENAI_API_KEY` / `OPENAI_API_KEY` from process env, then the same keys from `.viewer.env`. An empty profile `model` triggers one-shot discovery against the provider's `/v1/models`.
- `LLMResult` reports the winning provider/model, latency, and per-provider attempt outcomes (skipped-disabled, skipped-frozen, error) so callers can surface failover behavior; `LLMChainError` carries the same attempt list when every provider fails.

`backend/app/super_workspace_memory.py`

- Hindsight integration for Super Workspace chat-level memory. It reads `VIEWER_HINDSIGHT_API_URL` / `VIEWER_HINDSIGHT_API_TOKEN`, falling back to `~/.hindsight/codex.json`, and writes visible chat messages to Hindsight with short timeouts so memory failures do not block dispatch. `recall_chat_memories()` runs a query-aware Hindsight recall against the chat bank (budget `mid`, default 10s timeout, failures return an empty list) and is used by the context bridge; the Hindsight daemon's own extraction LLM is the local Ollama `gemma4:26b` via its OpenAI-compatible endpoint.
- Memory banks are chat-scoped only: `{prefix}::{user_id}::{workspace_id}::chat::{chat_id}`. The prefix defaults to `super-workspace` and is configurable in `~/.view/config.json` `super_workspace.hindsight_bank_prefix`.
- `retain_visible_message()` posts one visible query/final-answer message as an async Hindsight memory item with metadata and tags.

`backend/app/turn_summary.py`

- Per-turn summarization and context-bridge building for Super Workspace. `generate_turn_summary()` is fired as a background task when a dispatch turn completes: it pulls the turn's `super_workspace_messages` rows (query, assistant messages, tool-call/tool-result/patch events truncated per event to `turn_summary_tool_char_budget` characters, reasoning/plan/session-update events skipped) plus linked file changes (diffs truncated likewise), asks the Viewer LLM provider chain for a four-section Chinese summary, and stores it in `super_workspace_turn_summaries` keyed by turn id with provider/model provenance, latency, and source sizes. Turns with no assistant content are skipped.
- `build_turn_summaries_section()` picks summaries newest-first until the `context_bridge_summary_char_budget` character budget is exhausted (the newest summary is always kept, truncated if it alone exceeds the budget), so a fixed budget covers as many turns as possible; output is chronological.
- `build_unsummarized_tail_section()` covers the gap between the newest completed summary and now with raw visible messages (budget `chat_history_bootstrap_tokens`): summaries are generated asynchronously and may lag or fail. Chats with no summaries yet fall back to a plain recent-history tail.
- `build_new_session_context()` composes the full history bootstrap prepended when a fresh lower-layer session is created: budgeted turn summaries, the unsummarized-gap tail, and when `context_bridge_hindsight_enabled` a query-aware Hindsight recall section. `SuperAgentDriver.chat_history_prompt()` is a thin wrapper over it.
- `build_role_switch_bridge()` handles reused sessions: when the chat saw activity from other roles after this role's last assistant message, it prepends a "While you were away" bridge containing summaries of those unseen turns (excluding the requesting role's own turns, which its session already saw), the unsummarized-gap tail, plus the recall section. `SuperWorkspaceRuntime._dispatch_candidate()` invokes it only when the Chat+Role session will actually be reused.

`backend/app/agent_history.py`

- SQLite-backed agent history index stored at `VIEWER_DATA_DIR/agent-history.sqlite3` (legacy default `~/.view/agent-history.sqlite3`). It is the history source for Super Workspace chats.
- `super_workspace_provider_raw_events` is an append-oriented replay archive for complete provider notifications. Rows retain provider/session/turn and workspace/chat/query/driver lineage, event method/order/time, payload byte size, and the full JSON before normalization; normalized visible messages remain in `super_workspace_messages`.
- The viewer-owned history DB is accessed through SQLAlchemy ORM mapped rows and per-operation sessions in `AgentHistoryStore`; its SQLite engine uses `NullPool` so each session closes its DB connection after use. Provider-private history stores are not Viewer data sources.
- Defines `super_workspaces`, `super_workspace_user_state`, `super_workspace_roles`, `super_workspace_chats`, `super_workspace_chat_pins`, `super_workspace_chat_role_sessions`, `super_workspace_messages`, `super_workspace_driver_runs`, `super_workspace_target_health`, `super_workspace_message_file_changes`, `super_workspace_message_citations`, `super_workspace_turn_summaries`, and `super_workspace_driver_checkpoints`. Roles store profile ids and capability requirements; chats store per-Role profile overrides. Driver runs store the effective profile id, immutable execution-target JSON, and ordered attempt JSON. Target health uses Agent/provider/target scoped keys and expiry timestamps. Turn summaries store one LLM-condensed summary per completed turn with provider/model provenance (see `backend/app/turn_summary.py`).
- Super Workspace lineage is stored directly on messages: messages carry `parent_message_id`, `sender_role_id`, and `recipient_role_id`; provider output rows associated with a driver run also carry `query_message_id` and `driver_run_id`. Current UI presents non-empty `query` messages as runs, but the persisted shape can evolve into a query/message graph without a separate query table.
- `create_super_run()` records each Super Workspace user query as a `super_workspace_messages` row with explicit `workspace_id`, empty display `text`, non-empty `query`, selected role ids stored on that same row so direct and auto dispatch can return selected ids before driver runs exist, ordered citation edges written to `super_workspace_message_citations`, and a background Hindsight retain for that visible chat-level query when enabled.
- `record_super_target()` creates a queued dispatch-task row before any provider session is started. The worker later fills `session_ref`, `viewer_session_id`, `agent_prompt`, and context usage fields when it claims and starts the task; it also upserts the chat+role session state row.
- `claim_next_dispatch_task()` leases one queued task whose concrete chat+role pair has no claimed/running task, making session serialization DB-backed rather than process-memory-backed. Stale claimed leases are returned to queued, while running tasks keep that chat+role session occupied until the worker marks them completed/failed. The same role may run independently in different chats because session state is keyed by chat+role.
- `list_super_runs()` / `get_super_run()` return DB-only lazy pages of non-empty-query messages with dispatch-task targets. Provider message rows are selected by explicit `workspace_id` / `driver_run_id` lineage only. Reads do not reopen Codex rollout JSONL, Hermes state, or infer message ownership from prompt/time windows.
- `visible_chat_history_context()` walks backward through the current chat's frontend-visible messages, meaning query rows plus assistant `message:assistant` rows, and builds an oldest-to-newest prompt block capped by the rough token budget in `super_workspace.chat_history_bootstrap_tokens`. Optional `after_time`/`before_time` bounds restrict the window; the turn-summary context builders use `after_time` to cover only messages newer than the newest completed summary (the unsummarized gap).
- `latest_turn_summary_time()` returns the newest `occurred_at` among completed summaries in one chat, used as the lower bound of the unsummarized-gap tail.
- Provider message rows are inserted by the active ACP/App Server session manager as output arrives. Super Workspace dispatch passes query, dispatch-task, parent, sender, and recipient ids so every normalized event is directly linked to its Viewer turn. `AgentHistoryStore` does not reopen provider-private history as a read fallback.
- ACP event persistence namespaces each provider event id with the Viewer-owned `driver_run_id` (falling back to `query_message_id`). This keeps streaming updates within one dispatch idempotent while preventing a restored provider session from overwriting an earlier dispatch when its turn/message counters restart. Session-load history replay may refresh usage/config metadata but is excluded from current-turn message events, so replayed history cannot be concatenated into the next visible reply.

`backend/app/logging.py`

- Loguru setup and standard logging interception.
- `InterceptHandler.emit(record)`: redirects Python logging records into Loguru.
- `current_log_path()`: resolves `VIEWER_LOG_FILE`.
- `configure_logging(log_file, debug)`: configures stderr and rotating file logs, intercepts uvicorn/FastAPI error logs, and disables uvicorn access logs because app middleware already records failed/slow requests.
- `ensure_logging()`: idempotent fallback used by app import.

`backend/app/restart.py`

- Thin admin bridge from FastAPI to the Viewer supervisor or the standalone fallback manager.
- Under the supervisor, restart/stop launches a delayed control helper so the HTTP response can flush, then sends SIGHUP/SIGTERM to the stable supervisor PID inherited through `VIEWER_SUPERVISOR_PID`.
- Outside the supervisor, restart preserves the active Python executable and `run.py` argv instead of falling back to debug/reload/build parameters.

`scripts/supervise_viewer.py`

- Stable systemd MainPID for the single Viewer cgroup.
- Starts the backend with the exact command supplied after `--` and exports its own PID to the child.
- SIGHUP gracefully stops only the backend child and launches a replacement generation; worker processes stay alive for leadership drain. SIGTERM stops the child and exits so systemd can clean the complete cgroup.

`deploy/systemd/viewer.service`

- Canonical user-service template for the single Viewer cgroup. Its `ExecStart` runs the supervisor followed by the production backend command, and `KillMode=control-group` keeps an explicit systemd restart as the hard whole-generation cleanup path.

`backend/app/__init__.py`

- Empty package marker.

`backend/tests/test_super_workspace_role_prompts.py`

- Standard-library unit tests for the strict data boundary between dispatcher descriptions and Agent prompts, plus session invalidation when a prompt changes.

## Frontend Structure

`frontend/src/main.ts`

- Imports Bootstrap, icons, KaTeX, Highlight.js, and app CSS.
- Creates Vue app, installs Pinia, mounts `App.vue`.

`frontend/public/favicon.svg`

- Browser tab icon for the Vue app. Vite copies files from `frontend/public` into the built frontend root, and `frontend/index.html` links this SVG favicon directly.

`frontend/src/App.vue`

- Root shell: full-page settings and the Super Workspace split-pane shell as the normal page. There is no global navbar or profile selection screen.
- Defaults directly to Super Workspace; Settings is opened by the activity-rail button emitted through `FileSidebar.vue` and `SuperWorkspacePage.vue`.
- Loads config, the root file listing, and terminal lists before mounting the Super Workspace page.
- Applies appearance and active Markdown theme settings from `stores/files.ts` as CSS variables on the app shell, including pane-titlebar/icon size and Markdown/syntax colors.
- Connects SSE with `connectEvents()`.
- Refreshes affected file listings on change events.
- Dispatches every filesystem SSE as `viewer:file-changed`; individual panes filter these events against their own file path or viewer-specific dependency set.
- Polls terminal list every 15 seconds with overlap guarded in `stores/terminals.ts`.
- Pane toolbar rendering and pane navigation actions live in `PaneTitleBar.vue`, not the app shell. `stores/layout.ts` keeps each pane's local content history so a pane can return from a linked file, sidebar-opened file, diff, terminal, or agent session to its previous content.
- File viewer scroll memory lives in `utils/scrollMemory.ts` and `composables/useScrollMemory.ts`. Scroll positions are browser-local and keyed by pane id and file path; layout navigation emits `viewer:pane-before-navigate` before replacing pane content.
- Agent provider/session list is loaded on startup and polled every 15 seconds through `stores/agents.ts`.

`frontend/src/components/SuperWorkspacePage.vue`

- Default full-page Super Workspace split-pane shell.
- Loads the user's single Super Workspace and its roles through `/api/super-workspace`, chats through `/api/super-workspace/chats`, and agent provider metadata for role editing.
- Owns sidebar state and routes actions. Sidebar tools require the current chat's non-empty Root, preferring the active chat pane and otherwise using the backend active chat. Files cannot navigate above it.
- Chat CRUD opens chats as normal recursive layout panes through `layout.openChat(chatId)`. Role CRUD applies to the user's single Super Workspace; deleting a role also removes it from any chat membership lists. Chat list mutations emit a browser-local `super-workspace:chats-updated` event so already-open chat panes can refresh member-role context without waiting for a history poll.

`frontend/src/components/SuperWorkspaceChatPane.vue`

- Chat pane for a single `chatId`; loads flat display feed pages from `/api/super-workspace/runs?chat_id=...`, subscribes to `/api/super-workspace/events`, and incrementally refreshes changed runs. The default `detail=focus` feed contains user and final assistant messages; the optional Settings toggle requests `detail=full`, groups each Agent run into one response box, and interleaves persisted assistant text with one-line expandable reasoning/tool/command/edit rows by event time.
- Registers the resolved chat name in `stores/paneToolbar.ts` so its pane title bar follows chat creation/rename updates instead of displaying a generic `Chat` label.
- The composer creates a persisted run through `/api/super-workspace/runs`. It sends structured `role_ids` from `stores/superChatDispatch.ts` when manual target chips are selected in the Chats side panel or the composer dispatch dropdown selects one or more chat member roles; leaving the dropdown on Auto omits `role_ids` so the backend router LLM chooses among chat members. The composer dispatch button opens the dropdown only when no manual roles are selected; when highlighted with selected roles, clicking it clears back to Auto. Typed leading `@Role` mentions plus `@msg-{message_id}` citation tokens still work. The one-shot lightning action sets `parallel_dispatch`, forces a fresh Agent session, and promotes that session as the Role's default for subsequent normal queries once dispatch succeeds.
- The composer defaults to pinned/open when a chat has no local pin preference. Preferences and drafts are stored by `chatId` in non-namespaced localStorage.
- The composer registers `super-workspace:{chat_id}:composer` with `stores/inputSessions.ts` as a submit-capable input context. If the user presses Send while voice processing is still running, the input session marks a pending send, waits for voice final text, and submits the completed query to the original chat without requiring the pane to remain active.
- Role response headers are metadata rows: they show the role label, session id, context usage as both percentage and compact absolute `used / model window` tokens when available, and the cite action.
- Visible user messages and final role responses have small cite buttons that insert `@msg-{message_id}` into the leading composer prefix. Backend `super_workspace_runtime.py` parses citation tokens, writes citation edges and queued dispatch-task rows, and leaves execution to the independent Super Workspace worker process.
- The page renders flat display items directly: user query items show dispatch state and target chips, assistant `message:assistant` items with the same `driver_run_id` are grouped into one response bubble anchored at that run's first visible message even when multiple runs interleave, and reasoning/tool/thinking rows stay hidden at the display-feed query layer.
- When enabled in Super Workspace settings, the chat thread includes one viewport of virtual space after the final message. Initial loads and newly sent queries scroll to the message-end anchor rather than the absolute scroll-container end, so the latest message starts at the normal lower edge while readers can manually move it toward the middle or top of the pane.

`frontend/src/components/DirectoryPicker.vue`

- Reusable cwd autocomplete backed by `/api/tree`.
- Shows one path input plus a current-level directory dropdown. Paths are relative to the server boundary; absolute paths and `..` are rejected. Chat Root is required, while an empty Role cwd means the Chat Root itself.

`frontend/src/components/viewers/CsvViewer.vue`

- Dedicated CSV viewer for `.csv` files that otherwise arrive as text previews from the backend.
- Fetches text content with `/api/file/content`, parses RFC-style quoted fields including embedded commas/newlines and escaped quotes, and renders the first row as sticky table headers.
- Registers Table/Raw actions through `stores/paneToolbar.ts`; Raw mode displays plain CSV text without syntax highlighting.

`frontend/src/components/viewers/LargeTextViewer.vue`

- Virtualized read-only preview for oversized Markdown, CSV, and plain text files.
- Fetches bounded line windows from `/api/file/text-lines`, uses a fixed line height to map scroll position to file line numbers, and renders only the loaded window plus line-number gutter inside a full-height scroll spacer.
- Registers reload, top, end, and copy-current-window actions through `stores/paneToolbar.ts`. Search is intentionally not implemented.
- Saves and restores browser-local scroll position using the same workspace/pane/path scroll-memory keys as normal file viewers.

`frontend/src/components/Workspace.vue`

- Thin wrapper around recursive `SplitNode` for `layout.root`.

`frontend/src/components/SplitNode.vue`

- Recursive renderer for `LayoutNode`.
- Renders `ViewerPane` for pane nodes.
- Renders nested split children and a draggable resizer for split nodes.
- `startDrag(event)`: computes split ratio from pointer position and calls `layout.setRatio()`.

`frontend/src/components/ViewerPane.vue`

- Pane controller.
- Fetches `FileMeta` through `getMeta()`.
- Tracks `version` counter to force viewer reloads.
- `load(clearMeta)`: refreshes metadata for current file.
- Accepts a workspace-level loading flag so workspace switches can render every pane as a lightweight spinner before viewer components mount and start their backend fetches.
- `handleChange(event)`: reloads metadata when this pane's file changed, or when the pane file's parent directory changes (covers delete/recreate and atomic-save workflows).
- Chooses `TerminalViewer`, `SuperWorkspaceChatPane`, `DiffViewer`, `ImageViewer`, `LargeTextViewer`, `MarkdownViewer`, `HtmlViewer`, lazy-loaded `PdfViewer`, `TextViewer`, or `UnsupportedViewer`.
- Adds a transparent activation shield over inactive HTML iframe previews so iframe pointer handling cannot leave the wrong pane active before sidebar actions replace the active pane.
- Renders `PaneTitleBar.vue` above every pane body.

`frontend/src/components/PaneTitleBar.vue`

- Per-pane title bar replacing the former app-wide navbar. Reads the concrete pane's title/status/actions/controls from `stores/paneToolbar.ts`, combines them with refresh, local-history back, split, and clear/close actions, and targets the pane id directly.
- Shows the global voice/input completion status only when this pane is active, so the global state stays reachable without duplicating it across every pane.
- Uses a flat selected-color block to identify the active pane and horizontal overflow on narrow screens.

`frontend/src/components/FileSidebar.vue`

- Sidebar shell with a VS Code-style activity rail and one active tool panel at a time.
- Persists the active sidebar tool in `localStorage` under `viewer.sidebarActiveTool.v1`.
- Tools: Chats, Roles, Routes, Files, Changes, and Terminals. `RoutesPanel.vue` edits reusable routing profiles as ordered choices from the read-only Agent/provider/model catalog and displays stale/cooldown targets; it never edits provider credentials or lower-Agent configuration.
- Computes the required sidebar tool cwd from the current Chat Root and passes it to Files, Changes, and Terminals.
- The activity rail stays visible even when the tool panel is closed. Clicking a different tool changes only the active selection; clicking the already-active tool toggles the tool panel open/closed.
- Settings is a dedicated activity-rail button anchored below tools and pinned shortcuts.
- On phone-width screens, pinned and unpinned tool panels behave as an overlay beside the always-visible activity rail so the workspace is not narrowed by the saved desktop sidebar width.
- Re-emits `open-chat`, role CRUD, `open-file`, `open-diff`, and `open-terminal` events to `SuperWorkspacePage.vue`.

`frontend/src/components/sidebar/FilesPanel.vue`

- Files tool panel: pinned paths, current folder, parent button, upload button, drag-and-drop upload target, file delete confirmation/error display, and `FileTree`. When opened, it enters the current chat default cwd, falling back to root if that directory is unavailable.
- Shows the current path relative to the fixed filesystem boundary and prevents parent navigation above Chat Root.
- `openPinned(path)`: tries to enter pinned path as directory, otherwise emits `open-file`.

`frontend/src/components/sidebar/TerminalsPanel.vue`

- Terminals tool panel: new terminal button plus terminal list.
- `newTerminal()`: creates a terminal in the current Chat Root; the backend rejects empty cwd.
- `closeTerminal(id)`: deletes terminal and clears matching panes.

`frontend/src/components/sidebar/GitPanel.vue`

- Changes tool panel: lists Git changed files from `/api/git/status` scoped to the current chat default cwd, showing served-root-relative paths, status codes, and small `+/-` line counts.
- Loads Git status when the panel opens or the chat default cwd changes, and also supports manual Refresh; the sidebar does not poll Git status.
- Binary files are displayed with a `bin` chip but disabled so they cannot be opened in the diff viewer.
- Clicking a text change emits `open-diff` with the current chat default cwd so the active pane becomes a diff pane scoped to that directory.

`frontend/src/components/ConfigPanel.vue`

- Full-page configuration UI opened from the sidebar activity-rail Settings button.
- Edits `~/.view/config.json` through the existing `/api/config` endpoint.
- Uses a searchable category sidebar. Categories are Server, Appearance, Codex Models, Super Workspace, Voice, Markdown, Syntax Highlighting, and raw JSON.
- Server settings expose one confirmed graceful restart button backed by `/api/admin/restart`; the page polls `/api/health` until the backend-generation PID changes, then reloads. Stop calls `/api/admin/stop`.
- Appearance controls system/light/dark theme selection and compact/comfortable density; density maps directly to the shared control sizing rather than persisting arbitrary pixel sizes.
- Codex Models controls the default Codex model, the available model list used by Super Workspace Codex roles, and the optional Codex subprocess proxy.
- Super Workspace controls the optional chat virtual reading space, provider context recycle percentage/token defaults, chat-level Hindsight retain, optional Hindsight API URL override, chat memory bank prefix, and new-session visible chat-history bootstrap with a rough token budget.
- Voice controls voice enablement, the persisted Whisper model option list, selected model, language code, translation toggle, and target language used by `/api/voice/ws`.
- Markdown config stores an active theme plus a theme list. The editor can duplicate/reset themes and edit heading/body/paragraph/code font sizes, colors, weights, dedicated Strong/Bold color and weight, link/code/border colors, and Highlight.js token colors. Built-in Light/Default Strong text is dark blue, while Dark Strong text is pale yellow.

`frontend/src/components/FileTree.vue`

- Flat current-directory file list sorted by most recent file/directory visit time from `stores/files.ts`.
- `icon(entry)`: chooses Bootstrap icon by directory/MIME/extension.
- `select(entry)`: opens files and enters directories on single click.
- `isActive(entry)`: highlights open files.
- Pin button calls `files.togglePin(entry.path)`.
- File rows expose a delete button that emits `delete-file`; `FilesPanel` owns the confirmation and backend delete call.

`frontend/src/components/viewers/TextViewer.vue`

- Text/code preview and editor with Highlight.js.
- `extensionLanguages` / `filenameLanguages`: extension-to-highlight language maps.
- `escapeHtml(value)`: manual HTML escaping for plaintext fallback.
- `languageForPath(path)`: resolves preferred highlighter.
- `highlightText(value)`: returns highlighted HTML.
- Registers Text-specific pane-titlebar actions for manual reload, edit mode, and copy-all. `.env`, `.env.*`, and `*.env` paths use shell-style highlighting.
- Edit mode replaces the read-only highlighted preview with a split textarea and live highlighted preview plus bottom save/cancel controls. The editor and highlighted preview panes synchronize scroll position proportionally in both directions. Save writes through `/api/file/content` PUT, updates the highlighted preview, and exits edit mode; cancel restores the last loaded text without writing.
- `saveCurrentScroll()`: saves scroll position.
- `copyAll()`: clipboard write with textarea fallback.
- `load()`: fetches text, highlights, restores scroll.
- Uses syntax CSS variables from the active Markdown theme for Highlight.js token colors.

`frontend/src/components/viewers/DiffViewer.vue`

- Diff preview pane backed by `/api/git/diff`.
- Renders unified diffs with Highlight.js `diff` highlighting, word-level diff mode, and side-by-side split diff mode.
- Registers Diff-specific pane-titlebar actions through `stores/paneToolbar.ts`: view mode switches, refresh, stage file, stage all, revert file, commit, and push.
- Binary diffs render a disabled-state message instead of diff text.

`frontend/src/components/viewers/HtmlViewer.vue`

- HTML preview with rendered/raw modes.
- Rendered mode uses an iframe pointed at `/api/file/site/{path}` so normal browser loading handles local scripts, stylesheets, images, media, and in-document navigation.
- Raw mode fetches `/api/file/content` and highlights as XML/HTML with Highlight.js.
- Registers HTML-specific pane-titlebar actions for reload, rendered/raw switching, and opening the static-site URL in a new tab.
- Tracks direct local `src`, `href`, `poster`, `data`, and `srcset` dependencies from the HTML source and reloads the iframe when those files change. For `index.html`, changes under the same directory also trigger reloads so simple static folders update without precise dependency discovery.

`frontend/src/components/viewers/MarkdownViewer.vue`

- Markdown preview using `markdown-it` plugins, KaTeX, Mermaid, and Highlight.js.
- Enables raw HTML and `securityLevel: "loose"` for Mermaid, so trust boundary is local/private content.
- `escapeHtml(value)`: code-block fallback escaping.
- Custom fence renderer turns ```mermaid fences into Mermaid blocks.
- `renderMermaidIn()`: replaces Mermaid blocks with rendered SVG or marks errors.
- `load()`: fetches Markdown text, renders HTML with the current Markdown path as link context, tracks local image dependencies, renders Mermaid, restores scroll.
- Registers Markdown-specific pane-titlebar actions for manual reload, edit mode, and rendered/raw view switching.
- Edit mode replaces preview/raw display with a split textarea and live Markdown preview plus bottom save/cancel controls. The editor and preview panes synchronize scroll position proportionally in both directions. Save writes through `/api/file/content` PUT, updates rendered HTML and image dependencies, and exits edit mode; cancel restores the last loaded text without writing.
- Normal clicks on local Markdown links call `/api/file/resolve-link` and open the resolved file in the active viewer pane. Local images are rendered through `/api/file/raw` with the current Markdown path as `base` and a viewer version cache key.
- `persistCurrentScroll()`: saves scroll position.
- Uses Markdown and syntax CSS variables from the active theme for headings, paragraphs, links, code blocks, tables, and Highlight.js token colors.

`frontend/src/components/viewers/ImageViewer.vue`

- Image preview using raw file URL tagged with file `content_hash`.
- Supports transform-based pan/zoom interactions: mouse-wheel zoom, mouse/touch drag panning, one-finger pan, and two-finger pinch zoom.
- Resets zoom/pan state when image path/hash changes.

`frontend/src/components/viewers/PdfViewer.vue`

- PDF preview uses `vue-pdf-embed` / PDF.js against the raw file URL tagged with file `content_hash`, avoiding mobile Safari iframe PDF rendering issues.
- Provides a compact toolbar for zoom, rotate, reset, and opening the raw PDF in a new tab as a fallback.

`frontend/src/components/viewers/UnsupportedViewer.vue`

- Fallback for unsupported preview types.
- `formatSize(size)`: human-readable bytes/KB/MB.
- Shows MIME, size, and raw-open link.

`frontend/src/components/viewers/TerminalViewer.vue`

- xterm.js terminal pane connected to backend WebSocket.
- Registers terminal shell/status, quick-key controls, and terminate action with `stores/paneToolbar.ts` for rendering in that pane's title bar.
- The terminal text input pad uses `VoiceTextarea.vue` for microphone transcription and clear behavior, with terminal-specific Send, Send+Enter, Bracketed, and Slow paste actions injected through the reusable component action slot. Command sending stays under explicit user control.
- Maintains socket, xterm instance, fit addon, resize observer, parser disposables, output-version ordering, reconnect timer, and reset state.
- `firstParam()`: helper for xterm parser mode query parameters.
- `registerModeQueryHandlers(term)`, `modeQueryReply(sequence)`, `filterModeQueries(data, respond)`: handle terminal mode status queries.
- `ensureTerminal()`: creates xterm instance, key handlers, data forwarding, resize observer, theme.
- `disposeTerminal()`: disconnects xterm resources.
- `writeOutput(data)`: filters control queries and writes to xterm.
- `resetOutput(data, afterReset)`: clears/replays terminal buffer from snapshot safely.
- `applySnapshot(snapshot)`: applies initial backend state and replays pending output.
- `applyOutput(data, outputVersion)`: version-gated incremental output.
- `send(data)`: sends PTY input JSON over WebSocket.
- `controlSequence(key)`: maps Ctrl latch keys to control characters.
- `sendSoftInput(data)`, `sendShortcut(data)`, `toggleControlLatch()`: soft keyboard helpers.
- `resize()`: fits terminal and sends rows/cols to backend only from the active pane, so idle panes/windows do not push PTY size changes.
- `focusTerminal()`: focuses xterm.
- `connect()`: opens WebSocket, handles snapshot/output/status, reconnects after close.
- `load()`: starts connection.
- `endTerminal()`: calls terminal terminate API.

`frontend/src/components/VoiceTextarea.vue`

- Reusable textarea plus voice-input action bar. It owns only textarea binding, `VoiceInputButton.vue`, optional Clear, keyboard event forwarding, and an `actions` slot for caller-specific buttons such as Queue/Stop, Save/Delete, or Dispatch.
- The voice state still lives in `stores/voice.ts`; callers must provide a stable `contextId` so recording/transcription state stays scoped to the right prompt or role editor.

`frontend/src/stores/voice.ts`

- Browser voice job store keyed by input context ids such as `super-workspace:{chat_id}:composer` and `terminal:{terminal_id}:paste`.
- Owns `MediaRecorder`, microphone stream, voice WebSocket lifecycle, chunk sending, `ready` / `processing` / `partial` / `committed` / `final` / `error` handling, the persisted default-on `11M` language-model-refine toggle, and context text/status state.
- Each recording creates a distinct runtime job/WebSocket and a context-local ordered transcript segment. This lets a user stop one recording, immediately start another in the same input while the first is still in LLM refine, and have async partial/final results update their own segment without overwriting later recordings.
- Voice jobs survive component unmounts after recording has stopped, so users can switch sessions while final transcription is processing and return to the same pending/ready draft.
- Context statuses drive sidebar indicators: processing/recording contexts show as pending, completed but unsent voice text shows as ready.

`frontend/src/components/VoiceInputButton.vue`

- Reusable microphone transcription button backed by `/api/voice/ws`.
- Requires a stable `contextId`, binds the voice store's context text to its `v-model`, and toggles start/stop for that context. The browser sends the canonical `llm_refine` field to the standalone voice service. Consumers still decide when to send or queue the text.

## Frontend Stores And API

`frontend/src/api/client.ts`

- Shared fetch helpers for REST and raw/WS URLs.
- `request<T>()`: JSON request with error text on non-2xx.
- File APIs: `rawUrl(path, contentHash?)`, `getTree()`, `getMeta()`, `getText()`, `getConfig()`, `putConfig()`.
- Git APIs: `getGitStatus()`, `getGitDiff()`, `stageGitPath()`, `revertGitPath()`, `commitGit()`, and `pushGit()`.
- Admin APIs: `restartServer()` and `stopServer()`.
- Terminal APIs: `listTerminals()`, `createTerminal(cwd)`, `terminateTerminal()`, `deleteTerminal()`, and `terminalSocketUrl()`.
- Agent provider metadata API: `listAgentProviders()`.
- Super Workspace APIs: `getSuperWorkspace()`, chat/role CRUD helpers, `listSuperWorkspaceRuns()`, and `createSuperWorkspaceRun()` cover the user's workspace data and persisted chat/query flows. Normal role-message delivery and automatic routing are owned by the backend Super Workspace runtime, not by frontend wrappers.
- `frontend/src/api/client.ts` and `frontend/src/api/events.ts` call fixed-owner REST/WebSocket/SSE endpoints without user query parameters.
- Voice API helper: `voiceSocketUrl()` builds the browser WebSocket URL for `/api/voice/ws`, using `wss://` when the page is served over HTTPS.

`frontend/src/api/events.ts`

- `connectEvents(onChange, onState)`: creates `EventSource` for `/api/events`, reports connection state, parses `file-change` events.

`frontend/src/stores/files.ts`

- Pinia store for directory listings, current path, pins, appearance, Markdown, Codex, Super Workspace, and voice config. Browser state is non-namespaced; persisted runtime config contains no profile definitions.
- Getters: `currentEntries`, `parentPath`.
- Actions: `loadConfig()`, `saveConfig()`, `saveFullViewerConfig()`, `loadDirectory()`, `enterDirectory()`, `enterParentDirectory()`, `refreshAffected()`, `togglePin()`. `enterDirectory()` persists the last visited sidebar directory.

`frontend/src/stores/layout.ts`

- Pinia store for recursive split layout and active pane.
- Helpers: `id()`, `defaultLayout()`, `findPane()`, `mapNode()`, `firstPaneId()`, `mapAllPanes()`, `removePane()`.
- Getters: `activePane`, `openPaths`, `openTerminalIds`, `openDiffPaths`, and `openChatIds`.
- Actions: `load()`, `save()`, `snapshot()`, `restore()`, `reset()`, `setActive()`, `openFile()`, `openTerminal()`, `openDiff()`, `openChat()`, `splitPane()`, `setRatio()`, `clearPane()`, `closePane()`, `clearTerminal()`, and `goBack()`.
- Persists to `localStorage` key `viewer.layout.v1`.

`frontend/src/stores/superChatComposer.ts`

- Pinia store for per-chat composer UI preferences and drafts in non-namespaced localStorage.

`frontend/src/stores/superChatDispatch.ts`

- Pinia store for per-chat manual dispatch role selection in non-namespaced localStorage.

`frontend/src/stores/inputSessions.ts`

- Browser-local coordinator for long-running voice/input contexts. Tracks registered input contexts, optional submit targets, pending sends, and the global status rendered in the active pane title bar.
- The first submit target is Super Workspace chat: global Send stops the active recording if needed, waits for voice/LLM final text, then calls `/api/super-workspace/runs` for the context's original chat. Contexts without a submit target only finish voice processing and keep the final text in their owning input context.

`frontend/src/stores/paneToolbar.ts`

- Non-persistent per-pane toolbar registry.
- Exposes generic per-pane title/status/action/control metadata so viewers can contribute controls to their own `PaneTitleBar.vue` without coupling the app shell to viewer-specific behavior.
- Actions may hold callbacks owned by the registering viewer and are cleared when that viewer unmounts.

`frontend/src/stores/terminals.ts`

- Pinia store for terminal summaries and browser-local pinned terminal ids.
- Actions: `load()`, `create()`, `upsert()`, `terminate()`, `remove()`, and `togglePin()`.

`frontend/src/types/files.ts`

- TypeScript mirror of backend file/config/watch schemas: `EntryType`, `PreviewType`, `FileEntry`, `DirectoryListing`, `FileMeta`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `ViewerConfig`, `WatchEvent`.

`frontend/src/types/git.ts`

- TypeScript mirror of backend Git schemas: `GitDiffFile`, `GitStatus`, and `GitDiffText`.

`frontend/src/types/layout.ts`

- Recursive `LayoutNode` union and `SplitDirection`; pane nodes may hold `filePath`, `terminalId`, `diffPath`/`diffCwd`, or `chatId`.

`frontend/src/types/terminals.ts`

- TypeScript mirror of terminal schemas: `TerminalStatus`, `TerminalInfo`, `TerminalSnapshot`.

`frontend/src/types/agents.ts`

- Shared agent provider metadata types used by Super Workspace role/provider selectors.

`frontend/src/types/superWorkspace.ts`

- TypeScript mirror of Super Workspace storage, persisted run history, and dispatch response shapes: common prompt, `SuperRole`, role create/patch payloads, `AgentHistoryMessage`, `SuperHistoryRun`, `SuperHistoryTarget`, paginated run responses, selected route ids/rationale, and message metadata fields for provider session id plus context usage.

`frontend/src/utils/scrollMemory.ts`

- Browser-local scroll persistence under `viewer.scrollPositions.v1`.
- `readAll()`, `writeAll()`, `keyFor()`.
- `saveScrollPosition(path, element)`: stores `scrollTop` and `scrollLeft`.
- `restoreScrollPosition(path, element)`: retries restoration across animation frames while content lays out.

`frontend/src/composables/useScrollMemory.ts`

- Vue lifecycle wrapper around `utils/scrollMemory.ts` for viewers that reload when `path` or `version` changes.
- `useReloadingScrollMemory(path, version, element, load)`: registers `beforeunload`, saves the previous path's scroll position before reload, calls the provided loader, and returns `saveCurrentScroll()` for scroll handlers.

`frontend/src/utils/markdownRender.ts`

- Shared Markdown rendering with markdown-it plugins, KaTeX, Mermaid fences, and Highlight.js code highlighting.
- `renderMarkdown(source)`: returns rendered HTML.
- `renderMermaidIn(container, idPrefix)`: renders `.mermaid` blocks after Vue DOM updates and marks render failures.

`frontend/src/utils/paths.ts`

- Shared frontend path helpers.
- `parentPath(path)`: returns a slash-relative parent directory.
- `fileChangeAffectsPath(eventPath, filePath)`: matches exact file changes and parent-directory changes for atomic-save/delete-recreate flows.

`frontend/src/styles.css`

- Global app layout and shared classes: shell, top bar, sidebar drawer/pinned mode, resizers, workspace wrapper, icon buttons, mobile behavior, flattened sidebar panel/row/field primitives, shared Markdown rendering, and Highlight.js token colors. Feature sidebar components keep only feature-specific layout instead of duplicating these primitives. The sidebar, workspace, and pane canvas intentionally share one base background; muted/hover/selected color blocks provide hierarchy instead of container strokes, outer margins, or shadows.
- Semantic theme variables cover canvas/surface states, text hierarchy, borders, accent/status colors, focus, radius, overlay, and shared UI typography. The light palette intentionally uses low-saturation neutral blocks and softened status colors, while the dark palette remains higher contrast. Sidebar and main-panel chrome share a 12px UI size, secondary metadata uses 11px, and editable/long-form content can use the 13px content token; document/Markdown headings retain their own content hierarchy. Normal controls and panels use at most a 2px radius, popover shadows are disabled, and full circles are reserved for notification dots, spinners, and numeric badges. Borders remain primarily on actual form inputs and data structures where a boundary carries meaning.

`frontend/src/vite-env.d.ts`

- Vite TypeScript environment declarations.

## Root And Build Files

`next-go/`

- Go 1.26 implementation line for the Viewer microkernel. The assembled `cmd/viewerd` includes twelve resident plugins, including chat, voice, and the headless `viewer.agent-hermes` / `viewer.agent-codex` / `viewer.agent-opencode` services, plus the embedded frontend; its CLI separates public-capable gateway `--host` / `--port` from loopback-default kernel `--kernel-host` / `--kernel-port`, provides `--data-dir`, and supports a development `--static` override.
- `internal/protocol/protocol.go` owns the five wire-frame shapes, strong hello/envelope validation, UUIDv4 and manifest validation, dotted plugin-id-capable channel/pattern grammar, and the formal prefix/`*`/`>` matcher. Payload values remain arbitrary JSON.
- `internal/broker/broker.go` owns the serialized subscription table, publish fanout, ordered retained mailbox replacement/replay, atomic subscribe handoff, bounded drop-new outbound queues, priority protocol-error delivery, depth guard, and per-connection error-mailbox cleanup. `internal/broker/registry.go` owns the retained `plugins:_:list` snapshot and activated/deactivated lifecycle events.
- `internal/kernel/kernel.go` serves only `/ws`, establishes hello identity, stamps delivery time/origin, drains and non-fatally rejects oversized post-hello frames, runs one outbound writer plus WebSocket ping/pong heartbeat per connection, and closes every accepted socket with 4009 during shutdown. Core assembly and supervisor startup are deliberately outside the kernel.
- `internal/busclient/` is the concurrency-safe Go bus SDK shared unchanged by in-process and external Go plugins. It owns WebSocket transport isolation, UUIDv4 hello plus registry barrier, retained/live ordered handler workers, protocol-error callbacks, inbox RPC/timeout/cancel mapping, exponential reconnect, state callbacks, subscription replay, and plugin-attributed panic recovery around handlers/callbacks.
- `internal/pluginapi/` owns the in-process plugin contract, compile-time twelve-plugin registry, inspector-first ordered startup, reverse shutdown, loopback kernel-WS wiring, startup deadlines, and plugin-level panic isolation. `viewer.agent-hermes`, `viewer.agent-codex`, and `viewer.agent-opencode` start after config-store and before chat so their catalog mailboxes are ready for routing. `config.json` and `instance.json` live under the selected data directory; supervisor remains resident and manages only registry-listed external processes.
- `internal/plugins/gateway/` implements the C4 HTTP gateway: each browser `/ws` gets a dedicated kernel connection whose gateway hello reuses the browser UUID, while all later frames are relayed byte-for-byte; the same HTTP port serves an abstract `fs.FS`, with the directory adapter enforcing resolved-path and symlink containment. `web/` embeds `web/dist`, while `viewerd --static` supplies the development override.
- `cmd/viewer-gateway/main.go` exposes `--kernel-ws`, `--host`, `--port`, and `--static`; `scripts/smoke_gateway.py` checks registry identity, gateway-origin RPC inbox routing, static MIME/traversal handling, and kernel close-code propagation.
- `cmd/viewer-kernel/main.go` exposes the standalone kernel `--host` / `--port` ABI used by SDK integration tests, while `cmd/viewerd/main.go` remains the assembled single-binary entry point.
- `internal/plugins/terminal/` is the reusable Go terminal plugin: it owns PTY process sessions, incremental UTF-8 decoding, 30 ms/128 KiB output coalescing, the bounded in-memory snapshot ring, retained status replay after reconnect, 30-second post-exit history retention, resize/write/RPC handling, and whole-session process-group termination. Spawned zsh/bash processes use login-shell semantics so normal profile and interactive rc configuration is available.
- `cmd/viewer-terminal/main.go` is the external terminal plugin entrypoint. It accepts `--kernel-ws` and handles SIGINT/SIGTERM by terminating every owned PTY before disconnecting.
- `scripts/smoke_terminal.py` is the Python-SDK black-box terminal suite used unchanged against the Go kernel with the Go terminal plugin.
- `internal/plugins/supervisor/` implements the C0 process supervisor over the Go bus SDK: registry loading, managed `backend/run` process groups, append-only per-plugin logs, starting/running/crashed/broken state publication, lifecycle events, serialized manual restart RPCs, exponential backoff, the 60-second crash-loop breaker, and TERM-to-KILL tree shutdown. `cmd/viewer-supervisor/main.go` exposes `--kernel-ws`, `--registry`, and `--log-dir` for the standalone process form.
- `internal/plugins/inspector/` implements the Go bus-inspector: open `>` capture with client-side self-origin rejection, a bounded ring, compound protocol-glob/frame/origin/trace/payload filters, pause/resume/clear control, a fresh depth-zero match stream capped at 200 events per second, retained statistics, and newest-first 800KB-budgeted cursor snapshots. `cmd/viewer-inspector/main.go` exposes `--kernel-ws`, `--ring-size`, and `--echo`.
- `internal/plugins/configstore/`, `instancestore/`, and `fileservice/` implement the Go C1-C3 core services: atomic JSON plugin config, atomic JSON per-instance state with retained tombstones, and unrestricted absolute/tilde-expanded file resolve/read/hash plus unpaged directory listing. `file:_:list` filters hidden names, returns production-compatible file-entry metadata, and sorts directories first case-insensitively. `internal/plugins/pluginrpc/` supplies their inbox response helpers, while `internal/plugins/storefile/` owns sibling-temp-file JSON replacement.
- `internal/agentdriver/` defines the provider-neutral headless-agent bus types and normalized block-kind constants. Prompt requests require the chat-owned `turn_id`; start requests can carry it for implicit first turns, and every event/turn-ended frame echoes it so chat never infers ownership from a session id. `internal/acp/` implements the bounded NDJSON JSON-RPC ACP stdio client and owns ACP update-to-block parsing through `ParseBlock`; it includes initialize/new/load/prompt/cancel, response-id correlation, raw session/update dispatch, stderr diagnostics, malformed-line tolerance, and process-exit failure propagation. `internal/plugins/agenthermes/` is the single-instance `viewer.agent-hermes` service plugin: it reads the existing Hermes command/profile/YOLO environment controls, owns the session-to-ACP-subprocess pool and load fallback, immediately acknowledges prompts, publishes ordered raw-plus-parsed event frames and turn-ended events, and sets a best-effort/overridable retained catalog under `viewer.agent-hermes:_:catalog`.
- `internal/codexserver/` implements the bounded Codex App Server JSONL stdio client, thread/turn control, best-effort `model/list`, and Codex notification normalization through `ParseBlock`. `internal/plugins/agentcodex/` is the single-instance `viewer.agent-codex` service plugin: it reads the existing App Server enable/command/YOLO environment controls, owns the thread-to-session process pool and resume fallback, immediately acknowledges prompts, publishes ordered raw-plus-parsed event frames and turn-ended events, and sets a best-effort/overridable `openai-subscription` catalog under `viewer.agent-codex:_:catalog`.
- `internal/plugins/agentopencode/` is the second ACP tenant and single-instance `viewer.agent-opencode` service plugin. It launches `VIEWER_OPENCODE_COMMAND` (default `opencode`) with `VIEWER_OPENCODE_ARGS` (default `acp`), reuses `internal/acp` unchanged for initialize/new/load/prompt/cancel and `ParseBlock`, owns the session-to-subprocess pool with load fallback, immediately acknowledges prompts, and publishes ordered raw-plus-parsed events plus turn-ended. Its retained `{agent:"opencode", providers:[...]}` catalog defaults to the `default` provider and can be replaced through `plugins.viewer-agent-opencode.catalog`; unknown OpenCode ACP update kinds are retained as `other` blocks whose payload includes the original update type.
- `internal/plugins/chat/` is the provider-agnostic Super Workspace orchestrator. Plugin-level C1 configuration under `plugins.viewer-chat` retains workspace metadata, the `agents` mapping, LLM router, summaries/Hindsight, and budgets; Roles and routing policies are domain rows in GORM/modernc `chat.sqlite3`. Startup imports legacy C1 roles/routing exactly once when both DB tables are empty, synthesizes policies for legacy direct provider/model Roles, then removes the old C1 keys. `roles` stores Role prompts/cwd/session/context policy without direct provider/model columns; `routing_policies` stores ordered canonical candidate JSON plus failover settings. Catalog mailboxes determine online candidates and `chat:_:agent-catalog` aggregates online/offline entries. Every agent start/prompt/cancel and event/turn-ended exchange crosses the bus; chat has no ACP or Codex App Server import, parser, process, or in-process agent interface. Events are resolved by echoed `turn_id`, not a session-to-turn mapping. Ordered routing-policy candidates honor enabled candidates, `auto_failover`, and `max_attempts`; each `turn-completed` payload carries ordered attempt outcomes. Chats/messages/turns/turn summaries/chat×Role session triples share the plugin DB; bus event frames append raw `turn_events`, persist normalized `message_blocks`, and project only agent-text blocks into `messages`.
- `internal/plugins/voice/` is the external voice-service relay. Each `voice:_:start` reads `plugins.viewer-voice.service_ws/model/language` from config-store, opens one upstream WebSocket, forwards base64 bus chunks as binary frames and stop as JSON, normalizes service text messages into `voice:{rec}:event`, and owns concurrent session cancellation plus the ten-minute hard limit. `cmd/viewer-voice/main.go` exposes the standalone plugin form; `scripts/mock_voice_service.py` and `scripts/smoke_voice.py` provide its deterministic black-box suite.
- `cmd/viewer-configstore/`, `cmd/viewer-instancestore/`, and `cmd/viewer-fileservice/` expose the standalone migration binaries with the common `--kernel-ws` ABI and store-specific `--db` overrides. `scripts/smoke_stores.py` and `scripts/smoke_fileservice.py` use the Python SDK to black-box the RPC, persistence, retained-mailbox, tombstone, size-limit, and binary-content contracts.
- `scripts/smoke_kernel.py` injects `next/` to use the Python SDK and black-box checks hello close codes, fanout/matching, mailbox handoff, RPC inbox traffic, oversize errors, registry/lifecycle behavior, and SIGTERM close 4009 against the built Go daemon.
- `scripts/mock_acp_agent.py` is a deterministic, no-LLM Hermes ACP stdio peer for initialize/new/load/prompt/update/cancel. `scripts/mock_opencode_agent.py` is the OpenCode ACP fixture and emits an OpenCode-only update kind to verify lossless `other` normalization. `scripts/mock_codex_server.py` is the equivalent deterministic Codex App Server peer, including catalog discovery and an injected start failure, so verification never launches real Agents. `scripts/migrate_agent_history.py` performs the idempotent one-shot read-only/online-backup migration from the production history schema into `chat.sqlite3` and includes a temporary SQLite fixture self-test. `scripts/smoke_chat.py` launches isolated config-store + all three agent plugins + chat, asserts their catalog mailboxes and the aggregate catalog RPC, routes two Roles through an explicit Hermes policy, verifies Codex and OpenCode start/prompt/event persistence/turn-ended over the bus, checks ordered failover attempt details, verifies raw event/parsed block/DB behavior, and exercises asynchronous cancellation plus idempotent stop; `smoke_all.sh` includes chat as the ninth suite and voice as the tenth suite.
- `scripts/smoke_single_binary.py` starts one assembled `viewerd` on isolated ports/data, checks embedded HTTP, all twelve resident registrations through gateway `/ws`, both JSON stores, terminal IO, inspector capture, direct-kernel external-plugin compatibility, clean SIGTERM, and restart persistence. `web/build-release.sh` builds `next/frontend`, synchronizes its dist into the embed tree, and builds the release binary; `scripts/build-release.sh` remains the compatibility entry point.
- `scripts/smoke_supervisor.py` and `scripts/smoke_inspector.py` launch isolated Go-kernel/plugin stacks on ports 29371 and 29372 by default and use the Python SDK as an external black-box client. They cover supervisor registration/restart/breaker/retained state and inspector filtering/pause/pagination/stats/self-echo/downsampling respectively; their ports and binary paths are environment-overridable.
- `scripts/smoke_sdk.sh` builds `/tmp/viewerd` and runs the same Go SDK integration suite against both the Go and Python kernels, including a real kill/restart reconnect and retained replay.
- `examples/pingpong/` demonstrates two Go SDK clients making RPC calls in both directions.
- Standard checks from `next-go/`: `go build ./...`, `go vet ./...`, `go test ./... -count=1`; the smoke test runs with `next/.venv/bin/python` against a daemon on port 28766.

`next/frontend/README.md`

- Short manual loop for serving `next/frontend/dist` through `viewerd --static`, using the embedded UI, or running Vite with its `/ws` gateway proxy.

`next/frontend/src/plugins/files/`

- Singleton Files pane plugin registered through the shell's in-repo plugin loader. `FilesPane.vue` obtains the root and every expanded directory exclusively through `file:_:list`, keeps child directories lazy, distinguishes file/directory/symlink icons, and deliberately has no preview or mutation actions. Its root title is registered into the shell pane chrome rather than rendered as a second header; the shell's standard pane refresh remounts and reloads it.

`next/frontend/src/plugins/chat/`

- Per-chat frontend module registered through the same in-repo loader. It contributes only `ChatPane.vue` plus the live chat instance Dock list, filtered reactively to pinned or currently open chats. Its create action makes a default chat and immediately opens it, so the first-chat path does not depend on an existing pane. ChatPane registers its chat title and manager action into the shell pane chrome; chat-manager mutations emit the frontend-local `viewer:chats-changed` event so open titles and Dock pin/name state refresh without a new backend frame.

`next/frontend/src/plugins/voice/`

- Headless frontend plugin activated once by the shell loader. `voiceStore.ts` owns per-composer recording/processing/ready state, ordered transcript composition, one globally active MediaRecorder, 250 ms chunk encoding and bus delivery, and cancellation cleanup; a wildcard event subscription buffers an early ready event until the start RPC returns its recording id, after which the job also owns its exact recording-channel subscription. It probes `plugins:_:list` on activation and every bus reconnect, clearing backend availability on disconnect or probe failure. `VoiceInputButton.vue` binds that state to a draft through `defineModel` and renders disabled with an explanatory tooltip when the backend voice plugin is absent; ChatPane places it beside the composer textarea.

`next/frontend/src/plugins/chat-manager/`

- Pure-frontend singleton management plugin with one `ManagerPanel.vue`. Its pinned Dock entry opens a three-tab page; all three tabs share `MasterDetail.vue` (left narrow name-only list with a fixed bottom create button, right configuration panel holding all actions incl. delete/pin). Chats owns chat CRUD/pinning/activation, common prompts, roots/types, and member Roles; Roles owns Role prompt/cwd/session/context settings and routing-policy binding, with the aggregate Agent catalog as a three-level candidate helper; Routes owns ordered candidate editing via `TextSelect.vue` label-over-clickable-text pickers, drag-and-drop reordering, divider "+" insertion, failover/attempt limits, and a read-only full-policy JSON preview at the bottom. It consumes only `chat:_:*` RPC from the existing backend chat plugin.

`next/frontend/src/stores/layout.ts`, `next/frontend/src/stores/paneChrome.ts`, and the shell pane components

- `openInstance()` focuses an existing instance, then prefers the active/first empty pane, otherwise creates a vertical split to the active pane's right; occupied pane content is never silently replaced. The Dock stays a narrow icon rail (`flex: 0 0 var(--dock-width)` at all times — the workspace never reflows) and expands as an overlay (absolute-positioned `.dock-inner`, 220 px, shadow) after a configurable pointer-hover delay (default 500 ms, localStorage `viewer.dock.hoverExpandMs.v1`, edited via the gear popover in the dock footer which replaced the old bus-connection indicator), then retracts immediately on pointer leave. Singleton Dock providers are pinned by default, expose a hover pin toggle, and persist the per-provider choice in localStorage; unpinned singletons are visible only while open. `paneChrome.ts` stores instance-uid-keyed title/status/action/control contributions; `PluginCtx.setChrome()` registers them and clears them on dispose, while `PaneContainer.vue` renders them before the standard refresh/split/close controls.

`next/ts-sdk/tests/frontend-contract.test.ts`

- Go-backed Vitest contracts for every bus call used by the next/frontend terminal, Files, Bus Inspector, chat, and chat-manager panes, including the three manager tab RPC groups, real aggregated Agent catalog RPC, DB-backed Role/routing/chat CRUD, active-mailbox replacement, explicit ordered dual-Role relay sender frames, busy rejection, and router missing-config behavior.

`run.py`

- Main production/development launcher.
- This project is run through `uv`; use `uv run ...` for Python entrypoints and checks instead of calling system `python` directly.
- Constants: project paths, default root `~/Sync`, host `0.0.0.0`, port `18989`, log dir.
- `parse_args()`: CLI options for root, port, host, frontend dist, build, reload, debug, config directory, data directory, and log paths.
- `resolve_project_path(path)`: resolves relative paths against repo root.
- `build_frontend(debug)`: runs `npm run build` in `frontend/`, passing debug sourcemap env flags.
- `default_log_file(log_dir)`: timestamped log path.
- `main()`: validates served root, exports env, configures logging, optionally builds frontend, prints URLs, starts uvicorn.

Implementation checks:

- Frontend: `npm run build` from `frontend/`.
- Backend: `uv run python -m compileall backend/app` from the repo root.
- Do not start the frontend dev server for verification; the user tests the server manually.

`scripts/manage_viewer.py`

- Lightweight CLI process manager with `start`, `stop`, `restart`, and `status` commands.
- Stores `viewer.pid`, `viewer.json`, and `viewer.log` under the system temp directory at `viewer-process-manager/<project-hash>/`.
- Default standalone start command uses the current Python executable and the same serve-dir/host/port shape as the systemd backend command, without debug, reload, or frontend build flags.
- `stop` sends `SIGTERM` to the target's detached process group when possible, waits for exit, escalates to `SIGKILL` after a timeout, also handles the recorded manager parent PID when an explicit backend child PID is supplied, and clears the pid/state files.
- `restart` stops the active or explicitly supplied service process group, then starts the default or override command in a detached session.

`scripts/prepare_isolated_instance.py`

- Creates a worktree-local `.test-instance` by copying the current config and taking an online SQLite backup of the active Viewer history DB. `--replace` intentionally refreshes only that isolated target.

`scripts/migrate_codex_app_patches.py`

- Idempotent one-time backfill for Codex App Server turns whose historical patch notifications were ignored by Viewer. It reads structured `patch_apply_end` records from Codex rollout JSONL, maps provider session/turn ids to persisted driver runs, and inserts normalized patch text and file-change rows; dry-run is the default and `--apply` performs the write.

`scripts/preview_chat_memory_bootstrap.py`

- Read-only experiment for a query-aware new-session context. Given a Super Workspace query message id, it combines the canonical pre-query visible SQLite timeline with the configured chat-scoped Hindsight bank's recall and structured reflection, then writes a bounded Markdown preview without changing messages, sessions, configuration, or memories.

`scripts/run_isolated_instance.py`

- Launches the worktree with isolated config/data/provider run directories and port `19089` by default. It delegates to `run.py`, does not start a frontend development server, and leaves the normal instance and port untouched.

`pyproject.toml`

- Python project metadata and dependencies: FastAPI, uvicorn, watchfiles, pydantic-settings, loguru.

`uv.lock`

- Locked Python dependency graph for `uv sync`. Do not hand-edit.

`README.md`

- User-facing overview of the current Super Workspace, setup, runtime, persistence, configuration, and verification commands.

`.gitignore`

- Ignores Python caches, virtualenv/cache dirs, frontend dependencies/build output, local viewer state, `.codex`, logs, editor/OS files.

`.codex`

- Local ignored file currently present but empty. It is not used for project architecture instructions.

`AGENTS.md`

- Agent instruction file. Requires future agents to read `architecture.md` before code changes and keep it updated.

`architecture.md`

- This file. Current map of modules, functions, routes, data flow, and likely fault locations.

## Frontend Build And Package Files

`frontend/package.json`

- Frontend metadata, scripts, dependencies.
- Scripts: `dev`, `build` (`vue-tsc --noEmit && vite build`), `preview`.
- Main libraries: Vue, Vite, Pinia, Bootstrap, Bootstrap Icons, xterm, markdown-it plugins, KaTeX, Mermaid, Highlight.js.

`frontend/package-lock.json`

- Locked npm dependency graph. Do not hand-edit.

`frontend/tsconfig.json`

- TypeScript compiler config for Vue/Vite.

`frontend/vite.config.ts`

- Vite config with Vue plugin.
- Builds sourcemaps when `VIEWER_DEBUG=1`.
- Dev server binds `0.0.0.0` and proxies `/api` to `http://127.0.0.1:8000`.

`frontend/index.html`

- Minimal Vite HTML entry with `#app` and `/src/main.ts`.

`frontend/dist/`

- Generated frontend build output served by FastAPI. Ignored by git and should be regenerated with `npm run build` or `uv run python run.py --build-frontend`.

`frontend/node_modules/`

- Installed npm dependencies. Ignored by git.

## Data Contracts

Backend Pydantic models in `backend/app/models.py` should match frontend interfaces in `frontend/src/types/`.

- `FileEntry` <-> `FileEntry`
- `DirectoryListing` <-> `DirectoryListing`
- `FileMeta` <-> `FileMeta`
- `ConfigData` <-> `ViewerConfig`
- `AppearanceConfig` <-> `AppearanceConfig`
- `MarkdownConfig` / `MarkdownTheme` <-> `MarkdownConfig` / `MarkdownTheme`
- `WatchEvent` <-> `WatchEvent`
- `TerminalInfo` <-> `TerminalInfo` including PTY rows/cols and shared layout lock state.
- `TerminalSnapshot` <-> `TerminalSnapshot` including PTY rows/cols and shared layout lock state.

If a backend field changes, update the matching frontend type and all consumers.

## Persistence

- `VIEWER_CONFIG_DIR/config.json` (legacy default `~/.view/config.json`): appearance, voice, Markdown, routing profiles, and Super Workspace configuration, managed by `/api/config` and `/api/super-workspace/routing`. Agent credentials/models remain in Agent-owned configuration.
- `VIEWER_DATA_DIR/agent-history.sqlite3` (legacy default `~/.view/agent-history.sqlite3`): shared agent history index used first by Super Workspace and managed through SQLAlchemy ORM sessions with `NullPool` connection handling. It stores Super Workspace definitions, roles, chat route overrides, user messages/runs/targets, effective execution targets/routing attempts, and provider driver-written intermediate message rows. Runtime read/status paths do not resync or reparse provider source files as a fallback.
- `localStorage` layout/sidebar/draft/pin/scroll keys are not user-namespaced. `utils/storage.ts` migrates legacy `.dailing` keys when first read.
- `~/.view/logs/viewer-*.log`: timestamped runtime logs from `run.py`.
- `~/.view/logs/codex-app-server-sessions/*.json`: Viewer metadata for Codex App Server threads and turns.
- `~/.view/logs/terminals/*.log`: terminal replay logs.

## Common Fault Locations

- File cannot open or wrong preview type: check `backend/app/files.py` `preview_kind()`, `get_meta()`, frontend `ViewerPane.vue`, and specific viewer.
- Directory tree stale: check `backend/app/watcher.py`, `backend/app/events.py`, `frontend/src/api/events.ts`, and `files.refreshAffected()`.
- Live refresh not firing: check SSE `/api/events`, `App.vue` `connectEvents()` callback, and `ViewerPane.vue` `handleChange()`.
- Text too large: `settings.max_text_preview_bytes` and `read_text()`.
- Path/security issues: `normalize_relative()`, `resolve_path()`, symlink behavior in `files.py`.
- Terminal creation fails: `settings.terminal_shell`, `TerminalManager.create()`, shell availability, PTY permissions.
- Terminal output glitches: `TerminalViewer.vue` snapshot/output version logic and `TerminalManager._read_output()`.
- Terminal resize issues: `TerminalViewer.vue resize()` and `TerminalManager.resize()`.
- Super Workspace role dispatch fails: check `backend/app/super_workspace_runtime.py`, `backend/app/super_workspace_worker.py`, provider manager logs, `~/.view/agent-history.sqlite3`, and detached driver state under `WEAVER_RUN_DIR`.
- Codex role run fails: check `codex app-server --stdio` availability, `backend/app/codex_app_server.py`, App Server session metadata, and the persisted routing attempt error/scope.
- Frontend runtime errors: browser console. Backend and provider runtime errors are written through `backend/app/logging.py` under `~/.view/logs/`.
- Production frontend missing: build `frontend/dist` or set `VIEWER_FRONTEND_DIST`.

## Maintenance Rules

- Keep this file synchronized with code when responsibilities move or files are added/removed.
- Keep backend schemas and frontend TypeScript interfaces aligned.
- Do not hand-edit generated dependency/build artifacts (`uv.lock`, `frontend/package-lock.json`, `frontend/dist/`, `frontend/node_modules/`).
- The app is read-only for served files except terminal/Codex/loop processes, which can modify files because they run real commands in the served root. Viewer-owned config/state/log files live under `~/.view`.
