# Instance-Centric File Manager / File Viewer Split Implementation Plan

> **For Hermes:** Implement directly, task-by-task; do not delegate code and do not commit unless the user explicitly asks.

**Goal:** Introduce a class-like plugin instance model with serializable initialization parameters, use it to split File Manager from File Viewer, migrate Chat multi-view away from encoded ids, and add upload/download plus file and directory sizes.

**Architecture:** A plugin definition is a factory; a view instance is a unique id plus structured init params and plugin-owned state; a pane is only a placement referencing that instance. Singleton plugins have one fixed `main` instance, while Chat, File Manager and File Viewer have many. Shell opening and pane control are browser-local; backend domain protocols and the shared `file-service` remain separate.

**Tech Stack:** Vue 3, TypeScript, Pinia, Vitest, Go `file-service` / HTTP gateway, existing instance-store and ticket mechanisms.

---

## 1. Current Context

- `frontend/src/plugins/files/FilesPane.vue` owns both directory browsing and preview. `FileBrowser.vue` is an overlay inside the viewer, not an independently openable panel.
- `frontend/src/plugins/files/index.ts` implements a special pinned-folder launcher through `DockInstance.clickCreates`, so Dock opening semantics differ between files and other plugins.
- `frontend/src/stores/layout.ts` already implements open/focus, close, floating close/raise and DFS-order movement, but these are not exposed through a unified plugin-facing Shell API.
- Chat multi-view currently encodes two identities in one string: the primary view is `chatId`, copies are `chatId#suffix`. `ChatPane.vue`, `ComposerBox.vue`, `inputDispatch.ts`, `index.ts` and voice addressing consequently split strings or prefix-match panes.
- Chat backend RPCs already use structured `chat_id` and `branch_id`. Drafts are currently keyed by `(chat_id, branch_id)` and shared by every view of that line; this plan changes them to fixed per-view InputInstances. Only the frontend view identity is currently encoded incorrectly.
- `FileEntry` already includes `size`, `mtime`, MIME and symlink metadata. Ordinary file sizes need presentation work only.
- Downloads already use `file:_:ticket` + `GET /api/files/raw?download=1`. There is no upload data plane and no recursive directory-size RPC.
- `sdk/ts/src/plugin.ts` structurally mirrors the frontend plugin contract and must change with it.

## 2. Instance-Centric Design

### 2.1 Four distinct objects

Do not implement literal JavaScript classes or put live Vue objects in persistence. The class-like semantics come from a plugin factory plus plain serializable records.

1. **Plugin definition / factory**
   - Owns components, cardinality and the ability to create or reopen instances from structured params.
   - Declares `cardinality: "singleton" | "multi"`.

2. **View instance**
   - Has an opaque unique `instanceId`, a `paneType`, serializable `params`, and plugin-owned mutable state.
   - `params` are initialization/opening inputs, not a dump of runtime state.
   - Singleton cardinality resolves to the fixed id `main`; multi cardinality mints independent ids.

3. **Domain object**
   - A Chat, branch, file or terminal session is not itself a view instance.
   - A Chat view has `{chatId}`; the backend Chat remains the source of truth for messages, branches and chat-level pinning.
   - A File Viewer view has a file path; the file itself remains a filesystem object.

4. **Pane**
   - Retains the existing lightweight `{paneType, instanceId}` reference and placement.
   - It does not duplicate init params or domain state.

This supports two Chat view instances with different `instanceId`s, the same `params.chatId`, and independent branch selections.

### 2.2 Uniform opening contract

Replace the proposed generic command system with one standard instance-opening mechanism:

```ts
type PluginCardinality = "singleton" | "multi";

interface InstanceOpenRequest<P = unknown> {
  paneType: string;
  /** Existing instance to restore/update; omitted means create from params. */
  instanceId?: string;
  /** JSON-serializable plugin-specific opening parameters. */
  params?: P;
  /** Force a fresh pane placement, not necessarily a fresh instance. */
  newPane?: boolean;
}

interface OpenedInstance {
  paneId: string;
  instanceId: string;
}

interface ShellPanesApi {
  list(): ShellPaneInfo[];
  close(paneId: string): void;
  focus(paneId: string): void;
  move(paneId: string, delta: -1 | 1): void;
}

interface ShellApi {
  open<P = unknown>(request: InstanceOpenRequest<P>): Promise<OpenedInstance>;
  panes: ShellPanesApi;
}
```

