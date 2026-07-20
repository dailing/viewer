# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how frontend, backend, terminals, file watching, and preview rendering fit together.

## Purpose

Local Live File Viewer is a private-network file browser and preview app. A FastAPI backend exposes file, Git, terminal, voice, and Super Workspace APIs and serves the built Vue frontend. `VIEWER_ROOT` is the fixed server-side filesystem boundary; every chat has a required relative Chat Root that supplies the actual working context for Files, Git, terminals, Codex, and Hermes. The application has one fixed persistence owner, `dailing`, and no user-profile selection layer. The Vue app provides chats, roles, recursive split panes, file viewers, live refresh, and browser-local layout persistence.

## Runtime Flow

1. `run.py` loads project-local `.viewer.env` without overriding existing process variables, parses CLI flags, sets `VIEWER_*` environment variables including optional WhisperLiveKit voice settings, optionally builds the frontend, configures logging, and starts `uvicorn` on `app.main:app`; active requests and connections receive a configurable graceful-shutdown window (`--graceful-shutdown-timeout`, default 5 seconds) before Uvicorn cancels them.
2. `backend/app/main.py` creates the FastAPI app, installs CORS, gzip compression, and request logging middleware, starts `watch_root()` and the Super Workspace worker on startup, stops watcher and terminal sessions on shutdown, registers all REST/WebSocket/SSE routes, and mounts `frontend/dist` if it exists.
3. The frontend starts in `frontend/src/main.ts`, installs global client error logging, creates Pinia, and mounts `App.vue`.
4. `App.vue` loads config and terminal state directly, applies visual config as CSS variables, connects to `/api/events`, refreshes affected file listings, and dispatches every filesystem SSE as a `viewer:file-changed` browser event. Browser-local layout/sidebar/draft keys are no longer user-namespaced; `utils/storage.ts` migrates the former `.dailing` keys on first access. The normal page is the Super Workspace split-pane shell with no global navbar: every viewer pane owns a title bar, while Settings is launched from the sidebar activity bar.
5. `ViewerPane.vue` fetches file metadata and chooses the correct viewer component, including routing `.csv` text files to the CSV table viewer and oversized Markdown/CSV/text files to the virtualized large text viewer. Each pane renders `PaneTitleBar.vue`, which combines that pane's registered viewer controls with refresh/back/split/clear actions and dispatches `viewer:pane-refresh` for the concrete pane id; file panes clear visible content before refetching metadata and incrementing their pane `version`, diff panes clear and reload their Git diff directly, and terminal panes reconnect through their own viewer. Viewers fetch raw/text content and reload when their `version` prop changes; image and PDF panes include the pane version in raw-file URLs so manual refreshes can bypass browser cache even when the content hash is unchanged. Markdown panes render embedded images with browser lazy loading, track simple relative local image dependencies client-side, use a pane-level version cache key for raw-file URLs, reload/cache-bust embedded images when those image files change, and can switch into a split textarea/live-preview edit mode that saves through `/api/file/content` PUT. PDF panes configure the PDF.js worker explicitly, preload document metadata from `/api/file/raw` for the page count, and render individual pages lazily from the same cache-busted raw URL as page placeholders approach the scroll viewport. HTML panes load documents through an iframe-backed static-site route so relative scripts, stylesheets, images, and links resolve like a browser-served folder; inactive HTML panes render a transparent activation shield above the iframe so a first click can select the pane before iframe content consumes pointer events.
6. Terminal panes use REST for lifecycle operations and WebSocket `/api/terminals/{id}/ws` for interactive PTY input/output.
7. Codex and ACP provider sessions are internal runtime primitives for Super Workspace roles. Codex runs are launched through a detached background runner whose pid/stdout/stderr/state files live under `/tmp/viewer_run/codex` by default, so restarting the viewer service does not stop active Codex work. ACP agents use a shared protocol runtime/session manager over worker-owned stdio subprocesses; Hermes is the first thin process adapter and starts as `hermes -p default --yolo acp` by default. ACP creates and reloads sessions with the required Chat Root as their real cwd. Provider output and chat history are written only into `super_workspace_messages` in `~/.view/agent-history.sqlite3`; Viewer never reads a provider's private history database. The frontend does not expose low-level provider session panes or `/api/agents/sessions`.

## Backend Structure

`backend/app/main.py`

