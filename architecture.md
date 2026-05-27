# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how frontend, backend, terminals, file watching, and preview rendering fit together.

## Purpose

Local Live File Viewer is a private-network file browser and preview app. A FastAPI backend serves files from the active user's configured profile home, watches configured profile homes, exposes file, Git, terminal, and Codex APIs, and serves the built Vue frontend. The app has soft user profiles: each browser chooses a profile, API calls carry `user`, and workspace/sidebar/session lists are scoped to that profile while all processes still run as the same trusted OS user with access to the served root. The Vue app provides a sidebar file browser, Git diff panel, recursive split panes, file viewers, live refresh on filesystem changes, and browser-local layout persistence.

## Runtime Flow

1. `run.py` parses CLI flags, sets `VIEWER_*` environment variables including optional WhisperLiveKit voice settings, optionally builds the frontend, configures logging, and starts `uvicorn` on `app.main:app`.
2. `backend/app/main.py` creates the FastAPI app, installs CORS and request logging middleware, starts `watch_root()`, the agent-loop scheduler, and the Agent Task DAG monitor on startup, stops watcher, task monitor, loops, terminals, and agent sessions on shutdown, registers all REST/WebSocket/SSE routes, and mounts `frontend/dist` if it exists.
3. The frontend starts in `frontend/src/main.ts`, installs global client error logging, creates Pinia, and mounts `App.vue`.
4. `App.vue` loads available user profiles from `/api/users`. If browser storage has no active profile, it shows a profile picker before loading workspace state. Once selected, the active user id is stored in `localStorage`, API/SSE/WebSocket helpers append it as `user`, and browser-local layout/sidebar/heat/draft keys are namespaced by user. The app then loads file tree/config/terminal/workspace state, restores the active workspace layout and sidebar directory, applies visual config as CSS variables, connects to `/api/events`, refreshes affected file listings, and dispatches every filesystem SSE as a `viewer:file-changed` browser event so panes can decide whether indirect dependencies matter. The top bar switches between the workspace, full-page settings, full-page loop task editor, and full-page Agent Task DAG board.
5. `ViewerPane.vue` fetches file metadata and chooses the correct viewer component, including routing `.csv` text files to the CSV table viewer and oversized Markdown/CSV/text files to the virtualized large text viewer. Viewers fetch raw/text content and reload when their `version` prop changes; Markdown panes render embedded images with browser lazy loading, track simple relative local image dependencies client-side, use a pane-level version cache key for raw-file URLs, reload/cache-bust embedded images when those image files change, and can switch into a split textarea/live-preview edit mode that saves through `/api/file/content` PUT. PDF panes configure the PDF.js worker explicitly, load the document once from `/api/file/raw`, and render individual pages lazily as page placeholders approach the scroll viewport. HTML panes load documents through an iframe-backed static-site route so relative scripts, stylesheets, images, and links resolve like a browser-served folder; inactive HTML panes render a transparent activation shield above the iframe so a first click can select the pane before iframe content consumes pointer events.
6. Terminal panes use REST for lifecycle operations and WebSocket `/api/terminals/{id}/ws` for interactive PTY input/output.
7. Codex panes use REST for lifecycle/message operations and WebSocket `/api/codex/sessions/{id}/ws` for structured JSONL event updates rendered by the frontend rather than through terminal emulation. New Codex sessions may be created idle with no prompt; the first pane message starts the actual Codex CLI run. Codex runs are launched through a detached background runner whose pid/stdout/stderr/state files live under `/tmp/viewer_run/codex` by default, so restarting the viewer service does not stop active Codex work. Raw rollout events stay server-side; REST/WebSocket snapshots send compact display events, and the shared agent API defaults to `detail=focus` so focus-mode panes receive assistant messages without tool output/file-change payloads until the frontend requests `detail=full`.
8. Hermes panes mirror the Codex pane lifecycle shape through REST and WebSocket `/api/hermes/sessions/{id}/ws`, but the provider backend talks to the local Hermes API server at `VIEWER_HERMES_BASE_URL` / `http://127.0.0.1:8642` and reads canonical session history directly from `VIEWER_HERMES_STATE_DB` / `~/.hermes/state.db`. Viewer-local Hermes metadata lives under `~/.view/logs/hermes-sessions/`; the SQLite `sessions` and `messages` rows are compacted into the same frontend event shape used by Codex panes.
9. Loop tasks are Markdown files in `~/.view/loops/*.md` with generated YAML frontmatter plus a prompt body. `backend/app/agent_loops.py` runs an asyncio scheduler that creates/resumes Codex sessions through `codex_session_manager`, writes state to `~/.view/agent-loops.json`, and writes run logs under `~/.view/logs/agent-loops/`.
10. Agent Task DAG tasks live in `~/.view/agent-tasks.sqlite3` and are handled by `backend/app/agent_tasks.py`. The task monitor promotes dependency-satisfied tasks to `ready`, dispatches ready tasks when a group is in auto mode, tracks `waiting_process` task PIDs, and resumes the attached agent session when a recorded process exits. Logs and artifacts should be written under `~/.view/logs/agent-tasks/{task_id}/`.

## Backend Structure

`backend/app/main.py`