Each multi-instance plugin registers an instance adapter/factory. Its `open` operation resolves an existing record or creates a new one, applies supplied params, and returns the instance id. Shell then performs the common placement/open/focus operation. Singleton plugins use the fixed `main` instance and reject unexpected multi-instance creation.

Semantics:

- `open({paneType, instanceId})` restores/focuses an existing instance.
- `open({paneType, params})` creates an instance from params.
- `open({paneType, instanceId, params})` updates the existing instance through its plugin adapter, then focuses it. File Manager uses this to change the paired viewer's path.
- `newPane` controls placement only. Creating another view of the same Chat means omitting `instanceId` with the same `chatId` param.
- Unknown plugin/type, unknown instance and invalid params fail before layout mutation.
- Static and external loaders register adapters through one registry and remove them on hot unload.
- No `createCommands` / generic `invoke` mechanism is needed for the current requirements.

### 2.3 Pane-control API

`ShellPaneInfo` includes `paneId`, `paneType`, `instanceId`, `location: "tiled" | "floating"`, and `active`.

- `list()` is a snapshot covering tiled and floating panes.
- `close()` handles both container kinds. Closing a pane does not implicitly delete a pinned domain/view instance.
- `focus()` activates tiled panes and raises floating panes.
- `move()` retains the current adjacent DFS-order operation. Arbitrary split-tree mutation, ratios and floating geometry are not exposed in this iteration.
- All operations affect only this browser and never cross the backend bus.

### 2.4 One Dock path, no launcher special case

- Dock `+`, pinned entries, voice `open_instance`, notifications and plugin-initiated opening all finish through `ctx.shell.open`.
- `DockProvider.singleton` moves to the plugin instance cardinality declaration.
- Remove `DockInstance.clickCreates` and `create(fromInstanceId)`.
- A pinned Dock entry points to a real view instance and reopens/focuses it.
- Creating an instance and placing it are separate steps inside the same `open` flow.
- Plugins still own their persistence and lifetime rules; this design standardizes identity and opening without forcing every domain object into one central database.

### 2.5 Plugin-specific instance shapes

**Chat view + fixed InputInstance**

```ts
interface ChatViewParams {
  chatId: string;
  /** Initial selection only; subsequent selection is view state. */
  initialTabs?: string[];
}

interface ChatInputInstance {
  id: string;             // Stable 1:1 binding to one ChatViewInstance.
  viewInstanceId: string;
  chatId: string;
  currentBranchId: string;
  // Existing InputSession intent fields remain here: selected roles,
  // one-shot options, dispatch/queued-update mode, revision, etc.
}
```

- Each ChatViewInstance has exactly one fixed InputInstance. Reuse the existing InputSession mechanism as its implementation rather than creating a second input framework; `chat:<viewInstanceId>` is the stable binding.
- `instanceId` is an opaque view id, never `chatId#suffix`.
- InputInstance owns the current send target, selected roles, one-shot options, queued-edit/send intent, **per-branch drafts and per-branch raw mode**.
- Draft storage becomes `(inputInstanceId, branchId)`. Two views of the same branch may hold different drafts; sending clears only the originating InputInstance's draft for that line.
- Raw mode becomes InputInstance state keyed by its current branch. It is no longer a global `chatId + branchId` browser map. The backend meaning of `raw: true` is unchanged.
- `activeTabs` remains per-window, per-instance view state. Do not server-sync it; otherwise two windows would overwrite each other's branch selection.
- The view instance owns **history presentation state**: selected lines, loaded message/block spans, cache cursors, render window, scroll position and live/detached state.
- The **canonical conversation history remains shared backend Chat/Branch data**. Two views of the same branch see the same messages and a send from either appends to that same history; no per-view copy of conversation history is created.
- A pinned Chat keeps one default view+input pair for its Dock entry. Extra views have independent InputInstances and appear separately in the Dock only if explicitly pinned.

