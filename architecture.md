# Architecture

This document is the working project map for future agents. Read it before changing code so you can find the right file quickly and understand how frontend, backend, terminals, file watching, and preview rendering fit together.

## Purpose

Local Live File Viewer is a private-network, read-only file browser and preview app. A FastAPI backend serves files from `VIEWER_ROOT`, watches that directory, exposes file and terminal APIs, and serves the built Vue frontend. The Vue app provides a sidebar file browser, recursive split panes, file viewers, live refresh on filesystem changes, and browser-local layout persistence.

## Runtime Flow

1. `run.py` parses CLI flags, sets `VIEWER_*` environment variables including optional WhisperLiveKit voice settings, optionally builds the frontend, configures logging, and starts `uvicorn` on `app.main:app`.
2. `backend/app/main.py` creates the FastAPI app, installs CORS and request logging middleware, starts `watch_root()` on startup, stops watcher and terminals on shutdown, registers all REST/WebSocket/SSE routes, and mounts `frontend/dist` if it exists.
3. The frontend starts in `frontend/src/main.ts`, installs global client error logging, creates Pinia, and mounts `App.vue`.
4. `App.vue` loads file tree/config/terminal state, restores layout/sidebar state from `localStorage`, applies visual config as CSS variables, connects to `/api/events`, and dispatches `viewer:file-changed` browser events for open panes.
5. `ViewerPane.vue` fetches file metadata and chooses the correct viewer component. Viewers fetch raw/text content and reload when their `version` prop changes.
6. Terminal panes use REST for lifecycle operations and WebSocket `/api/terminals/{id}/ws` for interactive PTY input/output.

## Backend Structure

`backend/app/main.py`

- FastAPI application and route table.
- `log_requests(request, call_next)`: logs failed HTTP requests and debug API requests.
- `startup()`: ensures root exists, logs runtime config, starts filesystem watcher task.
- `shutdown()`: stops watcher task and terminates terminal sessions.
- `/api/health`: returns health and active root.
- `/api/debug/info`: returns debug/root/frontend/log file details.
- `/api/debug/log`: returns current log file content.
- `/api/debug/client-log`: receives frontend errors and writes them through Loguru.
- `/api/tree`: calls `list_directory()`.
- `/api/file/meta`: calls `get_meta()`.
- `/api/file/content`: calls `read_text()`.
- `/api/file/raw`: streams a file via `FileResponse` and emits `ETag` plus strong immutable browser cache headers.
- `/api/config` GET/PUT: reads and writes pinned paths, last sidebar directory, nav appearance, and Markdown theme config.
- `/api/events`: streams Server-Sent Events from `hub.subscribe()`.
- `/api/terminals`: lists or creates terminal sessions; POST accepts an optional relative `cwd`.
- `/api/terminals/{terminal_id}` routes: snapshot, terminate, delete, and WebSocket connect.
- `/api/voice/ws`: optional voice-input WebSocket endpoint backed by in-process WhisperLiveKit by default, or by a configured upstream ASR WebSocket when `VIEWER_VOICE_UPSTREAM_WS` is set.
- Mounts built frontend static files from `settings.frontend_dist_resolved`.

`backend/app/config.py`

- Central Pydantic settings object using `VIEWER_` env prefix.
- Defines `PROJECT_ROOT`, `DEFAULT_FRONTEND_DIST`.
- `Settings.root_resolved`: expanded absolute served directory.
- `Settings.frontend_dist_resolved`: expanded absolute frontend build directory.
- Important settings: `root`, `host`, `port`, `frontend_dist`, `max_text_preview_bytes`, `show_hidden`, `poll_delay_ms`, `terminal_shell`, `debug`, `log_file`.
- Voice settings: `voice_enabled`, `voice_model`, `voice_language`, `voice_target_language`, `voice_backend`, `voice_backend_policy`, `voice_direct_english_translation`, `voice_min_chunk_size`, `voice_vac`, and `voice_vad` configure the in-process WhisperLiveKit engine. `run.py` enables voice by default, uses `large-v3-turbo`, and sets the default backend to `whisper` with `localagreement` unless env vars override them. `voice_upstream_ws` bypasses the in-process engine and proxies microphone audio to a separate streaming ASR WebSocket.