- FastAPI application and route table.
- `log_requests(request, call_next)`: logs failed HTTP requests and debug API requests.
- `startup()`: ensures the global root and each configured profile home exist, logs runtime config, starts filesystem watcher task.
- `shutdown()`: stops watcher task and terminates terminal sessions.
- `/api/health`: returns health, active root, and current backend PID.
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
- `/api/config` GET/PUT: reads and writes nav appearance, workspace count and workspace heat timing, Codex model options, Task DAG API base URL, Markdown theme config, user profiles, and default user in `~/.view/config.json`. Normal settings saves preserve existing user profiles unless the JSON payload explicitly changes them. Sidebar current directory, pinned paths, per-workspace open-order visit timestamps, and workspace-associated agent refs live in per-user workspace files under `~/.view/users/{user}/workspaces.json`.
- `/api/workspaces` GET, `/api/workspaces/config` GET/PUT, `/api/workspaces/{id}` PUT, `/api/workspaces/{id}/activate` POST, `/api/workspaces/{id}/agent-sessions` POST/DELETE, and `/api/workspaces/{id}/pinned-agent-sessions` POST/DELETE: read workspace state, read/update workspace count config, write workspace snapshots, activate workspaces, add/remove workspace-associated agent sessions, and pin/unpin those sessions for the `user` query profile. Workspace count is stored globally in `~/.view/config.json`; per-workspace state is stored in `~/.view/users/{user}/workspaces.json`. Agent session association and pinned session refs are maintained by the backend through explicit add/remove routes; workspace snapshot writes preserve those sets instead of accepting full replacements from the browser.
- `/api/agent-loops` routes: list/create/update/delete loop task Markdown definitions, reload files, pause/resume, run now, reset the retained Codex session, and read run history/details.
- `/api/agent-tasks` routes: persistent Agent Task DAG CRUD and scheduling APIs backed by `~/.view/agent-tasks.sqlite3`. Routes list tasks by `group_id`/status, list group summaries, create/delete tasks, clear or retry a task plus all downstream dependents, read task context, list task artifact/workspace files with served-root preview paths, patch mutable not-yet-running tasks, patch dependencies with cycle checks, record long-running process PIDs, complete tasks with artifacts/results, manually dispatch a task to Codex/Hermes, dispatch ready tasks, and read/update per-group manual/auto scheduler settings.
- `/api/events`: streams Server-Sent Events from `hub.subscribe()`.
- `/api/terminals`: lists or creates terminal sessions for the `user` query profile; POST accepts an optional relative `cwd`. When `cwd` is omitted, the terminal starts in the profile home directory.
- `/api/terminals/{terminal_id}` routes: snapshot, terminate, delete, and WebSocket connect.
- `/api/agents/providers`: returns registered agent providers with frontend display metadata such as name and Bootstrap icon.
- `/api/agents/sessions`: shared agent session API. GET accepts optional `provider` plus `user` and returns only that profile's sessions; POST accepts a JSON `provider` plus prompt/cwd/model and creates a session owned by the `user` query profile. When `cwd` is omitted, Codex/Hermes start in the profile home directory.
- `/api/agents/sessions/{session_id}` routes: shared snapshot, send, queue append/update/delete, terminate, and WebSocket connect. Non-WebSocket mutating requests carry `provider` in the JSON body; GET/DELETE/WebSocket use a `provider` query parameter because they have no request JSON body. Snapshot and WebSocket routes accept `detail=focus|full`; focus is the default and filters event payloads down to assistant messages, while full includes tool calls/results, file changes, patch text, and raw previews.
- `/api/agents/sessions/{session_id}/approvals/{approval_id}` resolves a provider-normalized pending approval with choices such as `once`, `session`, `always`, or `deny`. Agent snapshots expose `pending_approvals` so the shared frontend transcript can render approve/deny controls without provider-specific UI.
- `/api/codex/sessions`: lists or creates Codex sessions. POST records metadata and, when a prompt is supplied, starts a detached background `codex exec --json` run in a served-root-relative `cwd`.
- `/api/codex/status`: returns the latest global Codex CLI rate-limit status parsed from recent `~/.codex/sessions/**/rollout-*.jsonl` `token_count` events by timestamp; pane-level context usage comes from each session's matched rollout file.
- `/api/codex/models`: returns selected and available models for Codex session creation from `~/.view/config.json` codex defaults only.
- `/api/codex/sessions/{session_id}` routes: snapshot, send a resumed message via a detached `codex exec resume --json` run, append/update/delete server-persisted queued messages, terminate a running Codex background process group, and WebSocket connect. There is intentionally no API for deleting Codex sessions; workspaces only hide/unassociate sessions from the current workspace.
- `/api/hermes/sessions`: compatibility wrapper for listing or creating Hermes sessions. New frontend calls use `/api/agents/sessions` with `provider=hermes`.
- `/api/hermes/sessions/{session_id}` routes: compatibility wrappers for snapshot from Hermes SQLite history plus viewer-local queue metadata, send a message through `/v1/runs`, append/update/delete server-persisted queued messages, stop the active Hermes run through `/v1/runs/{run_id}/stop`, and WebSocket connect.
- `/api/voice/ws`: optional voice-input WebSocket endpoint. By default the browser streams encoded audio chunks while recording, the backend saves them, and a single full-file `faster-whisper` transcription runs after the client sends `stop`. A configured upstream ASR WebSocket still bypasses the in-process path.
- Mounts built frontend static files from `settings.frontend_dist_resolved`.

`backend/app/config.py`

