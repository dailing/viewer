# Goal loop plugin

Implemented contract, framework v0.68. `viewer.loop` is an in-process Go plugin
registered after chat. It uses bus RPCs only; it does not import chat internals,
read chat's database, start agents or render messages itself.

## Ownership and storage

- `<data-dir>/loop.sqlite3`: `loops` and `iterations`. Loop owns goal, criteria,
  fixed chat/role/branch, deadlines, counters, scheduling intent, verdicts and
  references to chat dispatches/turns. Each iteration persists its immutable
  prompt before delivery. Bounded final output and checkpoint snapshots support
  repeatable judging; full transcripts and blocks remain in chat.
- `<data-dir>/chat.sqlite3`: `automation_leases`, `automation_gates`, and
  `dispatch_receipts`. These are generic chat scheduling primitives.
- `<effective agent cwd>/.loop/<id>/progress.md`: agent-maintained current
  checkpoint, at most 8 KiB, including `iteration: N`, evidence, artifacts,
  blockers and next step. `history.md` is the short agent-maintained work log.
  Effective cwd uses the same chat-root/role-cwd resolution as chat. Loop checks
  this path again before each new dispatch and pauses if it changes.
- Files are working memory, not authoritative scheduling state or proof that
  verification passed. Missing, oversized and stale checkpoints appear in the
  iteration UI and the next prompt. Files are created/updated by the agent.

A branch is a conversation partition, not filesystem isolation. A newly created
branch inherits the chosen fork's lineage. The plugin reuses the role's branch
session; it does not reset context or create worktrees.

## Bus API

All times in replies are Unix milliseconds; duration inputs are seconds.

| RPC | Input / behavior |
| --- | --- |
| `loop:_:create` | `id?` (client deduplication), `chat_id`, explicit `role_id`, `goal`, `criteria`, `new_branch?` (default true), `branch_id?`, `from_turn_id?`, `max_iterations?`, `duration_seconds?`, `turn_timeout_seconds?`, `min_interval_seconds?`, `judge_every?`. Creates a draft, provisions/claims its branch, returns the run. Retry with the same id and task after a lost reply. |
| `loop:_:list` | `chat_id?`, `before_created_at?`; pages of 20 runs, with large detail fields omitted. |
| `loop:_:get` | `id`, `before_iteration?`; full run and pages of 20 iteration summaries with `has_more`. Full execution history is in chat. |
| `loop:_:start` | `id`; starts a draft. |
| `loop:_:pause` | `id`; closes the automatic dispatch gate, lets the current turn end. |
| `loop:_:resume` | `id`, `extend_seconds?` (up to 86400); resumes a paused run, takes latest human feedback, clears retry/no-progress counters, retries a failed judge without rerunning the agent. |
| `loop:_:stop` | `id`; retracts this loop's queued dispatch and cancels only its bound turn; waits for confirmation. |
| `loop:_:recover` | `id`, `confirmed_stopped: true`; operator confirms an old-generation unknown task has stopped, marks it interrupted. This is not itself a process cancellation. |
| `loop:_:changed` event | `id`, `chat_id`, `revision`; clients re-fetch a bounded snapshot. A 5s frontend poll repairs missed events/reconnects. |

Defaults: 20 iterations, 3600s total, 900s per dispatch (queueing included), judge
progress every 3 iterations, no minimum trigger interval. The next iteration
dispatches no sooner than `min_interval_seconds` after the previous iteration's
dispatch (0 = immediately after the verdict); the wait never extends the
deadline. Limits: 1–1000 iterations, 1–86400s time budgets, 0–86400s trigger
interval, 1–100 judge interval, 8 KiB each goal/criteria. Deadlines include paused and
disconnected time. Completion claims always invoke the judge. Three consecutive
execution failures or three judge checkpoints without evidenced progress pause
the run; three judge failures pause without executing another agent turn.

One branch can be claimed by only one unfinished run, including a draft or
paused run. Terminal states retain branch/history and release the gate. Stop a
draft to free its branch. A chat with a claimed automation cannot be deleted until the loop is stopped. Roles are never selected implicitly.

## Chat's generic automation contract

