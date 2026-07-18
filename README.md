# Local Live File Viewer

Local Live File Viewer is a private-network workspace for coordinating Codex and Hermes roles while browsing and editing local files. A FastAPI backend serves the API and built Vue application; the normal frontend is a single Super Workspace shell with chats, roles, files, Git changes, terminals, Settings, and recursively split panes.

The application assumes a trusted machine and trusted LAN. Terminal, Git, file editing, and Agent processes can modify local files.

## Features

- Create direct or group chats and assign member roles.
- Route a message automatically through an OpenAI-compatible dispatcher or target roles explicitly.
- Run Codex and Hermes roles in the background and stream their persisted output into Chat panes.
- Retain visible Super Workspace chat messages to an optional chat-scoped Hindsight memory bank.
- Stop a running role with a two-click confirmation.
- Give each role a dispatcher-facing description and a separate Agent-facing prompt.
- Reuse role sessions per chat or start a new session for each run.
- Cite earlier messages with `@msg-...` references.
- Browse, upload, delete, edit, and live-refresh files under the current Chat Root.
- Preview Markdown, HTML, images, PDFs, CSV, text, source code, and Git diffs.
- Render Markdown with KaTeX, Mermaid, Highlight.js, tables, task lists, footnotes, and local links.
- Stage, revert, commit, and push changes from Git diff panes.
- Open reconnectable PTY terminals through WebSockets.
- Use optional voice dictation and LLM refinement.
- Arrange chats, files, diffs, and terminals in persisted recursive split panes.
- Configure appearance, Markdown themes, models, dispatcher profiles, voice, and server controls.

## Super Workspace

The application has one Super Workspace, persisted under the fixed owner `dailing:default`. It contains user-created chats and globally available roles.

Every chat requires a `root`, relative to the server's `VIEWER_ROOT` filesystem boundary. Files, Git, terminals, Codex, and Hermes use that Chat Root. A role may optionally add a subdirectory beneath it; there is no profile or global-working-directory fallback.

A role has two distinct instruction fields:

- `description`: routing metadata for the dispatcher. It describes when the role should be selected, its capabilities, and its dispatch constraints.
- `prompt`: operating instructions delivered directly to the selected Agent. It defines workflow, standards, style, and execution rules.

The dispatcher receives descriptions but not prompts. A role Agent receives its prompt but not its description. Changing a prompt clears that role's reusable chat-session mappings so the next run starts with the new rules.

Normal message delivery is asynchronous:

1. The backend persists the user query and one dispatch task for each selected role.
2. The independent Super Workspace worker claims queued tasks while serializing work per chat and role.
3. A Codex or Hermes provider session is created or resumed according to the role policy.
4. Provider output is persisted in `~/.view/agent-history.sqlite3` and announced through Super Workspace SSE.
5. The Chat pane incrementally reloads the changed run.

Codex work runs through detached background processes, so restarting the Viewer backend does not terminate an active Codex run. Hermes work uses an ACP subprocess for the selected Hermes Profile; the independent worker starts the default Profile's ACP adapter and creates each Hermes session with the Chat Root as its real working directory.

## Persistence

Viewer-owned state is stored under `~/.view`:

- `config.json`: appearance, Markdown, Codex, Voice, and dispatcher configuration.
- `agent-history.sqlite3`: Super Workspace, chats, roles, dispatch tasks, messages, citations, and reusable session mappings.
- `logs/codex-sessions/`: Viewer metadata and stderr for Codex provider sessions.
- `logs/hermes-sessions/`: Viewer metadata for Hermes provider sessions.
- `logs/terminals/`: reconnectable terminal output logs.
- `logs/`: backend, worker, manager, and voice logs.

Lightweight worker and detached process state defaults to `/tmp/viewer_run`.

Browser-local state stores pane layout, sidebar state, pins, drafts, dispatch selection, and scroll positions. Legacy `*.dailing` keys are migrated to their non-namespaced equivalents when first read.

## Hindsight memory

Hindsight retention is owned by Viewer, not by individual Agent processes. When `super_workspace.hindsight_retain_enabled` is enabled in `~/.view/config.json`, Viewer retains each visible user query and final assistant response into the current chat's bank:

