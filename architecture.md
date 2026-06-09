# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how frontend, backend, terminals, file watching, and preview rendering fit together.

## Purpose

Local Live File Viewer is a private-network file browser and preview app. A FastAPI backend serves files from the active user's configured profile home, watches configured profile homes, exposes file, Git, terminal, and Codex APIs, and serves the built Vue frontend. The app has soft user profiles: each browser chooses a profile, API calls carry `user`, and workspace/sidebar/session lists are scoped to that profile while all processes still run as the same trusted OS user with access to the served root. The Vue app provides a sidebar file browser, Git diff panel, recursive split panes, file viewers, live refresh on filesystem changes, and browser-local layout persistence.

## Runtime Flow

1. `run.py` loads project-local `.viewer.env` without overriding existing process variables, parses CLI flags, sets `VIEWER_*` environment variables including optional WhisperLiveKit voice settings, optionally builds the frontend, configures logging, and starts `uvicorn` on `app.main:app`.
2. `backend/app/main.py` creates the FastAPI app, installs CORS, gzip compression, and request logging middleware, starts `watch_root()`, the agent-loop scheduler, and the Agent Task DAG monitor on startup, stops watcher, task monitor, loops, terminals, and agent sessions on shutdown, registers all REST/WebSocket/SSE routes, and mounts `frontend/dist` if it exists.
3. The frontend starts in `frontend/src/main.ts`, installs global client error logging, creates Pinia, and mounts `App.vue`.
4. `App.vue` loads available user profiles from `/api/users`. If browser storage has no active profile, it shows a profile picker before loading app state. Once selected, the active user id is stored in `localStorage`, API/SSE/WebSocket helpers append it as `user`, and browser-local layout/sidebar/draft keys are namespaced by user. The app then loads file tree/config/terminal/agent state, applies visual config as CSS variables, connects to `/api/events`, refreshes affected file listings, and dispatches every filesystem SSE as a `viewer:file-changed` browser event so panes can decide whether indirect dependencies matter. The normal page is the Super Workspace split-pane shell; the top bar has no Super Workspace switch button and only opens utility pages such as settings, loop tasks, and the Agent Task DAG board. The outer app shell and document root suppress top-level overscroll/scroll chaining, while pane-level scroll containers keep their own contained momentum scrolling.
5. `ViewerPane.vue` fetches file metadata and chooses the correct viewer component, including routing `.csv` text files to the CSV table viewer and oversized Markdown/CSV/text files to the virtualized large text viewer. The app top bar exposes a global active-pane refresh action that dispatches `viewer:pane-refresh`; file panes clear visible content before refetching metadata and incrementing their pane `version`, diff panes clear and reload their Git diff directly, and terminal/agent panes clear stale content before reconnecting to fetch a fresh snapshot. Viewers fetch raw/text content and reload when their `version` prop changes; image and PDF panes include the pane version in raw-file URLs so manual refreshes can bypass browser cache even when the content hash is unchanged. Markdown panes render embedded images with browser lazy loading, track simple relative local image dependencies client-side, use a pane-level version cache key for raw-file URLs, reload/cache-bust embedded images when those image files change, and can switch into a split textarea/live-preview edit mode that saves through `/api/file/content` PUT. PDF panes configure the PDF.js worker explicitly, preload document metadata from `/api/file/raw` for the page count, and render individual pages lazily from the same cache-busted raw URL as page placeholders approach the scroll viewport. HTML panes load documents through an iframe-backed static-site route so relative scripts, stylesheets, images, and links resolve like a browser-served folder; inactive HTML panes render a transparent activation shield above the iframe so a first click can select the pane before iframe content consumes pointer events.
6. Terminal panes use REST for lifecycle operations and WebSocket `/api/terminals/{id}/ws` for interactive PTY input/output.
7. Codex panes use shared agent REST routes and WebSocket `/api/agents/sessions/{id}/ws?provider=codex` for structured JSONL event updates rendered by the frontend rather than through terminal emulation. New Codex sessions may be created idle with no prompt; the first pane message is represented as a conventional-workspace query in `~/.view/agent-history.sqlite3` and dispatched through the same `super_workspace_driver_runs` worker/driver path used by Super Workspace roles. Codex runs are launched through a detached background runner whose pid/stdout/stderr/state files live under `/tmp/viewer_run/codex` by default, so restarting the viewer service does not stop active Codex work. Raw rollout events stay server-side; REST/WebSocket snapshots send compact display events, and the shared agent API defaults to `detail=focus` so focus-mode panes receive assistant messages without tool output/file-change payloads until the frontend requests `detail=full`.
8. Hermes panes mirror the Codex pane lifecycle shape through shared agent REST routes and WebSocket `/api/agents/sessions/{id}/ws?provider=hermes`, but the provider backend talks to the local Hermes API server at `VIEWER_HERMES_BASE_URL` / `http://127.0.0.1:8642` and reads canonical session history directly from `VIEWER_HERMES_STATE_DB` / `~/.hermes/state.db`. Viewer-local Hermes metadata lives under `~/.view/logs/hermes-sessions/`; the SQLite `sessions` and `messages` rows are compacted into the same frontend event shape used by Codex panes. When Hermes is driven through Super Workspace or conventional workspace dispatch, the manager stores the run lineage in metadata and writes newly synced Hermes events into `super_workspace_messages`.
9. Loop tasks are Markdown files in `~/.view/loops/*.md` with generated YAML frontmatter plus a prompt body. `backend/app/agent_loops.py` runs an asyncio scheduler that creates/resumes Codex sessions through `codex_session_manager`, writes state to `~/.view/agent-loops.json`, and writes run logs under `~/.view/logs/agent-loops/`.
10. Agent Task DAG tasks live in `~/.view/agent-tasks.sqlite3` and are handled by `backend/app/agent_tasks.py`. The task monitor promotes dependency-satisfied tasks to `ready`, dispatches ready tasks when a group is in auto mode, tracks `waiting_process` task PIDs, and resumes the attached agent session when a recorded process exits. Logs and artifacts should be written under `~/.view/logs/agent-tasks/{task_id}/`.

## Backend Structure

`backend/app/main.py`