- Central Pydantic settings object using `VIEWER_` env prefix.
- Defines `PROJECT_ROOT`, `DEFAULT_FRONTEND_DIST`.
- `Settings.root_resolved`: expanded absolute served directory.
- `Settings.frontend_dist_resolved`: expanded absolute frontend build directory.
- Important settings: `root`, `host`, `port`, `frontend_dist`, `max_text_preview_bytes`, `show_hidden`, `poll_delay_ms`, `terminal_shell`, `debug`, `log_file`.
- Voice settings: `voice_enabled`, `voice_model`, `voice_language`, `voice_target_language`, `voice_backend`, `voice_backend_policy`, `voice_direct_english_translation`, `voice_min_chunk_size`, `voice_stop_timeout_seconds`, `voice_model_idle_timeout_seconds`, `voice_offline_beam_size`, `voice_offline_vad_filter`, `voice_vac`, and `voice_vad` configure voice input. `run.py` enables voice by default, uses `large-v3-turbo`, sets the default source language to `en`, and sets the default backend to `faster-whisper` with `localagreement` unless env vars override them. `voice_upstream_ws` bypasses the in-process offline path and proxies microphone audio to a separate streaming ASR WebSocket.

`backend/app/storage.py`

- Central viewer-local storage paths under `~/.view`.
- Defines `USERS_DIR` for per-profile state under `~/.view/users/{user}/`.
- Defines `CONFIG_PATH`, legacy `WORKSPACES_PATH`, `LOOPS_DIR`, `LOOP_STATE_PATH`, `LOG_DIR`, `CODEX_LOG_DIR`, `HERMES_LOG_DIR`, `TERMINAL_LOG_DIR`, `AGENT_LOOP_LOG_DIR`, `CODEX_RUN_DIR`, and `HERMES_RUN_DIR`.
- `CODEX_RUN_DIR` defaults to `/tmp/viewer_run/codex` and can be overridden with `VIEWER_CODEX_RUN_DIR`; it stores detached Codex runner state outside the viewer service process.
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
- `read_config()` / `write_config(config)`: load and save nav appearance, workspace count/heat timing, Codex model options, Task DAG API base URL, Markdown theme config, user profiles, and default user. Legacy workspace state keys (`pinned`, `current_path`, `visit_times`, and `workspaces`) are pruned from `~/.view/config.json` on read/write, with legacy `workspaces.count` migrated to `workspace.count`.
- `read_workspaces(user_id)` / `write_workspace(..., user_id)` / `add_workspace_agent_session(..., user_id)` / `remove_workspace_agent_session(..., user_id)` / `add_workspace_pinned_agent_session(..., user_id)` / `remove_workspace_pinned_agent_session(..., user_id)` / `set_active_workspace(..., user_id)` / `write_workspace_config(..., user_id)`: load and save `~/.view/users/{user}/workspaces.json` state plus `~/.view/config.json` workspace count. `workspaces.json` stores active workspace id and per-slot state only; each slot stores the frontend layout JSON, active pane id, current sidebar directory, pinned file paths, workspace-associated agent refs (`provider:session_id` in `agent_session_ids`), pinned agent refs (`provider:session_id` in `pinned_agent_session_ids`), per-workspace visit timestamps for open ordering, and update timestamp. Older workspace slots are migrated from layout and legacy Codex/Hermes id fields into `agent_session_ids`, but new writes omit the old provider-specific fields. Frontend layout snapshot saves do not replace agent session refs or pinned agent refs; the backend owns those sets through explicit add/remove operations so stale browsers cannot overwrite newer session associations.

`backend/app/agent_loops.py`

- Markdown-backed Codex loop task scheduler.
- Parses generated YAML frontmatter from `~/.view/loops/*.md`; the Markdown body is the Codex prompt.
- Supports schedule types `manual`, `once`, `interval`, `daily`, and `multi_daily`; local task times use `Asia/Shanghai` / UTC+8 by default.
- Supports Codex session policies `new_each_run`, `reuse`, `reuse_until_context`, and `reuse_with_limits`.
- Persists runtime state in `~/.view/agent-loops.json`, including current session id, current run id, run counts, consecutive failures, next run time, stopped/paused state, and last error.
- Writes per-task run history and detailed run snapshots under `~/.view/logs/agent-loops/{task_id}/`.
- Reuses `codex_session_manager` for actual Codex creation/resume and uses compact Codex snapshots for log display.

`backend/app/agent_tasks.py`