**File Manager view**

```ts
interface FileManagerParams { dir: string }
```

- Owns current directory, navigation, upload/download, sizes, refresh and manager pinning.
- A pinned directory reopens the same manager instance rather than launching a fresh copy.

**File Viewer view**

```ts
interface FileViewerParams { path: string }
```

- Owns current file plus preview options; no directory browser or overlay.
- Each manager remembers its paired viewer instance in browser-local storage.
- First file click creates a viewer; later clicks update/focus it. Explicit new-view creation omits the paired id.
- Existing preview types, watch behavior and download continue unchanged.

Singleton examples include Settings, Bus Inspector and Plugin Manager. Chat, Terminal, File Manager and File Viewer are multi-instance.

## 3. Implementation Tasks

### Task 1: Introduce instance adapters and the unified Shell API

**Files:**

- Create: `frontend/src/shell/instances.ts`
- Create: `frontend/src/shell/instances.test.ts`
- Create: `frontend/src/shell/panes.ts`
- Create: `frontend/src/shell/panes.test.ts`
- Modify: `frontend/src/shell/definePlugin.ts`
- Modify: `frontend/src/shell/registries.ts`
- Modify: `frontend/src/shell/loader.ts`
- Modify: `frontend/src/shell/externalLoader.ts`
- Modify: `frontend/src/shell/ctx.ts`
- Modify: `frontend/src/stores/layout.ts`
- Modify: `sdk/ts/src/plugin.ts`

**Steps:**

1. Write failing tests for singleton `main`, multi-instance creation, reopening, params validation, opaque ids and adapter hot-unload cleanup.
2. Add pane tests for open return ids, tiled focus, floating raise, list, close and adjacent move.
3. Define instance adapter/cardinality contracts and one shared registry.
4. Implement `ctx.shell.open` and `ctx.shell.panes` as thin adapters; do not duplicate layout algorithms.
5. Change `layout.openInstance` to return the actual pane id without changing placement behavior.
6. Mirror all structural types in the TS SDK.
7. Run focused Vitest and TypeScript checks.

**Acceptance:** A plugin can create/reopen a parameterized instance without importing another plugin or encoding params into an id.

### Task 2: Migrate Dock and existing opening call sites

**Files:**

- Modify: `frontend/src/shell/Dock.vue`
- Modify: `frontend/src/shell/dockModel.ts`
- Modify: `frontend/src/shell/dockModel.test.ts`
- Modify: `frontend/src/shell/definePlugin.ts`
- Modify: `frontend/src/shell/WorkspaceBar.vue`
- Modify: `frontend/src/plugins/voice-control/controller.ts`
- Modify direct `layout.openInstance` call sites where the Shell facade provides the same operation.
- Modify: `sdk/ts/src/plugin.ts`

**Steps:**

1. Update Dock tests for singleton and multi-instance creation/restoration.
2. Remove `clickCreates` and source-instance creation branches.
3. Route ordinary/pinned entries through `ctx.shell.open`.
4. Keep provider-specific domain deletion separate from pane closing.
5. Verify Settings, voice `open_instance`, chat notifications and terminal opening.

**Acceptance:** All entry points share the same instance-opening path and no files-only Dock branch remains.

### Task 3: Migrate Chat to view + fixed input instances

**Files:**