- FastAPI application and route table.
- `log_requests(request, call_next)`: logs failed and slow HTTP requests, while suppressing normal successful hot-path poll noise even in debug mode.
- `startup()`: ensures the global root and each configured profile home exist, logs runtime config, starts filesystem watcher task.
- `shutdown()`: stops watcher task and terminates terminal sessions.
- `/api/health`: returns health, active root, and current backend PID.
- `/profile`, `/api/profile`, and `/api/profile/reset`: in-process HTTP route profiler. Middleware records per-route request counts, status distribution, total/avg/min/max/p50/p95/p99 latency, and recent requests; the report also includes current process fd counts plus open SQLite-like fd paths. `/profile` serves an HTML report and `/api/profile` returns the same data as JSON.
- `/api/debug/info`: returns debug/root/frontend/log file details.
- `/api/debug/log`: returns current log file content.
- `/api/admin/restart`: launches the detached process manager to stop the current PID and start a replacement server with the manager's default command.
- `/api/admin/stop`: launches the detached process manager to stop the current backend PID.
- `/api/debug/client-log`: receives frontend errors and writes them through Loguru.
- `/api/tree`: calls `list_directory()` under the active user's profile home.
- `/api/file/upload`: streams one request body into a file under the requested active-user-root-relative directory. The directory must exist, filenames cannot contain path separators, and existing files are overwritten while directories are protected.
- `/api/file` DELETE: deletes an active-user-root-relative file only; directory deletion is intentionally rejected.
- `/api/file/meta`: calls `get_meta()` under the active user's profile home.
- `/api/file/content`: calls `read_text()` under the active user's profile home.
- `/api/file/text-lines`: calls `read_text_lines()` under the active user's profile home for virtualized large text previews.
- `/api/file/content` PUT: saves UTF-8 text to an existing file under the active user's profile home and returns updated metadata.
- `/api/file/raw`: streams a file via inline `FileResponse` and emits `ETag` plus strong immutable browser cache headers. When called with `base`, resolves Markdown-local relative/absolute file links before serving. The file APIs also accept the internal `__agent_task_files__/{task_id}/workspace/...` virtual path prefix so Task DAG workspace outputs remain previewable even when `~/.view/logs/agent-tasks/...` is outside the active user's served root.
- `/api/file/site/{path:path}` and `/api/file/site?path=...`: serve files as a static-site namespace for HTML preview iframes. The query form preserves absolute filesystem paths for outside-root files and symlink targets. HTML responses inject a `<base>` tag for relative assets and rewrite root-relative HTML/CSS asset URLs to the same `/api/file/site/` prefix; CSS responses rewrite root-relative `url(...)` and `@import` references; other files are returned through inline `FileResponse` so SVG/PDF/image assets render in-browser instead of downloading. Missing `generated/assets/...` requests fall back by searching upward for the nearest existing generated asset directory, which keeps static docs with page-relative generated-asset links working in the iframe preview. Cache headers are `no-cache` so local edits show after pane refreshes.
- `/api/file/resolve-link`: resolves a Markdown link target against a Markdown file path and returns a served-root-relative file path for viewer navigation, plus a stat-based `content_hash`/version when the target exists as a file. Markdown image rendering no longer calls this for every image during initial render; the frontend resolves simple relative image dependencies locally and lets `/api/file/raw?base=...` resolve actual image requests lazily.
- `/api/file/resolve-directory-link`: resolves a local link target against a served-root-relative directory, used by Codex session transcript links whose paths are relative to the session cwd.
- `/api/git/status`, `/api/git/diff`, `/api/git/stage`, `/api/git/revert`, `/api/git/commit`, and `/api/git/push`: expose Git working-tree status, per-file text diffs, and common Git actions rooted at the active user's profile home.
- `/api/users`: returns configured soft user profiles from `~/.view/config.json`; the bootstrapped profiles are `dailing` and `maomao`. Profile `home` values may be `~`-expanded, absolute, or relative to the OS home directory and define that user's file viewer root, not just a default directory.
- `/api/config` GET/PUT: reads and writes nav appearance, Codex model options, voice model/language/translation options, Task DAG API base URL, Markdown theme config, user profiles, and default user in `~/.view/config.json`. Normal settings saves preserve existing user profiles unless the JSON payload explicitly changes them. Super Workspace layout/sidebar/draft state is browser-local and user-namespaced.
- The old `/api/workspaces` pane-workspace route group is not mounted in this branch. The Super Workspace split-pane shell is the only normal workspace surface; files, diffs, terminals, and chat panes live inside that shell.
- `/api/agent-loops` routes: list/create/update/delete loop task Markdown definitions, reload files, pause/resume, run now, reset the retained Codex session, and read run history/details.
- `/api/agent-tasks` routes: persistent Agent Task DAG CRUD and scheduling APIs backed by `~/.view/agent-tasks.sqlite3`. Routes list tasks by `group_id`/status, list group summaries, create/delete tasks, clear or retry a task plus all downstream dependents, read task context, list task artifact/workspace files with served-root preview paths, patch mutable not-yet-running tasks, patch dependencies with cycle checks, record long-running process PIDs, complete tasks with artifacts/results, manually dispatch a task to Codex/Hermes, dispatch ready tasks, and read/update per-group manual/auto scheduler settings.
- `/api/events`: streams Server-Sent Events from `hub.subscribe()`.
- `/api/terminals`: lists or creates terminal sessions for the `user` query profile; POST accepts an optional relative `cwd`. When `cwd` is omitted, the terminal starts in the profile home directory.
- `/api/terminals/{terminal_id}` routes: snapshot, terminate, delete, and WebSocket connect.
- `/api/agents/providers`: returns registered agent providers with frontend display metadata such as name and Bootstrap icon.
- `/api/super-workspace` routes: per-user Super Workspace common prompt, workspace list/activation, role storage, persisted message/query history, role status summaries, and LLM dispatch. Super Workspaces and roles live in `~/.view/agent-history.sqlite3`: workspaces are rows in `super_workspaces`, each user's active Super Workspace is stored in `super_workspace_user_state.active_workspace_id`, the first/default workspace is named `Default Super Workspace` with user-scoped id `{user}:default`, and roles are rows in `super_workspace_roles` with stable role id, non-unique display name, fixed rules/description, provider, cwd, model, and current backing agent session ref. GET `/api/super-workspace/workspaces` returns that user's workspace list plus active workspace id; POST `/api/super-workspace/active-workspace/{workspace_id}` switches the active Super Workspace; GET `/api/super-workspace/role-statuses/{workspace_id}` returns one simplified role status per role in that workspace (`idle`, `busy`, or `failed`) by summarizing `super_workspace_driver_runs`: any `queued`/`claimed`/`running` target makes the role `busy`, otherwise the latest target is checked and only latest `failed` maps to `failed`. The agent-history SQLite engine uses `NullPool`, WAL mode, `synchronous=NORMAL`, and a 30-second busy timeout so independent backend, worker, and provider-driver processes wait briefly for the single SQLite writer instead of immediately failing on transient write locks. GET `/api/super-workspace/runs` is a display-oriented flat feed backed by the same DB for the active Super Workspace; it returns `items[]` ordered by message time where each item is either a user query row or one assistant `message:assistant` provider row, with explicit `workspace_id` plus query/target/role metadata attached for chips, citations, and future filters. The list route is a DB read and does not touch provider rollout/state files. It accepts `before=<created_at>` for older flat items and `after=<updated_at>` for SSE-driven incremental reads, returning changed/new display items plus `next_after` for the next request. POST `/api/super-workspace/runs` and `/api/super-workspace/messages` create a query message in the active Super Workspace, accept optional structured `role_ids` for manual dispatch, parse leading `@Role`/`@msg-...` prefix tokens, decide the target role ids when no explicit targets are supplied, and write one queued DB dispatch task per role id; the HTTP request does not start Codex/Hermes directly. A message can have both display `text` and an outbound `query`; a user query is represented as `role_id=user`, empty `text`, and non-empty `query`. Query text may start with one or more contiguous `@...` prefix tokens. Tokens matching role mention keys, such as `@Writer`, select explicit dispatch targets; tokens matching `@msg-{message_id}` or `@message-{message_id}` create message citation edges in `super_workspace_message_citations`; any later `@` in the query body is ordinary text. When a cited query is dispatched, `role_message_prompt()` prepends focus-mode cited message content before the current user query. On backend startup, the Super Workspace runtime ensures an independent `python -m backend.app.super_workspace_worker` process is running and registered under `WEAVER_RUN_DIR/worker.pid`. The worker process claims queued dispatch tasks only when the concrete target role id is idle within the same workspace, creates/resumes that role id's provider session, starts the detached provider driver, and updates task/query status from queued to running to completed/failed. Claiming is split into short DB operations: expired leases are reset quickly, candidates are read outside the write transaction, and each candidate is claimed with a conditional update; if a write still hits `database is locked`, the worker waits and retries instead of exiting or marking the target failed. Multiple roles with the same display name can run concurrently because serialization is per workspace role id, not per name. Provider output rows are written only by the provider driver process/watcher with `workspace_id` plus query/dispatch/parent lineage fields; the Codex driver compacts rollout events outside the DB transaction and writes each polling batch through one SQLite connection/transaction instead of opening one write transaction per event. The runtime and history read APIs do not perform fallback resyncs from Codex rollout JSONL or Hermes state, so missing DB messages remain visible as driver ingestion faults. `/api/super-workspace/events` streams lightweight Super Workspace SSE notifications for changed query messages; independent workers notify the backend through `/internal/super-workspace/notify`, and `SuperWorkspacePage.vue` calls `/api/super-workspace/runs?after=...` plus `/api/super-workspace/role-statuses/{workspace_id}` on events with a 30-second incremental fallback fetch. The Super Workspace page scrolls to the newest message on initial history load, preserves bottom stickiness for live updates, auto-loads older messages when its message scroller reaches the top and shows a "No more messages" divider when history is exhausted, merges consecutive assistant display items from the same role in the frontend display layer while preserving atomic DB rows and citation ids, and resolves internal Markdown links in role replies relative to that role session's `cwd_relative` through `/api/file/resolve-directory-link`. `/api/super-workspace/dispatch` remains as the raw role-router endpoint and sends the user message plus candidate role descriptions to an OpenAI-compatible chat-completions endpoint expecting JSON role ids. Dispatch defaults to DeepSeek direct API model `deepseek-v4-flash`, reads `VIEWER_SUPER_DISPATCH_API_KEY` / `DEEPSEEK_API_KEY` from the process environment or the project-local ignored `.viewer.env`, and can be overridden with `VIEWER_SUPER_DISPATCH_MODEL` or `VIEWER_SUPER_DISPATCH_URL`.
- Super Workspace chats add a scoped conversation layer inside each Super Workspace. Chats are persisted in `super_workspace_chats`, the active chat id is stored on `super_workspace_user_state`, and message rows use the existing `super_workspace_messages.conversation_id` as `chat_id`. Chats are user-created only: no default chat is seeded, an empty active chat id is valid, and dispatch requires an explicit existing chat. Each chat stores `member_role_ids_json` plus a chat-level `common_prompt`; auto-routing inside a chat only considers assigned member roles, so an empty member list cannot auto-route, while structured `role_ids` and explicit leading `@Role` mentions may target roles directly. Direct chats store exactly one member role and their display name follows that role name. `/api/super-workspace/chats` lists/creates/updates/deletes group or direct chats, `/api/super-workspace/active-chat/{chat_id}` switches the active chat, and `/api/super-workspace/runs` accepts `chat_id` for pane-local feed loading; when no active/explicit chat exists, the runs API returns an empty page instead of creating or selecting a chat. Provider lineage carries `chat_id` through Codex detached drivers and Hermes session metadata so role output rows stay in the originating chat. Chat `common_prompt` is prepended to role prompts as chat-level scoped instructions. The Super Workspace frontend is the only normal workspace shell: `FileSidebar.vue` has a Super Workspace mode with Chats, Roles, Files, Changes, and Terminals tools, and the main area is the existing recursive split-pane workspace. `ChatsPanel.vue` owns chat CRUD plus per-chat settings for name, type, prompt, and role membership; direct chat role membership is a single-select control and group membership is multi-select. When a chat pane is the active focused pane and the Chats side panel is open, `ChatsPanel.vue` expands that chat's member roles as send-icon manual dispatch chips backed by `stores/superChatDispatch.ts`; closing or unfocusing the panel collapses the chip list. `RolesPanel.vue` owns global role CRUD/editing. `LayoutNode` panes can contain `chatId`, `ViewerPane.vue` renders `SuperWorkspaceChatPane.vue` for chat panes, and the layout store saves this Super Workspace layout in user-namespaced localStorage.
- `/api/agents/sessions`: shared agent session API for direct provider panes. It is still backed by `backend/app/conventional_workspace.py` internally, but the old numbered workspace UI and `/api/workspaces` route group have been removed from the app surface. When `cwd` is omitted, Codex/Hermes start in the profile home directory.
- `/api/agents/sessions/{session_id}` routes: shared snapshot, send, DB-backed dispatch append, terminate, and WebSocket connect. Non-WebSocket mutating requests carry `provider` in the JSON body; GET/WebSocket use a `provider` query parameter because they have no request JSON body. Snapshot and WebSocket routes accept `detail=focus|full`; focus is the default and filters event payloads down to assistant messages, while full includes tool calls/results, file changes, patch text, and raw previews.
- `/api/agents/sessions/{session_id}/approvals/{approval_id}` resolves a provider-normalized pending approval with choices such as `once`, `session`, `always`, or `deny`. Agent snapshots expose `pending_approvals` so the shared frontend transcript can render approve/deny controls without provider-specific UI.
- `/api/codex/status`: returns the latest global Codex CLI rate-limit status parsed from recent `~/.codex/sessions/**/rollout-*.jsonl` `token_count` events by timestamp; pane-level context usage comes from each session's matched rollout file.
- `/api/codex/models`: returns selected and available models for Codex session creation from `~/.view/config.json` codex defaults only.
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
- Defines `USERS_DIR` for per-profile state under `~/.view/users/{user}/`.
- Defines `CONFIG_PATH`, legacy `WORKSPACES_PATH`, `LOOPS_DIR`, `LOOP_STATE_PATH`, `LOG_DIR`, `CODEX_LOG_DIR`, `HERMES_LOG_DIR`, `TERMINAL_LOG_DIR`, `AGENT_LOOP_LOG_DIR`, `CODEX_RUN_DIR`, `HERMES_RUN_DIR`, and `WEAVER_RUN_DIR`.
- `CODEX_RUN_DIR` defaults to `/tmp/viewer_run/codex` and can be overridden with `VIEWER_CODEX_RUN_DIR`; it stores detached Codex runner state outside the viewer service process.
- `WEAVER_RUN_DIR` defaults to `/tmp/viewer_run/weaver` and can be overridden with `VIEWER_WEAVER_RUN_DIR`; it stores lightweight PID/state registry files for the Super Workspace worker and provider driver processes. Worker files use `worker.pid/json`; Codex driver files use readable names such as `driver.{role_name}.{role_id}.{dispatch_task_id}.pid/json`. Worker and driver startup paths check the matching pid file before spawning: a live pid blocks duplicate startup with an error log, a stale/dead pid is warning-logged and overwritten, and a missing pid file is created normally.
- `HERMES_LOG_DIR` stores viewer-local Hermes metadata while canonical messages are read from `~/.hermes/state.db`; `HERMES_RUN_DIR` is reserved for Hermes detached-run state if the provider implementation later needs local runner files.
- `migrate_legacy_state()`: copies served-root `.viewer.config.json` into `~/.view/config.json` on first use when the new config does not exist; it does not create or copy the legacy global `~/.view/workspaces.json`.