- FastAPI application and route table.
- `log_requests(request, call_next)`: logs failed and slow HTTP requests, while suppressing normal successful hot-path poll noise even in debug mode.
- `startup()`: ensures the fixed `VIEWER_ROOT` filesystem boundary exists, logs runtime config, and starts the filesystem watcher task.
- `shutdown()`: stops watcher task and terminates terminal sessions.
- `/api/health`: returns health and the current backend PID; the filesystem boundary is not exposed as workspace state.
- `/api/admin/restart`: launches the detached process manager to stop the current PID and start a replacement server with the manager's default command. When called with `include_worker=true`, it first stops the registered Super Workspace worker and clears `WEAVER_RUN_DIR/worker.pid/json` so backend startup launches a fresh worker process.
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
- `/api/config` GET/PUT: reads and writes appearance, Markdown, Codex, voice, and Super Workspace dispatcher configuration. User profiles and default-user fields are not part of the schema.
- `/api/events`: streams Server-Sent Events from `hub.subscribe()`.
- `/api/terminals`: lists or creates terminal sessions for the fixed owner. POST requires a non-empty cwd supplied from the active Chat Root.
- `/api/terminals/{terminal_id}` routes: snapshot, terminate, delete, and WebSocket connect.
- `/api/agents/providers`: returns registered agent providers with frontend display metadata such as name and Bootstrap icon.
- `/api/super-workspace` routes: fixed-owner (`dailing`) Super Workspace role storage, persisted message/query history, chats, and LLM dispatch. Roles have separate dispatcher-facing `description` and Agent-facing `prompt` fields. The independent worker resolves provider cwd as required `chat.root` plus optional `role.cwd`; there is no global-directory fallback. Running targets support two-click stop confirmation in the Chat UI. Dispatch uses the active OpenAI-compatible profile from `~/.view/config.json` and only receives role descriptions, while provider sessions receive role prompts.
- Super Workspace chats are persisted in `super_workspace_chats`; every chat requires `root`, membership, and a chat-level prompt. `ChatsPanel.vue` owns creation and settings, including a directory picker for the required root. Direct chats have one role; group chats may have multiple roles. The current Chat Root drives Files, Changes, Terminals, and provider execution. Browser layout is stored in non-namespaced localStorage.
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

- Central viewer-local storage paths under `VIEWER_HOME` when set, otherwise `~/.view`. This allows isolated test state such as `VIEWER_HOME=~/.viewer_test` while reusing the same code and Python environment.
- Defines `CONFIG_PATH`, `LOG_DIR`, `CODEX_LOG_DIR`, `HERMES_LOG_DIR`, `TERMINAL_LOG_DIR`, `CODEX_RUN_DIR`, `HERMES_RUN_DIR`, and `WEAVER_RUN_DIR`.
- `CODEX_RUN_DIR` defaults to `/tmp/viewer_run/codex` and can be overridden with `VIEWER_CODEX_RUN_DIR`; it stores detached Codex runner state outside the viewer service process.
- `WEAVER_RUN_DIR` defaults to `/tmp/viewer_run/weaver` and can be overridden with `VIEWER_WEAVER_RUN_DIR`; it stores lightweight PID/state registry files for the Super Workspace worker and provider driver processes. Worker files use `worker.pid/json`; Codex driver files use readable names such as `driver.{role_name}.{role_id}.{dispatch_task_id}.pid/json`. Worker startup uses graceful leadership handover: a new worker always spawns and overwrites `worker.pid`, even if the pid file points to a live old worker; the old worker's 5s leadership monitor notices the overwrite, stops claiming new dispatch tasks, lets its in-flight agent runs finish writing their final status, and only then shuts down its provider runtimes and exits (Codex driver startup is unchanged — a live driver pid still blocks duplicate startup). On its first dispatch-loop iteration a fresh worker sweeps `super_workspace_driver_runs` for orphaned `running` rows whose driver pid/state file is dead or absent (the hermes ACP provider never sets those) and marks them `interrupted`; a legitimately draining old worker finalizes its own run afterward and never clobbers `cancelled`/`interrupted` terminal states.
- `HERMES_LOG_DIR` stores viewer-local Hermes-to-ACP session metadata only; message history lives in `~/.view/agent-history.sqlite3`. `HERMES_RUN_DIR` is reserved for Hermes detached-run state if the provider implementation later needs local runner files.

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
- `read_config()` / `write_config(config)`: load and save appearance, Codex, voice, Markdown, and Super Workspace config.

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

`backend/app/codex_sessions.py`