- SQLite-backed Agent Task DAG runtime stored at `~/.view/agent-tasks.sqlite3`.
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
- `send(session_id, prompt)`: starts a detached background runner for a new `codex exec --json` run when no Codex thread id has been captured yet, otherwise resumes with `codex exec resume --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox <thread_id> -`. It rejects concurrent sends while a session has an active background run and appends the prompt to metadata.
- Codex snapshots include the shared `pending_approvals` field, currently empty because Codex runs are still launched with approvals bypassed. The manager exposes the shared approval-resolution method so a future non-YOLO Codex runner can plug into the same frontend controls.
- Detached run state: each run writes `state.json`, `stdout.jsonl`, `stderr.log`, and `prompt.txt` under `CODEX_RUN_DIR/{viewer_session_id}/{run_id}/`. The viewer metadata stores those paths plus the runner pid and Codex child pid. On startup, running sessions are reattached by reading this state and checking whether the pid is still alive.
- `enqueue()`, `update_queue_item()`, and `delete_queue_item()`: manage pending prompts in session metadata. Appending to the queue starts the drain loop immediately when the session is not running.
- `_start_next_queued(session)`: pops the next queued prompt, appends it to normal prompts, broadcasts a snapshot, and starts a Codex run. `_run()` calls it after each completed subprocess unless the current run was terminated by the user. `resume_pending_queues()` runs on backend startup so persisted queued work resumes without a browser connection.
- Codex subprocess proxying is controlled by `~/.view/config.json` `codex.proxy`; when non-empty it sets `https_proxy`, `HTTPS_PROXY`, `http_proxy`, and `HTTP_PROXY` for Codex runs, and when empty those variables are removed from the Codex subprocess environment. The default is no proxy.
- Rendered Codex events are read from the canonical `~/.codex/sessions/**/rollout-*.jsonl` file matched by Codex session id. Viewer-local metadata/prompts and the matched `rollout_path` are stored in `~/.view/logs/codex-sessions/{viewer_session_id}.json`; stderr goes to `{viewer_session_id}.stderr.log`.
- API snapshots and WebSocket event messages compact raw rollout entries into a transport/display shape before sending them to the frontend. Full raw events remain in memory/on disk for status parsing; compact events carry `event_type`, rendered `text`, `file_changes`, `patch_text`, and a bounded `raw_preview`.
- Per-session Codex summaries parse `last_token_usage.total_tokens` from that session's matched rollout `token_count` events to expose `context_used_percent`, `model_context_window`, and `total_tokens`; navbar Codex chips use these session fields instead of the newest global rollout.
- `_find_session_id(raw)`: extracts `session_id`, `conversation_id`, or `thread_id` from JSON events so later messages can resume the correct Codex thread. For detached runs, the manager scans the persisted runner stdout JSONL file to discover this id after service restarts.
- Active Codex runs are monitored by polling detached runner state and matched rollout JSONL files, then broadcasting newly parsed events. The service can recreate this monitor after restart because stdout, state, and canonical rollout files are persisted outside process memory.
- Codex session status is updated from rollout turn-finish events and detached runner exit state: `task_complete` / `turn.completed` mark the viewer session `exited`, `turn_aborted` / `turn.failed` mark it `failed`, and runner `state.json` records the final process exit code when available.
- Codex live-refresh debugging uses Loguru in `codex_sessions.py`: background runner start/detach/finish, rollout matching, and turn-finish status changes log at info level; rollout unavailability, synced event counts, per-event broadcasts, and stale WebSocket clients log at debug level.
- `connect(session_id, websocket)`: sends snapshots and broadcasts live event/status/deleted messages to Codex panes.
- `terminate(session_id)`: stops the active detached runner process group for a session and broadcasts updated status.
- `codex_session_manager`: singleton used by routes.

`backend/app/codex_background_runner.py`

- Small process wrapper used by `codex_sessions.py` to keep Codex runs alive when the viewer service restarts.
- Writes atomic `state.json` updates with runner pid, Codex child pid, status, exit code, timestamps, command, and cwd.
- Redirects Codex stdout/stderr to run-local files so the service can rediscover Codex session ids and continue reading canonical rollout files after restart.

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
- `VoiceCapture`: saves the browser-sent audio chunks for each session under `~/.view/logs/voice/`, using a UTC finish-time filename plus a JSON sidecar with MIME type, size, chunk count, backend, and backend policy.
- Lazily loads a singleton offline `faster_whisper.WhisperModel` for the first in-process final transcription, keeps it warm across nearby dictations, and unloads it after `voice_model_idle_timeout_seconds` of inactivity to release GPU resources.
- `connect_voice(websocket)`: accepts browser audio chunks from `/api/voice/ws`, saves them during recording, runs full-file transcription after `stop`, and returns `processing` then `final` transcript JSON to the frontend. When `voice_upstream_ws` is configured, it proxies audio to the upstream ASR WebSocket and normalizes upstream messages.
- `_connect_whisperlivekit(websocket)`: creates one WhisperLiveKit `AudioProcessor` per browser voice session, forwards binary audio frames into it, saves the transmitted audio, and streams normalized result-state updates back to the client. On client stop, it flushes the processor and waits up to `voice_stop_timeout_seconds` for final model output before closing.
- `_connect_offline_voice(websocket)` / `_transcribe_offline(audio_path)`: save the streamed WebM/MP4 chunks, then transcribe the completed file with `faster-whisper` using `voice_offline_beam_size`, `voice_offline_vad_filter`, and the configured voice language.
- `_normalize_upstream_message(message)` / `_normalize_payload(payload)`: accept common streaming ASR response shapes plus WhisperLiveKit `lines` / `buffer_transcription` / `buffer_translation` state and normalize final/partial text.
- `_whisper_kwargs()`: normalizes WhisperLiveKit options and disables `voice_target_language` with a warning when `voice_language=auto` and `voice_backend_policy` is not `simulstreaming`, because WhisperLiveKit rejects that translation configuration.

`backend/app/models.py`