`backend/app/files.py`

- File tree, path normalization, metadata, content reading, and `.viewer.config.json` persistence.
- `normalize_relative(path)`: converts slashes, strips leading/trailing slashes, rejects `..` path segments.
- `resolve_path(path)`: joins normalized relative path to `settings.root_resolved`. Symlinks are allowed by current implementation.
- `relative_for(path)`: returns path relative to root when possible; symlink targets or external paths may become absolute if outside root.
- `guess_mime(path)`: MIME type from filename.
- `preview_kind(path, mime, size)`: maps file extension/MIME to `image`, `markdown`, `pdf`, `text`, or `unsupported`.
- `content_hash(path)`: computes SHA-256 for cache tagging.
- `entry_for(path)`: builds `FileEntry` for a directory child.
- `list_directory(path)`: validates directory, filters hidden files when configured, sorts directories first.
- `get_meta(path)`: validates file and returns `FileMeta`, including preview type, text-size limit flag, and `content_hash`.
- `read_text(path)`: reads UTF-8 with replacement fallback; rejects oversized text previews.
- `config_path()`: root-local `.viewer.config.json`.
- `read_config()` / `write_config(config)`: load and save pinned paths, last sidebar directory, nav appearance, and Markdown theme config; missing/invalid saved directories fall back to root.

`backend/app/events.py`

- Server-Sent Events fanout for filesystem changes.
- `EventHub.publish(event)`: pushes a `WatchEvent` to every subscriber queue and drops full queues.
- `EventHub.subscribe()`: async generator yielding `ready` then `file-change` SSE messages.
- `hub`: singleton used by `main.py` and `watcher.py`.

`backend/app/watcher.py`

- Watches `settings.root_resolved` with `watchfiles.awatch`.
- `event_type(change)`: converts `watchfiles.Change` enum to API strings.
- `is_ignored_path(path)`: ignores project `logs/` changes.
- `watch_root(stop_event)`: debounced watch loop; publishes `WatchEvent` with type, relative path, directory flag, and mtime.

`backend/app/terminals.py`

- Interactive shell session manager using OS PTYs, async tasks, and WebSockets.
- `TerminalClient`: websocket client plus outgoing queue and writer task.
- `TerminalSession`: PTY process state, buffered output, per-session output log path, current PTY rows/cols, shared layout lock state, connected clients, locks, and tasks.
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
- `_broadcast()`, `_enqueue()`, `_client_writer()`, `_remove_client()`: websocket fanout and cleanup.
- `_set_size(fd, rows, cols)`: low-level `TIOCSWINSZ`.
- `terminal_manager`: singleton used by routes.

`backend/app/voice.py`

- Optional WebSocket bridge for low-latency voice input.
- Lazily loads a singleton WhisperLiveKit `TranscriptionEngine` on the first voice connection when `VIEWER_VOICE_ENABLED=true` and no upstream WebSocket is configured.
- `connect_voice(websocket)`: accepts browser audio chunks from `/api/voice/ws`, runs them through WhisperLiveKit or the configured upstream ASR WebSocket, and returns normalized `partial` / `final` transcript JSON to the frontend.
- `_connect_whisperlivekit(websocket)`: creates one WhisperLiveKit `AudioProcessor` per browser voice session, forwards binary audio frames into it, and streams normalized result-state updates back to the client.
- `_normalize_upstream_message(message)` / `_normalize_payload(payload)`: accept common streaming ASR response shapes plus WhisperLiveKit `lines` / `buffer_transcription` / `buffer_translation` state and normalize final/partial text.

`backend/app/models.py`

- Pydantic API schemas: `FileEntry`, `DirectoryListing`, `FileMeta`, `ConfigData`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `WatchEvent`, `TerminalInfo`, `TerminalCreate`, `TerminalSnapshot`, `ClientLog`.
- These should stay aligned with TypeScript interfaces under `frontend/src/types/`.

`backend/app/logging.py`

- Loguru setup and standard logging interception.
- `InterceptHandler.emit(record)`: redirects Python logging records into Loguru.
- `current_log_path()`: resolves `VIEWER_LOG_FILE`.
- `configure_logging(log_file, debug)`: configures stderr and rotating file logs, intercepts uvicorn/FastAPI logs.
- `ensure_logging()`: idempotent fallback used by app import.