- Structured Codex session manager using `codex exec --json` subprocesses instead of PTY/TUI rendering.
- `CodexSession`: viewer-local provider state including title, working directory, Codex thread/session id, rollout path, prompts, parsed rollout JSON events, process status, detached runner pid/Codex pid/run id, and paths for metadata/stderr/run logs.
- Codex metadata retains the fixed `dailing` owner. Super Workspace always supplies Chat Root plus optional Role cwd.
- Session metadata now stores an optional `model`; when set, runs pass `-m <model>` to `codex exec`.
- `cli_status()`: scans recent `~/.codex/sessions/**/rollout-*.jsonl` files, parses `token_count` events, and exposes a cached coarse status payload using the newest event timestamp for global 5-hour/weekly rate-limit chips. Codex `rate_limits.*.used_percent` values are treated as percentage points, and the API also exposes `*_remaining_percent` for "usage left" UI.
- `CodexSessionManager.create(prompt, cwd)`: creates a viewer session and writes metadata. If `prompt` is blank, the session stays `idle`; otherwise it starts a detached background runner for `codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -C <cwd> -`.
- `send(session_id, prompt)`: starts a detached background runner for a new `codex exec --json` run when no Codex thread id has been captured yet, otherwise resumes with `codex exec resume --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox <thread_id> -`. It rejects concurrent sends while a session has an active background run, appends the prompt to metadata, and writes that user prompt row to `~/.view/agent-history.sqlite3` for Super Workspace history association.
- Codex snapshots include the shared `pending_approvals` field, currently empty because Codex runs are still launched with approvals bypassed. The manager exposes the shared approval-resolution method so a future non-YOLO Codex runner can plug into the same frontend controls.
- Detached driver state: each run writes `state.json`, `stdout.jsonl`, `stderr.log`, and `prompt.txt` under `CODEX_RUN_DIR/{viewer_session_id}/{run_id}/`. The viewer metadata stores those paths plus the detached driver pid and Codex child pid. On startup, running sessions are reattached by reading this state and checking whether the driver pid is still alive.
- Codex subprocess proxying is controlled by `~/.view/config.json` `codex.proxy`; when non-empty it sets `https_proxy`, `HTTPS_PROXY`, `http_proxy`, and `HTTP_PROXY` for Codex runs, and when empty those variables are removed from the Codex subprocess environment. The default is no proxy.
- Rendered Codex events are read from the canonical `~/.codex/sessions/**/rollout-*.jsonl` file matched by Codex session id. Viewer-local metadata/prompts and the matched `rollout_path` are stored in `~/.view/logs/codex-sessions/{viewer_session_id}.json`; stderr goes to `{viewer_session_id}.stderr.log`.
- Provider snapshots compact raw rollout entries into a display shape. Full raw events remain in memory/on disk for status parsing; compact events carry `event_type`, rendered `text`, `file_changes`, `patch_text`, and a bounded `raw_preview`. The detached Codex driver process performs the same visible-event compaction while tailing rollout JSONL and writes provider message rows to `~/.view/agent-history.sqlite3`; this detached driver is the Codex provider-message ingestion path, not the Super Workspace runtime or backend watcher.
- Per-session Codex summaries parse `last_token_usage.total_tokens` from that session's matched rollout `token_count` events to expose `context_used_percent`, `model_context_window`, and `total_tokens`; pane titlebar Codex chips use these session fields instead of the newest global rollout.
- `_find_session_id(raw)`: extracts `session_id`, `conversation_id`, or `thread_id` from JSON events so later messages can resume the correct Codex thread. For detached runs, the manager scans the persisted runner stdout JSONL file to discover this id after service restarts.
- Active Codex runs are monitored by polling detached runner state and matched rollout JSONL files, then broadcasting newly parsed events. The service can recreate this monitor after restart because stdout, state, and canonical rollout files are persisted outside process memory.
- Codex session status is updated from rollout turn-finish events and detached runner exit state: `task_complete` / `turn.completed` mark the viewer session `exited`, `turn_aborted` / `turn.failed` mark it `failed`, and runner `state.json` records the final process exit code when available.
- Codex runtime debugging uses Loguru in `codex_sessions.py`: background runner start/detach/finish, rollout matching, and turn-finish status changes log at info level; rollout unavailability and synced event counts log at debug level.
- `terminate(session_id)`: stops the active detached runner process group for a session and broadcasts updated status.
- `codex_session_manager`: singleton used by routes.

`backend/app/codex_background_runner.py`