- Pydantic API schemas: `FileEntry`, `DirectoryListing`, `FileMeta`, `ConfigData`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `WatchEvent`, `TerminalInfo`, `TerminalCreate`, `TerminalSnapshot`, `ClientLog`.
- `ConfigData` stores global appearance, workspace count/heat timing, Markdown, Codex defaults, and Task DAG defaults.
- `WorkspaceConfig.count` defaults to 5 and controls how many numbered workspace buttons appear in the sidebar activity rail. `heat_interval_seconds` and `heat_step_percent` control frontend workspace button heat: on each interval the active workspace score moves toward 1 by the configured percentage and inactive scores move toward 0 by the same percentage. `WorkspaceData.count` mirrors the count config in `/api/workspaces` responses for frontend convenience, while persisted workspace slots in `~/.view/workspaces.json` include per-workspace visit timestamps used to sort the current folder by most recent visit.
- Agent schemas: shared `AgentPrompt`, compact `AgentEvent`, `AgentFileChange`, and `AgentQueueItem` describe the normalized intermediate representation used across providers. `AgentEvent.event_type` is typed by the backend `AgentEventType` enum and is a fixed backend IR, not a provider-native event name. Current fixed event types are `message:assistant`, `reasoning`, `tool_call`, `tool_result`, `custom_tool_call`, `exec_command_begin`, `exec_command_end`, `function_call`, `function_call_output`, `custom_tool_call_output`, `view_image_tool_call`, `patch_apply_end`, and fallback `operation` for logged-but-unclassified visible events. Agent drivers are responsible for converting provider-native logs into `AgentEventType` values before snapshots or WebSocket events reach the frontend. Frontend transcript code should only depend on this fixed IR; adding a new agent provider should require backend driver conversion work, not new frontend event-name handling, unless a genuinely new shared IR event type is introduced. Codex schemas add `CodexSessionInfo`, `CodexSessionSnapshot`, `CodexSessionCreate`, `CodexSessionMessage`, and `CodexQueueMessage`; `CodexSessionInfo.cwd_relative` mirrors the absolute `cwd` relative to the served root when possible, and running sessions expose background `pid`, `codex_pid`, `run_id`, and `run_started_at` fields when available. Hermes schemas reuse the shared agent prompt/event/queue shape, with Hermes-specific ids and DB path in `HermesSessionInfo`.
- `CodexConfig` stores Codex model options, muted operation-message alpha, the Diff viewer auto-commit prompt, and an optional `proxy` for `~/.view/config.json`; `default_model` controls new/resumed Codex runs unless the user manually selects a different model in the pane toolbar. The built-in default model is `gpt-5.5`, `muted_message_alpha` defaults to `0.56`, and `proxy` defaults to empty/no proxy.
- `DagConfig.base_url` stores the Viewer API origin injected into Agent Task manager/executor prompts; when empty, the backend falls back to `http://127.0.0.1:{VIEWER_PORT}` during prompt generation.
- These should stay aligned with TypeScript interfaces under `frontend/src/types/`.

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
- `configure_logging(log_file, debug)`: configures stderr and rotating file logs, intercepts uvicorn/FastAPI logs.
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

- Root shell: top bar, sidebar drawer/pinned layout, workspace, full-page settings, and full-page loop tasks.
- Loads config/terminal lists, restores the saved sidebar directory, loads that directory, restores layout, and exposes top-bar buttons for Settings and Loop Tasks.
- Applies appearance and active Markdown theme settings from `stores/files.ts` as CSS variables on the app shell, including nav height/icon size and Markdown/syntax colors.
- Connects SSE with `connectEvents()`.
- Refreshes affected file listings on change events.
- Dispatches every filesystem SSE as `viewer:file-changed`; individual panes filter these events against their own file path or viewer-specific dependency set.
- Polls terminal list every 3 seconds.
- Renders active pane toolbar metadata, actions, and generic controls from `stores/paneToolbar.ts` in the top bar while the workspace page is active, plus global pane split actions.
- On phone-width screens, active-pane toolbar actions and controls collapse behind a top-bar overflow menu while global pane actions remain inline.
- Top-bar ownership rule: cross-viewer pane actions such as split belong in `App.vue`; view-specific icons/controls/status belong in the owning viewer and must be registered through `stores/paneToolbar.ts`, not hard-coded in `App.vue`. The global SSE connection dot is intentionally not rendered.
- The global pane toolbar in `App.vue` owns pane-level navigation actions such as Go Back, Split, and Close. `stores/layout.ts` keeps each pane's local content history so a pane can return from a linked file, sidebar-opened file, diff, terminal, or agent session to its previous content.
- Sidebar state functions: `toggleSidebarPin()`, `clampSidebarWidth()`, `startSidebarResize()`.
- Workspace actions: `openFile()`, `openTerminal()`, `splitActivePane()`, `closeActivePane()`.
- User selection: `useUsersStore` loads `/api/users`, reads/writes `viewer.activeUser.v1`, and blocks normal app startup behind the first-run profile picker when no valid active profile is stored. The Settings page includes a profile selector that stores the new user and reloads the app so all stores reconnect under the new profile.
- Workspace switching: `restoreInitialWorkspace()` loads the active `~/.view/users/{user}/workspaces.json` slot at startup; `switchWorkspace()` immediately marks the target workspace active in the UI, restores its saved pane layout, shows pane-level loading placeholders for one animation frame, then lets each viewer mount and fetch its content while the previous slot save, sidebar directory load, and backend workspace activation complete. The sidebar tool stays unchanged. A debounced watcher autosaves the active workspace after layout, active pane, pinned file paths, sidebar directory, visit timestamps/open ordering, or workspace-associated agent refs change.
- File viewer scroll memory lives in `utils/scrollMemory.ts` and `composables/useScrollMemory.ts`. Scroll positions are browser-local, user-namespaced, and keyed by workspace id, pane id, and file path so two panes can keep different positions for the same file; layout navigation emits `viewer:pane-before-navigate` before replacing pane content and workspace switching emits `viewer:workspace-before-switch` before the displayed workspace id changes so the current viewer can save its last scroll position under the correct key.
- Agent provider/session list is loaded on startup and polled every 3 seconds like terminals through `stores/agents.ts`. Workspaces keep one `agent_session_ids` list using `provider:session_id` refs; new or opened sessions are remembered in the active workspace so the Agents sidebar is workspace-scoped rather than directory-scoped. Pinned agent sessions are stored per workspace in `pinned_agent_session_ids` and sorted above unpinned sessions in the Agents sidebar. Removing a row from the sidebar only removes that workspace entry and matching panes; it does not delete viewer session metadata or canonical provider history.
- `LocalFilePreview.vue` is a floating file preview dialog used for transient local-file inspection from Codex transcript links. It resolves to normal viewer components and offers only three navigation actions: open in the active pane, open in a vertical split, or open in a horizontal split.