- Create: `frontend/src/plugins/chat/viewInstances.ts`
- Create: `frontend/src/plugins/chat/viewInstances.test.ts`
- Modify: `frontend/src/plugins/chat/index.ts`
- Modify: `frontend/src/plugins/chat/ChatPane.vue`
- Modify: `frontend/src/plugins/chat/ComposerBox.vue`
- Modify: `frontend/src/plugins/chat/inputDispatch.ts`
- Modify: `frontend/src/plugins/chat/inputDispatch.test.ts`
- Modify: `frontend/src/plugins/chat/chatCache.ts`
- Modify: `frontend/src/plugins/chat/chatCache.test.ts`
- Modify: `frontend/src/plugins/chat/branchTabs.ts`
- Modify: `frontend/src/plugins/chat/branchTabs.test.ts`
- Modify: `frontend/src/plugins/chat/inputSessionState.ts` and `inputSessionState.test.ts`
- Modify: `frontend/src/plugins/chat/voiceDictation.ts` only where view/domain addressing is separated
- Remove after migration: `frontend/src/plugins/chat/rawMode.ts`
- Modify: `frontend/src/stores/inputSessions.ts`, `inputSessionState.ts` and their tests for the fixed opaque InputInstance/target params
- Modify: `internal/plugins/chat/draft.go`
- Modify: `internal/plugins/chat/store.go`
- Modify: `internal/plugins/chat/runtime.go`
- Modify: `internal/plugins/chat/chat_test.go`
- Modify: `internal/plugins/chat/draft_lifecycle_test.go`

**Steps:**

1. Add tests proving two view instances can share one `chatId` while retaining different branch selections, history caches, drafts, raw settings and complete send intents.
2. Create the Chat view-instance registry/adapter with `{chatId, initialTabs?}` params and opaque ids.
3. Promote the existing InputSession to the fixed 1:1 InputInstance for each view. Preserve registered-input-identity, branch/mode, queued-edit id and revision semantics.
4. Move per-branch raw state into that InputInstance and remove the independent global `rawMode.ts` map. Backend raw dispatch/session behavior stays unchanged.
5. Replace `viewKey.split("#")[0]` with structured `chatId` resolution in `ChatPane` and `inputDispatch`; pass `chatId` explicitly to Composer and voice dictation.
6. Change draft rows/RPC/events to carry `input_instance_id`, with uniqueness on `(input_instance_id, branch_id)` and `chat_id` retained for validation and lifecycle queries.
7. Carry the originating `input_instance_id` through human dispatch so backend intake clears only that input's target-line draft. Automatic/loop dispatch retains its existing no-clear behavior.
8. Update merge/archive non-empty-draft guards to inspect every InputInstance on the source line; chat/branch deletion cleans all associated rows, while deleting one view/input cleans only its own drafts.
9. Key branch tabs by window + view instance id. Preserve current per-window isolation and stale-branch pruning.
10. Key message/history-presentation cache by `chatId + viewInstanceId + selectedLines`; canonical backend history remains shared and no message rows gain a view-instance key.
11. Replace Dock `chat:<id>#` prefix matching with structured instance lookup across tiled and floating panes.
12. Implement “open in new panel” by creating another Chat view+input pair with the same `chatId`; remove `chatId#random` generation.
13. Rebuild the transient draft table or migrate each legacy line draft once to the Chat's default InputInstance; never duplicate it into every view.

**Acceptance:** No production frontend code derives `chatId` by splitting `instanceId`; two panes of one Chat have independent inputs and history presentation but still append to the same canonical Chat/Branch history.

### Task 4: Split File Manager and File Viewer using the same model

**Files:**

- Create: `frontend/src/plugins/file-manager/index.ts`
- Create: `frontend/src/plugins/file-manager/FileManagerPane.vue`
- Create: `frontend/src/plugins/file-manager/FileBrowser.vue`
- Create: `frontend/src/plugins/file-manager/instanceStore.ts`
- Create: `frontend/src/plugins/file-manager/viewerTarget.ts`
- Create: `frontend/src/plugins/file-viewer/index.ts`
- Create: `frontend/src/plugins/file-viewer/FileViewerPane.vue`
- Create: `frontend/src/plugins/file-viewer/instanceStore.ts`
- Move existing preview implementations/helpers to `frontend/src/plugins/file-viewer/`.
- Create shared file client/types under `frontend/src/services/`.
- Remove: `frontend/src/plugins/files/` after imports are migrated.

**Steps:**

