# Local Live File Viewer

Local Live File Viewer is a private-network workspace for browsing a local folder, previewing files, watching changes live, running shells, reviewing Git changes, and driving Codex sessions from the browser.

It is designed for a laptop or workstation that serves one trusted directory to devices on the same LAN, such as a phone, tablet, or another computer.

## What it can do

- Browse a configured root folder from a Vue sidebar file tree.
- Pin files and folders, remember the active directory, and keep separate state per workspace.
- Open files in recursive split panes with browser-local layout persistence.
- Switch between numbered workspaces, with per-workspace layouts, pins, open order, active pane, and Codex session associations.
- Preview images, PDFs, Markdown, CSV files, text, and source code.
- Render Markdown with raw HTML, tables, task lists, footnotes, definition lists, anchors, attributes, KaTeX math, Mermaid diagrams, and Highlight.js code blocks.
- Toggle Markdown between rendered and raw views.
- Open local Markdown links and images through the viewer when they resolve under the served root.
- Preserve scroll position while files live-refresh.
- Watch the served directory and push file-change events to the browser with Server-Sent Events.
- Run interactive terminal panes through PTYs and WebSockets, including reconnectable output logs.
- Use mobile-friendly terminal text entry and optional voice dictation.
- Review Git working-tree changes from the sidebar, open text diffs, stage, revert, commit, and push through backend APIs.
- Start a Codex-assisted auto-commit flow for the current directory.
- Create Codex panes that run `codex exec --json`, stream structured transcript updates, show file changes and patches, and survive viewer service restarts through detached background runners.
- Queue Codex prompts, edit queued items, resume sessions, terminate active runs, and view model/context/rate-limit status chips.
- Configure Codex models, proxy settings, muted operation-message opacity, and the auto-commit prompt from settings.
- Create scheduled Codex loop tasks stored as Markdown files under `~/.view/loops`.
- Run loop tasks manually or on `once`, `interval`, `daily`, and `multi_daily` schedules.
- Reuse or rotate Codex sessions for loop tasks based on run count, failure, or context usage.
- Customize navigation size and Markdown themes from the settings page.
- Use optional voice input through in-process `faster-whisper` / WhisperLiveKit handling or an upstream ASR WebSocket.
- Inspect backend health, debug info, current logs, and browser error reports through debug endpoints.
- Restart or stop the managed backend through admin endpoints.

## Architecture at a glance

- `run.py` sets `VIEWER_*` environment variables, optionally builds the frontend, configures logging, and starts Uvicorn.
- `backend/app` contains the FastAPI API, file serving, file watcher, Git integration, terminal manager, Codex session manager, voice bridge, settings persistence, and loop scheduler.
- `frontend/src` contains the Vue 3 app, Pinia stores, sidebar panels, split workspace, file viewers, terminal viewer, Codex viewer, settings page, and loop task editor.
- Built frontend assets are served by the same FastAPI process from `frontend/dist` by default.

Persistent viewer state is stored under `~/.view`:

- `~/.view/config.json` for appearance, Markdown, workspace count, and Codex defaults.
- `~/.view/workspaces.json` for active workspace, layouts, pins, current directories, open-order timestamps, and associated Codex sessions.
- `~/.view/loops/*.md` for loop task definitions.
- `~/.view/agent-loops.json` for loop runtime state.
- `~/.view/logs/` for viewer, terminal, Codex, voice, and loop logs.

Detached Codex runner state defaults to `/tmp/viewer_run/codex` and can be changed with `VIEWER_CODEX_RUN_DIR`.

## Setup

Install backend dependencies:

```bash
uv sync
```

Install frontend dependencies:

```bash
cd frontend
npm install
```

Build the frontend static files:

```bash
cd frontend
npm run build
```

## Run

FastAPI serves both the API and the built Vue frontend:

```bash
uv run python run.py
```

By default this serves `~/Sync` on port `18989` and binds to `0.0.0.0`.

Open the viewer on the same machine:

```text
http://localhost:18989
```

From a phone or tablet on the same LAN, use this machine's LAN IP with port `18989`.

Choose a folder and port:

```bash
uv run python run.py /path/to/folder --port 8000
```

Or use the explicit serve-directory option:

```bash
uv run python run.py --serve-dir /path/to/folder --port 8000
```

Build the frontend before starting:

```bash
uv run python run.py --build-frontend
```

The backend serves `frontend/dist` by default. Override that with:

```bash
uv run python run.py --frontend-dist /other/dist/path
```

or:

```bash
VIEWER_FRONTEND_DIST=/other/dist/path uv run python run.py
```

## Common options

```bash
uv run python run.py --host 127.0.0.1
uv run python run.py --port 8000
uv run python run.py --reload
uv run python run.py --debug
uv run python run.py --log-dir ~/.view/logs
uv run python run.py --log-file /tmp/viewer.log
```

Useful environment variables include:

- `VIEWER_ROOT`: directory served by the viewer.
- `VIEWER_FRONTEND_DIST`: built frontend directory.
- `VIEWER_MAX_TEXT_PREVIEW_BYTES`: text preview size limit.
- `VIEWER_SHOW_HIDDEN`: whether hidden files appear in the file tree.
- `VIEWER_POLL_DELAY_MS`: file-watch debounce delay.
- `VIEWER_TERMINAL_SHELL`: shell used by terminal panes.
- `VIEWER_CODEX_RUN_DIR`: detached Codex runner state directory.
- `VIEWER_LOG_FILE`: backend log file path.

