# Agent Instructions

Before making code changes in this repository, read `architecture.md`.

Use `architecture.md` as the project map for finding backend APIs, frontend stores, viewer components, terminal handling, live update flow, runtime configuration, and likely fault locations. Keep it updated when files are added, removed, renamed, or when responsibilities move between modules.

The implementation is Go-first: the kernel and all core plugins live under `cmd/` and `internal/` (Go), the frontend under `frontend/` (Vue/TS). Language SDKs live under `sdk/` (`sdk/go`, `sdk/python`, `sdk/ts`). Python exists only as development tooling under `scripts/` (smoke tests, mocks, migrations); there is no Python product code.

Do not start the frontend dev server for verification; the user will test the server manually. Standard implementation checks:

- Frontend: `cd frontend && npx vue-tsc --noEmit && npm run build`
- Backend: `gofmt -l . && go build ./... && go test ./...`
- Black-box: `bash scripts/smoke_all.sh` (requires `.venv` python for the Python smoke clients)

Do not run `systemctl` against `viewer.service`, and do not stop or restart Viewer directly, unless the user explicitly requests that exact operational action. When a user explicitly requests a normal development restart, call `POST /api/admin/restart`; the supervisor replaces the backend generation while old workers drain running turns. `systemctl --user restart viewer.service` is reserved for an explicitly requested hard restart of the complete cgroup.

Hermes may modify code directly when the user explicitly instructs "改下agents.md 可以你直接修改执行" or equivalent. In such cases, Hermes should implement the agreed plan, run the standard checks above, and report results. The user retains final review and testing responsibility.