- Detached Codex driver process used by `codex_sessions.py` to keep Codex runs and history ingestion alive when the viewer service restarts.
- Starts the Codex child process, writes atomic `state.json` updates with driver pid, Codex child pid, per-invocation viewer run id, discovered Codex session id, matched rollout path, rollout line count, status, exit code, timestamps, command, and cwd.
- Captures Codex stdout/stderr to run-local files, discovers the Codex session id from stdout JSON, matches the canonical `~/.codex/sessions/**/rollout-*.jsonl`, tails new rollout lines, converts visible raw Codex events into the fixed AgentEvent IR, and writes provider message rows plus file changes directly to `~/.view/agent-history.sqlite3`. When resuming an existing Codex session, the detached runner receives the prior Codex session id before launching Codex and initializes `rollout_line_count` to the existing rollout file length so old provider events are not re-attributed to the new Super Workspace dispatch task; each resume still gets a fresh viewer run id and `driver_run_id`.
- When a Codex provider row is a frontend-visible assistant final message (`message:assistant`), it also calls `super_workspace_memory.retain_visible_message()` so chat-level Hindsight memory sees the same message the frontend displays.
- Backend server restart does not stop this driver. While the backend is down, the driver continues running Codex, monitoring rollout JSONL, and writing DB rows; after restart, `codex_sessions.py` reads `state.json` to recover the driver pid, Codex pid, Codex session id, and rollout path.

`backend/app/acp_runtime.py`

- Provider-neutral ACP client and stdio process runtime. `ACPProcessConfig` supplies provider id, executable, arguments, enabled state, profile, and YOLO metadata; adding another conforming local ACP agent requires a process adapter rather than another protocol implementation.
- Performs initialize/capability negotiation and implements new/load/list/fork/resume/prompt/cancel/close/model/mode. It validates ACP ContentBlocks, capability-gates images/audio/resources, drains subprocess stderr, and deliberately declines client-hosted filesystem and terminal capabilities.
- A missing `session/load` result—including the all-default response object produced when ACP SDK 0.9 deserializes a JSON-RPC null—is treated as missing and is never marked as bound.

`backend/app/acp_sessions.py`

- Provider-neutral Viewer session manager layered over `ACPRuntime`. It owns Viewer/provider session-id mapping, cwd/model validation, local metadata, prompt submission, list/fork/resume/cancel, status, usage, and structured ACP update normalization.
- ACP prompt/RPC failures are terminal provider failures: partial or error text remains visible and is finalized in history, while the Viewer session, driver target, and parent run leave `running` as `failed`. The Viewer ACP layer does not retry or replay provider prompts; retry policy remains owned by each Agent.
- ACP `session/update` notifications are the sole provider-event source. Agent/thought chunks are coalesced per message, tool call/update pairs upsert one tool event with structured diff/file-change data, and plan/mode/config/usage/session/command updates refresh session metadata.
- Every normalized update is written/upserted directly into `~/.view/agent-history.sqlite3` and announced through Super Workspace SSE. The manager never opens or synchronizes a provider-private database; historical chat reads use the Viewer history DB.

`backend/app/codex_app_server.py` and `backend/app/codex_app_server_sessions.py`

- Experimental Codex-native App Server provider over JSONL stdio (not ACP). Each subprocess connection completes `initialize` and the `initialized` acknowledgement before thread methods, then uses `thread/start` / `thread/resume`, `turn/start` / `turn/interrupt`, and waits for `turn/completed` before leaving `running`.
- Normalizes the current slash-form Codex notifications (`item/agentMessage/delta`, reasoning/command/file-change deltas, `thread/tokenUsage/updated`, and `turn/completed`) into Viewer AgentEvent IR. Deltas with the same Codex `itemId` upsert one streaming event and are finalized when the turn completes.
- Viewer does not implement Codex App Server client-side approval or interactive-input requests; unsupported server requests receive an explicit JSON-RPC method error instead of hanging. Provider/model retry remains owned by Codex.

`backend/app/hermes_acp.py` and `backend/app/hermes_sessions.py`