`backend/app/users.py`

- Soft user profile helpers. Loads `users` and `default_user` from `~/.view/config.json`, validates user ids, normalizes profile homes, supports absolute homes, `~`-expanded homes, and paths relative to the OS home directory, bootstraps `dailing` and `maomao` when no profiles are configured, and derives per-user workspace state paths. The profile home is the user's file viewer root.
- `user_workspaces_path(user_id)`: returns `~/.view/users/{user}/workspaces.json`; legacy workspace files are not copied implicitly.

`backend/app/files.py`

- File tree, path normalization, metadata, upload/delete helpers, content reading, and `~/.view/config.json` persistence.
- `normalize_relative(path)`: converts slashes, strips leading/trailing slashes, rejects `..` path segments.
- `served_root(user_id)`: returns the active user's profile home when a `user` is supplied, otherwise the global `VIEWER_ROOT` fallback.
- `resolve_path(path, user_id)`: joins normalized relative paths to the active user's profile home and preserves explicit absolute filesystem paths. Symlinks are allowed by current implementation, including symlinks whose targets live outside the profile home.
- `resolve_markdown_link(base_path, target, user_id)`: resolves local Markdown image/link targets relative to the Markdown file, supports absolute/file URLs, strips common editor `:line[:column]` suffixes, and returns absolute paths when targets live outside the active user's root.
- `resolve_directory_link(base_dir, target, user_id)`: resolves local file links relative to a directory, supports absolute/file URLs, strips common editor `:line[:column]` suffixes, and returns absolute paths when targets live outside the active user's root.
- `resolve_served_directory(path, label, user_id)`: resolves a working directory for terminal/Codex launches. Explicit absolute paths are allowed; relative paths resolve inside the active user's profile home. Missing directories fall back to the active user's root.
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
- `config_path()`: returns `~/.view/config.json` and triggers one-way legacy copy from root-local `.viewer.config.json` when needed.
- `read_config()` / `write_config(config)`: load and save nav appearance, Codex model options, Task DAG API base URL, Markdown theme config, user profiles, and default user. Old workspace-state keys in config are ignored rather than migrated.
- Legacy workspace helper functions remain in `files.py` only for internal provider-pane compatibility; they are no longer exposed through `/api/workspaces` or used by the normal frontend shell.

`backend/app/conventional_workspace.py`

- Thin adapter from traditional pane workspaces to the Super Workspace driver queue. It owns provider/session ref validation, conventional role attach/remove/list/status helpers, active traditional workspace lookup, idle provider session creation, and pane-message dispatch.
- Traditional pane messages are represented as conventional role runs in `super_workspace_driver_runs` with `raw.type=traditional_workspace_query_message`; provider execution still flows through `SuperWorkspaceRuntime` and the shared Codex/Hermes managers. The runtime prompt uses traditional-workspace wording for `{user}:workspace:{slot}` workspace ids while preserving the same routing metadata and lineage fields used by Super Workspace.

`backend/app/agent_loops.py`

- Markdown-backed Codex loop task scheduler.
- Parses generated YAML frontmatter from `~/.view/loops/*.md`; the Markdown body is the Codex prompt.
- Supports schedule types `manual`, `once`, `interval`, `daily`, and `multi_daily`; local task times use `Asia/Shanghai` / UTC+8 by default.
- Supports Codex session policies `new_each_run`, `reuse`, `reuse_until_context`, and `reuse_with_limits`.
- Persists runtime state in `~/.view/agent-loops.json`, including current session id, current run id, run counts, consecutive failures, next run time, stopped/paused state, and last error.
- Writes per-task run history and detailed run snapshots under `~/.view/logs/agent-loops/{task_id}/`.
- Reuses `codex_session_manager` for actual Codex creation/resume and uses compact Codex snapshots for log display.

`backend/app/agent_tasks.py`

- SQLite-backed Agent Task DAG runtime stored at `~/.view/agent-tasks.sqlite3`, accessed through SQLAlchemy ORM mapped rows and per-operation sessions in `AgentTaskManager`. The SQLite engine uses `NullPool` so request-scoped sessions close their DB connection instead of keeping idle pooled file descriptors.
- Defines task API models locally so the feature stays isolated from the legacy loop scheduler.
- Supports statuses `draft`, `backlog`, `ready`, `claimed`, `running`, `waiting_process`, `review`, `done`, `failed`, `blocked`, and `cancelled`.
- Uses a two-role operating model. A Codex manager/planner owns each group-level goal, plan, context, constraints, DAG mutations, rescheduling, retries, and shared/common code changes. Executor agents run one task at a time, can create/modify scripts/files/outputs inside their task-local workspace, should not edit shared project code or mutate the DAG unless explicitly instructed, and request manager review when the plan, dependencies, or common code need to change.
- Keeps `parent_id`/`root_id` for task-tree display separate from `depends_on`, which is the scheduler DAG dependency list.
- Allows plan patches only for `draft`, `backlog`, `ready`, and `blocked` tasks. Running/waiting/completed tasks are execution history and should be replaced or followed up instead of rewritten.
- Validates dependency existence, group isolation, and cycles before writing dependency changes.
- `recompute_ready()` promotes dependency-satisfied backlog/blocked tasks to `ready` and blocks tasks whose dependencies are unfinished or failed.
- Per-group settings store manual/auto dispatch settings plus `goal`, `plan`, `context`, `constraints_json`, `project_root`, and the reusable `manager_session_id`. `project_root` is the group-level Codex/Hermes starting directory used when a task has no explicit workspace override.
- `/api/agent-tasks/plan` reads/writes group-level plan context and `/api/agent-tasks/manager` creates or resumes the group's Codex manager session. The first manager request creates the session with the full manager bootstrap prompt and current DAG/task context; later requests reuse that session and send only the user/request prompt so the transcript is not polluted with repeated bootstrap context.
- `dispatch()` first creates an idle Codex/Hermes executor session, records the real `agent_session_id`, then sends a task-specific prompt containing the group goal/plan/constraints, task contract, dependency artifact paths, and a worker-only Task API contract with absolute URLs based on `dag.base_url`. Running, waiting, and terminal tasks are rejected for dispatch even when force is requested, which prevents duplicate sessions from stale ready rows or repeated clicks.
- `dispatch()` also creates `~/.view/logs/agent-tasks/{task_id}/workspace/`, records it in `runtime.task_workspace`, and adds it as a `task_workspace` artifact so executors have an isolated write area.
- `/api/agent-tasks/{task_id}/reset` accepts `action=clear|retry`, finds the selected task plus every direct/indirect downstream task that depends on it, rejects the operation if any affected task is active, deletes each affected `~/.view/logs/agent-tasks/{task_id}/` directory, clears runtime/artifacts/result/session state, resets status to backlog/ready/blocked through `recompute_ready()`, and dispatches the selected task when `action=retry` and dependencies are satisfied.
- `set_process()` records a long-running external PID and moves the task to `waiting_process`.
- `/api/agent-tasks/{task_id}/manager-request` is the executor shortcut for sending task-scoped requests to the group manager session.
- `/api/agent-tasks/{task_id}/complete` expects `result` to contain `summary`, `metrics`, `failure_reason`, `next_suggestions`, and `user_decision_needed`, but also accepts legacy/top-level `summary`, `metrics`, and related fields and folds them into `result`. If `agent_session_id` is supplied, it must match the task's active session before completion is accepted.
- Manager prompts include a manager Task API contract with concrete request shapes for listing tasks, reading context, creating tasks, patching mutable task fields, patching dependencies, updating the global plan, clearing/retrying downstream task state, and completing reviewed tasks. Worker prompts include only the APIs workers should use: read context, manager request, register process, and complete. Both prompt types state the configured API base URL and instruct agents not to infer host or port from memory.
- The monitor loop checks waiting-process PIDs, moves exited tasks to `review`, asks the group manager to inspect logs/results and reschedule, and auto-dispatches one ready task per auto-mode group on each tick.
- `events` table records task mutations with before/after JSON and reasons for auditability.
- `frontend/src/components/AgentTasksPage.vue` opens new groups through a modal dialog that captures group id, goal, context, and project root. The last opened group is saved in user-namespaced browser storage and used as the default group the next time the page opens.

`docs/agent_tasks.md`

- User-facing design and API documentation for the Agent Task DAG system.

`skills/agent-task-dag/SKILL.md`

- Portable Codex-facing instructions for operating the Agent Task DAG HTTP API when this project is published or reused.

`backend/app/events.py`

- Server-Sent Events fanout for filesystem changes.
- `EventHub.publish(event)`: pushes a `WatchEvent` to every subscriber queue and drops full queues.
- `EventHub.subscribe()`: async generator yielding `ready` then `file-change` SSE messages.
- `hub`: singleton used by `main.py` and `watcher.py`.

`backend/app/ws_clients.py`

- Shared WebSocket client queue/fanout helpers for backend managers.
- `WebSocketClient`: websocket, outgoing queue, and writer task.
- `add_client()`, `enqueue()`, `broadcast()`, `remove_client()`, and `client_writer()`: common JSON message queueing, stale-client cleanup, timeout-bounded writes, and socket close handling used by terminal and Codex session managers.

`backend/app/git_diff.py`

- Git working-tree integration for the Diff sidebar and viewer.
- Resolves the active Git context from the requested file/sidebar directory, matching the current Files directory model used by Terminal and Codex launches. Status queries are scoped to that directory, while returned paths are mapped back to served-root-relative paths for the frontend.
- `git_status()`: parses `git status --porcelain=v1 -z`, adds `git diff --numstat HEAD` counts, marks binary files, and returns relative paths for changed files.
- `git_diff(path)`: returns a unified text diff against `HEAD`; untracked text files are rendered as new-file diffs and binary files return `is_binary=true` with no diff text.
- `git_stage(path)`, `git_revert(path)`, `git_commit(request)`, and `git_push()`: implement the toolbar Git actions. Revert removes only untracked files, refusing untracked directories.

`backend/app/watcher.py`