`frontend/src/components/viewers/CsvViewer.vue`

- Dedicated CSV viewer for `.csv` files that otherwise arrive as text previews from the backend.
- Fetches text content with `/api/file/content`, parses RFC-style quoted fields including embedded commas/newlines and escaped quotes, and renders the first row as sticky table headers.
- Registers Table/Raw actions through `stores/paneToolbar.ts`; Raw mode displays plain CSV text without syntax highlighting.

`frontend/src/components/viewers/LargeTextViewer.vue`

- Virtualized read-only preview for oversized Markdown, CSV, and plain text files.
- Fetches bounded line windows from `/api/file/text-lines`, uses a fixed line height to map scroll position to file line numbers, and renders only the loaded window plus line-number gutter inside a full-height scroll spacer.
- Registers reload, top, end, and copy-current-window actions through `stores/paneToolbar.ts`. Search is intentionally not implemented.
- Saves and restores browser-local scroll position using the same workspace/pane/path scroll-memory keys as normal file viewers.

- Workspace button agent status is tracked by `App.vue`: workspace buttons show small status dots when any contained agent session is running or when inactive workspaces have sticky unread completion/failure notices. Button background is reserved for local workspace heat, with scores persisted in browser localStorage and displayed through a logarithmic red intensity curve.

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
- Tools: Files, Changes, Terminals, and Agents. Future side tools should be added to this shell instead of mixing unrelated lists into one panel.
- The activity rail stays visible even when the tool panel is closed. Clicking a different tool or workspace changes only the active selection; clicking the already-active tool or workspace toggles the tool panel open/closed.
- On phone-width screens, pinned and unpinned tool panels behave as an overlay beside the always-visible activity rail so the workspace is not narrowed by the saved desktop sidebar width.
- Renders one-click numbered workspace buttons in the activity rail. Clicking a different workspace saves the current workspace and restores the selected workspace without changing the tool panel open/closed state.
- Workspace buttons receive agent notices and local heat scores from `App.vue`; status is rendered as a small green/amber/red dot for running or unread completion/failure, while the button background uses logarithmic red heat intensity based on recent active workspace time. During a workspace switch, the target button becomes active immediately and displays a small spinner while backend persistence/activation finishes.
- Re-emits `open-file`, `open-diff`, `open-terminal`, and `open-agent-session` events to `App.vue`.

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
- Binary files are displayed with a `bin` chip but disabled so they cannot be opened in the diff viewer.
- Clicking a text change emits `open-diff` with the current Files directory so the active pane becomes a diff pane scoped to that directory.

`frontend/src/components/sidebar/AgentSessionsPanel.vue`

- Unified Agents tool panel. Fetches provider metadata from the backend-backed `stores/agents.ts`, shows a provider dropdown for new sessions, creates the selected agent in the current sidebar directory, filters the global agent session list to refs remembered by the active workspace, and opens rows through `layout.openAgentSession(ref)`. Rows render provider icons, active row state, voice pending/ready row state, running status dots, unread completion/failure indicators, error display, and remove-from-workspace action from one shared appearance layer.

`frontend/src/components/ConfigPanel.vue`

- Full-page configuration UI opened from the top bar Settings button.
- Edits `~/.view/config.json` through the existing `/api/config` endpoint.
- Sections: Server, Appearance, Codex Models, Markdown, Syntax Highlighting, and raw JSON.
- Server section has confirmed restart and stop buttons. Restart calls `/api/admin/restart`, polls `/api/health` until the PID changes, then reloads the page. Stop calls `/api/admin/stop` and leaves a command-line restart hint.
- Appearance currently controls nav bar size, which also drives icon/button size via CSS variables.
- Codex Models controls the default Codex model, the available model list used by `/api/codex/models` and the Codex pane toolbar, and the optional Codex subprocess proxy.
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
- The terminal text input pad uses `VoiceInputButton.vue` for microphone transcription into the pad and leaves command sending under explicit user control.
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

