# Decision Center Plugin Implementation Plan

> **For Hermes:** Implement directly, task-by-task; do not delegate code.

**Goal:** Extract the global Decision Center into an independently registered frontend plugin, with one pending-first list and expandable source-Turn context for each Decision.

**Architecture:** `chat` remains the sole owner of Decision, Task, Turn and Message persistence, invariants and mutations. The `decision-center` frontend plugin owns the global pane and Dock action. It uses Chat RPC for authoritative snapshots, source context and mutations, with Decision events as refresh triggers. No direct SQLite access, backend Decision Center plugin, Turn-completion feed or duplicate read model is introduced.

**Tech Stack:** Vue 3, TypeScript, Viewer frontend plugin contracts, internal message bus, Go, GORM, Vitest, Go tests and Python black-box smoke tests.

---

## Final scope

1. One list, not tabs: pending/dispatching Decisions first, history after them. Each section has independent cursor pagination.
2. No Turn-completion list. Turn ended is not treated as Task completed.
3. Status colors: pending/dispatching, resolved, cancelled and invalid are visually distinct; cancelled history is subdued.
4. Every Decision has a collapsed “查看这一轮的消息” control. Expanding it loads the triggering user message and visible assistant messages from the source Turn.
5. Source context excludes tool calls, tool results, commands, file changes, reasoning, errors and raw events. It does not create a second Markdown renderer.
6. Chat owns a `chat:_:decisions:context` RPC. The new frontend plugin never opens Chat SQLite.
7. Initial mount/window focus uses RPC; Decision events update visible state and schedule debounced authoritative reconciliation. Mutations continue through existing Decision RPCs.

## Task 1: Add source-Turn visible-message RPC

**Files:**
- Modify: `internal/plugins/chat/decision.go`
- Modify: `internal/plugins/chat/chat.go`
- Test: `internal/plugins/chat/decision_context_test.go`
- Test: `scripts/smoke_decision.py`

**Implementation:**
- Look up the Decision and its source Turn.
- Query visible user/assistant `messages` whose `turn_id` is either the agent Turn ID or its dispatch ID, ordered by `(created_at, id)`.
- Return `{decision_id, source_turn_id, messages}`.
- Do not query `message_blocks` or `turn_events`.
- Cover the triggering user message, source assistant message, another Turn exclusion and tool/reasoning block exclusion.

## Task 2: Share Chat Turn rendering with an activity switch

**Files:**
- Create: `frontend/src/plugins/chat/TurnContent.vue`
- Create: `frontend/src/plugins/chat/turnContent.ts`
- Test: `frontend/src/plugins/chat/turnContent.test.ts`
- Modify: `frontend/src/plugins/chat/TaskPane.vue`

**Implementation:**
- Move message Markdown, activity grouping and ToolActivity rendering into `TurnContent`.
- `showActivities=true` preserves the Task timeline; `false` keeps only visible messages.
- Share valid-Decision marker stripping and preserve invalid marker source.
- Optional message labels identify User versus the source Role in Decision context.
- TaskPane uses the shared component instead of maintaining a duplicate rendering path.

## Task 3: Extract the independent single-list Decision Center

**Files:**
- Create: `frontend/src/plugins/decision-center/index.ts`
- Create: `frontend/src/plugins/decision-center/DecisionCenterPane.vue`
- Create: `frontend/src/plugins/decision-center/DecisionEntry.vue`
- Modify: `frontend/src/plugins/chat/index.ts`
- Modify: `frontend/src/plugins/chat/DecisionCard.vue`
- Delete: `frontend/src/plugins/chat/DecisionCenter.vue`

**Implementation:**
- Register pane type `decision-center`, singleton `main` and the Dock badge in the new plugin; remove `chat-decisions` from Chat.
- Load pending and history independently and render pending first. Use request generations to reject stale responses.
- Move a Decision between pending/history immediately on events, then reconcile through RPC.
- `DecisionEntry` lazily loads source context on first expansion and renders it through `TurnContent` with activities disabled.
- Preserve respond, edited response, free response, cancellation, End Task and exact source navigation.
- Inline Task Decisions continue to work without the global pane.

## Task 4: Documentation and verification

**Files:**
- Modify: `architecture.md`
- Modify: `docs/plugin-framework.md`

**Verification:**
- `export PATH="$HOME/.local/go/bin:$PATH"; gofmt -l . && go build ./... && go test ./...`
- `cd frontend && npx vue-tsc --noEmit && npm run build && npx vitest run`
- Build an isolated `viewerd` and run `scripts/smoke_decision.py`, then the complete `scripts/smoke_all.sh`.
- `git diff --check`
- Do not commit or restart Viewer unless the user explicitly requests it.

## Explicitly deferred

- Turn-completion feeds, Completion tables and cross-device unread/read models.
- Backend `decision-center` plugin or direct SQLite access from another plugin.
- Generic automation, Agent-to-Agent calls, A2A and arbitrary Decision actions.