```text
{hindsight_bank_prefix}::{user_id}::{workspace_id}::chat::{chat_id}
```

The default prefix is `super-workspace`. Each Viewer message uses a stable document id, so streaming updates and retries upsert the same memory instead of retaining an entire Codex transcript repeatedly.

Codex and Hermes do not need their own Hindsight retain hooks. In particular, do not register Hindsight `SessionStart`, `UserPromptSubmit`, or `Stop` commands in `~/.codex/hooks.json` for Viewer-managed roles. Viewer retention is provider-independent and applies to messages after they enter the Super Workspace history database.

The Hindsight API URL is resolved in this order:

1. `VIEWER_HINDSIGHT_API_URL`
2. `super_workspace.hindsight_api_url` in `~/.view/config.json`
3. `hindsightApiUrl` in `~/.hindsight/codex.json`

The API token uses `VIEWER_HINDSIGHT_API_TOKEN`, falling back to `hindsightApiToken` in `~/.hindsight/codex.json`. Retention failures are non-blocking. Viewer currently performs retain only; it does not recall Hindsight memories into provider prompts.

## Requirements

- Python 3.11 or newer
- Node.js and npm
- `codex` on `PATH` for Codex roles
- `hermes` on `PATH` with the ACP optional dependency for Hermes roles
- An OpenAI-compatible chat-completions endpoint for automatic routing

## Install

Install backend dependencies:

```bash
uv sync
```

Install frontend dependencies:

```bash
cd frontend
npm install
```

Build the frontend:

```bash
cd frontend
npm run build
```

## Run

Start the backend and serve the built frontend:

```bash
uv run python run.py
```

The default bind address is `0.0.0.0:18989`. To select another root or port:

```bash
uv run python run.py --serve-dir /path/to/root --port 8000
```

Build before starting:

```bash
uv run python run.py --build-frontend
```

Useful options:

```bash
uv run python run.py --host 127.0.0.1
uv run python run.py --reload
uv run python run.py --debug
uv run python run.py --log-dir ~/.view/logs
uv run python run.py --log-file /tmp/viewer.log
```

`--debug` enables verbose backend logging and frontend source maps when used with `--build-frontend`.

## Configuration

The main environment variables are:

- `VIEWER_ROOT`: fallback served root.
- `VIEWER_FRONTEND_DIST`: built frontend directory.
- `VIEWER_LOG_FILE`: backend log file.
- `VIEWER_MAX_TEXT_PREVIEW_BYTES`: large-text preview threshold.
- `VIEWER_SHOW_HIDDEN`: whether hidden files appear.
- `VIEWER_TERMINAL_SHELL`: terminal shell.
- `VIEWER_CODEX_RUN_DIR`: detached Codex process state directory.
- `VIEWER_WEAVER_RUN_DIR`: Super Workspace worker and provider-driver registry directory.
- `VIEWER_HERMES_ACP_ENABLED`: start Hermes ACP with the worker; defaults to `true`.
- `VIEWER_HERMES_PROFILE`: Hermes Profile used by ACP; defaults to `default`.
- `VIEWER_HERMES_YOLO`: start only the Viewer-owned Hermes ACP process with `--yolo`; defaults to `true`. Hermes' gateway/profile approval configuration is not changed.
- `VIEWER_HERMES_COMMAND`: Hermes executable; defaults to `hermes`.
- `VIEWER_HINDSIGHT_API_URL`: optional Hindsight API URL override for Viewer-managed chat retention.
- `VIEWER_HINDSIGHT_API_TOKEN`: optional Hindsight API token override.
- `VIEWER_SUPER_DISPATCH_URL`: fallback automatic-dispatch endpoint.

Most user-facing settings are managed from Settings and persisted in `~/.view/config.json`.

## Standard checks

Do not start the frontend development server for routine verification. Use:

```bash
cd frontend && npm run build
```

```bash
.venv/bin/python3 -m compileall backend/app backend/tests
```

Role routing, prompt-boundary, and Hermes ACP tests:

```bash
.venv/bin/python3 -m unittest backend.tests.test_super_workspace_role_prompts backend.tests.test_hermes_acp_sessions -v
```

For the detailed module map, data flow, API inventory, and fault locations, see [`architecture.md`](architecture.md).