- Watches `settings.root_resolved` plus every configured profile home with non-recursive `watchfiles.watch` in a worker thread so scan/setup does not follow large symlinked trees and block FastAPI startup.
- `event_type(change)`: converts `watchfiles.Change` enum to API strings.
- `is_ignored_path(path)`: ignores high-churn `__outputs` directories while the `watchfiles` default filter ignores common development directories such as `.git`, `.venv`, and `node_modules`.
- `watch_root(stop_event)`: debounced watch loop; publishes `WatchEvent` with type, best-match root-relative path, directory flag, and mtime.

`backend/app/terminals.py`

- Interactive shell session manager using OS PTYs, async tasks, and WebSockets.
- `TerminalSession`: PTY process state, buffered output, per-session output log path, current PTY rows/cols, shared layout lock state, connected clients, locks, and tasks.
- Each terminal records `user_id`; `TerminalManager.list(user_id)` filters summaries to the active profile. New terminals use the profile home as their default cwd when no cwd is supplied.
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
- `CodexSession`: viewer-local session state including title, working directory, served-root-relative working directory for frontend filtering, Codex thread/session id, rollout path, prompts, server-persisted queued prompts, parsed rollout JSON events, process status, detached runner pid/codex pid/run id, connected WebSocket clients, and paths for metadata/stderr/run logs.
- Each Codex session metadata file includes `user_id`; list endpoints filter by active profile, and old metadata without `user_id` belongs to the configured default user. New sessions use the profile home as their default cwd when no cwd is supplied.
- Session metadata now stores an optional `model`; when set, runs pass `-m <model>` to `codex exec`.
- `cli_status()`: scans recent `~/.codex/sessions/**/rollout-*.jsonl` files, parses `token_count` events, and exposes a cached coarse status payload using the newest event timestamp for global 5-hour/weekly rate-limit chips. Codex `rate_limits.*.used_percent` values are treated as percentage points, and the API also exposes `*_remaining_percent` for "usage left" UI.
- `CodexSessionManager.create(prompt, cwd)`: creates a viewer session and writes metadata. If `prompt` is blank, the session stays `idle`; otherwise it starts a detached background runner for `codex exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -C <cwd> -`.
- `send(session_id, prompt)`: starts a detached background runner for a new `codex exec --json` run when no Codex thread id has been captured yet, otherwise resumes with `codex exec resume --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox <thread_id> -`. It rejects concurrent sends while a session has an active background run, appends the prompt to metadata, and writes that user prompt row to `~/.view/agent-history.sqlite3` for Super Workspace history association.
- Codex snapshots include the shared `pending_approvals` field, currently empty because Codex runs are still launched with approvals bypassed. The manager exposes the shared approval-resolution method so a future non-YOLO Codex runner can plug into the same frontend controls.
- Detached driver state: each run writes `state.json`, `stdout.jsonl`, `stderr.log`, and `prompt.txt` under `CODEX_RUN_DIR/{viewer_session_id}/{run_id}/`. The viewer metadata stores those paths plus the detached driver pid and Codex child pid. On startup, running sessions are reattached by reading this state and checking whether the driver pid is still alive.
- `enqueue()`, `update_queue_item()`, and `delete_queue_item()`: manage pending prompts in session metadata. Appending to the queue starts the drain loop immediately when the session is not running.
- `_start_next_queued(session)`: pops the next queued prompt, appends it to normal prompts, broadcasts a snapshot, and starts a Codex run. `_run()` calls it after each completed subprocess unless the current run was terminated by the user. `resume_pending_queues()` runs on backend startup so persisted queued work resumes without a browser connection.
- Codex subprocess proxying is controlled by `~/.view/config.json` `codex.proxy`; when non-empty it sets `https_proxy`, `HTTPS_PROXY`, `http_proxy`, and `HTTP_PROXY` for Codex runs, and when empty those variables are removed from the Codex subprocess environment. The default is no proxy.
- Rendered Codex events are read from the canonical `~/.codex/sessions/**/rollout-*.jsonl` file matched by Codex session id. Viewer-local metadata/prompts and the matched `rollout_path` are stored in `~/.view/logs/codex-sessions/{viewer_session_id}.json`; stderr goes to `{viewer_session_id}.stderr.log`.
- API snapshots and WebSocket event messages compact raw rollout entries into a transport/display shape before sending them to the frontend. Full raw events remain in memory/on disk for status parsing; compact events carry `event_type`, rendered `text`, `file_changes`, `patch_text`, and a bounded `raw_preview`. The detached Codex driver process also performs the same visible-event compaction while tailing rollout JSONL and writes provider message rows to `~/.view/agent-history.sqlite3`; this detached driver is the Codex provider-message ingestion path, not the Super Workspace runtime or backend watcher.
- Per-session Codex summaries parse `last_token_usage.total_tokens` from that session's matched rollout `token_count` events to expose `context_used_percent`, `model_context_window`, and `total_tokens`; navbar Codex chips use these session fields instead of the newest global rollout.
- `_find_session_id(raw)`: extracts `session_id`, `conversation_id`, or `thread_id` from JSON events so later messages can resume the correct Codex thread. For detached runs, the manager scans the persisted runner stdout JSONL file to discover this id after service restarts.
- Active Codex runs are monitored by polling detached runner state and matched rollout JSONL files, then broadcasting newly parsed events. The service can recreate this monitor after restart because stdout, state, and canonical rollout files are persisted outside process memory.
- Codex session status is updated from rollout turn-finish events and detached runner exit state: `task_complete` / `turn.completed` mark the viewer session `exited`, `turn_aborted` / `turn.failed` mark it `failed`, and runner `state.json` records the final process exit code when available.
- Codex live-refresh debugging uses Loguru in `codex_sessions.py`: background runner start/detach/finish, rollout matching, and turn-finish status changes log at info level; rollout unavailability, synced event counts, per-event broadcasts, and stale WebSocket clients log at debug level.
- `connect(session_id, websocket)`: sends snapshots and broadcasts live event/status/deleted messages to Codex panes.
- `terminate(session_id)`: stops the active detached runner process group for a session and broadcasts updated status.
- `codex_session_manager`: singleton used by routes.

`backend/app/codex_background_runner.py`

- Detached Codex driver process used by `codex_sessions.py` to keep Codex runs and history ingestion alive when the viewer service restarts.
- Starts the Codex child process, writes atomic `state.json` updates with driver pid, Codex child pid, discovered Codex session id, matched rollout path, rollout line count, status, exit code, timestamps, command, and cwd.
- Captures Codex stdout/stderr to run-local files, discovers the Codex session id from stdout JSON, matches the canonical `~/.codex/sessions/**/rollout-*.jsonl`, tails new rollout lines, converts visible raw Codex events into the fixed AgentEvent IR, and writes provider message rows plus file changes directly to `~/.view/agent-history.sqlite3`.
- Backend server restart does not stop this driver. While the backend is down, the driver continues running Codex, monitoring rollout JSONL, and writing DB rows; after restart, `codex_sessions.py` reads `state.json` to recover the driver pid, Codex pid, Codex session id, and rollout path.

`backend/app/hermes_sessions.py`

- Structured Hermes session manager using the local Hermes API server and Hermes SQLite state instead of terminal rendering.
- `HermesSession`: viewer-local state including title, working directory, Hermes session id, Hermes run id, prompts, queued prompts, compacted DB events, process-like status, connected WebSocket clients, and metadata path.
- Each Hermes metadata file includes `user_id`; list endpoints filter by active profile, and old metadata without `user_id` belongs to the configured default user. New sessions use the profile home as their default cwd when no cwd is supplied.
- `create(prompt, cwd)`: creates idle viewer metadata with `hermes_session_id` seeded from the viewer session id; a non-empty prompt immediately starts a Hermes `/v1/runs` request.
- `send(session_id, prompt)`: rejects concurrent running sends, persists the prompt, starts `/v1/runs` with `session_id`, and monitors run status through `/v1/runs/{run_id}` while polling `~/.hermes/state.db` for new messages.
- Hermes run monitoring also subscribes to `/v1/runs/{run_id}/events` so `approval.request` events become shared `pending_approvals` in viewer snapshots. The shared approval route calls Hermes `/v1/runs/{run_id}/approval` and updates the session after `approval.responded`.
- `enqueue()`, `update_queue_item()`, and `delete_queue_item()`: mirror Codex queue behavior and drain server-side when the session is not running.
- `_sync_db_events()`: reads `sessions` and `messages` from `VIEWER_HERMES_STATE_DB` / `~/.hermes/state.db`, hides user messages already represented by prompts, and converts assistant/tool rows into the compact `AgentEvent` transport shape used by the frontend. Hermes uses an exact source map: `role:assistant:reasoning_content` / `role:assistant:reasoning` -> `reasoning`, `role:assistant:content` -> `message:assistant`, `role:assistant:tool_calls` -> `tool_call`, and `role:tool:content` -> `tool_result`. Unmapped Hermes roles or DB fields are logged with bounded content previews so new provider shapes can be added deliberately.
- `terminate(session_id)`: calls Hermes `/v1/runs/{run_id}/stop` when a run id is known, cancels local monitoring, persists status, and broadcasts a session update.
- `connect(session_id, websocket)`: sends snapshots and broadcasts live event/status messages to Hermes panes.
- `hermes_session_manager`: singleton used by routes.

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

- Pydantic API schemas: `FileEntry`, `DirectoryListing`, `FileMeta`, `ConfigData`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `WatchEvent`, `TerminalInfo`, `TerminalCreate`, `TerminalSnapshot`, `ClientLog`.
- `ConfigData` stores global appearance, Markdown, Codex defaults, voice settings, and Task DAG defaults. Legacy workspace config models still exist for old state compatibility but are not exposed in the current frontend settings page.
- Agent schemas: shared `AgentPrompt`, compact `AgentEvent`, `AgentFileChange`, and `AgentQueueItem` describe the normalized intermediate representation used across providers. `AgentEvent.event_type` is typed by the backend `AgentEventType` enum and is a fixed backend IR, not a provider-native event name. Current fixed event types are `message:assistant`, `reasoning`, `tool_call`, `tool_result`, `custom_tool_call`, `exec_command_begin`, `exec_command_end`, `function_call`, `function_call_output`, `custom_tool_call_output`, `view_image_tool_call`, `patch_apply_end`, and fallback `operation` for logged-but-unclassified visible events. Agent drivers are responsible for converting provider-native logs into `AgentEventType` values before snapshots or WebSocket events reach the frontend. Frontend transcript code should only depend on this fixed IR; adding a new agent provider should require backend driver conversion work, not new frontend event-name handling, unless a genuinely new shared IR event type is introduced. Codex schemas expose `CodexSessionInfo`, `CodexSessionSnapshot`, `CodexCliStatus`, and `CodexModelOptions`; session creation/send/queue uses shared agent schemas and routes. `CodexSessionInfo.cwd_relative` mirrors the absolute `cwd` relative to the served root when possible, and running sessions expose background `pid`, `codex_pid`, `run_id`, and `run_started_at` fields when available. Hermes schemas reuse the shared agent prompt/event/queue shape, with Hermes-specific ids and DB path in `HermesSessionInfo`.
- `CodexConfig` stores Codex model options, muted operation-message alpha, the Diff viewer auto-commit prompt, and an optional `proxy` for `~/.view/config.json`; `default_model` controls new/resumed Codex runs unless the user manually selects a different model in the pane toolbar. The built-in default model is `gpt-5.5`, `muted_message_alpha` defaults to `0.56`, and `proxy` defaults to empty/no proxy.
- `DagConfig.base_url` stores the Viewer API origin injected into Agent Task manager/executor prompts; when empty, the backend falls back to `http://127.0.0.1:{VIEWER_PORT}` during prompt generation.
- These should stay aligned with TypeScript interfaces under `frontend/src/types/`.