`backend/app/__init__.py`

- Empty package marker.

## Frontend Structure

`frontend/src/main.ts`

- Imports Bootstrap, icons, KaTeX, Highlight.js, and app CSS.
- Calls `installClientLogging()`.
- Creates Vue app, installs Pinia, mounts `App.vue`.

`frontend/src/App.vue`

- Root shell: top bar, sidebar drawer/pinned layout, workspace.
- Loads config/terminal lists, restores the saved sidebar directory, loads that directory, restores layout, and exposes the top-bar configuration panel button.
- Applies appearance and active Markdown theme settings from `stores/files.ts` as CSS variables on the app shell, including nav height/icon size and Markdown/syntax colors.
- Connects SSE with `connectEvents()`.
- Refreshes affected file listings on change events.
- Dispatches `viewer:file-changed` when an open file changes.
- Polls terminal list every 3 seconds.
- Renders active pane toolbar metadata/actions from `stores/paneToolbar.ts` in the top bar.
- Sidebar state functions: `toggleSidebarPin()`, `clampSidebarWidth()`, `startSidebarResize()`.
- Workspace actions: `openFile()`, `openTerminal()`, `splitActivePane()`, `closeActivePane()`.

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
- `handleChange(event)`: reloads metadata when this pane's file changed.
- Chooses `TerminalViewer`, `ImageViewer`, `MarkdownViewer`, `PdfViewer`, `TextViewer`, or `UnsupportedViewer`.

`frontend/src/components/FileSidebar.vue`

- Sidebar content: new terminal button, terminal list, pinned paths, current folder, parent button, and `FileTree`.
- `openPinned(path)`: tries to enter pinned path as directory, otherwise opens file.
- `newTerminal()`: creates terminal in the current sidebar directory and opens it in active pane.
- `closeTerminal(id)`: deletes terminal and clears matching panes.

`frontend/src/components/ConfigPanel.vue`

- Configuration UI opened from the top bar.
- Edits `.viewer.config.json` through the existing `/api/config` endpoint.
- Sections: Appearance, Markdown, Syntax Highlighting, and raw JSON.
- Appearance currently controls nav bar size, which also drives icon/button size via CSS variables.
- Markdown config stores an active theme plus a theme list. The editor can duplicate/reset themes and edit heading/body/paragraph/code font sizes, colors, weights, link/code/border colors, and Highlight.js token colors.

`frontend/src/components/FileTree.vue`

- Flat current-directory file list.
- `icon(entry)`: chooses Bootstrap icon by directory/MIME/extension.
- `select(entry)`: opens files.
- `enter(entry)`: enters directories on double click.
- `isActive(entry)`: highlights open files.
- Pin button calls `files.togglePin(entry.path)`.

`frontend/src/components/viewers/TextViewer.vue`

- Text/code preview with Highlight.js and copy-all button.
- `extensionLanguages` / `filenameLanguages`: extension-to-highlight language maps.
- `escapeHtml(value)`: manual HTML escaping for plaintext fallback.
- `languageForPath(path)`: resolves preferred highlighter.
- `highlightText(value)`: returns highlighted HTML.
- `persistCurrentScroll()`: saves scroll position.
- `copyAll()`: clipboard write with textarea fallback.
- `load()`: fetches text, highlights, restores scroll.
- Uses syntax CSS variables from the active Markdown theme for Highlight.js token colors.

`frontend/src/components/viewers/MarkdownViewer.vue`