- Thin Hermes registration layer over the shared ACP runtime/session manager. It supplies `hermes`, `-p <profile> [--yolo] acp`, the Hermes metadata directory, and legacy Viewer-metadata key migration.
- ACP is enabled by default through `VIEWER_HERMES_ACP_ENABLED`; `VIEWER_HERMES_PROFILE` defaults to `default`, `VIEWER_HERMES_COMMAND` defaults to `hermes`, and `VIEWER_HERMES_YOLO` defaults to `true`. YOLO affects only the Viewer-owned subprocess and does not change Hermes gateway/profile configuration.
- `hermes_session_manager` remains the compatibility singleton used by Super Workspace. Hermes private `state.db` is owned solely by Hermes and is never read by Viewer.
- Hermes itself is not patched by Viewer. Because the current Hermes ACP adapter can encode terminal model failure as an `end_turn` message (or an empty `end_turn`) instead of a failed RPC/stop reason, the thin Viewer Hermes session adapter recognizes those terminal message shapes and marks the turn failed; retry remains entirely inside Hermes.
- Uses the official `agent-client-protocol` Python SDK pinned to the Hermes-compatible `0.9.x` line.

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
- `ConfigData` stores appearance, Markdown, Codex defaults, voice, and Super Workspace settings; it has no user-profile fields. `SuperWorkspaceConfig.provider_context_limits` stores provider-level context recycle percentage/token defaults, with optional Role settings taking precedence.
- `AgentEventType` is the fixed backend intermediate-representation enum shared by provider parsers. Current values are `message:assistant`, `reasoning`, `tool_call`, `tool_result`, `custom_tool_call`, `exec_command_begin`, `exec_command_end`, `function_call`, `function_call_output`, `custom_tool_call_output`, `view_image_tool_call`, `patch_apply_end`, and fallback `operation` for logged-but-unclassified visible events. Provider drivers convert native logs into these values before writing Super Workspace history.
- `CodexConfig` stores Codex model options, muted operation-message alpha, and an optional `proxy` for `~/.view/config.json`; `default_model` controls new/resumed Super Workspace Codex runs unless a role specifies a model. The built-in model options include `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna`; the built-in default model remains `gpt-5.5`, `muted_message_alpha` defaults to `0.56`, and `proxy` defaults to empty/no proxy.
- These should stay aligned with TypeScript interfaces under `frontend/src/types/`.

`backend/app/super_workspace.py`

- Per-user Super Workspace role manager backed by `super_workspaces`, `super_workspace_user_state`, and `super_workspace_roles` rows in `~/.view/agent-history.sqlite3`. The single workspace is named `Default Super Workspace` and uses user-scoped id `{user}:default`; `super_workspace_user_state` records the active chat id for each user.
- Defines local API models for `SuperWorkspaceData`, `SuperRole`, role update payloads, and dispatch responses.
- `SuperWorkspaceManager.read()` / `update()` / create/update/delete role methods persist the common prompt, dispatcher-facing role descriptions, Agent-facing role prompts, and runtime settings. Dispatcher role JSON/table rendering includes `description` but intentionally excludes `prompt`. Provider session refs are not role fields; current reusable sessions are owned by chat+role state rows in the history DB; changing a role prompt clears those mappings so the next dispatch creates a session with the new rules.
- `dispatch()` reads role descriptions, calls an OpenAI-compatible chat-completions endpoint with JSON response format, validates returned role ids against the candidate roles, and returns the selected ids/rationale. It intentionally only routes messages; the backend runtime owns creating/resuming the actual role agent sessions for normal Super Workspace dispatch.

`backend/app/super_workspace_runtime.py`

- DB-backed Super Workspace dispatcher and independent dispatch-task worker process.
- Defines `SuperWorkspaceMessageCreate` for new user/role-originated query messages. The payload carries optional structured `role_ids`, ACP-compatible `content_blocks`, plus optional `parent_message_id` and `sender_role_id` lineage fields. Structured blocks are persisted in the query message raw JSON and routed to Hermes without requiring Viewer-hosted filesystem or terminal methods.
- `SuperWorkspaceRuntime.submit()` parses leading `@Role ` query prefixes against all roles in the active Super Workspace, merges them with structured `role_ids`, persists the query message without the dispatch prefix, auto-dispatches through `SuperWorkspaceManager.dispatch()` using only the active chat's member roles when no explicit targets are supplied, marks the run queued, and writes one queued `super_workspace_driver_runs` dispatch-task row per concrete role id. Role mention keys are derived from role names using ASCII variable-name characters so frontend insertion and backend parsing agree.
- The backend runtime does not run the dispatch loop in-process. On startup it ensures a separate `super_workspace_worker.py` process is alive, with PID/state registered in `WEAVER_RUN_DIR`; startup refuses a live `worker.pid`, warning-logs and overwrites stale pid files, and direct worker invocation applies the same guard. The worker claims queued dispatch-task rows with a lease, skips concrete role ids that already have claimed/running tasks, and requeues tasks when that role id's current provider session is still running. Claimed tasks move through `claimed`, `running`, and terminal `completed`/`failed` states, and the parent query status is summarized from its target task statuses. The worker sends lightweight HTTP notifications to `/internal/super-workspace/notify` so the backend SSE stream can prompt frontend refreshes.
- `SuperAgentDriver` is the provider-driver base. It checks the current chat+role backing session from `super_workspace_chat_role_sessions`, creates a clean provider session when missing/stale/cwd-or-model-mismatched or when context usage reaches the Role override / configured provider percentage or token limit, and starts the first role turn with common prompt plus fixed role rules plus optional recent visible chat history plus the routed query. Later tasks resume the existing chat+role provider session with only the routed query. Queueing is represented exclusively by DB task rows.
- `CodexSuperDriver` and `HermesSuperDriver` adapt the base driver to the detached Codex runner and Hermes ACP session manager.
- `SuperWorkspaceEventHub` streams lightweight run-created/run-updated notifications through `/api/super-workspace/events`; the history DB remains the source for actual messages, and the frontend uses the display item `updated_at` cursor with `/api/super-workspace/runs?after=...` to fetch changed flat items instead of reloading the newest page after every event.