`backend/app/super_workspace.py`

- Per-user Super Workspace role/workspace manager backed by `super_workspaces`, `super_workspace_user_state`, and `super_workspace_roles` rows in `~/.view/agent-history.sqlite3`. The first/default workspace is named `Default Super Workspace` and uses user-scoped id `{user}:default`; `super_workspace_user_state` records the active workspace id for each user.
- Exposes workspace list/activation helpers and role status summaries. Role status is deliberately simplified for frontend role displays: `queued`, `claimed`, and `running` driver-run rows map to `busy`; when no busy row exists, only the latest driver-run row can make the role `failed`; all other cases are `idle`.
- Defines local API models for `SuperWorkspaceData`, `SuperRole`, workspace/role update payloads, and dispatch responses so the feature stays isolated from workspace slots and the Agent Task DAG.
- `SuperWorkspaceManager.read()` / `write()` / `update()` / create/update/delete role methods persist the common prompt, fixed role rules, and current backing `provider:session_id` ref.
- `dispatch()` reads role descriptions, calls an OpenAI-compatible chat-completions endpoint with JSON response format, validates returned role ids against the candidate roles, and returns the selected ids/rationale. It intentionally only routes messages; the backend runtime owns creating/resuming the actual role agent sessions for normal Super Workspace dispatch.

`backend/app/super_workspace_runtime.py`

- DB-backed Super Workspace dispatcher and independent dispatch-task worker process.
- Defines `SuperWorkspaceMessageCreate` for new user/role-originated query messages. The payload carries optional structured `role_ids` for manual dispatch plus optional `parent_message_id` and `sender_role_id` lineage fields so a later role-to-role request can be tied back to the message that produced it.
- `SuperWorkspaceRuntime.submit()` parses leading `@Role ` query prefixes against all roles in the active Super Workspace, merges them with structured `role_ids`, persists the query message without the dispatch prefix, auto-dispatches through `SuperWorkspaceManager.dispatch()` using only the active chat's member roles when no explicit targets are supplied, marks the run queued, and writes one queued `super_workspace_driver_runs` dispatch-task row per concrete role id. Role mention keys are derived from role names using ASCII variable-name characters so frontend insertion and backend parsing agree.
- The backend runtime does not run the dispatch loop in-process. On startup it ensures a separate `super_workspace_worker.py` process is alive, with PID/state registered in `WEAVER_RUN_DIR`; startup refuses a live `worker.pid`, warning-logs and overwrites stale pid files, and direct worker invocation applies the same guard. The worker claims queued dispatch-task rows with a lease, skips concrete role ids that already have claimed/running tasks, and requeues tasks when that role id's current provider session is still running. Claimed tasks move through `claimed`, `running`, and terminal `completed`/`failed` states, and the parent query status is summarized from its target task statuses. The worker sends lightweight HTTP notifications to `/internal/super-workspace/notify` so the backend SSE stream can prompt frontend refreshes.
- `SuperAgentDriver` is the provider-driver base. It checks whether the role's current backing session is active, creates a fresh provider session when missing/stale, and starts the first role turn with common prompt plus fixed role rules plus the routed query. Later tasks resume the existing provider session with only the routed query. It does not use the provider session queue for Super Workspace dispatch; queueing is represented by DB task rows.
- `CodexSuperDriver` and `HermesSuperDriver` adapt the base driver to the existing provider managers. They reuse the existing detached Codex runner / Hermes run implementation for now while moving Super Workspace dispatch ownership out of the frontend.
- `SuperWorkspaceEventHub` streams lightweight run-created/run-updated notifications through `/api/super-workspace/events`; the history DB remains the source for actual messages, and the frontend uses the display item `updated_at` cursor with `/api/super-workspace/runs?after=...` to fetch changed flat items instead of reloading the newest page after every event.

`backend/app/agent_history.py`

- SQLite-backed agent history index stored at `~/.view/agent-history.sqlite3`. It is currently used by Super Workspace history only; normal workspace panes and agent panes still use the existing session snapshot APIs.
- The viewer-owned history DB is accessed through SQLAlchemy ORM mapped rows and per-operation sessions in `AgentHistoryStore`; its SQLite engine uses `NullPool` so each session closes its DB connection after use. Hermes `~/.hermes/state.db` remains an external read-only SQLite source.
- Defines `super_workspaces`, `super_workspace_user_state`, `super_workspace_roles`, `super_workspace_messages`, `super_workspace_driver_runs`, `super_workspace_message_file_changes`, `super_workspace_message_citations`, and `super_workspace_driver_checkpoints`. `super_workspaces` stores workspace id/name/common prompt, `super_workspace_user_state` stores each user's active workspace id, and `super_workspace_roles` stores fixed role prompts/settings per workspace. `super_workspace_messages` stores one intermediate-representation row per indexed message/event with scalar IR fields mapped directly to columns: `workspace_id`, `event_index`, `received_at`, `event_type`, `text`, `query`, and `patch_text`, plus provider/session/source path/line metadata, source-derived `occurred_at`, role, full provider `raw_json`, query/dispatch status, and driver-run association. `super_workspace_driver_runs` is the Super Workspace dispatch-task table: each row binds one workspace query message to one target role, stores the role snapshot, provider/session refs, routed prompt, parent/sender/recipient lineage, claim lease fields, attempt metadata, task status, and timestamps. `super_workspace_message_file_changes` stores the IR `file_changes[]` array as child rows with `path`, `change_type`, and `diff` columns instead of embedding it as JSON. `super_workspace_message_citations` stores ordered message-to-message citation edges where `source_message_id` is the query message and `cited_message_id` is a referenced Super Workspace message. `raw_preview` is not stored as an IR JSON blob because it is derivable from `raw_json`.
- Super Workspace lineage is stored directly on messages: messages carry `parent_message_id`, `sender_role_id`, and `recipient_role_id`; provider output rows associated with a driver run also carry `query_message_id` and `driver_run_id`. Current UI presents non-empty `query` messages as runs, but the persisted shape can evolve into a query/message graph without a separate query table.
- `create_super_run()` records each Super Workspace user query as a `super_workspace_messages` row with explicit `workspace_id`, empty display `text`, non-empty `query`, selected role ids stored on that same row so direct and auto dispatch can return selected ids before driver runs exist, and ordered citation edges written to `super_workspace_message_citations`.
- `record_super_target()` creates a queued dispatch-task row before any provider session is started. The worker later fills `session_ref`, `viewer_session_id`, and `agent_prompt` when it claims and starts the task.
- `claim_next_dispatch_task()` leases one queued task whose concrete role id has no claimed/running task, making role serialization DB-backed rather than process-memory-backed. Stale claimed leases are returned to queued, while running tasks keep the role id occupied until the worker marks them completed/failed. Roles with the same display name but different ids are independent and may run in parallel.
- `list_super_runs()` / `get_super_run()` return DB-only lazy pages of non-empty-query messages with dispatch-task targets. Provider message rows are selected by explicit `workspace_id` / `driver_run_id` lineage. Reads do not reopen Codex rollout JSONL or Hermes state.
- Provider message rows are inserted by the active provider driver process/watcher as provider output arrives. For Codex, that writer is the detached `codex_background_runner.py` driver, not the backend server. Super Workspace dispatch passes query, dispatch-task, parent, sender, and recipient ids into the Codex runner so every newly ingested Codex prompt/output row is directly linked to its dispatch task. `AgentHistoryStore` does not expose runtime-side resync helpers that reopen Codex rollout JSONL or Hermes state as a fallback; if a role response is visible in provider output but absent from this DB, the fault is in the driver ingestion path.

`frontend/src/components/AgentTasksPage.vue`

- Full-page Agent Task DAG board opened from the top bar beside Loop Tasks.
- Loads task groups and goals, selects tasks by `group_id`, shows manual/auto scheduler mode, exposes "Run Ready" for approved one-at-a-time debugging, renders status columns plus a simple parent/child tree, and edits/deletes the selected task's title, workspace, description, instruction, dependencies, runtime, artifacts, and result summary.
- The plan panel renders the group goal/plan/context/constraints through the shared Markdown renderer by default and can switch into an edit mode for modifying the underlying fields.
- The selected task toolbar includes `Retry` and `Clear`, both with browser confirmation. Both actions include every downstream task that depends on the selected task; `Clear` removes stored runtime/output state and `Retry` clears the same state before dispatching the selected task when it is ready.
- Includes a manager prompt box that sends planning/debug/rescheduling requests to the reusable Codex manager session. Manager and per-task agent sessions open in an overlay that embeds the full `AgentViewer.vue`, so the DAG page uses the same session transcript, prompt composer, focus/full detail switching, queue, and local-link preview behavior as normal workspace panes.

`frontend/src/stores/agentTasks.ts`

- Pinia wrapper around the Agent Task DAG REST API. Tracks selected group/task, task list, selected task context, per-group settings, dispatch actions, and dependency patches.

`frontend/src/types/agentTasks.ts`

- TypeScript mirror of the Agent Task DAG transport shapes: task status, execution, runtime, artifacts, result, policy, settings, context, and mutation payloads.

`backend/app/logging.py`

- Loguru setup and standard logging interception.
- `InterceptHandler.emit(record)`: redirects Python logging records into Loguru.
- `current_log_path()`: resolves `VIEWER_LOG_FILE`.
- `configure_logging(log_file, debug)`: configures stderr and rotating file logs, intercepts uvicorn/FastAPI error logs, and disables uvicorn access logs because app middleware already records failed/slow requests.
- `ensure_logging()`: idempotent fallback used by app import.