1. Add instance tests for independent manager/viewer params, state, pinning and pruning.
2. Move generic file RPC/ticket/path helpers to the shared service; neither plugin imports the other's internals.
3. Create the viewer adapter supporting `{path}` create/update and the viewer-only pane.
4. Create the independently openable manager from `FileBrowser.vue` with `{dir}` params.
5. Open files through `ctx.shell.open`; implement paired-viewer reuse and explicit new-view behavior.
6. Include floating panes in open-instance pruning so live floating instances are never deleted.
7. Remove directory state and overlay actions from the viewer and the old `files` pane type.

**Persisted state:** Use independent `instance-store:file-manager:*` and `instance-store:file-viewer:*` records. Do not keep a runtime legacy `files` adapter. If old pinned records must be retained, use a one-time migration utility: records with `file != null` become viewers, folder-only records become managers, then old records are deleted.

**Acceptance:** Manager and viewer open, close, pin and restore independently; clicking a supported file reaches the existing preview pipeline.

### Task 5: Display file sizes and add on-demand directory size

**Files:**

- Create: `frontend/src/services/formatBytes.ts`
- Create: `frontend/src/services/formatBytes.test.ts`
- Modify: `frontend/src/plugins/file-manager/FileBrowser.vue`
- Create: `frontend/src/plugins/file-manager/directorySizes.ts`
- Create: `frontend/src/plugins/file-manager/directorySizes.test.ts`
- Modify: `internal/plugins/fileservice/fileservice.go`
- Modify: `internal/plugins/fileservice/fileservice_test.go`
- Create: `internal/plugins/fileservice/du.go`
- Create: `internal/plugins/fileservice/du_test.go`

**RPC:**

```text
file:_:du {path} ->
  {path, size, files, directories, errors, complete}
```

**Steps:**

1. Test byte formatting for zero, boundaries, fractions and large values.
2. Display ordinary file sizes from existing list metadata.
3. Add per-directory calculate/loading/error/partial states.
4. Implement recursive `WalkDir` sizing without following directory symlinks.
5. Count unreadable entries and return partial results.
6. Make cancellation stop the walk; one request must not block unrelated handlers.
7. Never recursively calculate every directory during ordinary list loading.

**Acceptance:** Directory opening remains a shallow `ReadDir`; directory size is on demand and symlink-safe.

### Task 6: Add streaming upload and manager download

**Files:**

- Modify: `internal/plugins/fileservice/fileservice.go`
- Modify: `internal/plugins/fileservice/ticket.go`
- Modify: `internal/plugins/fileservice/ticket_test.go`
- Create: `internal/plugins/fileservice/uploadticket.go`
- Create: `internal/plugins/fileservice/uploadticket_test.go`
- Modify: `internal/plugins/gateway/gateway.go`
- Create: `internal/plugins/gateway/fileupload.go`
- Create: `internal/plugins/gateway/fileupload_test.go`
- Create: `frontend/src/services/uploadFile.ts`
- Create: `frontend/src/services/uploadFile.test.ts`
- Modify: `frontend/src/plugins/file-manager/FileManagerPane.vue`
- Modify: `frontend/src/plugins/file-manager/FileBrowser.vue`

**Contract:**

1. Frontend requests `file:_:upload:ticket {dir, name, overwrite}`.
2. file-service validates a basename-only name and returns a short-lived upload ticket bound to the exact target and overwrite policy. Read/upload tickets are not interchangeable.
3. Browser sends the raw `File` body to `POST /api/files/upload?ticket=...`; no base64 and no bus frame.
4. Gateway streams to a temporary file in the target directory and atomically renames on success. Failure removes the temporary file.
5. Existing targets return HTTP 409 unless overwrite was explicitly authorized. UI asks before requesting an overwrite ticket.

**Steps:**

1. Write ticket separation, invalid-name, collision and overwrite tests.
2. Write gateway tests for success, invalid ticket, collision, interrupted-body cleanup and large streams.
3. Implement the backend data plane.
4. Add multi-file selection and per-file status; refresh affected entries after completion.
5. Reuse the existing raw-file ticket endpoint for manager download.
6. Add an end-to-end smoke case that uploads, lists, downloads and compares content.

**Acceptance:** Files larger than the bus cap upload/download correctly, collisions never silently overwrite and failures leave no partial target.

