# Agent Task DAG

Agent Task DAG is a persistent task graph runtime for coordinating Codex/Hermes work from Viewer. It is separate from Markdown Loop Tasks. Loop Tasks are repeated prompts; Agent Tasks are mutable, dependency-aware work items that agents can create, patch, run, and complete.

## Storage

State lives in `~/.view/agent-tasks.sqlite3`.

Long-running task logs should be written under:

```text
~/.view/logs/agent-tasks/{task_id}/
```

## State Model

Tasks use these statuses:

```text
draft
backlog
ready
claimed
running
waiting_process
review
done
failed
blocked
cancelled
```

The scheduler promotes `backlog` or dependency-waiting `blocked` tasks to `ready` when every task in `depends_on` is `done`. If a dependency fails or is cancelled, the task becomes `blocked` with `blocked_reason=failed_dependency`.

Plan-mutability is intentionally limited. Tasks in `draft`, `backlog`, `ready`, and `blocked` can be patched. Tasks in `running` and `waiting_process` are execution records and should not have their plan changed. For completed tasks, create follow-up tasks instead of rewriting history.

## Core Fields

Important task fields:

- `group_id`: project isolation key, for example `pami_pp`.
- `parent_id` / `root_id`: task tree display lineage.
- `depends_on`: true scheduling dependencies; this can form a DAG.
- `workspace`: agent working directory, using the same path rules as Viewer agent sessions.
- `assigned_agent`: currently `codex` or `hermes`.
- `agent_session_id`: Viewer agent session created for a running task.
- `runtime.pid`: external long-running process tracked by the backend monitor.
- `artifacts`: logs, result directories, figures, reports, and expected outputs.
- `result.summary`: concise structured result for later agents and the UI.

## API

List tasks:

```http
GET /api/agent-tasks?group_id=pami_pp
```

Create task:

```http
POST /api/agent-tasks
Content-Type: application/json

{
  "group_id": "pami_pp",
  "title": "Run corrected tuning",
  "description": "Run the corrected alpha grid and save outputs.",
  "status": "backlog",
  "priority": 60,
  "workspace": "/path/to/project",
  "assigned_agent": "codex",
  "depends_on": [],
  "execution": {
    "mode": "agent",
    "instruction": "Run the corrected alpha grid and write a short summary.",
    "cwd": "",
    "env": {}
  }
}
```

Read context for an agent:

```http
GET /api/agent-tasks/{task_id}/context
```

Patch a not-yet-running task:

```http
PATCH /api/agent-tasks/{task_id}
Content-Type: application/json

{
  "expected_version": 3,
  "priority": 80,
  "reason": "The report became more urgent after the tuning result."
}
```

Patch dependencies:

```http
POST /api/agent-tasks/{task_id}/dependencies
Content-Type: application/json

{
  "add": ["task_new_experiment"],
  "remove": [],
  "expected_version": 4,
  "reason": "The final report must wait for the corrected tuning run."
}
```

Dispatch a task to an agent:

```http
POST /api/agent-tasks/{task_id}/dispatch
Content-Type: application/json

{ "force": false }
```

Record a long-running process:

```http
POST /api/agent-tasks/{task_id}/process
Content-Type: application/json

{
  "pid": 12345,
  "process_group_id": 12345,
  "log_path": "~/.view/logs/agent-tasks/task_x/attempt_1/run.log",
  "expected_outputs": ["results/experiment_x/summary.json"],
  "reason": "Started tuning as a detached process."
}
```

Complete a task:

```http
POST /api/agent-tasks/{task_id}/complete
Content-Type: application/json

{
  "status": "done",
  "result": {
    "summary": "Corrected tuning completed and selected alpha=0.03.",
    "decision": "Use corrected tuning for final report.",
    "metrics": {},
    "failure_reason": null,
    "next_suggestions": [],
    "user_decision_needed": null
  },
  "artifacts": [
    { "type": "log", "path": "results/tuning/run.log" },
    { "type": "result_dir", "path": "results/tuning/corrected_alpha" }
  ],
  "reason": "Outputs checked."
}
```

## Manual vs Auto Mode

The Task DAG page exposes a per-group mode:

- `manual`: ready tasks do not start automatically. Use the UI Start button or `POST /api/agent-tasks/{id}/dispatch`.
- `auto`: the backend scheduler periodically dispatches ready tasks unless the task policy requires approval or disables auto dispatch.

Settings API:

```http
GET /api/agent-tasks/settings?group_id=pami_pp
PUT /api/agent-tasks/settings
```

```json
{
  "default_group_id": "pami_pp",
  "mode": "manual",
  "default_agent": "codex",
  "default_model": "gpt-5.5",
  "auto_tick_seconds": 10
}
```

## Long-Running Process Flow

1. Agent starts a task and creates or reuses an output directory.
2. Agent launches the expensive experiment as a background process.
3. Agent calls `/api/agent-tasks/{task_id}/process` with `pid`, log path, and expected outputs.
4. Task becomes `waiting_process`.
5. Backend monitor detects when the pid exits.
6. Backend resumes the attached agent session.
7. Agent inspects logs and artifacts.
8. Agent calls `/complete` with `done`, `failed`, `review`, or `blocked`.

## Frontend

The top bar has a Task DAG page next to Loop Tasks. It provides:

- group selection
- manual/auto mode toggle
- "Run Ready" for approved one-at-a-time debugging
- status columns
- simple task tree
- selected task editor
- dependency add/remove controls
- runtime pid/session visibility
- artifacts and result summary display