`backend/app/restart.py`

- Thin admin bridge from FastAPI to the detached process manager.
- `_run_manager(command)`: starts `scripts/manage_viewer.py` in a new session with the current backend PID and a short delay so the HTTP response can flush before the server is signaled.
- `request_restart()`: asks the manager to stop the current backend PID and start the default managed server command.
- `request_stop()`: asks the manager to stop the current backend PID without starting a replacement.

`backend/app/__init__.py`

- Empty package marker.

## Frontend Structure

`frontend/src/main.ts`

- Imports Bootstrap, icons, KaTeX, Highlight.js, and app CSS.
- Calls `installClientLogging()`.
- Creates Vue app, installs Pinia, mounts `App.vue`.

`frontend/public/favicon.svg`

- Browser tab icon for the Vue app. Vite copies files from `frontend/public` into the built frontend root, and `frontend/index.html` links this SVG favicon directly.

`frontend/src/App.vue`

- Root shell: top bar, profile selection, full-page settings, full-page loop tasks, full-page Agent Task DAG, and the Super Workspace split-pane shell as the normal page.
- Defaults directly to Super Workspace. The top bar has no Super Workspace button and no old pane-workspace button.
- Loads config, the root file listing, terminal lists, Codex model options, and agent provider/session state before mounting the Super Workspace page.
- Applies appearance and active Markdown theme settings from `stores/files.ts` as CSS variables on the app shell, including nav height/icon size and Markdown/syntax colors.
- Connects SSE with `connectEvents()`.
- Refreshes affected file listings on change events.
- Dispatches every filesystem SSE as `viewer:file-changed`; individual panes filter these events against their own file path or viewer-specific dependency set.
- Polls terminal list every 15 seconds with overlap guarded in `stores/terminals.ts`.
- Renders active pane toolbar metadata, actions, and generic controls from `stores/paneToolbar.ts` in the top bar while Super Workspace is active, plus global pane split actions.
- On phone-width screens, active-pane toolbar actions and controls collapse behind a top-bar overflow menu while global pane actions remain inline.
- Top-bar ownership rule: cross-viewer pane actions such as split belong in `App.vue`; view-specific icons/controls/status belong in the owning viewer and must be registered through `stores/paneToolbar.ts`, not hard-coded in `App.vue`. The global SSE connection dot is intentionally not rendered.
- The global pane toolbar in `App.vue` owns pane-level navigation actions such as Go Back, Split, and Close. `stores/layout.ts` keeps each pane's local content history so a pane can return from a linked file, sidebar-opened file, diff, terminal, or agent session to its previous content.
- User selection: `useUsersStore` loads `/api/users`, reads/writes `viewer.activeUser.v1`, and blocks normal app startup behind the first-run profile picker when no valid active profile is stored. The Settings page includes a profile selector that stores the new user and reloads the app so all stores reconnect under the new profile.
- File viewer scroll memory lives in `utils/scrollMemory.ts` and `composables/useScrollMemory.ts`. Scroll positions are browser-local, user-namespaced, and keyed by workspace id, pane id, and file path so two panes can keep different positions for the same file; layout navigation emits `viewer:pane-before-navigate` before replacing pane content and workspace switching emits `viewer:workspace-before-switch` before the displayed workspace id changes so the current viewer can save its last scroll position under the correct key.
- Agent provider/session list is loaded on startup and polled every 15 seconds through `stores/agents.ts`.
- `LocalFilePreview.vue` is a floating file preview dialog used for transient local-file inspection from Codex transcript links. It resolves to normal viewer components and offers only three navigation actions: open in the active pane, open in a vertical split, or open in a horizontal split.

`frontend/src/components/SuperWorkspacePage.vue`

- Default full-page Super Workspace split-pane shell.
- Loads per-user Super Workspace workspace state through `/api/super-workspace/workspaces`, active chats through `/api/super-workspace/chats`, active workspace roles through `/api/super-workspace`, and agent provider metadata for role editing.
- Owns the sidebar drawer/pin/resize state and routes `FileSidebar.vue` events into chat, role, file, diff, and terminal pane actions.
- Chat CRUD opens chats as normal recursive layout panes through `layout.openChat(chatId)`. Role CRUD is global to the active Super Workspace; deleting a role also removes it from any chat membership lists.

`frontend/src/components/SuperWorkspaceChatPane.vue`

- Chat pane for a single `chatId`; loads flat display feed pages from `/api/super-workspace/runs?chat_id=...`, subscribes to `/api/super-workspace/events`, and incrementally refreshes changed runs.
- The composer creates a persisted run through `/api/super-workspace/runs`. It sends structured `role_ids` from `stores/superChatDispatch.ts` when a manual target chip is selected in the Chats side panel, and still supports typed leading `@Role` mentions plus `@msg-{message_id}` citation tokens.
- Visible user messages and final role responses have small cite buttons that insert `@msg-{message_id}` into the leading composer prefix. Backend `super_workspace_runtime.py` parses citation tokens, writes citation edges and queued dispatch-task rows, and leaves execution to the independent Super Workspace worker process.
- The page renders flat display items directly: user query items show dispatch state and target chips, assistant `message:assistant` items show the target role label and Markdown-rendered message text, while reasoning/tool/thinking rows stay hidden at the display-feed query layer. Focus snapshots from the shared agent API are still used for link base directories, not as the primary role lifecycle source.

`frontend/src/components/DirectoryPicker.vue`

- Reusable stepped directory selector backed by `/api/tree`.
- Displays one select per path level starting at the active profile root, loads child directories for each selected parent, and emits the selected served-root-relative directory string for cwd-style fields.

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
- Chooses `TerminalViewer`, unified `AgentViewer`, `DiffViewer`, `ImageViewer`, `LargeTextViewer`, `MarkdownViewer`, `HtmlViewer`, lazy-loaded `PdfViewer`, `TextViewer`, or `UnsupportedViewer`.
- Adds a transparent activation shield over inactive HTML iframe previews so iframe pointer handling cannot leave the wrong pane active before sidebar actions replace the active pane.

`frontend/src/components/FileSidebar.vue`

- Sidebar shell with a VS Code-style activity rail and one active tool panel at a time.
- Persists the active sidebar tool in `localStorage` under `viewer.sidebarActiveTool.v1`.
- Tools: Chats, Roles, Files, Changes, and Terminals. Future side tools should be added to this shell instead of mixing unrelated lists into one panel.
- The activity rail stays visible even when the tool panel is closed. Clicking a different tool changes only the active selection; clicking the already-active tool toggles the tool panel open/closed.
- On phone-width screens, pinned and unpinned tool panels behave as an overlay beside the always-visible activity rail so the workspace is not narrowed by the saved desktop sidebar width.
- Re-emits `open-chat`, role CRUD, `open-file`, `open-diff`, and `open-terminal` events to `SuperWorkspacePage.vue`.

`frontend/src/components/sidebar/FilesPanel.vue`

- Files tool panel: pinned paths, current folder, parent button, upload button, drag-and-drop upload target, file delete confirmation/error display, and `FileTree`.
- Shows the active profile `home` as the current path label when browsing the user's root (`path=""`).
- `openPinned(path)`: tries to enter pinned path as directory, otherwise emits `open-file`.

`frontend/src/components/sidebar/TerminalsPanel.vue`

- Terminals tool panel: new terminal button plus terminal list.
- `newTerminal()`: creates terminal in the current sidebar directory and emits `open-terminal`.
- `closeTerminal(id)`: deletes terminal and clears matching panes.

`frontend/src/components/sidebar/GitPanel.vue`

- Changes tool panel: lists Git changed files from `/api/git/status`, showing served-root-relative paths, status codes, and small `+/-` line counts.
- Loads Git status only from the manual Refresh button; the sidebar does not poll or auto-refresh Git status.
- Binary files are displayed with a `bin` chip but disabled so they cannot be opened in the diff viewer.
- Clicking a text change emits `open-diff` with the current Files directory so the active pane becomes a diff pane scoped to that directory.

`frontend/src/components/sidebar/AgentSessionsPanel.vue`

- Unified Agents tool panel. Fetches provider metadata from the backend-backed `stores/agents.ts`, shows a provider dropdown for new sessions, creates the selected agent in the current sidebar directory, filters the global agent session list to refs remembered by the active workspace, and opens rows through `layout.openAgentSession(ref)`. Rows render provider icons, active row state, voice pending/ready row state, running status dots, unread completion/failure indicators, error display, and remove-from-workspace action from one shared appearance layer.

`frontend/src/components/ConfigPanel.vue`

- Full-page configuration UI opened from the top bar Settings button.
- Edits `~/.view/config.json` through the existing `/api/config` endpoint.
- Sections: Server, User Profile, Appearance, Workspace, Codex Models, Voice, Task DAG, Markdown, Syntax Highlighting, and raw JSON.
- Server section has confirmed restart and stop buttons. Restart calls `/api/admin/restart`, polls `/api/health` until the PID changes, then reloads the page. Stop calls `/api/admin/stop` and leaves a command-line restart hint.
- Appearance currently controls nav bar size, which also drives icon/button size via CSS variables.
- Codex Models controls the default Codex model, the available model list used by `/api/codex/models` and the Codex pane toolbar, and the optional Codex subprocess proxy.
- Voice controls voice enablement, the persisted Whisper model option list, selected model, language code, translation toggle, and target language used by `/api/voice/ws`.
- Markdown config stores an active theme plus a theme list. The editor can duplicate/reset themes and edit heading/body/paragraph/code font sizes, colors, weights, link/code/border colors, and Highlight.js token colors.

`frontend/src/components/LoopTasksPage.vue`

- Full-page Loop Tasks UI opened from the top bar Loop Tasks button.
- Left panel lists loop task names, current state, last status, and next run time.
- Right panel has an Edit/Logs segmented switch.
- Edit mode exposes generated YAML frontmatter as form controls: enabled, Codex model, working directory, UTC+8 timezone display, schedule type/time fields, run limits, session policy/reset fields, stop regex, and Markdown prompt body.
- Logs mode lists run records as collapsible panels. Expanding a run loads the backend detail JSON and renders the stored compact Codex session snapshot with prompt, event text, patch text, and file changes.

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
- Registers Text-specific top-bar actions for manual reload, edit mode, and copy-all. `.env`, `.env.*`, and `*.env` paths use shell-style highlighting.
- Edit mode replaces the read-only highlighted preview with a split textarea and live highlighted preview plus bottom save/cancel controls. The editor and highlighted preview panes synchronize scroll position proportionally in both directions. Save writes through `/api/file/content` PUT, updates the highlighted preview, and exits edit mode; cancel restores the last loaded text without writing.
- `saveCurrentScroll()`: saves scroll position.
- `copyAll()`: clipboard write with textarea fallback.
- `load()`: fetches text, highlights, restores scroll.
- Uses syntax CSS variables from the active Markdown theme for Highlight.js token colors.