## File previews

The viewer chooses a preview from file metadata and MIME/extension hints:

- Images are served through the raw file endpoint with cache tags.
- PDFs render in the browser viewer.
- Markdown renders through `markdown-it`, KaTeX, Mermaid, and Highlight.js.
- CSV files open as a table viewer.
- Text and code files open in a text viewer.
- Unsupported files show metadata and cannot be previewed inline.

Large text files are protected by the configured preview byte limit. Symlinks are allowed by the current implementation, including symlink targets outside `VIEWER_ROOT`.

## Git tools

The Git sidebar resolves the active Git context from the current sidebar directory. Status and actions are scoped to that context, while paths are mapped back into the served root for opening diffs.

Available Git operations:

- List changed files with status, added/deleted counts, and binary detection.
- Open unified text diffs.
- Stage one file or a scope.
- Revert tracked file changes or remove an untracked file.
- Commit with a message.
- Push the current branch.
- Start a Codex auto-commit session for the current directory.

Use the Git actions only on repositories you trust. Terminal, Git, Codex, and loop features can modify files.

## Terminals

Terminal panes run the configured shell in a served-root-relative working directory. They use REST for lifecycle operations and WebSockets for interactive input/output.

Terminal sessions support:

- Creating sessions from the current directory.
- Reconnecting to existing sessions.
- Persisted per-session output logs.
- Resize events from the browser.
- Process-group termination and cleanup.
- Mobile text entry with Send and Send + Enter.
- Optional voice dictation into the terminal input pad.

## Codex

Codex panes use `codex exec --json` and render structured events directly instead of emulating a terminal. A new pane can start idle, then the first prompt launches the actual Codex run.

Codex features include:

- Per-session model selection from configured model options.
- Detached background runs that keep going if the viewer service restarts.
- Resume support through captured Codex thread/session ids.
- Live transcript updates over WebSocket.
- Compact display events so raw rollout details stay server-side.
- File-change and patch rendering.
- Focus mode that hides noisy operation events.
- Prompt draft persistence in browser storage.
- Server-persisted queued prompts with edit/delete support.
- Termination for active background runs.
- Context usage and global rate-limit status parsed from Codex rollout files.
- Optional proxy injection for Codex subprocesses.

The built-in model defaults are stored in `~/.view/config.json`; the current default is `gpt-5.5` unless changed in settings.

## Loop tasks

Loop tasks are Codex automation jobs stored as Markdown files in `~/.view/loops/*.md`. The generated YAML frontmatter holds scheduling and session policy; the Markdown body is the prompt.

Supported schedule types:

- `manual`
- `once`
- `interval`
- `daily`
- `multi_daily`

Supported session policies:

- `new_each_run`
- `reuse`
- `reuse_until_context`
- `reuse_with_limits`

The scheduler writes runtime state to `~/.view/agent-loops.json` and run logs to `~/.view/logs/agent-loops/`. The loop task page can create, edit, pause, resume, run now, reset retained sessions, and inspect run history.

## Voice input

Voice input is enabled by default:

```bash
uv run python run.py
```

Disable it:

```bash
uv run python run.py --no-voice
```

Choose a model or source language:

```bash
uv run python run.py --voice-model small --voice-language auto
```

The persisted voice defaults live in `~/.view/config.json` under `voice` and can also be edited from Settings:

```json
{
  "voice": {
    "enabled": true,
    "model": "large-v3-turbo",
    "language": "auto",
    "translation_enabled": false,
    "target_language": "en"
  }
}
```

Useful voice environment variables:

- `VIEWER_VOICE_ENABLED`
- `VIEWER_VOICE_MODEL`
- `VIEWER_VOICE_LANGUAGE`
- `VIEWER_VOICE_TARGET_LANGUAGE`
- `VIEWER_VOICE_BACKEND`
- `VIEWER_VOICE_BACKEND_POLICY`
- `VIEWER_VOICE_UPSTREAM_WS`
- `VIEWER_VOICE_STOP_TIMEOUT_SECONDS`
- `VIEWER_VOICE_MODEL_IDLE_TIMEOUT_SECONDS`
- `VIEWER_VOICE_OFFLINE_BEAM_SIZE`
- `VIEWER_VOICE_OFFLINE_VAD_FILTER`
- `VIEWER_VOICE_VAC`
- `VIEWER_VOICE_VAD`

The browser sends recorded audio chunks to `/api/voice/ws`. By default the backend saves the recording under `~/.view/logs/voice/` and returns a final transcript after processing with the configured local backend. When `VIEWER_VOICE_UPSTREAM_WS` is set, the backend proxies audio to that ASR WebSocket and normalizes the upstream response.

If the viewer is loaded over HTTPS, the frontend automatically uses secure WebSockets for terminal, Codex, and voice connections.

## Debugging

Each `run.py` startup creates a timestamped log file under `~/.view/logs/` by default and prints a local log URL.

```bash
uv run python run.py --debug --build-frontend
```

`--debug` enables verbose backend logging, frontend sourcemaps during `--build-frontend`, xterm debug logging, and browser error reporting to `/api/debug/client-log`.

Useful endpoints:

- `/api/health`
- `/api/debug/info`
- `/api/debug/log`
- `/api/debug/client-log`
- `/api/admin/restart`
- `/api/admin/stop`

## Development checks

Use these checks before handing off changes:

```bash
cd frontend
npm run build
```

```bash
python -m compileall backend/app
```