- Shared Codex/Hermes prompt composer with the Codex input styling and behavior: server-backed queue list, queued-message edit/save/cancel/delete controls, microphone transcription through `VoiceInputButton.vue`, queue, clear, and stop actions. Parent viewers own persistence and API calls through emitted events.

`frontend/src/components/AgentSessionTranscript.vue`

- Shared Codex/Hermes transcript display layer. Owns agent content appearance and behavior: session metadata, idle/running/error rows, prompt/event timeline ordering, Markdown rendering, Mermaid rendering, local link click emission, focus-mode muted operation filtering through an exact `AgentEvent.event_type` display map, raw JSON preview, file-change/patch diffs, word-level diff highlighting, derived result snippets, and scroll-to-bottom helpers exposed to parent viewers. Because drivers normalize provider-native events into the fixed backend `AgentEvent` IR, this component should not contain provider-specific event-name parsing. Unknown frontend event types are posted to `/api/debug/client-log` with bounded previews because they indicate the backend emitted an unregistered shared IR type.

`frontend/src/stores/voice.ts`

- Browser voice job store keyed by input context ids such as `codex:{session_id}:prompt` and `terminal:{terminal_id}:paste`.
- Owns `MediaRecorder`, microphone stream, voice WebSocket lifecycle, chunk sending, `ready` / `processing` / `final` / `error` handling, and context text/status state.
- Voice jobs survive component unmounts after recording has stopped, so users can switch sessions while final transcription is processing and return to the same pending/ready draft.
- Context statuses drive sidebar indicators: processing/recording contexts show as pending, completed but unsent voice text shows as ready.

`frontend/src/components/VoiceInputButton.vue`

- Reusable microphone transcription button backed by `/api/voice/ws`.
- Requires a stable `contextId`, binds the voice store's context text to its `v-model`, and toggles start/stop for that context. Consumers still decide when to send or queue the text.

## Frontend Stores And API

`frontend/src/api/client.ts`

- Shared fetch helpers for REST and raw/WS URLs.
- `request<T>()`: JSON request with error text on non-2xx.
- File APIs: `rawUrl(path, contentHash?)`, `getTree()`, `getMeta()`, `getText()`, `getConfig()`, `putConfig()`.
- Git APIs: `getGitStatus()`, `getGitDiff()`, `stageGitPath()`, `revertGitPath()`, `commitGit()`, and `pushGit()`.
- Admin APIs: `restartServer()` and `stopServer()`.
- Terminal APIs: `listTerminals()`, `createTerminal(cwd)`, `getTerminal()`, `terminateTerminal()`, `deleteTerminal()`, `terminalSocketUrl()`.
- Codex APIs: `listCodexSessions()`, `createCodexSession(prompt, cwd)`, `getCodexSession()`, `sendCodexMessage()`, `queueCodexMessage()`, `updateCodexQueuedMessage()`, `deleteCodexQueuedMessage()`, `codexSessionSocketUrl()`.
- `frontend/src/api/client.ts` appends the active user id from `viewer.activeUser.v1` to REST and WebSocket API URLs; `frontend/src/api/events.ts` does the same for the SSE stream.
- Codex and Hermes session helpers call the shared `/api/agents/sessions` endpoints and pass `provider` in the request body or query string; old provider-specific backend routes remain for compatibility.
- Hermes APIs: `listHermesSessions()`, `createHermesSession(prompt, cwd)`, `getHermesSession()`, `sendHermesMessage()`, `queueHermesMessage()`, `updateHermesQueuedMessage()`, `deleteHermesQueuedMessage()`, `terminateHermesSession()`, and `hermesSessionSocketUrl()`.
- Agent loop APIs: `listAgentLoops()`, `createAgentLoop()`, `reloadAgentLoops()`, `updateAgentLoop()`, `deleteAgentLoop()`, `runAgentLoop()`, `pauseAgentLoop()`, `resumeAgentLoop()`, `resetAgentLoopSession()`, `listAgentLoopRuns()`, and `getAgentLoopRun()`.
- Voice API helper: `voiceSocketUrl()` builds the browser WebSocket URL for `/api/voice/ws`, using `wss://` when the page is served over HTTPS.

`frontend/src/api/events.ts`

- `connectEvents(onChange, onState)`: creates `EventSource` for `/api/events`, reports connection state, parses `file-change` events.

`frontend/src/stores/files.ts`

- Pinia store for directory listings, current path, expanded dirs, pinned paths, per-workspace visit timestamps, appearance config, workspace count/heat config, Markdown theme config, and loading state. Appearance, workspace count/heat timing, Markdown themes, Codex config, and profile definitions are persisted in `~/.view/config.json`; current path, pins, and visit timestamps/open ordering are persisted per workspace slot in `~/.view/users/{user}/workspaces.json`.
- Getters: `rootEntries`, `currentEntries`, `parentPath`, `activeMarkdownTheme`.
- Actions: `loadConfig()`, `saveConfig()`, `saveAppearance()`, `saveMarkdown()`, `saveViewerConfig()`, `saveFullViewerConfig()`, `loadDirectory()`, `enterDirectory()`, `enterParentDirectory()`, `toggleDirectory()`, `refreshAffected()`, `togglePin()`. `enterDirectory()` persists the last visited sidebar directory.

`frontend/src/stores/layout.ts`