`chat:_:automation` takes `op`, `owner`, `fence`, and where applicable
`chat_id`, `branch_id`, `automation_id`, `revision`, `role_id`:

- `lease`: acquire/renew the single scheduler lease (30s). A takeover or expired
  reacquisition increments its monotonic fence. Loop renews every 5s.
- `release-lease`: expires a matching lease at shutdown.
- `describe`: validates a chat member role and active branch, returns effective
  `root` and `role_name`; avoids fetching chat's entire turn/session history.
- `claim`: claims a branch idempotently, initially paused.
- `get`: returns gate owner, paused flag, revision and latest human feedback.
- `resume`: compare-and-set the gate revision, opening it only if no newer human
  input or control operation intervened.
- `pause` / `release`: closes the gate; release also frees its owner.

`chat:_:dispatch` additionally accepts `automation_id`, `idempotency_key`,
`owner`, `fence`. Automatic dispatch requires exactly one explicit role and
forbids parallel/lane continuation. Admission validates the lease and gate and
persists a receipt under the SQLite write lock. Retries with the same key and
payload return the original dispatch; reuse with changed content fails. A chat
mutex also serializes admission with ordinary user sends and merge-confirm.
Human sends close the branch gate, record feedback, and retract an automatic
message still in the queue. Editing an automatic queued message also pauses continuation and records the edit as human feedback. Manually cancelling that queue entry pauses the loop. An already-started turn may finish.

`chat:_:dispatch-status {idempotency_key}` returns
`missing | pending | running | completed | cancelled | failed | unknown`, plus
available dispatch/turn ids, timestamps, final stop reason and bounded output /
recent tool text. It reads durable turn records, not asynchronous summaries.
`op: cancel` additionally requires scheduler `owner/fence`; cancellation is
scoped to that receipt. `op: resolve` with the same credentials and
`confirmed_stopped: true` can mark only an old-generation receipt interrupted.

`chat:_:branches:create` also accepts `idempotency_key`, deriving a stable branch
id scoped to the chat so provisioning retries do not create duplicate tabs.

## State and recovery

Loop states: `draft`, `running`, `paused`, `stopping`, `limiting`, `completed`,
`stopped`, `limited`. Iteration states: `dispatching`, `waiting`, `ready`,
`judging`, `done`. Agent candidate failover stays inside one logical iteration.

The scheduler checks durable dispatch status on each tick; turn events only
wake it sooner. It cannot advance twice on duplicate events or redeliver a
request merely because the RPC reply was lost. A persisted in-flight receipt
from another chat generation is `unknown` until a terminal turn record exists
or the operator explicitly resolves it. Loop pauses rather than replaying
possible side effects. Cancellation remains `stopping`/`limiting` until chat
confirms termination. Automatic deadline cancellation is bounded to this run.

The current backend's normal restart starts the replacement with `--wait-pid`
and waits for the old process to exit. The lease/fence additionally handles a
reconnected scheduler or overlapping owners. This protocol deduplicates logical
scheduling; it does not promise exactly-once external effects or rollback.

Agent final output ends in `<loop-result>{...}</loop-result>` with status
`continue|complete|blocked`, summary, evidence, next_step and blockers. Only a
trailing result segment in assistant output is parsed; tool text is never a
completion marker. Invalid/missing results invoke the judge. The judge uses
`llm:_:complete` JSON mode and bounded evidence and must return status, reason,
feedback and an explicit progress boolean. Human input/pause during judging is
rechecked before applying a verdict; a stale completion cannot bypass the gate.

## Frontend and validation

`frontend/src/plugins/loop/LoopStatus.vue` is hosted by ChatPane. It provides
create-and-start, explicit role selection, branch selection, limits, run history,
iteration results, checkpoint diagnostics and pause/resume/stop/recovery actions.
Execution links select the original branch and load a bounded timeline window
around an older turn. Existing message/tool rendering is reused.

Unit tests cover deduplication, payload conflicts, fencing, human gates,
bounded durable recovery, lost replies, cancellation confirmation, deadlines,
completion judging and retries. `scripts/smoke_loop.py` runs actual loop/chat
plugins over the kernel with a deterministic ACP agent and LLM judge; it is part
of `scripts/smoke_all.sh`. No running production service is restarted by tests.
