# Local Live File Viewer - Project Plan

## Goal

Build a local web app that serves and previews files from a configured directory on this machine. The app should make it easy to browse folders, open files, and see changes quickly when files are edited by agents, scripts, or other programs.

The intended stack is:

- Backend: Python + FastAPI
- Frontend: Vue + Vite
- UI: Bootstrap / Bootstrap-compatible Vue components
- Live updates: filesystem watcher on the backend plus WebSocket or Server-Sent Events notifications

## Core User Experience

The app has two main areas:

- Sidebar: a file browser for the configured root folder
- Main workspace: one or more viewer panes arranged by a configurable split layout

The default layout is one viewer pane. A pane can be split horizontally or vertically. Each resulting pane can be split again, forming a recursive layout tree. Split ratios are adjustable and persisted locally so the layout is restored on reload.

Opening a file loads it into the currently active pane. If that file changes on disk, the backend notifies the frontend and that pane refreshes automatically.

## Supported File Types

Initial preview support:

- Images: `png`, `jpg`, `jpeg`, `gif`, `webp`, `bmp`, `svg`
- Markdown: `md`, `markdown`
- PDF: `pdf`
- Plain text / code: common text files can be displayed as text
- Unsupported files: show a file icon, metadata, and an unsupported-preview message

Markdown should support a full-featured rendering path:

- GitHub-flavored Markdown
- Tables, task lists, code blocks
- LaTeX math
- Mermaid diagrams
- Syntax highlighting

Recommended frontend Markdown stack:

- `markdown-it`
- `markdown-it-texmath` or KaTeX integration
- `katex`
- `mermaid`
- `highlight.js` or Shiki

## Backend Design

FastAPI service responsibilities:

- Serve the frontend build in production
- Expose file tree and file metadata APIs
- Serve file contents safely from a configured root directory
- Watch the configured root directory for changes
- Notify connected clients when watched files or directories change

Suggested backend package layout:

```text
backend/
  app/
    main.py
    config.py
    models.py
    files.py
    watcher.py
    events.py
  pyproject.toml
```

Important backend behavior:

- All file access must be constrained to the configured root directory.
- Paths sent by the frontend should be relative paths, not arbitrary absolute paths.
- Symlink handling should be explicit. Default proposal: allow symlinks only if the resolved target stays inside the configured root.
- File content responses should include metadata such as modified time, size, MIME type, and an entity tag or version token.
- File watching should use `watchfiles` or `watchdog`. Polling can be used as fallback if native watching is unavailable.

Suggested APIs:

```text
GET /api/tree?path=
GET /api/file/meta?path=relative/path
GET /api/file/content?path=relative/path
GET /api/file/raw?path=relative/path
GET /api/events
```

`/api/events` can be implemented with Server-Sent Events first because the client mainly needs one-way notifications from backend to frontend. WebSocket can be added later if bidirectional coordination becomes useful.

## Frontend Design

Vue app responsibilities:

- Render responsive file browser sidebar
- Render recursive split-pane workspace
- Preview supported file types
- Track active pane and opened file per pane
- Listen for backend file-change events
- Refresh affected panes when their file changes
- Persist layout, open files, and sidebar state

Suggested frontend layout:

```text
frontend/
  src/
    main.ts
    App.vue
    api/
      client.ts
      events.ts
    components/
      FileSidebar.vue
      FileTree.vue
      Workspace.vue
      SplitNode.vue
      ViewerPane.vue
      viewers/
        ImageViewer.vue
        MarkdownViewer.vue
        PdfViewer.vue
        TextViewer.vue
        UnsupportedViewer.vue
    stores/
      layout.ts
      files.ts
    types/
      layout.ts
      files.ts
```

Recommended frontend dependencies:

- `vue`
- `vite`
- `typescript`
- `pinia`
- `bootstrap`
- `bootstrap-icons` or `lucide-vue-next`
- `splitpanes` or a small custom recursive splitter
- `markdown-it`
- `katex`
- `mermaid`
- `highlight.js` or `shiki`

For the recursive split layout, I recommend using a simple layout tree in app state:

```ts
type LayoutNode =
  | { type: "pane"; id: string; filePath?: string }
  | {
      type: "split";
      id: string;
      direction: "horizontal" | "vertical";
      ratio: number;
      first: LayoutNode;
      second: LayoutNode;
    };
```

Persisted state:

- Layout tree
- Active pane id
- Open file path per pane
- Sidebar width / collapsed state
- Expanded folders

Use `localStorage` first. A server-side profile store can be added later if needed.

## Responsive Behavior

Desktop / tablet:

- Sidebar and main workspace side by side
- Resizable sidebar
- Full split-pane controls

Phone:

- Sidebar becomes an off-canvas drawer or collapsible panel
- Main workspace fills the screen
- Split panes remain usable, but controls should be compact
- File selection closes the sidebar and opens the file in the active pane

## Live Update Flow

1. Backend watches the configured root directory.
2. File changes produce normalized events with relative path, event type, and timestamp.
3. Frontend receives events through SSE.
4. If an open pane is showing that path, the pane reloads content.
5. If a directory changes, the sidebar refreshes the affected folder.

Possible event payload:

```json
{
  "type": "modified",
  "path": "notes/example.md",
  "is_dir": false,
  "mtime": 1777012800.123
}
```

## Development Commands

Proposed local development:

```text
backend:  uvicorn app.main:app --reload --host 0.0.0.0 --port 8000
frontend: npm run dev -- --host 0.0.0.0 --port 5173
```

The frontend dev server proxies `/api` to FastAPI. For production, FastAPI can serve the built frontend from `frontend/dist`.

## Configuration

Minimum configuration:

```text
VIEWER_ROOT=/path/to/folder/to/view
VIEWER_HOST=0.0.0.0
VIEWER_PORT=8000
```

Optional later configuration:

- Max file size for inline text preview
- Hidden file visibility
- Ignored glob patterns
- Whether to follow symlinks
- Polling fallback interval

## Security Model

This is intended for local/private network use. Still, the app should avoid obvious unsafe behavior:

- Restrict file reads to `VIEWER_ROOT`
- Normalize and validate all requested paths
- Do not expose arbitrary absolute path reads
- Do not allow writes in the initial version
- Avoid rendering raw unsafe HTML from Markdown by default
- Consider a simple token/password later if exposed beyond trusted devices

## Implementation Phases

### Phase 1: Working MVP

- Create FastAPI backend
- Create Vue/Vite frontend
- Configure backend root directory
- Implement file tree API
- Implement raw file serving
- Implement sidebar file browser
- Implement single-pane viewer
- Support image, Markdown, PDF, text, and unsupported previews

### Phase 2: Live Reload

- Add filesystem watcher
- Add SSE endpoint
- Refresh open pane when file changes
- Refresh directory listing when directory changes

### Phase 3: Split Layout

- Add recursive layout model
- Add split vertical / horizontal actions
- Add close pane / replace pane behavior
- Add adjustable split ratios
- Persist layout to `localStorage`

### Phase 4: Mobile Polish

- Add responsive sidebar behavior
- Tighten pane controls for iPad and phone
- Test common screen sizes

test