`backend/app/super_workspace_memory.py`

- Hindsight integration for Super Workspace chat-level memory. It reads `VIEWER_HINDSIGHT_API_URL` / `VIEWER_HINDSIGHT_API_TOKEN`, falling back to `~/.hindsight/codex.json`, and writes visible chat messages to Hindsight with short timeouts so memory failures do not block dispatch.
- Memory banks are chat-scoped only: `{prefix}::{user_id}::{workspace_id}::chat::{chat_id}`. The prefix defaults to `super-workspace` and is configurable in `~/.view/config.json` `super_workspace.hindsight_bank_prefix`.
- `retain_visible_message()` posts one visible query/final-answer message as an async Hindsight memory item with metadata and tags. It does not recall or inject Hindsight long-term memories into provider sessions.

`backend/app/agent_history.py`

- SQLite-backed agent history index stored at `~/.view/agent-history.sqlite3`. It is the history source for Super Workspace chats.
- The viewer-owned history DB is accessed through SQLAlchemy ORM mapped rows and per-operation sessions in `AgentHistoryStore`; its SQLite engine uses `NullPool` so each session closes its DB connection after use. Provider-private history stores are not Viewer data sources.
- Defines `super_workspaces`, `super_workspace_user_state`, `super_workspace_roles`, `super_workspace_chats`, `super_workspace_chat_pins`, `super_workspace_chat_role_sessions`, `super_workspace_messages`, `super_workspace_driver_runs`, `super_workspace_message_file_changes`, `super_workspace_message_citations`, and `super_workspace_driver_checkpoints`. `super_workspaces` stores the single user-scoped workspace id/name/common prompt, `super_workspace_user_state` stores each user's active chat id, `super_workspace_roles` stores separate routing descriptions and Agent prompts plus provider/runtime settings, `super_workspace_chats` stores chat metadata and membership, `super_workspace_chat_pins` stores the pinned chat shortcuts, and `super_workspace_chat_role_sessions` stores the current reusable provider session for each `(user, workspace, chat, role, provider)` plus cwd/model and latest context usage. `super_workspace_messages` stores one intermediate-representation row per indexed message/event with scalar IR fields mapped directly to columns: `workspace_id`, `event_index`, `received_at`, `event_type`, `text`, `query`, and `patch_text`, plus provider/session/source path/line metadata, source-derived `occurred_at`, role, full provider `raw_json`, query/dispatch status, and driver-run association. `super_workspace_driver_runs` is the Super Workspace dispatch-task table: each row binds one workspace query message to one target role, stores the role snapshot, provider/session refs, routed prompt, context usage snapshot, parent/sender/recipient lineage, claim lease fields, attempt metadata, task status, and timestamps. `super_workspace_message_file_changes` stores the IR `file_changes[]` array as child rows with `path`, `change_type`, and `diff` columns instead of embedding it as JSON. `super_workspace_message_citations` stores ordered message-to-message citation edges where `source_message_id` is the query message and `cited_message_id` is a referenced Super Workspace message. `raw_preview` is not stored as an IR JSON blob because it is derivable from `raw_json`.
- Super Workspace lineage is stored directly on messages: messages carry `parent_message_id`, `sender_role_id`, and `recipient_role_id`; provider output rows associated with a driver run also carry `query_message_id` and `driver_run_id`. Current UI presents non-empty `query` messages as runs, but the persisted shape can evolve into a query/message graph without a separate query table.
- `create_super_run()` records each Super Workspace user query as a `super_workspace_messages` row with explicit `workspace_id`, empty display `text`, non-empty `query`, selected role ids stored on that same row so direct and auto dispatch can return selected ids before driver runs exist, ordered citation edges written to `super_workspace_message_citations`, and a background Hindsight retain for that visible chat-level query when enabled.
- `record_super_target()` creates a queued dispatch-task row before any provider session is started. The worker later fills `session_ref`, `viewer_session_id`, `agent_prompt`, and context usage fields when it claims and starts the task; it also upserts the chat+role session state row.
- `claim_next_dispatch_task()` leases one queued task whose concrete chat+role pair has no claimed/running task, making session serialization DB-backed rather than process-memory-backed. Stale claimed leases are returned to queued, while running tasks keep that chat+role session occupied until the worker marks them completed/failed. The same role may run independently in different chats because session state is keyed by chat+role.
- `list_super_runs()` / `get_super_run()` return DB-only lazy pages of non-empty-query messages with dispatch-task targets. Provider message rows are selected by explicit `workspace_id` / `driver_run_id` lineage only. Reads do not reopen Codex rollout JSONL, Hermes state, or infer message ownership from prompt/time windows.
- `visible_chat_history_context()` walks backward through the current chat's frontend-visible messages, meaning query rows plus assistant `message:assistant` rows, and builds an oldest-to-newest prompt block capped by the rough token budget in `super_workspace.chat_history_bootstrap_tokens`.
- Provider message rows are inserted by the active provider driver process/watcher as provider output arrives. For Codex, that writer is the detached `codex_background_runner.py` driver, not the backend server. Super Workspace dispatch passes query, dispatch-task, parent, sender, and recipient ids into the Codex runner so every newly ingested Codex prompt/output row is directly linked to its dispatch task. `AgentHistoryStore` does not expose runtime-side resync helpers that reopen Codex rollout JSONL or Hermes state as a fallback; if a role response is visible in provider output but absent from this DB, the fault is in the driver ingestion path.
- ACP event persistence namespaces each provider event id with the Viewer-owned `driver_run_id` (falling back to `query_message_id`). This keeps streaming updates within one dispatch idempotent while preventing a restored provider session from overwriting an earlier dispatch when its turn/message counters restart. Session-load history replay may refresh usage/config metadata but is excluded from current-turn message events, so replayed history cannot be concatenated into the next visible reply.