- Pinia store for recursive split layout and active pane.
- Helpers: `id()`, `defaultLayout()`, `findPane()`, `mapNode()`, `firstPaneId()`, `mapAllPanes()`, `removePane()`.
- Getters: `activePane`, `openPaths`, `openTerminalIds`, `openCodexSessionIds`, `openHermesSessionIds`, `openDiffPaths`.
- Actions: `load()`, `save()`, `snapshot()`, `restore()`, `reset()`, `setActive()`, `openFile()`, `openTerminal()`, `openCodexSession()`, `openHermesSession()`, `openDiff()`, `splitPane()`, `setRatio()`, `clearPane()`, `closePane()`, `clearTerminal()`, `clearCodexSession()`, `clearHermesSession()`.
- `openFileInSplit(path, direction)` creates a new split beside the active pane, opens a file in the new pane, and makes that pane active. It is used by floating local-file previews.
- Persists to `localStorage` key `viewer.layout.v1`.

`frontend/src/stores/workspaces.ts`

- Pinia store for the active user's `~/.view/users/{user}/workspaces.json` state plus the config-derived workspace count returned by `/api/workspaces`.
- Actions: `load()`, `snapshotFor()`, `saveSlot()`, and `activate()` call `/api/workspaces` endpoints and track the active numbered workspace.
- Tracks active workspace agent refs through `activeAgentSessionRefs` and pinned agent refs through `activePinnedAgentSessionRefs`.

`frontend/src/stores/codex.ts`

- Pinia store for Codex session summaries.
- Actions: `load()`, `create(prompt, cwd)`, `send(id, prompt)`, `upsert(session)`, `remove(id)`.
- Queue actions: `queue(id, prompt)`, `updateQueued(id, itemId, prompt)`, and `deleteQueued(id, itemId)` call the server-backed Codex queue APIs and upsert the returned session summary.

`frontend/src/stores/hermes.ts`

- Pinia store for Hermes session summaries.
- Actions: `load()`, `create(prompt, cwd)`, `send(id, prompt)`, `queue(id, prompt)`, `updateQueued(id, itemId, prompt)`, `deleteQueued(id, itemId)`, `terminate(id)`, `upsert(session)`, `markUnread(id)`, and `markRead(id)`.

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

- Recursive `LayoutNode` union and `SplitDirection`; pane nodes may hold `filePath`, `terminalId`, `codexSessionId`, `hermesSessionId`, `diffPath`, or `diffCwd`.

`frontend/src/types/terminals.ts`

- TypeScript mirror of terminal schemas: `TerminalStatus`, `TerminalInfo`, `TerminalSnapshot`.

`frontend/src/types/codex.ts`

- TypeScript mirror of Codex schemas: `CodexStatus`, `CodexSessionInfo`, `CodexSessionSnapshot`, and Codex-specific status/model fields. Shared prompt/event/file-change/queue item shapes live in `types/agents.ts` as `AgentPrompt`, `AgentEvent`, `AgentFileChange`, and `AgentQueueItem`; `types/codex.ts` keeps Codex aliases for compatibility.

`frontend/src/types/agents.ts`

- Shared agent provider/session types plus normalized `AgentPrompt`, `AgentEvent`, `AgentFileChange`, and `AgentQueueItem` shapes used by Codex, Hermes, and the shared agent transcript UI.

`frontend/src/types/hermes.ts`

- TypeScript mirror of Hermes schemas: `HermesStatus`, `HermesSessionInfo`, and `HermesSessionSnapshot`; it reuses the shared agent prompt/event/queue item shapes for normalized transcript display.

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
- Default start command is `uv run run.py --build-frontend --debug -p 18888`, matching the local server command used for this project.
- `stop` sends `SIGTERM`, waits for exit, escalates to `SIGKILL` after a timeout, and clears the pid/state files.
- `restart` stops the active or explicitly supplied PID, then starts the default or override command in a detached session.

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

- `~/.view/config.json`: appearance, workspace count, Codex model options, Markdown themes, user profiles, and default user, managed by `/api/config`, `/api/users`, and `/api/workspaces/config`. On first use, missing config is copied from served-root `.viewer.config.json` if present; legacy workspace keys (`pinned`, `current_path`, `visit_times`, and `workspaces`) are removed on subsequent reads/writes after `workspaces.count` is migrated to `workspace.count`.
- `~/.view/users/{user}/workspaces.json`: active workspace id plus per-slot split tree, active pane, open file paths, open terminal IDs, sidebar directory, pinned files/folders, workspace-associated agent refs, legacy Codex/Hermes session id compatibility lists, per-workspace visit timestamps/open ordering, and update timestamp, managed by `/api/workspaces?user=...`. Legacy `count` is migrated into `~/.view/config.json` and pruned from the workspace state file on write.
- `~/.view/loops/*.md`: Markdown loop task definitions with generated YAML frontmatter and prompt body.
- `~/.view/agent-loops.json`: scheduler runtime state for loop tasks.
- `localStorage viewer.activeUser.v1`: selected soft user profile id.
- `localStorage viewer.layout.v1.{user}`: legacy/fallback split tree, active pane, open file paths, open terminal IDs.
- `localStorage viewer.sidebarPinned.v1.{user}`: sidebar pinned state.
- `localStorage viewer.sidebarWidth.v1.{user}`: sidebar width.
- `localStorage viewer.sidebarActiveTool.v1.{user}` and `viewer.workspaceHeat.v1.{user}`: sidebar tab and workspace heat state.
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
