# Local Live File Viewer

A private-network, read-only file viewer for browsing a configured folder and live-refreshing open previews when files change.

## Features

- FastAPI backend serving files from `VIEWER_ROOT`
- Vue + Vite frontend
- Sidebar file browser with pinned files/folders
- Root-local pin storage in `.viewer.config.json`
- Recursive split-pane workspace with browser-local persistence
- Image, Markdown, PDF, text/code, and unsupported-file previews
- Markdown with raw HTML, KaTeX math, Mermaid diagrams, tables, and code highlighting
- Server-Sent Events for live file-change notifications
- Symlinks are allowed, including targets outside `VIEWER_ROOT`
- Read-only by design

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

By default this serves `~/Sync` on port `18989`.

To choose a folder and port:

```bash
uv run python run.py /path/to/folder --port 8000
```

Or use the explicit serve-directory option:

```bash
uv run python run.py --serve-dir /path/to/folder --port 8000
```

Open `http://localhost:18989`. On your phone or iPad, use this machine's LAN IP with port `18989`.

When frontend code changes, rebuild it with:

```bash
uv run python run.py --build-frontend
```

This runs `npm run build` in `frontend/` before starting the server. The backend serves `frontend/dist` by default. You can override that with `VIEWER_FRONTEND_DIST=/other/dist/path` or `--frontend-dist /other/dist/path`.