`backend/app/logging.py`

- Loguru setup and standard logging interception.
- `InterceptHandler.emit(record)`: redirects Python logging records into Loguru.
- `current_log_path()`: resolves `VIEWER_LOG_FILE`.
- `configure_logging(log_file, debug)`: configures stderr and rotating file logs, intercepts uvicorn/FastAPI error logs, and disables uvicorn access logs because app middleware already records failed/slow requests.
- `ensure_logging()`: idempotent fallback used by app import.

`backend/app/restart.py`

- Thin admin bridge from FastAPI to the detached process manager.
- `_terminate_worker()`: stops the registered Super Workspace worker process and clears its process-registry files before a full restart.
- `_run_manager(command, include_worker=False)`: starts `scripts/manage_viewer.py` in a new session with the current backend PID and a short delay so the HTTP response can flush before the server is signaled; optionally stops the Super Workspace worker first.
- `request_restart(include_worker=False)`: asks the manager to stop the current backend PID and start the default managed server command; `include_worker=True` makes the replacement backend spawn a fresh worker on startup.
- `request_stop()`: asks the manager to stop the current backend PID without starting a replacement.

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

- Chat pane for a single `chatId`; loads flat display feed pages from `/api/super-workspace/runs?chat_id=...`, subscribes to `/api/super-workspace/events`, and incrementally refreshes changed runs.
- Registers the resolved chat name in `stores/paneToolbar.ts` so its pane title bar follows chat creation/rename updates instead of displaying a generic `Chat` label.
- The composer creates a persisted run through `/api/super-workspace/runs`. It sends structured `role_ids` from `stores/superChatDispatch.ts` when manual target chips are selected in the Chats side panel or the composer dispatch dropdown selects one or more chat member roles; leaving the dropdown on Auto omits `role_ids` so the backend router LLM chooses among chat members. The composer dispatch button opens the dropdown only when no manual roles are selected; when highlighted with selected roles, clicking it clears back to Auto. Typed leading `@Role` mentions plus `@msg-{message_id}` citation tokens still work.
- The composer defaults to pinned/open when a chat has no local pin preference. Preferences and drafts are stored by `chatId` in non-namespaced localStorage.
- The composer registers `super-workspace:{chat_id}:composer` with `stores/inputSessions.ts` as a submit-capable input context. If the user presses Send while voice processing is still running, the input session marks a pending send, waits for voice final text, and submits the completed query to the original chat without requiring the pane to remain active.
- Role response headers are metadata rows: they show the role label, session id, context usage as both percentage and compact absolute `used / model window` tokens when available, and the cite action.
- Visible user messages and final role responses have small cite buttons that insert `@msg-{message_id}` into the leading composer prefix. Backend `super_workspace_runtime.py` parses citation tokens, writes citation edges and queued dispatch-task rows, and leaves execution to the independent Super Workspace worker process.
- The page renders flat display items directly: user query items show dispatch state and target chips, assistant `message:assistant` items with the same `driver_run_id` are grouped into one response bubble anchored at that run's first visible message even when multiple runs interleave, and reasoning/tool/thinking rows stay hidden at the display-feed query layer.

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
- Tools: Chats, Roles, Files, Changes, and Terminals. Future side tools should be added to this shell instead of mixing unrelated lists into one panel.
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
- Server section has confirmed backend-only restart, backend+worker restart, and stop buttons. Both restart buttons call `/api/admin/restart`, with the full restart adding `include_worker=true`; the page polls `/api/health` until the PID changes, then reloads. Stop calls `/api/admin/stop` and leaves a command-line restart hint.
- Appearance controls system/light/dark theme selection and compact/comfortable density; density maps directly to the shared control sizing rather than persisting arbitrary pixel sizes.
- Codex Models controls the default Codex model, the available model list used by Super Workspace Codex roles, and the optional Codex subprocess proxy.
- Super Workspace controls provider context recycle percentage/token defaults, chat-level Hindsight retain, optional Hindsight API URL override, chat memory bank prefix, and new-session visible chat-history bootstrap with a rough token budget.
- Voice controls voice enablement, the persisted Whisper model option list, selected model, language code, translation toggle, and target language used by `/api/voice/ws`.
- Markdown config stores an active theme plus a theme list. The editor can duplicate/reset themes and edit heading/body/paragraph/code font sizes, colors, weights, link/code/border colors, and Highlight.js token colors.

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
- Admin APIs: `restartServer(includeWorker?)` and `stopServer()`.
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

