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
- Optional terminal voice input through in-process WhisperLiveKit or an upstream ASR WebSocket
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

## Voice input

Terminal panes have a text input pad for mobile entry. Open it by double-tapping the terminal or using the text button in the terminal toolbar, then use the mic button inside the text box. Transcribed text is inserted into the pad only; use Send or Send + Enter when you are ready.

Voice input is enabled by default with the in-process WhisperLiveKit backend and the `large-v3-turbo` model:

```bash
uv run python run.py
```

Useful voice options:

```bash
uv run python run.py --no-voice
uv run python run.py --voice-model small --voice-language auto
VIEWER_VOICE_BACKEND_POLICY=localagreement uv run python run.py
```

The browser sends `MediaRecorder` audio chunks to `/api/voice/ws`. The backend decodes and transcribes them with WhisperLiveKit, then streams normalized partial transcript text back to the paste pad. If the viewer is loaded over HTTPS, the frontend automatically uses `wss://` for terminal and voice sockets. You can still point `VIEWER_VOICE_UPSTREAM_WS` at a separate WhisperLiveKit `/asr` service; when it is set, the backend proxies audio there instead of loading an in-process model.

The default backend is `whisper` with `localagreement` because it avoids WhisperLiveKit's faster-whisper CTranslate2 CUDA runtime. Advanced backend overrides are available through `VIEWER_VOICE_BACKEND` and `VIEWER_VOICE_BACKEND_POLICY`; faster-whisper on Linux may require CUDA 12 runtime libraries such as `libcublas.so.12`.

WhisperLiveKit translation can be enabled with `VIEWER_VOICE_TARGET_LANGUAGE`, but its NLLB translation dependency is separate from the default transcription install.

## Debug logs

Each `run.py` startup creates a timestamped log file under `logs/` and prints a local log URL:

```bash
uv run python run.py --debug --build-frontend
```

`--debug` enables verbose backend logging, frontend sourcemaps during `--build-frontend`, xterm debug logging, and browser error reporting to `/api/debug/client-log`. The current log can be viewed at `/api/debug/log`.
