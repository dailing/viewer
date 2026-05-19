# Agent Task DAG Demo

This demo is a small workflow for testing the Viewer Agent Task DAG manager/executor design.

It exercises:

- group-level goal/plan/context/constraints
- manager prompt entry
- executor task-local workspace writes
- executor long-running process registration
- executor-to-manager request when shared code needs a change
- final report dependency behavior

## Layout

```text
examples/agent_task_demo/
  README.md
  seed_demo.py
  shared/simulate_metric.py
  expected_outputs/
  results/
```

`shared/simulate_metric.py` is shared/common code. Executors may read and run it, but should not edit it unless the task explicitly grants permission. Executors should create task-local scripts under:

```text
~/.view/logs/agent-tasks/{task_id}/workspace/
```

## Seed The Demo

Start Viewer normally, then run:

```bash
python examples/agent_task_demo/seed_demo.py --base-url http://127.0.0.1:8000 --user dailing
```

This creates group `agent_task_demo` with a global plan and four tasks:

1. `Demo baseline metric run`
2. `Demo variant run that should request manager`
3. `Demo manager-owned shared-code fix`
4. `Demo final report`

The final report depends on the baseline and fix tasks. The variant task is designed to fail unless the manager chooses to patch `shared/simulate_metric.py`.

## Suggested Test

1. Open the Task DAG page.
2. Load group `agent_task_demo`.
3. Confirm Manual mode.
4. Click `Run Ready` for one task at a time.
5. For executor tasks, check whether task-local workspace paths appear in Runtime.
6. When the variant task fails, the executor should ask the manager instead of editing shared code itself.
7. Use the Manager prompt box to ask:

```text
Inspect the demo DAG and resolve the failed variant run safely. Decide whether shared code should be patched, create or adjust tasks, and keep the final report dependencies correct.
```

## Expected Behavior

- Executors can write scripts and outputs inside their task-local workspace.
- Executors should not edit `shared/simulate_metric.py`.
- Manager can edit `shared/simulate_metric.py` or create a manager-owned patch task.
- Manager can add retry/follow-up tasks and patch dependencies.
- Final report should not run until dependencies are done.