`run.py`

- Main production/development launcher.
- This project is run through `uv`; use `uv run ...` for Python entrypoints and checks instead of calling system `python` directly.
- Constants: project paths, default root `~/Sync`, host `0.0.0.0`, port `18989`, log dir.
- `parse_args()`: CLI options for root, port, host, frontend dist, build, reload, debug, log paths.
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
- Default start command is `uv run run.py --build-frontend --debug -p 18989`, matching the project default server port.
- `stop` sends `SIGTERM` to the target's detached process group when possible, waits for exit, escalates to `SIGKILL` after a timeout, also handles the recorded manager parent PID when an explicit backend child PID is supplied, and clears the pid/state files.
- `restart` stops the active or explicitly supplied service process group, then starts the default or override command in a detached session.

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

- `~/.view/config.json`: appearance, Codex, voice, Markdown, and Super Workspace configuration, managed by `/api/config`.
- `~/.view/agent-history.sqlite3`: shared agent history index used first by Super Workspace and managed through SQLAlchemy ORM sessions with `NullPool` connection handling. It stores Super Workspace definitions, roles, user messages/runs/targets plus provider driver-written intermediate message rows with full raw JSON, structured IR columns (`workspace_id`, `event_index`, `received_at`, `event_type`, `text`, `patch_text`, and `file_changes` child rows), provider/session/source path/line metadata, and source-derived timestamps. Runtime read/status paths do not resync or reparse provider source files as a fallback.
- `localStorage` layout/sidebar/draft/pin/scroll keys are not user-namespaced. `utils/storage.ts` migrates legacy `.dailing` keys when first read.
- `~/.view/logs/viewer-*.log`: timestamped runtime logs from `run.py`.
- `~/.view/logs/codex-sessions/*.stderr.log`: stderr from Codex subprocesses.
- `~/.view/logs/codex-sessions/*.json`: viewer-local Codex session metadata including prompts, discovered Codex thread id, selected model, status, and matched rollout path.
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
- Codex role run fails: check `codex` availability on PATH, `backend/app/codex_sessions.py` command construction, `~/.view/logs/codex-sessions/*.stderr.log`, and whether a `thread_id` was captured from raw JSON.
- Frontend runtime errors: browser console. Backend and provider runtime errors are written through `backend/app/logging.py` under `~/.view/logs/`.
- Production frontend missing: build `frontend/dist` or set `VIEWER_FRONTEND_DIST`.

## Maintenance Rules

- Keep this file synchronized with code when responsibilities move or files are added/removed.
- Keep backend schemas and frontend TypeScript interfaces aligned.
- Do not hand-edit generated dependency/build artifacts (`uv.lock`, `frontend/package-lock.json`, `frontend/dist/`, `frontend/node_modules/`).
- The app is read-only for served files except terminal/Codex/loop processes, which can modify files because they run real commands in the served root. Viewer-owned config/state/log files live under `~/.view`.