- Markdown preview using `markdown-it` plugins, KaTeX, Mermaid, and Highlight.js.
- Enables raw HTML and `securityLevel: "loose"` for Mermaid, so trust boundary is local/private content.
- `escapeHtml(value)`: code-block fallback escaping.
- Custom fence renderer turns ```mermaid fences into Mermaid blocks.
- `renderMermaid()`: replaces Mermaid blocks with rendered SVG or marks errors.
- `load()`: fetches Markdown text, renders HTML, renders Mermaid, restores scroll.
- `persistCurrentScroll()`: saves scroll position.
- Uses Markdown and syntax CSS variables from the active theme for headings, paragraphs, links, code blocks, tables, and Highlight.js token colors.

`frontend/src/components/viewers/ImageViewer.vue`

- Image preview using raw file URL tagged with file `content_hash`.
- Saves and restores scroll position around path/hash changes.

`frontend/src/components/viewers/PdfViewer.vue`

- PDF preview via full-pane iframe pointed at raw file URL tagged with file `content_hash`.

`frontend/src/components/viewers/UnsupportedViewer.vue`

- Fallback for unsupported preview types.
- `formatSize(size)`: human-readable bytes/KB/MB.
- Shows MIME, size, and raw-open link.

`frontend/src/components/viewers/TerminalViewer.vue`

- xterm.js terminal pane connected to backend WebSocket.
- Registers terminal shell/status, quick-key controls, and terminate action with `stores/paneToolbar.ts` for top-bar rendering while the pane is active.
- The terminal text input pad includes a mic button that records browser audio with `MediaRecorder`, streams chunks to `/api/voice/ws`, displays partial transcript text in the pad, and leaves command sending under explicit user control.
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

## Frontend Stores And API

`frontend/src/api/client.ts`

- Shared fetch helpers for REST and raw/WS URLs.
- `request<T>()`: JSON request with error text on non-2xx.
- File APIs: `rawUrl(path, contentHash?)`, `getTree()`, `getMeta()`, `getText()`, `getConfig()`, `putConfig()`.
- Terminal APIs: `listTerminals()`, `createTerminal(cwd)`, `getTerminal()`, `terminateTerminal()`, `deleteTerminal()`, `terminalSocketUrl()`.
- Voice API helper: `voiceSocketUrl()` builds the browser WebSocket URL for `/api/voice/ws`, using `wss://` when the page is served over HTTPS.

`frontend/src/api/events.ts`

- `connectEvents(onChange, onState)`: creates `EventSource` for `/api/events`, reports connection state, parses `file-change` events.

`frontend/src/stores/files.ts`

- Pinia store for directory listings, current path, expanded dirs, pinned paths, appearance config, Markdown theme config, and loading state. The current path, pins, appearance, and Markdown themes are persisted in `.viewer.config.json`.
- Getters: `rootEntries`, `currentEntries`, `parentPath`, `activeMarkdownTheme`.
- Actions: `loadConfig()`, `saveConfig()`, `saveAppearance()`, `saveMarkdown()`, `saveViewerConfig()`, `loadDirectory()`, `enterDirectory()`, `enterParentDirectory()`, `toggleDirectory()`, `refreshAffected()`, `togglePin()`. `enterDirectory()` persists the last visited sidebar directory.

`frontend/src/stores/layout.ts`

- Pinia store for recursive split layout and active pane.
- Helpers: `id()`, `defaultLayout()`, `findPane()`, `mapNode()`, `firstPaneId()`, `mapAllPanes()`, `removePane()`.
- Getters: `activePane`, `openPaths`, `openTerminalIds`.
- Actions: `load()`, `save()`, `setActive()`, `openFile()`, `openTerminal()`, `splitPane()`, `setRatio()`, `clearPane()`, `closePane()`, `clearTerminal()`.
- Persists to `localStorage` key `viewer.layout.v1`.

`frontend/src/stores/paneToolbar.ts`

- Non-persistent active-pane toolbar registry.
- Exposes generic per-pane title/status/action metadata so viewers can contribute top-bar controls without coupling `App.vue` to viewer-specific behavior.
- Actions may hold callbacks owned by the registering viewer and are cleared when that viewer unmounts.

`frontend/src/stores/terminals.ts`

- Pinia store for terminal summaries.
- Actions: `load()`, `create()`, `upsert()`, `terminate()`, `remove()`.

`frontend/src/types/files.ts`

- TypeScript mirror of backend file/config/watch schemas: `EntryType`, `PreviewType`, `FileEntry`, `DirectoryListing`, `FileMeta`, `AppearanceConfig`, `MarkdownConfig`, `MarkdownTheme`, `ViewerConfig`, `WatchEvent`.

`frontend/src/types/layout.ts`

- Recursive `LayoutNode` union and `SplitDirection`.

`frontend/src/types/terminals.ts`

- TypeScript mirror of terminal schemas: `TerminalStatus`, `TerminalInfo`, `TerminalSnapshot`.