`frontend/src/components/viewers/DiffViewer.vue`

- Diff preview pane backed by `/api/git/diff`.
- Renders unified diffs with Highlight.js `diff` highlighting, word-level diff mode, and side-by-side split diff mode.
- Registers Diff-specific top-bar actions through `stores/paneToolbar.ts`: view mode switches, auto commit, refresh, stage file, stage all, revert file, commit, and push. Auto commit creates a Codex session in the diff directory using `codex.auto_commit_prompt`, remembers it in the active workspace's Codex session list, and lets the session handle summarize/commit/push.
- Binary diffs render a disabled-state message instead of diff text.

`frontend/src/components/viewers/HtmlViewer.vue`

- HTML preview with rendered/raw modes.
- Rendered mode uses an iframe pointed at `/api/file/site/{path}` so normal browser loading handles local scripts, stylesheets, images, media, and in-document navigation.
- Raw mode fetches `/api/file/content` and highlights as XML/HTML with Highlight.js.
- Registers HTML-specific top-bar actions for reload, rendered/raw switching, and opening the static-site URL in a new tab.
- Tracks direct local `src`, `href`, `poster`, `data`, and `srcset` dependencies from the HTML source and reloads the iframe when those files change. For `index.html`, changes under the same directory also trigger reloads so simple static folders update without precise dependency discovery.

`frontend/src/components/viewers/MarkdownViewer.vue`

