# Agent Instructions

Before making code changes in this repository, read `architecture.md`.

Use `architecture.md` as the project map for finding backend APIs, frontend stores, viewer components, terminal handling, live update flow, runtime configuration, and likely fault locations. Keep it updated when files are added, removed, renamed, or when responsibilities move between modules.

Do not start the frontend dev server for verification; the user will test the server manually. Use `npm run build` and `python -m compileall backend/app` as the standard implementation checks.