### Task 7: Documentation and full verification

**Files:**

- Modify: `docs/plugin-framework.md` (new version header and matching revision entry)
- Modify: `docs/plugin-development.md`
- Modify: `docs/plugin-protocol.md` only where file RPC/HTTP contracts belong
- Modify: `architecture.md`
- Update relevant viewer-app skill references after implementation changes established workflows

**Document:**

- Plugin definition / view instance / domain object / pane distinctions.
- Singleton versus multi cardinality and serializable open params.
- `ctx.shell.open` and `ctx.shell.panes`, including local-only scope and hot-unload behavior.
- Chat ViewInstance ↔ fixed InputInstance binding; per-input/per-branch drafts and raw mode; canonical Chat/Branch history versus per-view history presentation; no encoded `chatId#suffix`.
- Independent File Manager/File Viewer ownership and Dock behavior.
- Upload ticket/data-plane and on-demand `file:_:du` contracts.

**Run:**

```bash
cd frontend
npm test -- --run
npx vue-tsc --noEmit
npm run build

cd ..
gofmt -l .
go build ./...
go test ./...

cd sdk/ts
npx tsc --noEmit

cd ../..
bash scripts/smoke_all.sh
git diff --check
```

Do not restart the running Viewer unless the user explicitly requests it.

## 4. Manual Acceptance Matrix

1. Open two Chat view instances with the same `chatId` and different branches; messages, tab state, history presentation and fixed InputInstances remain independent.
2. Open the same branch in both views and type different drafts and raw settings; each InputInstance retains its own values. Send from one view: only its draft clears, while both views converge on the same newly appended canonical message history.
3. Close/reopen a pinned Chat's default view+input pair; its own drafts restore, and no encoded id or duplicate Dock entry appears.
4. Open two managers at different directories; navigation and pins remain independent.
5. Close/reopen a pinned manager; the same instance returns rather than spawning a launcher copy.
6. Click two files in one manager; the paired viewer updates/focuses. Explicit new-view creation opens a second viewer.
7. Float a Chat/manager/viewer; Shell list, focus and close still work and live floating instances are not pruned.
8. Close an unpinned instance; it is pruned. Close a pinned instance; its Dock entry remains.
9. Verify every existing preview type still renders, refreshes, watches changes and downloads.
10. Upload multiple files, including one larger than 1 MiB; collision and overwrite confirmation behave correctly.
11. Calculate directory size on a tree containing a symlink and an unreadable entry; no symlink recursion and partial errors are visible.
12. Hot-unload an external plugin with an instance adapter; open fails cleanly and no registry entry remains.
13. Confirm no Shell instance/pane operation is published to the backend bus or affects a second browser.

## 5. Risks and Tradeoffs

- **Identity confusion:** `chatId`, view `instanceId` and `paneId` must remain separate. No production code should reconstruct one by parsing another.
- **Shared versus instance state:** Drafts/raw/send intent belong to the fixed InputInstance; branch tabs/cache/scroll belong to the ViewInstance; canonical messages/branches remain Chat/Branch domain data. Never duplicate canonical history per view.
- **Input binding:** Reuse and strengthen the existing InputSession rather than adding a parallel input store. Every asynchronous send must retain the originating input identity as well as branch and revision.
- **Cross-window persistence:** Server-synced InputInstance drafts may be shared by reopening the same view, but active branch selection stays window-local to avoid last-writer-wins behavior.
- **Pane close versus domain lifetime:** Shell manipulates containers; plugins continue to own pin/prune/process semantics.
- **Viewer explosion:** Paired-viewer reuse is the default; unconditional create-on-click would make normal browsing unusable.
- **Migration size:** Chat migration and file splitting are separate acceptance stages. Do not combine them into one untestable edit.
- **Directory-size cost:** Recursive sizing is deliberately on demand; caching/indexing is deferred.
- **Upload integrity:** Atomic rename and explicit overwrite authorization are required.
- **Scope control:** Parameterized instance opening is included because Chat and files need it now. Arbitrary commands, layout editing and generic workflow automation remain deferred.