- Markdown preview using `markdown-it` plugins, KaTeX, Mermaid, and Highlight.js.
- Enables raw HTML and `securityLevel: "loose"` for Mermaid, so trust boundary is local/private content.
- `escapeHtml(value)`: code-block fallback escaping.
- Custom fence renderer turns ```mermaid fences into Mermaid blocks.
- `renderMermaidIn()`: replaces Mermaid blocks with rendered SVG or marks errors.
- `load()`: fetches Markdown text, renders HTML with the current Markdown path as link context, tracks local image dependencies, renders Mermaid, restores scroll.
- Registers Markdown-specific top-bar actions for manual reload, edit mode, and rendered/raw view switching.
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
- Registers terminal shell/status, quick-key controls, and terminate action with `stores/paneToolbar.ts` for top-bar rendering while the pane is active.
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

`frontend/src/components/viewers/AgentViewer.vue`

- Unified structured agent session pane connected to `/api/agents/sessions/{id}/ws?provider={provider}&detail={focus|full}`.
- Accepts one `agentRef` (`provider:session_id`), receives the initial snapshot and live JSON events/status over WebSocket, and uses REST snapshots only for explicit refresh/fallback. Focus mode connects with `detail=focus`; showing operation details or raw JSON reconnects with `detail=full`.
- Uses `AgentSessionTranscript.vue` for shared prompt/event rendering, status, provider session id, cwd, local transcript links, inline patch/file-change details, wrapped word-level highlights inside paired added/deleted diff lines, derived post-change result snippets below diffs, and raw JSON preview.
- Local links in rendered transcript Markdown are resolved relative to `session.cwd_relative` through `/api/file/resolve-directory-link` and open in the floating `LocalFilePreview` dialog before the user chooses active-pane or split navigation.
- Registers provider-aware top-bar controls through `stores/paneToolbar.ts`: refresh, create another same-provider session in the same working directory, focus composer, raw JSON toggle, token/context chips, and Codex model/rate-limit controls when the provider is Codex.
- Sends follow-up prompts and server-backed queue operations through the generic `/api/agents/sessions/*` REST APIs. Queue rows can be clicked into `AgentPromptComposer.vue` for edit/save/cancel/delete, and queued work drains server-side even after the browser closes.
- Persists unqueued composer drafts per provider/session ref in browser `localStorage` under `viewer.agentDrafts.v1`, restores them after pane/workspace remounts, and clears the draft after a queue request succeeds.

`frontend/src/components/AgentPromptComposer.vue`

- Shared Codex/Hermes prompt composer with the Codex input styling and behavior: server-backed queue list, queued-message edit/save/cancel/delete controls, microphone transcription through `VoiceTextarea.vue`, queue, clear, and stop actions. Parent viewers own persistence and API calls through emitted events.

`frontend/src/components/VoiceTextarea.vue`

- Reusable textarea plus voice-input action bar. It owns only textarea binding, `VoiceInputButton.vue`, optional Clear, keyboard event forwarding, and an `actions` slot for caller-specific buttons such as Queue/Stop, Save/Delete, or Dispatch.
- The voice state still lives in `stores/voice.ts`; callers must provide a stable `contextId` so recording/transcription state stays scoped to the right prompt or role editor.

`frontend/src/components/AgentSessionTranscript.vue`

- Shared Codex/Hermes transcript display layer. Owns agent content appearance and behavior: session metadata, idle/running/error rows, prompt/event timeline ordering, Markdown rendering, Mermaid rendering, local link click emission, focus-mode muted operation filtering through an exact `AgentEvent.event_type` display map, raw JSON preview, file-change/patch diffs, word-level diff highlighting, derived result snippets, and scroll-to-bottom helpers exposed to parent viewers. Because drivers normalize provider-native events into the fixed backend `AgentEvent` IR, this component should not contain provider-specific event-name parsing. Unknown frontend event types are posted to `/api/debug/client-log` with bounded previews because they indicate the backend emitted an unregistered shared IR type.

`frontend/src/stores/voice.ts`

- Browser voice job store keyed by input context ids such as `codex:{session_id}:prompt` and `terminal:{terminal_id}:paste`.
- Owns `MediaRecorder`, microphone stream, voice WebSocket lifecycle, chunk sending, `ready` / `processing` / `partial` / `committed` / `final` / `error` handling, the persisted default-on `11M` language-model-refine toggle, and context text/status state. `partial` and `final` replace the current voice transcript suffix from the recording's base text because voice-service sends full accumulated text.
- Voice jobs survive component unmounts after recording has stopped, so users can switch sessions while final transcription is processing and return to the same pending/ready draft.
- Context statuses drive sidebar indicators: processing/recording contexts show as pending, completed but unsent voice text shows as ready.

`frontend/src/components/VoiceInputButton.vue`

- Reusable microphone transcription button backed by `/api/voice/ws`.
- Requires a stable `contextId`, binds the voice store's context text to its `v-model`, toggles start/stop for that context, and renders the adjacent `11M` toggle. When enabled, the browser `start` message includes `llm_refine`, `language_model_refine`, and `languageModelRefine` for compatibility with the standalone voice service. Consumers still decide when to send or queue the text.

## Frontend Stores And API

`frontend/src/api/client.ts`

- Shared fetch helpers for REST and raw/WS URLs.
- `request<T>()`: JSON request with error text on non-2xx.
- File APIs: `rawUrl(path, contentHash?)`, `getTree()`, `getMeta()`, `getText()`, `getConfig()`, `putConfig()`.
- Git APIs: `getGitStatus()`, `getGitDiff()`, `stageGitPath()`, `revertGitPath()`, `commitGit()`, and `pushGit()`.
- Admin APIs: `restartServer()` and `stopServer()`.
- Terminal APIs: `listTerminals()`, `createTerminal(cwd)`, `getTerminal()`, `terminateTerminal()`, `deleteTerminal()`, `terminalSocketUrl()`.
- Agent APIs: `listAgentProviders()`, `listAgentSessions(provider)`, `createAgentSession(provider, prompt, cwd, model)`, `getAgentSession(provider, id, detail)`, `sendAgentMessage(provider, id, prompt, model)`, `queueAgentMessage(provider, id, prompt, model)`, `terminateAgentSession(provider, id)`, `resolveAgentApproval(...)`, and `agentSessionSocketUrl(provider, id, detail)`.
- Super Workspace APIs: `listSuperWorkspaces()`, `activateSuperWorkspace()`, `getSuperWorkspace()`, `updateSuperWorkspace()`, `getSuperRoleStatuses()`, role CRUD helpers, `listSuperWorkspaceRuns()`, `createSuperWorkspaceRun()`, and the raw `dispatchSuperWorkspace()` wrapper cover workspace activation, role storage/status, persisted query history, and LLM routing. Normal role-message delivery is owned by the backend Super Workspace runtime, not by frontend shared agent session helpers.
- `frontend/src/api/client.ts` appends the active user id from `viewer.activeUser.v1` to REST and WebSocket API URLs; `frontend/src/api/events.ts` does the same for the SSE stream.
- Agent loop APIs: `listAgentLoops()`, `createAgentLoop()`, `reloadAgentLoops()`, `updateAgentLoop()`, `deleteAgentLoop()`, `runAgentLoop()`, `pauseAgentLoop()`, `resumeAgentLoop()`, `resetAgentLoopSession()`, `listAgentLoopRuns()`, and `getAgentLoopRun()`.
- Voice API helper: `voiceSocketUrl()` builds the browser WebSocket URL for `/api/voice/ws`, using `wss://` when the page is served over HTTPS.

`frontend/src/api/events.ts`

- `connectEvents(onChange, onState)`: creates `EventSource` for `/api/events`, reports connection state, parses `file-change` events.

`frontend/src/stores/files.ts`

- Pinia store for directory listings, current path, expanded dirs, pinned paths, visit timestamps, appearance config, Markdown theme config, Codex config, voice config, Task DAG config, and loading state. Appearance, Markdown themes, Codex config, voice config, DAG config, and profile definitions are persisted in `~/.view/config.json`.
- Getters: `rootEntries`, `currentEntries`, `parentPath`, `activeMarkdownTheme`.
- Actions: `loadConfig()`, `saveConfig()`, `saveAppearance()`, `saveMarkdown()`, `saveViewerConfig()`, `saveFullViewerConfig()`, `loadDirectory()`, `enterDirectory()`, `enterParentDirectory()`, `toggleDirectory()`, `refreshAffected()`, `togglePin()`. `enterDirectory()` persists the last visited sidebar directory.

`frontend/src/stores/layout.ts`

- Pinia store for recursive split layout and active pane.
- Helpers: `id()`, `defaultLayout()`, `findPane()`, `mapNode()`, `firstPaneId()`, `mapAllPanes()`, `removePane()`.
- Getters: `activePane`, `openPaths`, `openTerminalIds`, `openCodexSessionIds`, `openHermesSessionIds`, `openDiffPaths`.
- Actions: `load()`, `save()`, `snapshot()`, `restore()`, `reset()`, `setActive()`, `openFile()`, `openTerminal()`, `openCodexSession()`, `openHermesSession()`, `openDiff()`, `splitPane()`, `setRatio()`, `clearPane()`, `closePane()`, `clearTerminal()`, `clearCodexSession()`, `clearHermesSession()`.
- `openFileInSplit(path, direction)` creates a new split beside the active pane, opens a file in the new pane, and makes that pane active. It is used by floating local-file previews.
- Persists to `localStorage` key `viewer.layout.v1`.

`frontend/src/stores/codex.ts`

- Pinia store for Codex global status, model options, and the Git auto-commit helper's Codex session creation. Agent transcript lifecycle is otherwise owned by `stores/agents.ts`.

`frontend/src/stores/agentLoops.ts`

- Pinia store for loop task summaries, selected task id, run summaries, and loaded run details.
- Actions wrap `/api/agent-loops` endpoints: load/reload/create/save/delete, run now, pause/resume, reset session, load runs, and load one run detail.

`frontend/src/stores/paneToolbar.ts`

- Non-persistent active-pane toolbar registry.
- Exposes generic per-pane title/status/action/control metadata so viewers can contribute top-bar controls without coupling `App.vue` to viewer-specific behavior.
- Actions may hold callbacks owned by the registering viewer and are cleared when that viewer unmounts.

`frontend/src/stores/terminals.ts`

- Pinia store for terminal summaries.
- Actions: `load()`, `create()`, `upsert()`, `terminate()`, `remove()`.

`frontend/src/types/files.ts`

- TypeScript mirror of backend file/config/watch schemas: `EntryType`, `PreviewType`, `FileEntry`, `DirectoryListing`, `FileMeta`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `ViewerConfig`, `WatchEvent`.

`frontend/src/types/git.ts`

- TypeScript mirror of backend Git schemas: `GitDiffFile`, `GitStatus`, and `GitDiffText`.

`frontend/src/types/layout.ts`

- Recursive `LayoutNode` union and `SplitDirection`; pane nodes may hold `filePath`, `terminalId`, `agentSession`, `diffPath`, or `diffCwd`.

`frontend/src/types/terminals.ts`

- TypeScript mirror of terminal schemas: `TerminalStatus`, `TerminalInfo`, `TerminalSnapshot`.

`frontend/src/types/codex.ts`

- TypeScript mirror of Codex schemas: `CodexStatus`, `CodexSessionInfo`, `CodexSessionSnapshot`, and Codex-specific status/model fields. Shared prompt/event/file-change/queue item shapes live in `types/agents.ts` as `AgentPrompt`, `AgentEvent`, `AgentFileChange`, and `AgentQueueItem`.

`frontend/src/types/agents.ts`

- Shared agent provider/session types plus normalized `AgentPrompt`, `AgentEvent`, `AgentFileChange`, and `AgentQueueItem` shapes used by Codex, Hermes, and the shared agent transcript UI.

`frontend/src/types/hermes.ts`

- TypeScript mirror of Hermes schemas: `HermesStatus`, `HermesSessionInfo`, and `HermesSessionSnapshot`; it reuses the shared agent prompt/event/queue item shapes for normalized transcript display.

`frontend/src/types/superWorkspace.ts`

- TypeScript mirror of Super Workspace storage, workspace list/activation, role status, persisted run history, and dispatch response shapes: common prompt, `SuperRole`, `SuperWorkspaceSummary`, `SuperRoleStatus`, workspace/role create/patch payloads, `AgentHistoryMessage`, `SuperHistoryRun`, `SuperHistoryTarget`, paginated run responses, and selected route ids/rationale.

`frontend/src/types/agentLoops.ts`

- TypeScript mirror of agent loop schemas: task definition, schedule/run/session/stop config, runtime state, task info, and run record/detail.

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

`frontend/src/utils/clientLog.ts`

- Sends frontend errors to `/api/debug/client-log`.
- `stringify(value)`: converts unknown error payloads.
- `sendClientLog(entry)`: uses `navigator.sendBeacon()` with fetch fallback.
- `installClientLogging()`: hooks `window.error`, `unhandledrejection`, and `console.error`.

`frontend/src/styles.css`

- Global app layout and shared classes: shell, top bar, sidebar drawer/pinned mode, resizers, workspace wrapper, icon buttons, mobile sidebar behavior, scroll area, muted text, shared Markdown rendering, and shared Highlight.js token colors.
- CSS variables: `--sidebar-width`, `--topbar-height`, `--nav-button-size`, `--nav-icon-size`, `--border`, `--panel`, `--surface`, `--text-muted`.

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

- User-facing setup, run, feature, and debug-log instructions.

`PROJECT_PLAN.md`

- Original implementation plan and desired architecture notes. Some details are historical; prefer this `architecture.md` for current code.

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
- `WorkspaceData` / `WorkspaceSnapshot` <-> matching workspace TypeScript interfaces.
- `WorkspaceData` / `WorkspaceSnapshot` <-> `WorkspaceData` / `WorkspaceSnapshot`
- `MarkdownConfig` / `MarkdownTheme` <-> `MarkdownConfig` / `MarkdownTheme`
- `WatchEvent` <-> `WatchEvent`
- `TerminalInfo` <-> `TerminalInfo` including PTY rows/cols and shared layout lock state.
- `TerminalSnapshot` <-> `TerminalSnapshot` including PTY rows/cols and shared layout lock state.
- `AgentLoopDefinition` / `AgentLoopInfo` / `AgentLoopRunRecord` <-> matching loop task TypeScript interfaces.

If a backend field changes, update the matching frontend type and all consumers.

## Persistence

- `~/.view/config.json`: appearance, Codex model options, voice model/language/translation options, Markdown themes, user profiles, and default user, managed by `/api/config` and `/api/users`. On first use, missing config is copied from served-root `.viewer.config.json` if present; old workspace-state keys in config are ignored rather than migrated.
- `~/.view/agent-history.sqlite3`: shared agent history index used first by Super Workspace and managed through SQLAlchemy ORM sessions with `NullPool` connection handling. It stores Super Workspace definitions, roles, user messages/runs/targets plus provider driver-written intermediate message rows with full raw JSON, structured IR columns (`workspace_id`, `event_index`, `received_at`, `event_type`, `text`, `patch_text`, and `file_changes` child rows), provider/session/source path/line metadata, and source-derived timestamps. Runtime read/status paths do not resync or reparse provider source files as a fallback.
- `~/.view/agent-tasks.sqlite3`: Agent Task DAG persistence managed through SQLAlchemy ORM sessions with `NullPool` connection handling. It stores tasks, dependency/runtime/artifact/result JSON payloads, group-level scheduler/manager settings, and audit events with before/after JSON.
- `~/.view/loops/*.md`: Markdown loop task definitions with generated YAML frontmatter and prompt body.
- `~/.view/agent-loops.json`: scheduler runtime state for loop tasks.
- `localStorage viewer.activeUser.v1`: selected soft user profile id.
- `localStorage viewer.layout.v1.{user}` and `viewer.layout.v1.{user}.super-workspace`: split tree, active pane, and open pane content ids, with the Super Workspace scope used by the normal app shell.
- `localStorage viewer.superSidebarPinned.v1.{user}` and `viewer.superSidebarWidth.v1.{user}`: Super Workspace sidebar state.
- `localStorage viewer.sidebarActiveTool.v1.{user}`: selected Super Workspace sidebar tool.
- `localStorage viewer.scrollPositions.v1.{user}`: per-path scroll positions.
- `localStorage viewer.agentDrafts.v1.{user}`: unsent agent prompt drafts.
- `~/.view/logs/viewer-*.log`: timestamped runtime logs from `run.py`.
- `~/.view/logs/codex-sessions/*.jsonl`: obsolete viewer-local Codex stdout caches from older versions; rendering now reads canonical rollout JSONL files under `~/.codex/sessions/`.
- `~/.view/logs/codex-sessions/*.stderr.log`: stderr from Codex subprocesses.
- `~/.view/logs/codex-sessions/*.json`: viewer-local Codex session metadata including prompts, discovered Codex thread id, selected model, status, and matched rollout path.
- `~/.view/logs/terminals/*.log`: terminal replay logs.
- `~/.view/logs/agent-loops/{task_id}/runs.jsonl` and `{run_id}.json`: loop run summaries and detailed compact Codex snapshots.

## Common Fault Locations

- File cannot open or wrong preview type: check `backend/app/files.py` `preview_kind()`, `get_meta()`, frontend `ViewerPane.vue`, and specific viewer.
- Directory tree stale: check `backend/app/watcher.py`, `backend/app/events.py`, `frontend/src/api/events.ts`, and `files.refreshAffected()`.
- Live refresh not firing: check SSE `/api/events`, `App.vue` `connectEvents()` callback, and `ViewerPane.vue` `handleChange()`.
- Text too large: `settings.max_text_preview_bytes` and `read_text()`.
- Path/security issues: `normalize_relative()`, `resolve_path()`, symlink behavior in `files.py`.
- Terminal creation fails: `settings.terminal_shell`, `TerminalManager.create()`, shell availability, PTY permissions.
- Terminal output glitches: `TerminalViewer.vue` snapshot/output version logic and `TerminalManager._read_output()`.
- Terminal resize issues: `TerminalViewer.vue resize()` and `TerminalManager.resize()`.
- Codex session creation/resume fails: check `codex` availability on PATH, `backend/app/codex_sessions.py` command construction, `~/.view/logs/codex-sessions/*.stderr.log`, and whether a `thread_id` was captured from raw JSON.
- Agent pane rendering looks incomplete: inspect `AgentSessionTranscript.vue` extraction/rendering and use `AgentViewer.vue` raw JSON toggle to compare against the provider's raw session log.
- Loop task does not run: check `~/.view/loops/*.md` frontmatter parse errors in the Loop Tasks page, `backend/app/agent_loops.py`, `~/.view/agent-loops.json`, and `~/.view/logs/agent-loops/{task_id}/`.
- Frontend runtime errors: browser console, `/api/debug/client-log`, `backend/app/logging.py`, `~/.view/logs/`.
- Production frontend missing: build `frontend/dist` or set `VIEWER_FRONTEND_DIST`.

## Maintenance Rules

- Keep this file synchronized with code when responsibilities move or files are added/removed.
- Keep backend schemas and frontend TypeScript interfaces aligned.
- Do not hand-edit generated dependency/build artifacts (`uv.lock`, `frontend/package-lock.json`, `frontend/dist/`, `frontend/node_modules/`).
- The app is read-only for served files except terminal/Codex/loop processes, which can modify files because they run real commands in the served root. Viewer-owned config/state/log files live under `~/.view`.