`frontend/src/utils/scrollMemory.ts`

- Browser-local scroll persistence under `viewer.scrollPositions.v1`.
- `readAll()`, `writeAll()`, `keyFor()`.
- `saveScrollPosition(path, element)`: stores `scrollTop` and `scrollLeft`.
- `restoreScrollPosition(path, element)`: retries restoration across animation frames while content lays out.

`frontend/src/utils/clientLog.ts`

- Sends frontend errors to `/api/debug/client-log`.
- `stringify(value)`: converts unknown error payloads.
- `sendClientLog(entry)`: uses `navigator.sendBeacon()` with fetch fallback.
- `installClientLogging()`: hooks `window.error`, `unhandledrejection`, and `console.error`.

`frontend/src/styles.css`

- Global app layout and shared classes: shell, top bar, sidebar drawer/pinned mode, resizers, workspace wrapper, icon buttons, mobile sidebar behavior, scroll area, muted text.
- CSS variables: `--sidebar-width`, `--topbar-height`, `--nav-button-size`, `--nav-icon-size`, `--border`, `--panel`, `--surface`, `--text-muted`.

`frontend/src/vite-env.d.ts`

- Vite TypeScript environment declarations.

## Root And Build Files

`run.py`

- Main production/development launcher.
- Constants: project paths, default root `~/Sync`, host `0.0.0.0`, port `18989`, log dir.
- `parse_args()`: CLI options for root, port, host, frontend dist, build, reload, debug, log paths.
- `resolve_project_path(path)`: resolves relative paths against repo root.
- `build_frontend(debug)`: runs `npm run build` in `frontend/`, passing debug sourcemap env flags.
- `default_log_file(log_dir)`: timestamped log path.
- `main()`: validates served root, exports env, configures logging, optionally builds frontend, prints URLs, starts uvicorn.

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

`.viewer.config.json`

- Root-local runtime pin storage when this repo is itself the served root. Ignored by git.

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

- Root `.viewer.config.json`: pinned files/folders, last sidebar directory, appearance, and Markdown themes, managed by `/api/config`.
- `localStorage viewer.layout.v1`: split tree, active pane, open file paths, open terminal IDs.
- `localStorage viewer.sidebarPinned.v1`: sidebar pinned state.
- `localStorage viewer.sidebarWidth.v1`: sidebar width.
- `localStorage viewer.scrollPositions.v1`: per-path scroll positions.
- `logs/viewer-*.log`: timestamped runtime logs from `run.py`.

## Common Fault Locations

- File cannot open or wrong preview type: check `backend/app/files.py` `preview_kind()`, `get_meta()`, frontend `ViewerPane.vue`, and specific viewer.
- Directory tree stale: check `backend/app/watcher.py`, `backend/app/events.py`, `frontend/src/api/events.ts`, and `files.refreshAffected()`.
- Live refresh not firing: check SSE `/api/events`, `App.vue` `connectEvents()` callback, and `ViewerPane.vue` `handleChange()`.
- Text too large: `settings.max_text_preview_bytes` and `read_text()`.
- Path/security issues: `normalize_relative()`, `resolve_path()`, symlink behavior in `files.py`.
- Terminal creation fails: `settings.terminal_shell`, `TerminalManager.create()`, shell availability, PTY permissions.
- Terminal output glitches: `TerminalViewer.vue` snapshot/output version logic and `TerminalManager._read_output()`.
- Terminal resize issues: `TerminalViewer.vue resize()` and `TerminalManager.resize()`.
- Frontend runtime errors: browser console, `/api/debug/client-log`, `backend/app/logging.py`, `logs/`.
- Production frontend missing: build `frontend/dist` or set `VIEWER_FRONTEND_DIST`.

## Maintenance Rules

- Keep this file synchronized with code when responsibilities move or files are added/removed.
- Keep backend schemas and frontend TypeScript interfaces aligned.
- Do not hand-edit generated dependency/build artifacts (`uv.lock`, `frontend/package-lock.json`, `frontend/dist/`, `frontend/node_modules/`).
- The app is read-only for served files except `.viewer.config.json` pin storage and terminal processes, which can modify files because they run a real shell in the served root.
