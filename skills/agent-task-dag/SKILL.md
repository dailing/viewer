# Agent Task DAG Skill

Use this skill when operating Viewer Agent Task DAG tasks through the local HTTP API.

## Purpose

Agent Task DAG is a persistent task graph for multi-step work. A task is the unit of work. Tasks can depend on other tasks, produce artifacts, and be completed by an agent. Use the API rather than ad hoc notes so future agents can continue the work.

There are two roles:

- Manager/planner: owns the global plan and DAG, creates/reschedules tasks, patches dependencies, and changes code when needed.
- Executor: runs one assigned task, writes task-local scripts/files/outputs, records PID/artifacts/results, and asks the manager when the shared plan, DAG, or common code needs to change.

## Base API

The Viewer server exposes:

```text
/api/agent-tasks
```

If calling from a terminal, include the active user query parameter when needed:

```text
?user=<user_id>
```

## Standard Workflow

1. List tasks for the target group:

```bash
curl 'http://127.0.0.1:8000/api/agent-tasks?group_id=pami_pp'
```

2. Read context before working:

```bash
curl 'http://127.0.0.1:8000/api/agent-tasks/<task_id>/context'
```

3. If you are the manager and the plan is missing work, create a new task and patch dependencies of not-yet-running downstream tasks.

If you are an executor, use the task-local workspace for custom scripts and outputs:

```text
~/.view/logs/agent-tasks/<task_id>/workspace/
```

You may read and call shared project code. Do not modify shared project/common code unless the task explicitly grants that permission. If common code or DAG changes are needed, ask the manager:

```bash
curl -X POST 'http://127.0.0.1:8000/api/agent-tasks/<task_id>/manager-request' \
  -H 'Content-Type: application/json' \
  -d '{
    "prompt": "This task found a missing dependency. Please replan.",
    "reason": "Executor requested manager review.",
    "trigger": "executor_request"
  }'
```

4. If starting a long-running process, write logs under:

```text
~/.view/logs/agent-tasks/<task_id>/attempt_<n>/
```

Then call:

```http
POST /api/agent-tasks/<task_id>/process
```

with `pid`, optional `process_group_id`, `log_path`, and `expected_outputs`.

5. When finished, call:

```http
POST /api/agent-tasks/<task_id>/complete
```

with status `done`, `failed`, `review`, or `blocked`.

## Rules

- Do not mark a task `done` until outputs/logs are checked.
- Executors can create/modify files in the task-local workspace.
- Executors do not edit shared project/common source code or mutate the DAG unless the task explicitly says so.
- Managers do not patch `running` or `waiting_process` task plans. Cancel or fail and create a replacement task instead.
- For completed tasks, create follow-up tasks instead of rewriting history.
- If a downstream task has not run and now needs more input, add a dependency to it.
- Include `expected_version` when patching a task or dependencies if you fetched the task first.
- Keep `result.summary` concise and actionable.
- Record artifacts with stable paths.

## Manager Plan API

```bash
curl -X PUT 'http://127.0.0.1:8000/api/agent-tasks/plan' \
  -H 'Content-Type: application/json' \
  -d '{
    "group_id": "pami_pp",
    "goal": "Produce reliable paper experiment outputs.",
    "plan": "Run experiments, validate logs, then generate reports.",
    "context": "Use existing splits and preserve previous results.",
    "constraints": [
      "Executors do not edit code unless explicitly instructed.",
      "Expensive new runs start in backlog."
    ],
    "reason": "Initialize plan."
  }'
```

## Dependency Patch Example

```bash
curl -X POST 'http://127.0.0.1:8000/api/agent-tasks/task_report/dependencies' \
  -H 'Content-Type: application/json' \
  -d '{
    "add": ["task_corrected_tuning"],
    "expected_version": 4,
    "reason": "Final report must wait for the corrected tuning experiment."
  }'
```

## Completion Example

```bash
curl -X POST 'http://127.0.0.1:8000/api/agent-tasks/task_corrected_tuning/complete' \
  -H 'Content-Type: application/json' \
  -d '{
    "status": "done",
    "result": {
      "summary": "Corrected tuning finished; alpha=0.03 was selected.",
      "decision": "Use corrected tuning for final report.",
      "metrics": {},
      "failure_reason": null,
      "next_suggestions": [],
      "user_decision_needed": null
    },
    "artifacts": [
      {"type": "log", "path": "results/tuning/run.log"},
      {"type": "result_dir", "path": "results/tuning/corrected_alpha"}
    ],
    "reason": "Outputs checked."
  }'
```
